// Package pipeline defines optional extension points on the Interior: a
// ContentStage that can rewrite request and response content, and an
// ObservationStage that can observe requests. Both default to no-ops.
//
// Ingress runs after Admit and before buildCanonical and the Scrub; Egress runs
// after the inward Scrub and before the response is written. A stage sees only
// Interior-side logical content, never secrets or provider identity, and must
// not alter Permeability, the Scrub, the Transporter or the SanitizedRequest
// type.
package pipeline

import (
	"context"

	"github.com/inferencepath/membrane/internal/types"
)

// ContentStage is an optional extension point that transforms logical content
// in place. The default, NoOp, does nothing.
//
// Ingress runs after Admit and before buildCanonical and may rewrite request
// message content. A non-nil error fails the request closed with a generic
// error, and nothing crosses the Membrane.
//
// Egress runs after the inward Scrub, once per streamed chunk when streaming,
// and may rewrite response content. A non-nil error fails the response closed.
type ContentStage interface {
	Ingress(ctx context.Context, rc *RequestContext) error
	Egress(ctx context.Context, rc *ResponseContext) error
}

// RequestContext is what a ContentStage sees on ingress. None of its fields
// cross the Membrane.
//
// Messages shares its backing array with the handler, so assigning to
// Messages[i].Content is seen by the Scrub. A stage must not grow or shrink the
// slice.
type RequestContext struct {
	// RequestID is minted per request and keys any request-scoped stage state.
	RequestID string
	// CallerID is the authenticated Caller from Admit.
	CallerID string
	// PathwayID is the logical Pathway for this request.
	PathwayID string
	// Messages is the request's conversation turns, rewritten in place.
	Messages []types.Message
	// Protection is a content-free marker the stage sets during Ingress. The
	// ObservationStage reads it to decide whether content may be captured. The
	// zero value means unprotected.
	Protection ProtectionMarker
}

// ResponseContext is what a ContentStage sees on egress. Message is set for a
// unary response and Chunk for a streamed one, never both.
type ResponseContext struct {
	// RequestID correlates to the request's ingress state.
	RequestID string
	// Message is the unary response turn to rewrite in place; nil when streaming.
	Message *types.Message
	// Chunk is the current streamed chunk to rewrite in place; nil when unary.
	Chunk *types.StreamChunk
}
