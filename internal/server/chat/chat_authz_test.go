package chat_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/inferencepath/membrane/internal/interior/authorize"
	"github.com/inferencepath/membrane/internal/observability/metrics"
	"github.com/inferencepath/membrane/internal/server/chat"
	fakeoa "github.com/inferencepath/membrane/test/fakes/openai"
	"github.com/inferencepath/membrane/test/harness"
)

// buildAuthzChat returns a chat Handler whose Caller (acme-app) is authorized
// for the given Pathways only.
func buildAuthzChat(t *testing.T, endpoint string, allow ...string) http.Handler {
	t.Helper()
	store, err := harness.CallerStoreWithAllow(allow...)
	if err != nil {
		t.Fatalf("caller store: %v", err)
	}
	h, err := harness.BuildChat(endpoint, nil, chat.WithAuthorizer(authorize.New(store)))
	if err != nil {
		t.Fatalf("build chat: %v", err)
	}
	return h
}

func TestAuthorizeAllowedPathwayPasses(t *testing.T) {
	t.Setenv(harness.EgressEnv, harness.EgressToken)
	fake := fakeoa.New(fakeoa.Config{Content: "ok", Model: "m", FinishReason: "stop"})
	defer fake.Close()
	ts := httptest.NewServer(buildAuthzChat(t, fake.URL(), "chat"))
	defer ts.Close()

	resp := post(t, ts.URL+chat.Route, harness.CallerKey, `{"pathway":"chat","messages":[{"role":"user","content":"hi"}]}`)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("authorized request status = %d, want 200", resp.StatusCode)
	}
}

func TestAuthorizeDeniedForbidden(t *testing.T) {
	t.Setenv(harness.EgressEnv, harness.EgressToken)
	fake := fakeoa.New(fakeoa.Config{Content: "x"})
	defer fake.Close()
	// Authorized for "summarize" only; a chat request must be denied.
	ts := httptest.NewServer(buildAuthzChat(t, fake.URL(), "summarize"))
	defer ts.Close()

	resp := post(t, ts.URL+chat.Route, harness.CallerKey, `{"pathway":"chat","messages":[{"role":"user","content":"hi"}]}`)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("unauthorized pathway status = %d, want 403", resp.StatusCode)
	}
	// The denied request must not reach the Provider.
	if fake.Last().Body != nil {
		t.Fatalf("provider was contacted on a forbidden request")
	}
}

func TestAuthorizeDenyByDefault(t *testing.T) {
	t.Setenv(harness.EgressEnv, harness.EgressToken)
	fake := fakeoa.New(fakeoa.Config{Content: "x"})
	defer fake.Close()
	// An empty allowlist authorizes nothing.
	ts := httptest.NewServer(buildAuthzChat(t, fake.URL()))
	defer ts.Close()

	resp := post(t, ts.URL+chat.Route, harness.CallerKey, `{"pathway":"chat","messages":[{"role":"user","content":"hi"}]}`)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("deny-by-default status = %d, want 403", resp.StatusCode)
	}
	if fake.Last().Body != nil {
		t.Fatalf("provider was contacted under deny-by-default")
	}
}

// TestAuthorizeDecisionsObserved checks that allow and deny decisions are counted.
func TestAuthorizeDecisionsObserved(t *testing.T) {
	t.Setenv(harness.EgressEnv, harness.EgressToken)
	fake := fakeoa.New(fakeoa.Config{Content: "ok", Model: "m", FinishReason: "stop"})
	defer fake.Close()

	store, err := harness.CallerStoreWithAllow("chat")
	if err != nil {
		t.Fatalf("caller store: %v", err)
	}
	reg := metrics.NewRegistry()
	h, err := harness.BuildChatHandlerRec(fake.URL(), nil, reg, nil, chat.WithAuthorizer(authorize.New(store)))
	if err != nil {
		t.Fatalf("build chat: %v", err)
	}
	ts := httptest.NewServer(h)
	defer ts.Close()

	// An allowed Pathway records an allow.
	post(t, ts.URL+chat.Route, harness.CallerKey, `{"pathway":"chat","messages":[{"role":"user","content":"hi"}]}`).Body.Close()
	// An unauthorized Pathway records a deny.
	post(t, ts.URL+chat.Route, harness.CallerKey, `{"pathway":"summarize","messages":[{"role":"user","content":"hi"}]}`).Body.Close()

	var sb strings.Builder
	reg.WriteProm(&sb)
	out := sb.String()
	for _, w := range []string{
		`inferencepath_authz_decisions_total{pathway="chat",decision="allow"} 1`,
		`inferencepath_authz_decisions_total{pathway="summarize",decision="deny"} 1`,
	} {
		if !strings.Contains(out, w) {
			t.Fatalf("authz counter missing %q\n---\n%s", w, out)
		}
	}
}

// TestAuthorizeNoExistenceOracle checks that an unauthorized Caller gets the
// same 403 whether or not the Pathway exists.
func TestAuthorizeNoExistenceOracle(t *testing.T) {
	t.Setenv(harness.EgressEnv, harness.EgressToken)
	fake := fakeoa.New(fakeoa.Config{Content: "x"})
	defer fake.Close()
	ts := httptest.NewServer(buildAuthzChat(t, fake.URL(), "summarize"))
	defer ts.Close()

	for _, cap := range []string{"chat", "nonexistent"} {
		resp := post(t, ts.URL+chat.Route, harness.CallerKey, `{"pathway":"`+cap+`","messages":[{"role":"user","content":"hi"}]}`)
		if resp.StatusCode != http.StatusForbidden {
			t.Fatalf("pathway %q status = %d, want 403 (no oracle)", cap, resp.StatusCode)
		}
		resp.Body.Close()
	}
}
