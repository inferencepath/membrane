package gemini

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/inferencepath/membrane/internal/exterior/providers"
	"github.com/inferencepath/membrane/internal/types"
)

// audioMIME maps the upload extensions to the MIME types Gemini accepts for
// inline audio.
var audioMIME = map[string]string{
	"wav": "audio/wav", "mp3": "audio/mp3", "mpeg": "audio/mpeg", "mpga": "audio/mpeg",
	"flac": "audio/flac", "ogg": "audio/ogg", "oga": "audio/ogg",
	"m4a": "audio/mp4", "mp4": "audio/mp4", "webm": "audio/webm",
}

const transcribeInstruction = "Transcribe the speech in this audio exactly as spoken. " +
	"Output only the transcript text, with no commentary, labels or timestamps."

// transcribeBody builds a generateContent request that asks the model for a
// verbatim transcript of the inline audio. language and prompt (vocabulary or
// context) become hints in the instruction; temperature defaults to 0. Gemini
// returns no segments, so response_format is not sent.
func transcribeBody(req providers.Request) ([]byte, error) {
	up := types.AudioUploadOf(req.Payload["file"])
	if len(up.Data) == 0 {
		return nil, fmt.Errorf("gemini: transcription request has no audio")
	}
	mime, ok := audioMIME[up.Ext]
	if !ok {
		mime = http.DetectContentType(up.Data)
		if !strings.HasPrefix(mime, "audio/") {
			mime = "audio/wav"
		}
	}
	instruction := transcribeInstruction
	if lang, _ := req.Payload["language"].(string); lang != "" {
		instruction += " The audio is in language code " + lang + "."
	}
	if p, _ := req.Payload["prompt"].(string); p != "" {
		instruction += " Context and spelling hints: " + p
	}
	temp := 0.0
	switch v := req.Payload["temperature"].(type) {
	case float64:
		temp = v
	case string:
		if f, err := strconv.ParseFloat(v, 64); err == nil {
			temp = f
		}
	}
	return json.Marshal(map[string]any{
		"contents": []any{map[string]any{"role": "user", "parts": []any{
			map[string]any{"text": instruction},
			map[string]any{"inlineData": map[string]any{"mimeType": mime, "data": base64.StdEncoding.EncodeToString(up.Data)}},
		}}},
		"generationConfig": map[string]any{"temperature": temp},
	})
}

// Transcribe implements providers.TranscriptionProvider with generateContent on
// a Gemini model that accepts audio input.
func (a *Adapter) Transcribe(ctx context.Context, req providers.Request, id providers.EgressIdentity) (types.CanonicalTranscription, error) {
	body, err := transcribeBody(req)
	if err != nil {
		return types.CanonicalTranscription{}, err
	}
	httpReq, err := a.newHTTPRequest(ctx, a.generateURL(req.Model, "generateContent", ""), body, id, false)
	if err != nil {
		return types.CanonicalTranscription{}, err
	}
	resp, err := a.client.Do(httpReq)
	if err != nil {
		return types.CanonicalTranscription{}, mapErr(ctx, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode/100 != 2 {
		return types.CanonicalTranscription{}, providers.UpstreamStatusError{Status: resp.StatusCode, Provider: a.id}
	}
	var out struct {
		Candidates []struct {
			Content struct {
				Parts []struct {
					Text    string `json:"text"`
					Thought bool   `json:"thought"`
				} `json:"parts"`
			} `json:"content"`
		} `json:"candidates"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return types.CanonicalTranscription{}, fmt.Errorf("gemini: decode response: %w", err)
	}
	var b strings.Builder
	if len(out.Candidates) > 0 {
		for _, p := range out.Candidates[0].Content.Parts {
			if !p.Thought {
				b.WriteString(p.Text)
			}
		}
	}
	return types.CanonicalTranscription{
		Text:     strings.TrimSpace(b.String()),
		Provider: a.id,
		Model:    req.Model,
	}, nil
}
