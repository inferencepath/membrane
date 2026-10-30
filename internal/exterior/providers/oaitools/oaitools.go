// Package oaitools maps the Membrane's provider-neutral tool shapes to and from
// the OpenAI Chat Completions wire format. The OpenAI and Azure OpenAI adapters
// share it. It sees only the allowlisted payload and the decoded response, never
// Caller identity, the Caller Key, or the Reveal flag.
//
// Canonical (neutral) shapes:
//   - tools:      [{name, description?, parameters?}]
//   - tool_calls: [{id, name, arguments?}]   (on an assistant message)
//
// OpenAI wire shapes:
//   - tools:      [{type:"function", function:{name, description?, parameters?}}]
//   - tool_calls: [{id, type:"function", function:{name, arguments:"<json string>"}}]
package oaitools

import (
	"encoding/json"

	"github.com/inferencepath/membrane/internal/types"
)

// Tools converts the neutral tools payload value into the OpenAI tools shape.
// Unexpected shapes are returned unchanged and left to the Provider to reject.
func Tools(raw any) any {
	arr, ok := raw.([]any)
	if !ok {
		return raw
	}
	out := make([]any, 0, len(arr))
	for _, e := range arr {
		m, ok := e.(map[string]any)
		if !ok {
			out = append(out, e)
			continue
		}
		fn := map[string]any{"name": m["name"]}
		if d, ok := m["description"]; ok {
			fn["description"] = d
		}
		if p, ok := m["parameters"]; ok {
			fn["parameters"] = p
		}
		out = append(out, map[string]any{"type": "function", "function": fn})
	}
	return out
}

// Messages converts any assistant message carrying neutral tool_calls into the
// OpenAI tool_calls shape. Messages without tool_calls pass through unchanged.
func Messages(raw any) any {
	arr, ok := raw.([]any)
	if !ok {
		return raw
	}
	out := make([]any, 0, len(arr))
	for _, e := range arr {
		m, ok := e.(map[string]any)
		if !ok {
			out = append(out, e)
			continue
		}
		tc, has := m["tool_calls"]
		if !has {
			out = append(out, e)
			continue
		}
		nm := make(map[string]any, len(m))
		for k, v := range m {
			if k != "tool_calls" {
				nm[k] = v
			}
		}
		nm["tool_calls"] = toolCalls(tc)
		out = append(out, nm)
	}
	return out
}

func toolCalls(raw any) any {
	arr, ok := raw.([]any)
	if !ok {
		return raw
	}
	out := make([]any, 0, len(arr))
	for _, e := range arr {
		m, ok := e.(map[string]any)
		if !ok {
			out = append(out, e)
			continue
		}
		fn := map[string]any{"name": m["name"]}
		if args, ok := m["arguments"]; ok {
			fn["arguments"] = rawToString(args)
		}
		out = append(out, map[string]any{"id": m["id"], "type": "function", "function": fn})
	}
	return out
}

// rawToString renders an opaque arguments value as the JSON string OpenAI
// expects for tool_call function arguments.
func rawToString(v any) string {
	switch x := v.(type) {
	case json.RawMessage:
		return string(x)
	case string:
		return x
	case []byte:
		return string(x)
	default:
		b, _ := json.Marshal(x)
		return string(b)
	}
}

// WireToolCall is the OpenAI response tool_call shape, embedded by the adapters'
// response message struct.
type WireToolCall struct {
	ID       string `json:"id"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

// WireToolCallDelta is the OpenAI streaming delta.tool_calls fragment: the first
// fragment for an index carries id/name, later fragments carry argument slices.
type WireToolCallDelta struct {
	Index    int    `json:"index"`
	ID       string `json:"id"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

// DeltaToCanonical converts streamed OpenAI delta.tool_calls fragments into
// provider-neutral tool-call deltas. The Caller side reassembles them by index.
func DeltaToCanonical(ds []WireToolCallDelta) []types.ToolCallDelta {
	if len(ds) == 0 {
		return nil
	}
	out := make([]types.ToolCallDelta, 0, len(ds))
	for _, d := range ds {
		out = append(out, types.ToolCallDelta{
			Index:             d.Index,
			ID:                d.ID,
			Name:              d.Function.Name,
			ArgumentsFragment: d.Function.Arguments,
		})
	}
	return out
}

// ToCanonical converts decoded OpenAI response tool_calls to neutral ToolCalls.
func ToCanonical(tcs []WireToolCall) []types.ToolCall {
	if len(tcs) == 0 {
		return nil
	}
	out := make([]types.ToolCall, 0, len(tcs))
	for _, tc := range tcs {
		c := types.ToolCall{ID: tc.ID, Name: tc.Function.Name}
		if tc.Function.Arguments != "" {
			c.Arguments = json.RawMessage(tc.Function.Arguments)
		}
		out = append(out, c)
	}
	return out
}
