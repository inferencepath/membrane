package bedrock

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/inferencepath/membrane/internal/exterior/providers"
)

func TestEmbeddingsTitan(t *testing.T) {
	var gotPath string
	var gotBody map[string]any
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &gotBody)
		_, _ = io.WriteString(w, `{"embedding":[0.1,0.2,0.3],"inputTextTokenCount":7}`)
	}))
	defer ts.Close()

	a := New("us-east-1", ts.URL, 0, WithBearerTokenAuth())
	resp, err := a.Embeddings(context.Background(), providers.Request{
		Model:   "amazon.titan-embed-text-v2",
		Payload: map[string]any{"inputText": "hello", "model": "drop", "stream": true},
	}, providers.EgressIdentity{Secret: "tok"})
	if err != nil {
		t.Fatalf("Embeddings: %v", err)
	}
	if !strings.Contains(gotPath, "/model/amazon.titan-embed-text-v2/invoke") {
		t.Errorf("path = %q", gotPath)
	}
	if _, ok := gotBody["model"]; ok {
		t.Error("model must be dropped (path selects it)")
	}
	if _, ok := gotBody["stream"]; ok {
		t.Error("stream must not be sent")
	}
	if len(resp.Data) != 1 || resp.Usage.PromptTokens != 7 || resp.Provider != "bedrock" || resp.Model != "amazon.titan-embed-text-v2" {
		t.Errorf("resp = %+v", resp)
	}
}

func TestEmbeddingsCohereArray(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"embeddings":[[0.1,0.2],[0.3,0.4]]}`)
	}))
	defer ts.Close()

	a := New("us-east-1", ts.URL, 0, WithBearerTokenAuth())
	resp, err := a.Embeddings(context.Background(), providers.Request{Model: "cohere.embed", Payload: map[string]any{"texts": []any{"a", "b"}}}, providers.EgressIdentity{Secret: "tok"})
	if err != nil {
		t.Fatalf("Embeddings: %v", err)
	}
	if len(resp.Data) != 2 || resp.Data[1].Index != 1 {
		t.Errorf("resp = %+v", resp)
	}
}

func TestRerank(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer tok" {
			t.Errorf("auth = %q", r.Header.Get("Authorization"))
		}
		_, _ = io.WriteString(w, `{"results":[{"index":2,"relevance_score":0.9},{"index":0,"relevance_score":0.4}]}`)
	}))
	defer ts.Close()

	a := New("us-east-1", ts.URL, 0, WithBearerTokenAuth())
	resp, err := a.Rerank(context.Background(), providers.Request{Model: "cohere.rerank", Payload: map[string]any{"query": "q", "documents": []any{"a", "b", "c"}}}, providers.EgressIdentity{Secret: "tok"})
	if err != nil {
		t.Fatalf("Rerank: %v", err)
	}
	if len(resp.Results) != 2 || resp.Results[0].Index != 2 || resp.Results[0].RelevanceScore != 0.9 {
		t.Errorf("resp = %+v", resp)
	}
}

func TestImages(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"images":["QUJD","REVG"]}`)
	}))
	defer ts.Close()

	a := New("us-east-1", ts.URL, 0, WithBearerTokenAuth())
	resp, err := a.Images(context.Background(), providers.Request{Model: "amazon.titan-image", Payload: map[string]any{"textToImageParams": map[string]any{"text": "x"}}}, providers.EgressIdentity{Secret: "tok"})
	if err != nil {
		t.Fatalf("Images: %v", err)
	}
	if len(resp.Data) != 2 || resp.Data[0].B64JSON != "QUJD" || resp.Usage.Images != 2 {
		t.Errorf("resp = %+v", resp)
	}
}

func TestOperationUpstreamError(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer ts.Close()

	a := New("us-east-1", ts.URL, 0, WithBearerTokenAuth())
	_, err := a.Embeddings(context.Background(), providers.Request{Model: "m", Payload: map[string]any{}}, providers.EgressIdentity{Secret: "tok"})
	var se providers.UpstreamStatusError
	if !errors.As(err, &se) || se.Status != http.StatusInternalServerError {
		t.Fatalf("err = %v, want UpstreamStatusError 500", err)
	}
}
