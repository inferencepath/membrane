package scrub_test

import (
	"testing"

	"github.com/inferencepath/membrane/internal/membrane/permeability"
	"github.com/inferencepath/membrane/internal/membrane/scrub"
	"github.com/inferencepath/membrane/internal/types"
)

func inwardSpec(t *testing.T) permeability.Spec {
	t.Helper()
	spec, err := permeability.Parse([]byte(`
version: 1
pathways:
  chat:
    outbound:
      allow: [messages]
    inbound:
      default: [message, finish_reason, usage]
      on_reveal: [provider, model]
`))
	if err != nil {
		t.Fatal(err)
	}
	return spec
}

func providerResponse() types.CanonicalResponse {
	return types.CanonicalResponse{
		Message:      types.Message{Role: "assistant", Content: "hi"},
		FinishReason: "stop",
		Usage:        types.Usage{PromptTokens: 1, CompletionTokens: 2, TotalTokens: 3},
		Provider:     "openai",
		Model:        "gpt-4o-mini",
	}
}

func TestScrubResponseNeutralByDefault(t *testing.T) {
	out, err := scrub.ScrubResponse(providerResponse(), inwardSpec(t), "chat", false)
	if err != nil {
		t.Fatal(err)
	}
	if out.Provider != "" || out.Model != "" {
		t.Fatalf("default response must be provider-neutral, got provider=%q model=%q", out.Provider, out.Model)
	}
	if out.Message.Content != "hi" || out.FinishReason != "stop" || out.Usage.TotalTokens != 3 {
		t.Fatalf("neutral fields wrong: %+v", out)
	}
}

func TestScrubResponseRevealIncludesProviderModel(t *testing.T) {
	out, err := scrub.ScrubResponse(providerResponse(), inwardSpec(t), "chat", true)
	if err != nil {
		t.Fatal(err)
	}
	if out.Provider != "openai" || out.Model != "gpt-4o-mini" {
		t.Fatalf("reveal response must include provider/model, got %+v", out)
	}
}

func TestScrubResponseRevealGatedByOnRevealSet(t *testing.T) {
	// on_reveal omits provider/model → even with reveal, they stay empty.
	spec, err := permeability.Parse([]byte(`
version: 1
pathways:
  chat:
    outbound:
      allow: [messages]
    inbound:
      default: [message, finish_reason, usage]
`))
	if err != nil {
		t.Fatal(err)
	}
	out, err := scrub.ScrubResponse(providerResponse(), spec, "chat", true)
	if err != nil {
		t.Fatal(err)
	}
	if out.Provider != "" || out.Model != "" {
		t.Fatalf("provider/model must stay empty when not in on_reveal, got %+v", out)
	}
}

func TestScrubResponseUnknownPathway(t *testing.T) {
	if _, err := scrub.ScrubResponse(providerResponse(), inwardSpec(t), "nope", false); err == nil {
		t.Fatal("expected error for unknown pathway")
	}
}
