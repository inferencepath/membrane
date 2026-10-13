package types

import "encoding/json"

// Message is a single, provider-agnostic conversation turn. The tool fields are
// additive and omitempty: a plain text turn serializes exactly as before.
type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
	// ToolCalls is set on an assistant turn that invokes one or more tools. It is
	// provider-neutral and carries no provider identity.
	ToolCalls []ToolCall `json:"tool_calls,omitempty"`
	// ToolCallID correlates a tool-result turn (role "tool") to a prior ToolCall.
	ToolCallID string `json:"tool_call_id,omitempty"`
	// Name is the tool name on a tool-result turn (role "tool").
	Name string `json:"name,omitempty"`
}

// ToolDefinition is a provider-agnostic tool the Caller offers to the model.
// Parameters is an opaque JSON Schema object; the barrier never interprets it.
type ToolDefinition struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters,omitempty"`
}

// ToolCall is a model-emitted request to invoke a tool. Arguments is opaque JSON
// (an object); the barrier never interprets it.
type ToolCall struct {
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments,omitempty"`
}

// Usage holds token accounting for a request/response.
type Usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

// StreamChunk is a provider-agnostic incremental streaming unit on the inward
// path. It carries only normalized token text and stop metadata — never any
// provider-identifying fields.
type StreamChunk struct {
	// Delta is the incremental token text for this chunk.
	Delta string
	// FinishReason is set on the terminal content chunk (normalized).
	FinishReason string
	// ToolCalls carries incremental tool-call fragments for this chunk. The first
	// fragment for a given Index carries ID and Name; subsequent fragments for the
	// same Index carry ArgumentsFragment pieces, reassembled by the Caller. It is
	// provider-neutral and carries no provider identity.
	ToolCalls []ToolCallDelta
	// Done marks the end of the stream.
	Done bool
}

// ToolCallDelta is one incremental tool-call fragment on the streaming path.
// Fragments for the same Index belong to the same tool call and are reassembled
// in order: ID/Name arrive once, ArgumentsFragment accumulates.
type ToolCallDelta struct {
	// Index identifies the tool call within the assistant turn (0-based), so
	// parallel calls can be reassembled independently.
	Index int
	// ID is the provider tool-call id; set on the opening fragment.
	ID string
	// Name is the tool name; set on the opening fragment.
	Name string
	// ArgumentsFragment is an incremental slice of the arguments JSON string.
	ArgumentsFragment string
}

// CanonicalRequest is the provider-agnostic inference request on the Interior,
// before the Scrub. It MAY carry Interior-only control state (the Reveal flag)
// and Caller context; it is Interior-only and MUST NOT cross the Membrane.
type CanonicalRequest struct {
	PathwayID PathwayID
	// Payload is the full inbound logical payload. Keys absent from the outbound
	// Permeability allowlist are dropped by the Scrub and never cross the Membrane.
	Payload map[string]any
	// Reveal is an Interior-only control flag (opt-in provider disclosure). It is
	// consumed on the Interior and never crosses the Membrane.
	Reveal bool
	// Caller is the authenticated caller context. Interior-only.
	Caller Caller
}

// CanonicalResponse is the provider-agnostic response assembled on the inward
// path.
type CanonicalResponse struct {
	PathwayID    PathwayID
	Message      Message
	FinishReason string
	Usage        Usage
	// Provider and Model are populated for the Caller only when the Reveal flag
	// was set; otherwise they are empty (provider-neutral).
	Provider ProviderID
	Model    string
}

// Target is the resolved egress target: which Provider and model to call. It is
// Exterior-facing information attached at/after the Membrane.
type Target struct {
	Provider ProviderID
	Model    string
}
