package metrics_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/inferencepath/membrane/internal/observability/metrics"
)

func TestNopIsSafe(t *testing.T) {
	// Must not panic and must satisfy the Recorder interface.
	var r metrics.Recorder = metrics.Nop{}
	r.RecordChat("chat", true, metrics.OutcomeSuccess, time.Second, 1, 2, 3)
}

func TestRegistryExposition(t *testing.T) {
	reg := metrics.NewRegistry()
	reg.RecordChat("chat", false, metrics.OutcomeSuccess, 30*time.Millisecond, 5, 7, 12)
	reg.RecordChat("chat", false, metrics.OutcomeSuccess, 70*time.Millisecond, 1, 1, 2)
	reg.RecordChat("chat", true, metrics.OutcomeSuccess, 2*time.Second, 0, 0, 0)
	reg.RecordChat("chat", false, metrics.OutcomeTimeout, 5*time.Second, 0, 0, 0)

	var sb strings.Builder
	reg.WriteProm(&sb)
	out := sb.String()

	wants := []string{
		"# TYPE inferencepath_chat_requests_total counter",
		`inferencepath_chat_requests_total{pathway="chat",stream="false",outcome="success"} 2`,
		`inferencepath_chat_requests_total{pathway="chat",stream="false",outcome="timeout"} 1`,
		`inferencepath_chat_requests_total{pathway="chat",stream="true",outcome="success"} 1`,
		"# TYPE inferencepath_chat_request_duration_seconds histogram",
		`inferencepath_chat_request_duration_seconds_count{pathway="chat",stream="false",outcome="success"} 2`,
		`le="+Inf"`,
		"# TYPE inferencepath_chat_tokens_total counter",
		`inferencepath_chat_tokens_total{pathway="chat",kind="prompt"} 6`,
		`inferencepath_chat_tokens_total{pathway="chat",kind="completion"} 8`,
		`inferencepath_chat_tokens_total{pathway="chat",kind="total"} 14`,
	}
	for _, w := range wants {
		if !strings.Contains(out, w) {
			t.Fatalf("exposition missing %q\n---\n%s", w, out)
		}
	}

	// Histogram cumulative buckets: both success unary observations (30ms, 70ms)
	// are <= 0.1s; exactly one is <= 0.05s.
	if !strings.Contains(out, `le="0.1"} 2`) {
		t.Errorf("expected le=0.1 bucket to count 2:\n%s", out)
	}
	if !strings.Contains(out, `le="0.05"} 1`) {
		t.Errorf("expected le=0.05 bucket to count 1:\n%s", out)
	}
}

func TestRegistrySnapshot(t *testing.T) {
	reg := metrics.NewRegistry()
	reg.RecordChat("chat", false, metrics.OutcomeSuccess, 30*time.Millisecond, 5, 7, 12)
	reg.RecordChat("chat", true, metrics.OutcomeTimeout, 2*time.Second, 0, 0, 0)
	reg.ObserveHealth("chat", "openai", true)
	reg.ObserveHealth("chat", "anthropic", false)
	reg.ObserveFailover("chat", "openai")
	reg.ObserveAuthorization("chat", true)
	reg.ObserveAuthorization("chat", false)
	reg.ObserveReload("applied")

	snap := reg.Snapshot()

	if len(snap.Requests) != 2 {
		t.Fatalf("requests = %+v", snap.Requests)
	}
	if len(snap.Tokens) != 1 || snap.Tokens[0].Total != 12 {
		t.Fatalf("tokens = %+v", snap.Tokens)
	}
	// Health is sorted by (pathway, provider): anthropic before openai.
	if len(snap.Health) != 2 || snap.Health[0].Provider != "anthropic" || snap.Health[0].Healthy {
		t.Fatalf("health = %+v", snap.Health)
	}
	if snap.Health[1].Provider != "openai" || !snap.Health[1].Healthy {
		t.Fatalf("health[1] = %+v", snap.Health[1])
	}
	if len(snap.Failovers) != 1 || snap.Failovers[0].Count != 1 {
		t.Fatalf("failovers = %+v", snap.Failovers)
	}
	// Authz sorted by decision: allow before deny.
	if len(snap.Authz) != 2 || snap.Authz[0].Decision != "allow" || snap.Authz[1].Decision != "deny" {
		t.Fatalf("authz = %+v", snap.Authz)
	}
	if len(snap.Reloads) != 1 || snap.Reloads[0].Result != "applied" {
		t.Fatalf("reloads = %+v", snap.Reloads)
	}
}

func TestSnapshotEmpty(t *testing.T) {
	snap := metrics.NewRegistry().Snapshot()
	if len(snap.Requests) != 0 || len(snap.Health) != 0 || len(snap.Reloads) != 0 {
		t.Fatalf("expected empty snapshot, got %+v", snap)
	}
}

func TestRegistryNoIdentityLabels(t *testing.T) {
	reg := metrics.NewRegistry()
	reg.RecordChat("chat", false, metrics.OutcomeSuccess, time.Millisecond, 1, 1, 2)
	var sb strings.Builder
	reg.WriteProm(&sb)
	out := strings.ToLower(sb.String())
	// Caller/org identity and secrets must NEVER appear anywhere in the exposition.
	for _, forbidden := range []string{"caller", "authorization", "secret", "key_id", "bearer", "tenant"} {
		if strings.Contains(out, forbidden) {
			t.Fatalf("metrics exposition leaked %q:\n%s", forbidden, sb.String())
		}
	}
	// Per-request series must NOT carry a provider label (provider family is only
	// an operational label on the resilience series, never on request metrics).
	for _, line := range strings.Split(sb.String(), "\n") {
		if strings.HasPrefix(line, "inferencepath_chat_requests_total{") ||
			strings.HasPrefix(line, "inferencepath_chat_request_duration_seconds") ||
			strings.HasPrefix(line, "inferencepath_chat_tokens_total{") {
			if strings.Contains(line, "provider=") {
				t.Fatalf("per-request metric carries a provider label: %q", line)
			}
		}
	}
}

func TestRegistryResilienceExposition(t *testing.T) {
	reg := metrics.NewRegistry()
	// Observer implementation satisfies both the metrics.Observer contract.
	var obs metrics.Observer = reg
	obs.ObserveFailover("chat", "openai")
	obs.ObserveFailover("chat", "openai")
	obs.ObserveHealth("chat", "openai", false)
	obs.ObserveHealth("chat", "anthropic", true)

	var sb strings.Builder
	reg.WriteProm(&sb)
	out := sb.String()

	wants := []string{
		"# TYPE inferencepath_chat_failovers_total counter",
		`inferencepath_chat_failovers_total{pathway="chat",provider="openai"} 2`,
		"# TYPE inferencepath_target_healthy gauge",
		`inferencepath_target_healthy{pathway="chat",provider="anthropic"} 1`,
		`inferencepath_target_healthy{pathway="chat",provider="openai"} 0`,
	}
	for _, w := range wants {
		if !strings.Contains(out, w) {
			t.Fatalf("resilience exposition missing %q\n---\n%s", w, out)
		}
	}
}

func TestNopObserverIsSafe(t *testing.T) {
	var o metrics.Observer = metrics.Nop{}
	o.ObserveFailover("chat", "openai")
	o.ObserveHealth("chat", "openai", true)
	var az metrics.AuthzObserver = metrics.Nop{}
	az.ObserveAuthorization("chat", true)
	var rl metrics.ReloadObserver = metrics.Nop{}
	rl.ObserveReload("applied")
}

func TestRegistryAuthzExposition(t *testing.T) {
	reg := metrics.NewRegistry()
	var az metrics.AuthzObserver = reg
	az.ObserveAuthorization("chat", true)
	az.ObserveAuthorization("chat", true)
	az.ObserveAuthorization("chat", false)

	var sb strings.Builder
	reg.WriteProm(&sb)
	out := sb.String()

	wants := []string{
		"# TYPE inferencepath_authz_decisions_total counter",
		`inferencepath_authz_decisions_total{pathway="chat",decision="allow"} 2`,
		`inferencepath_authz_decisions_total{pathway="chat",decision="deny"} 1`,
	}
	for _, w := range wants {
		if !strings.Contains(out, w) {
			t.Fatalf("authz exposition missing %q\n---\n%s", w, out)
		}
	}
}

func TestRegistryReloadExposition(t *testing.T) {
	reg := metrics.NewRegistry()
	var rl metrics.ReloadObserver = reg
	rl.ObserveReload("applied")
	rl.ObserveReload("rejected")
	rl.ObserveReload("rejected")

	var sb strings.Builder
	reg.WriteProm(&sb)
	out := sb.String()

	wants := []string{
		"# TYPE inferencepath_config_reloads_total counter",
		`inferencepath_config_reloads_total{result="applied"} 1`,
		`inferencepath_config_reloads_total{result="rejected"} 2`,
	}
	for _, w := range wants {
		if !strings.Contains(out, w) {
			t.Fatalf("reload exposition missing %q\n---\n%s", w, out)
		}
	}
}

func TestRegistryHandler(t *testing.T) {
	reg := metrics.NewRegistry()
	reg.RecordChat("chat", false, metrics.OutcomeSuccess, time.Millisecond, 1, 1, 2)
	ts := httptest.NewServer(reg.Handler())
	defer ts.Close()

	resp, err := http.Get(ts.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/plain") {
		t.Fatalf("content-type = %q", ct)
	}
}

func TestRegistryEmptyExposition(t *testing.T) {
	// An empty registry still emits the HELP/TYPE headers without panicking.
	var sb strings.Builder
	metrics.NewRegistry().WriteProm(&sb)
	if !strings.Contains(sb.String(), "inferencepath_chat_requests_total") {
		t.Fatalf("empty exposition missing headers:\n%s", sb.String())
	}
}
