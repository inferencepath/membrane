package speech_test

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/inferencepath/membrane/internal/server/speech"
	fakeoa "github.com/inferencepath/membrane/test/fakes/openai"
	"github.com/inferencepath/membrane/test/harness"
)

func post(t *testing.T, url, key, body string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, url, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func serve(t *testing.T, cfg fakeoa.Config) *httptest.Server {
	t.Helper()
	t.Setenv(harness.EgressEnv, harness.EgressToken)
	fake := fakeoa.New(cfg)
	t.Cleanup(fake.Close)
	h, err := harness.BuildSpeech(fake.URL(), nil)
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(h)
	t.Cleanup(ts.Close)
	return ts
}

func TestSpeechSuccessBinaryNeutral(t *testing.T) {
	audio := []byte{0x49, 0x44, 0x33, 0x04, 0x00} // fake "ID3" mp3 header-ish
	ts := serve(t, fakeoa.Config{Model: "backing", SpeechAudio: audio, SpeechContentType: "audio/mpeg"})
	resp := post(t, ts.URL+speech.Route, harness.CallerKey, `{"pathway":"speak","input":"hello","voice":"alloy"}`)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "audio/mpeg" {
		t.Errorf("content-type = %q, want audio/mpeg", ct)
	}
	// No reveal: provider/model headers must be absent.
	if resp.Header.Get("X-InferencePath-Provider") != "" || resp.Header.Get("X-InferencePath-Model") != "" {
		t.Errorf("provider/model headers leaked without reveal")
	}
	got, _ := io.ReadAll(resp.Body)
	if string(got) != string(audio) {
		t.Errorf("audio body = %v, want %v", got, audio)
	}
}

func TestSpeechRevealHeaders(t *testing.T) {
	ts := serve(t, fakeoa.Config{Model: "backing", SpeechAudio: []byte("x"), SpeechContentType: "audio/ogg"})
	resp := post(t, ts.URL+speech.Route, harness.CallerKey, `{"pathway":"speak","input":"hi","voice":"alloy","format":"opus","reveal":true}`)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	if resp.Header.Get("X-InferencePath-Provider") != "openai" {
		t.Errorf("reveal provider header = %q", resp.Header.Get("X-InferencePath-Provider"))
	}
	if resp.Header.Get("X-InferencePath-Model") != harness.Model {
		t.Errorf("reveal model header = %q", resp.Header.Get("X-InferencePath-Model"))
	}
	_, _ = io.ReadAll(resp.Body)
}

func TestSpeechMethodNotAllowed(t *testing.T) {
	ts := serve(t, fakeoa.Config{})
	req, _ := http.NewRequest(http.MethodGet, ts.URL+speech.Route, nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", resp.StatusCode)
	}
}

func TestSpeechMissingFields(t *testing.T) {
	ts := serve(t, fakeoa.Config{})
	for _, body := range []string{
		`{"pathway":"speak","voice":"alloy"}`, // no input
		`{"pathway":"speak","input":"hi"}`,    // no voice
		`{"input":"hi","voice":"alloy"}`,      // no pathway
	} {
		resp := post(t, ts.URL+speech.Route, harness.CallerKey, body)
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("body %s: status = %d, want 400", body, resp.StatusCode)
		}
		resp.Body.Close()
	}
}

func TestSpeechUnknownPathway(t *testing.T) {
	ts := serve(t, fakeoa.Config{})
	resp := post(t, ts.URL+speech.Route, harness.CallerKey, `{"pathway":"nope","input":"hi","voice":"alloy"}`)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
}
