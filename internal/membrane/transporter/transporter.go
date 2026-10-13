// Package transporter defines the Membrane crossing. The Transporter is the only
// mover across the Membrane and, by its signature, accepts ONLY a
// scrub.SanitizedRequest — a non-sanitized request cannot be transported.
//
// The concrete Transporter (Membrane) owns, per Pathway, a Pool of ordered
// Provider targets. For each request it selects a target (priority or
// round-robin), resolves that target's Egress Identity from the Keyring, and
// dispatches the allowlisted payload — attaching identity only here, at/after
// the Membrane. On a retryable failure it fails over to the next healthy target;
// a per-target circuit breaker skips targets that are failing. Streaming fails
// over only before the first byte is relayed.
package transporter

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/inferencepath/membrane/internal/exterior/providers"
	"github.com/inferencepath/membrane/internal/membrane/scrub"
	"github.com/inferencepath/membrane/internal/types"
)

// ErrNotImplemented is returned by the stub Transporter.
var ErrNotImplemented = errors.New("transporter: not implemented")

// ErrNoProvider indicates no pool/target is available to serve the request.
var ErrNoProvider = errors.New("transporter: no provider adapter for target")

// Transporter carries a SanitizedRequest across the Membrane, attaching the
// Egress Identity and dispatching to a selected Provider. Both methods accept
// only scrub.SanitizedRequest, so a non-sanitized request cannot be transported.
type Transporter interface {
	Transport(ctx context.Context, req scrub.SanitizedRequest) (types.CanonicalResponse, error)
	TransportStream(ctx context.Context, req scrub.SanitizedRequest, emit func(types.StreamChunk) error) error
}

// KeyringResolver resolves an Egress Identity by credential reference. It is
// satisfied by the exterior Keyring; defined here to avoid importing it.
type KeyringResolver interface {
	Resolve(ref string) (providers.EgressIdentity, error)
}

// Strategy is a deterministic target-selection policy.
type Strategy string

const (
	// StrategyPriority always prefers the lowest-priority-number healthy target.
	StrategyPriority Strategy = "priority"
	// StrategyRoundRobin cycles across healthy targets in priority order.
	StrategyRoundRobin Strategy = "round_robin"
)

// TargetSpec describes one pooled egress target: its adapter, the Keyring
// reference for its Egress Identity, the resolved model, and per-target
// timeouts. Endpoints/regions/deployments are baked into the adapter and never
// cross back into the Interior.
type TargetSpec struct {
	Adapter providers.Provider
	// ProviderName is the configured provider's name (the admin-chosen key, e.g.
	// "openai-main"). A caller cascade tier selects a target by this name; it is
	// never caller/org identity and never crosses the Membrane to a Provider.
	ProviderName string
	KeyringRef   string
	Model        string
	Priority     int
	TimeoutTotal time.Duration
	TimeoutIdle  time.Duration
	// AmbientAuth marks a target whose adapter authenticates from an ambient
	// source (IAM role / managed identity) baked in at build time, so there is
	// no keyring credential to resolve. The Egress Identity passed to the adapter
	// is empty in that case.
	AmbientAuth bool
}

// PoolSpec is a Pathway's set of targets plus its selection Strategy.
type PoolSpec struct {
	Strategy Strategy
	Targets  []TargetSpec
}

// BackoffPolicy configures the per-target circuit breaker. A zero
// BreakerThreshold disables the breaker (targets are always attempted).
//
// The open-state cooldown escalates with the number of consecutive breaker
// trips: when Base > 0 the Nth consecutive open waits Base*Factor^(N-1), capped
// at Max; this backs off a persistently failing target so repeated half-open
// probes do not hammer it. When Base <= 0 the fixed BreakerCooldown is used for
// every trip (no escalation).
type BackoffPolicy struct {
	Base             time.Duration
	Factor           float64
	Max              time.Duration
	BreakerThreshold int
	BreakerCooldown  time.Duration
}

// cooldownFor returns the open-state cooldown for the given consecutive trip
// count (1-based). Without an escalation Base it is the fixed BreakerCooldown.
func (b BackoffPolicy) cooldownFor(trips int) time.Duration {
	if b.Base <= 0 {
		return b.BreakerCooldown
	}
	d := b.Base
	for i := 1; i < trips; i++ {
		if b.Factor > 1 {
			d = time.Duration(float64(d) * b.Factor)
		}
		if b.Max > 0 && d >= b.Max {
			return b.Max
		}
	}
	if b.Max > 0 && d > b.Max {
		return b.Max
	}
	return d
}

// Observer receives identity-free resilience signals (failover count, target
// health). The provider argument is an Exterior Provider FAMILY label only; it
// never carries caller identity and never crosses the Membrane to a Provider.
type Observer interface {
	ObserveFailover(pathway, provider string)
	ObserveHealth(pathway, provider string, healthy bool)
}

// PlanTier is one tier of a caller-driven cascade: which configured provider to
// use (by name) and which of that provider's allowed models. The chat handler
// builds the ordered plan (ascending cascade) after validating every tier
// against the pathway allowlist, and attaches it to the request context via
// WithPlan. An empty Model means the provider's default model.
//
// The plan is an Interior-only routing control: it names providers/models by
// configured key and never carries caller identity, so it is safe to pass
// alongside a SanitizedRequest without crossing the Membrane into a payload.
type PlanTier struct {
	Provider string
	Model    string
}

type planCtxKey struct{}

// WithPlan returns a context carrying an ordered caller cascade plan. When
// present, Transport/TransportStream attempt the named providers in order
// instead of the Pathway's default pool ordering.
func WithPlan(ctx context.Context, plan []PlanTier) context.Context {
	if len(plan) == 0 {
		return ctx
	}
	return context.WithValue(ctx, planCtxKey{}, plan)
}

func planFromContext(ctx context.Context) []PlanTier {
	p, _ := ctx.Value(planCtxKey{}).([]PlanTier)
	return p
}

// Membrane is the concrete pool-aware Transporter.
type Membrane struct {
	pools    map[types.PathwayID]*routedPool
	byName   map[types.PathwayID]map[string]*boundTarget
	keyring  KeyringResolver
	backoff  BackoffPolicy
	now      func() time.Time
	observer Observer
}

type boundTarget struct {
	adapter      providers.Provider
	providerName string
	keyringRef   string
	model        string
	priority     int
	timeoutTotal time.Duration
	ambientAuth  bool
	health       *targetHealth
}

// attempt is one dispatch try: a bound target plus the model to request from it
// (which may be a caller-chosen allowed model overriding the target default).
type attempt struct {
	t     *boundTarget
	model string
}

// identity resolves the Egress Identity for a target. Ambient-auth targets have
// no keyring credential; their adapter authenticates from a baked-in ambient
// source, so an empty identity is returned without touching the keyring.
func (m *Membrane) identity(t *boundTarget) (providers.EgressIdentity, error) {
	if t.ambientAuth {
		return providers.EgressIdentity{}, nil
	}
	return m.keyring.Resolve(t.keyringRef)
}

type routedPool struct {
	strategy Strategy
	targets  []*boundTarget
	rr       atomic.Uint64
}

// Option configures a Membrane.
type Option func(*Membrane)

// WithBackoff sets the circuit-breaker policy.
func WithBackoff(b BackoffPolicy) Option { return func(m *Membrane) { m.backoff = b } }

// WithClock overrides the clock (for deterministic breaker tests).
func WithClock(now func() time.Time) Option { return func(m *Membrane) { m.now = now } }

// WithObserver attaches a resilience Observer (failover/health telemetry). A nil
// Observer is ignored (the default no-op is kept).
func WithObserver(o Observer) Option {
	return func(m *Membrane) {
		if o != nil {
			m.observer = o
		}
	}
}

// New returns a pool-aware Membrane Transporter. For each Pathway the targets
// are ordered by priority (ascending). It is the single mover across the
// Membrane.
func New(pools map[types.PathwayID]PoolSpec, keyring KeyringResolver, opts ...Option) *Membrane {
	m := &Membrane{
		pools:   make(map[types.PathwayID]*routedPool, len(pools)),
		byName:  make(map[types.PathwayID]map[string]*boundTarget, len(pools)),
		keyring: keyring,
		now:     time.Now,
	}
	for _, o := range opts {
		o(m)
	}
	for cap, p := range pools {
		rp := &routedPool{strategy: normalizeStrategy(p.Strategy)}
		named := make(map[string]*boundTarget, len(p.Targets))
		for _, ts := range p.Targets {
			bt := &boundTarget{
				adapter:      ts.Adapter,
				providerName: ts.ProviderName,
				keyringRef:   ts.KeyringRef,
				model:        ts.Model,
				priority:     ts.Priority,
				timeoutTotal: ts.TimeoutTotal,
				ambientAuth:  ts.AmbientAuth,
				health:       &targetHealth{},
			}
			rp.targets = append(rp.targets, bt)
			if ts.ProviderName != "" {
				named[ts.ProviderName] = bt
			}
		}
		sort.SliceStable(rp.targets, func(i, j int) bool {
			return rp.targets[i].priority < rp.targets[j].priority
		})
		m.pools[cap] = rp
		m.byName[cap] = named
	}
	return m
}

// attempts returns the ordered dispatch list for a request. When the context
// carries a caller cascade plan, it resolves each tier's provider by name
// (sticky model: the caller-chosen allowed model, or the target default) in
// plan order. Otherwise it falls back to the Pathway pool's selection order,
// requesting each target's default model.
func (m *Membrane) attempts(ctx context.Context, capID types.PathwayID) []attempt {
	if plan := planFromContext(ctx); len(plan) > 0 {
		named := m.byName[capID]
		out := make([]attempt, 0, len(plan))
		for _, tier := range plan {
			t, ok := named[tier.Provider]
			if !ok {
				continue
			}
			model := tier.Model
			if model == "" {
				model = t.model
			}
			out = append(out, attempt{t: t, model: model})
		}
		return out
	}
	pool, ok := m.pools[capID]
	if !ok {
		return nil
	}
	ordered := pool.order()
	out := make([]attempt, 0, len(ordered))
	for _, t := range ordered {
		out = append(out, attempt{t: t, model: t.model})
	}
	return out
}

func normalizeStrategy(s Strategy) Strategy {
	if s == StrategyRoundRobin {
		return StrategyRoundRobin
	}
	return StrategyPriority
}

// order returns the pool's targets in selection order for this request.
func (p *routedPool) order() []*boundTarget {
	if p.strategy == StrategyRoundRobin && len(p.targets) > 0 {
		start := int((p.rr.Add(1) - 1) % uint64(len(p.targets)))
		out := make([]*boundTarget, 0, len(p.targets))
		for i := 0; i < len(p.targets); i++ {
			out = append(out, p.targets[(start+i)%len(p.targets)])
		}
		return out
	}
	return p.targets
}

// onFailover records an identity-free failover + unhealthy signal for a target.
func (m *Membrane) onFailover(capID types.PathwayID, t *boundTarget) {
	if m.observer == nil {
		return
	}
	p := string(t.adapter.ID())
	m.observer.ObserveFailover(string(capID), p)
	m.observer.ObserveHealth(string(capID), p, false)
}

// onHealthy records an identity-free healthy signal for a target.
func (m *Membrane) onHealthy(capID types.PathwayID, t *boundTarget) {
	if m.observer == nil {
		return
	}
	m.observer.ObserveHealth(string(capID), string(t.adapter.ID()), true)
}

// Transport performs a non-streaming crossing with failover across the pool.
func (m *Membrane) Transport(ctx context.Context, sr scrub.SanitizedRequest) (types.CanonicalResponse, error) {
	capID := sr.Pathway()
	tries := m.attempts(ctx, capID)
	if len(tries) == 0 {
		return types.CanonicalResponse{}, ErrNoProvider
	}

	lastErr := ErrNoProvider
	skipped := 0
	for _, at := range tries {
		t := at.t
		if !t.health.available(m.now(), m.backoff) {
			skipped++
			continue
		}
		id, err := m.identity(t)
		if err != nil {
			t.health.recordFailure(m.now(), m.backoff)
			m.onFailover(capID, t)
			lastErr = err
			continue
		}

		cctx := ctx
		var cancel context.CancelFunc
		if t.timeoutTotal > 0 {
			cctx, cancel = context.WithTimeout(ctx, t.timeoutTotal)
		}
		req := providers.Request{Model: at.model, Payload: sr.Payload(), Stream: false}
		resp, err := t.adapter.Complete(cctx, req, id)
		if cancel != nil {
			cancel()
		}
		if err == nil {
			t.health.recordSuccess()
			m.onHealthy(capID, t)
			return resp, nil
		}
		if ctx.Err() != nil {
			return types.CanonicalResponse{}, ctx.Err()
		}
		switch classify(err) {
		case outcomeNoFailover:
			return types.CanonicalResponse{}, err
		default:
			t.health.recordFailure(m.now(), m.backoff)
			m.onFailover(capID, t)
			lastErr = err
		}
	}
	if skipped > 0 && lastErr == ErrNoProvider {
		return types.CanonicalResponse{}, fmt.Errorf("transporter: all targets unavailable (circuit open): %w", ErrNoProvider)
	}
	return types.CanonicalResponse{}, lastErr
}

// TransportStream performs a streaming crossing. It fails over across the pool
// ONLY before the first byte is relayed; once any chunk has been emitted, a
// mid-stream failure terminates cleanly without a silent provider switch.
func (m *Membrane) TransportStream(ctx context.Context, sr scrub.SanitizedRequest, emit func(types.StreamChunk) error) error {
	capID := sr.Pathway()
	tries := m.attempts(ctx, capID)
	if len(tries) == 0 {
		return ErrNoProvider
	}

	lastErr := ErrNoProvider
	for _, at := range tries {
		t := at.t
		if !t.health.available(m.now(), m.backoff) {
			continue
		}
		id, err := m.identity(t)
		if err != nil {
			t.health.recordFailure(m.now(), m.backoff)
			m.onFailover(capID, t)
			lastErr = err
			continue
		}

		wroteAny := false
		guard := func(c types.StreamChunk) error {
			if !c.Done {
				wroteAny = true
			}
			return emit(c)
		}
		req := providers.Request{Model: at.model, Payload: sr.Payload(), Stream: true}
		err = t.adapter.Stream(ctx, req, id, guard)
		if err == nil {
			t.health.recordSuccess()
			m.onHealthy(capID, t)
			return nil
		}
		if wroteAny || ctx.Err() != nil {
			// Bytes already relayed, or the client cancelled: do not fail over.
			return err
		}
		if classify(err) == outcomeNoFailover {
			return err
		}
		t.health.recordFailure(m.now(), m.backoff)
		m.onFailover(capID, t)
		lastErr = err
	}
	return lastErr
}

type outcome int

const (
	outcomeFailover outcome = iota
	outcomeNoFailover
)

// classify decides whether a provider error should trigger failover. Timeouts,
// 5xx, 429/408/409/425, and auth (401/403) are retryable (try the next target);
// other 4xx are request-level and are returned without failover. Unknown errors
// (e.g. transient network) are treated as retryable.
func classify(err error) outcome {
	if errors.Is(err, providers.ErrUpstreamTimeout) || errors.Is(err, context.DeadlineExceeded) {
		return outcomeFailover
	}
	// Tool-calling-unsupported is a request-level incompatibility, not a transient
	// failure: return it immediately rather than masking it via failover.
	if errors.Is(err, providers.ErrToolCallingUnsupported) {
		return outcomeNoFailover
	}
	var se providers.UpstreamStatusError
	if errors.As(err, &se) {
		switch {
		case se.Status >= 500,
			se.Status == 401, se.Status == 403,
			se.Status == 408, se.Status == 409,
			se.Status == 425, se.Status == 429:
			return outcomeFailover
		default:
			return outcomeNoFailover
		}
	}
	return outcomeFailover
}

// --- per-target circuit breaker ---

type breakerState int

const (
	stateClosed breakerState = iota
	stateOpen
	stateHalfOpen
)

type targetHealth struct {
	mu       sync.Mutex
	state    breakerState
	consec   int
	trips    int
	openedAt time.Time
}

// available reports whether the target may be attempted now. An open breaker
// stays closed to traffic until its cooldown elapses, after which a single
// half-open probe is permitted; while that probe is in flight the target is
// unavailable to other requests until its outcome is recorded.
func (h *targetHealth) available(now time.Time, b BackoffPolicy) bool {
	if b.BreakerThreshold <= 0 {
		return true
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	switch h.state {
	case stateOpen:
		if now.Sub(h.openedAt) >= b.cooldownFor(h.trips) {
			h.state = stateHalfOpen
			return true
		}
		return false
	case stateHalfOpen:
		return false // a probe is already in flight
	default:
		return true
	}
}

func (h *targetHealth) recordSuccess() {
	h.mu.Lock()
	h.state = stateClosed
	h.consec = 0
	h.trips = 0
	h.mu.Unlock()
}

func (h *targetHealth) recordFailure(now time.Time, b BackoffPolicy) {
	if b.BreakerThreshold <= 0 {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.state == stateHalfOpen {
		// A half-open probe failed: reopen and escalate the cooldown.
		h.state = stateOpen
		h.openedAt = now
		h.trips++
		return
	}
	h.consec++
	if h.consec >= b.BreakerThreshold {
		h.state = stateOpen
		h.openedAt = now
		h.trips++
	}
}

// Stub is a Transporter that performs no egress. Retained so the no-egress
// contracts/tests remain valid.
type Stub struct{}

// NewStub returns a stub Transporter.
func NewStub() Stub { return Stub{} }

// Transport always returns ErrNotImplemented.
func (Stub) Transport(_ context.Context, _ scrub.SanitizedRequest) (types.CanonicalResponse, error) {
	return types.CanonicalResponse{}, ErrNotImplemented
}

// TransportStream always returns ErrNotImplemented.
func (Stub) TransportStream(_ context.Context, _ scrub.SanitizedRequest, _ func(types.StreamChunk) error) error {
	return fmt.Errorf("stream: %w", ErrNotImplemented)
}
