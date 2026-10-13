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
	"time"

	"github.com/aws/smithy-go/eventstream"

	"github.com/inferencepath/membrane/internal/exterior/providers"
	"github.com/inferencepath/membrane/internal/types"
)

func TestID(t *testing.T) {
	if got := New("us-east-1", "", 0).ID(); got != "bedrock" {
		t.Fatalf("ID = %q", got)
	}
}

func TestInvokeURLDefault(t *testing.T) {
	a := New("eu-west-1", "", 0)
	got := a.invokeURL("anthropic.claude-3")
	want := "https://bedrock-runtime.eu-west-1.amazonaws.com/model/anthropic.claude-3/invoke"
	if got != want {
		t.Errorf("invokeURL = %q, want %q", got, want)
	}
}

func TestCompleteSignsAndSucceeds(t *testing.T) {
	var gotAuth, gotPath string
	var gotBody map[string]any
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotPath = r.URL.Path
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &gotBody)
		_, _ = io.WriteString(w, `{"output":{"message":{"role":"assistant","content":[{"text":"yo"}]}},"stopReason":"end_turn","usage":{"inputTokens":4,"outputTokens":3,"totalTokens":7}}`)
	}))
	defer ts.Close()

	a := New("us-east-1", ts.URL, 0)
	resp, err := a.Complete(context.Background(), providers.Request{
		Model: "anthropic.claude-3-5-sonnet",
		Payload: map[string]any{
			"messages": []any{
				map[string]any{"role": "system", "content": "be brief"},
				map[string]any{"role": "user", "content": "hi"},
			},
			"stream": true,
		},
	}, providers.EgressIdentity{Secret: `{"access_key_id":"AKID","secret_access_key":"SECRET"}`})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}

	if !strings.HasPrefix(gotAuth, "AWS4-HMAC-SHA256") {
		t.Errorf("Authorization = %q, want SigV4", gotAuth)
	}
	if !strings.Contains(gotPath, "/model/anthropic.claude-3-5-sonnet/converse") {
		t.Errorf("path = %q", gotPath)
	}
	// System turns are lifted to the top-level "system" field; they must NOT
	// remain inside messages (the bug that returned 502).
	sys, ok := gotBody["system"].([]any)
	if !ok || len(sys) != 1 {
		t.Fatalf("system = %v, want one entry lifted out of messages", gotBody["system"])
	}
	if got := sys[0].(map[string]any)["text"]; got != "be brief" {
		t.Errorf("system text = %v", got)
	}
	msgs, ok := gotBody["messages"].([]any)
	if !ok || len(msgs) != 1 {
		t.Fatalf("messages = %v, want only the user turn", gotBody["messages"])
	}
	if role := msgs[0].(map[string]any)["role"]; role != "user" {
		t.Errorf("message role = %v, want user", role)
	}
	inf, ok := gotBody["inferenceConfig"].(map[string]any)
	if !ok || inf["maxTokens"] == nil {
		t.Errorf("inferenceConfig.maxTokens default not applied: %v", gotBody["inferenceConfig"])
	}
	if _, ok := gotBody["stream"]; ok {
		t.Error("stream must not be sent")
	}
	if _, ok := gotBody["model"]; ok {
		t.Error("model must not be sent (selected via path)")
	}
	if resp.Message.Content != "yo" || resp.Usage.TotalTokens != 7 || resp.Provider != "bedrock" {
		t.Errorf("resp = %+v", resp)
	}
}

func TestCompleteToolCalling(t *testing.T) {
	var gotPath string
	var gotBody map[string]any
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &gotBody)
		_, _ = io.WriteString(w, `{"output":{"message":{"role":"assistant","content":[`+
			`{"toolUse":{"toolUseId":"tu_1","name":"get_weather","input":{"city":"NYC"}}}]}},`+
			`"stopReason":"tool_use","usage":{"inputTokens":5,"outputTokens":3,"totalTokens":8}}`)
	}))
	defer ts.Close()

	a := New("us-east-1", ts.URL, 0)
	r := providers.Request{Model: "anthropic.claude-3", Payload: map[string]any{
		"messages":    []any{map[string]any{"role": "user", "content": "weather?"}},
		"tools":       []any{map[string]any{"name": "get_weather", "description": "w", "parameters": json.RawMessage(`{"type":"object"}`)}},
		"tool_choice": json.RawMessage(`"auto"`),
	}}
	resp, err := a.Complete(context.Background(), r, providers.EgressIdentity{Secret: "AKID:SECRET"})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if !strings.Contains(gotPath, "/converse") {
		t.Errorf("path = %q", gotPath)
	}

	// Outbound: neutral tools mapped to Converse toolConfig.
	tcfg, ok := gotBody["toolConfig"].(map[string]any)
	if !ok {
		t.Fatalf("toolConfig missing: %v", gotBody)
	}
	spec := tcfg["tools"].([]any)[0].(map[string]any)["toolSpec"].(map[string]any)
	if spec["name"] != "get_weather" {
		t.Errorf("toolSpec.name = %v", spec["name"])
	}
	if _, ok := spec["inputSchema"].(map[string]any)["json"]; !ok {
		t.Errorf("inputSchema.json missing: %v", spec)
	}
	if tc, _ := tcfg["toolChoice"].(map[string]any); tc["auto"] == nil {
		t.Errorf("toolChoice = %v", tcfg["toolChoice"])
	}

	// Inbound: toolUse normalized to canonical; tool_use → tool_calls.
	if len(resp.Message.ToolCalls) != 1 {
		t.Fatalf("tool_calls = %+v", resp.Message.ToolCalls)
	}
	tc := resp.Message.ToolCalls[0]
	if tc.ID != "tu_1" || tc.Name != "get_weather" || string(tc.Arguments) != `{"city":"NYC"}` {
		t.Errorf("tool call = %+v (args=%s)", tc, tc.Arguments)
	}
	if resp.FinishReason != "tool_calls" {
		t.Errorf("finish = %q, want normalized tool_calls", resp.FinishReason)
	}
}

func TestBuildBodyToolResultBlocks(t *testing.T) {
	var gotBody map[string]any
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &gotBody)
		_, _ = io.WriteString(w, `{"output":{"message":{"role":"assistant","content":[{"text":"ok"}]}},"stopReason":"end_turn","usage":{"totalTokens":1}}`)
	}))
	defer ts.Close()

	a := New("us-east-1", ts.URL, 0)
	r := providers.Request{Model: "m", Payload: map[string]any{"messages": []any{
		map[string]any{"role": "assistant", "content": "", "tool_calls": []any{
			map[string]any{"id": "tu_1", "name": "f", "arguments": json.RawMessage(`{"x":1}`)},
		}},
		map[string]any{"role": "tool", "tool_call_id": "tu_1", "content": `{"ok":true}`},
	}}}
	if _, err := a.Complete(context.Background(), r, providers.EgressIdentity{Secret: "AKID:SECRET"}); err != nil {
		t.Fatalf("Complete: %v", err)
	}
	msgs := gotBody["messages"].([]any)
	asst := msgs[0].(map[string]any)
	if _, ok := asst["content"].([]any)[0].(map[string]any)["toolUse"]; !ok {
		t.Errorf("assistant missing toolUse block: %v", asst)
	}
	usr := msgs[1].(map[string]any)
	if usr["role"] != "user" {
		t.Errorf("tool-result role = %v, want user (Converse carries results in a user turn)", usr["role"])
	}
	if _, ok := usr["content"].([]any)[0].(map[string]any)["toolResult"]; !ok {
		t.Errorf("missing toolResult block: %v", usr)
	}
}

func TestCompleteBearerTokenAuth(t *testing.T) {
	var gotAuth, gotPath string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotPath = r.URL.Path
		_, _ = io.WriteString(w, `{"output":{"message":{"role":"assistant","content":[{"text":"yo"}]}},"stopReason":"end_turn","usage":{"inputTokens":1,"outputTokens":1,"totalTokens":2}}`)
	}))
	defer ts.Close()

	a := New("us-east-1", ts.URL, 0, WithBearerTokenAuth())
	resp, err := a.Complete(context.Background(), providers.Request{
		Model:   "anthropic.claude-3-5-sonnet",
		Payload: map[string]any{"messages": []any{}},
	}, providers.EgressIdentity{Secret: "ABS.example-token"})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if gotAuth != "Bearer ABS.example-token" {
		t.Errorf("Authorization = %q, want bearer token", gotAuth)
	}
	if !strings.Contains(gotPath, "/model/anthropic.claude-3-5-sonnet/converse") {
		t.Errorf("path = %q", gotPath)
	}
	if resp.Message.Content != "yo" {
		t.Errorf("resp = %+v", resp)
	}
}

func TestCompleteBadCredentials(t *testing.T) {
	a := New("us-east-1", "http://unused", 0)
	if _, err := a.Complete(context.Background(), providers.Request{Model: "m"}, providers.EgressIdentity{Secret: ""}); err == nil {
		t.Fatal("want credential error")
	}
}

func TestCompleteUpstreamError(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer ts.Close()
	a := New("us-east-1", ts.URL, 0)
	_, err := a.Complete(context.Background(), providers.Request{Model: "m"}, providers.EgressIdentity{Secret: "AKID:SECRET"})
	var se providers.UpstreamStatusError
	if !errors.As(err, &se) || se.Status != http.StatusForbidden {
		t.Fatalf("err = %v", err)
	}
}

func TestCompleteTimeout(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(100 * time.Millisecond)
	}))
	defer ts.Close()
	ctx, cancel := context.WithTimeout(context.Background(), time.Millisecond)
	defer cancel()
	a := New("us-east-1", ts.URL, 0)
	_, err := a.Complete(ctx, providers.Request{Model: "m"}, providers.EgressIdentity{Secret: "AKID:SECRET"})
	if !errors.Is(err, providers.ErrUpstreamTimeout) {
		t.Fatalf("err = %v, want ErrUpstreamTimeout", err)
	}
}

func TestCompleteBadJSON(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `not json`)
	}))
	defer ts.Close()
	a := New("us-east-1", ts.URL, 0)
	if _, err := a.Complete(context.Background(), providers.Request{Model: "m"}, providers.EgressIdentity{Secret: "AKID:SECRET"}); err == nil {
		t.Fatal("want decode error")
	}
}

func writeBedrockFrame(t *testing.T, enc *eventstream.Encoder, w io.Writer, eventType, payloadJSON string) {
	t.Helper()
	msg := eventstream.Message{
		Headers: eventstream.Headers{{Name: ":event-type", Value: eventstream.StringValue(eventType)}},
		Payload: []byte(payloadJSON),
	}
	if err := enc.Encode(w, msg); err != nil {
		t.Fatal(err)
	}
}

func TestStreamSuccess(t *testing.T) {
	var gotPath, gotAuth string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		enc := eventstream.NewEncoder()
		writeBedrockFrame(t, enc, w, "contentBlockDelta", `{"delta":{"text":"Hel"}}`)
		writeBedrockFrame(t, enc, w, "contentBlockDelta", `{"delta":{"text":"lo"}}`)
		writeBedrockFrame(t, enc, w, "messageStop", `{"stopReason":"end_turn"}`)
	}))
	defer ts.Close()

	a := New("us-east-1", ts.URL, 0)
	var deltas []string
	var finish string
	var done bool
	err := a.Stream(context.Background(), providers.Request{Model: "anthropic.claude-3", Payload: map[string]any{"messages": []any{}}},
		providers.EgressIdentity{Secret: "AKID:SECRET"}, func(c types.StreamChunk) error {
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
	if !strings.Contains(gotPath, "/converse-stream") {
		t.Errorf("path = %q", gotPath)
	}
	if !strings.HasPrefix(gotAuth, "AWS4-HMAC-SHA256") {
		t.Errorf("auth = %q", gotAuth)
	}
	if strings.Join(deltas, "") != "Hello" || finish != "end_turn" || !done {
		t.Fatalf("deltas=%v finish=%q done=%v", deltas, finish, done)
	}
}

func TestStreamToolCalling(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		enc := eventstream.NewEncoder()
		writeBedrockFrame(t, enc, w, "contentBlockStart", `{"contentBlockIndex":0,"start":{"toolUse":{"toolUseId":"tu_1","name":"get_weather"}}}`)
		writeBedrockFrame(t, enc, w, "contentBlockDelta", `{"contentBlockIndex":0,"delta":{"toolUse":{"input":"{\"city\":"}}}`)
		writeBedrockFrame(t, enc, w, "contentBlockDelta", `{"contentBlockIndex":0,"delta":{"toolUse":{"input":"\"NYC\"}"}}}`)
		writeBedrockFrame(t, enc, w, "messageStop", `{"stopReason":"tool_use"}`)
	}))
	defer ts.Close()

	a := New("us-east-1", ts.URL, 0)
	var id, name, args, finish string
	err := a.Stream(context.Background(), providers.Request{Model: "anthropic.claude-3", Payload: map[string]any{"messages": []any{}}},
		providers.EgressIdentity{Secret: "AKID:SECRET"}, func(c types.StreamChunk) error {
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
	if id != "tu_1" || name != "get_weather" || args != `{"city":"NYC"}` {
		t.Errorf("reassembled call id=%q name=%q args=%q", id, name, args)
	}
	if finish != "tool_calls" {
		t.Errorf("finish = %q, want normalized tool_calls", finish)
	}
}

func TestStreamUpstreamError(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer ts.Close()
	a := New("us-east-1", ts.URL, 0)
	err := a.Stream(context.Background(), providers.Request{Model: "m"}, providers.EgressIdentity{Secret: "AKID:SECRET"}, func(types.StreamChunk) error { return nil })
	var se providers.UpstreamStatusError
	if !errors.As(err, &se) || se.Status != http.StatusTooManyRequests {
		t.Fatalf("err = %v", err)
	}
}

func TestParseCredentials(t *testing.T) {
	tests := []struct {
		name    string
		secret  string
		wantAK  string
		wantTok string
		wantErr bool
	}{
		{"json full", `{"access_key_id":"A","secret_access_key":"S","session_token":"T"}`, "A", "T", false},
		{"json missing secret", `{"access_key_id":"A"}`, "", "", true},
		{"json malformed", `{bad`, "", "", true},
		{"colon pair", "A:S", "A", "", false},
		{"colon triple", "A:S:T", "A", "T", false},
		{"empty", "   ", "", "", true},
		{"colon missing secret", "A:", "", "", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c, err := parseCredentials(tc.secret)
			if tc.wantErr {
				if err == nil {
					t.Fatal("want error")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if c.AccessKeyID != tc.wantAK || c.SessionToken != tc.wantTok {
				t.Errorf("creds = %+v", c)
			}
		})
	}
}
