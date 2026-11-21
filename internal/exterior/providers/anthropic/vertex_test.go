package anthropic_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/inferencepath/membrane/internal/exterior/providers"
	"github.com/inferencepath/membrane/internal/exterior/providers/anthropic"
	"github.com/inferencepath/membrane/internal/types"
)

func staticToken(tok string) func(context.Context) (string, error) {
	return func(context.Context) (string, error) { return tok, nil }
}

func TestVertexCompleteRoutesToRawPredict(t *testing.T) {
	var gotPath, gotAuth, gotAPIKey, gotVersionHdr string
	var gotBody map[string]any
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotAuth = r.URL.Path, r.Header.Get("Authorization")
		gotAPIKey, gotVersionHdr = r.Header.Get("x-api-key"), r.Header.Get("anthropic-version")
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &gotBody)
		_, _ = io.WriteString(w, `{"model":"claude-sonnet-5-5","stop_reason":"end_turn","content":[{"type":"text","text":"hi"}],"usage":{"input_tokens":4,"output_tokens":1}}`)
	}))
	defer ts.Close()

	a := anthropic.New(ts.URL, 0, anthropic.WithVertex("proj-1", "us-east5"), anthropic.WithTokenSource(staticToken("gcp-tok")))
	resp, err := a.Complete(context.Background(), providers.Request{
		Model:   "claude-sonnet-5-5",
		Payload: map[string]any{"messages": []any{map[string]any{"role": "user", "content": "hello"}}},
	}, providers.EgressIdentity{Secret: "unused"})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if want := "/v1/projects/proj-1/locations/us-east5/publishers/anthropic/models/claude-sonnet-5-5:rawPredict"; gotPath != want {
		t.Errorf("path = %q, want %q", gotPath, want)
	}
	if gotAuth != "Bearer gcp-tok" {
		t.Errorf("Authorization = %q", gotAuth)
	}
	if gotAPIKey != "" || gotVersionHdr != "" {
		t.Errorf("api.anthropic.com headers must not be sent to Vertex: x-api-key=%q anthropic-version=%q", gotAPIKey, gotVersionHdr)
	}
	if _, ok := gotBody["model"]; ok {
		t.Error("model must move from the body into the URL")
	}
	if gotBody["anthropic_version"] != "vertex-2023-10-16" {
		t.Errorf("anthropic_version = %v", gotBody["anthropic_version"])
	}
	if resp.Message.Content != "hi" {
		t.Errorf("content = %+v", resp)
	}
}

func TestVertexStreamUsesStreamRawPredict(t *testing.T) {
	var gotPath string
	var gotBody map[string]any
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &gotBody)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"yo\"}}\n\n")
		_, _ = io.WriteString(w, "event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"}}\n\n")
		_, _ = io.WriteString(w, "event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
	}))
	defer ts.Close()

	a := anthropic.New(ts.URL, 0, anthropic.WithVertex("p", "global"), anthropic.WithTokenSource(staticToken("t")))
	var text string
	err := a.Stream(context.Background(), providers.Request{
		Model:   "claude-haiku-5-5",
		Payload: map[string]any{"messages": []any{map[string]any{"role": "user", "content": "hi"}}},
	}, providers.EgressIdentity{}, func(c types.StreamChunk) error {
		text += c.Delta
		return nil
	})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	if want := "/v1/projects/p/locations/global/publishers/anthropic/models/claude-haiku-5-5:streamRawPredict"; gotPath != want {
		t.Errorf("path = %q, want %q", gotPath, want)
	}
	if gotBody["stream"] != true {
		t.Errorf("stream flag = %v", gotBody["stream"])
	}
	if text != "yo" {
		t.Errorf("streamed text = %q", text)
	}
}

func TestVertexMultimodalRoutesToRawPredict(t *testing.T) {
	var gotPath string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		_, _ = io.WriteString(w, `{"model":"claude","stop_reason":"end_turn","content":[{"type":"text","text":"a cat"}],"usage":{"input_tokens":9,"output_tokens":2}}`)
	}))
	defer ts.Close()

	a := anthropic.New(ts.URL, 0, anthropic.WithVertex("p", "europe-west1"), anthropic.WithTokenSource(staticToken("t")))
	res, err := a.CompleteMultimodal(context.Background(), providers.Request{
		Model: "claude-sonnet-5-5",
		Payload: map[string]any{"messages": []types.MultimodalMessage{{Role: "user", Parts: []types.ContentPart{
			{Type: types.PartText, Text: "what is this"},
			{Type: types.PartImage, Data: []byte{1, 2, 3}, Format: "png"},
		}}}},
	}, providers.EgressIdentity{})
	if err != nil {
		t.Fatalf("CompleteMultimodal: %v", err)
	}
	if want := "/v1/projects/p/locations/europe-west1/publishers/anthropic/models/claude-sonnet-5-5:rawPredict"; gotPath != want {
		t.Errorf("path = %q, want %q", gotPath, want)
	}
	if res.Text != "a cat" {
		t.Errorf("text = %q", res.Text)
	}
}

func TestVertexWithoutCredentialFailsClosed(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("no request may reach Vertex without a credential")
	}))
	defer ts.Close()
	a := anthropic.New(ts.URL, 0, anthropic.WithVertex("p", "us-east5"))
	_, err := a.Complete(context.Background(), providers.Request{
		Model:   "claude-sonnet-5-5",
		Payload: map[string]any{"messages": []any{map[string]any{"role": "user", "content": "hi"}}},
	}, providers.EgressIdentity{Secret: "an-anthropic-key"})
	if err == nil {
		t.Fatal("want error without a GCP credential")
	}
}

func TestVertexServiceAccountKeyRejectsNonKey(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("no request may reach Vertex with an invalid key")
	}))
	defer ts.Close()
	a := anthropic.New(ts.URL, 0, anthropic.WithVertex("p", "us-east5"), anthropic.WithServiceAccountKey())
	_, err := a.Complete(context.Background(), providers.Request{
		Model:   "claude-sonnet-5-5",
		Payload: map[string]any{"messages": []any{map[string]any{"role": "user", "content": "hi"}}},
	}, providers.EgressIdentity{Secret: "sk-ant-not-a-json-key"})
	if err == nil {
		t.Fatal("want error for a non-JSON service account key")
	}
}
