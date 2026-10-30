// Package metrics provides identity-free, Pathway-keyed metrics for the egress
// path. Per-request metrics are labelled by the logical Pathway, the stream
// flag, and a coarse outcome, never by caller or org identity or the Caller
// Key.
//
// Resilience metrics (failover count, target health) also carry a coarse
// Provider family label such as "openai" or "anthropic". That label is not a
// caller identity and stays in Membrane-local operator telemetry, so Callers
// remain anonymous while operators can see which family is degrading.
//
// The exposition is Prometheus text format, version 0.0.4. The package has no
// third-party dependencies.
package metrics

import (
	"fmt"
	"io"
	"net/http"
	"sort"
	"strconv"
	"sync"
	"time"
)

// Outcome is a coarse, identity-free result classification for a request.
type Outcome string

const (
	// OutcomeSuccess is a completed request (2xx to the Caller).
	OutcomeSuccess Outcome = "success"
	// OutcomeTimeout is an upstream timeout (504 to the Caller).
	OutcomeTimeout Outcome = "timeout"
	// OutcomeUpstreamError is a non-timeout upstream failure (502 to the Caller).
	OutcomeUpstreamError Outcome = "upstream_error"
	// OutcomeInternalError is a Membrane-side failure (500 to the Caller).
	OutcomeInternalError Outcome = "internal_error"
	// OutcomeFailedClosed is a request blocked by an Interior content stage that
	// failed closed before anything crossed the Membrane.
	OutcomeFailedClosed Outcome = "failed_closed"
)

// Recorder records Pathway-keyed request metrics. Implementations must not
// accept or store caller or org identity or a Caller Key.
type Recorder interface {
	// RecordChat records one chat request. Token counts are 0 when unknown,
	// for example on streaming responses that do not report usage.
	RecordChat(pathway string, stream bool, outcome Outcome, latency time.Duration, promptTokens, completionTokens, totalTokens int)
}

// Observer receives identity-free resilience signals from the Membrane. The
// provider argument is a Provider family label, never a caller identity, and
// it does not leave the Membrane.
type Observer interface {
	// ObserveFailover records that a request failed over away from one target.
	ObserveFailover(pathway, provider string)
	// ObserveHealth records a target's last-known health.
	ObserveHealth(pathway, provider string, healthy bool)
}

// AuthzObserver records authorization decisions keyed by Pathway and an allow
// or deny decision, never by Caller.
type AuthzObserver interface {
	ObserveAuthorization(pathway string, allowed bool)
}

// ReloadObserver records config reload outcomes such as applied, rejected, or
// unchanged.
type ReloadObserver interface {
	ObserveReload(result string)
}

// Nop implements every recorder and observer interface and discards all input.
type Nop struct{}

// RecordChat implements Recorder.
func (Nop) RecordChat(string, bool, Outcome, time.Duration, int, int, int) {}

// ObserveFailover implements Observer.
func (Nop) ObserveFailover(string, string) {}

// ObserveHealth implements Observer.
func (Nop) ObserveHealth(string, string, bool) {}

// ObserveAuthorization implements AuthzObserver.
func (Nop) ObserveAuthorization(string, bool) {}

// ObserveReload implements ReloadObserver.
func (Nop) ObserveReload(string) {}

// defaultBuckets are the latency histogram upper bounds, in seconds.
var defaultBuckets = []float64{0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10}

type labelSet struct {
	pathway string
	stream  bool
	outcome Outcome
}

type reqStat struct {
	count   uint64
	sum     float64
	buckets []uint64 // cumulative counts aligned to defaultBuckets
}

type tokenStat struct {
	prompt     uint64
	completion uint64
	total      uint64
}

// capProvider keys a resilience series by Pathway and Provider family.
type capProvider struct {
	pathway  string
	provider string
}

// authzKey keys an authorization series by Pathway and decision.
type authzKey struct {
	pathway  string
	decision string // "allow" or "deny"
}

// Registry is an in-process, concurrency-safe implementation of the recorder
// and observer interfaces with Prometheus exposition.
type Registry struct {
	mu        sync.Mutex
	reqs      map[labelSet]*reqStat
	tokens    map[string]*tokenStat // keyed by pathway
	failovers map[capProvider]uint64
	health    map[capProvider]int // 1 = healthy, 0 = unhealthy
	authz     map[authzKey]uint64
	reloads   map[string]uint64 // keyed by result
}

// NewRegistry returns an empty Registry.
func NewRegistry() *Registry {
	return &Registry{
		reqs:      make(map[labelSet]*reqStat),
		tokens:    make(map[string]*tokenStat),
		failovers: make(map[capProvider]uint64),
		health:    make(map[capProvider]int),
		authz:     make(map[authzKey]uint64),
		reloads:   make(map[string]uint64),
	}
}

// ObserveAuthorization implements AuthzObserver.
func (r *Registry) ObserveAuthorization(pathway string, allowed bool) {
	decision := "deny"
	if allowed {
		decision = "allow"
	}
	r.mu.Lock()
	r.authz[authzKey{pathway, decision}]++
	r.mu.Unlock()
}

// ObserveReload implements ReloadObserver.
func (r *Registry) ObserveReload(result string) {
	r.mu.Lock()
	r.reloads[result]++
	r.mu.Unlock()
}

// ObserveFailover implements Observer.
func (r *Registry) ObserveFailover(pathway, provider string) {
	r.mu.Lock()
	r.failovers[capProvider{pathway, provider}]++
	r.mu.Unlock()
}

// ObserveHealth implements Observer.
func (r *Registry) ObserveHealth(pathway, provider string, healthy bool) {
	v := 0
	if healthy {
		v = 1
	}
	r.mu.Lock()
	r.health[capProvider{pathway, provider}] = v
	r.mu.Unlock()
}

// RecordChat implements Recorder.
func (r *Registry) RecordChat(pathway string, stream bool, outcome Outcome, latency time.Duration, prompt, completion, total int) {
	secs := latency.Seconds()
	l := labelSet{pathway: pathway, stream: stream, outcome: outcome}

	r.mu.Lock()
	defer r.mu.Unlock()

	s := r.reqs[l]
	if s == nil {
		s = &reqStat{buckets: make([]uint64, len(defaultBuckets))}
		r.reqs[l] = s
	}
	s.count++
	s.sum += secs
	for i, b := range defaultBuckets {
		if secs <= b {
			s.buckets[i]++
		}
	}

	if prompt > 0 || completion > 0 || total > 0 {
		t := r.tokens[pathway]
		if t == nil {
			t = &tokenStat{}
			r.tokens[pathway] = t
		}
		t.prompt += uint64(prompt)
		t.completion += uint64(completion)
		t.total += uint64(total)
	}
}

// RequestSeries is one identity-free request series in a Snapshot.
type RequestSeries struct {
	Pathway    string  `json:"pathway"`
	Stream     bool    `json:"stream"`
	Outcome    string  `json:"outcome"`
	Count      uint64  `json:"count"`
	SumSeconds float64 `json:"sumSeconds"`
}

// TokenSeries is cumulative token usage for one Pathway.
type TokenSeries struct {
	Pathway    string `json:"pathway"`
	Prompt     uint64 `json:"prompt"`
	Completion uint64 `json:"completion"`
	Total      uint64 `json:"total"`
}

// TargetHealth is the last-known health of one target, identified by Pathway
// and Provider family.
type TargetHealth struct {
	Pathway  string `json:"pathway"`
	Provider string `json:"provider"`
	Healthy  bool   `json:"healthy"`
}

// FailoverSeries counts failovers away from one target, identified by Pathway
// and Provider family.
type FailoverSeries struct {
	Pathway  string `json:"pathway"`
	Provider string `json:"provider"`
	Count    uint64 `json:"count"`
}

// AuthzSeries counts authorization decisions for one Pathway and decision.
type AuthzSeries struct {
	Pathway  string `json:"pathway"`
	Decision string `json:"decision"`
	Count    uint64 `json:"count"`
}

// ReloadSeries counts config reload outcomes by result.
type ReloadSeries struct {
	Result string `json:"result"`
	Count  uint64 `json:"count"`
}

// Snapshot is a point-in-time view of the Registry for the Membrane Console's
// read endpoints. It carries only Pathway and Provider family labels, never
// caller or org identity or secrets. Series appear in the same order as the
// Prometheus exposition.
type Snapshot struct {
	Requests  []RequestSeries  `json:"requests"`
	Tokens    []TokenSeries    `json:"tokens"`
	Health    []TargetHealth   `json:"health"`
	Failovers []FailoverSeries `json:"failovers"`
	Authz     []AuthzSeries    `json:"authz"`
	Reloads   []ReloadSeries   `json:"reloads"`
}

// Snapshot returns a sorted copy of the current counters and gauges.
func (r *Registry) Snapshot() Snapshot {
	r.mu.Lock()
	defer r.mu.Unlock()

	snap := Snapshot{}

	labels := make([]labelSet, 0, len(r.reqs))
	for l := range r.reqs {
		labels = append(labels, l)
	}
	sort.Slice(labels, func(i, j int) bool {
		a, b := labels[i], labels[j]
		if a.pathway != b.pathway {
			return a.pathway < b.pathway
		}
		if a.stream != b.stream {
			return !a.stream
		}
		return a.outcome < b.outcome
	})
	for _, l := range labels {
		s := r.reqs[l]
		snap.Requests = append(snap.Requests, RequestSeries{
			Pathway: l.pathway, Stream: l.stream, Outcome: string(l.outcome),
			Count: s.count, SumSeconds: s.sum,
		})
	}

	caps := make([]string, 0, len(r.tokens))
	for c := range r.tokens {
		caps = append(caps, c)
	}
	sort.Strings(caps)
	for _, c := range caps {
		t := r.tokens[c]
		snap.Tokens = append(snap.Tokens, TokenSeries{
			Pathway: c, Prompt: t.prompt, Completion: t.completion, Total: t.total,
		})
	}

	for _, k := range sortedCapProviders(r.health) {
		snap.Health = append(snap.Health, TargetHealth{
			Pathway: k.pathway, Provider: k.provider, Healthy: r.health[k] == 1,
		})
	}
	for _, k := range sortedCapProviders(r.failovers) {
		snap.Failovers = append(snap.Failovers, FailoverSeries{
			Pathway: k.pathway, Provider: k.provider, Count: r.failovers[k],
		})
	}

	azKeys := make([]authzKey, 0, len(r.authz))
	for k := range r.authz {
		azKeys = append(azKeys, k)
	}
	sort.Slice(azKeys, func(i, j int) bool {
		if azKeys[i].pathway != azKeys[j].pathway {
			return azKeys[i].pathway < azKeys[j].pathway
		}
		return azKeys[i].decision < azKeys[j].decision
	})
	for _, k := range azKeys {
		snap.Authz = append(snap.Authz, AuthzSeries{
			Pathway: k.pathway, Decision: k.decision, Count: r.authz[k],
		})
	}

	rlKeys := make([]string, 0, len(r.reloads))
	for k := range r.reloads {
		rlKeys = append(rlKeys, k)
	}
	sort.Strings(rlKeys)
	for _, k := range rlKeys {
		snap.Reloads = append(snap.Reloads, ReloadSeries{Result: k, Count: r.reloads[k]})
	}

	return snap
}

// Handler returns an http.Handler serving the Prometheus text exposition.
func (r *Registry) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		r.WriteProm(w)
	})
}

// WriteProm writes the Prometheus text exposition to w in a stable order.
func (r *Registry) WriteProm(w io.Writer) {
	r.mu.Lock()
	defer r.mu.Unlock()

	labels := make([]labelSet, 0, len(r.reqs))
	for l := range r.reqs {
		labels = append(labels, l)
	}
	sort.Slice(labels, func(i, j int) bool {
		a, b := labels[i], labels[j]
		if a.pathway != b.pathway {
			return a.pathway < b.pathway
		}
		if a.stream != b.stream {
			return !a.stream // false before true
		}
		return a.outcome < b.outcome
	})

	fmt.Fprintln(w, "# HELP inferencepath_chat_requests_total Total chat requests, by pathway/stream/outcome.")
	fmt.Fprintln(w, "# TYPE inferencepath_chat_requests_total counter")
	for _, l := range labels {
		fmt.Fprintf(w, "inferencepath_chat_requests_total{pathway=%q,stream=\"%t\",outcome=%q} %d\n",
			l.pathway, l.stream, string(l.outcome), r.reqs[l].count)
	}

	fmt.Fprintln(w, "# HELP inferencepath_chat_request_duration_seconds Chat request latency, by pathway/stream/outcome.")
	fmt.Fprintln(w, "# TYPE inferencepath_chat_request_duration_seconds histogram")
	for _, l := range labels {
		s := r.reqs[l]
		for i, b := range defaultBuckets {
			fmt.Fprintf(w, "inferencepath_chat_request_duration_seconds_bucket{pathway=%q,stream=\"%t\",outcome=%q,le=%q} %d\n",
				l.pathway, l.stream, string(l.outcome), strconv.FormatFloat(b, 'g', -1, 64), s.buckets[i])
		}
		fmt.Fprintf(w, "inferencepath_chat_request_duration_seconds_bucket{pathway=%q,stream=\"%t\",outcome=%q,le=\"+Inf\"} %d\n",
			l.pathway, l.stream, string(l.outcome), s.count)
		fmt.Fprintf(w, "inferencepath_chat_request_duration_seconds_sum{pathway=%q,stream=\"%t\",outcome=%q} %g\n",
			l.pathway, l.stream, string(l.outcome), s.sum)
		fmt.Fprintf(w, "inferencepath_chat_request_duration_seconds_count{pathway=%q,stream=\"%t\",outcome=%q} %d\n",
			l.pathway, l.stream, string(l.outcome), s.count)
	}

	caps := make([]string, 0, len(r.tokens))
	for c := range r.tokens {
		caps = append(caps, c)
	}
	sort.Strings(caps)

	fmt.Fprintln(w, "# HELP inferencepath_chat_tokens_total Token usage, by pathway/kind.")
	fmt.Fprintln(w, "# TYPE inferencepath_chat_tokens_total counter")
	for _, c := range caps {
		t := r.tokens[c]
		fmt.Fprintf(w, "inferencepath_chat_tokens_total{pathway=%q,kind=\"prompt\"} %d\n", c, t.prompt)
		fmt.Fprintf(w, "inferencepath_chat_tokens_total{pathway=%q,kind=\"completion\"} %d\n", c, t.completion)
		fmt.Fprintf(w, "inferencepath_chat_tokens_total{pathway=%q,kind=\"total\"} %d\n", c, t.total)
	}

	foKeys := sortedCapProviders(r.failovers)
	fmt.Fprintln(w, "# HELP inferencepath_chat_failovers_total Failovers away from a target, by pathway/provider family.")
	fmt.Fprintln(w, "# TYPE inferencepath_chat_failovers_total counter")
	for _, k := range foKeys {
		fmt.Fprintf(w, "inferencepath_chat_failovers_total{pathway=%q,provider=%q} %d\n",
			k.pathway, k.provider, r.failovers[k])
	}

	hKeys := sortedCapProviders(r.health)
	fmt.Fprintln(w, "# HELP inferencepath_target_healthy Last-known target health (1 healthy, 0 unhealthy), by pathway/provider family.")
	fmt.Fprintln(w, "# TYPE inferencepath_target_healthy gauge")
	for _, k := range hKeys {
		fmt.Fprintf(w, "inferencepath_target_healthy{pathway=%q,provider=%q} %d\n",
			k.pathway, k.provider, r.health[k])
	}

	azKeys := make([]authzKey, 0, len(r.authz))
	for k := range r.authz {
		azKeys = append(azKeys, k)
	}
	sort.Slice(azKeys, func(i, j int) bool {
		if azKeys[i].pathway != azKeys[j].pathway {
			return azKeys[i].pathway < azKeys[j].pathway
		}
		return azKeys[i].decision < azKeys[j].decision
	})
	fmt.Fprintln(w, "# HELP inferencepath_authz_decisions_total Authz decisions, by pathway/decision (identity-free).")
	fmt.Fprintln(w, "# TYPE inferencepath_authz_decisions_total counter")
	for _, k := range azKeys {
		fmt.Fprintf(w, "inferencepath_authz_decisions_total{pathway=%q,decision=%q} %d\n",
			k.pathway, k.decision, r.authz[k])
	}

	rlKeys := make([]string, 0, len(r.reloads))
	for k := range r.reloads {
		rlKeys = append(rlKeys, k)
	}
	sort.Strings(rlKeys)
	fmt.Fprintln(w, "# HELP inferencepath_config_reloads_total Configuration reload outcomes, by result.")
	fmt.Fprintln(w, "# TYPE inferencepath_config_reloads_total counter")
	for _, k := range rlKeys {
		fmt.Fprintf(w, "inferencepath_config_reloads_total{result=%q} %d\n", k, r.reloads[k])
	}
}

// sortedCapProviders returns the keys of m sorted by pathway, then provider.
func sortedCapProviders[V any](m map[capProvider]V) []capProvider {
	keys := make([]capProvider, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].pathway != keys[j].pathway {
			return keys[i].pathway < keys[j].pathway
		}
		return keys[i].provider < keys[j].provider
	})
	return keys
}
