package chat_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/inferencepath/membrane/internal/exterior/providers"
	"github.com/inferencepath/membrane/internal/interior/admit"
	"github.com/inferencepath/membrane/internal/interior/audit"
	"github.com/inferencepath/membrane/internal/interior/resolve"
	"github.com/inferencepath/membrane/internal/membrane/permeability"
	"github.com/inferencepath/membrane/internal/membrane/scrub"
	"github.com/inferencepath/membrane/internal/membrane/transporter"
	"github.com/inferencepath/membrane/internal/observability/metrics"
	"github.com/inferencepath/membrane/internal/server/chat"
	"github.com/inferencepath/membrane/internal/types"
	fakeoa "github.com/inferencepath/membrane/test/fakes/openai"
	"github.com/inferencepath/membrane/test/harness"
)

// TestReloadableNilSnapshotFailsClosed checks that a nil Snapshot yields 503.
func TestReloadableNilSnapshotFailsClosed(t *testing.T) {
	h := chat.NewReloadable(func() *chat.Snapshot { return nil },
		slog.New(slog.NewTextHandler(io.Discard, nil)), metrics.Nop{})
	ts := httptest.NewServer(h)
	defer ts.Close()

	resp, err := http.Post(ts.URL+chat.Route, "application/json", strings.NewReader(`{}`))
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "unavailable") {
		t.Fatalf("body = %s, want unavailable code", body)
	}
}

// TestChatOptionalParams checks that sampling parameters reach the upstream body.
func TestChatOptionalParams(t *testing.T) {
	t.Setenv(harness.EgressEnv, harness.EgressToken)
	fake := fakeoa.New(fakeoa.Config{Content: "ok", FinishReason: "stop"})
	defer fake.Close()
	h, _ := harness.BuildChat(fake.URL(), nil)
	ts := httptest.NewServer(h)
	defer ts.Close()

	body := `{"pathway":"chat","temperature":0.5,"max_tokens":64,"top_p":0.9,"stop":["\n"],"messages":[{"role":"user","content":"hi"}]}`
	resp := post(t, ts.URL+chat.Route, harness.CallerKey, body)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	up := string(fake.Last().Body)
	for _, want := range []string{`"temperature":0.5`, `"max_tokens":64`, `"top_p":0.9`, `"stop":`} {
		if !strings.Contains(up, want) {
			t.Fatalf("upstream body missing %s: %s", want, up)
		}
	}
}

// TestChatBearerEmptyToken covers the empty-token branch of bearer parsing.
func TestChatBearerEmptyToken(t *testing.T) {
	ts := httptest.NewServer(handlerWith(t, errTransport{err: errors.New("unused")}))
	defer ts.Close()
	req, _ := http.NewRequest(http.MethodPost, ts.URL+chat.Route, strings.NewReader(`{"pathway":"chat","messages":[{"role":"user","content":"hi"}]}`))
	req.Header.Set("Authorization", "Bearer    ") // whitespace-only token
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", resp.StatusCode)
	}
}

// TestChatDanglingToolResult checks that a tool result with no prior tool_call
// is rejected with 400 without calling the transport.
func TestChatDanglingToolResult(t *testing.T) {
	ts := httptest.NewServer(handlerWith(t, errTransport{err: errors.New("transport must not be called")}))
	defer ts.Close()
	body := `{"pathway":"chat","messages":[` +
		`{"role":"user","content":"weather?"},` +
		`{"role":"tool","tool_call_id":"missing","content":"{}"}]}`
	resp := post(t, ts.URL+chat.Route, harness.CallerKey, body)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
}

// TestChatToolResultWithPriorCallOK checks that a well-formed tool loop is accepted.
func TestChatToolResultWithPriorCallOK(t *testing.T) {
	ts := httptest.NewServer(handlerWith(t, successTransport{}))
	defer ts.Close()
	body := `{"pathway":"chat","messages":[` +
		`{"role":"user","content":"weather?"},` +
		`{"role":"assistant","content":"","tool_calls":[{"id":"call_1","name":"f","arguments":{"x":1}}]},` +
		`{"role":"tool","tool_call_id":"call_1","content":"{}"}]}`
	resp := post(t, ts.URL+chat.Route, harness.CallerKey, body)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
}

// toolPolicySpec allows tools on the chat Pathway, with a tool_policy limited
// to the "openai" destination.
const toolPolicySpec = `version: 1
pathways:
  chat:
    outbound:
      allow: [messages, tools, tool_choice, stream]
    inbound:
      default: [message, finish_reason, usage]
      on_reveal: [provider, model]
    tool_policy:
      tools_allow: [get_weather]
      tools_deny: [shell_exec]
      max_tools: 2
      max_tool_result_bytes: 16
      redact: ['secret-\w+']
      destinations: [openai]
`

func toolPolicyHandler(t *testing.T, fakeURL string) http.Handler {
	t.Helper()
	h, err := harness.BuildChatPermeability(fakeURL, nil, toolPolicySpec, nil)
	if err != nil {
		t.Fatal(err)
	}
	return h
}

// TestToolPolicyDeniesNonAllowlistedTool checks that a tool outside tools_allow
// is rejected before crossing the Membrane.
func TestToolPolicyDeniesNonAllowlistedTool(t *testing.T) {
	t.Setenv(harness.EgressEnv, harness.EgressToken)
	fake := fakeoa.New(fakeoa.Config{Content: "ok", FinishReason: "stop"})
	defer fake.Close()
	ts := httptest.NewServer(toolPolicyHandler(t, fake.URL()))
	defer ts.Close()

	body := `{"pathway":"chat","tools":[{"name":"search"}],"messages":[{"role":"user","content":"hi"}]}`
	resp := post(t, ts.URL+chat.Route, harness.CallerKey, body)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
	if len(fake.Last().Body) != 0 {
		t.Fatal("request crossed the Membrane despite policy rejection")
	}
}

// TestToolPolicyDeniedTool checks that a tool on tools_deny is rejected.
func TestToolPolicyDeniedTool(t *testing.T) {
	t.Setenv(harness.EgressEnv, harness.EgressToken)
	fake := fakeoa.New(fakeoa.Config{Content: "ok", FinishReason: "stop"})
	defer fake.Close()
	ts := httptest.NewServer(toolPolicyHandler(t, fake.URL()))
	defer ts.Close()

	body := `{"pathway":"chat","tools":[{"name":"shell_exec"}],"messages":[{"role":"user","content":"hi"}]}`
	resp := post(t, ts.URL+chat.Route, harness.CallerKey, body)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
}

// TestToolPolicyCaps checks that max_tools and max_tool_result_bytes are enforced.
func TestToolPolicyCaps(t *testing.T) {
	t.Setenv(harness.EgressEnv, harness.EgressToken)
	fake := fakeoa.New(fakeoa.Config{Content: "ok", FinishReason: "stop"})
	defer fake.Close()
	ts := httptest.NewServer(toolPolicyHandler(t, fake.URL()))
	defer ts.Close()

	// Three allowed tools against max_tools: 2.
	tooMany := `{"pathway":"chat","tools":[{"name":"get_weather"},{"name":"get_weather"},{"name":"get_weather"}],"messages":[{"role":"user","content":"hi"}]}`
	resp := post(t, ts.URL+chat.Route, harness.CallerKey, tooMany)
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("max_tools: status = %d, want 400", resp.StatusCode)
	}

	// A tool result longer than max_tool_result_bytes: 16.
	big := `{"pathway":"chat","messages":[` +
		`{"role":"assistant","content":"","tool_calls":[{"id":"c1","name":"get_weather","arguments":{}}]},` +
		`{"role":"tool","tool_call_id":"c1","content":"this result is definitely longer than sixteen bytes"}]}`
	resp2 := post(t, ts.URL+chat.Route, harness.CallerKey, big)
	resp2.Body.Close()
	if resp2.StatusCode != http.StatusBadRequest {
		t.Fatalf("max_tool_result_bytes: status = %d, want 400", resp2.StatusCode)
	}
}

// TestToolPolicyDestinationScope checks that tools are rejected when the bound
// provider ("openai") is not in tool_policy.destinations.
func TestToolPolicyDestinationScope(t *testing.T) {
	t.Setenv(harness.EgressEnv, harness.EgressToken)
	fake := fakeoa.New(fakeoa.Config{Content: "ok", FinishReason: "stop"})
	defer fake.Close()
	spec := strings.Replace(toolPolicySpec, "destinations: [openai]", "destinations: [internal-llm]", 1)
	h, err := harness.BuildChatPermeability(fake.URL(), nil, spec, nil)
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(h)
	defer ts.Close()

	body := `{"pathway":"chat","tools":[{"name":"get_weather"}],"messages":[{"role":"user","content":"hi"}]}`
	resp := post(t, ts.URL+chat.Route, harness.CallerKey, body)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
	if len(fake.Last().Body) != 0 {
		t.Fatal("tool request crossed to a non-enabled destination")
	}
}

// TestToolPolicyRedactsResultBeforeEgress checks that redact patterns apply to
// tool results before they cross the Membrane.
func TestToolPolicyRedactsResultBeforeEgress(t *testing.T) {
	t.Setenv(harness.EgressEnv, harness.EgressToken)
	fake := fakeoa.New(fakeoa.Config{Content: "ok", FinishReason: "stop"})
	defer fake.Close()
	// Raise the byte cap so the result is redacted rather than rejected.
	spec := strings.Replace(toolPolicySpec, "max_tool_result_bytes: 16", "max_tool_result_bytes: 4096", 1)
	h, err := harness.BuildChatPermeability(fake.URL(), nil, spec, nil)
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(h)
	defer ts.Close()

	body := `{"pathway":"chat","messages":[` +
		`{"role":"assistant","content":"","tool_calls":[{"id":"c1","name":"get_weather","arguments":{}}]},` +
		`{"role":"tool","tool_call_id":"c1","content":"token is secret-abc123 ok"}]}`
	resp := post(t, ts.URL+chat.Route, harness.CallerKey, body)
	resp.Body.Close()
	up := string(fake.Last().Body)
	if strings.Contains(up, "secret-abc123") {
		t.Fatalf("DIFFUSION LEAK: unredacted tool result crossed: %s", up)
	}
	if !strings.Contains(up, "[REDACTED]") {
		t.Fatalf("redaction placeholder not present upstream: %s", up)
	}
}

// capSink captures audit records for assertions.
type capSink struct{ records []audit.Record }

func (c *capSink) Write(r audit.Record) { c.records = append(c.records, r) }

// TestToolPolicyAuditCountsOnly checks that the audit record holds tool counts
// but no tool names, arguments or results.
func TestToolPolicyAuditCountsOnly(t *testing.T) {
	t.Setenv(harness.EgressEnv, harness.EgressToken)
	fake := fakeoa.New(fakeoa.Config{
		ToolCalls:    []fakeoa.ToolCall{{ID: "c1", Name: "get_weather", Arguments: `{"city":"NYC"}`}},
		FinishReason: "tool_calls",
	})
	defer fake.Close()
	sink := &capSink{}
	spec := strings.Replace(toolPolicySpec, "max_tool_result_bytes: 16", "max_tool_result_bytes: 4096", 1)
	h, err := harness.BuildChatPermeability(fake.URL(), nil, spec, []chat.HandlerOption{chat.WithAudit(sink)})
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(h)
	defer ts.Close()

	body := `{"pathway":"chat",` +
		`"tools":[{"name":"get_weather"}],` +
		`"messages":[` +
		`{"role":"user","content":"weather?"},` +
		`{"role":"assistant","content":"","tool_calls":[{"id":"c0","name":"get_weather","arguments":{}}]},` +
		`{"role":"tool","tool_call_id":"c0","content":"{}"}]}`
	resp := post(t, ts.URL+chat.Route, harness.CallerKey, body)
	resp.Body.Close()

	if len(sink.records) != 1 {
		t.Fatalf("want 1 audit record, got %d", len(sink.records))
	}
	rec := sink.records[0]
	if rec.ToolsOffered != 1 || rec.ToolTurnsSent != 1 || rec.ToolCallsReturned != 1 {
		t.Fatalf("counts = offered:%d turns:%d returned:%d", rec.ToolsOffered, rec.ToolTurnsSent, rec.ToolCallsReturned)
	}
	b, _ := json.Marshal(rec)
	for _, forbidden := range []string{"get_weather", "city", "NYC", "weather?"} {
		if strings.Contains(string(b), forbidden) {
			t.Fatalf("audit record leaked tool content %q: %s", forbidden, b)
		}
	}
}

// noFlushWriter is an http.ResponseWriter without http.Flusher.
type noFlushWriter struct {
	header http.Header
	code   int
}

func (w *noFlushWriter) Header() http.Header {
	if w.header == nil {
		w.header = http.Header{}
	}
	return w.header
}
func (w *noFlushWriter) Write(b []byte) (int, error) { return len(b), nil }
func (w *noFlushWriter) WriteHeader(code int)        { w.code = code }

// TestChatStreamUnsupported checks that streaming without a Flusher returns 500.
func TestChatStreamUnsupported(t *testing.T) {
	h := handlerWith(t, errTransport{err: errors.New("unused")})
	req, _ := http.NewRequest(http.MethodPost, chat.Route, strings.NewReader(`{"pathway":"chat","stream":true,"messages":[{"role":"user","content":"hi"}]}`))
	req.Header.Set("Authorization", "Bearer "+harness.CallerKey)
	w := &noFlushWriter{}
	h.ServeHTTP(w, req)
	if w.code != http.StatusInternalServerError {
		t.Fatalf("code = %d, want 500", w.code)
	}
}

// midErrTransport emits one chunk and then fails.
type midErrTransport struct{}

func (midErrTransport) Transport(context.Context, scrub.SanitizedRequest) (types.CanonicalResponse, error) {
	return types.CanonicalResponse{}, errors.New("unused")
}

func (midErrTransport) TransportStream(_ context.Context, _ scrub.SanitizedRequest, emit func(types.StreamChunk) error) error {
	_ = emit(types.StreamChunk{Delta: "partial"})
	return errors.New("upstream dropped mid-stream")
}

// successTransport returns a fixed response with token usage.
type successTransport struct{}

func (successTransport) Transport(context.Context, scrub.SanitizedRequest) (types.CanonicalResponse, error) {
	return types.CanonicalResponse{
		Message:      types.Message{Role: "assistant", Content: "ok"},
		FinishReason: "stop",
		Usage:        types.Usage{PromptTokens: 5, CompletionTokens: 7, TotalTokens: 12},
		Provider:     "openai",
		Model:        "m",
	}, nil
}

func (successTransport) TransportStream(context.Context, scrub.SanitizedRequest, func(types.StreamChunk) error) error {
	return nil
}

func handlerWithMetrics(t *testing.T, trans transporter.Transporter, rec *metrics.Registry) http.Handler {
	t.Helper()
	spec, err := permeability.Parse([]byte("version: 1\npathways:\n  chat:\n    outbound:\n      allow: [messages, stream]\n    inbound:\n      default: [message, finish_reason, usage]\n"))
	if err != nil {
		t.Fatal(err)
	}
	store, err := admit.ParseStore([]byte("callers:\n  - key_id: ck_demo\n    caller_id: acme\n    hash: " + admit.HashSecret("s3cr3t-demo-key") + "\n"))
	if err != nil {
		t.Fatal(err)
	}
	resolver := resolve.New(map[types.PathwayID]resolve.Binding{"chat": {Provider: "openai", Model: "m"}})
	return chat.New(spec, store, resolver, trans, slog.New(slog.NewTextHandler(io.Discard, nil)), rec)
}

func TestChatMetricsRecorded(t *testing.T) {
	reg := metrics.NewRegistry()
	ts := httptest.NewServer(handlerWithMetrics(t, successTransport{}, reg))
	defer ts.Close()

	// One success (with token usage) and one upstream error.
	resp := post(t, ts.URL+chat.Route, harness.CallerKey, `{"pathway":"chat","messages":[{"role":"user","content":"hi"}]}`)
	resp.Body.Close()

	errTS := httptest.NewServer(handlerWithMetrics(t, errTransport{err: providers.UpstreamStatusError{Status: 500}}, reg))
	defer errTS.Close()
	resp2 := post(t, errTS.URL+chat.Route, harness.CallerKey, `{"pathway":"chat","messages":[{"role":"user","content":"hi"}]}`)
	resp2.Body.Close()

	var sb strings.Builder
	reg.WriteProm(&sb)
	out := sb.String()
	for _, want := range []string{
		`inferencepath_chat_requests_total{pathway="chat",stream="false",outcome="success"} 1`,
		`inferencepath_chat_requests_total{pathway="chat",stream="false",outcome="upstream_error"} 1`,
		`inferencepath_chat_tokens_total{pathway="chat",kind="total"} 12`,
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("metrics missing %q:\n%s", want, out)
		}
	}
}

func TestChatStreamMidError(t *testing.T) {
	ts := httptest.NewServer(handlerWith(t, midErrTransport{}))
	defer ts.Close()
	resp := post(t, ts.URL+chat.Route, harness.CallerKey, `{"pathway":"chat","stream":true,"messages":[{"role":"user","content":"hi"}]}`)
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	body := string(raw)
	if !strings.Contains(body, `"delta":"partial"`) {
		t.Fatalf("expected relayed partial chunk: %s", body)
	}
	if !strings.Contains(body, "event: error") {
		t.Fatalf("expected terminal error event after partial relay: %s", body)
	}
	// The terminal event must stay provider-neutral.
	if strings.Contains(strings.ToLower(body), "openai") || strings.Contains(body, "upstream dropped mid-stream") {
		t.Fatalf("terminal error leaked internals: %s", body)
	}
}
