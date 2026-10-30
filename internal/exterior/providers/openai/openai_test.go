package openai_test

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
	"github.com/inferencepath/membrane/internal/exterior/providers/openai"
	"github.com/inferencepath/membrane/internal/types"
	fakeoa "github.com/inferencepath/membrane/test/fakes/openai"
)

func req() providers.Request {
	return providers.Request{
		Model: "test-model",
		Payload: map[string]any{
			"messages": []any{map[string]any{"role": "user", "content": "hi"}},
		},
	}
}

func TestID(t *testing.T) {
	if got := openai.New("openai", "https://api.example.com", 0).ID(); got != "openai" {
		t.Fatalf("ID() = %q, want openai", got)
	}
}

func TestCompleteToolCalling(t *testing.T) {
	var gotBody map[string]any
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &gotBody)
		_, _ = io.WriteString(w, `{"model":"m","choices":[{"message":{"role":"assistant","content":"",`+
			`"tool_calls":[{"id":"call_1","type":"function","function":{"name":"get_weather","arguments":"{\"city\":\"NYC\"}"}}]},`+
			`"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`)
	}))
	defer ts.Close()

	a := openai.New("openai", ts.URL, 0)
	r := providers.Request{Model: "test-model", Payload: map[string]any{
		"messages":    []any{map[string]any{"role": "user", "content": "weather?"}},
		"tools":       []any{map[string]any{"name": "get_weather", "description": "w", "parameters": json.RawMessage(`{"type":"object"}`)}},
		"tool_choice": json.RawMessage(`"auto"`),
	}}
	resp, err := a.Complete(context.Background(), r, providers.EgressIdentity{Secret: "tok"})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}

	// Neutral tools map to the OpenAI function schema.
	tools, _ := gotBody["tools"].([]any)
	if len(tools) != 1 {
		t.Fatalf("tools = %v", gotBody["tools"])
	}
	tool0 := tools[0].(map[string]any)
	if tool0["type"] != "function" {
		t.Errorf("tool type = %v", tool0["type"])
	}
	if fn, _ := tool0["function"].(map[string]any); fn["name"] != "get_weather" {
		t.Errorf("function.name = %v", fn["name"])
	}
	if gotBody["tool_choice"] != "auto" {
		t.Errorf("tool_choice = %v", gotBody["tool_choice"])
	}

	// Response tool_calls map to canonical tool calls.
	if len(resp.Message.ToolCalls) != 1 {
		t.Fatalf("tool_calls = %+v", resp.Message.ToolCalls)
	}
	tc := resp.Message.ToolCalls[0]
	if tc.ID != "call_1" || tc.Name != "get_weather" || string(tc.Arguments) != `{"city":"NYC"}` {
		t.Errorf("tool call = %+v (args=%s)", tc, tc.Arguments)
	}
	if resp.FinishReason != "tool_calls" {
		t.Errorf("finish = %q", resp.FinishReason)
	}
}

func TestCompleteToolResultRoundTrip(t *testing.T) {
	var gotBody map[string]any
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &gotBody)
		_, _ = io.WriteString(w, `{"model":"m","choices":[{"message":{"role":"assistant","content":"clear"},"finish_reason":"stop"}],"usage":{}}`)
	}))
	defer ts.Close()

	a := openai.New("openai", ts.URL, 0)
	r := providers.Request{Model: "m", Payload: map[string]any{"messages": []any{
		map[string]any{"role": "user", "content": "weather?"},
		map[string]any{"role": "assistant", "content": "", "tool_calls": []any{
			map[string]any{"id": "call_1", "name": "get_weather", "arguments": json.RawMessage(`{"city":"NYC"}`)},
		}},
		map[string]any{"role": "tool", "tool_call_id": "call_1", "name": "get_weather", "content": `{"tempF":61}`},
	}}}
	if _, err := a.Complete(context.Background(), r, providers.EgressIdentity{Secret: "tok"}); err != nil {
		t.Fatalf("Complete: %v", err)
	}

	msgs := gotBody["messages"].([]any)
	asst := msgs[1].(map[string]any)
	tc0 := asst["tool_calls"].([]any)[0].(map[string]any)
	if tc0["type"] != "function" {
		t.Errorf("tool_call type = %v", tc0["type"])
	}
	fn := tc0["function"].(map[string]any)
	if fn["name"] != "get_weather" || fn["arguments"] != `{"city":"NYC"}` {
		t.Errorf("function = %v (arguments must be a JSON string)", fn)
	}
	toolMsg := msgs[2].(map[string]any)
	if toolMsg["role"] != "tool" || toolMsg["tool_call_id"] != "call_1" {
		t.Errorf("tool message = %v", toolMsg)
	}
}

func TestComplete(t *testing.T) {
	fake := fakeoa.New(fakeoa.Config{
		Content: "hello there", Model: "backing-model", FinishReason: "stop",
		PromptTokens: 5, CompletionTokens: 7, TotalTokens: 12,
	})
	defer fake.Close()

	a := openai.New("openai", fake.URL(), 0)
	resp, err := a.Complete(context.Background(), req(), providers.EgressIdentity{Secret: "tok-123"})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if resp.Message.Content != "hello there" || resp.FinishReason != "stop" {
		t.Fatalf("resp = %+v", resp)
	}
	if resp.Usage.TotalTokens != 12 {
		t.Fatalf("usage = %+v", resp.Usage)
	}
	if resp.Provider != "openai" || resp.Model != "backing-model" {
		t.Fatalf("provider/model = %q/%q", resp.Provider, resp.Model)
	}

	// The upstream request carries the Egress Identity and the model, and only
	// the allowlisted payload.
	cap := fake.Last()
	if cap.Path != "/v1/chat/completions" {
		t.Fatalf("path = %q, want /v1/chat/completions", cap.Path)
	}
	if got := cap.Header.Get("Authorization"); got != "Bearer tok-123" {
		t.Fatalf("Authorization = %q", got)
	}
	body := string(cap.Body)
	if !strings.Contains(body, `"model":"test-model"`) {
		t.Fatalf("body missing model: %s", body)
	}
}

func TestCompleteBaseURLWithV1Suffix(t *testing.T) {
	fake := fakeoa.New(fakeoa.Config{Content: "ok", Model: "m", FinishReason: "stop"})
	defer fake.Close()

	// Endpoints are often configured with a trailing /v1, which must not be
	// doubled.
	a := openai.New("openai", fake.URL()+"/v1", 0)
	if _, err := a.Complete(context.Background(), req(), providers.EgressIdentity{Secret: "tok"}); err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if got := fake.Last().Path; got != "/v1/chat/completions" {
		t.Fatalf("path = %q, want /v1/chat/completions", got)
	}
}

func TestCompleteUpstreamStatus(t *testing.T) {
	fake := fakeoa.New(fakeoa.Config{Status: 500})
	defer fake.Close()
	a := openai.New("openai", fake.URL(), 0)
	_, err := a.Complete(context.Background(), req(), providers.EgressIdentity{})
	var se providers.UpstreamStatusError
	if !errors.As(err, &se) || se.Status != 500 {
		t.Fatalf("err = %v, want UpstreamStatusError{500}", err)
	}
}

func TestCompleteTimeout(t *testing.T) {
	fake := fakeoa.New(fakeoa.Config{Content: "slow", Delay: 300 * time.Millisecond})
	defer fake.Close()
	a := openai.New("openai", fake.URL(), 0)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	_, err := a.Complete(ctx, req(), providers.EgressIdentity{})
	if !errors.Is(err, providers.ErrUpstreamTimeout) {
		t.Fatalf("err = %v, want ErrUpstreamTimeout", err)
	}
}

func TestStream(t *testing.T) {
	fake := fakeoa.New(fakeoa.Config{StreamDeltas: []string{"Tic", "ket"}, FinishReason: "stop"})
	defer fake.Close()

	a := openai.New("openai", fake.URL(), 0)
	r := req()
	r.Stream = true

	var text strings.Builder
	var finish string
	done := false
	err := a.Stream(context.Background(), r, providers.EgressIdentity{Secret: "tok"}, func(c types.StreamChunk) error {
		if c.Done {
			done = true
			return nil
		}
		text.WriteString(c.Delta)
		if c.FinishReason != "" {
			finish = c.FinishReason
		}
		return nil
	})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	if text.String() != "Ticket" {
		t.Fatalf("streamed text = %q, want Ticket", text.String())
	}
	if finish != "stop" {
		t.Fatalf("finish = %q", finish)
	}
	if !done {
		t.Fatal("expected a terminal Done chunk")
	}
}

func TestStreamToolCalling(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"call_1\",\"function\":{\"name\":\"get_weather\"}}]}}]}\n\n")
		_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"function\":{\"arguments\":\"{\\\"city\\\":\"}}]}}]}\n\n")
		_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"function\":{\"arguments\":\"\\\"NYC\\\"}\"}}]}}]}\n\n")
		_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"tool_calls\"}]}\n\n")
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
	}))
	defer ts.Close()

	a := openai.New("openai", ts.URL, 0)
	var id, name, args, finish string
	err := a.Stream(context.Background(), providers.Request{Model: "m", Stream: true, Payload: map[string]any{"messages": []any{}}}, providers.EgressIdentity{Secret: "tok"}, func(c types.StreamChunk) error {
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
	if id != "call_1" || name != "get_weather" || args != `{"city":"NYC"}` {
		t.Errorf("reassembled call id=%q name=%q args=%q", id, name, args)
	}
	if finish != "tool_calls" {
		t.Errorf("finish = %q", finish)
	}
}

func TestStreamUpstreamStatus(t *testing.T) {
	fake := fakeoa.New(fakeoa.Config{Status: 503})
	defer fake.Close()
	a := openai.New("openai", fake.URL(), 0)
	r := req()
	r.Stream = true
	err := a.Stream(context.Background(), r, providers.EgressIdentity{}, func(types.StreamChunk) error { return nil })
	var se providers.UpstreamStatusError
	if !errors.As(err, &se) || se.Status != 503 {
		t.Fatalf("err = %v, want UpstreamStatusError{503}", err)
	}
}
