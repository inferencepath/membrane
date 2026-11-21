package azureopenai

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/inferencepath/membrane/internal/exterior/providers"
	"github.com/inferencepath/membrane/internal/types"
)

// audioAPIVersion is the api-version for chat completions with audio input or
// output. Azure serves input_audio and the audio modality only on preview
// api-versions.
const audioAPIVersion = "2025-01-01-preview"

var multimodalOutFormats = map[string]string{
	"wav": "wav", "mp3": "mp3", "flac": "flac", "opus": "opus", "aac": "aac", "pcm": "pcm16",
}

type multimodalResponse struct {
	Choices []struct {
		Message struct {
			Content *string `json:"content"`
			Audio   *struct {
				Data       string `json:"data"`
				Transcript string `json:"transcript"`
			} `json:"audio"`
		} `json:"message"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Usage struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
	} `json:"usage"`
}

// CompleteMultimodal implements providers.MultimodalProvider over the
// deployment's Chat Completions endpoint: text parts become text content,
// images become data-URL image_url parts, and audio becomes input_audio.
// Earlier assistant turns are sent as text. A spoken reply is requested with
// modalities and audio.
func (a *Adapter) CompleteMultimodal(ctx context.Context, req providers.Request, id providers.EgressIdentity) (types.CanonicalMultimodal, error) {
	msgs, _ := req.Payload["messages"].([]types.MultimodalMessage)
	usesAudio := false
	out := make([]any, 0, len(msgs))
	for _, m := range msgs {
		if m.Role != "user" {
			out = append(out, map[string]any{"role": m.Role, "content": joinText(m.Parts)})
			continue
		}
		parts := make([]any, 0, len(m.Parts))
		for _, p := range m.Parts {
			switch p.Type {
			case types.PartText:
				parts = append(parts, map[string]any{"type": "text", "text": p.Text})
			case types.PartImage:
				url := "data:image/" + p.Format + ";base64," + base64.StdEncoding.EncodeToString(p.Data)
				parts = append(parts, map[string]any{"type": "image_url", "image_url": map[string]any{"url": url}})
			case types.PartAudio:
				usesAudio = true
				parts = append(parts, map[string]any{"type": "input_audio", "input_audio": map[string]any{
					"data": base64.StdEncoding.EncodeToString(p.Data), "format": p.Format,
				}})
			}
		}
		out = append(out, map[string]any{"role": "user", "content": parts})
	}

	body := map[string]any{"messages": out}
	if v, ok := req.Payload["max_tokens"]; ok {
		body["max_completion_tokens"] = v
	}
	if v, ok := req.Payload["temperature"]; ok {
		body["temperature"] = v
	}
	var replyFormat string
	if ao, ok := req.Payload["audio_output"].(types.MultimodalAudioOutput); ok {
		f, known := multimodalOutFormats[ao.Format]
		if !known {
			return types.CanonicalMultimodal{}, fmt.Errorf("azureopenai: %q reply audio: %w", ao.Format, providers.ErrFormatUnsupported)
		}
		voice := ao.Voice
		if voice == "" {
			voice = "alloy"
		}
		body["modalities"] = []string{"text", "audio"}
		body["audio"] = map[string]any{"voice": voice, "format": f}
		replyFormat = ao.Format
		usesAudio = true
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return types.CanonicalMultimodal{}, fmt.Errorf("azureopenai: marshal request: %w", err)
	}

	endpoint := a.opEndpoint("/chat/completions")
	if usesAudio {
		endpoint = a.opEndpointVersion("/chat/completions", audioAPIVersion)
	}
	httpReq, err := a.newJSONRequestURL(ctx, endpoint, raw, id, "application/json")
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

	var oa multimodalResponse
	if err := json.NewDecoder(resp.Body).Decode(&oa); err != nil {
		return types.CanonicalMultimodal{}, fmt.Errorf("azureopenai: decode response: %w", err)
	}
	if len(oa.Choices) == 0 {
		return types.CanonicalMultimodal{}, fmt.Errorf("azureopenai: response carried no choices")
	}
	c := oa.Choices[0]
	res := types.CanonicalMultimodal{
		FinishReason: c.FinishReason,
		Usage:        types.Usage{PromptTokens: oa.Usage.PromptTokens, CompletionTokens: oa.Usage.CompletionTokens, TotalTokens: oa.Usage.PromptTokens + oa.Usage.CompletionTokens},
		Provider:     a.id,
		Model:        req.Model,
	}
	if c.Message.Content != nil {
		res.Text = *c.Message.Content
	}
	if c.Message.Audio != nil && c.Message.Audio.Data != "" {
		audio, err := base64.StdEncoding.DecodeString(c.Message.Audio.Data)
		if err != nil {
			return types.CanonicalMultimodal{}, fmt.Errorf("azureopenai: decode audio: %w", err)
		}
		res.Audio = audio
		res.AudioFormat = replyFormat
		res.Transcript = c.Message.Audio.Transcript
	}
	return res, nil
}

func joinText(parts []types.ContentPart) string {
	var b strings.Builder
	for _, p := range parts {
		if p.Type != types.PartText {
			continue
		}
		if b.Len() > 0 {
			b.WriteString("\n")
		}
		b.WriteString(p.Text)
	}
	return b.String()
}
