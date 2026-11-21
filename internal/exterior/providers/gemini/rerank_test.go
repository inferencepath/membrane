package gemini_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/inferencepath/membrane/internal/exterior/providers"
	"github.com/inferencepath/membrane/internal/exterior/providers/gemini"
)

func TestRerankVertex(t *testing.T) {
	var gotPath, gotAuth string
	var gotBody map[string]any
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotAuth = r.URL.Path, r.Header.Get("Authorization")
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &gotBody)
		_, _ = io.WriteString(w, `{"records":[{"id":"1","score":0.91},{"id":"0","score":0.12}]}`)
	}))
	defer ts.Close()

	a := gemini.New(ts.URL, 0, gemini.WithVertex("proj", "us-central1"),
		gemini.WithTokenSource(func(context.Context) (string, error) { return "tok", nil }))
	res, err := a.Rerank(context.Background(), providers.Request{
		Model: "semantic-ranker-default@latest",
		Payload: map[string]any{
			"query": "billing", "documents": []any{"weather report", map[string]any{"text": "invoice overdue"}},
			"top_n": 2, "return_documents": true,
		},
	}, providers.EgressIdentity{})
	if err != nil {
		t.Fatalf("Rerank: %v", err)
	}
	if gotPath != "/v1/projects/proj/locations/global/rankingConfigs/default_ranking_config:rank" || gotAuth != "Bearer tok" {
		t.Errorf("path = %q, auth = %q", gotPath, gotAuth)
	}
	recs := gotBody["records"].([]any)
	if len(recs) != 2 || recs[1].(map[string]any)["content"] != "invoice overdue" || recs[1].(map[string]any)["id"] != "1" {
		t.Errorf("records = %v", recs)
	}
	if gotBody["topN"] != float64(2) || gotBody["ignoreRecordDetailsInResponse"] != false || gotBody["model"] != "semantic-ranker-default@latest" {
		t.Errorf("body = %v", gotBody)
	}
	if len(res.Results) != 2 || res.Results[0].Index != 1 || res.Results[0].Document != "invoice overdue" || res.Results[0].RelevanceScore != 0.91 {
		t.Errorf("results = %+v", res.Results)
	}
}

func TestRerankAIStudioUnsupported(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("no request expected on AI Studio")
	}))
	defer ts.Close()
	a := gemini.New(ts.URL, 0)
	_, err := a.Rerank(context.Background(), providers.Request{Model: "m", Payload: map[string]any{"query": "q", "documents": []any{"d"}}}, providers.EgressIdentity{Secret: "k"})
	if !errors.Is(err, providers.ErrOperationUnsupported) {
		t.Fatalf("err = %v, want ErrOperationUnsupported", err)
	}
}

func TestRerankUnknownRecordID(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"records":[{"id":"7","score":0.5}]}`)
	}))
	defer ts.Close()
	a := gemini.New(ts.URL, 0, gemini.WithVertex("p", "global"),
		gemini.WithTokenSource(func(context.Context) (string, error) { return "t", nil }))
	if _, err := a.Rerank(context.Background(), providers.Request{Model: "m", Payload: map[string]any{"query": "q", "documents": []any{"d"}}}, providers.EgressIdentity{}); err == nil {
		t.Fatal("want error for an id outside the documents")
	}
}
