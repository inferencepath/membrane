package transporter_test

import (
	"context"
	"testing"

	"github.com/inferencepath/membrane/internal/exterior/providers"
	"github.com/inferencepath/membrane/internal/membrane/transporter"
	"github.com/inferencepath/membrane/internal/types"
)

// namedPool builds a one-Pathway pool of named targets for plan selection.
func namedPool(targets ...transporter.TargetSpec) map[types.PathwayID]transporter.PoolSpec {
	return map[types.PathwayID]transporter.PoolSpec{"chat": {Strategy: transporter.StrategyPriority, Targets: targets}}
}

func namedTarget(a providers.Provider, name string, priority int) transporter.TargetSpec {
	return transporter.TargetSpec{Adapter: a, ProviderName: name, KeyringRef: name, Model: "default-model", Priority: priority}
}

// TestTransportPlanSelectsProviderAndModel checks that a plan picks the named
// provider and the Caller's model.
func TestTransportPlanSelectsProviderAndModel(t *testing.T) {
	openai := &fakeProvider{id: "openai", resp: types.CanonicalResponse{FinishReason: "stop"}}
	bedrock := &fakeProvider{id: "bedrock", resp: types.CanonicalResponse{FinishReason: "stop"}}
	m := transporter.New(
		namedPool(namedTarget(openai, "openai-main", 0), namedTarget(bedrock, "bedrock-main", 1)),
		fakeKeyring{def: providers.EgressIdentity{Secret: "tok"}},
	)

	ctx := transporter.WithPlan(context.Background(), []transporter.PlanTier{
		{Provider: "bedrock-main", Model: "claude-3-5-sonnet"},
	})
	if _, err := m.Transport(ctx, sanitize(t, false)); err != nil {
		t.Fatalf("Transport: %v", err)
	}
	if openai.calls != 0 {
		t.Fatalf("openai should not be called, calls=%d", openai.calls)
	}
	if bedrock.calls != 1 || bedrock.gotReq.Model != "claude-3-5-sonnet" {
		t.Fatalf("bedrock calls=%d model=%q", bedrock.calls, bedrock.gotReq.Model)
	}
}

// TestTransportPlanCascadeFailover checks that failover follows plan order,
// with each tier keeping its own model.
func TestTransportPlanCascadeFailover(t *testing.T) {
	first := &fakeProvider{id: "openai", returnErr: providers.UpstreamStatusError{Status: 500}}
	second := &fakeProvider{id: "bedrock", resp: types.CanonicalResponse{FinishReason: "stop"}}
	m := transporter.New(
		namedPool(namedTarget(first, "openai-main", 0), namedTarget(second, "bedrock-main", 1)),
		fakeKeyring{def: providers.EgressIdentity{Secret: "tok"}},
	)
	ctx := transporter.WithPlan(context.Background(), []transporter.PlanTier{
		{Provider: "openai-main", Model: "gpt-4o"},
		{Provider: "bedrock-main", Model: "claude-3-5-sonnet"},
	})
	if _, err := m.Transport(ctx, sanitize(t, false)); err != nil {
		t.Fatalf("Transport: %v", err)
	}
	if first.calls != 1 || first.gotReq.Model != "gpt-4o" {
		t.Fatalf("first calls=%d model=%q", first.calls, first.gotReq.Model)
	}
	if second.calls != 1 || second.gotReq.Model != "claude-3-5-sonnet" {
		t.Fatalf("second calls=%d model=%q", second.calls, second.gotReq.Model)
	}
}

// TestTransportPlanDefaultModel checks that an empty tier model uses the
// target's default.
func TestTransportPlanDefaultModel(t *testing.T) {
	openai := &fakeProvider{id: "openai", resp: types.CanonicalResponse{FinishReason: "stop"}}
	m := transporter.New(
		namedPool(namedTarget(openai, "openai-main", 0)),
		fakeKeyring{def: providers.EgressIdentity{Secret: "tok"}},
	)
	ctx := transporter.WithPlan(context.Background(), []transporter.PlanTier{{Provider: "openai-main"}})
	if _, err := m.Transport(ctx, sanitize(t, false)); err != nil {
		t.Fatalf("Transport: %v", err)
	}
	if openai.gotReq.Model != "default-model" {
		t.Fatalf("model = %q, want default-model", openai.gotReq.Model)
	}
}
