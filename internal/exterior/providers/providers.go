// Package providers defines the Exterior Provider port and the neutral types
// used to call a Provider across the Membrane. Concrete adapters live in
// subpackages (e.g. openai). This package must not import Interior packages.
//
// A Provider is driven only by a Request (the allowlisted, resolved egress
// payload built from a SanitizedRequest by the Transporter) plus an
// EgressIdentity. It never receives Caller context, the Caller Key, or the
// Reveal flag.
package providers

import (
	"context"
	"errors"
	"fmt"

	"github.com/inferencepath/membrane/internal/types"
)

// Request is the neutral, egress-ready request handed to a Provider adapter. It
// is built by the Transporter from a SanitizedRequest and therefore contains no
// caller/org identity or control fields.
type Request struct {
	// Model is the resolved egress model.
	Model string
	// Payload is the allowlisted logical payload (messages + permitted params).
	Payload map[string]any
	// Stream indicates a streaming response is desired.
	Stream bool
}

// EgressIdentity is the opaque Provider credential attached at/after the
// Membrane. For OpenAI-compatible Providers it is a bearer token. It MUST NEVER
// be logged or surfaced to a Caller.
type EgressIdentity struct {
	// Secret is the opaque credential material (e.g. an API key / bearer token).
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

// ErrStreamingUnsupported indicates the Provider adapter does not implement the
// streaming path. Adapters that lack it
// fail closed rather than silently degrading.
var ErrStreamingUnsupported = errors.New("providers: streaming not supported")

// ErrToolCallingUnsupported indicates a request carried tool definitions/calls
// but the resolved Provider adapter does not implement tool calling. It is a
// request-level (not transient) failure: the handler maps it to a 400 and the
// Transporter does not fail over on it.
var ErrToolCallingUnsupported = errors.New("providers: tool calling not supported")

// RequestHasTools reports whether a neutral allowlisted payload carries tool
// definitions or tool-bearing messages (assistant tool_calls or a tool-result
// turn). Adapters that do not implement tool calling use it to fail closed
// before any upstream contact.
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

// UpstreamStatusError indicates the Provider returned a non-2xx HTTP status. The
// status is retained for provider-neutral mapping; no provider body is carried.
// Provider is the family that returned the status, retained ONLY so the handler
// may disclose it when the Caller sets Reveal; Error() never includes it, so the
// family cannot leak through logs or non-revealed error bodies.
type UpstreamStatusError struct {
	Status   int
	Provider types.ProviderID
}

func (e UpstreamStatusError) Error() string {
	// Deliberately provider-neutral: no family/endpoint/credential detail.
	return fmt.Sprintf("providers: upstream status %d", e.Status)
}

// FailureKind is a provider-neutral classification of an egress failure, shared
// by the server's error mapping so no provider/endpoint/credential detail is
// needed to decide the Caller-facing result.
type FailureKind int

const (
	// FailureProvider is an upstream-returned error (auth/429/5xx and any other
	// upstream status). Mapped to 502 Bad Gateway.
	FailureProvider FailureKind = iota
	// FailureTimeout is an upstream non-response within the deadline. Mapped to
	// 504 Gateway Timeout.
	FailureTimeout
	// FailureUnavailable is an upstream that is explicitly unavailable (503/504).
	// Mapped to 504 Gateway Timeout.
	FailureUnavailable
)

// ClassifyFailure maps an egress error to a provider-neutral FailureKind. It is
// the single source of truth for turning adapter/transport errors into a coarse,
// identity-free outcome.
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

// RevealableProvider returns the Provider family responsible for an error when
// it is known (an UpstreamStatusError stamped by an adapter), or empty. Callers
// MUST gate disclosure on the Reveal flag.
func RevealableProvider(err error) types.ProviderID {
	var se UpstreamStatusError
	if errors.As(err, &se) {
		return se.Provider
	}
	return ""
}
