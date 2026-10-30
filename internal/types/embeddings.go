package types

// EmbeddingsRequest is the inbound embeddings request. Input is a string, an
// array of strings, or an array of token arrays; the handler validates it.
type EmbeddingsRequest struct {
	Pathway string `json:"pathway"`
	Input   any    `json:"input"`
	// EncodingFormat is "float" (default) or "base64".
	EncodingFormat string `json:"encoding_format,omitempty"`
	// Dimensions optionally requests a specific output dimensionality.
	Dimensions *int `json:"dimensions,omitempty"`
	// Reveal asks for the provider and model in the response. It is consumed on
	// the Interior and does not cross the Membrane.
	Reveal bool `json:"reveal,omitempty"`
	// Targets is the Caller's cascade, honored only when the Pathway sets
	// cascade_allowed. It does not cross the Membrane.
	Targets []ChatTarget `json:"targets,omitempty"`
}

// EmbeddingVector is the embedding for the input at Index. Embedding is a
// []float64 by default or a base64 string when encoding_format is base64, passed
// through from the provider unchanged.
type EmbeddingVector struct {
	Index     int `json:"index"`
	Embedding any `json:"embedding"`
}

// EmbeddingsUsage holds token accounting for an embeddings request.
type EmbeddingsUsage struct {
	PromptTokens int `json:"prompt_tokens"`
	TotalTokens  int `json:"total_tokens"`
}

// CanonicalEmbeddings is the embeddings result assembled on the inward path.
// Provider and Model reach the Caller only when Reveal was set and the inbound
// Permeability on_reveal set allows them.
type CanonicalEmbeddings struct {
	PathwayID PathwayID
	Data      []EmbeddingVector
	Usage     EmbeddingsUsage
	Provider  ProviderID
	Model     string
}

// EmbeddingsResponse is the embeddings response returned to the Caller. Provider
// and Model are set only when the Caller requested Reveal.
type EmbeddingsResponse struct {
	Pathway  string            `json:"pathway"`
	Data     []EmbeddingVector `json:"data"`
	Usage    EmbeddingsUsage   `json:"usage"`
	Provider string            `json:"provider,omitempty"`
	Model    string            `json:"model,omitempty"`
}
