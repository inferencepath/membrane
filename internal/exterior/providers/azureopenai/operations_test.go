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

// assertNoIdentityLeak fails if any caller-identity header crossed to the provider.
func assertNoIdentityLeak(t *testing.T, r *http.Request) {
	t.Helper()
	if r.Header.Get("Authorization") != "" {
		t.Error("Authorization header leaked to provider")
	}
	for _, h := range []string{"X-Caller", "X-Caller-Key", "X-Forwarded-For", "Cookie"} {
		if r.Header.Get(h) != "" {
			t.Errorf("identity header %q leaked", h)
		}
	}
}

func TestEmbeddings(t *testing.T) {
	var gotPath, gotKey string
	var gotBody map[string]any
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotKey = r.Header.Get("api-key")
		assertNoIdentityLeak(t, r)
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &gotBody)
		_, _ = io.WriteString(w, `{"model":"text-embedding-3","data":[{"index":0,"embedding":[0.1,0.2]},{"index":1,"embedding":[0.3]}],"usage":{"prompt_tokens":5,"total_tokens":5}}`)
	}))
	defer ts.Close()

	a := azureopenai.New(ts.URL, "embed-deploy", 0)
	resp, err := a.Embeddings(context.Background(), providers.Request{
		Model:   "text-embedding-3",
		Payload: map[string]any{"input": []any{"a", "b"}, "model": "should-be-dropped", "stream": true},
	}, providers.EgressIdentity{Secret: "azkey"})
	if err != nil {
		t.Fatalf("Embeddings: %v", err)
	}
	if !strings.Contains(gotPath, "/openai/deployments/embed-deploy/embeddings") {
		t.Errorf("path = %q", gotPath)
	}
	if gotKey != "azkey" {
		t.Errorf("api-key = %q", gotKey)
	}
	if _, ok := gotBody["model"]; ok {
		t.Error("model must be dropped (deployment selects it)")
	}
	if _, ok := gotBody["stream"]; ok {
		t.Error("stream must not be sent")
	}
	if len(resp.Data) != 2 || resp.Provider != "azure-openai" {
		t.Errorf("resp = %+v", resp)
	}
}

func TestResponsesUnary(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assertNoIdentityLeak(t, r)
		if !strings.Contains(r.URL.Path, "/responses") {
			t.Errorf("path = %q", r.URL.Path)
		}
		_, _ = io.WriteString(w, `{"model":"gpt","status":"completed","output":[{"type":"message"}],"usage":{"input_tokens":3,"output_tokens":4,"total_tokens":7}}`)
	}))
	defer ts.Close()

	a := azureopenai.New(ts.URL, "dep", 0)
	resp, err := a.Responses(context.Background(), providers.Request{
		Model:   "gpt",
		Payload: map[string]any{"input": "hi"},
	}, providers.EgressIdentity{Secret: "k"})
	if err != nil {
		t.Fatalf("Responses: %v", err)
	}
	if resp.FinishReason != "completed" || resp.Usage.TotalTokens != 7 || len(resp.Output) != 1 {
		t.Errorf("resp = %+v", resp)
	}
}

func TestResponsesStream(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assertNoIdentityLeak(t, r)
		fl, _ := w.(http.Flusher)
		_, _ = io.WriteString(w, "data: {\"type\":\"response.output_text.delta\",\"delta\":\"he\"}\n\n")
		if fl != nil {
			fl.Flush()
		}
		_, _ = io.WriteString(w, "data: {\"type\":\"response.output_text.delta\",\"delta\":\"llo\"}\n\n")
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
	}))
	defer ts.Close()

	a := azureopenai.New(ts.URL, "dep", 0)
	var deltas []string
	var done bool
	err := a.ResponsesStream(context.Background(), providers.Request{Model: "gpt", Payload: map[string]any{"input": "hi"}},
		providers.EgressIdentity{Secret: "k"},
		func(c types.ResponsesStreamChunk) error {
			if c.Delta != "" {
				deltas = append(deltas, c.Delta)
			}
			if c.Done {
				done = true
			}
			return nil
		})
	if err != nil {
		t.Fatalf("ResponsesStream: %v", err)
	}
	if strings.Join(deltas, "") != "hello" || !done {
		t.Errorf("deltas=%v done=%v", deltas, done)
	}
}

func TestImages(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assertNoIdentityLeak(t, r)
		if !strings.Contains(r.URL.Path, "/images/generations") {
			t.Errorf("path = %q", r.URL.Path)
		}
		_, _ = io.WriteString(w, `{"data":[{"b64_json":"QUJD"}]}`)
	}))
	defer ts.Close()

	a := azureopenai.New(ts.URL, "dep", 0)
	resp, err := a.Images(context.Background(), providers.Request{Model: "dall-e", Payload: map[string]any{"prompt": "x"}}, providers.EgressIdentity{Secret: "k"})
	if err != nil {
		t.Fatalf("Images: %v", err)
	}
	if len(resp.Data) != 1 || resp.Data[0].B64JSON != "QUJD" || resp.Usage.Images != 1 || resp.Model != "dall-e" {
		t.Errorf("resp = %+v", resp)
	}
}

func TestModerate(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assertNoIdentityLeak(t, r)
		_, _ = io.WriteString(w, `{"model":"text-moderation","results":[{"flagged":true,"categories":{"hate":true},"category_scores":{"hate":0.9}}]}`)
	}))
	defer ts.Close()

	a := azureopenai.New(ts.URL, "dep", 0)
	resp, err := a.Moderate(context.Background(), providers.Request{Model: "m", Payload: map[string]any{"input": "x"}}, providers.EgressIdentity{Secret: "k"})
	if err != nil {
		t.Fatalf("Moderate: %v", err)
	}
	if len(resp.Results) != 1 || !resp.Results[0].Flagged {
		t.Errorf("resp = %+v", resp)
	}
}

func TestTranscribe(t *testing.T) {
	var gotFilename string
	var gotCT string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assertNoIdentityLeak(t, r)
		gotCT = r.Header.Get("Content-Type")
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			t.Fatalf("parse multipart: %v", err)
		}
		if f := r.MultipartForm.File["file"]; len(f) > 0 {
			gotFilename = f[0].Filename
		}
		_, _ = io.WriteString(w, `{"text":"hello world","duration":1.5}`)
	}))
	defer ts.Close()

	a := azureopenai.New(ts.URL, "dep", 0)
	resp, err := a.Transcribe(context.Background(), providers.Request{
		Model:   "whisper",
		Payload: map[string]any{"file": []byte("RIFFDATA"), "language": "en"},
	}, providers.EgressIdentity{Secret: "k"})
	if err != nil {
		t.Fatalf("Transcribe: %v", err)
	}
	if !strings.HasPrefix(gotCT, "multipart/form-data") {
		t.Errorf("content-type = %q", gotCT)
	}
	if gotFilename != "audio" {
		t.Errorf("filename = %q, want generic 'audio' (client filename must not cross)", gotFilename)
	}
	if resp.Text != "hello world" || resp.Usage.Seconds != 1.5 {
		t.Errorf("resp = %+v", resp)
	}
}

func TestSpeech(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assertNoIdentityLeak(t, r)
		w.Header().Set("Content-Type", "audio/mpeg")
		_, _ = w.Write([]byte("AUDIOBYTES"))
	}))
	defer ts.Close()

	a := azureopenai.New(ts.URL, "dep", 0)
	var head *types.SpeechChunk
	var audio []byte
	var done bool
	err := a.Speech(context.Background(), providers.Request{Model: "tts", Payload: map[string]any{"input": "hi", "format": "mp3"}},
		providers.EgressIdentity{Secret: "k"},
		func(c types.SpeechChunk) error {
			switch {
			case c.Head:
				h := c
				head = &h
			case c.Done:
				done = true
			default:
				audio = append(audio, c.Audio...)
			}
			return nil
		})
	if err != nil {
		t.Fatalf("Speech: %v", err)
	}
	if head == nil || head.ContentType != "audio/mpeg" || head.Provider != "azure-openai" || head.Model != "tts" {
		t.Errorf("head = %+v", head)
	}
	if string(audio) != "AUDIOBYTES" || !done {
		t.Errorf("audio=%q done=%v", audio, done)
	}
}
