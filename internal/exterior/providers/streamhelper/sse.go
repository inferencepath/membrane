// Package streamhelper provides a reusable Server-Sent Events (SSE) pump with an
// idle timeout, shared by the OpenAI-compatible, Anthropic, and Azure streaming
// adapters. It handles only transport framing (line reading, the idle watchdog,
// and context cancellation); each adapter supplies a family-specific decoder.
//
// It is Exterior and must not import Interior packages.
package streamhelper

import (
	"bufio"
	"context"
	"errors"
	"io"
	"strings"
	"time"

	"github.com/inferencepath/membrane/internal/exterior/providers"
)

// PumpSSE reads an SSE body line by line, invoking onData for each `data:`
// payload (whitespace-trimmed). onData returns stop=true to end the stream
// early (e.g. on a family-specific terminal sentinel). PumpSSE enforces an idle
// timeout: if no line arrives within idle, it cancels the request and returns
// providers.ErrUpstreamTimeout. On clean end of body it returns nil; the caller
// is then responsible for emitting the terminal canonical chunk.
//
// cancel must cancel the context used to issue the HTTP request, so the idle
// watchdog can abort a stalled upstream.
func PumpSSE(ctx context.Context, cancel context.CancelFunc, body io.Reader, idle time.Duration, onData func(data string) (stop bool, err error)) error {
	if idle <= 0 {
		idle = 30 * time.Second
	}
	idleFired := false
	timer := time.AfterFunc(idle, func() {
		idleFired = true
		cancel()
	})
	defer timer.Stop()

	reader := bufio.NewReader(body)
	for {
		line, err := reader.ReadString('\n')
		atEOF := errors.Is(err, io.EOF)
		if err != nil {
			if atEOF && strings.TrimSpace(line) == "" {
				return nil
			}
			if idleFired {
				return providers.ErrUpstreamTimeout
			}
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if !atEOF {
				return err
			}
			// EOF with a trailing partial line: fall through to process it.
		}
		timer.Reset(idle)

		data, ok := parseSSELine(line)
		if !ok {
			if atEOF {
				return nil
			}
			continue
		}
		stop, derr := onData(data)
		if derr != nil {
			return derr
		}
		if stop || atEOF {
			return nil
		}
	}
}

// parseSSELine extracts the payload of a `data:` SSE line, returning ok=false for
// non-data lines (comments, event:, blank).
func parseSSELine(line string) (string, bool) {
	line = strings.TrimRight(line, "\r\n")
	rest, ok := strings.CutPrefix(line, "data:")
	if !ok {
		return "", false
	}
	return strings.TrimSpace(rest), true
}
