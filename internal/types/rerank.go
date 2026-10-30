package types

// RerankRequest is the inbound rerank request: a query ranked against a list of
// candidate documents.
type RerankRequest struct {
	Pathway   string   `json:"pathway"`
	Query     string   `json:"query"`
	Documents []string `json:"documents"`
	// TopN optionally limits the results to the highest-scoring N.
	TopN *int `json:"top_n,omitempty"`
	// ReturnDocuments echoes the original document text in each result.
	ReturnDocuments bool `json:"return_documents,omitempty"`
	// Reveal asks for the provider and model in the response. It is consumed
	// on the Interior and does not cross the Membrane.
	Reveal bool `json:"reveal,omitempty"`
	// Targets is the Caller's cascade, honored only when the Pathway sets
	// cascade_allowed. It does not cross the Membrane.
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

// CanonicalRerank is the rerank result assembled on the inward path. Provider
// and Model reach the Caller only on Reveal, gated by the inbound Permeability
// on_reveal set.
type CanonicalRerank struct {
	PathwayID PathwayID
	Results   []RerankResult
	Usage     RerankUsage
	Provider  ProviderID
	Model     string
}

// RerankResponse is the rerank response returned to the Caller. Provider and
// Model are set only when the Caller requested Reveal.
type RerankResponse struct {
	Pathway  string         `json:"pathway"`
	Results  []RerankResult `json:"results"`
	Usage    RerankUsage    `json:"usage"`
	Provider string         `json:"provider,omitempty"`
	Model    string         `json:"model,omitempty"`
}
