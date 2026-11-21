package bedrock

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/inferencepath/membrane/internal/exterior/providers"
	"github.com/inferencepath/membrane/internal/types"
)

var bedrockImageFormats = map[string]struct{}{
	"png": {}, "jpeg": {}, "gif": {}, "webp": {},
}

// CompleteMultimodal implements providers.MultimodalProvider with Bedrock
// Converse. Text and image content use the model-neutral Converse blocks, so
// any configured Bedrock model that supports image input can handle the same
// request shape. Bedrock model capability and account access remain upstream
// concerns; audio is not a universally supported Converse input block and is
// rejected before egress.
func (a *Adapter) CompleteMultimodal(ctx context.Context, req providers.Request, id providers.EgressIdentity) (types.CanonicalMultimodal, error) {
	if _, ok := req.Payload["audio_output"]; ok {
		return types.CanonicalMultimodal{}, fmt.Errorf("bedrock: spoken reply: %w", providers.ErrFormatUnsupported)
	}
	msgs, _ := req.Payload["messages"].([]types.MultimodalMessage)
	body, err := buildMultimodalBody(req, msgs)
	if err != nil {
		return types.CanonicalMultimodal{}, err
	}
	httpReq, err := a.authRequest(ctx, a.converseURL(req.Model), body, id)
	if err != nil {
		return types.CanonicalMultimodal{}, err
	}
	resp, err := a.client.Do(httpReq)
	if err != nil {
		return types.CanonicalMultimodal{}, mapErr(ctx, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode/100 != 2 {
		return types.CanonicalMultimodal{}, providers.UpstreamStatusError{Status: resp.StatusCode, Provider: a.id}
	}

	var cr converseResponse
	if err := json.NewDecoder(resp.Body).Decode(&cr); err != nil {
		return types.CanonicalMultimodal{}, fmt.Errorf("bedrock: decode response: %w", err)
	}
	var text strings.Builder
	for _, part := range cr.Output.Message.Content {
		text.WriteString(part.Text)
	}
	return types.CanonicalMultimodal{
		Text:         text.String(),
		FinishReason: cr.StopReason,
		Usage: types.Usage{
			PromptTokens:     cr.Usage.InputTokens,
			CompletionTokens: cr.Usage.OutputTokens,
			TotalTokens:      cr.Usage.InputTokens + cr.Usage.OutputTokens,
		},
		Provider: a.id,
		Model:    req.Model,
	}, nil
}

func buildMultimodalBody(req providers.Request, msgs []types.MultimodalMessage) ([]byte, error) {
	system := make([]map[string]any, 0)
	messages := make([]map[string]any, 0, len(msgs))
	for _, msg := range msgs {
		parts := make([]any, 0, len(msg.Parts))
		for _, part := range msg.Parts {
			switch part.Type {
			case types.PartText:
				parts = append(parts, map[string]any{"text": part.Text})
			case types.PartImage:
				if msg.Role != "user" {
					return nil, fmt.Errorf("bedrock: image on %s turn: %w", msg.Role, providers.ErrFormatUnsupported)
				}
				if _, ok := bedrockImageFormats[part.Format]; !ok {
					return nil, fmt.Errorf("bedrock: %q image: %w", part.Format, providers.ErrFormatUnsupported)
				}
				parts = append(parts, map[string]any{"image": map[string]any{
					"format": part.Format,
					"source": map[string]any{"bytes": part.Data},
				}})
			case types.PartAudio:
				return nil, fmt.Errorf("bedrock: audio input: %w", providers.ErrFormatUnsupported)
			default:
				return nil, fmt.Errorf("bedrock: %q media: %w", part.Type, providers.ErrFormatUnsupported)
			}
		}
		if msg.Role == "system" {
			for _, part := range parts {
				text, _ := part.(map[string]any)["text"].(string)
				system = append(system, map[string]any{"text": text})
			}
			continue
		}
		role := msg.Role
		if role != "assistant" {
			role = "user"
		}
		messages = append(messages, map[string]any{"role": role, "content": parts})
	}

	inference := map[string]any{"maxTokens": defaultMaxTokens}
	if v, ok := asInt(req.Payload["max_tokens"]); ok {
		inference["maxTokens"] = v
	}
	if v, ok := req.Payload["temperature"]; ok {
		inference["temperature"] = v
	}
	body := map[string]any{"messages": messages, "inferenceConfig": inference}
	if len(system) > 0 {
		body["system"] = system
	}
	return json.Marshal(body)
}
