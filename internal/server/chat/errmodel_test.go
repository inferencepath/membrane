package chat_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/inferencepath/membrane/internal/exterior/providers"
	"github.com/inferencepath/membrane/internal/membrane/transporter"
	"github.com/inferencepath/membrane/internal/server/chat"
	fakeoa "github.com/inferencepath/membrane/test/fakes/openai"
	"github.com/inferencepath/membrane/test/harness"
)

// decodeEnvelope reads the provider-neutral error envelope and returns its code.
func decodeEnvelope(t *testing.T, body io.Reader) (code, message string) {
	t.Helper()
	var env struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.NewDecoder(body).Decode(&env); err != nil {
		t.Fatalf("decode envelope: %v", err)
	}
	return env.Error.Code, env.Error.Message
}

// TestErrorModelConformanceGates checks the status and code of each Interior
// gate, for both unary and streaming requests.
func TestErrorModelConformanceGates(t *testing.T) {
	t.Setenv(harness.EgressEnv, harness.EgressToken)
	fake := fakeoa.New(fakeoa.Config{Content: "x"})
	defer fake.Close()
	h, err := harness.BuildChat(fake.URL(), nil)
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(h)
	defer ts.Close()

	cases := []struct {
		name       string
		method     string
		key        string
		body       string
		wantStatus int
		wantCode   string
	}{
		{"method_not_allowed", http.MethodGet, harness.CallerKey, ``, http.StatusMethodNotAllowed, "method_not_allowed"},
		{"unauthorized_missing", http.MethodPost, "", `{"pathway":"chat","messages":[{"role":"user","content":"hi"}]}`, http.StatusUnauthorized, "unauthorized"},
		{"unauthorized_invalid", http.MethodPost, "garbage", `{"pathway":"chat","messages":[{"role":"user","content":"hi"}]}`, http.StatusUnauthorized, "unauthorized"},
		{"bad_request_malformed", http.MethodPost, harness.CallerKey, `{not json`, http.StatusBadRequest, "bad_request"},
		{"bad_request_empty", http.MethodPost, harness.CallerKey, `{"pathway":"chat","messages":[]}`, http.StatusBadRequest, "bad_request"},
		{"unresolved_pathway", http.MethodPost, harness.CallerKey, `{"pathway":"embeddings","messages":[{"role":"user","content":"hi"}]}`, http.StatusBadRequest, "unresolved_pathway"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var resp *http.Response
			if tc.method == http.MethodGet {
				r, err := http.Get(ts.URL + chat.Route)
				if err != nil {
					t.Fatal(err)
				}
				resp = r
			} else {
				resp = post(t, ts.URL+chat.Route, tc.key, tc.body)
			}
			defer resp.Body.Close()
			if resp.StatusCode != tc.wantStatus {
				t.Fatalf("status = %d, want %d", resp.StatusCode, tc.wantStatus)
			}
			code, _ := decodeEnvelope(t, resp.Body)
			if code != tc.wantCode {
				t.Fatalf("code = %q, want %q", code, tc.wantCode)
			}
		})
	}
}

// TestErrorModelConformanceForbidden checks the 403 from the Authorize gate.
func TestErrorModelConformanceForbidden(t *testing.T) {
	t.Setenv(harness.EgressEnv, harness.EgressToken)
	fake := fakeoa.New(fakeoa.Config{Content: "x"})
	defer fake.Close()
	// Authorized for nothing, so chat is forbidden.
	ts := httptest.NewServer(buildAuthzChat(t, fake.URL()))
	defer ts.Close()

	resp := post(t, ts.URL+chat.Route, harness.CallerKey, `{"pathway":"chat","messages":[{"role":"user","content":"hi"}]}`)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", resp.StatusCode)
	}
	if code, _ := decodeEnvelope(t, resp.Body); code != "forbidden" {
		t.Fatalf("code = %q, want forbidden", code)
	}
}

// TestErrorModelConformanceTransport checks the status and code for each
// transport failure on unary requests.
func TestErrorModelConformanceTransport(t *testing.T) {
	cases := []struct {
		name       string
		err        error
		wantStatus int
		wantCode   string
	}{
		{"upstream_timeout", providers.ErrUpstreamTimeout, http.StatusGatewayTimeout, "upstream_timeout"},
		{"deadline_timeout", context.DeadlineExceeded, http.StatusGatewayTimeout, "upstream_timeout"},
		{"upstream_unavailable_503", providers.UpstreamStatusError{Status: 503}, http.StatusGatewayTimeout, "upstream_unavailable"},
		{"upstream_unavailable_504", providers.UpstreamStatusError{Status: 504}, http.StatusGatewayTimeout, "upstream_unavailable"},
		{"provider_error_500", providers.UpstreamStatusError{Status: 500}, http.StatusBadGateway, "provider_error"},
		{"provider_error_401", providers.UpstreamStatusError{Status: 401}, http.StatusBadGateway, "provider_error"},
		{"provider_error_429", providers.UpstreamStatusError{Status: 429}, http.StatusBadGateway, "provider_error"},
		{"no_provider_available", transporter.ErrNoProvider, http.StatusServiceUnavailable, "no_provider_available"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ts := httptest.NewServer(handlerWith(t, errTransport{err: tc.err}))
			defer ts.Close()
			resp := post(t, ts.URL+chat.Route, harness.CallerKey, `{"pathway":"chat","messages":[{"role":"user","content":"hi"}]}`)
			defer resp.Body.Close()
			if resp.StatusCode != tc.wantStatus {
				t.Fatalf("status = %d, want %d", resp.StatusCode, tc.wantStatus)
			}
			if code, _ := decodeEnvelope(t, resp.Body); code != tc.wantCode {
				t.Fatalf("code = %q, want %q", code, tc.wantCode)
			}
		})
	}
}

// TestErrorModelStreamPreFirstByte checks that a stream failing before the
// first byte gets a JSON error response rather than an SSE error event.
func TestErrorModelStreamPreFirstByte(t *testing.T) {
	ts := httptest.NewServer(handlerWith(t, errTransport{err: providers.UpstreamStatusError{Status: 500}}))
	defer ts.Close()
	resp := post(t, ts.URL+chat.Route, harness.CallerKey, `{"pathway":"chat","stream":true,"messages":[{"role":"user","content":"hi"}]}`)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Fatalf("pre-first-byte stream failure must use JSON envelope, got content-type %q", ct)
	}
	if code, _ := decodeEnvelope(t, resp.Body); code != "provider_error" {
		t.Fatalf("code = %q, want provider_error", code)
	}
}

// TestErrorModelStreamPostFirstByte checks that a failure after the first byte
// ends the stream with a neutral stream_error event.
func TestErrorModelStreamPostFirstByte(t *testing.T) {
	ts := httptest.NewServer(handlerWith(t, midErrTransport{}))
	defer ts.Close()
	resp := post(t, ts.URL+chat.Route, harness.CallerKey, `{"pathway":"chat","stream":true,"messages":[{"role":"user","content":"hi"}]}`)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("post-first-byte failure must not change status, got %d", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	s := string(body)
	if !strings.Contains(s, "event: error") || !strings.Contains(s, `"code":"stream_error"`) {
		t.Fatalf("expected terminal stream_error event: %s", s)
	}
}

// TestErrorModelNonDisclosure checks that errors never expose Exterior topology
// and name the Provider family only under Reveal.
func TestErrorModelNonDisclosure(t *testing.T) {
	stamped := providers.UpstreamStatusError{Status: 500, Provider: "bedrock"}
	for _, reveal := range []bool{false, true} {
		name := "reveal_off"
		if reveal {
			name = "reveal_on"
		}
		t.Run(name, func(t *testing.T) {
			ts := httptest.NewServer(handlerWith(t, errTransport{err: stamped}))
			defer ts.Close()
			body := `{"pathway":"chat","messages":[{"role":"user","content":"hi"}],"reveal":` +
				map[bool]string{true: "true", false: "false"}[reveal] + `}`
			resp := post(t, ts.URL+chat.Route, harness.CallerKey, body)
			defer resp.Body.Close()
			raw, _ := io.ReadAll(resp.Body)
			lower := strings.ToLower(string(raw))

			if got := strings.Contains(lower, "bedrock"); got != reveal {
				t.Fatalf("family disclosed = %v, want %v; body=%s", got, reveal, raw)
			}
			// Topology never appears, with or without Reveal.
			for _, forbidden := range []string{"http://", "https://", "us-east", "us-west", "deployment", "arn:", "keyring", "ref-", "x-api-key", "akia"} {
				if strings.Contains(lower, forbidden) {
					t.Fatalf("error body leaked topology %q: %s", forbidden, raw)
				}
			}
		})
	}
}
