package resolve_test

import (
	"testing"
	"time"

	"github.com/inferencepath/membrane/internal/interior/resolve"
	"github.com/inferencepath/membrane/internal/types"
)

func TestPriorityPrefersLowestPriorityNumber(t *testing.T) {
	r := resolve.NewWithPools(map[types.PathwayID]resolve.Pool{
		"chat": {
			Strategy: resolve.StrategyPriority,
			Targets: []resolve.Binding{
				{Provider: "chat#1", Model: "b", Priority: 5},
				{Provider: "chat#0", Model: "a", Priority: 0},
			},
		},
	})
	for i := 0; i < 3; i++ {
		b, ok := r.Resolve("chat")
		if !ok || b.Provider != "chat#0" {
			t.Fatalf("priority select = %+v ok=%v, want chat#0", b, ok)
		}
	}
}

func TestRoundRobinCyclesInPriorityOrder(t *testing.T) {
	r := resolve.NewWithPools(map[types.PathwayID]resolve.Pool{
		"chat": {
			Strategy: resolve.StrategyRoundRobin,
			Targets: []resolve.Binding{
				{Provider: "chat#2", Priority: 2},
				{Provider: "chat#0", Priority: 0},
				{Provider: "chat#1", Priority: 1},
			},
		},
	})
	want := []types.ProviderID{"chat#0", "chat#1", "chat#2", "chat#0", "chat#1"}
	for i, w := range want {
		b, ok := r.Resolve("chat")
		if !ok || b.Provider != w {
			t.Fatalf("rr[%d] = %q ok=%v, want %q", i, b.Provider, ok, w)
		}
	}
}

func TestNewWithPoolsAppliesDefaultTimeouts(t *testing.T) {
	r := resolve.NewWithPools(map[types.PathwayID]resolve.Pool{
		"chat": {Targets: []resolve.Binding{{Provider: "chat#0"}}},
	})
	b, _ := r.Resolve("chat")
	if b.TimeoutTotal != resolve.DefaultTimeoutTotal || b.TimeoutIdle != resolve.DefaultTimeoutIdle {
		t.Fatalf("timeouts = %v/%v, want defaults", b.TimeoutTotal, b.TimeoutIdle)
	}
}

func TestNewWithPoolsKeepsExplicitTimeouts(t *testing.T) {
	r := resolve.NewWithPools(map[types.PathwayID]resolve.Pool{
		"chat": {Targets: []resolve.Binding{{Provider: "chat#0", TimeoutTotal: time.Second, TimeoutIdle: 2 * time.Second}}},
	})
	b, _ := r.Resolve("chat")
	if b.TimeoutTotal != time.Second || b.TimeoutIdle != 2*time.Second {
		t.Fatalf("timeouts = %v/%v", b.TimeoutTotal, b.TimeoutIdle)
	}
}

func TestUnknownStrategyDefaultsToPriority(t *testing.T) {
	r := resolve.NewWithPools(map[types.PathwayID]resolve.Pool{
		"chat": {Strategy: "bogus", Targets: []resolve.Binding{
			{Provider: "chat#0", Priority: 0}, {Provider: "chat#1", Priority: 1},
		}},
	})
	b, _ := r.Resolve("chat")
	if b.Provider != "chat#0" {
		t.Fatalf("provider = %q, want chat#0", b.Provider)
	}
}

func TestResolveUnknownPathway(t *testing.T) {
	r := resolve.NewWithPools(map[types.PathwayID]resolve.Pool{})
	if _, ok := r.Resolve("nope"); ok {
		t.Fatal("want not found")
	}
}
