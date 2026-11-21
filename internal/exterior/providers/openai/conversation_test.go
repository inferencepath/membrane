package openai_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/inferencepath/membrane/internal/exterior/providers"
	"github.com/inferencepath/membrane/internal/exterior/providers/openai"
	"github.com/inferencepath/membrane/internal/types"
)

func TestConverseMapsAudioChatRequest(t *testing.T) {
	var got map[string]any
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &got)
		_, _ = io.WriteString(w, `{"choices":[{"message":{"audio":{"data":"UkVQTFk=","transcript":"hello"}}}],"usage":{"prompt_tokens":2,"completion_tokens":3}}`)
	}))
	defer ts.Close()

	a := openai.New("openai", ts.URL, 0)
	resp, err := a.Converse(context.Background(), providers.Request{
		Model: "audio-model",
		Payload: map[string]any{
			"file":   types.AudioUpload{Ext: "wav", Data: []byte("RIFF\x00\x00\x00\x00WAVE")},
			"format": "mp3", "voice": "alloy", "instructions": "be brief",
		},
	}, providers.EgressIdentity{Secret: "test"})
	if err != nil {
		t.Fatalf("Converse: %v", err)
	}
	if string(resp.Audio) != "REPLY" || resp.Format != "mp3" || resp.Transcript != "hello" {
		t.Fatalf("response = %#v", resp)
	}
	if got["model"] != "audio-model" {
		t.Fatalf("request model = %v", got["model"])
	}
	modalities, _ := got["modalities"].([]any)
	if len(modalities) != 2 || modalities[1] != "audio" {
		t.Fatalf("request modalities = %#v", got["modalities"])
	}
}
