package anthropic_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/inferencepath/membrane/internal/exterior/providers"
	"github.com/inferencepath/membrane/internal/exterior/providers/anthropic"
	"github.com/inferencepath/membrane/internal/types"
)

func TestCompleteToolCalling(t *testing.T) {
	var gotBody map[string]any
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &gotBody)
		_, _ = io.WriteString(w, `{"model":"claude","stop_reason":"tool_use","content":[`+
			`{"type":"text","text":"let me check"},`+
			`{"type":"tool_use","id":"toolu_1","name":"get_weather","input":{"city":"NYC"}}],`+
			`"usage":{"input_tokens":5,"output_tokens":3}}`)
	}))
	defer ts.Close()

	a := anthropic.New(ts.URL, 0)
	r := providers.Request{Model: "claude", Payload: map[string]any{
		"messages":    []any{map[string]any{"role": "system", "content": "be terse"}, map[string]any{"role": "user", "content": "weather?"}},
		"tools":       []any{map[string]any{"name": "get_weather", "description": "w", "parameters": json.RawMessage(`{"type":"object"}`)}},
		"tool_choice": json.RawMessage(`"auto"`),
	}}
	resp, err := a.Complete(context.Background(), r, providers.EgressIdentity{Secret: "k"})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}

	// System role lifted to the top-level field; not left in messages.
	if gotBody["system"] != "be terse" {
		t.Errorf("system = %v", gotBody["system"])
	}
	if msgs, _ := gotBody["messages"].([]any); len(msgs) != 1 {
		t.Errorf("messages = %v (system should be lifted out)", gotBody["messages"])
	}
	// Neutral tools → Anthropic input_schema shape.
	tool0 := gotBody["tools"].([]any)[0].(map[string]any)
	if tool0["name"] != "get_weather" {
		t.Errorf("tool name = %v", tool0["name"])
	}
	if _, ok := tool0["input_schema"]; !ok {
		t.Errorf("input_schema missing: %v", tool0)
	}
	if tc, _ := gotBody["tool_choice"].(map[string]any); tc["type"] != "auto" {
		t.Errorf("tool_choice = %v", gotBody["tool_choice"])
	}

	// Response tool_use → canonical; stop_reason normalized.
	if len(resp.Message.ToolCalls) != 1 {
		t.Fatalf("tool_calls = %+v", resp.Message.ToolCalls)
	}
	call := resp.Message.ToolCalls[0]
	if call.ID != "toolu_1" || call.Name != "get_weather" || string(call.Arguments) != `{"city":"NYC"}` {
		t.Errorf("tool call = %+v (args=%s)", call, call.Arguments)
	}
	if resp.Message.Content != "let me check" {
		t.Errorf("content = %q", resp.Message.Content)
	}
	if resp.FinishReason != "tool_calls" {
		t.Errorf("finish = %q, want normalized tool_calls", resp.FinishReason)
	}
}

func TestBuildBodyToolResultRoundTrip(t *testing.T) {
	var gotBody map[string]any
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &gotBody)
		_, _ = io.WriteString(w, `{"content":[{"type":"text","text":"clear"}],"stop_reason":"end_turn"}`)
	}))
	defer ts.Close()

	a := anthropic.New(ts.URL, 0)
	r := providers.Request{Model: "claude", Payload: map[string]any{"messages": []any{
		map[string]any{"role": "user", "content": "weather?"},
		map[string]any{"role": "assistant", "content": "", "tool_calls": []any{
			map[string]any{"id": "toolu_1", "name": "get_weather", "arguments": json.RawMessage(`{"city":"NYC"}`)},
		}},
		map[string]any{"role": "tool", "tool_call_id": "toolu_1", "content": `{"tempF":61}`},
	}}}
	if _, err := a.Complete(context.Background(), r, providers.EgressIdentity{Secret: "k"}); err != nil {
		t.Fatalf("Complete: %v", err)
	}
	msgs := gotBody["messages"].([]any)
	asst := msgs[1].(map[string]any)
	block := asst["content"].([]any)[0].(map[string]any)
	if block["type"] != "tool_use" || block["id"] != "toolu_1" {
		t.Errorf("assistant tool_use block = %v", block)
	}
	toolTurn := msgs[2].(map[string]any)
	if toolTurn["role"] != "user" {
		t.Errorf("tool-result role = %v, want user", toolTurn["role"])
	}
	tr := toolTurn["content"].([]any)[0].(map[string]any)
	if tr["type"] != "tool_result" || tr["tool_use_id"] != "toolu_1" {
		t.Errorf("tool_result block = %v", tr)
	}
}

func TestStreamToolCalling(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"tool_use\",\"id\":\"toolu_1\",\"name\":\"get_weather\"}}\n\n")
		_, _ = io.WriteString(w, "event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"input_json_delta\",\"partial_json\":\"{\\\"city\\\":\"}}\n\n")
		_, _ = io.WriteString(w, "event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"input_json_delta\",\"partial_json\":\"\\\"NYC\\\"}\"}}\n\n")
		_, _ = io.WriteString(w, "event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"tool_use\"}}\n\n")
		_, _ = io.WriteString(w, "event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
	}))
	defer ts.Close()

	a := anthropic.New(ts.URL, 0)
	var id, name, args, finish string
	err := a.Stream(context.Background(), providers.Request{Model: "claude", Payload: map[string]any{"messages": []any{}}}, providers.EgressIdentity{Secret: "k"}, func(c types.StreamChunk) error {
		for _, d := range c.ToolCalls {
			if d.ID != "" {
				id = d.ID
			}
			if d.Name != "" {
				name = d.Name
			}
			args += d.ArgumentsFragment
		}
		if c.FinishReason != "" {
			finish = c.FinishReason
		}
		return nil
	})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	if id != "toolu_1" || name != "get_weather" || args != `{"city":"NYC"}` {
		t.Errorf("reassembled call id=%q name=%q args=%q", id, name, args)
	}
	if finish != "tool_calls" {
		t.Errorf("finish = %q, want normalized tool_calls", finish)
	}
}

func TestID(t *testing.T) {
	if got := anthropic.New("https://api.anthropic.com", 0).ID(); got != "anthropic" {
		t.Fatalf("ID = %q, want anthropic", got)
	}
}

func TestCompleteSuccess(t *testing.T) {
	var gotPath, gotKey, gotVersion string
	var gotBody map[string]any
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotKey = r.Header.Get("x-api-key")
		gotVersion = r.Header.Get("anthropic-version")
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &gotBody)
		// No caller identity may reach the Provider.
		if r.Header.Get("Authorization") != "" {
			t.Error("Authorization header leaked to provider")
		}
		_, _ = io.WriteString(w, `{"model":"claude-3-5-sonnet","stop_reason":"end_turn","content":[{"type":"text","text":"hi there"}],"usage":{"input_tokens":5,"output_tokens":2}}`)
	}))
	defer ts.Close()

	a := anthropic.New(ts.URL, 0)
	resp, err := a.Complete(context.Background(), providers.Request{
		Model:   "claude-3-5-sonnet",
		Payload: map[string]any{"messages": []any{map[string]any{"role": "user", "content": "hi"}}, "stream": false},
	}, providers.EgressIdentity{Secret: "sk-ant-xyz"})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}

	if gotPath != "/v1/messages" {
		t.Errorf("path = %q", gotPath)
	}
	if gotKey != "sk-ant-xyz" {
		t.Errorf("x-api-key = %q", gotKey)
	}
	if gotVersion != "2023-06-01" {
		t.Errorf("anthropic-version = %q", gotVersion)
	}
	if gotBody["model"] != "claude-3-5-sonnet" {
		t.Errorf("body model = %v", gotBody["model"])
	}
	if _, ok := gotBody["max_tokens"]; !ok {
		t.Error("max_tokens default not applied")
	}
	if _, ok := gotBody["stream"]; ok {
		t.Error("stream must not be sent on non-streaming path")
	}
	if resp.Message.Content != "hi there" || resp.FinishReason != "end_turn" {
		t.Errorf("resp = %+v", resp)
	}
	if resp.Usage.TotalTokens != 7 || resp.Provider != "anthropic" {
		t.Errorf("usage/provider = %+v", resp)
	}
}

func TestCompleteKeepsExplicitMaxTokens(t *testing.T) {
	var gotBody map[string]any
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &gotBody)
		_, _ = io.WriteString(w, `{"content":[{"type":"text","text":"x"}],"stop_reason":"end_turn"}`)
	}))
	defer ts.Close()

	a := anthropic.New(ts.URL, 0)
	_, err := a.Complete(context.Background(), providers.Request{
		Model:   "m",
		Payload: map[string]any{"max_tokens": float64(32)},
	}, providers.EgressIdentity{Secret: "k"})
	if err != nil {
		t.Fatal(err)
	}
	if gotBody["max_tokens"] != float64(32) {
		t.Errorf("max_tokens = %v, want 32", gotBody["max_tokens"])
	}
}

func TestCompleteUpstreamError(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer ts.Close()

	a := anthropic.New(ts.URL, 0)
	_, err := a.Complete(context.Background(), providers.Request{Model: "m"}, providers.EgressIdentity{Secret: "k"})
	var se providers.UpstreamStatusError
	if !errors.As(err, &se) || se.Status != http.StatusTooManyRequests {
		t.Fatalf("err = %v, want UpstreamStatusError 429", err)
	}
}

func TestCompleteBadJSON(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `not json`)
	}))
	defer ts.Close()

	a := anthropic.New(ts.URL, 0)
	if _, err := a.Complete(context.Background(), providers.Request{Model: "m"}, providers.EgressIdentity{Secret: "k"}); err == nil {
		t.Fatal("want decode error")
	}
}

func TestCompleteBadURL(t *testing.T) {
	a := anthropic.New("http://bad host with spaces", 0)
	if _, err := a.Complete(context.Background(), providers.Request{Model: "m"}, providers.EgressIdentity{Secret: "k"}); err == nil {
		t.Fatal("want request-construction error")
	}
}

func TestCompleteTimeout(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(100 * time.Millisecond)
	}))
	defer ts.Close()
	ctx, cancel := context.WithTimeout(context.Background(), time.Millisecond)
	defer cancel()
	a := anthropic.New(ts.URL, 0)
	_, err := a.Complete(ctx, providers.Request{Model: "m"}, providers.EgressIdentity{Secret: "k"})
	if !errors.Is(err, providers.ErrUpstreamTimeout) {
		t.Fatalf("err = %v, want ErrUpstreamTimeout", err)
	}
}

func TestStreamSuccess(t *testing.T) {
	var gotKey, gotAccept string
	var gotBody map[string]any
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotKey = r.Header.Get("x-api-key")
		gotAccept = r.Header.Get("Accept")
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &gotBody)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"delta\":{\"type\":\"text_delta\",\"text\":\"Hel\"}}\n\n")
		_, _ = io.WriteString(w, "event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"delta\":{\"type\":\"text_delta\",\"text\":\"lo\"}}\n\n")
		_, _ = io.WriteString(w, "event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"}}\n\n")
		_, _ = io.WriteString(w, "event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
	}))
	defer ts.Close()

	a := anthropic.New(ts.URL, 0)
	var deltas []string
	var finish string
	var done bool
	err := a.Stream(context.Background(), providers.Request{
		Model:   "claude",
		Payload: map[string]any{"messages": []any{}},
	}, providers.EgressIdentity{Secret: "sk"}, func(c types.StreamChunk) error {
		switch {
		case c.Done:
			done = true
		case c.FinishReason != "":
			finish = c.FinishReason
		default:
			deltas = append(deltas, c.Delta)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	if gotKey != "sk" || gotAccept != "text/event-stream" {
		t.Errorf("headers key=%q accept=%q", gotKey, gotAccept)
	}
	if gotBody["stream"] != true {
		t.Errorf("stream flag not set: %v", gotBody["stream"])
	}
	if strings.Join(deltas, "") != "Hello" || finish != "end_turn" || !done {
		t.Fatalf("deltas=%v finish=%q done=%v", deltas, finish, done)
	}
}

func TestStreamUpstreamError(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer ts.Close()
	a := anthropic.New(ts.URL, 0)
	err := a.Stream(context.Background(), providers.Request{Model: "m"}, providers.EgressIdentity{Secret: "k"}, func(types.StreamChunk) error { return nil })
	var se providers.UpstreamStatusError
	if !errors.As(err, &se) || se.Status != http.StatusServiceUnavailable {
		t.Fatalf("err = %v", err)
	}
}
