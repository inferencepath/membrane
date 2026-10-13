package types

// This file defines the logical, provider-agnostic `rerank` API DTOs and the
// canonical inward representation. As with chat and embeddings, these are
// Interior-only shapes: the Caller Key is carried in the request transport
// (Authorization header), never in the body, and the Reveal flag is an
// Interior-only control field that never crosses the Membrane.

// RerankRequest is the inbound logical rerank request: a query ranked against a
// list of candidate documents.
type RerankRequest struct {
	Pathway   string   `json:"pathway"`
	Query     string   `json:"query"`
	Documents []string `json:"documents"`
	// TopN optionally caps the number of returned results (by descending score).
	TopN *int `json:"top_n,omitempty"`
	// ReturnDocuments echoes the original document text in each result.
	ReturnDocuments bool `json:"return_documents,omitempty"`
	// Reveal is the Interior-only opt-in to receive provider/model. It is consumed
	// on the Interior and MUST NOT cross the Membrane.
	Reveal bool `json:"reveal,omitempty"`
	// Targets is the caller-supplied cascade (honored only when the pathway sets
	// cascade_allowed). Interior-only routing control; never crosses the Membrane.
	Targets []ChatTarget `json:"targets,omitempty"`
}

// RerankResult is one scored document, keyed by its position in the input
// documents. Document is populated only when return_documents was requested.
type RerankResult struct {
	Index          int     `json:"index"`
	RelevanceScore float64 `json:"relevance_score"`
	Document       string  `json:"document,omitempty"`
}

// RerankUsage holds token accounting for a rerank request.
type RerankUsage struct {
	TotalTokens int `json:"total_tokens"`
}

// CanonicalRerank is the provider-agnostic rerank result assembled on the inward
// path. Provider and Model are surfaced to the Caller only when the Reveal flag
// was set (gated by the inbound Permeability on_reveal set).
type CanonicalRerank struct {
	PathwayID PathwayID
	Results   []RerankResult
	Usage     RerankUsage
	Provider  ProviderID
	Model     string
}

// RerankResponse is the Caller-facing, provider-neutral rerank response. Provider
// and Model are populated only when the Caller set the Reveal flag.
type RerankResponse struct {
	Pathway  string         `json:"pathway"`
	Results  []RerankResult `json:"results"`
	Usage    RerankUsage    `json:"usage"`
	Provider string         `json:"provider,omitempty"`
	Model    string         `json:"model,omitempty"`
}
