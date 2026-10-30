// Package diffusionleak holds the Diffusion-leak suite. It proves the boundary
// types cannot express a leak (structural form) and extends that to the live
// egress path.
package diffusionleak

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/inferencepath/membrane/internal/membrane/permeability"
	"github.com/inferencepath/membrane/internal/membrane/scrub"
	"github.com/inferencepath/membrane/internal/membrane/transporter"
	"github.com/inferencepath/membrane/internal/types"
)

// denylistSubstrings are identity/control terms that must not appear as fields
// of any type that crosses the Membrane.
var denylistSubstrings = []string{
	"caller", "key", "auth", "ip", "forwarded", "tenant",
	"user", "metadata", "cookie", "agent", "reveal", "token", "secret",
}

// TestSanitizedRequestHasNoExportedFields checks that no external package can
// read or set a SanitizedRequest field, so identity cannot be injected into one.
func TestSanitizedRequestHasNoExportedFields(t *testing.T) {
	rt := reflect.TypeOf(scrub.SanitizedRequest{})
	for i := 0; i < rt.NumField(); i++ {
		f := rt.Field(i)
		if f.IsExported() {
			t.Errorf("SanitizedRequest.%s is exported; the boundary type must have no exported fields", f.Name)
		}
	}
}

// TestSanitizedRequestFieldsAreNotIdentity proves the (unexported) field names
// of the boundary type do not resemble identity/control fields.
func TestSanitizedRequestFieldsAreNotIdentity(t *testing.T) {
	rt := reflect.TypeOf(scrub.SanitizedRequest{})
	for i := 0; i < rt.NumField(); i++ {
		name := strings.ToLower(rt.Field(i).Name)
		for _, bad := range denylistSubstrings {
			if strings.Contains(name, bad) {
				t.Errorf("SanitizedRequest field %q resembles identity/control term %q", name, bad)
			}
		}
	}
}

// TestTransporterAcceptsOnlySanitizedRequest proves the Membrane crossing accepts
// exactly scrub.SanitizedRequest and nothing else.
func TestTransporterAcceptsOnlySanitizedRequest(t *testing.T) {
	it := reflect.TypeOf((*transporter.Transporter)(nil)).Elem()
	m, ok := it.MethodByName("Transport")
	if !ok {
		t.Fatal("Transporter has no Transport method")
	}
	// Interface method type: In(0)=context.Context, In(1)=scrub.SanitizedRequest.
	if m.Type.NumIn() != 2 {
		t.Fatalf("Transport takes %d params, want 2", m.Type.NumIn())
	}
	want := reflect.TypeOf(scrub.SanitizedRequest{})
	if got := m.Type.In(1); got != want {
		t.Fatalf("Transport request param = %v, want %v", got, want)
	}
	ctxType := reflect.TypeOf((*context.Context)(nil)).Elem()
	if m.Type.In(0) != ctxType {
		t.Fatalf("Transport first param = %v, want context.Context", m.Type.In(0))
	}
}

// TestScrubDropsIdentityOnTheWire proves that, given an identity-laden request,
// the Scrub produces a payload free of identity and the Reveal flag.
func TestScrubDropsIdentityOnTheWire(t *testing.T) {
	spec, err := permeability.Parse([]byte(`
version: 1
pathways:
  chat:
    outbound:
      allow: [messages, temperature, stream]
    inbound:
      default: [message, finish_reason, usage]
      on_reveal: [provider, model]
`))
	if err != nil {
		t.Fatal(err)
	}

	req := types.CanonicalRequest{
		PathwayID: "chat",
		Payload: map[string]any{
			"messages":        []any{map[string]any{"role": "user", "content": "hi"}},
			"temperature":     0.3,
			"stream":          false,
			"user":            "agent@acme.com",
			"metadata":        map[string]any{"tenant": "emea"},
			"authorization":   "Bearer ck_live",
			"caller_key":      "ck_live",
			"x_forwarded_for": "10.0.0.1",
			"reveal":          true,
		},
		Reveal: true,
		Caller: types.Caller{ID: "acme"},
	}

	sr, err := scrub.Sanitize(req, spec)
	if err != nil {
		t.Fatal(err)
	}

	payload := sr.Payload()
	forbidden := []string{"user", "metadata", "authorization", "caller_key", "x_forwarded_for", "reveal"}
	for _, f := range forbidden {
		if _, ok := payload[f]; ok {
			t.Errorf("DIFFUSION LEAK: %q crossed the Membrane", f)
		}
	}
	// Only allowlisted fields may be present.
	allowed := map[string]struct{}{"messages": {}, "temperature": {}, "stream": {}}
	for k := range payload {
		if _, ok := allowed[k]; !ok {
			t.Errorf("DIFFUSION LEAK: non-allowlisted key %q crossed the Membrane", k)
		}
	}
}
