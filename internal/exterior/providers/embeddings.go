package providers

import (
	"context"
	"errors"

	"github.com/inferencepath/membrane/internal/types"
)

// ErrOperationUnsupported indicates a Provider adapter does not implement the
// requested egress operation (e.g. embeddings on a chat-only adapter). It is a
// defensive runtime guard; wiring a Pathway to a provider that lacks its
// operation is already rejected fail-closed at config assembly.
var ErrOperationUnsupported = errors.New("providers: operation not supported by adapter")

// EmbeddingsProvider is an OPTIONAL capability a Provider adapter may implement
// to serve the `embeddings` operation. Adapters
// that do not implement it are simply not type-assertable to it; the Transporter
// then returns ErrOperationUnsupported. Keeping this out of the core Provider
// interface means adding an operation never forces every adapter (or test mock)
// to change.
//
// Like Provider, an EmbeddingsProvider is driven only by a neutral Request (the
// allowlisted, resolved egress payload) plus an EgressIdentity. It never receives
// Caller context, the Caller Key, or the Reveal flag.
type EmbeddingsProvider interface {
	// Embeddings performs a non-streaming embeddings request and returns a
	// provider-neutral CanonicalEmbeddings.
	Embeddings(ctx context.Context, req Request, id EgressIdentity) (types.CanonicalEmbeddings, error)
}

// RerankProvider is an OPTIONAL capability a Provider adapter may implement to
// serve the `rerank` operation. Like
// EmbeddingsProvider it is kept out of the core Provider interface so adding an
// operation never forces every adapter (or test mock) to change. It is driven
// only by a neutral Request plus an EgressIdentity; it never receives Caller
// context, the Caller Key, or the Reveal flag.
type RerankProvider interface {
	// Rerank performs a non-streaming rerank request and returns a
	// provider-neutral CanonicalRerank.
	Rerank(ctx context.Context, req Request, id EgressIdentity) (types.CanonicalRerank, error)
}

// ModerationsProvider is an OPTIONAL capability a Provider adapter may implement
// to serve the `moderations` operation. Like the
// other operation capabilities it is kept out of the core Provider interface. It
// is driven only by a neutral Request plus an EgressIdentity.
type ModerationsProvider interface {
	// Moderate performs a non-streaming moderations request.
	Moderate(ctx context.Context, req Request, id EgressIdentity) (types.CanonicalModerations, error)
}

// TranscriptionProvider is an OPTIONAL capability a Provider adapter may implement
// to serve the `audio_transcription` operation. The
// audio bytes travel in the neutral Request payload under the "file" key (already
// allowlisted by the Scrub); the adapter reconstructs the outbound multipart from
// the payload, never forwarding the client filename or extra parts.
type TranscriptionProvider interface {
	// Transcribe performs a non-streaming speech-to-text request.
	Transcribe(ctx context.Context, req Request, id EgressIdentity) (types.CanonicalTranscription, error)
}

// SpeechProvider is an OPTIONAL capability a Provider adapter may implement to
// serve the `audio_speech` operation. The response
// is a binary audio stream relayed incrementally via emit; the first emitted
// SpeechChunk is a Head carrying the Content-Type and Provider/Model. Like the
// other operation capabilities it is kept out of the core Provider interface.
type SpeechProvider interface {
	// Speech performs a text-to-speech request, relaying audio via emit.
	Speech(ctx context.Context, req Request, id EgressIdentity, emit func(types.SpeechChunk) error) error
}

// ImageProvider is an OPTIONAL capability a Provider adapter may implement to
// serve the `image_generation` operation. Like the
// other operation capabilities it is kept out of the core Provider interface so
// adding an operation never forces every adapter (or test mock) to change. It is
// driven only by a neutral Request plus an EgressIdentity; it never receives
// Caller context, the Caller Key, or the Reveal flag.
type ImageProvider interface {
	// Images performs a non-streaming image generation request and returns a
	// provider-neutral CanonicalImage.
	Images(ctx context.Context, req Request, id EgressIdentity) (types.CanonicalImage, error)
}

// ResponsesProvider is an OPTIONAL capability a Provider adapter may implement to
// serve the `responses` operation. It is agentic /
// tool-capable and supports both non-streaming and SSE streaming, so it exposes
// two methods mirroring the core Provider's Complete/Stream split. Like the other
// operation capabilities it is kept out of the core Provider interface so adding
// an operation never forces every adapter (or test mock) to change. It is driven
// only by a neutral Request plus an EgressIdentity; it never receives Caller
// context, the Caller Key, or the Reveal flag.
type ResponsesProvider interface {
	// Responses performs a non-streaming responses request.
	Responses(ctx context.Context, req Request, id EgressIdentity) (types.CanonicalResponses, error)
	// ResponsesStream performs a streaming responses request, relaying each
	// incremental event via emit.
	ResponsesStream(ctx context.Context, req Request, id EgressIdentity, emit func(types.ResponsesStreamChunk) error) error
}
