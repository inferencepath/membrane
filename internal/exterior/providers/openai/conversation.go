package openai

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/inferencepath/membrane/internal/exterior/providers"
	"github.com/inferencepath/membrane/internal/types"
)

// conversationOutFormats maps Membrane's reply formats onto the Chat
// Completions audio output formats.
var conversationOutFormats = map[string]string{
	"wav": "wav", "mp3": "mp3", "flac": "flac", "opus": "opus", "aac": "aac", "pcm": "pcm16",
}

type oaConversationResponse struct {
	Choices []struct {
		Message struct {
			Audio struct {
				Data       string `json:"data"`
				Transcript string `json:"transcript"`
			} `json:"audio"`
		} `json:"message"`
	} `json:"choices"`
	Usage struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
	} `json:"usage"`
}

// Converse implements providers.ConversationProvider with an audio-capable chat
// model (e.g. gpt-audio-1.5): the spoken request is sent as input_audio, and the
// model answers with audio plus its transcript. Only wav and mp3 input exist on
// this API; other inputs and unknown reply formats fail before egress.
func (a *Adapter) Converse(ctx context.Context, req providers.Request, id providers.EgressIdentity) (types.CanonicalConversation, error) {
	up := types.AudioUploadOf(req.Payload["file"])
	if up.Ext != "wav" && up.Ext != "mp3" {
		return types.CanonicalConversation{}, fmt.Errorf("openai: %q input audio: %w", up.Ext, providers.ErrFormatUnsupported)
	}
	format, _ := req.Payload["format"].(string)
	if format == "" {
		format = "wav"
	}
	outFormat, ok := conversationOutFormats[format]
	if !ok {
		return types.CanonicalConversation{}, fmt.Errorf("openai: %q reply audio: %w", format, providers.ErrFormatUnsupported)
	}
	voice, _ := req.Payload["voice"].(string)
	if voice == "" {
		voice = "alloy"
	}

	var messages []any
	if s, _ := req.Payload["instructions"].(string); s != "" {
		messages = append(messages, map[string]any{"role": "system", "content": s})
	}
	messages = append(messages, map[string]any{"role": "user", "content": []any{map[string]any{
		"type":        "input_audio",
		"input_audio": map[string]any{"data": base64.StdEncoding.EncodeToString(up.Data), "format": up.Ext},
	}}})
	body, err := json.Marshal(map[string]any{
		"model":      req.Model,
		"modalities": []string{"text", "audio"},
		"audio":      map[string]any{"voice": voice, "format": outFormat},
		"messages":   messages,
	})
	if err != nil {
		return types.CanonicalConversation{}, fmt.Errorf("openai: marshal request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, opURL(a.baseURL, chatCompletionsPath), bytes.NewReader(body))
	if err != nil {
		return types.CanonicalConversation{}, err
	}
	httpReq.Header.Set("Authorization", "Bearer "+id.Secret)
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "application/json")

	resp, err := a.client.Do(httpReq)
	if err != nil {
		return types.CanonicalConversation{}, mapErr(ctx, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode/100 != 2 {
		return types.CanonicalConversation{}, providers.UpstreamStatusError{Status: resp.StatusCode, Provider: a.id}
	}

	var oa oaConversationResponse
	if err := json.NewDecoder(resp.Body).Decode(&oa); err != nil {
		return types.CanonicalConversation{}, fmt.Errorf("openai: decode response: %w", err)
	}
	if len(oa.Choices) == 0 || oa.Choices[0].Message.Audio.Data == "" {
		return types.CanonicalConversation{}, fmt.Errorf("openai: response carried no audio")
	}
	audio, err := base64.StdEncoding.DecodeString(oa.Choices[0].Message.Audio.Data)
	if err != nil {
		return types.CanonicalConversation{}, fmt.Errorf("openai: decode audio: %w", err)
	}
	return types.CanonicalConversation{
		Audio:      audio,
		Format:     format,
		Transcript: oa.Choices[0].Message.Audio.Transcript,
		Usage:      types.ConversationUsage{InputTokens: oa.Usage.PromptTokens, OutputTokens: oa.Usage.CompletionTokens},
		Provider:   a.id,
		Model:      req.Model,
	}, nil
}
