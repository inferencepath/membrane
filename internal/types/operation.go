package types

import "fmt"

// Operation is the kind of egress work a Pathway performs. Where a Pathway names
// intent and routing, an Operation fixes the request/response shape, the egress
// route, the per-operation Permeability vocabulary, and the handler. It is a
// closed, versioned enum; an absent Operation defaults to OperationChat for
// backward compatibility.
type Operation string

const (
	// OperationChat is the default operation: anonymized Chat Completions.
	OperationChat Operation = "chat"
	// OperationEmbeddings turns text into numeric vectors.
	OperationEmbeddings Operation = "embeddings"
	// OperationRerank orders documents by relevance to a query.
	OperationRerank Operation = "rerank"
	// OperationResponses is the agentic/tool-capable Responses operation.
	OperationResponses Operation = "responses"
	// OperationImageGeneration produces image(s) from a prompt.
	OperationImageGeneration Operation = "image_generation"
	// OperationAudioTranscription is speech-to-text (multipart audio in).
	OperationAudioTranscription Operation = "audio_transcription"
	// OperationAudioSpeech is text-to-speech (binary audio out).
	OperationAudioSpeech Operation = "audio_speech"
	// OperationModerations classifies input against moderation categories.
	OperationModerations Operation = "moderations"
)

// DefaultOperation is the operation assumed when a Pathway declares none.
const DefaultOperation = OperationChat

// operationRoutes maps each Operation to its canonical egress route. It is the
// single source of truth for route-per-operation dispatch.
var operationRoutes = map[Operation]string{
	OperationChat:               "/v1/chat",
	OperationEmbeddings:         "/v1/embeddings",
	OperationRerank:             "/v1/rerank",
	OperationResponses:          "/v1/responses",
	OperationImageGeneration:    "/v1/images/generations",
	OperationAudioTranscription: "/v1/audio/transcriptions",
	OperationAudioSpeech:        "/v1/audio/speech",
	OperationModerations:        "/v1/moderations",
}

// AllOperations returns the closed set of operations in a stable, tier order.
func AllOperations() []Operation {
	return []Operation{
		OperationChat,
		OperationEmbeddings,
		OperationRerank,
		OperationResponses,
		OperationImageGeneration,
		OperationAudioTranscription,
		OperationAudioSpeech,
		OperationModerations,
	}
}

// ParseOperation validates and normalizes a raw operation string. An empty
// string yields DefaultOperation (chat); any unrecognized value fails closed.
func ParseOperation(s string) (Operation, error) {
	if s == "" {
		return DefaultOperation, nil
	}
	op := Operation(s)
	if _, ok := operationRoutes[op]; !ok {
		return "", fmt.Errorf("unknown operation %q", s)
	}
	return op, nil
}

// Valid reports whether the Operation is a recognized member of the enum.
func (o Operation) Valid() bool {
	_, ok := operationRoutes[o]
	return ok
}

// Route returns the canonical egress route for the Operation (e.g. "/v1/chat").
// Returns "" for an unrecognized operation.
func (o Operation) Route() string {
	return operationRoutes[o]
}

// String returns the operation's wire string.
func (o Operation) String() string { return string(o) }
