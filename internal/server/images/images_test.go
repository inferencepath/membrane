package images_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/inferencepath/membrane/internal/server/images"
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

type imageResponse struct {
	Pathway string `json:"pathway"`
	Data    []struct {
		URL     string `json:"url"`
		B64JSON string `json:"b64_json"`
	} `json:"data"`
	Usage struct {
		Images int `json:"images"`
	} `json:"usage"`
	Provider string `json:"provider"`
	Model    string `json:"model"`
}

func serve(t *testing.T, cfg fakeoa.Config) *httptest.Server {
	t.Helper()
	t.Setenv(harness.EgressEnv, harness.EgressToken)
	fake := fakeoa.New(cfg)
	t.Cleanup(fake.Close)
	h, err := harness.BuildImages(fake.URL(), nil)
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(h)
	t.Cleanup(ts.Close)
	return ts
}

func TestImageSuccessNeutral(t *testing.T) {
	ts := serve(t, fakeoa.Config{ImageURLs: []string{"https://host/img1.png", "https://host/img2.png"}})
	resp := post(t, ts.URL+images.Route, harness.CallerKey, `{"pathway":"paint","prompt":"a cat","n":2}`)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	var out imageResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if out.Pathway != "paint" {
		t.Errorf("pathway = %q", out.Pathway)
	}
	if len(out.Data) != 2 || out.Data[0].URL == "" {
		t.Fatalf("unexpected data: %+v", out.Data)
	}
	if out.Usage.Images != 2 {
		t.Errorf("images = %d, want 2", out.Usage.Images)
	}
	if out.Provider != "" || out.Model != "" {
		t.Errorf("provider/model leaked without reveal: provider=%q model=%q", out.Provider, out.Model)
	}
}

func TestImageB64(t *testing.T) {
	ts := serve(t, fakeoa.Config{ImageB64: []string{"aGVsbG8="}})
	resp := post(t, ts.URL+images.Route, harness.CallerKey, `{"pathway":"paint","prompt":"a cat","response_format":"b64_json"}`)
	defer resp.Body.Close()
	var out imageResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if len(out.Data) != 1 || out.Data[0].B64JSON != "aGVsbG8=" {
		t.Fatalf("unexpected b64 data: %+v", out.Data)
	}
}

func TestImageRevealSurfacesProvider(t *testing.T) {
	ts := serve(t, fakeoa.Config{ImageURLs: []string{"https://host/img.png"}})
	resp := post(t, ts.URL+images.Route, harness.CallerKey, `{"pathway":"paint","prompt":"x","reveal":true}`)
	defer resp.Body.Close()
	var out imageResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if out.Provider != "openai" || out.Model != "test-model" {
		t.Errorf("reveal did not surface provider/model: provider=%q model=%q", out.Provider, out.Model)
	}
}

func TestImageMethodNotAllowed(t *testing.T) {
	ts := serve(t, fakeoa.Config{})
	req, _ := http.NewRequest(http.MethodGet, ts.URL+images.Route, nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", resp.StatusCode)
	}
}

func TestImageMissingPrompt(t *testing.T) {
	ts := serve(t, fakeoa.Config{})
	resp := post(t, ts.URL+images.Route, harness.CallerKey, `{"pathway":"paint"}`)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
}

func TestImageUnauthorized(t *testing.T) {
	ts := serve(t, fakeoa.Config{})
	resp := post(t, ts.URL+images.Route, "ck_demo.wrong-secret", `{"pathway":"paint","prompt":"x"}`)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", resp.StatusCode)
	}
}

func TestImageUnknownPathway(t *testing.T) {
	ts := serve(t, fakeoa.Config{})
	resp := post(t, ts.URL+images.Route, harness.CallerKey, `{"pathway":"nope","prompt":"x"}`)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
}
