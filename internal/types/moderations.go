package types

// This file defines the logical, provider-agnostic `moderations` API DTOs and the
// canonical inward representation. These are Interior-only shapes: the Caller
// Key is carried in the request transport (Authorization header), never in the
// body, and the Reveal flag is an Interior-only control field that never crosses
// the Membrane.

// ModerationsRequest is the inbound logical moderations request. Input is a
// string or an array of strings (validated by the handler).
type ModerationsRequest struct {
	Pathway string `json:"pathway"`
	Input   any    `json:"input"`
	// Reveal is the Interior-only opt-in to receive provider/model.
	Reveal bool `json:"reveal,omitempty"`
	// Targets is the caller-supplied cascade (honored only when the pathway sets
	// cascade_allowed). Interior-only routing control; never crosses the Membrane.
	Targets []ChatTarget `json:"targets,omitempty"`
}

// ModerationResult is one input's moderation classification.
type ModerationResult struct {
	Flagged        bool               `json:"flagged"`
	Categories     map[string]bool    `json:"categories"`
	CategoryScores map[string]float64 `json:"category_scores"`
}

// CanonicalModerations is the provider-agnostic moderations result assembled on
// the inward path. Provider and Model are surfaced only when the Reveal flag was
// set (gated by the inbound Permeability on_reveal set).
type CanonicalModerations struct {
	PathwayID PathwayID
	Results   []ModerationResult
	Provider  ProviderID
	Model     string
}

// ModerationsResponse is the Caller-facing, provider-neutral moderations
// response. Provider and Model are populated only when the Caller set Reveal.
type ModerationsResponse struct {
	Pathway  string             `json:"pathway"`
	Results  []ModerationResult `json:"results"`
	Provider string             `json:"provider,omitempty"`
	Model    string             `json:"model,omitempty"`
}
