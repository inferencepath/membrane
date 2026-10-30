package types

import "encoding/json"

// Chat API shapes exchanged with Callers. These stay on the Interior. The Caller
// Key travels in the Authorization header, not the body.

// ChatRequest is the inbound logical chat request.
type ChatRequest struct {
	Pathway     string    `json:"pathway"`
	Messages    []Message `json:"messages"`
	Temperature *float64  `json:"temperature,omitempty"`
	MaxTokens   *int      `json:"max_tokens,omitempty"`
	TopP        *float64  `json:"top_p,omitempty"`
	Stop        []string  `json:"stop,omitempty"`
	Stream      bool      `json:"stream,omitempty"`
	// Reveal asks for the provider and model in the response. It is consumed on
	// the Interior and does not cross the Membrane.
	Reveal bool `json:"reveal,omitempty"`
	// Targets is the Caller's cascade: an ordered list of provider and model
	// choices, honored only when the Pathway sets cascade_allowed. It is used
	// for routing and is not forwarded in the payload.
	Targets []ChatTarget `json:"targets,omitempty"`
	// Tools is the set of tools the Caller offers to the model. It crosses the
	// Membrane only when the Pathway's outbound allowlist includes "tools".
	Tools []ToolDefinition `json:"tools,omitempty"`
	// ToolChoice is the opaque tool-selection directive, such as "auto", "none",
	// "required", or an object. It crosses only when allowlisted.
	ToolChoice json.RawMessage `json:"tool_choice,omitempty"`
}

// ChatTarget is one entry in a Caller's cascade: a configured provider and,
// optionally, one of its allowed models. Lower Cascade values are tried first.
type ChatTarget struct {
	Provider string `json:"provider"`
	Model    string `json:"model,omitempty"`
	Cascade  int    `json:"cascade,omitempty"`
}

// ChatResponse is the chat response returned to the Caller. Provider and Model
// are set only when the Caller requested Reveal.
type ChatResponse struct {
	Pathway      string  `json:"pathway"`
	Message      Message `json:"message"`
	FinishReason string  `json:"finish_reason"`
	Usage        Usage   `json:"usage"`
	Provider     string  `json:"provider,omitempty"`
	Model        string  `json:"model,omitempty"`
}
