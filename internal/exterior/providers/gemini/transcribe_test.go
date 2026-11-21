package gemini_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/inferencepath/membrane/internal/exterior/providers"
	"github.com/inferencepath/membrane/internal/exterior/providers/gemini"
	"github.com/inferencepath/membrane/internal/types"
)

func TestTranscribe(t *testing.T) {
	var gotPath string
	var gotBody map[string]any
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &gotBody)
		_, _ = io.WriteString(w, `{"candidates":[{"content":{"parts":[{"text":"thinking","thought":true},{"text":" Hello Maria. \n"}]}}]}`)
	}))
	defer ts.Close()

	a := gemini.New(ts.URL, 0)
	res, err := a.Transcribe(context.Background(), providers.Request{
		Model: "gemini-3.8-flash",
		Payload: map[string]any{
			"file":     types.AudioUpload{Ext: "mp3", Data: []byte{1, 2, 3}},
			"language": "en", "prompt": "names: Maria", "temperature": "0.2", "response_format": "verbose_json",
		},
	}, providers.EgressIdentity{Secret: "k"})
	if err != nil {
		t.Fatalf("Transcribe: %v", err)
	}
	if gotPath != "/v1beta/models/gemini-3.8-flash:generateContent" {
		t.Errorf("path = %q", gotPath)
	}
	parts := gotBody["contents"].([]any)[0].(map[string]any)["parts"].([]any)
	instr := parts[0].(map[string]any)["text"].(string)
	if !strings.Contains(instr, "language code en") || !strings.Contains(instr, "names: Maria") {
		t.Errorf("instruction = %q", instr)
	}
	inline := parts[1].(map[string]any)["inlineData"].(map[string]any)
	if inline["mimeType"] != "audio/mp3" || inline["data"] != "AQID" {
		t.Errorf("inlineData = %v", inline)
	}
	if gotBody["generationConfig"].(map[string]any)["temperature"] != 0.2 {
		t.Errorf("generationConfig = %v", gotBody["generationConfig"])
	}
	if strings.Contains(string(mustJSON(gotBody)), "verbose_json") {
		t.Error("response_format must not be sent")
	}
	if res.Text != "Hello Maria." || res.Model != "gemini-3.8-flash" {
		t.Errorf("result = %+v", res)
	}
}

func TestTranscribeNoAudioFailsWithoutCall(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("no request expected without audio")
	}))
	defer ts.Close()
	a := gemini.New(ts.URL, 0)
	if _, err := a.Transcribe(context.Background(), providers.Request{Model: "m", Payload: map[string]any{}}, providers.EgressIdentity{Secret: "k"}); err == nil {
		t.Fatal("want error without audio")
	}
}

func mustJSON(v any) []byte {
	b, _ := json.Marshal(v)
	return b
}
