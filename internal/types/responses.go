package types

// Responses API shapes. The Responses operation supports tools and both plain
// and SSE streaming replies, with the same relay and cancellation behavior as
// chat.

// ResponsesRequest is the inbound responses request. Input is a string prompt
// or an array of structured input items, validated by the handler.
type ResponsesRequest struct {
	Pathway string `json:"pathway"`
	Input   any    `json:"input"`
	// Instructions is an optional top-level system instruction.
	Instructions string `json:"instructions,omitempty"`
	// Tools and ToolChoice declare and steer tool use. They are opaque and
	// pass the Scrub only when allowlisted.
	Tools      []map[string]any `json:"tools,omitempty"`
	ToolChoice any              `json:"tool_choice,omitempty"`
	// Sampling and length controls.
	Temperature     *float64 `json:"temperature,omitempty"`
	MaxOutputTokens *int     `json:"max_output_tokens,omitempty"`
	TopP            *float64 `json:"top_p,omitempty"`
	// Stream requests an SSE stream of incremental events.
	Stream bool `json:"stream,omitempty"`
	// Reveal asks for the provider and model in the response. It does not
	// cross the Membrane.
	Reveal bool `json:"reveal,omitempty"`
	// Targets is the Caller's cascade, honored only when the Pathway sets
	// cascade_allowed. It does not cross the Membrane.
	Targets []ChatTarget `json:"targets,omitempty"`
}

// ResponsesUsage holds token accounting for a responses request.
type ResponsesUsage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
	TotalTokens  int `json:"total_tokens"`
}

// CanonicalResponses is the non-streaming responses result assembled on the
// inward path. Provider and Model reach the Caller only on Reveal, gated by the
// inbound Permeability on_reveal set.
type CanonicalResponses struct {
	PathwayID    PathwayID
	Output       []map[string]any
	FinishReason string
	Usage        ResponsesUsage
	Provider     ProviderID
	Model        string
}

// ResponsesResponse is the responses reply returned to the Caller. Provider and
// Model are set only when the Caller requested Reveal.
type ResponsesResponse struct {
	Pathway      string           `json:"pathway"`
	Output       []map[string]any `json:"output"`
	FinishReason string           `json:"finish_reason,omitempty"`
	Usage        ResponsesUsage   `json:"usage"`
	Provider     string           `json:"provider,omitempty"`
	Model        string           `json:"model,omitempty"`
}

// ResponsesStreamChunk is one streaming event relayed from a Provider. Type
// names the event, such as "output_text.delta"; Delta carries incremental text,
// Item a structured output item, and Done marks the end of the stream.
type ResponsesStreamChunk struct {
	Type  string
	Delta string
	Item  map[string]any
	Done  bool
}
