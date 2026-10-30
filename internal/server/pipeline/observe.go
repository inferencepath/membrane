// The ObservationStage is called at three points: OnRequest after Admit,
// OnProviderExchange at the Membrane boundary, and OnComplete once the response
// is finalized. Calls are best-effort; a request never blocks or fails because
// of them, and the stage cannot alter routing, Permeability, the Scrub, the
// Transporter or whether a request proceeds.

package pipeline

import (
	"context"

	"github.com/inferencepath/membrane/internal/types"
)

// ObservationStage is an optional extension point for observing requests. The
// default, ObserveNoOp, does nothing. Implementations must be non-blocking; a
// slow or failing stage must not degrade the data plane.
type ObservationStage interface {
	// OnRequest is called after Admit, once the request ID is minted.
	OnRequest(ctx context.Context, ev RequestEvent)

	// OnProviderExchange is called after the Provider response arrives and
	// before ContentStage Egress, with the sanitized request and response.
	// Whether to retain them is up to the stage, based on ev.Protection.
	OnProviderExchange(ctx context.Context, ev ProviderExchangeEvent)

	// OnComplete is called once the response to the Caller is finalized.
	OnComplete(ctx context.Context, ev CompleteEvent)
}

// ProtectionMarker is a content-free signal set by a ContentStage during
// Ingress and read by the ObservationStage. The zero value means unprotected,
// so content is never eligible for capture without a content stage.
type ProtectionMarker struct {
	// Protected reports whether a stage transformed content.
	Protected bool
	// Level is the stage-defined protection level ("off" means none).
	Level string
	// PolicyVersion is the version of the policy in force; may be empty.
	PolicyVersion string
}

// RequestEvent is the content-free event sent after Admit.
type RequestEvent struct {
	RequestID string
	CallerID  string
	PathwayID string
	Operation string
	Stream    bool
}

// ProviderExchangeEvent carries the sanitized payloads exchanged with the
// Transporter. Content of an unprotected request (Protected false or Level
// "off") must not be retained.
type ProviderExchangeEvent struct {
	RequestID  string
	CallerID   string
	PathwayID  string
	Protection ProtectionMarker
	// Stream reports whether this exchange was a streamed response.
	Stream bool
	// Outbound is the sanitized request turns as they cross the Membrane.
	Outbound []types.Message
	// Inbound is the sanitized response turn before Egress. For a stream it is
	// the assembled response text.
	Inbound *types.Message
}

// CompleteEvent is the content-free per-request outcome.
type CompleteEvent struct {
	RequestID        string
	CallerID         string
	PathwayID        string
	Operation        string
	Protection       ProtectionMarker
	Outcome          string
	DurationMillis   int64
	PromptTokens     int
	CompletionTokens int
	TotalTokens      int
	Stream           bool
	// ProviderFamily is the coarse Provider family, such as "openai"; never an
	// endpoint, region or credential. Empty when unknown.
	ProviderFamily string
	// Model is the routed model ID; empty when unknown.
	Model string
}
