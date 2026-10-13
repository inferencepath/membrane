// Package logging provides structured logging (slog) with secret redaction. Logs
// must never contain secrets or caller identity on the egress side.
package logging

import (
	"io"
	"log/slog"
	"os"
	"strings"
)

// sensitiveKeys are attribute keys whose values are redacted in logs.
var sensitiveKeys = map[string]struct{}{
	"authorization": {},
	"caller_key":    {},
	"secret":        {},
	"secret_ref":    {},
	"api_key":       {},
	"token":         {},
	"cookie":        {},
}

// Redacted is the placeholder emitted in place of a sensitive attribute value.
const Redacted = "REDACTED"

// New returns a JSON structured logger at the given level, writing to stderr and
// redacting sensitive attribute values.
func New(level slog.Level) *slog.Logger {
	return NewWithWriter(os.Stderr, level)
}

// NewWithWriter is like New but writes to w (useful for tests).
func NewWithWriter(w io.Writer, level slog.Level) *slog.Logger {
	h := slog.NewJSONHandler(w, &slog.HandlerOptions{
		Level:       level,
		ReplaceAttr: redact,
	})
	return slog.New(h)
}

func redact(_ []string, a slog.Attr) slog.Attr {
	if _, ok := sensitiveKeys[strings.ToLower(a.Key)]; ok {
		return slog.String(a.Key, Redacted)
	}
	return a
}
