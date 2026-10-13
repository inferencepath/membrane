package moderations_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/inferencepath/membrane/internal/server/moderations"
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

type modResponse struct {
	Pathway string `json:"pathway"`
	Results []struct {
		Flagged        bool               `json:"flagged"`
		Categories     map[string]bool    `json:"categories"`
		CategoryScores map[string]float64 `json:"category_scores"`
	} `json:"results"`
	Provider string `json:"provider"`
	Model    string `json:"model"`
}

func serve(t *testing.T, cfg fakeoa.Config) *httptest.Server {
	t.Helper()
	t.Setenv(harness.EgressEnv, harness.EgressToken)
	fake := fakeoa.New(cfg)
	t.Cleanup(fake.Close)
	h, err := harness.BuildModerations(fake.URL(), nil)
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(h)
	t.Cleanup(ts.Close)
	return ts
}

func TestModerationsSuccessNeutral(t *testing.T) {
	ts := serve(t, fakeoa.Config{
		Model:                "backing",
		ModerationFlagged:    true,
		ModerationCategories: map[string]bool{"violence": true},
		ModerationScores:     map[string]float64{"violence": 0.99},
	})
	resp := post(t, ts.URL+moderations.Route, harness.CallerKey, `{"pathway":"moderate","input":"bad text"}`)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	var out modResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if len(out.Results) != 1 || !out.Results[0].Flagged {
		t.Fatalf("unexpected results: %+v", out.Results)
	}
	if !out.Results[0].Categories["violence"] || out.Results[0].CategoryScores["violence"] != 0.99 {
		t.Errorf("category data missing: %+v", out.Results[0])
	}
	if out.Provider != "" || out.Model != "" {
		t.Errorf("provider/model leaked without reveal")
	}
}

func TestModerationsReveal(t *testing.T) {
	ts := serve(t, fakeoa.Config{Model: "backing", ModerationCategories: map[string]bool{}, ModerationScores: map[string]float64{}})
	resp := post(t, ts.URL+moderations.Route, harness.CallerKey, `{"pathway":"moderate","input":"x","reveal":true}`)
	defer resp.Body.Close()
	var out modResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if out.Provider != "openai" || out.Model != "backing" {
		t.Errorf("reveal did not surface provider/model: %q / %q", out.Provider, out.Model)
	}
}

func TestModerationsMethodNotAllowed(t *testing.T) {
	ts := serve(t, fakeoa.Config{})
	req, _ := http.NewRequest(http.MethodGet, ts.URL+moderations.Route, nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", resp.StatusCode)
	}
}

func TestModerationsMissingInput(t *testing.T) {
	ts := serve(t, fakeoa.Config{})
	resp := post(t, ts.URL+moderations.Route, harness.CallerKey, `{"pathway":"moderate"}`)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
}

func TestModerationsUnknownPathway(t *testing.T) {
	ts := serve(t, fakeoa.Config{})
	resp := post(t, ts.URL+moderations.Route, harness.CallerKey, `{"pathway":"nope","input":"x"}`)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
}
