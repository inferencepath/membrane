// Package anthropic implements a Provider adapter for the Anthropic Messages
// API. Requests are built only from a providers.Request and the EgressIdentity,
// which is sent as the x-api-key header. The adapter has no access to Caller
// identity, the Caller Key, or the Reveal flag.
package anthropic

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/inferencepath/membrane/internal/exterior/providers"
	"github.com/inferencepath/membrane/internal/exterior/providers/streamhelper"
	"github.com/inferencepath/membrane/internal/types"
)

const (
	messagesPath = "/v1/messages"
	// apiVersion pins the Anthropic API version.
	apiVersion = "2023-06-01"
	// defaultMaxTokens is applied when the allowlisted payload omits max_tokens,
	// which the Messages API requires.
	defaultMaxTokens = 1024
	// defaultIdleTimeout bounds inactivity on a streaming response when unset.
	defaultIdleTimeout = 30 * time.Second
)

// Adapter is an Anthropic Messages API Provider adapter.
type Adapter struct {
	id          types.ProviderID
	baseURL     string
	idleTimeout time.Duration
	client      *http.Client
}

// New returns an Anthropic adapter for the given base URL. idleTimeout bounds
// inactivity on streaming responses; a value <= 0 selects the default. The
// reported ID is "anthropic".
func New(baseURL string, idleTimeout time.Duration) *Adapter {
	if idleTimeout <= 0 {
		idleTimeout = defaultIdleTimeout
	}
	return &Adapter{
		id:          "anthropic",
		baseURL:     strings.TrimRight(baseURL, "/"),
		idleTimeout: idleTimeout,
		client:      &http.Client{},
	}
}

// ID implements providers.Provider.
func (a *Adapter) ID() types.ProviderID { return a.id }

func (a *Adapter) buildBody(req providers.Request, stream bool) ([]byte, error) {
	body := make(map[string]any, len(req.Payload)+3)
	for k, v := range req.Payload {
		body[k] = v
	}
	// The Messages API requires model and max_tokens. The Scrub does not set
	// model, so it comes from the resolved target here.
	body["model"] = req.Model
	if _, ok := body["max_tokens"]; !ok {
		body["max_tokens"] = defaultMaxTokens
	}
	if stream {
		body["stream"] = true
	} else {
		delete(body, "stream")
	}
	// The Messages API rejects a "system" role inside messages, so system turns
	// move to the top-level "system" field. Tool calls and results become
	// tool_use and tool_result blocks.
	if raw, ok := body["messages"].([]any); ok {
		system, messages := anthropicMessages(raw)
		body["messages"] = messages
		if system != "" {
			body["system"] = system
		}
	}
	// Map tools and tool_choice to the Anthropic schema.
	if raw, ok := body["tools"]; ok {
		body["tools"] = anthropicTools(raw)
	}
	if raw, ok := body["tool_choice"]; ok {
		if tc := anthropicToolChoice(raw); tc != nil {
			body["tool_choice"] = tc
		} else {
			delete(body, "tool_choice")
		}
	}
	return json.Marshal(body)
}

// anthropicMessages lifts system turns to a top-level system string and rebuilds
// the remaining turns into the Anthropic shape: assistant tool_calls become
// tool_use content blocks, and a neutral tool-result turn (role "tool") becomes a
// user turn with a tool_result block. Plain turns are reduced to role and
// content.
func anthropicMessages(arr []any) (system string, messages []any) {
	var sys strings.Builder
	out := make([]any, 0, len(arr))
	for _, m := range arr {
		mm, ok := m.(map[string]any)
		if !ok {
			out = append(out, m)
			continue
		}
		role, _ := mm["role"].(string)
		switch role {
		case "system":
			text, _ := mm["content"].(string)
			if sys.Len() > 0 {
				sys.WriteString("\n\n")
			}
			sys.WriteString(text)
		case "tool":
			id, _ := mm["tool_call_id"].(string)
			text, _ := mm["content"].(string)
			out = append(out, map[string]any{
				"role": "user",
				"content": []any{map[string]any{
					"type":        "tool_result",
					"tool_use_id": id,
					"content":     text,
				}},
			})
		case "assistant":
			tcs, hasTools := mm["tool_calls"].([]any)
			if !hasTools {
				out = append(out, map[string]any{"role": "assistant", "content": mm["content"]})
				continue
			}
			content := make([]any, 0, len(tcs)+1)
			if text, _ := mm["content"].(string); text != "" {
				content = append(content, map[string]any{"type": "text", "text": text})
			}
			for _, tc := range tcs {
				tcm, ok := tc.(map[string]any)
				if !ok {
					continue
				}
				block := map[string]any{"type": "tool_use", "id": tcm["id"], "name": tcm["name"]}
				if args, ok := tcm["arguments"]; ok {
					block["input"] = args
				} else {
					block["input"] = map[string]any{}
				}
				content = append(content, block)
			}
			out = append(out, map[string]any{"role": "assistant", "content": content})
		default: // user and unknown roles
			out = append(out, map[string]any{"role": role, "content": mm["content"]})
		}
	}
	return sys.String(), out
}

// anthropicTools maps the neutral tools payload into the Anthropic tools shape
// ([{name, description?, input_schema?}]). Unexpected shapes pass through.
func anthropicTools(raw any) any {
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
		tool := map[string]any{"name": m["name"]}
		if d, ok := m["description"]; ok {
			tool["description"] = d
		}
		if p, ok := m["parameters"]; ok {
			tool["input_schema"] = p
		}
		out = append(out, tool)
	}
	return out
}

// anthropicToolChoice maps the neutral (OpenAI-style) tool_choice directive into
// the Anthropic tool_choice shape. "none" and unknown values return nil, which
// leaves the model default.
func anthropicToolChoice(raw any) map[string]any {
	var v any
	switch r := raw.(type) {
	case json.RawMessage:
		if json.Unmarshal(r, &v) != nil {
			return nil
		}
	case []byte:
		if json.Unmarshal(r, &v) != nil {
			return nil
		}
	default:
		v = raw
	}
	switch c := v.(type) {
	case string:
		switch c {
		case "auto":
			return map[string]any{"type": "auto"}
		case "required", "any":
			return map[string]any{"type": "any"}
		}
	case map[string]any:
		if fn, ok := c["function"].(map[string]any); ok {
			if name, ok := fn["name"]; ok {
				return map[string]any{"type": "tool", "name": name}
			}
		}
	}
	return nil
}

func (a *Adapter) newHTTPRequest(ctx context.Context, body []byte, id providers.EgressIdentity, stream bool) (*http.Request, error) {
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, a.baseURL+messagesPath, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	// Only the Egress Identity and content and version headers are sent: no
	// Caller identity, Caller Key, forwarding, or correlation headers.
	httpReq.Header.Set("x-api-key", id.Secret)
	httpReq.Header.Set("anthropic-version", apiVersion)
	httpReq.Header.Set("Content-Type", "application/json")
	if stream {
		httpReq.Header.Set("Accept", "text/event-stream")
	} else {
		httpReq.Header.Set("Accept", "application/json")
	}
	return httpReq, nil
}

func mapErr(ctx context.Context, err error) error {
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return providers.ErrUpstreamTimeout
	}
	return err
}

type messagesResponse struct {
	Model      string `json:"model"`
	StopReason string `json:"stop_reason"`
	Content    []struct {
		Type  string          `json:"type"`
		Text  string          `json:"text"`
		ID    string          `json:"id"`
		Name  string          `json:"name"`
		Input json.RawMessage `json:"input"`
	} `json:"content"`
	Usage struct {
		InputTokens  int `json:"input_tokens"`
		OutputTokens int `json:"output_tokens"`
	} `json:"usage"`
}

// Complete implements providers.Provider for the non-streaming path.
func (a *Adapter) Complete(ctx context.Context, req providers.Request, id providers.EgressIdentity) (types.CanonicalResponse, error) {
	body, err := a.buildBody(req, false)
	if err != nil {
		return types.CanonicalResponse{}, fmt.Errorf("anthropic: marshal request: %w", err)
	}
	httpReq, err := a.newHTTPRequest(ctx, body, id, false)
	if err != nil {
		return types.CanonicalResponse{}, err
	}

	resp, err := a.client.Do(httpReq)
	if err != nil {
		return types.CanonicalResponse{}, mapErr(ctx, err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode/100 != 2 {
		return types.CanonicalResponse{}, providers.UpstreamStatusError{Status: resp.StatusCode, Provider: a.id}
	}

	var mr messagesResponse
	if err := json.NewDecoder(resp.Body).Decode(&mr); err != nil {
		return types.CanonicalResponse{}, fmt.Errorf("anthropic: decode response: %w", err)
	}

	msg := types.Message{Role: "assistant"}
	var text strings.Builder
	for _, c := range mr.Content {
		switch c.Type {
		case "text":
			text.WriteString(c.Text)
		case "tool_use":
			msg.ToolCalls = append(msg.ToolCalls, types.ToolCall{
				ID:        c.ID,
				Name:      c.Name,
				Arguments: c.Input,
			})
		}
	}
	msg.Content = text.String()
	// Report tool use with the canonical "tool_calls" finish reason.
	finish := mr.StopReason
	if finish == "tool_use" {
		finish = "tool_calls"
	}
	return types.CanonicalResponse{
		Message:      msg,
		FinishReason: finish,
		Usage: types.Usage{
			PromptTokens:     mr.Usage.InputTokens,
			CompletionTokens: mr.Usage.OutputTokens,
			TotalTokens:      mr.Usage.InputTokens + mr.Usage.OutputTokens,
		},
		Provider: a.id,
		Model:    mr.Model,
	}, nil
}

// anthropicStreamEvent is the minimal shape of a Messages API SSE data frame. A
// tool call opens with a content_block_start (content_block.type "tool_use",
// carrying id and name) and streams its arguments as content_block_delta frames of
// type "input_json_delta" (partial_json), both keyed by the block index.
type anthropicStreamEvent struct {
	Type         string `json:"type"`
	Index        int    `json:"index"`
	ContentBlock struct {
		Type string `json:"type"`
		ID   string `json:"id"`
		Name string `json:"name"`
	} `json:"content_block"`
	Delta struct {
		Type        string `json:"type"`
		Text        string `json:"text"`
		PartialJSON string `json:"partial_json"`
		StopReason  string `json:"stop_reason"`
	} `json:"delta"`
}

// Stream implements providers.Provider for the streaming (SSE) path. tool_use
// blocks become tool-call fragments, text deltas become Delta, message_delta
// sets FinishReason, and message_stop or a clean EOF ends with Done.
func (a *Adapter) Stream(ctx context.Context, req providers.Request, id providers.EgressIdentity, emit func(types.StreamChunk) error) error {
	body, err := a.buildBody(req, true)
	if err != nil {
		return fmt.Errorf("anthropic: marshal request: %w", err)
	}

	streamCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	httpReq, err := a.newHTTPRequest(streamCtx, body, id, true)
	if err != nil {
		return err
	}
	resp, err := a.client.Do(httpReq)
	if err != nil {
		return mapErr(ctx, err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode/100 != 2 {
		return providers.UpstreamStatusError{Status: resp.StatusCode, Provider: a.id}
	}

	err = streamhelper.PumpSSE(streamCtx, cancel, resp.Body, a.idleTimeout, func(data string) (bool, error) {
		var ev anthropicStreamEvent
		if json.Unmarshal([]byte(data), &ev) != nil {
			return false, nil // skip malformed and keepalive frames
		}
		switch ev.Type {
		case "content_block_start":
			if ev.ContentBlock.Type == "tool_use" {
				chunk := types.StreamChunk{ToolCalls: []types.ToolCallDelta{{
					Index: ev.Index,
					ID:    ev.ContentBlock.ID,
					Name:  ev.ContentBlock.Name,
				}}}
				if emitErr := emit(chunk); emitErr != nil {
					return false, emitErr
				}
			}
		case "content_block_delta":
			if ev.Delta.Text != "" {
				if emitErr := emit(types.StreamChunk{Delta: ev.Delta.Text}); emitErr != nil {
					return false, emitErr
				}
			}
			if ev.Delta.PartialJSON != "" {
				chunk := types.StreamChunk{ToolCalls: []types.ToolCallDelta{{
					Index:             ev.Index,
					ArgumentsFragment: ev.Delta.PartialJSON,
				}}}
				if emitErr := emit(chunk); emitErr != nil {
					return false, emitErr
				}
			}
		case "message_delta":
			if ev.Delta.StopReason != "" {
				finish := ev.Delta.StopReason
				if finish == "tool_use" {
					finish = "tool_calls"
				}
				if emitErr := emit(types.StreamChunk{FinishReason: finish}); emitErr != nil {
					return false, emitErr
				}
			}
		case "message_stop":
			return true, nil
		}
		return false, nil
	})
	if err != nil {
		return err
	}
	return emit(types.StreamChunk{Done: true})
}
