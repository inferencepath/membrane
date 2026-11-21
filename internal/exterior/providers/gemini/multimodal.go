package gemini

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/inferencepath/membrane/internal/exterior/providers"
	"github.com/inferencepath/membrane/internal/types"
)

var multimodalMime = map[string]string{
	"png": "image/png", "jpeg": "image/jpeg", "gif": "image/gif", "webp": "image/webp",
	"wav": "audio/wav", "mp3": "audio/mp3",
}

// CompleteMultimodal implements providers.MultimodalProvider over
// generateContent: text parts map to text, images and audio to inlineData, and
// system turns to systemInstruction. Gemini chat models answer in text only, so
// a spoken reply request fails before egress and the pool moves on.
func (a *Adapter) CompleteMultimodal(ctx context.Context, req providers.Request, id providers.EgressIdentity) (types.CanonicalMultimodal, error) {
	if _, ok := req.Payload["audio_output"]; ok {
		return types.CanonicalMultimodal{}, fmt.Errorf("gemini: spoken reply: %w", providers.ErrFormatUnsupported)
	}
	msgs, _ := req.Payload["messages"].([]types.MultimodalMessage)
	var system []string
	contents := make([]any, 0, len(msgs))
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
			role = "model"
		}
		parts := make([]any, 0, len(m.Parts))
		for _, p := range m.Parts {
			if p.Type == types.PartText {
				parts = append(parts, map[string]any{"text": p.Text})
				continue
			}
			mime, ok := multimodalMime[p.Format]
			if !ok {
				return types.CanonicalMultimodal{}, fmt.Errorf("gemini: %q media: %w", p.Format, providers.ErrFormatUnsupported)
			}
			parts = append(parts, map[string]any{"inlineData": map[string]any{
				"mimeType": mime, "data": base64.StdEncoding.EncodeToString(p.Data),
			}})
		}
		contents = append(contents, map[string]any{"role": role, "parts": parts})
	}

	body := map[string]any{"contents": contents}
	if len(system) > 0 {
		body["systemInstruction"] = map[string]any{"parts": []any{map[string]any{"text": strings.Join(system, "\n")}}}
	}
	gen := map[string]any{}
	if v, ok := asInt(req.Payload["max_tokens"]); ok {
		gen["maxOutputTokens"] = v
	}
	if v, ok := req.Payload["temperature"]; ok {
		gen["temperature"] = v
	}
	if len(gen) > 0 {
		body["generationConfig"] = gen
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return types.CanonicalMultimodal{}, fmt.Errorf("gemini: marshal request: %w", err)
	}

	httpReq, err := a.newHTTPRequest(ctx, a.generateURL(req.Model, "generateContent", ""), raw, id, false)
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

	var gr generateResponse
	if err := json.NewDecoder(resp.Body).Decode(&gr); err != nil {
		return types.CanonicalMultimodal{}, fmt.Errorf("gemini: decode response: %w", err)
	}
	var text strings.Builder
	var finish string
	if len(gr.Candidates) > 0 {
		finish = gr.Candidates[0].FinishReason
		for _, p := range gr.Candidates[0].Content.Parts {
			text.WriteString(p.Text)
		}
	}
	return types.CanonicalMultimodal{
		Text:         text.String(),
		FinishReason: normalizeFinish(finish, false),
		Usage: types.Usage{
			PromptTokens:     gr.UsageMetadata.PromptTokenCount,
			CompletionTokens: gr.UsageMetadata.CandidatesTokenCount,
			TotalTokens:      gr.UsageMetadata.TotalTokenCount,
		},
		Provider: a.id,
		Model:    req.Model,
	}, nil
}
