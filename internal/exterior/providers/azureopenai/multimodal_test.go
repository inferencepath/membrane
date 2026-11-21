package azureopenai_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/inferencepath/membrane/internal/exterior/providers"
	"github.com/inferencepath/membrane/internal/exterior/providers/azureopenai"
	"github.com/inferencepath/membrane/internal/types"
)

func TestCompleteMultimodalImage(t *testing.T) {
	var gotPath, gotQuery, gotKey string
	var gotBody map[string]any
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotQuery = r.URL.Path, r.URL.RawQuery
		gotKey = r.Header.Get("api-key")
		assertNoIdentityLeak(t, r)
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &gotBody)
		_, _ = io.WriteString(w, `{"choices":[{"message":{"content":"a red square"},"finish_reason":"stop"}],"usage":{"prompt_tokens":9,"completion_tokens":3}}`)
	}))
	defer ts.Close()

	a := azureopenai.New(ts.URL, "vision", 0)
	res, err := a.CompleteMultimodal(context.Background(), providers.Request{
		Model: "gpt-4o",
		Payload: map[string]any{
			"messages": []types.MultimodalMessage{
				{Role: "system", Parts: []types.ContentPart{{Type: types.PartText, Text: "be brief"}}},
				{Role: "user", Parts: []types.ContentPart{
					{Type: types.PartText, Text: "what is this?"},
					{Type: types.PartImage, Data: []byte{1, 2, 3}, Format: "png"},
				}},
			},
			"max_tokens": 50,
		},
	}, providers.EgressIdentity{Secret: "azkey"})
	if err != nil {
		t.Fatalf("CompleteMultimodal: %v", err)
	}
	if gotPath != "/openai/deployments/vision/chat/completions" || !strings.Contains(gotQuery, "api-version=2024-10-21") {
		t.Errorf("url = %s?%s", gotPath, gotQuery)
	}
	if gotKey != "azkey" {
		t.Errorf("api-key = %q", gotKey)
	}
	if _, ok := gotBody["model"]; ok {
		t.Error("model must not be sent (deployment selects it)")
	}
	if gotBody["max_completion_tokens"] != float64(50) {
		t.Errorf("max_completion_tokens = %v", gotBody["max_completion_tokens"])
	}
	msgs := gotBody["messages"].([]any)
	user := msgs[1].(map[string]any)["content"].([]any)
	img := user[1].(map[string]any)["image_url"].(map[string]any)["url"].(string)
	if img != "data:image/png;base64,AQID" {
		t.Errorf("image url = %q", img)
	}
	if res.Text != "a red square" || res.Usage.PromptTokens != 9 || res.Usage.CompletionTokens != 3 || res.Model != "gpt-4o" {
		t.Errorf("result = %+v", res)
	}
}

func TestCompleteMultimodalAudioUsesPreviewVersion(t *testing.T) {
	var gotQuery string
	var gotBody map[string]any
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.RawQuery
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &gotBody)
		_, _ = io.WriteString(w, `{"choices":[{"message":{"content":null,"audio":{"data":"AQI=","transcript":"hello"}},"finish_reason":"stop"}]}`)
	}))
	defer ts.Close()

	a := azureopenai.New(ts.URL, "audio", 0)
	res, err := a.CompleteMultimodal(context.Background(), providers.Request{
		Model: "gpt-4o-audio-preview",
		Payload: map[string]any{
			"messages": []types.MultimodalMessage{{Role: "user", Parts: []types.ContentPart{
				{Type: types.PartAudio, Data: []byte{9}, Format: "wav"},
			}}},
			"audio_output": types.MultimodalAudioOutput{Format: "wav"},
		},
	}, providers.EgressIdentity{Secret: "azkey"})
	if err != nil {
		t.Fatalf("CompleteMultimodal: %v", err)
	}
	if !strings.Contains(gotQuery, "api-version=2025-01-01-preview") {
		t.Errorf("query = %q", gotQuery)
	}
	if gotBody["audio"].(map[string]any)["voice"] != "alloy" {
		t.Errorf("audio = %v", gotBody["audio"])
	}
	if string(res.Audio) != "\x01\x02" || res.Transcript != "hello" || res.AudioFormat != "wav" {
		t.Errorf("result = %+v", res)
	}
}

func TestCompleteMultimodalUpstreamError(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
	}))
	defer ts.Close()
	a := azureopenai.New(ts.URL, "vision", 0)
	_, err := a.CompleteMultimodal(context.Background(), providers.Request{Payload: map[string]any{}}, providers.EgressIdentity{Secret: "k"})
	if err == nil {
		t.Fatal("want error on 400")
	}
}
