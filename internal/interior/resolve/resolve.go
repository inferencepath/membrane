// Package resolve maps a logical Pathway to a concrete Binding: a Provider, a
// model, and per-Binding timeouts. It is Interior-only, and Bindings are never
// disclosed to a Caller.
//
// A Pathway resolves to a Pool of targets with a deterministic selection
// Strategy, priority or round-robin. A single Binding is a one-target priority
// Pool.
package resolve

import (
	"errors"
	"sort"
	"sync/atomic"
	"time"

	"github.com/inferencepath/membrane/internal/types"
)

// ErrTargetNotAllowed is returned by Plan when a caller cascade references a
// provider not attached to the Pathway, or a model not in that provider's
// allowed list. The message contains no routing details.
var ErrTargetNotAllowed = errors.New("provider/model not allowed in this pathway")

// PlanTarget is one resolved cascade tier: the configured provider (by name) and
// the concrete model to request. It is Interior-only; the Membrane maps the
// provider name to an adapter and the model is forwarded as the request model.
type PlanTarget struct {
	Provider string
	Model    string
}

// ProviderPolicy is a provider's admin-controlled model allowlist and default.
type ProviderPolicy struct {
	Models       []string
	DefaultModel string
}

// PathwayPolicy is a Pathway's provider allowlist, default provider, and whether
// a caller may supply its own cascade. Providers maps a provider name to its
// model policy.
type PathwayPolicy struct {
	// Operation is the egress operation this Pathway serves (default chat).
	Operation       types.Operation
	Providers       map[string]ProviderPolicy
	DefaultProvider string
	CascadeAllowed  bool
}

func (p ProviderPolicy) allows(model string) bool {
	for _, m := range p.Models {
		if m == model {
			return true
		}
	}
	return false
}

// Default per-Binding timeouts.
const (
	DefaultTimeoutTotal = 60 * time.Second
	DefaultTimeoutIdle  = 30 * time.Second
)

// Strategy is a deterministic target-selection policy for a Pool.
type Strategy string

const (
	// StrategyPriority always selects the lowest-priority-number target.
	StrategyPriority Strategy = "priority"
	// StrategyRoundRobin cycles across targets in priority order.
	StrategyRoundRobin Strategy = "round_robin"
)

// Binding is the resolved egress target for a Pathway. It does not include the
// Provider endpoint or credential; those are applied at the Membrane or later
// and never appear in the Interior resolution result.
//
// Provider is a pool-local target key that the Caller never sees. The
// Transporter maps it to a concrete adapter and the Membrane attaches the
// credential. It is never a caller or org identity.
type Binding struct {
	Provider types.ProviderID
	Model    string
	// Priority orders targets within a Pool (lower is preferred).
	Priority int
	// TimeoutTotal bounds a non-streaming upstream call.
	TimeoutTotal time.Duration
	// TimeoutIdle bounds inactivity on a streaming upstream call.
	TimeoutIdle time.Duration
}

// Pool is a set of interchangeable targets for one Pathway with a selection
// Strategy. Selection is deterministic and does not depend on Caller identity.
// Pool has no runtime state, so it can be copied freely.
type Pool struct {
	Strategy Strategy
	Targets  []Binding
}

// poolRuntime holds a Pool plus its selection state, such as the round-robin
// cursor. It must not be copied after construction.
type poolRuntime struct {
	strategy Strategy
	targets  []Binding
	rr       atomic.Uint64
}

// Resolver holds the static map from Pathway to Pool and, in managed mode, the
// per-Pathway routing policy consulted by Plan.
type Resolver struct {
	pools    map[types.PathwayID]*poolRuntime
	policies map[types.PathwayID]PathwayPolicy
}

// SetPolicy installs the Pathway routing policies consulted by Plan. Without a
// policy for a Pathway, Plan returns nil and the Transporter uses the Pool.
func (r *Resolver) SetPolicy(policies map[types.PathwayID]PathwayPolicy) {
	r.policies = policies
}

// Plan computes the ordered cascade for a request. It applies the Pathway's
// cascade policy:
//
//   - With no policy for the Pathway, the plan is nil and the Transporter uses
//     its pool.
//   - If cascade_allowed is false or the caller supplied no targets, the plan
//     is a single tier: the default provider with its default model. The caller
//     cannot choose a model in this case.
//   - If cascade_allowed is true and targets are given, the plan is the targets
//     in ascending cascade order. Each target is validated first: its provider
//     must be attached to the Pathway, and its model (or the provider default)
//     must be in that provider's allowed list. Any violation returns
//     ErrTargetNotAllowed and no plan.
//
// A caller-chosen model is never substituted across the cascade.
func (r *Resolver) Plan(pathway types.PathwayID, targets []types.ChatTarget) ([]PlanTarget, error) {
	policy, ok := r.policies[pathway]
	if !ok {
		return nil, nil
	}
	if !policy.CascadeAllowed || len(targets) == 0 {
		def := policy.Providers[policy.DefaultProvider]
		return []PlanTarget{{Provider: policy.DefaultProvider, Model: def.DefaultModel}}, nil
	}

	ordered := make([]types.ChatTarget, len(targets))
	copy(ordered, targets)
	sort.SliceStable(ordered, func(i, j int) bool {
		return ordered[i].Cascade < ordered[j].Cascade
	})

	plan := make([]PlanTarget, 0, len(ordered))
	for _, t := range ordered {
		pp, ok := policy.Providers[t.Provider]
		if !ok {
			return nil, ErrTargetNotAllowed
		}
		model := t.Model
		if model == "" {
			model = pp.DefaultModel
		}
		if !pp.allows(model) {
			return nil, ErrTargetNotAllowed
		}
		plan = append(plan, PlanTarget{Provider: t.Provider, Model: model})
	}
	return plan, nil
}

// New returns a Resolver over single static Bindings, applying default timeouts
// where unset. Each Binding becomes a one-target priority Pool.
func New(bindings map[types.PathwayID]Binding) *Resolver {
	pools := make(map[types.PathwayID]Pool, len(bindings))
	for cap, b := range bindings {
		pools[cap] = Pool{Strategy: StrategyPriority, Targets: []Binding{b}}
	}
	return NewWithPools(pools)
}

// NewWithPools returns a Resolver over Pools, applying default timeouts where
// unset and ordering each Pool's targets by priority (ascending). An unknown or
// empty Strategy defaults to priority.
func NewWithPools(pools map[types.PathwayID]Pool) *Resolver {
	normalized := make(map[types.PathwayID]*poolRuntime, len(pools))
	for cap, p := range pools {
		np := &poolRuntime{strategy: normalizeStrategy(p.Strategy)}
		np.targets = make([]Binding, len(p.Targets))
		for i, b := range p.Targets {
			if b.TimeoutTotal <= 0 {
				b.TimeoutTotal = DefaultTimeoutTotal
			}
			if b.TimeoutIdle <= 0 {
				b.TimeoutIdle = DefaultTimeoutIdle
			}
			np.targets[i] = b
		}
		sort.SliceStable(np.targets, func(i, j int) bool {
			return np.targets[i].Priority < np.targets[j].Priority
		})
		normalized[cap] = np
	}
	return &Resolver{pools: normalized}
}

func normalizeStrategy(s Strategy) Strategy {
	if s == StrategyRoundRobin {
		return StrategyRoundRobin
	}
	return StrategyPriority
}

// Operation returns the egress operation configured for a Pathway, and whether a
// policy exists for it. A policy with an empty Operation reports the default,
// chat. A Pathway with no policy reports (DefaultOperation, false).
func (r *Resolver) Operation(pathway types.PathwayID) (types.Operation, bool) {
	policy, ok := r.policies[pathway]
	if !ok {
		return types.DefaultOperation, false
	}
	if policy.Operation == "" {
		return types.DefaultOperation, true
	}
	return policy.Operation, true
}

// Resolve selects a Binding for a Pathway from its Pool, and whether one
// exists. Selection is deterministic for priority Pools and round-robins for
// round-robin Pools; it never depends on Caller identity.
func (r *Resolver) Resolve(pathway types.PathwayID) (Binding, bool) {
	p, ok := r.pools[pathway]
	if !ok {
		return Binding{}, false
	}
	return p.selectBinding()
}

func (p *poolRuntime) selectBinding() (Binding, bool) {
	if len(p.targets) == 0 {
		return Binding{}, false
	}
	if p.strategy == StrategyRoundRobin {
		n := p.rr.Add(1) - 1
		return p.targets[int(n%uint64(len(p.targets)))], true
	}
	// Targets are sorted by priority, so the first is preferred.
	return p.targets[0], true
}
