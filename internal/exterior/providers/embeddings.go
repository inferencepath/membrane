package providers

import (
	"context"
	"errors"

	"github.com/inferencepath/membrane/internal/types"
)

// ErrOperationUnsupported indicates a Provider adapter does not implement the
// requested egress operation, such as embeddings on a chat-only adapter. Config
// assembly already rejects a Pathway whose Provider lacks its operation; this is
// the runtime backstop.
var ErrOperationUnsupported = errors.New("providers: operation not supported by adapter")

// EmbeddingsProvider is an optional adapter capability for the embeddings
// operation. If an adapter does not implement it, the Transporter returns
// ErrOperationUnsupported. Operation capabilities are kept out of Provider so a
// new operation does not require changes to every adapter and test mock.
//
// Like Provider, it receives only a neutral Request and an EgressIdentity, never
// Caller context, the Caller Key, or the Reveal flag.
type EmbeddingsProvider interface {
	// Embeddings performs a non-streaming embeddings request and returns a
	// provider-neutral CanonicalEmbeddings.
	Embeddings(ctx context.Context, req Request, id EgressIdentity) (types.CanonicalEmbeddings, error)
}

// RerankProvider is an optional adapter capability for the rerank operation.
type RerankProvider interface {
	// Rerank performs a non-streaming rerank request and returns a
	// provider-neutral CanonicalRerank.
	Rerank(ctx context.Context, req Request, id EgressIdentity) (types.CanonicalRerank, error)
}

// ModerationsProvider is an optional adapter capability for the moderations
// operation.
type ModerationsProvider interface {
	// Moderate performs a non-streaming moderations request.
	Moderate(ctx context.Context, req Request, id EgressIdentity) (types.CanonicalModerations, error)
}

// TranscriptionProvider is an optional adapter capability for the
// audio_transcription operation. The audio bytes arrive in the payload under the
// "file" key. The adapter builds a new multipart body from the payload and does
// not forward the client filename or extra parts.
type TranscriptionProvider interface {
	// Transcribe performs a non-streaming speech-to-text request.
	Transcribe(ctx context.Context, req Request, id EgressIdentity) (types.CanonicalTranscription, error)
}

// SpeechProvider is an optional adapter capability for the audio_speech
// operation. Audio is relayed incrementally through emit; the first SpeechChunk
// is a Head carrying the Content-Type, Provider, and Model.
type SpeechProvider interface {
	// Speech performs a text-to-speech request, relaying audio via emit.
	Speech(ctx context.Context, req Request, id EgressIdentity, emit func(types.SpeechChunk) error) error
}

// ImageProvider is an optional adapter capability for the image_generation
// operation.
type ImageProvider interface {
	// Images performs a non-streaming image generation request and returns a
	// provider-neutral CanonicalImage.
	Images(ctx context.Context, req Request, id EgressIdentity) (types.CanonicalImage, error)
}

// ResponsesProvider is an optional adapter capability for the responses
// operation. It supports tools and both plain and SSE streaming responses, so,
// like Provider, it has separate non-streaming and streaming methods.
type ResponsesProvider interface {
	// Responses performs a non-streaming responses request.
	Responses(ctx context.Context, req Request, id EgressIdentity) (types.CanonicalResponses, error)
	// ResponsesStream performs a streaming responses request, relaying each
	// incremental event via emit.
	ResponsesStream(ctx context.Context, req Request, id EgressIdentity, emit func(types.ResponsesStreamChunk) error) error
}
