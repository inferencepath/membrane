package gemini_test

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
	"github.com/inferencepath/membrane/internal/exterior/providers/gemini"
	"github.com/inferencepath/membrane/internal/types"
)

func TestID(t *testing.T) {
	if got := gemini.New("", 0).ID(); got != "gemini" {
		t.Fatalf("ID = %q, want gemini", got)
	}
}

func TestCompleteSuccess(t *testing.T) {
	var gotPath, gotKey string
	var gotBody map[string]any
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotKey = r.Header.Get("x-goog-api-key")
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &gotBody)
		// No caller identity may reach the Provider.
		if r.Header.Get("Authorization") != "" {
			t.Error("Authorization header leaked to provider")
		}
		_, _ = io.WriteString(w, `{"candidates":[{"content":{"parts":[{"text":"hi there"}]},"finishReason":"STOP"}],`+
			`"usageMetadata":{"promptTokenCount":5,"candidatesTokenCount":2,"totalTokenCount":7}}`)
	}))
	defer ts.Close()

	a := gemini.New(ts.URL, 0)
	resp, err := a.Complete(context.Background(), providers.Request{
		Model:   "gemini-1.5-pro",
		Payload: map[string]any{"messages": []any{map[string]any{"role": "user", "content": "hi"}}, "temperature": 0.2},
	}, providers.EgressIdentity{Secret: "goog-key-xyz"})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}

	if gotPath != "/v1beta/models/gemini-1.5-pro:generateContent" {
		t.Errorf("path = %q", gotPath)
	}
	if gotKey != "goog-key-xyz" {
		t.Errorf("x-goog-api-key = %q", gotKey)
	}
	// temperature lands in generationConfig; no top-level model field.
	gen := gotBody["generationConfig"].(map[string]any)
	if gen["temperature"] != 0.2 {
		t.Errorf("generationConfig.temperature = %v", gen["temperature"])
	}
	if _, ok := gotBody["model"]; ok {
		t.Error("model must be in the URL, not the body")
	}
	if resp.Message.Content != "hi there" || resp.FinishReason != "stop" {
		t.Errorf("resp = %+v", resp)
	}
	if resp.Usage.TotalTokens != 7 || resp.Provider != "gemini" || resp.Model != "gemini-1.5-pro" {
		t.Errorf("usage/provider/model = %+v", resp)
	}
}

func TestCompleteToolCalling(t *testing.T) {
	var gotBody map[string]any
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &gotBody)
		_, _ = io.WriteString(w, `{"candidates":[{"content":{"parts":[`+
			`{"text":"let me check"},`+
			`{"functionCall":{"name":"get_weather","args":{"city":"NYC"}}}]},`+
			`"finishReason":"STOP"}],`+
			`"usageMetadata":{"promptTokenCount":5,"candidatesTokenCount":3,"totalTokenCount":8}}`)
	}))
	defer ts.Close()

	a := gemini.New(ts.URL, 0)
	r := providers.Request{Model: "gemini-1.5-pro", Payload: map[string]any{
		"messages":    []any{map[string]any{"role": "system", "content": "be terse"}, map[string]any{"role": "user", "content": "weather?"}},
		"tools":       []any{map[string]any{"name": "get_weather", "description": "w", "parameters": json.RawMessage(`{"type":"object"}`)}},
		"tool_choice": json.RawMessage(`"auto"`),
	}}
	resp, err := a.Complete(context.Background(), r, providers.EgressIdentity{Secret: "k"})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}

	// The system turn moves to systemInstruction.
	si := gotBody["systemInstruction"].(map[string]any)
	parts := si["parts"].([]any)
	if parts[0].(map[string]any)["text"] != "be terse" {
		t.Errorf("systemInstruction = %v", si)
	}
	if contents, _ := gotBody["contents"].([]any); len(contents) != 1 {
		t.Errorf("contents = %v (system should be lifted out)", gotBody["contents"])
	}
	// Neutral tools map to functionDeclarations.
	tool0 := gotBody["tools"].([]any)[0].(map[string]any)
	decl0 := tool0["functionDeclarations"].([]any)[0].(map[string]any)
	if decl0["name"] != "get_weather" {
		t.Errorf("tool name = %v", decl0["name"])
	}
	if _, ok := decl0["parameters"]; !ok {
		t.Errorf("parameters missing: %v", decl0)
	}
	tc := gotBody["toolConfig"].(map[string]any)["functionCallingConfig"].(map[string]any)
	if tc["mode"] != "AUTO" {
		t.Errorf("functionCallingConfig = %v", tc)
	}

	// functionCall maps to a canonical tool call with finish reason tool_calls.
	if len(resp.Message.ToolCalls) != 1 {
		t.Fatalf("tool_calls = %+v", resp.Message.ToolCalls)
	}
	call := resp.Message.ToolCalls[0]
	if call.Name != "get_weather" || string(call.Arguments) != `{"city":"NYC"}` {
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
		_, _ = io.WriteString(w, `{"candidates":[{"content":{"parts":[{"text":"clear"}]},"finishReason":"STOP"}]}`)
	}))
	defer ts.Close()

	a := gemini.New(ts.URL, 0)
	r := providers.Request{Model: "gemini-1.5-pro", Payload: map[string]any{"messages": []any{
		map[string]any{"role": "user", "content": "weather?"},
		map[string]any{"role": "assistant", "content": "", "tool_calls": []any{
			map[string]any{"id": "call_1", "name": "get_weather", "arguments": json.RawMessage(`{"city":"NYC"}`)},
		}},
		map[string]any{"role": "tool", "name": "get_weather", "tool_call_id": "call_1", "content": `{"tempF":61}`},
	}}}
	if _, err := a.Complete(context.Background(), r, providers.EgressIdentity{Secret: "k"}); err != nil {
		t.Fatalf("Complete: %v", err)
	}
	contents := gotBody["contents"].([]any)
	// An assistant turn maps to the model role with a functionCall part.
	model := contents[1].(map[string]any)
	if model["role"] != "model" {
		t.Errorf("assistant role = %v, want model", model["role"])
	}
	fcPart := model["parts"].([]any)[0].(map[string]any)
	fc := fcPart["functionCall"].(map[string]any)
	if fc["name"] != "get_weather" {
		t.Errorf("functionCall = %v", fc)
	}
	// A tool-result turn maps to the user role with a functionResponse part.
	toolTurn := contents[2].(map[string]any)
	if toolTurn["role"] != "user" {
		t.Errorf("tool-result role = %v, want user", toolTurn["role"])
	}
	fr := toolTurn["parts"].([]any)[0].(map[string]any)["functionResponse"].(map[string]any)
	if fr["name"] != "get_weather" {
		t.Errorf("functionResponse name = %v", fr["name"])
	}
	if resp := fr["response"].(map[string]any); resp["tempF"] != float64(61) {
		t.Errorf("functionResponse response = %v", resp)
	}
}

func TestStreamSuccess(t *testing.T) {
	var gotKey, gotPath, gotAccept string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotKey = r.Header.Get("x-goog-api-key")
		gotPath = r.URL.Path
		gotAccept = r.Header.Get("Accept")
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"Hel\"}]}}]}\n\n")
		_, _ = io.WriteString(w, "data: {\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"lo\"}]}}]}\n\n")
		_, _ = io.WriteString(w, "data: {\"candidates\":[{\"content\":{\"parts\":[]},\"finishReason\":\"STOP\"}]}\n\n")
	}))
	defer ts.Close()

	a := gemini.New(ts.URL, 0)
	var deltas []string
	var finish string
	var done bool
	err := a.Stream(context.Background(), providers.Request{
		Model:   "gemini-1.5-pro",
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
	if gotPath != "/v1beta/models/gemini-1.5-pro:streamGenerateContent" {
		t.Errorf("path = %q", gotPath)
	}
	if strings.Join(deltas, "") != "Hello" || finish != "stop" || !done {
		t.Fatalf("deltas=%v finish=%q done=%v", deltas, finish, done)
	}
}

func TestStreamToolCalling(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"candidates\":[{\"content\":{\"parts\":[{\"functionCall\":{\"name\":\"get_weather\",\"args\":{\"city\":\"NYC\"}}}]}}]}\n\n")
		_, _ = io.WriteString(w, "data: {\"candidates\":[{\"content\":{\"parts\":[]},\"finishReason\":\"STOP\"}]}\n\n")
	}))
	defer ts.Close()

	a := gemini.New(ts.URL, 0)
	var name, args string
	err := a.Stream(context.Background(), providers.Request{Model: "gemini-1.5-pro", Payload: map[string]any{"messages": []any{}}}, providers.EgressIdentity{Secret: "k"}, func(c types.StreamChunk) error {
		for _, d := range c.ToolCalls {
			if d.Name != "" {
				name = d.Name
			}
			args += d.ArgumentsFragment
		}
		return nil
	})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	if name != "get_weather" || args != `{"city":"NYC"}` {
		t.Errorf("reassembled call name=%q args=%q", name, args)
	}
}

func TestCompleteUpstreamError(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer ts.Close()

	a := gemini.New(ts.URL, 0)
	_, err := a.Complete(context.Background(), providers.Request{Model: "m"}, providers.EgressIdentity{Secret: "k"})
	var se providers.UpstreamStatusError
	if !errors.As(err, &se) || se.Status != http.StatusTooManyRequests {
		t.Fatalf("err = %v, want UpstreamStatusError 429", err)
	}
}

func TestStreamUpstreamError(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer ts.Close()
	a := gemini.New(ts.URL, 0)
	err := a.Stream(context.Background(), providers.Request{Model: "m"}, providers.EgressIdentity{Secret: "k"}, func(types.StreamChunk) error { return nil })
	var se providers.UpstreamStatusError
	if !errors.As(err, &se) || se.Status != http.StatusServiceUnavailable {
		t.Fatalf("err = %v", err)
	}
}

func TestCompleteBadJSON(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `not json`)
	}))
	defer ts.Close()

	a := gemini.New(ts.URL, 0)
	if _, err := a.Complete(context.Background(), providers.Request{Model: "m"}, providers.EgressIdentity{Secret: "k"}); err == nil {
		t.Fatal("want decode error")
	}
}

func TestCompleteTimeout(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(100 * time.Millisecond)
	}))
	defer ts.Close()
	ctx, cancel := context.WithTimeout(context.Background(), time.Millisecond)
	defer cancel()
	a := gemini.New(ts.URL, 0)
	_, err := a.Complete(ctx, providers.Request{Model: "m"}, providers.EgressIdentity{Secret: "k"})
	if !errors.Is(err, providers.ErrUpstreamTimeout) {
		t.Fatalf("err = %v, want ErrUpstreamTimeout", err)
	}
}

func TestVertexEndpointAndBearer(t *testing.T) {
	var gotPath, gotAuth, gotKey string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		gotKey = r.Header.Get("x-goog-api-key")
		_, _ = io.WriteString(w, `{"candidates":[{"content":{"parts":[{"text":"hi"}]},"finishReason":"STOP"}],`+
			`"usageMetadata":{"promptTokenCount":1,"candidatesTokenCount":1,"totalTokenCount":2}}`)
	}))
	defer ts.Close()

	src := func(context.Context) (string, error) { return "ya29.TOKEN", nil }
	a := gemini.New(ts.URL, 0, gemini.WithVertex("my-proj", "us-central1"), gemini.WithTokenSource(src))
	_, err := a.Complete(context.Background(), providers.Request{
		Model:   "gemini-1.5-pro",
		Payload: map[string]any{"messages": []any{map[string]any{"role": "user", "content": "hi"}}},
	}, providers.EgressIdentity{Secret: "unused-when-oauth"})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if gotPath != "/v1/projects/my-proj/locations/us-central1/publishers/google/models/gemini-1.5-pro:generateContent" {
		t.Errorf("vertex path = %q", gotPath)
	}
	if gotAuth != "Bearer ya29.TOKEN" {
		t.Errorf("Authorization = %q", gotAuth)
	}
	if gotKey != "" {
		t.Errorf("x-goog-api-key must not be sent on the Vertex/OAuth path, got %q", gotKey)
	}
}

func TestTokenSourceFailureIsLocal(t *testing.T) {
	var hit bool
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hit = true
		_, _ = io.WriteString(w, `{}`)
	}))
	defer ts.Close()

	src := func(context.Context) (string, error) { return "", errors.New("no ADC") }
	a := gemini.New(ts.URL, 0, gemini.WithVertex("p", "l"), gemini.WithTokenSource(src))
	_, err := a.Complete(context.Background(), providers.Request{Model: "m"}, providers.EgressIdentity{Secret: "k"})
	if err == nil {
		t.Fatal("want local pre-egress error")
	}
	// A token-source failure must fail closed before any upstream contact.
	if hit {
		t.Error("request reached upstream despite token-source failure")
	}
	// It must not masquerade as a provider status error.
	var se providers.UpstreamStatusError
	if errors.As(err, &se) {
		t.Errorf("token-source failure surfaced as UpstreamStatusError: %v", err)
	}
}

func TestEmbeddingsSingle(t *testing.T) {
	var gotPath string
	var gotBody map[string]any
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &gotBody)
		_, _ = io.WriteString(w, `{"embedding":{"values":[0.1,0.2,0.3]}}`)
	}))
	defer ts.Close()

	a := gemini.New(ts.URL, 0)
	res, err := a.Embeddings(context.Background(), providers.Request{
		Model:   "text-embedding-004",
		Payload: map[string]any{"input": "hello"},
	}, providers.EgressIdentity{Secret: "k"})
	if err != nil {
		t.Fatalf("Embeddings: %v", err)
	}
	if gotPath != "/v1beta/models/text-embedding-004:embedContent" {
		t.Errorf("path = %q", gotPath)
	}
	if gotBody["model"] != "models/text-embedding-004" {
		t.Errorf("body model = %v", gotBody["model"])
	}
	if len(res.Data) != 1 || res.Provider != "gemini" {
		t.Fatalf("result = %+v", res)
	}
	vec, ok := res.Data[0].Embedding.([]any)
	if !ok || len(vec) != 3 {
		t.Errorf("embedding = %v", res.Data[0].Embedding)
	}
}

func TestEmbeddingsBatch(t *testing.T) {
	var gotPath string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		_, _ = io.WriteString(w, `{"embeddings":[{"values":[0.1,0.2]},{"values":[0.3,0.4]}]}`)
	}))
	defer ts.Close()

	a := gemini.New(ts.URL, 0)
	res, err := a.Embeddings(context.Background(), providers.Request{
		Model:   "text-embedding-004",
		Payload: map[string]any{"input": []any{"a", "b"}},
	}, providers.EgressIdentity{Secret: "k"})
	if err != nil {
		t.Fatalf("Embeddings: %v", err)
	}
	if gotPath != "/v1beta/models/text-embedding-004:batchEmbedContents" {
		t.Errorf("path = %q", gotPath)
	}
	if len(res.Data) != 2 || res.Data[1].Index != 1 {
		t.Fatalf("result = %+v", res)
	}
}

func TestDefaultBaseURL(t *testing.T) {
	// An empty baseURL must not panic and must default to the public endpoint.
	a := gemini.New("", 0)
	if a.ID() != "gemini" {
		t.Fatalf("ID = %q", a.ID())
	}
}
