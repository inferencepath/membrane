package streamhelper_test

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/inferencepath/membrane/internal/exterior/providers"
	"github.com/inferencepath/membrane/internal/exterior/providers/streamhelper"
)

func collect(body string) ([]string, error) {
	var got []string
	err := streamhelper.PumpSSE(context.Background(), func() {}, strings.NewReader(body), time.Second,
		func(data string) (bool, error) {
			if data == "[DONE]" {
				return true, nil
			}
			got = append(got, data)
			return false, nil
		})
	return got, err
}

func TestPumpSSEDataAndStop(t *testing.T) {
	got, err := collect("data: a\n\ndata: b\n\ndata: [DONE]\n\n")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(got, ",") != "a,b" {
		t.Fatalf("got %v", got)
	}
}

func TestPumpSSESkipsNonDataLines(t *testing.T) {
	got, err := collect("event: foo\n: comment\n\ndata: x\n\n")
	if err != nil || len(got) != 1 || got[0] != "x" {
		t.Fatalf("got=%v err=%v", got, err)
	}
}

func TestPumpSSECleanEOFWithoutSentinel(t *testing.T) {
	got, err := collect("data: only\n\n")
	if err != nil || len(got) != 1 {
		t.Fatalf("got=%v err=%v", got, err)
	}
}

func TestPumpSSEOnDataError(t *testing.T) {
	boom := errors.New("boom")
	err := streamhelper.PumpSSE(context.Background(), func() {}, strings.NewReader("data: x\n\n"), time.Second,
		func(string) (bool, error) { return false, boom })
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v", err)
	}
}

// blockingReader emits data once, then blocks until ctx is cancelled.
type blockingReader struct {
	ctx  context.Context
	data string
	sent bool
}

func (r *blockingReader) Read(p []byte) (int, error) {
	if !r.sent {
		r.sent = true
		return copy(p, r.data), nil
	}
	<-r.ctx.Done()
	return 0, io.ErrClosedPipe
}

func TestPumpSSEIdleTimeout(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r := &blockingReader{ctx: ctx, data: "data: partial-without-newline"}
	err := streamhelper.PumpSSE(ctx, cancel, r, 10*time.Millisecond, func(string) (bool, error) { return false, nil })
	if !errors.Is(err, providers.ErrUpstreamTimeout) {
		t.Fatalf("err = %v, want ErrUpstreamTimeout", err)
	}
}

func TestPumpSSEContextCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	r := &blockingReader{ctx: ctx, data: "data: partial"}
	// Cancel shortly; with a long idle, the ctx error path (not idle) is taken.
	go func() { time.Sleep(10 * time.Millisecond); cancel() }()
	err := streamhelper.PumpSSE(ctx, cancel, r, time.Hour, func(string) (bool, error) { return false, nil })
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
}
