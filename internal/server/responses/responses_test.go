package responses_test

import (
	"bufio"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/inferencepath/membrane/internal/server/responses"
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

type responsesResponse struct {
	Pathway      string           `json:"pathway"`
	Output       []map[string]any `json:"output"`
	FinishReason string           `json:"finish_reason"`
	Usage        struct {
		InputTokens  int `json:"input_tokens"`
		OutputTokens int `json:"output_tokens"`
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
	h, err := harness.BuildResponses(fake.URL(), nil)
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(h)
	t.Cleanup(ts.Close)
	return ts
}

func TestResponsesSuccessNeutral(t *testing.T) {
	ts := serve(t, fakeoa.Config{
		Model:            "backing",
		ResponsesStatus:  "completed",
		ResponsesOutput:  []map[string]any{{"type": "message", "content": "hi"}},
		PromptTokens:     3,
		CompletionTokens: 4,
		TotalTokens:      7,
	})
	resp := post(t, ts.URL+responses.Route, harness.CallerKey, `{"pathway":"respond","input":"hello"}`)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	var out responsesResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if out.Pathway != "respond" {
		t.Errorf("pathway = %q", out.Pathway)
	}
	if len(out.Output) != 1 || out.Output[0]["content"] != "hi" {
		t.Errorf("unexpected output: %+v", out.Output)
	}
	if out.FinishReason != "completed" || out.Usage.TotalTokens != 7 {
		t.Errorf("finish/usage = %q / %d", out.FinishReason, out.Usage.TotalTokens)
	}
	if out.Provider != "" || out.Model != "" {
		t.Errorf("provider/model leaked without reveal: provider=%q model=%q", out.Provider, out.Model)
	}
}

func TestResponsesRevealSurfacesProvider(t *testing.T) {
	ts := serve(t, fakeoa.Config{Model: "backing", ResponsesOutput: []map[string]any{{"type": "message"}}, TotalTokens: 1})
	resp := post(t, ts.URL+responses.Route, harness.CallerKey, `{"pathway":"respond","input":"x","reveal":true}`)
	defer resp.Body.Close()
	var out responsesResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if out.Provider != "openai" || out.Model != "backing" {
		t.Errorf("reveal did not surface provider/model: provider=%q model=%q", out.Provider, out.Model)
	}
}

func TestResponsesStreaming(t *testing.T) {
	ts := serve(t, fakeoa.Config{StreamDeltas: []string{"he", "llo"}})
	resp := post(t, ts.URL+responses.Route, harness.CallerKey, `{"pathway":"respond","input":"x","stream":true}`)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Fatalf("content-type = %q", ct)
	}
	var deltas []string
	sawDone := false
	sc := bufio.NewScanner(resp.Body)
	for sc.Scan() {
		line := sc.Text()
		data, ok := strings.CutPrefix(line, "data: ")
		if !ok {
			continue
		}
		if data == "[DONE]" {
			sawDone = true
			continue
		}
		var ev struct {
			Type  string `json:"type"`
			Delta string `json:"delta"`
		}
		if err := json.Unmarshal([]byte(data), &ev); err != nil {
			t.Fatalf("bad event %q: %v", data, err)
		}
		if ev.Delta != "" {
			deltas = append(deltas, ev.Delta)
		}
	}
	if strings.Join(deltas, "") != "hello" {
		t.Errorf("deltas = %v, want he+llo", deltas)
	}
	if !sawDone {
		t.Error("stream did not terminate with [DONE]")
	}
}

func TestResponsesMethodNotAllowed(t *testing.T) {
	ts := serve(t, fakeoa.Config{})
	req, _ := http.NewRequest(http.MethodGet, ts.URL+responses.Route, nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", resp.StatusCode)
	}
}

func TestResponsesMissingInput(t *testing.T) {
	ts := serve(t, fakeoa.Config{})
	resp := post(t, ts.URL+responses.Route, harness.CallerKey, `{"pathway":"respond"}`)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
}

func TestResponsesUnknownPathway(t *testing.T) {
	ts := serve(t, fakeoa.Config{})
	resp := post(t, ts.URL+responses.Route, harness.CallerKey, `{"pathway":"nope","input":"x"}`)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
}
