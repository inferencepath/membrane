package anthropic

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/inferencepath/membrane/internal/exterior/providers"
	"github.com/inferencepath/membrane/internal/types"
)

var imageMime = map[string]string{
	"png": "image/png", "jpeg": "image/jpeg", "gif": "image/gif", "webp": "image/webp",
}

// CompleteMultimodal implements providers.MultimodalProvider over the Messages
// API: text parts map to text blocks, images to base64 image blocks, and system
// turns to the top-level system field. Claude takes no audio in or out, so audio
// input or a spoken reply request fails before egress and the pool moves on.
func (a *Adapter) CompleteMultimodal(ctx context.Context, req providers.Request, id providers.EgressIdentity) (types.CanonicalMultimodal, error) {
	if _, ok := req.Payload["audio_output"]; ok {
		return types.CanonicalMultimodal{}, fmt.Errorf("anthropic: spoken reply: %w", providers.ErrFormatUnsupported)
	}
	msgs, _ := req.Payload["messages"].([]types.MultimodalMessage)
	var system []string
	messages := make([]any, 0, len(msgs))
	for _, m := range msgs {
		if m.Role == "system" {
			for _, p := range m.Parts {
				if p.Type == types.PartText {
					system = append(system, p.Text)
				}
			}
			continue
		}
		role := "user"
		if m.Role == "assistant" {
			role = m.Role
		}
		blocks := make([]any, 0, len(m.Parts))
		for _, p := range m.Parts {
			if p.Type == types.PartText {
				blocks = append(blocks, map[string]any{"type": "text", "text": p.Text})
				continue
			}
			mime, ok := imageMime[p.Format]
			if p.Type != types.PartImage || !ok {
				return types.CanonicalMultimodal{}, fmt.Errorf("anthropic: %q media: %w", p.Format, providers.ErrFormatUnsupported)
			}
			blocks = append(blocks, map[string]any{"type": "image", "source": map[string]any{
				"type": "base64", "media_type": mime, "data": base64.StdEncoding.EncodeToString(p.Data),
			}})
		}
		messages = append(messages, map[string]any{"role": role, "content": blocks})
	}

	body := map[string]any{"model": req.Model, "messages": messages, "max_tokens": defaultMaxTokens}
	if len(system) > 0 {
		body["system"] = strings.Join(system, "\n")
	}
	if v, ok := req.Payload["max_tokens"]; ok {
		body["max_tokens"] = v
	}
	if v, ok := req.Payload["temperature"]; ok {
		body["temperature"] = v
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return types.CanonicalMultimodal{}, fmt.Errorf("anthropic: marshal request: %w", err)
	}

	httpReq, err := a.newHTTPRequest(ctx, raw, id, false)
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

	var mr messagesResponse
	if err := json.NewDecoder(resp.Body).Decode(&mr); err != nil {
		return types.CanonicalMultimodal{}, fmt.Errorf("anthropic: decode response: %w", err)
	}
	var text strings.Builder
	for _, c := range mr.Content {
		if c.Type == "text" {
			text.WriteString(c.Text)
		}
	}
	return types.CanonicalMultimodal{
		Text:         text.String(),
		FinishReason: normalizeStopReason(mr.StopReason),
		Usage:        types.Usage{PromptTokens: mr.Usage.InputTokens, CompletionTokens: mr.Usage.OutputTokens, TotalTokens: mr.Usage.InputTokens + mr.Usage.OutputTokens},
		Provider:     a.id,
		Model:        req.Model,
	}, nil
}

func normalizeStopReason(reason string) string {
	if reason == "tool_use" {
		return "tool_calls"
	}
	return reason
}

// chatContent maps OpenAI-style content parts on a chat turn to Anthropic
// blocks: image_url data URLs become base64 image blocks and http(s) URLs become
// url image blocks. String content and other parts pass through unchanged.
func chatContent(content any) any {
	parts, ok := content.([]any)
	if !ok {
		return content
	}
	out := make([]any, 0, len(parts))
	for _, p := range parts {
		pm, ok := p.(map[string]any)
		if !ok || pm["type"] != "image_url" {
			out = append(out, p)
			continue
		}
		var url string
		switch iu := pm["image_url"].(type) {
		case string:
			url = iu
		case map[string]any:
			url, _ = iu["url"].(string)
		}
		if mime, data, ok := strings.Cut(strings.TrimPrefix(url, "data:"), ";base64,"); ok && strings.HasPrefix(url, "data:") {
			out = append(out, map[string]any{"type": "image", "source": map[string]any{"type": "base64", "media_type": mime, "data": data}})
			continue
		}
		out = append(out, map[string]any{"type": "image", "source": map[string]any{"type": "url", "url": url}})
	}
	return out
}
