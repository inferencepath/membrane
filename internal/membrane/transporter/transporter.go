// Package transporter implements the Membrane crossing. The Transporter is the
// only path across the Membrane, and it accepts only a scrub.SanitizedRequest.
//
// Membrane, the concrete Transporter, keeps an ordered pool of Provider
// targets per Pathway. For each request it picks a target by priority or
// round-robin, resolves the target's Egress Identity from the Keyring, and
// sends the allowlisted payload. Egress Identity is attached only here. On a
// retryable failure it moves to the next healthy target, and a per-target
// circuit breaker skips targets that keep failing. Streaming fails over only
// before the first byte is relayed.
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

// ErrNoProvider indicates that no target is available to serve the request.
var ErrNoProvider = errors.New("transporter: no provider adapter for target")

// Transporter carries a SanitizedRequest across the Membrane, attaches the
// Egress Identity, and dispatches it to a Provider. Both methods take a
// scrub.SanitizedRequest, so an unscrubbed request cannot be sent.
type Transporter interface {
	Transport(ctx context.Context, req scrub.SanitizedRequest) (types.CanonicalResponse, error)
	TransportStream(ctx context.Context, req scrub.SanitizedRequest, emit func(types.StreamChunk) error) error
}

// KeyringResolver resolves an Egress Identity by credential reference. The
// Exterior Keyring implements it.
type KeyringResolver interface {
	Resolve(ref string) (providers.EgressIdentity, error)
}

// Strategy is a deterministic target-selection policy.
type Strategy string

const (
	// StrategyPriority prefers the healthy target with the lowest priority
	// number.
	StrategyPriority Strategy = "priority"
	// StrategyRoundRobin cycles across healthy targets in priority order.
	StrategyRoundRobin Strategy = "round_robin"
)

// TargetSpec describes one pooled egress target: its adapter, the Keyring
// reference for its Egress Identity, the model, and timeouts. Endpoints,
// regions, and deployments live in the adapter and do not reach the Interior.
type TargetSpec struct {
	Adapter providers.Provider
	// ProviderName is the operator's name for the configured provider, such
	// as "openai-main". A Caller's cascade selects targets by this name. It is
	// not Caller identity and is not sent to the Provider.
	ProviderName string
	KeyringRef   string
	Model        string
	Priority     int
	TimeoutTotal time.Duration
	TimeoutIdle  time.Duration
	// AmbientAuth marks a target whose adapter authenticates from an ambient
	// source such as an IAM role or managed identity. There is no Keyring
	// credential to resolve, and the adapter gets an empty Egress Identity.
	AmbientAuth bool
}

// PoolSpec is a Pathway's set of targets plus its selection Strategy.
type PoolSpec struct {
	Strategy Strategy
	Targets  []TargetSpec
}

// BackoffPolicy configures the per-target circuit breaker. A zero
// BreakerThreshold disables the breaker.
//
// When Base > 0 the cooldown grows with consecutive trips: the Nth waits
// Base*Factor^(N-1), capped at Max, so half-open probes back off from a target
// that keeps failing. When Base <= 0 every trip uses BreakerCooldown.
type BackoffPolicy struct {
	Base             time.Duration
	Factor           float64
	Max              time.Duration
	BreakerThreshold int
	BreakerCooldown  time.Duration
}

// cooldownFor returns the open-state cooldown after trips consecutive trips,
// counting from 1.
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

// Observer receives failover and target health signals. The provider argument
// is the Provider family label; no Caller identity is passed.
type Observer interface {
	ObserveFailover(pathway, provider string)
	ObserveHealth(pathway, provider string, healthy bool)
}

// PlanTier is one step of a Caller's cascade: a configured provider, by name,
// and one of its allowed models. An empty Model means the provider's default.
// The handler validates each tier against the Pathway, orders them, and
// attaches the plan with WithPlan.
//
// A plan names providers and models by configured key and holds no Caller
// identity. It travels in the context, not in the payload.
type PlanTier struct {
	Provider string
	Model    string
}

type planCtxKey struct{}

// WithPlan returns a context carrying a Caller's cascade plan. When present,
// Transport and TransportStream try the named providers in plan order instead
// of the Pathway's pool order.
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

// attempt is one dispatch try: a target and the model to request, which may be
// a Caller-chosen model instead of the target default.
type attempt struct {
	t     *boundTarget
	model string
}

// identity resolves the Egress Identity for t. Ambient-auth targets get an
// empty identity without a Keyring lookup.
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

// WithObserver sets the Observer for failover and health signals. A nil
// Observer is ignored.
func WithObserver(o Observer) Option {
	return func(m *Membrane) {
		if o != nil {
			m.observer = o
		}
	}
}

// New returns a Membrane Transporter for the given pools. Each Pathway's
// targets are sorted by ascending priority.
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

// attempts returns the ordered dispatch list for a request. With a cascade plan
// in ctx it resolves each tier's provider by name, using the tier's model or
// the target default. Otherwise it uses the pool's selection order and each
// target's default model.
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

// onFailover reports a failover and marks t unhealthy.
func (m *Membrane) onFailover(capID types.PathwayID, t *boundTarget) {
	if m.observer == nil {
		return
	}
	p := string(t.adapter.ID())
	m.observer.ObserveFailover(string(capID), p)
	m.observer.ObserveHealth(string(capID), p, false)
}

// onHealthy reports t as healthy.
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

// TransportStream performs a streaming crossing. It fails over only before the
// first chunk is relayed; after that, a failure ends the stream rather than
// silently switching providers.
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

// classify decides whether a provider error triggers failover. Timeouts, 5xx,
// 401, 403, 408, 409, 425, and 429 move on to the next target; other 4xx
// statuses are returned as is. Unrecognized errors, such as network failures,
// are treated as retryable.
func classify(err error) outcome {
	if errors.Is(err, providers.ErrUpstreamTimeout) || errors.Is(err, context.DeadlineExceeded) {
		return outcomeFailover
	}
	// Unsupported tool calling is a problem with the request, not a transient
	// failure, so failover would only hide it.
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

// Per-target circuit breaker.

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

// available reports whether the target may be tried now. An open breaker
// rejects traffic until its cooldown elapses, then admits one half-open probe.
// Other requests are turned away until the probe's outcome is recorded.
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

// Stub is a Transporter that performs no egress.
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
