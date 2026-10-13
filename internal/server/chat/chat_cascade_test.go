package chat_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/inferencepath/membrane/internal/exterior/providers"
	"github.com/inferencepath/membrane/internal/interior/admit"
	"github.com/inferencepath/membrane/internal/interior/resolve"
	"github.com/inferencepath/membrane/internal/membrane/permeability"
	"github.com/inferencepath/membrane/internal/membrane/transporter"
	"github.com/inferencepath/membrane/internal/observability/metrics"
	"github.com/inferencepath/membrane/internal/server/chat"
	"github.com/inferencepath/membrane/internal/types"
	"github.com/inferencepath/membrane/test/harness"
)

// recordingAdapter is a minimal provider adapter that records the requested
// model and optionally fails to drive cascade failover.
type recordingAdapter struct {
	id       types.ProviderID
	model    string
	failWith error
}

func (a *recordingAdapter) ID() types.ProviderID { return a.id }

func (a *recordingAdapter) Complete(_ context.Context, req providers.Request, _ providers.EgressIdentity) (types.CanonicalResponse, error) {
	a.model = req.Model
	if a.failWith != nil {
		return types.CanonicalResponse{}, a.failWith
	}
	return types.CanonicalResponse{FinishReason: "stop", Provider: a.id, Model: req.Model}, nil
}

func (a *recordingAdapter) Stream(_ context.Context, req providers.Request, _ providers.EgressIdentity, emit func(types.StreamChunk) error) error {
	a.model = req.Model
	if a.failWith != nil {
		return a.failWith
	}
	return emit(types.StreamChunk{Done: true})
}

func cascadeHandler(t *testing.T, cascadeAllowed bool, openai, bedrock *recordingAdapter) http.Handler {
	t.Helper()
	spec, err := permeability.Parse([]byte("version: 1\npathways:\n  chat:\n    outbound:\n      allow: [messages, stream]\n    inbound:\n      default: [message, finish_reason, usage]\n      on_reveal: [provider, model]\n"))
	if err != nil {
		t.Fatal(err)
	}
	store, err := admit.ParseStore([]byte("callers:\n  - key_id: ck_demo\n    caller_id: acme\n    hash: " + admit.HashSecret("s3cr3t-demo-key") + "\n"))
	if err != nil {
		t.Fatal(err)
	}
	pools := map[types.PathwayID]transporter.PoolSpec{"chat": {Strategy: transporter.StrategyPriority, Targets: []transporter.TargetSpec{
		{Adapter: openai, ProviderName: "openai-main", KeyringRef: "openai-main", Model: "gpt-4o-mini", Priority: 0},
		{Adapter: bedrock, ProviderName: "bedrock-main", KeyringRef: "bedrock-main", Model: "claude-3-5-sonnet", Priority: 1},
	}}}
	trans := transporter.New(pools, fakeKR{})
	resolver := resolve.New(map[types.PathwayID]resolve.Binding{"chat": {}})
	resolver.SetPolicy(map[types.PathwayID]resolve.PathwayPolicy{"chat": {
		DefaultProvider: "openai-main",
		CascadeAllowed:  cascadeAllowed,
		Providers: map[string]resolve.ProviderPolicy{
			"openai-main":  {Models: []string{"gpt-4o", "gpt-4o-mini"}, DefaultModel: "gpt-4o-mini"},
			"bedrock-main": {Models: []string{"claude-3-5-sonnet"}, DefaultModel: "claude-3-5-sonnet"},
		},
	}})
	return chat.New(spec, store, resolver, trans, slog.New(slog.NewTextHandler(io.Discard, nil)), metrics.Nop{})
}

type fakeKR struct{}

func (fakeKR) Resolve(string) (providers.EgressIdentity, error) {
	return providers.EgressIdentity{Secret: "tok"}, nil
}

func TestChatCascadeDefaultWhenDisallowed(t *testing.T) {
	openai := &recordingAdapter{id: "openai"}
	bedrock := &recordingAdapter{id: "bedrock"}
	ts := httptest.NewServer(cascadeHandler(t, false, openai, bedrock))
	defer ts.Close()

	// cascade_allowed=false: targets are ignored, default provider+model used.
	body := `{"pathway":"chat","messages":[{"role":"user","content":"hi"}],"targets":[{"provider":"bedrock-main","model":"claude-3-5-sonnet"}]}`
	resp := post(t, ts.URL+chat.Route, harness.CallerKey, body)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	if bedrock.model != "" {
		t.Fatalf("bedrock should not be used, got model %q", bedrock.model)
	}
	if openai.model != "gpt-4o-mini" {
		t.Fatalf("default model = %q, want gpt-4o-mini", openai.model)
	}
}

func TestChatCascadeHonorsTargets(t *testing.T) {
	openai := &recordingAdapter{id: "openai"}
	bedrock := &recordingAdapter{id: "bedrock"}
	ts := httptest.NewServer(cascadeHandler(t, true, openai, bedrock))
	defer ts.Close()

	body := `{"pathway":"chat","messages":[{"role":"user","content":"hi"}],"targets":[{"provider":"bedrock-main","model":"claude-3-5-sonnet","cascade":1}]}`
	resp := post(t, ts.URL+chat.Route, harness.CallerKey, body)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	if openai.model != "" {
		t.Fatalf("openai should not be used, got %q", openai.model)
	}
	if bedrock.model != "claude-3-5-sonnet" {
		t.Fatalf("bedrock model = %q", bedrock.model)
	}
}

func TestChatCascadeRejectsDisallowedModel(t *testing.T) {
	openai := &recordingAdapter{id: "openai"}
	bedrock := &recordingAdapter{id: "bedrock"}
	ts := httptest.NewServer(cascadeHandler(t, true, openai, bedrock))
	defer ts.Close()

	body := `{"pathway":"chat","messages":[{"role":"user","content":"hi"}],"targets":[{"provider":"openai-main","model":"gpt-5-ultra"}]}`
	resp := post(t, ts.URL+chat.Route, harness.CallerKey, body)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
	var out struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&out)
	if out.Error.Code != "target_not_allowed" {
		t.Fatalf("code = %q, want target_not_allowed", out.Error.Code)
	}
	if openai.model != "" || bedrock.model != "" {
		t.Fatalf("no adapter should be called on validation failure")
	}
}
