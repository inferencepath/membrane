package scrub_test

import (
	"testing"

	"github.com/inferencepath/membrane/internal/membrane/permeability"
	"github.com/inferencepath/membrane/internal/membrane/scrub"
	"github.com/inferencepath/membrane/internal/types"
)

func mustSpec(t *testing.T, yaml string) permeability.Spec {
	t.Helper()
	spec, err := permeability.Parse([]byte(yaml))
	if err != nil {
		t.Fatalf("parse spec: %v", err)
	}
	return spec
}

const chatSpec = `
version: 1
pathways:
  chat:
    outbound:
      allow: [messages, temperature, stream]
    inbound:
      default: [message, finish_reason, usage]
      on_reveal: [provider, model]
`

func identityLadenRequest() types.CanonicalRequest {
	return types.CanonicalRequest{
		PathwayID: "chat",
		Payload: map[string]any{
			"messages":      []any{map[string]any{"role": "user", "content": "hi"}},
			"temperature":   0.3,
			"stream":        true,
			"user":          "agent-maria@acme.com",
			"metadata":      map[string]any{"team": "emea"},
			"authorization": "Bearer ck_live_123",
			"reveal":        true,
		},
		Reveal: true,
		Caller: types.Caller{ID: "acme-app", Name: "Acme"},
	}
}

func TestSanitizeDropsToolsWhenNotAllowlisted(t *testing.T) {
	// chatSpec does not allowlist tools or tool_choice, so both are dropped.
	spec := mustSpec(t, chatSpec)
	req := types.CanonicalRequest{
		PathwayID: "chat",
		Payload: map[string]any{
			"messages":    []any{map[string]any{"role": "user", "content": "hi"}},
			"tools":       []any{map[string]any{"name": "get_weather"}},
			"tool_choice": "auto",
		},
	}
	sr, err := scrub.Sanitize(req, spec)
	if err != nil {
		t.Fatalf("Sanitize: %v", err)
	}
	if _, ok := sr.Payload()["tools"]; ok {
		t.Error("tools crossed the Membrane without being allowlisted")
	}
	if _, ok := sr.Payload()["tool_choice"]; ok {
		t.Error("tool_choice crossed the Membrane without being allowlisted")
	}
}

func TestSanitizeAllowsToolsWhenAllowlisted(t *testing.T) {
	spec := mustSpec(t, `
version: 1
pathways:
  chat:
    outbound:
      allow: [messages, tools, tool_choice]
    inbound:
      default: [message, finish_reason, usage]
`)
	req := types.CanonicalRequest{
		PathwayID: "chat",
		Payload: map[string]any{
			"messages":    []any{map[string]any{"role": "user", "content": "hi"}},
			"tools":       []any{map[string]any{"name": "get_weather"}},
			"tool_choice": "auto",
		},
	}
	sr, err := scrub.Sanitize(req, spec)
	if err != nil {
		t.Fatalf("Sanitize: %v", err)
	}
	if _, ok := sr.Payload()["tools"]; !ok {
		t.Error("allowlisted tools were dropped")
	}
	if _, ok := sr.Payload()["tool_choice"]; !ok {
		t.Error("allowlisted tool_choice was dropped")
	}
}

func TestSanitizeDropsIdentityAndReveal(t *testing.T) {
	spec := mustSpec(t, chatSpec)

	sr, err := scrub.Sanitize(identityLadenRequest(), spec)
	if err != nil {
		t.Fatalf("Sanitize: %v", err)
	}

	payload := sr.Payload()
	allowed := map[string]struct{}{"messages": {}, "temperature": {}, "stream": {}}
	for k := range payload {
		if _, ok := allowed[k]; !ok {
			t.Errorf("payload leaked non-allowlisted key %q", k)
		}
	}
	for _, forbidden := range []string{"user", "metadata", "authorization", "reveal"} {
		if _, ok := payload[forbidden]; ok {
			t.Errorf("payload must not contain %q", forbidden)
		}
	}
	if sr.Pathway() != "chat" {
		t.Errorf("pathway = %q, want chat", sr.Pathway())
	}
	if !sr.Stream() {
		t.Errorf("stream = false, want true")
	}
}

func TestPayloadReturnsCopy(t *testing.T) {
	spec := mustSpec(t, chatSpec)
	sr, err := scrub.Sanitize(identityLadenRequest(), spec)
	if err != nil {
		t.Fatal(err)
	}
	p := sr.Payload()
	p["injected"] = true
	if _, ok := sr.Payload()["injected"]; ok {
		t.Fatalf("mutating returned payload must not affect the SanitizedRequest")
	}
}

func TestSanitizeUnknownPathway(t *testing.T) {
	spec := mustSpec(t, chatSpec)
	req := identityLadenRequest()
	req.PathwayID = "nope"
	if _, err := scrub.Sanitize(req, spec); err == nil {
		t.Fatalf("expected error for unknown pathway")
	}
}

func TestSanitizeStreamHandling(t *testing.T) {
	// A non-bool stream value means no streaming.
	spec := mustSpec(t, chatSpec)
	req := identityLadenRequest()
	req.Payload["stream"] = "yes"
	sr, err := scrub.Sanitize(req, spec)
	if err != nil {
		t.Fatal(err)
	}
	if sr.Stream() {
		t.Errorf("stream = true, want false for non-bool value")
	}

	// A stream field outside the allowlist is not copied.
	noStream := mustSpec(t, `
version: 1
pathways:
  chat:
    outbound:
      allow: [messages]
    inbound:
      default: [message]
`)
	sr2, err := scrub.Sanitize(identityLadenRequest(), noStream)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := sr2.Payload()["stream"]; ok {
		t.Errorf("stream must not be copied when not allowlisted")
	}
	if sr2.Stream() {
		t.Errorf("stream = true, want false when not allowlisted")
	}
}
