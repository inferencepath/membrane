package transcription_test

import (
	"bytes"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/inferencepath/membrane/internal/server/transcription"
	fakeoa "github.com/inferencepath/membrane/test/fakes/openai"
	"github.com/inferencepath/membrane/test/harness"
)

// multipartBody builds a transcription multipart body with a named file part
// and extra form fields.
func multipartBody(t *testing.T, filename string, fileBytes []byte, fields map[string]string) (*bytes.Buffer, string) {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	for k, v := range fields {
		if err := mw.WriteField(k, v); err != nil {
			t.Fatal(err)
		}
	}
	if filename != "" || fileBytes != nil {
		fw, err := mw.CreateFormFile("file", filename)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := fw.Write(fileBytes); err != nil {
			t.Fatal(err)
		}
	}
	if err := mw.Close(); err != nil {
		t.Fatal(err)
	}
	return &buf, mw.FormDataContentType()
}

func postMultipart(t *testing.T, url, key string, body *bytes.Buffer, contentType string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, url, body)
	if err != nil {
		t.Fatal(err)
	}
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	req.Header.Set("Content-Type", contentType)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

type transcriptionResponse struct {
	Pathway string `json:"pathway"`
	Text    string `json:"text"`
	Usage   struct {
		Seconds float64 `json:"seconds"`
	} `json:"usage"`
	Provider string `json:"provider"`
	Model    string `json:"model"`
}

func serve(t *testing.T, cfg fakeoa.Config, hopts ...transcription.HandlerOption) (*httptest.Server, *fakeoa.Fake) {
	t.Helper()
	t.Setenv(harness.EgressEnv, harness.EgressToken)
	fake := fakeoa.New(cfg)
	t.Cleanup(fake.Close)
	h, err := harness.BuildTranscription(fake.URL(), nil, hopts...)
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(h)
	t.Cleanup(ts.Close)
	return ts, fake
}

func TestTranscriptionSuccessNeutral(t *testing.T) {
	ts, fake := serve(t, fakeoa.Config{Model: "backing", TranscriptionText: "hello world", TranscriptionDuration: 2.5})
	body, ct := multipartBody(t, "secret-recording.wav", []byte("RIFFfakeaudio"), map[string]string{
		"pathway":  "listen",
		"language": "en",
	})
	resp := postMultipart(t, ts.URL+transcription.Route, harness.CallerKey, body, ct)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	var out transcriptionResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if out.Text != "hello world" || out.Usage.Seconds != 2.5 {
		t.Errorf("unexpected response: %+v", out)
	}
	if out.Provider != "" || out.Model != "" {
		t.Errorf("provider/model leaked without reveal")
	}
	// The client filename must not be forwarded upstream.
	if bytes.Contains(fake.Last().Body, []byte("secret-recording.wav")) {
		t.Errorf("DIFFUSION LEAK: client filename crossed the Membrane")
	}
}

func TestTranscriptionReveal(t *testing.T) {
	ts, _ := serve(t, fakeoa.Config{Model: "backing", TranscriptionText: "hi"})
	body, ct := multipartBody(t, "a.wav", []byte("audio"), map[string]string{"pathway": "listen", "reveal": "true"})
	resp := postMultipart(t, ts.URL+transcription.Route, harness.CallerKey, body, ct)
	defer resp.Body.Close()
	var out transcriptionResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if out.Provider != "openai" || out.Model != harness.Model {
		t.Errorf("reveal did not surface provider/model: %q / %q", out.Provider, out.Model)
	}
}

func TestTranscriptionMethodNotAllowed(t *testing.T) {
	ts, _ := serve(t, fakeoa.Config{})
	req, _ := http.NewRequest(http.MethodGet, ts.URL+transcription.Route, nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", resp.StatusCode)
	}
}

func TestTranscriptionMissingFile(t *testing.T) {
	ts, _ := serve(t, fakeoa.Config{})
	body, ct := multipartBody(t, "", nil, map[string]string{"pathway": "listen"})
	resp := postMultipart(t, ts.URL+transcription.Route, harness.CallerKey, body, ct)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
}

func TestTranscriptionPayloadTooLarge(t *testing.T) {
	ts, _ := serve(t, fakeoa.Config{TranscriptionText: "x"}, transcription.WithMaxUploadBytes(64))
	body, ct := multipartBody(t, "big.wav", bytes.Repeat([]byte("A"), 4096), map[string]string{"pathway": "listen"})
	resp := postMultipart(t, ts.URL+transcription.Route, harness.CallerKey, body, ct)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413", resp.StatusCode)
	}
}

func TestTranscriptionUnknownPathway(t *testing.T) {
	ts, _ := serve(t, fakeoa.Config{})
	body, ct := multipartBody(t, "a.wav", []byte("audio"), map[string]string{"pathway": "nope"})
	resp := postMultipart(t, ts.URL+transcription.Route, harness.CallerKey, body, ct)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
}
