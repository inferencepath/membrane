package types

// This file defines the logical, provider-agnostic `responses` API DTOs and the
// canonical inward representation. The Responses operation is agentic/
// tool-capable and supports both non-streaming and SSE streaming, mirroring
// chat's incremental-relay and cancellation guarantees. As with the other
// operations these are Interior-only shapes: the Caller Key is carried in the
// request transport (Authorization header), never in the body, and the Reveal
// flag is an Interior-only control field that never crosses the Membrane.

// ResponsesRequest is the inbound logical responses request. Input is a string
// prompt or an array of structured input items (validated by the handler).
type ResponsesRequest struct {
	Pathway string `json:"pathway"`
	Input   any    `json:"input"`
	// Instructions is an optional top-level system instruction.
	Instructions string `json:"instructions,omitempty"`
	// Tools and ToolChoice declare and steer tool/function use. They are opaque,
	// provider-neutral structures carried through the Scrub by allowlist.
	Tools      []map[string]any `json:"tools,omitempty"`
	ToolChoice any              `json:"tool_choice,omitempty"`
	// Sampling / limit controls.
	Temperature     *float64 `json:"temperature,omitempty"`
	MaxOutputTokens *int     `json:"max_output_tokens,omitempty"`
	TopP            *float64 `json:"top_p,omitempty"`
	// Stream requests an SSE stream of incremental events.
	Stream bool `json:"stream,omitempty"`
	// Reveal is the Interior-only opt-in to receive provider/model.
	Reveal bool `json:"reveal,omitempty"`
	// Targets is the caller-supplied cascade (honored only when the pathway sets
	// cascade_allowed). Interior-only routing control; never crosses the Membrane.
	Targets []ChatTarget `json:"targets,omitempty"`
}

// ResponsesUsage holds token accounting for a responses request.
type ResponsesUsage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
	TotalTokens  int `json:"total_tokens"`
}

// CanonicalResponses is the provider-agnostic responses result assembled on the
// inward (non-streaming) path. Output items are passed through provider-neutral.
// Provider and Model are surfaced only when the Reveal flag was set (gated by the
// inbound Permeability on_reveal set).
type CanonicalResponses struct {
	PathwayID    PathwayID
	Output       []map[string]any
	FinishReason string
	Usage        ResponsesUsage
	Provider     ProviderID
	Model        string
}

// ResponsesResponse is the Caller-facing, provider-neutral responses response.
// Provider and Model are populated only when the Caller set the Reveal flag.
type ResponsesResponse struct {
	Pathway      string           `json:"pathway"`
	Output       []map[string]any `json:"output"`
	FinishReason string           `json:"finish_reason,omitempty"`
	Usage        ResponsesUsage   `json:"usage"`
	Provider     string           `json:"provider,omitempty"`
	Model        string           `json:"model,omitempty"`
}

// ResponsesStreamChunk is one incremental streaming event relayed from a Provider
// across the Membrane. It is provider-neutral: Type names the event (e.g.
// "output_text.delta"), Delta carries incremental text, Item carries a structured
// output item, and Done marks the end of the stream.
type ResponsesStreamChunk struct {
	Type  string
	Delta string
	Item  map[string]any
	Done  bool
}
