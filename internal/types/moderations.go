package types

// ModerationsRequest is the inbound moderations request. Input is a string or
// an array of strings, validated by the handler.
type ModerationsRequest struct {
	Pathway string `json:"pathway"`
	Input   any    `json:"input"`
	// Reveal asks for the provider and model in the response. It does not
	// cross the Membrane.
	Reveal bool `json:"reveal,omitempty"`
	// Targets is the Caller's cascade, honored only when the Pathway sets
	// cascade_allowed. It does not cross the Membrane.
	Targets []ChatTarget `json:"targets,omitempty"`
}

// ModerationResult is one input's moderation classification.
type ModerationResult struct {
	Flagged        bool               `json:"flagged"`
	Categories     map[string]bool    `json:"categories"`
	CategoryScores map[string]float64 `json:"category_scores"`
}

// CanonicalModerations is the moderations result assembled on the inward path.
// Provider and Model reach the Caller only on Reveal, gated by the inbound
// Permeability on_reveal set.
type CanonicalModerations struct {
	PathwayID PathwayID
	Results   []ModerationResult
	Provider  ProviderID
	Model     string
}

// ModerationsResponse is the moderations response returned to the Caller.
// Provider and Model are set only when the Caller requested Reveal.
type ModerationsResponse struct {
	Pathway  string             `json:"pathway"`
	Results  []ModerationResult `json:"results"`
	Provider string             `json:"provider,omitempty"`
	Model    string             `json:"model,omitempty"`
}
