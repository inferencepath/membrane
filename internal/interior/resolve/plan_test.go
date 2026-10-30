package resolve_test

import (
	"errors"
	"testing"

	"github.com/inferencepath/membrane/internal/interior/resolve"
	"github.com/inferencepath/membrane/internal/types"
)

func policyResolver(cascadeAllowed bool) *resolve.Resolver {
	r := resolve.New(map[types.PathwayID]resolve.Binding{"chat": {}})
	r.SetPolicy(map[types.PathwayID]resolve.PathwayPolicy{
		"chat": {
			DefaultProvider: "openai-main",
			CascadeAllowed:  cascadeAllowed,
			Providers: map[string]resolve.ProviderPolicy{
				"openai-main":  {Models: []string{"gpt-4o", "gpt-4o-mini"}, DefaultModel: "gpt-4o-mini"},
				"bedrock-main": {Models: []string{"claude-3-5-sonnet"}, DefaultModel: "claude-3-5-sonnet"},
			},
		},
	})
	return r
}

func TestPlanNoPolicyReturnsNil(t *testing.T) {
	r := resolve.New(map[types.PathwayID]resolve.Binding{"chat": {}})
	plan, err := r.Plan("chat", nil)
	if err != nil || plan != nil {
		t.Fatalf("plan = %+v, err = %v; want nil,nil", plan, err)
	}
}

func TestPlanDefaultWhenNoTargets(t *testing.T) {
	r := policyResolver(true)
	plan, err := r.Plan("chat", nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan) != 1 || plan[0].Provider != "openai-main" || plan[0].Model != "gpt-4o-mini" {
		t.Fatalf("plan = %+v", plan)
	}
}

func TestPlanIgnoresTargetsWhenCascadeDisallowed(t *testing.T) {
	r := policyResolver(false)
	plan, err := r.Plan("chat", []types.ChatTarget{{Provider: "bedrock-main", Model: "claude-3-5-sonnet"}})
	if err != nil {
		t.Fatal(err)
	}
	// With cascade_allowed false the targets are ignored and the default
	// provider and model are used.
	if len(plan) != 1 || plan[0].Provider != "openai-main" || plan[0].Model != "gpt-4o-mini" {
		t.Fatalf("plan = %+v", plan)
	}
}

func TestPlanOrdersByCascadeAndIsSticky(t *testing.T) {
	r := policyResolver(true)
	plan, err := r.Plan("chat", []types.ChatTarget{
		{Provider: "bedrock-main", Model: "claude-3-5-sonnet", Cascade: 3},
		{Provider: "openai-main", Model: "gpt-4o", Cascade: 1},
		{Provider: "openai-main", Cascade: 2}, // model omitted, so the provider default applies
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []resolve.PlanTarget{
		{Provider: "openai-main", Model: "gpt-4o"},
		{Provider: "openai-main", Model: "gpt-4o-mini"},
		{Provider: "bedrock-main", Model: "claude-3-5-sonnet"},
	}
	if len(plan) != len(want) {
		t.Fatalf("plan = %+v", plan)
	}
	for i := range want {
		if plan[i] != want[i] {
			t.Fatalf("plan[%d] = %+v, want %+v", i, plan[i], want[i])
		}
	}
}

func TestPlanRejectsUnknownProvider(t *testing.T) {
	r := policyResolver(true)
	_, err := r.Plan("chat", []types.ChatTarget{{Provider: "nope", Model: "gpt-4o"}})
	if !errors.Is(err, resolve.ErrTargetNotAllowed) {
		t.Fatalf("err = %v, want ErrTargetNotAllowed", err)
	}
}

func TestPlanRejectsDisallowedModel(t *testing.T) {
	r := policyResolver(true)
	_, err := r.Plan("chat", []types.ChatTarget{{Provider: "openai-main", Model: "gpt-5-ultra"}})
	if !errors.Is(err, resolve.ErrTargetNotAllowed) {
		t.Fatalf("err = %v, want ErrTargetNotAllowed", err)
	}
}
