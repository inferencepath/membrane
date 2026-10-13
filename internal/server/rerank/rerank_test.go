package rerank_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/inferencepath/membrane/internal/server/rerank"
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

type rerankResponse struct {
	Pathway string `json:"pathway"`
	Results []struct {
		Index          int     `json:"index"`
		RelevanceScore float64 `json:"relevance_score"`
		Document       string  `json:"document"`
	} `json:"results"`
	Usage struct {
		TotalTokens int `json:"total_tokens"`
	} `json:"usage"`
	Provider string `json:"provider"`
	Model    string `json:"model"`
}

func serve(t *testing.T, cfg fakeoa.Config) *httptest.Server {
	t.Helper()
	t.Setenv(harness.EgressEnv, harness.EgressToken)
	fake := fakeoa.New(cfg)
	t.Cleanup(fake.Close)
	h, err := harness.BuildRerank(fake.URL(), nil)
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(h)
	t.Cleanup(ts.Close)
	return ts
}

func TestRerankSuccessNeutral(t *testing.T) {
	ts := serve(t, fakeoa.Config{Model: "backing", RerankResults: []float64{0.9, 0.1}, RerankDocuments: []string{"doc a", "doc b"}, TotalTokens: 7})
	resp := post(t, ts.URL+rerank.Route, harness.CallerKey,
		`{"pathway":"rank","query":"q","documents":["doc a","doc b"],"return_documents":true}`)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	var out rerankResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if out.Pathway != "rank" {
		t.Errorf("pathway = %q", out.Pathway)
	}
	if len(out.Results) != 2 {
		t.Fatalf("results len = %d, want 2", len(out.Results))
	}
	if out.Results[0].RelevanceScore != 0.9 || out.Results[0].Document != "doc a" {
		t.Errorf("unexpected result[0]: %+v", out.Results[0])
	}
	if out.Usage.TotalTokens != 7 {
		t.Errorf("total tokens = %d", out.Usage.TotalTokens)
	}
	if out.Provider != "" || out.Model != "" {
		t.Errorf("provider/model leaked without reveal: provider=%q model=%q", out.Provider, out.Model)
	}
}

func TestRerankRevealSurfacesProvider(t *testing.T) {
	ts := serve(t, fakeoa.Config{Model: "backing", RerankResults: []float64{0.5}, TotalTokens: 1})
	resp := post(t, ts.URL+rerank.Route, harness.CallerKey,
		`{"pathway":"rank","query":"q","documents":["x"],"reveal":true}`)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	var out rerankResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if out.Provider != "openai" || out.Model != "backing" {
		t.Errorf("reveal did not surface provider/model: provider=%q model=%q", out.Provider, out.Model)
	}
}

func TestRerankMethodNotAllowed(t *testing.T) {
	ts := serve(t, fakeoa.Config{})
	req, _ := http.NewRequest(http.MethodGet, ts.URL+rerank.Route, nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", resp.StatusCode)
	}
}

func TestRerankMissingFields(t *testing.T) {
	ts := serve(t, fakeoa.Config{})
	for _, body := range []string{
		`{"pathway":"rank","documents":["a"]}`, // no query
		`{"pathway":"rank","query":"q"}`,       // no documents
		`{"query":"q","documents":["a"]}`,      // no pathway
	} {
		resp := post(t, ts.URL+rerank.Route, harness.CallerKey, body)
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("body %s: status = %d, want 400", body, resp.StatusCode)
		}
		resp.Body.Close()
	}
}

func TestRerankUnauthorized(t *testing.T) {
	ts := serve(t, fakeoa.Config{})
	resp := post(t, ts.URL+rerank.Route, "ck_demo.wrong-secret", `{"pathway":"rank","query":"q","documents":["a"]}`)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", resp.StatusCode)
	}
}

func TestRerankUnknownPathway(t *testing.T) {
	ts := serve(t, fakeoa.Config{})
	resp := post(t, ts.URL+rerank.Route, harness.CallerKey, `{"pathway":"nope","query":"q","documents":["a"]}`)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
}
