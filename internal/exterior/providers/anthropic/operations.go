// Anthropic has no /responses endpoint, so the Responses operation is served
// through the Messages API: input maps to messages, instructions to system, and
// max_output_tokens to max_tokens, and the reply is mapped back to the canonical
// Responses shape.

package anthropic

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/inferencepath/membrane/internal/exterior/providers"
	"github.com/inferencepath/membrane/internal/exterior/providers/streamhelper"
	"github.com/inferencepath/membrane/internal/types"
)

// buildResponsesBody translates an allowlisted responses payload into a Messages
// API request body.
func (a *Adapter) buildResponsesBody(req providers.Request, stream bool) ([]byte, error) {
	body := make(map[string]any, len(req.Payload)+3)
	for k, v := range req.Payload {
		switch k {
		case "input", "instructions", "max_output_tokens", "stream":
			// Translated below.
		default:
			body[k] = v
		}
	}
	if in, ok := req.Payload["input"]; ok {
		switch t := in.(type) {
		case string:
			body["messages"] = []any{map[string]any{"role": "user", "content": t}}
		default:
			// A structured message array passes through unchanged.
			body["messages"] = t
		}
	}
	if instr, ok := req.Payload["instructions"]; ok {
		body["system"] = instr
	}
	if mot, ok := req.Payload["max_output_tokens"]; ok {
		body["max_tokens"] = mot
	}
	body["model"] = req.Model
	if _, ok := body["max_tokens"]; !ok {
		body["max_tokens"] = defaultMaxTokens
	}
	if stream {
		body["stream"] = true
	}
	return json.Marshal(body)
}

// Responses implements providers.ResponsesProvider. The Messages reply becomes a
// single output message item.
func (a *Adapter) Responses(ctx context.Context, req providers.Request, id providers.EgressIdentity) (types.CanonicalResponses, error) {
	body, err := a.buildResponsesBody(req, false)
	if err != nil {
		return types.CanonicalResponses{}, fmt.Errorf("anthropic: marshal request: %w", err)
	}
	httpReq, err := a.newHTTPRequest(ctx, body, id, false)
	if err != nil {
		return types.CanonicalResponses{}, err
	}
	resp, err := a.client.Do(httpReq)
	if err != nil {
		return types.CanonicalResponses{}, mapErr(ctx, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode/100 != 2 {
		return types.CanonicalResponses{}, providers.UpstreamStatusError{Status: resp.StatusCode, Provider: a.id}
	}

	var mr messagesResponse
	if err := json.NewDecoder(resp.Body).Decode(&mr); err != nil {
		return types.CanonicalResponses{}, fmt.Errorf("anthropic: decode response: %w", err)
	}
	var text string
	for _, c := range mr.Content {
		if c.Type == "text" {
			text += c.Text
		}
	}
	model := mr.Model
	if model == "" {
		model = req.Model
	}
	outputItem := map[string]any{
		"type": "message",
		"role": "assistant",
		"content": []any{
			map[string]any{"type": "output_text", "text": text},
		},
	}
	return types.CanonicalResponses{
		Output:       []map[string]any{outputItem},
		FinishReason: mr.StopReason,
		Usage: types.ResponsesUsage{
			InputTokens:  mr.Usage.InputTokens,
			OutputTokens: mr.Usage.OutputTokens,
			TotalTokens:  mr.Usage.InputTokens + mr.Usage.OutputTokens,
		},
		Provider: a.id,
		Model:    model,
	}, nil
}

// ResponsesStream implements providers.ResponsesProvider. Text deltas are
// relayed as response.output_text.delta chunks; message_stop or a clean EOF ends
// the stream.
func (a *Adapter) ResponsesStream(ctx context.Context, req providers.Request, id providers.EgressIdentity, emit func(types.ResponsesStreamChunk) error) error {
	body, err := a.buildResponsesBody(req, true)
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

	perr := streamhelper.PumpSSE(streamCtx, cancel, resp.Body, a.idleTimeout, func(data string) (bool, error) {
		var ev anthropicStreamEvent
		if json.Unmarshal([]byte(data), &ev) != nil {
			return false, nil
		}
		switch ev.Type {
		case "content_block_delta":
			if ev.Delta.Text != "" {
				if emitErr := emit(types.ResponsesStreamChunk{Type: "response.output_text.delta", Delta: ev.Delta.Text}); emitErr != nil {
					return false, emitErr
				}
			}
		case "message_stop":
			return true, nil
		}
		return false, nil
	})
	if perr != nil {
		return perr
	}
	return emit(types.ResponsesStreamChunk{Done: true})
}
