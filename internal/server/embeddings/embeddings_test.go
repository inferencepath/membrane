package embeddings_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/inferencepath/membrane/internal/server/embeddings"
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

type embResponse struct {
	Pathway string `json:"pathway"`
	Data    []struct {
		Index     int       `json:"index"`
		Embedding []float64 `json:"embedding"`
	} `json:"data"`
	Usage struct {
		PromptTokens int `json:"prompt_tokens"`
		TotalTokens  int `json:"total_tokens"`
	} `json:"usage"`
	Provider string `json:"provider"`
	Model    string `json:"model"`
}

func serve(t *testing.T, cfg fakeoa.Config) *httptest.Server {
	t.Helper()
	t.Setenv(harness.EgressEnv, harness.EgressToken)
	fake := fakeoa.New(cfg)
	t.Cleanup(fake.Close)
	h, err := harness.BuildEmbeddings(fake.URL(), nil)
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(h)
	t.Cleanup(ts.Close)
	return ts
}

func TestEmbeddingsSuccessNeutral(t *testing.T) {
	ts := serve(t, fakeoa.Config{Model: "backing", EmbeddingVectors: [][]float64{{0.1, 0.2}, {0.3, 0.4}}, PromptTokens: 5, TotalTokens: 5})
	resp := post(t, ts.URL+embeddings.Route, harness.CallerKey, `{"pathway":"embed","input":["a","b"]}`)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	var out embResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if out.Pathway != "embed" {
		t.Errorf("pathway = %q", out.Pathway)
	}
	if len(out.Data) != 2 {
		t.Fatalf("data len = %d, want 2", len(out.Data))
	}
	if out.Data[1].Index != 1 || len(out.Data[1].Embedding) != 2 {
		t.Errorf("unexpected vector: %+v", out.Data[1])
	}
	if out.Usage.TotalTokens != 5 {
		t.Errorf("total tokens = %d", out.Usage.TotalTokens)
	}
	// Provider-neutral by default: provider/model must not be disclosed.
	if out.Provider != "" || out.Model != "" {
		t.Errorf("provider/model leaked without reveal: provider=%q model=%q", out.Provider, out.Model)
	}
}

func TestEmbeddingsRevealSurfacesProvider(t *testing.T) {
	ts := serve(t, fakeoa.Config{Model: "backing", EmbeddingVectors: [][]float64{{0.1}}, PromptTokens: 1, TotalTokens: 1})
	resp := post(t, ts.URL+embeddings.Route, harness.CallerKey, `{"pathway":"embed","input":"a","reveal":true}`)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	var out embResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if out.Provider != "openai" || out.Model != "backing" {
		t.Errorf("reveal did not surface provider/model: provider=%q model=%q", out.Provider, out.Model)
	}
}

func TestEmbeddingsMethodNotAllowed(t *testing.T) {
	ts := serve(t, fakeoa.Config{})
	req, _ := http.NewRequest(http.MethodGet, ts.URL+embeddings.Route, nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", resp.StatusCode)
	}
}

func TestEmbeddingsMissingInput(t *testing.T) {
	ts := serve(t, fakeoa.Config{})
	resp := post(t, ts.URL+embeddings.Route, harness.CallerKey, `{"pathway":"embed"}`)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
}

func TestEmbeddingsUnauthorized(t *testing.T) {
	ts := serve(t, fakeoa.Config{})
	resp := post(t, ts.URL+embeddings.Route, "ck_demo.wrong-secret", `{"pathway":"embed","input":"a"}`)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", resp.StatusCode)
	}
}

func TestEmbeddingsUnknownPathway(t *testing.T) {
	ts := serve(t, fakeoa.Config{})
	resp := post(t, ts.URL+embeddings.Route, harness.CallerKey, `{"pathway":"nope","input":"a"}`)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
}
