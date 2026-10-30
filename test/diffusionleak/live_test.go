// Live-path Diffusion-leak tests. They drive the wired chat handler against a
// capturing fake Provider and check that the upstream request carries the Egress
// Identity and none of the caller or org identity, Caller Key, or Interior
// control fields, on the non-streaming, streaming, and error paths.
package diffusionleak

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	fakeoa "github.com/inferencepath/membrane/test/fakes/openai"
	"github.com/inferencepath/membrane/test/harness"
)

// Identity markers injected on the Caller-to-Membrane hop. None of them may
// appear on the Membrane-to-Provider hop.
const (
	fwdIP       = "203.0.113.9"
	callerUA    = "evilbrowser/9000"
	cookieVal   = "session=supersecret-cookie"
	callerOrgID = "acme-app"
)

func sendWithIdentity(t *testing.T, url, body string) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, url, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+harness.CallerKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Forwarded-For", fwdIP)
	req.Header.Set("X-Real-IP", fwdIP)
	req.Header.Set("User-Agent", callerUA)
	req.Header.Set("Cookie", cookieVal)
	req.Header.Set("X-Tenant", callerOrgID)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.ReadAll(resp.Body)
	_ = resp.Body.Close()
}

// assertUpstreamClean fails if the captured upstream request leaks any identity
// or control field, and confirms it carries the Egress Identity.
func assertUpstreamClean(t *testing.T, cap fakeoa.Capture) {
	t.Helper()

	if got := cap.Header.Get("Authorization"); got != "Bearer "+harness.EgressToken {
		t.Fatalf("upstream Authorization = %q, want the Egress Identity", got)
	}

	// No forwarding/identity headers may be present on the egress hop.
	for _, h := range []string{"X-Forwarded-For", "X-Real-Ip", "Cookie", "X-Tenant"} {
		if v := cap.Header.Get(h); v != "" {
			t.Errorf("DIFFUSION LEAK: upstream carried header %s=%q", h, v)
		}
	}

	// Scan the full serialized request (headers + body) for identity markers.
	var sb strings.Builder
	_ = cap.Header.Write(&sb)
	sb.Write(cap.Body)
	hay := sb.String()

	forbidden := map[string]string{
		"caller key":    harness.CallerKey,
		"caller secret": "s3cr3t-demo-key",
		"caller org":    callerOrgID,
		"forwarded ip":  fwdIP,
		"caller UA":     callerUA,
		"cookie":        cookieVal,
		"reveal flag":   "reveal",
	}
	for label, needle := range forbidden {
		if strings.Contains(hay, needle) {
			t.Errorf("DIFFUSION LEAK: %s (%q) crossed the Membrane", label, needle)
		}
	}
}

func TestLiveLeakNonStreaming(t *testing.T) {
	t.Setenv(harness.EgressEnv, harness.EgressToken)
	fake := fakeoa.New(fakeoa.Config{Content: "ok", Model: "backing", FinishReason: "stop"})
	defer fake.Close()
	h, err := harness.BuildChat(fake.URL(), nil)
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(h)
	defer ts.Close()

	sendWithIdentity(t, ts.URL+"/v1/chat", `{"pathway":"chat","reveal":true,"messages":[{"role":"user","content":"hi"}]}`)
	assertUpstreamClean(t, fake.Last())
}

func TestLiveLeakStreaming(t *testing.T) {
	t.Setenv(harness.EgressEnv, harness.EgressToken)
	fake := fakeoa.New(fakeoa.Config{StreamDeltas: []string{"a", "b"}, FinishReason: "stop"})
	defer fake.Close()
	h, _ := harness.BuildChat(fake.URL(), nil)
	ts := httptest.NewServer(h)
	defer ts.Close()

	sendWithIdentity(t, ts.URL+"/v1/chat", `{"pathway":"chat","stream":true,"messages":[{"role":"user","content":"hi"}]}`)
	assertUpstreamClean(t, fake.Last())
}

func TestLiveLeakErrorPath(t *testing.T) {
	t.Setenv(harness.EgressEnv, harness.EgressToken)
	fake := fakeoa.New(fakeoa.Config{Status: 500})
	defer fake.Close()
	h, _ := harness.BuildChat(fake.URL(), nil)
	ts := httptest.NewServer(h)
	defer ts.Close()

	sendWithIdentity(t, ts.URL+"/v1/chat", `{"pathway":"chat","messages":[{"role":"user","content":"hi"}]}`)
	assertUpstreamClean(t, fake.Last())
}

func TestLiveRevealIsByteIdenticalUpstream(t *testing.T) {
	t.Setenv(harness.EgressEnv, harness.EgressToken)
	fake := fakeoa.New(fakeoa.Config{Content: "ok", Model: "backing", FinishReason: "stop"})
	defer fake.Close()
	h, _ := harness.BuildChat(fake.URL(), nil)
	ts := httptest.NewServer(h)
	defer ts.Close()

	sendWithIdentity(t, ts.URL+"/v1/chat", `{"pathway":"chat","reveal":false,"messages":[{"role":"user","content":"hi"}]}`)
	withoutReveal := string(fake.Last().Body)

	sendWithIdentity(t, ts.URL+"/v1/chat", `{"pathway":"chat","reveal":true,"messages":[{"role":"user","content":"hi"}]}`)
	withReveal := string(fake.Last().Body)

	if withoutReveal != withReveal {
		t.Fatalf("upstream bodies differ by Reveal flag:\n reveal=false: %s\n reveal=true:  %s", withoutReveal, withReveal)
	}
	if strings.Contains(withReveal, "reveal") {
		t.Fatalf("DIFFUSION LEAK: reveal flag present upstream: %s", withReveal)
	}
}
