// Log-scan coverage: on the rotation and failover paths, no Egress Identity
// secret (before or after rotation) and no Caller identity or Caller Key may
// appear in the Membrane's logs.
package diffusionleak

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/inferencepath/membrane/internal/exterior/keyring"
	"github.com/inferencepath/membrane/internal/exterior/providers/anthropic"
	"github.com/inferencepath/membrane/internal/interior/admit"
	"github.com/inferencepath/membrane/internal/interior/resolve"
	"github.com/inferencepath/membrane/internal/membrane/permeability"
	"github.com/inferencepath/membrane/internal/membrane/transporter"
	"github.com/inferencepath/membrane/internal/observability/metrics"
	"github.com/inferencepath/membrane/internal/server/chat"
	"github.com/inferencepath/membrane/internal/types"
)

// callersYAML stores only the hash of the Caller Key secret "s3cr3t-demo-key".
const logscanCallersYAML = `callers:
  - key_id: ck_demo
    caller_id: acme-app
    hash: 458d6534a760ed4c8c2f1c8f86a517572cb56fa6a0f8554efdda2cb8532a4816
`

const logscanCallerKey = "ck_demo.s3cr3t-demo-key"

const logscanPermeability = `version: 1
pathways:
  chat:
    outbound:
      allow: [messages, temperature, max_tokens, stream]
    inbound:
      default: [message, finish_reason, usage]
      on_reveal: [provider, model]
`

// envResolver resolves "env"-style refs from an in-memory, mutable table so the
// test can rotate a secret value and call Keyring.Refresh deterministically.
type envResolver struct{ vals map[string]string }

func (e *envResolver) resolve(ref string) (string, error) {
	if v, ok := e.vals[ref]; ok {
		return v, nil
	}
	return "", io.EOF
}

func TestLogsCleanAcrossRotationAndFailover(t *testing.T) {
	const (
		primarySecret   = "sk-ant-PRIMARY-secret-aaa"
		secondaryV1     = "sk-ant-SECONDARY-secret-bbb"
		secondaryV2     = "sk-ant-SECONDARY-rotated-ccc"
		callerID        = "acme-app"
		callerSecret    = "s3cr3t-demo-key"
		revealedContent = "hello from secondary"
	)

	// Primary always fails (forces failover); secondary succeeds.
	primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer primary.Close()
	var secondaryAuth string
	secondary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		secondaryAuth = r.Header.Get("x-api-key")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"model":       "claude-3",
			"stop_reason": "end_turn",
			"content":     []map[string]any{{"type": "text", "text": revealedContent}},
			"usage":       map[string]int{"input_tokens": 1, "output_tokens": 2},
		})
	}))
	defer secondary.Close()

	er := &envResolver{vals: map[string]string{
		"env:PRIMARY":   primarySecret,
		"env:SECONDARY": secondaryV1,
	}}
	kr, err := keyring.New(
		[]keyring.Entry{
			{Name: "primary", SecretRef: "env:PRIMARY"},
			{Name: "secondary", SecretRef: "env:SECONDARY"},
		},
		keyring.WithRefResolver(er.resolve),
	)
	if err != nil {
		t.Fatalf("keyring: %v", err)
	}

	spec, err := permeability.Parse([]byte(logscanPermeability))
	if err != nil {
		t.Fatal(err)
	}
	store, err := admit.ParseStore([]byte(logscanCallersYAML))
	if err != nil {
		t.Fatal(err)
	}
	pools := map[types.PathwayID]transporter.PoolSpec{"chat": {
		Strategy: transporter.StrategyPriority,
		Targets: []transporter.TargetSpec{
			{Adapter: anthropic.New(primary.URL, 0), KeyringRef: "primary", Model: "claude-3", Priority: 0},
			{Adapter: anthropic.New(secondary.URL, 0), KeyringRef: "secondary", Model: "claude-3", Priority: 1},
		},
	}}
	trans := transporter.New(pools, kr)
	resolver := resolve.New(map[types.PathwayID]resolve.Binding{"chat": {}})

	var logs bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	h := chat.New(spec, store, resolver, trans, logger, metrics.Nop{})
	ts := httptest.NewServer(h)
	defer ts.Close()

	body := `{"pathway":"chat","messages":[{"role":"user","content":"hi"}]}`
	doPost := func() {
		t.Helper()
		req, err := http.NewRequest(http.MethodPost, ts.URL+chat.Route, strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer "+logscanCallerKey)
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
	}

	// Request 1: fails over from primary to secondary (secondary uses the v1 secret).
	doPost()
	if secondaryAuth != secondaryV1 {
		t.Fatalf("secondary received %q, want pre-rotation secret", secondaryAuth)
	}

	// Rotate the secondary secret value and refresh the Keyring.
	er.vals["env:SECONDARY"] = secondaryV2
	if err := kr.Refresh(); err != nil {
		t.Fatalf("rotation refresh: %v", err)
	}

	// Request 2: post-rotation; secondary must now authenticate with v2.
	doPost()
	if secondaryAuth != secondaryV2 {
		t.Fatalf("secondary received %q, want post-rotation secret", secondaryAuth)
	}

	// The egress side (logs) must be clean of every secret and Caller identity.
	out := logs.String()
	forbidden := []string{
		primarySecret, secondaryV1, secondaryV2, // Egress Identity secrets (pre+post rotation)
		callerSecret, logscanCallerKey, // Caller Key
		"s3cr3t",            // Caller secret fragment
		callerID, "ck_demo", // Caller identity / Key id
	}
	for _, f := range forbidden {
		if strings.Contains(out, f) {
			t.Fatalf("LOG LEAK: %q appeared in Membrane logs:\n%s", f, out)
		}
	}
}
