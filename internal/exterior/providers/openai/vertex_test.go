package openai_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/inferencepath/membrane/internal/exterior/providers"
	"github.com/inferencepath/membrane/internal/exterior/providers/openai"
)

func TestVertexOpenAIBase(t *testing.T) {
	if got := openai.VertexOpenAIBase("proj", "global"); got != "https://aiplatform.googleapis.com/v1/projects/proj/locations/global/endpoints/openapi" {
		t.Errorf("global = %q", got)
	}
	if got := openai.VertexOpenAIBase("proj", "us-central1"); got != "https://us-central1-aiplatform.googleapis.com/v1/projects/proj/locations/us-central1/endpoints/openapi" {
		t.Errorf("regional = %q", got)
	}
}

func TestTokenSourceReplacesStaticKey(t *testing.T) {
	var gotPath, gotAuth string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotAuth = r.URL.Path, r.Header.Get("Authorization")
		_, _ = io.WriteString(w, `{"model":"meta/llama","choices":[{"message":{"content":"ok"},"finish_reason":"stop"}]}`)
	}))
	defer ts.Close()

	a := openai.New("local", ts.URL+"/v1/projects/p/locations/global/endpoints/openapi", 0,
		openai.WithTokenSource(func(context.Context) (string, error) { return "gcp-tok", nil }))
	_, err := a.Complete(context.Background(), providers.Request{
		Model:   "meta/llama-4-maverick-17b-128e-instruct-maas",
		Payload: map[string]any{"messages": []any{map[string]any{"role": "user", "content": "hi"}}},
	}, providers.EgressIdentity{Secret: "ignored"})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if gotPath != "/v1/projects/p/locations/global/endpoints/openapi/chat/completions" {
		t.Errorf("path = %q", gotPath)
	}
	if gotAuth != "Bearer gcp-tok" {
		t.Errorf("Authorization = %q", gotAuth)
	}
}

func TestServiceAccountKeyRejectsNonKey(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("no request expected with an invalid key")
	}))
	defer ts.Close()
	a := openai.New("local", ts.URL, 0, openai.WithServiceAccountKey())
	if _, err := a.Complete(context.Background(), providers.Request{
		Model: "m", Payload: map[string]any{"messages": []any{map[string]any{"role": "user", "content": "hi"}}},
	}, providers.EgressIdentity{Secret: "not-json"}); err == nil {
		t.Fatal("want error for a non-JSON key")
	}
}
