package types

// This file defines the logical, provider-agnostic `embeddings` API DTOs and the
// canonical inward representation. As with chat, these are Interior-only shapes:
// the Caller Key is carried in the request transport (Authorization header),
// never in the body, and the Reveal flag is an Interior-only control field that
// never crosses the Membrane.

// EmbeddingsRequest is the inbound logical embeddings request. Input is a string,
// an array of strings, or an array of token arrays (validated by the handler).
type EmbeddingsRequest struct {
	Pathway string `json:"pathway"`
	Input   any    `json:"input"`
	// EncodingFormat is "float" (default) or "base64".
	EncodingFormat string `json:"encoding_format,omitempty"`
	// Dimensions optionally requests a specific output dimensionality.
	Dimensions *int `json:"dimensions,omitempty"`
	// Reveal is the Interior-only opt-in to receive provider/model. It is consumed
	// on the Interior and MUST NOT cross the Membrane.
	Reveal bool `json:"reveal,omitempty"`
	// Targets is the caller-supplied cascade (honored only when the pathway sets
	// cascade_allowed). Interior-only routing control; never crosses the Membrane.
	Targets []ChatTarget `json:"targets,omitempty"`
}

// EmbeddingVector is one input's embedding, carrying its position in input order.
// Embedding is a float vector ([]float64) by default, or a base64 string when
// encoding_format=base64; it is passed through from the provider as-is.
type EmbeddingVector struct {
	Index     int `json:"index"`
	Embedding any `json:"embedding"`
}

// EmbeddingsUsage holds token accounting for an embeddings request.
type EmbeddingsUsage struct {
	PromptTokens int `json:"prompt_tokens"`
	TotalTokens  int `json:"total_tokens"`
}

// CanonicalEmbeddings is the provider-agnostic embeddings result assembled on the
// inward path. Provider and Model are surfaced to the Caller only when the Reveal
// flag was set (gated by the inbound Permeability on_reveal set).
type CanonicalEmbeddings struct {
	PathwayID PathwayID
	Data      []EmbeddingVector
	Usage     EmbeddingsUsage
	Provider  ProviderID
	Model     string
}

// EmbeddingsResponse is the Caller-facing, provider-neutral embeddings response.
// Provider and Model are populated only when the Caller set the Reveal flag.
type EmbeddingsResponse struct {
	Pathway  string            `json:"pathway"`
	Data     []EmbeddingVector `json:"data"`
	Usage    EmbeddingsUsage   `json:"usage"`
	Provider string            `json:"provider,omitempty"`
	Model    string            `json:"model,omitempty"`
}
