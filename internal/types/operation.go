package types

import "fmt"

// Operation is the kind of egress work a Pathway performs. It determines the
// request and response shapes, the egress route, the Permeability vocabulary,
// and the handler. The set is closed; an empty Operation means OperationChat.
type Operation string

const (
	// OperationChat is the default operation: chat completions.
	OperationChat Operation = "chat"
	// OperationEmbeddings turns text into numeric vectors.
	OperationEmbeddings Operation = "embeddings"
	// OperationRerank orders documents by relevance to a query.
	OperationRerank Operation = "rerank"
	// OperationResponses is the tool-capable Responses operation.
	OperationResponses Operation = "responses"
	// OperationImageGeneration produces image(s) from a prompt.
	OperationImageGeneration Operation = "image_generation"
	// OperationAudioTranscription is speech-to-text from a multipart upload.
	OperationAudioTranscription Operation = "audio_transcription"
	// OperationAudioSpeech is text-to-speech returning binary audio.
	OperationAudioSpeech Operation = "audio_speech"
	// OperationModerations classifies input against moderation categories.
	OperationModerations Operation = "moderations"
)

// DefaultOperation is the operation assumed when a Pathway declares none.
const DefaultOperation = OperationChat

// operationRoutes maps each Operation to its egress route. It also defines the
// set of valid operations.
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

// AllOperations returns every operation in a stable order.
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

// ParseOperation parses an operation string. An empty string yields
// DefaultOperation; an unknown value is an error.
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

// Valid reports whether o is a known operation.
func (o Operation) Valid() bool {
	_, ok := operationRoutes[o]
	return ok
}

// Route returns the egress route for o, such as "/v1/chat", or "" if o is
// unknown.
func (o Operation) Route() string {
	return operationRoutes[o]
}

// String returns the operation's wire string.
func (o Operation) String() string { return string(o) }
