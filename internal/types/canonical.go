package types

import "encoding/json"

// Message is a single provider-agnostic conversation turn. The tool fields are
// omitempty, so a plain text turn carries only role and content.
type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
	// ToolCalls is set on an assistant turn that invokes tools. It carries no
	// provider identity.
	ToolCalls []ToolCall `json:"tool_calls,omitempty"`
	// ToolCallID links a tool-result turn (role "tool") to an earlier ToolCall.
	ToolCallID string `json:"tool_call_id,omitempty"`
	// Name is the tool name on a tool-result turn (role "tool").
	Name string `json:"name,omitempty"`
}

// ToolDefinition is a tool the Caller offers to the model. Parameters is an
// opaque JSON Schema object that the Membrane does not interpret.
type ToolDefinition struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters,omitempty"`
}

// ToolCall is a model-emitted request to invoke a tool. Arguments is an opaque
// JSON object that the Membrane does not interpret.
type ToolCall struct {
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments,omitempty"`
}

// Usage holds token accounting for a request and its response.
type Usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

// StreamChunk is one incremental streaming unit on the inward path. It carries
// normalized token text and stop metadata, and no provider-identifying fields.
type StreamChunk struct {
	// Delta is the incremental token text for this chunk.
	Delta string
	// FinishReason is set, normalized, on the final content chunk.
	FinishReason string
	// ToolCalls carries incremental tool-call fragments. The first fragment for
	// an Index carries ID and Name; later ones carry ArgumentsFragment pieces
	// for the Caller to reassemble.
	ToolCalls []ToolCallDelta
	// Done marks the end of the stream.
	Done bool
}

// ToolCallDelta is one incremental tool-call fragment on the streaming path.
// Fragments with the same Index belong to one tool call: ID and Name arrive
// once and ArgumentsFragment accumulates in order.
type ToolCallDelta struct {
	// Index is the 0-based position of the tool call within the assistant
	// turn, so parallel calls can be reassembled independently.
	Index int
	// ID is the provider's tool-call ID, set on the first fragment.
	ID string
	// Name is the tool name, set on the first fragment.
	Name string
	// ArgumentsFragment is the next piece of the arguments JSON string.
	ArgumentsFragment string
}

// CanonicalRequest is the inference request on the Interior, before the Scrub.
// It may carry Interior control state such as Reveal and the Caller, and must
// not cross the Membrane.
type CanonicalRequest struct {
	PathwayID PathwayID
	// Payload is the full inbound payload. The Scrub drops any key not in the
	// outbound Permeability allowlist.
	Payload map[string]any
	// Reveal opts in to provider disclosure. It is consumed on the Interior.
	Reveal bool
	// Caller is the authenticated Caller context.
	Caller Caller
}

// CanonicalResponse is the chat response assembled on the inward path.
type CanonicalResponse struct {
	PathwayID    PathwayID
	Message      Message
	FinishReason string
	Usage        Usage
	// Provider and Model are set only when the Caller requested Reveal.
	Provider ProviderID
	Model    string
}

// Target is the resolved egress target: the Provider and model to call. It is
// attached at the Membrane, on the Exterior side.
type Target struct {
	Provider ProviderID
	Model    string
}
