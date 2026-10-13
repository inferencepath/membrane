package types

import "encoding/json"

// This file defines the logical, provider-agnostic `chat` API DTOs (the shapes
// exchanged with Callers). They are Interior-only representations: the Caller
// Key is carried in the request transport (Authorization header), never in the
// body, and the Reveal flag is an Interior-only control field that never crosses
// the Membrane.

// ChatRequest is the inbound logical chat request.
type ChatRequest struct {
	Pathway     string    `json:"pathway"`
	Messages    []Message `json:"messages"`
	Temperature *float64  `json:"temperature,omitempty"`
	MaxTokens   *int      `json:"max_tokens,omitempty"`
	TopP        *float64  `json:"top_p,omitempty"`
	Stop        []string  `json:"stop,omitempty"`
	Stream      bool      `json:"stream,omitempty"`
	// Reveal is the Interior-only opt-in to receive provider/model. It is
	// consumed on the Interior and MUST NOT cross the Membrane.
	Reveal bool `json:"reveal,omitempty"`
	// Targets is the caller-supplied cascade: an ordered list of
	// provider+model tiers to try, honored ONLY when the pathway sets
	// cascade_allowed. It is an Interior-only routing control and MUST NOT cross
	// the Membrane (provider/model are never forwarded in the payload).
	Targets []ChatTarget `json:"targets,omitempty"`
	// Tools is the provider-agnostic set of tools the Caller offers to the model.
	// It crosses the Membrane ONLY when the Pathway's outbound allowlist includes
	// "tools" (closed by default).
	Tools []ToolDefinition `json:"tools,omitempty"`
	// ToolChoice is the opaque tool-selection directive (e.g. "auto"/"none"/
	// "required" or an object). It crosses only when allowlisted.
	ToolChoice json.RawMessage `json:"tool_choice,omitempty"`
}

// ChatTarget is one caller-chosen cascade tier: which configured provider to use
// and (optionally) which of that provider's allowed models. Cascade orders the
// tiers (ascending; lower is tried first).
type ChatTarget struct {
	Provider string `json:"provider"`
	Model    string `json:"model,omitempty"`
	Cascade  int    `json:"cascade,omitempty"`
}

// ChatResponse is the Caller-facing, provider-neutral chat response. Provider
// and Model are populated only when the Caller set the Reveal flag.
type ChatResponse struct {
	Pathway      string  `json:"pathway"`
	Message      Message `json:"message"`
	FinishReason string  `json:"finish_reason"`
	Usage        Usage   `json:"usage"`
	Provider     string  `json:"provider,omitempty"`
	Model        string  `json:"model,omitempty"`
}
