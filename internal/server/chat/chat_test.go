package chat_test

import (
	"bytes"
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

func post(t *testing.T, url, key, body string) *http.Response {
	t.Helper()
	httpReq, err := http.NewRequest(http.MethodPost, url, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if key != "" {
		httpReq.Header.Set("Authorization", "Bearer "+key)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(httpReq)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func TestChatCompletionNeutral(t *testing.T) {
	t.Setenv(harness.EgressEnv, harness.EgressToken)
	fake := fakeoa.New(fakeoa.Config{Content: "hi there", Model: "backing", FinishReason: "stop", TotalTokens: 9})
	defer fake.Close()
	h, err := harness.BuildChat(fake.URL(), nil)
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(h)
	defer ts.Close()

	resp := post(t, ts.URL+chat.Route, harness.CallerKey, `{"pathway":"chat","messages":[{"role":"user","content":"hello"}]}`)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	var out types.ChatResponse
	_ = json.NewDecoder(resp.Body).Decode(&out)
	if out.Message.Content != "hi there" || out.FinishReason != "stop" {
		t.Fatalf("response = %+v", out)
	}
	if out.Provider != "" || out.Model != "" {
		t.Fatalf("default response must be provider-neutral: provider=%q model=%q", out.Provider, out.Model)
	}
}

func TestChatReveal(t *testing.T) {
	t.Setenv(harness.EgressEnv, harness.EgressToken)
	fake := fakeoa.New(fakeoa.Config{Content: "hi", Model: "backing-model", FinishReason: "stop"})
	defer fake.Close()
	h, _ := harness.BuildChat(fake.URL(), nil)
	ts := httptest.NewServer(h)
	defer ts.Close()

	resp := post(t, ts.URL+chat.Route, harness.CallerKey, `{"pathway":"chat","reveal":true,"messages":[{"role":"user","content":"hi"}]}`)
	defer resp.Body.Close()
	var out types.ChatResponse
	_ = json.NewDecoder(resp.Body).Decode(&out)
	if out.Provider != "openai" || out.Model != "backing-model" {
		t.Fatalf("reveal response must include provider/model: %+v", out)
	}
}

func TestChatUnauthorized(t *testing.T) {
	t.Setenv(harness.EgressEnv, harness.EgressToken)
	fake := fakeoa.New(fakeoa.Config{Content: "x"})
	defer fake.Close()
	h, _ := harness.BuildChat(fake.URL(), nil)
	ts := httptest.NewServer(h)
	defer ts.Close()

	for _, key := range []string{"", "garbage", "ck_demo.wrong"} {
		resp := post(t, ts.URL+chat.Route, key, `{"pathway":"chat","messages":[{"role":"user","content":"hi"}]}`)
		if resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("key %q: status = %d, want 401", key, resp.StatusCode)
		}
		resp.Body.Close()
	}
	// No Provider contact on rejected requests.
	if fake.Last().Body != nil {
		t.Fatalf("provider was contacted on an unauthorized request")
	}
}

func TestChatValidation(t *testing.T) {
	t.Setenv(harness.EgressEnv, harness.EgressToken)
	fake := fakeoa.New(fakeoa.Config{Content: "x"})
	defer fake.Close()
	h, _ := harness.BuildChat(fake.URL(), nil)
	ts := httptest.NewServer(h)
	defer ts.Close()

	cases := []struct {
		name, body string
		status     int
	}{
		{"malformed", `{not json`, http.StatusBadRequest},
		{"missing pathway", `{"messages":[{"role":"user","content":"hi"}]}`, http.StatusBadRequest},
		{"no messages", `{"pathway":"chat","messages":[]}`, http.StatusBadRequest},
		{"unknown field", `{"pathway":"chat","messages":[{"role":"user","content":"hi"}],"bogus":1}`, http.StatusBadRequest},
		{"unresolved pathway", `{"pathway":"embeddings","messages":[{"role":"user","content":"hi"}]}`, http.StatusBadRequest},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp := post(t, ts.URL+chat.Route, harness.CallerKey, tc.body)
			defer resp.Body.Close()
			if resp.StatusCode != tc.status {
				t.Fatalf("status = %d, want %d", resp.StatusCode, tc.status)
			}
		})
	}
}

func TestChatMethodNotAllowed(t *testing.T) {
	t.Setenv(harness.EgressEnv, harness.EgressToken)
	fake := fakeoa.New(fakeoa.Config{Content: "x"})
	defer fake.Close()
	h, _ := harness.BuildChat(fake.URL(), nil)
	ts := httptest.NewServer(h)
	defer ts.Close()

	resp, err := http.Get(ts.URL + chat.Route)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", resp.StatusCode)
	}
}

func TestChatStreaming(t *testing.T) {
	t.Setenv(harness.EgressEnv, harness.EgressToken)
	fake := fakeoa.New(fakeoa.Config{StreamDeltas: []string{"Hel", "lo"}, FinishReason: "stop"})
	defer fake.Close()
	h, _ := harness.BuildChat(fake.URL(), nil)
	ts := httptest.NewServer(h)
	defer ts.Close()

	resp := post(t, ts.URL+chat.Route, harness.CallerKey, `{"pathway":"chat","stream":true,"messages":[{"role":"user","content":"hi"}]}`)
	defer resp.Body.Close()
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Fatalf("content-type = %q", ct)
	}
	body, _ := io.ReadAll(resp.Body)
	s := string(body)
	if !strings.Contains(s, `"delta":"Hel"`) || !strings.Contains(s, `"delta":"lo"`) {
		t.Fatalf("stream missing deltas: %s", s)
	}
	if !strings.Contains(s, "data: [DONE]") {
		t.Fatalf("stream missing terminal [DONE]: %s", s)
	}
}

// Error mapping via a fake transporter.

type errTransport struct{ err error }

func (e errTransport) Transport(context.Context, scrub.SanitizedRequest) (types.CanonicalResponse, error) {
	return types.CanonicalResponse{}, e.err
}

func (e errTransport) TransportStream(context.Context, scrub.SanitizedRequest, func(types.StreamChunk) error) error {
	return e.err
}

func handlerWith(t *testing.T, trans transporter.Transporter) http.Handler {
	t.Helper()
	spec, err := permeability.Parse([]byte("version: 1\npathways:\n  chat:\n    outbound:\n      allow: [messages, stream]\n    inbound:\n      default: [message, finish_reason, usage]\n      on_reveal: [provider, model]\n"))
	if err != nil {
		t.Fatal(err)
	}
	store, err := admit.ParseStore([]byte("callers:\n  - key_id: ck_demo\n    caller_id: acme\n    hash: " + admit.HashSecret("s3cr3t-demo-key") + "\n"))
	if err != nil {
		t.Fatal(err)
	}
	resolver := resolve.New(map[types.PathwayID]resolve.Binding{"chat": {Provider: "openai", Model: "m"}})
	return chat.New(spec, store, resolver, trans, slog.New(slog.NewTextHandler(io.Discard, nil)), metrics.Nop{})
}

func TestChatErrorMapping(t *testing.T) {
	cases := []struct {
		name   string
		err    error
		status int
	}{
		{"timeout", providers.ErrUpstreamTimeout, http.StatusGatewayTimeout},
		{"deadline", context.DeadlineExceeded, http.StatusGatewayTimeout},
		{"upstream 500", providers.UpstreamStatusError{Status: 500}, http.StatusBadGateway},
		{"upstream 401 auth", providers.UpstreamStatusError{Status: 401}, http.StatusBadGateway},
		{"upstream 429", providers.UpstreamStatusError{Status: 429}, http.StatusBadGateway},
		{"upstream 503 unavailable", providers.UpstreamStatusError{Status: 503}, http.StatusGatewayTimeout},
		{"upstream 504 unavailable", providers.UpstreamStatusError{Status: 504}, http.StatusGatewayTimeout},
		{"upstream 400 request", providers.UpstreamStatusError{Status: 400}, http.StatusBadGateway},
		{"no provider", transporter.ErrNoProvider, http.StatusServiceUnavailable},
		{"generic", errors.New("boom"), http.StatusBadGateway},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ts := httptest.NewServer(handlerWith(t, errTransport{err: tc.err}))
			defer ts.Close()
			resp := post(t, ts.URL+chat.Route, harness.CallerKey, `{"pathway":"chat","messages":[{"role":"user","content":"hi"}]}`)
			defer resp.Body.Close()
			if resp.StatusCode != tc.status {
				t.Fatalf("status = %d, want %d", resp.StatusCode, tc.status)
			}
			body, _ := io.ReadAll(resp.Body)
			if strings.Contains(strings.ToLower(string(body)), "openai") {
				t.Fatalf("error body leaked provider identity: %s", body)
			}
		})
	}
}

// TestChatErrorNonDisclosure checks that errors name the Provider family only
// under Reveal.
func TestChatErrorNonDisclosure(t *testing.T) {
	stamped := providers.UpstreamStatusError{Status: 500, Provider: "anthropic"}

	cases := []struct {
		name       string
		reveal     bool
		wantFamily bool
	}{
		{"reveal off hides family", false, false},
		{"reveal on names family", true, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ts := httptest.NewServer(handlerWith(t, errTransport{err: stamped}))
			defer ts.Close()
			body := `{"pathway":"chat","messages":[{"role":"user","content":"hi"}],"reveal":` +
				map[bool]string{true: "true", false: "false"}[tc.reveal] + `}`
			resp := post(t, ts.URL+chat.Route, harness.CallerKey, body)
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusBadGateway {
				t.Fatalf("status = %d, want 502", resp.StatusCode)
			}
			raw, _ := io.ReadAll(resp.Body)
			got := strings.Contains(strings.ToLower(string(raw)), "anthropic")
			if got != tc.wantFamily {
				t.Fatalf("family disclosed = %v, want %v; body=%s", got, tc.wantFamily, raw)
			}
			// Endpoint, region and deployment detail never appear.
			for _, forbidden := range []string{"http://", "https://", "us-east", "deployment", "api-key", "x-api-key"} {
				if strings.Contains(strings.ToLower(string(raw)), forbidden) {
					t.Fatalf("error body leaked egress detail %q: %s", forbidden, raw)
				}
			}
		})
	}
}

func TestChatLogsAreCleanOfSecretsAndIdentity(t *testing.T) {
	t.Setenv(harness.EgressEnv, harness.EgressToken)
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, nil))
	fake := fakeoa.New(fakeoa.Config{Content: "hi", Model: "backing"})
	defer fake.Close()
	h, err := harness.BuildChat(fake.URL(), logger)
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(h)
	defer ts.Close()

	// Identity fields in the body that the Scrub must drop.
	resp := post(t, ts.URL+chat.Route, harness.CallerKey,
		`{"pathway":"chat","messages":[{"role":"user","content":"hi"}]}`)
	resp.Body.Close()

	logs := buf.String()
	for _, forbidden := range []string{harness.EgressToken, "s3cr3t-demo-key", harness.CallerKey} {
		if strings.Contains(logs, forbidden) {
			t.Fatalf("logs leaked %q: %s", forbidden, logs)
		}
	}
}
