// Package providers defines the Exterior Provider port and the neutral types
// used to call a Provider across the Membrane. Concrete adapters live in
// subpackages. This package must not import Interior packages.
//
// A Provider sees only a Request (the allowlisted egress payload the
// Transporter builds from a SanitizedRequest) and an EgressIdentity. It never
// receives Caller context, the Caller Key, or the Reveal flag.
package providers

import (
	"context"
	"errors"
	"fmt"

	"github.com/inferencepath/membrane/internal/types"
)

// Request is the neutral request handed to a Provider adapter. The Transporter
// builds it from a SanitizedRequest, so it carries no Caller identity or
// control fields.
type Request struct {
	// Model is the resolved egress model.
	Model string
	// Payload is the allowlisted payload: messages plus permitted parameters.
	Payload map[string]any
	// Stream requests a streaming response.
	Stream bool
}

// EgressIdentity is the opaque Provider credential attached at the Membrane.
// For OpenAI-compatible Providers it is a bearer token. It must never be
// logged or shown to a Caller.
type EgressIdentity struct {
	// Secret is the credential material, such as an API key.
	Secret string
}

// Provider is an external AI backend reached across the Membrane.
type Provider interface {
	// ID returns the Provider's identifier (e.g. "openai").
	ID() types.ProviderID
	// Complete performs a non-streaming request and returns a CanonicalResponse.
	Complete(ctx context.Context, req Request, id EgressIdentity) (types.CanonicalResponse, error)
	// Stream performs a streaming request, invoking emit for each normalized
	// chunk as it arrives. Returning an error from emit aborts the stream.
	Stream(ctx context.Context, req Request, id EgressIdentity, emit func(types.StreamChunk) error) error
}

// ErrUpstreamTimeout indicates the Provider did not respond within the deadline.
var ErrUpstreamTimeout = errors.New("providers: upstream timeout")

// ErrStreamingUnsupported indicates the Provider adapter has no streaming
// path. Such adapters fail closed instead of falling back to non-streaming.
var ErrStreamingUnsupported = errors.New("providers: streaming not supported")

// ErrToolCallingUnsupported indicates a request carried tool definitions or
// calls but the resolved adapter does not support tool calling. It is a
// request error, not a transient one: the handler maps it to 400 and the
// Transporter does not fail over on it.
var ErrToolCallingUnsupported = errors.New("providers: tool calling not supported")

// RequestHasTools reports whether payload carries tool definitions or
// tool-bearing messages (assistant tool_calls or a tool-result turn). Adapters
// without tool support use it to fail closed before contacting upstream.
func RequestHasTools(payload map[string]any) bool {
	if _, ok := payload["tools"]; ok {
		return true
	}
	msgs, ok := payload["messages"].([]any)
	if !ok {
		return false
	}
	for _, m := range msgs {
		mm, ok := m.(map[string]any)
		if !ok {
			continue
		}
		if role, _ := mm["role"].(string); role == "tool" {
			return true
		}
		if _, ok := mm["tool_calls"]; ok {
			return true
		}
	}
	return false
}

// UpstreamStatusError indicates the Provider returned a non-2xx HTTP status.
// The upstream body is not kept. Provider records which family returned the
// status so the handler can disclose it when the Caller sets Reveal; Error
// never includes it, so it cannot leak through logs or unrevealed errors.
type UpstreamStatusError struct {
	Status   int
	Provider types.ProviderID
}

func (e UpstreamStatusError) Error() string {
	// No family, endpoint, or credential detail.
	return fmt.Sprintf("providers: upstream status %d", e.Status)
}

// FailureKind is a provider-neutral classification of an egress failure. The
// server maps it to a Caller-facing status without needing provider detail.
type FailureKind int

const (
	// FailureProvider is an error status returned by upstream (auth, 429,
	// 5xx, or any other). Maps to 502 Bad Gateway.
	FailureProvider FailureKind = iota
	// FailureTimeout means upstream did not respond before the deadline. Maps
	// to 504 Gateway Timeout.
	FailureTimeout
	// FailureUnavailable means upstream reported itself unavailable (503 or
	// 504). Maps to 504 Gateway Timeout.
	FailureUnavailable
)

// ClassifyFailure maps an adapter or transport error to a FailureKind.
func ClassifyFailure(err error) FailureKind {
	if errors.Is(err, ErrUpstreamTimeout) || errors.Is(err, context.DeadlineExceeded) {
		return FailureTimeout
	}
	var se UpstreamStatusError
	if errors.As(err, &se) {
		if se.Status == 503 || se.Status == 504 {
			return FailureUnavailable
		}
		return FailureProvider
	}
	return FailureProvider
}

// RevealableProvider returns the Provider family recorded on an
// UpstreamStatusError in err's chain, or "" if there is none. Callers must
// only disclose it when Reveal is set.
func RevealableProvider(err error) types.ProviderID {
	var se UpstreamStatusError
	if errors.As(err, &se) {
		return se.Provider
	}
	return ""
}
