package anthropic_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/inferencepath/membrane/internal/exterior/providers"
	"github.com/inferencepath/membrane/internal/exterior/providers/anthropic"
	"github.com/inferencepath/membrane/internal/types"
)

func TestResponsesBridgesToMessages(t *testing.T) {
	var gotPath string
	var gotBody map[string]any
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		if r.Header.Get("Authorization") != "" {
			t.Error("Authorization leaked")
		}
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &gotBody)
		_, _ = io.WriteString(w, `{"model":"claude-3-5","stop_reason":"end_turn","content":[{"type":"text","text":"Hamlet dies."}],"usage":{"input_tokens":6,"output_tokens":3}}`)
	}))
	defer ts.Close()

	a := anthropic.New(ts.URL, 0)
	resp, err := a.Responses(context.Background(), providers.Request{
		Model:   "claude-3-5",
		Payload: map[string]any{"input": "Summarize Hamlet", "instructions": "Be terse", "max_output_tokens": float64(64)},
	}, providers.EgressIdentity{Secret: "sk-ant"})
	if err != nil {
		t.Fatalf("Responses: %v", err)
	}
	// Request translation: input→messages, instructions→system, max_output_tokens→max_tokens.
	if gotPath != "/v1/messages" {
		t.Errorf("path = %q", gotPath)
	}
	if _, ok := gotBody["messages"]; !ok {
		t.Error("input was not translated to messages")
	}
	if gotBody["system"] != "Be terse" {
		t.Errorf("system = %v", gotBody["system"])
	}
	if gotBody["max_tokens"] != float64(64) {
		t.Errorf("max_tokens = %v", gotBody["max_tokens"])
	}
	if _, ok := gotBody["input"]; ok {
		t.Error("raw responses 'input' must not cross")
	}
	// Response mapping: one canonical output message carrying the text.
	if resp.FinishReason != "end_turn" || resp.Usage.TotalTokens != 9 || resp.Provider != "anthropic" {
		t.Errorf("resp = %+v", resp)
	}
	if len(resp.Output) != 1 {
		t.Fatalf("want 1 output item, got %d", len(resp.Output))
	}
	content, _ := resp.Output[0]["content"].([]any)
	if len(content) != 1 {
		t.Fatalf("content = %v", resp.Output[0]["content"])
	}
	first, _ := content[0].(map[string]any)
	if first["text"] != "Hamlet dies." {
		t.Errorf("output text = %v", first["text"])
	}
}

func TestResponsesStream(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fl, _ := w.(http.Flusher)
		_, _ = io.WriteString(w, "data: {\"type\":\"content_block_delta\",\"delta\":{\"text\":\"He\"}}\n\n")
		if fl != nil {
			fl.Flush()
		}
		_, _ = io.WriteString(w, "data: {\"type\":\"content_block_delta\",\"delta\":{\"text\":\"llo\"}}\n\n")
		_, _ = io.WriteString(w, "data: {\"type\":\"message_stop\"}\n\n")
	}))
	defer ts.Close()

	a := anthropic.New(ts.URL, 0)
	var deltas []string
	var done bool
	err := a.ResponsesStream(context.Background(), providers.Request{Model: "claude", Payload: map[string]any{"input": "hi"}},
		providers.EgressIdentity{Secret: "sk-ant"},
		func(c types.ResponsesStreamChunk) error {
			if c.Delta != "" {
				if c.Type != "response.output_text.delta" {
					t.Errorf("chunk type = %q", c.Type)
				}
				deltas = append(deltas, c.Delta)
			}
			if c.Done {
				done = true
			}
			return nil
		})
	if err != nil {
		t.Fatalf("ResponsesStream: %v", err)
	}
	if strings.Join(deltas, "") != "Hello" || !done {
		t.Errorf("deltas=%v done=%v", deltas, done)
	}
}
