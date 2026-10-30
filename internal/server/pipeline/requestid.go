package pipeline

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"io"
)

type requestIDKey struct{}

// NewRequestID returns a random, opaque request ID that is not derived from the
// Caller or the content. If the system RNG fails it returns "", which callers
// treat as having no request-scoped state.
func NewRequestID() string {
	return newRequestID(rand.Reader)
}

func newRequestID(entropy io.Reader) string {
	var b [16]byte
	if _, err := io.ReadFull(entropy, b[:]); err != nil {
		return ""
	}
	return hex.EncodeToString(b[:])
}

// WithRequestID returns a context carrying the given request ID.
func WithRequestID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, requestIDKey{}, id)
}

// RequestIDFrom returns the request ID carried by ctx, or "" if none is set.
func RequestIDFrom(ctx context.Context) string {
	id, _ := ctx.Value(requestIDKey{}).(string)
	return id
}

type protectionKey struct{}

// WithProtection returns a context carrying m. The handler attaches it after
// Ingress so the ObservationStage can read it.
func WithProtection(ctx context.Context, m ProtectionMarker) context.Context {
	return context.WithValue(ctx, protectionKey{}, m)
}

// ProtectionFrom returns the ProtectionMarker carried by ctx, or the zero
// (unprotected) value if none is set.
func ProtectionFrom(ctx context.Context) ProtectionMarker {
	m, _ := ctx.Value(protectionKey{}).(ProtectionMarker)
	return m
}
