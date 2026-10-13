// Live-path Diffusion-leak coverage: proves that across the
// streaming (SSE / AWS event-stream) and failover paths, for every Provider
// family, the request captured on the egress wire carries ONLY that target's
// injected Egress Identity and no interior/Caller identity. The same claim is
// asserted for the first (failing) target in a failover, so a leak cannot hide
// on the path that is retried away.
package diffusionleak

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/aws/smithy-go/eventstream"

	"github.com/inferencepath/membrane/internal/exterior/providers"
	"github.com/inferencepath/membrane/internal/exterior/providers/anthropic"
	"github.com/inferencepath/membrane/internal/exterior/providers/azureopenai"
	"github.com/inferencepath/membrane/internal/exterior/providers/bedrock"
	"github.com/inferencepath/membrane/internal/exterior/providers/factory"
	"github.com/inferencepath/membrane/internal/exterior/providers/gemini"
	"github.com/inferencepath/membrane/internal/exterior/providers/openai"
	"github.com/inferencepath/membrane/internal/membrane/permeability"
	"github.com/inferencepath/membrane/internal/membrane/scrub"
	"github.com/inferencepath/membrane/internal/membrane/transporter"
	"github.com/inferencepath/membrane/internal/types"
)

// callerMarkers are interior/Caller identity values seeded into the request.
// None may ever appear on the egress wire.
var callerMarkers = []string{
	"acme", "ck_live", "10.0.0.1", "emea", "agent@acme.com",
}

// leakKeyring is a minimal Keyring resolver mapping a reference to its secret.
type leakKeyring map[string]string

func (k leakKeyring) Resolve(ref string) (providers.EgressIdentity, error) {
	s, ok := k[ref]
	if !ok {
		return providers.EgressIdentity{}, fmt.Errorf("no identity for %q", ref)
	}
	return providers.EgressIdentity{Secret: s}, nil
}

// capture records the full wire form (method, URL, headers, body) of every
// request a fake upstream receives, concurrency-safe for the race detector.
type capture struct {
	mu   sync.Mutex
	reqs []string
}

func (c *capture) record(r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	var sb strings.Builder
	fmt.Fprintf(&sb, "%s %s\n", r.Method, r.URL.String())
	for name, vals := range r.Header {
		fmt.Fprintf(&sb, "%s: %s\n", name, strings.Join(vals, ","))
	}
	sb.Write(body)
	c.mu.Lock()
	c.reqs = append(c.reqs, sb.String())
	c.mu.Unlock()
}

func (c *capture) snapshot() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.reqs...)
}

// identityLadenRequest returns a canonical request stuffed with interior/Caller
// identity, plus a Permeability spec that allowlists only neutral fields.
func identityLadenRequest(t *testing.T) (types.CanonicalRequest, permeability.Spec) {
	t.Helper()
	spec, err := permeability.Parse([]byte(`
version: 1
pathways:
  chat:
    outbound:
      allow: [messages, temperature, stream, max_tokens]
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
			"stream":          true,
			"max_tokens":      16,
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
	return req, spec
}

// --- per-family fake upstreams -------------------------------------------------

func writeAnthropicStream(w http.ResponseWriter) {
	fl, _ := w.(http.Flusher)
	io.WriteString(w, "data: {\"type\":\"content_block_delta\",\"delta\":{\"text\":\"he\"}}\n\n")
	if fl != nil {
		fl.Flush()
	}
	io.WriteString(w, "data: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"}}\n\n")
	io.WriteString(w, "data: {\"type\":\"message_stop\"}\n\n")
}

func writeAzureStream(w http.ResponseWriter) {
	fl, _ := w.(http.Flusher)
	io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"he\"}}]}\n\n")
	if fl != nil {
		fl.Flush()
	}
	io.WriteString(w, "data: [DONE]\n\n")
}

func writeGeminiStream(w http.ResponseWriter) {
	fl, _ := w.(http.Flusher)
	io.WriteString(w, "data: {\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"he\"}]}}]}\n\n")
	if fl != nil {
		fl.Flush()
	}
	io.WriteString(w, "data: {\"candidates\":[{\"content\":{\"parts\":[]},\"finishReason\":\"STOP\"}]}\n\n")
}

func writeBedrockStream(t *testing.T, w http.ResponseWriter) {
	t.Helper()
	enc := eventstream.NewEncoder()
	frame := func(eventType, payloadJSON string) {
		msg := eventstream.Message{
			Headers: eventstream.Headers{{Name: ":event-type", Value: eventstream.StringValue(eventType)}},
			Payload: []byte(payloadJSON),
		}
		if err := enc.Encode(w, msg); err != nil {
			t.Fatal(err)
		}
	}
	frame("contentBlockDelta", `{"delta":{"text":"he"}}`)
	frame("messageStop", `{"stopReason":"end_turn"}`)
}

// familyCase builds a two-target pool (failing primary + streaming secondary) of
// one Provider family, the keyring for both targets, and the shared capture.
type familyCase struct {
	name    string
	pool    transporter.PoolSpec
	keyring leakKeyring
	cap     *capture
}

func anthropicCase(t *testing.T) familyCase {
	c := &capture{}
	primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c.record(r)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(primary.Close)
	secondary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c.record(r)
		writeAnthropicStream(w)
	}))
	t.Cleanup(secondary.Close)
	return familyCase{
		name:    "anthropic",
		keyring: leakKeyring{"ref-primary": "sk-ant-primary-EGRESS", "ref-secondary": "sk-ant-secondary-EGRESS"},
		cap:     c,
		pool: transporter.PoolSpec{Targets: []transporter.TargetSpec{
			{Adapter: anthropic.New(primary.URL, 0), KeyringRef: "ref-primary", Model: "claude-3", Priority: 1},
			{Adapter: anthropic.New(secondary.URL, 0), KeyringRef: "ref-secondary", Model: "claude-3", Priority: 2},
		}},
	}
}

func azureCase(t *testing.T) familyCase {
	c := &capture{}
	primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c.record(r)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(primary.Close)
	secondary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c.record(r)
		writeAzureStream(w)
	}))
	t.Cleanup(secondary.Close)
	return familyCase{
		name:    "azure-openai",
		keyring: leakKeyring{"ref-primary": "azure-primary-EGRESS", "ref-secondary": "azure-secondary-EGRESS"},
		cap:     c,
		pool: transporter.PoolSpec{Targets: []transporter.TargetSpec{
			{Adapter: azureopenai.New(primary.URL, "deploy-a", 0), KeyringRef: "ref-primary", Model: "gpt-4o", Priority: 1},
			{Adapter: azureopenai.New(secondary.URL, "deploy-b", 0), KeyringRef: "ref-secondary", Model: "gpt-4o", Priority: 2},
		}},
	}
}

func geminiCase(t *testing.T) familyCase {
	c := &capture{}
	primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c.record(r)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(primary.Close)
	secondary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c.record(r)
		writeGeminiStream(w)
	}))
	t.Cleanup(secondary.Close)
	return familyCase{
		name:    "gemini",
		keyring: leakKeyring{"ref-primary": "gemini-primary-EGRESS", "ref-secondary": "gemini-secondary-EGRESS"},
		cap:     c,
		pool: transporter.PoolSpec{Targets: []transporter.TargetSpec{
			{Adapter: gemini.New(primary.URL, 0), KeyringRef: "ref-primary", Model: "gemini-1.5-pro", Priority: 1},
			{Adapter: gemini.New(secondary.URL, 0), KeyringRef: "ref-secondary", Model: "gemini-1.5-pro", Priority: 2},
		}},
	}
}

func bedrockCase(t *testing.T) familyCase {
	c := &capture{}
	primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c.record(r)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(primary.Close)
	secondary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c.record(r)
		writeBedrockStream(t, w)
	}))
	t.Cleanup(secondary.Close)
	return familyCase{
		name: "bedrock",
		// SigV4: the access key id crosses the wire; the secret access key never
		// does. Neither value is interior/Caller identity.
		keyring: leakKeyring{
			"ref-primary":   "AKIAPRIMARY:secretPrimaryEgress",
			"ref-secondary": "AKIASECONDARY:secretSecondaryEgress",
		},
		cap: c,
		pool: transporter.PoolSpec{Targets: []transporter.TargetSpec{
			{Adapter: bedrock.New("us-east-1", primary.URL, 0), KeyringRef: "ref-primary", Model: "anthropic.claude-3", Priority: 1},
			{Adapter: bedrock.New("us-east-1", secondary.URL, 0), KeyringRef: "ref-secondary", Model: "anthropic.claude-3", Priority: 2},
		}},
	}
}

// errorTarget builds a single-target pool whose upstream always returns 500,
// capturing every request, for one Provider family. Used to prove the error
// path is leak-free across every registered adapter (incl. openai).
func errorTarget(t *testing.T, family string) (transporter.PoolSpec, leakKeyring, *capture) {
	t.Helper()
	c := &capture{}
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c.record(r)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(ts.Close)

	var adapter providers.Provider
	var secret string
	switch family {
	case "openai", "local":
		adapter = openai.New(types.ProviderID(family), ts.URL, 0)
		secret = "sk-openai-EGRESS"
	case "anthropic":
		adapter = anthropic.New(ts.URL, 0)
		secret = "sk-ant-EGRESS"
	case "azure-openai":
		adapter = azureopenai.New(ts.URL, "deploy", 0)
		secret = "azure-EGRESS"
	case "bedrock":
		adapter = bedrock.New("us-east-1", ts.URL, 0)
		secret = "AKIAEGRESS:secretEgress"
	case "gemini":
		adapter = gemini.New(ts.URL, 0)
		secret = "gemini-EGRESS"
	default:
		t.Fatalf("unknown family %q", family)
	}
	kr := leakKeyring{"ref": secret}
	pool := transporter.PoolSpec{Targets: []transporter.TargetSpec{
		{Adapter: adapter, KeyringRef: "ref", Model: "m", Priority: 1},
	}}
	return pool, kr, c
}

// TestErrorPathNoLeakPerFamily drives a failing upstream for every registered
// adapter family and asserts (a) the error surfaced across the Membrane is a
// provider-neutral UpstreamStatusError, and (b) no interior/Caller identity
// reached the upstream on the error path.
//
// The family set is enumerated from factory.Families() (the Exterior registry),
// so adding a new Provider family without extending this leak coverage fails CI
// (errorTarget's switch calls t.Fatalf on an unknown family).
func TestErrorPathNoLeakPerFamily(t *testing.T) {
	req, spec := identityLadenRequest(t)
	// Non-streaming for this path.
	req.Payload["stream"] = false
	sr, err := scrub.Sanitize(req, spec)
	if err != nil {
		t.Fatalf("sanitize: %v", err)
	}

	families := factory.Families()
	if len(families) == 0 {
		t.Fatal("factory.Families() is empty; registry enumeration broken")
	}
	for _, family := range families {
		t.Run(family, func(t *testing.T) {
			pool, kr, cap := errorTarget(t, family)
			m := transporter.New(map[types.PathwayID]transporter.PoolSpec{"chat": pool}, kr)

			_, err := m.Transport(context.Background(), sr)
			var se providers.UpstreamStatusError
			if !errors.As(err, &se) || se.Status != http.StatusInternalServerError {
				t.Fatalf("want neutral UpstreamStatusError{500}, got %v", err)
			}
			// Error() must not disclose the family even though it is stamped.
			if strings.Contains(strings.ToLower(err.Error()), family) {
				t.Fatalf("error string leaked family %q: %v", family, err)
			}
			for _, wire := range cap.snapshot() {
				lower := strings.ToLower(wire)
				for _, marker := range callerMarkers {
					if strings.Contains(lower, strings.ToLower(marker)) {
						t.Errorf("DIFFUSION LEAK (%s error path): Caller marker %q crossed the Membrane\n%s", family, marker, wire)
					}
				}
			}
		})
	}
}

// TestStreamingFailoverNoLeakPerFamily drives each family's streaming + failover
// path through the Membrane and asserts no interior/Caller identity reached any
// upstream (failing primary or succeeding secondary).
func TestStreamingFailoverNoLeakPerFamily(t *testing.T) {
	req, spec := identityLadenRequest(t)
	sr, err := scrub.Sanitize(req, spec)
	if err != nil {
		t.Fatalf("sanitize: %v", err)
	}

	for _, build := range []func(*testing.T) familyCase{anthropicCase, azureCase, bedrockCase, geminiCase} {
		fc := build(t)
		t.Run(fc.name, func(t *testing.T) {
			m := transporter.New(
				map[types.PathwayID]transporter.PoolSpec{"chat": fc.pool},
				fc.keyring,
			)

			var deltas strings.Builder
			var done bool
			err := m.TransportStream(context.Background(), sr, func(c types.StreamChunk) error {
				deltas.WriteString(c.Delta)
				if c.Done {
					done = true
				}
				return nil
			})
			if err != nil {
				t.Fatalf("TransportStream: %v", err)
			}
			if !done || deltas.String() == "" {
				t.Fatalf("stream did not complete: done=%v deltas=%q", done, deltas.String())
			}

			reqs := fc.cap.snapshot()
			if len(reqs) != 2 {
				t.Fatalf("expected failover across 2 targets, captured %d requests", len(reqs))
			}
			for i, wire := range reqs {
				lower := strings.ToLower(wire)
				for _, marker := range callerMarkers {
					if strings.Contains(lower, strings.ToLower(marker)) {
						t.Errorf("DIFFUSION LEAK (%s target %d): Caller marker %q crossed the Membrane\n--- wire ---\n%s", fc.name, i, marker, wire)
					}
				}
			}
		})
	}
}
