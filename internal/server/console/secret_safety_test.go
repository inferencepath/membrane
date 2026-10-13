package console_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/inferencepath/membrane/internal/config"
	"github.com/inferencepath/membrane/internal/consoleaudit"
	"github.com/inferencepath/membrane/internal/consolestore"
	"github.com/inferencepath/membrane/internal/observability/metrics"
	"github.com/inferencepath/membrane/internal/secrets"
	"github.com/inferencepath/membrane/internal/server/console"
	"github.com/inferencepath/membrane/internal/store"
)

// Canary values that MUST never appear in any console response or log: a
// resolved credential value, the resolved admin token, and a Caller Key's
// plaintext secret (only its hash is ever stored on disk).
const (
	canarySecretValue = "sk-LEAK-CANARY-9f3a2b"
	canaryAdminToken  = "admintok-CANARY-7b2c4d"
	canaryCallerPlain = "caller-plaintext-CANARY-e5f6"
)

// TestConsoleNeverLeaksSecrets wires the real app-layer adapter (consolestore)
// over a managed store that references secrets by scheme, then scans every read
// response and the logs for the canaries. This is the console's secret-safety
// gate: credentials are reference-only, Caller secrets exist only as hashes, and
// the admin token is never echoed.
func TestConsoleNeverLeaksSecrets(t *testing.T) {
	dir := t.TempDir()
	callerHash := hex.EncodeToString(func() []byte { s := sha256.Sum256([]byte(canaryCallerPlain)); return s[:] }())
	auditPath := filepath.Join(dir, "audit.jsonl")

	st, err := store.Open(filepath.Join(dir, store.DBFileName))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := st.DB().Exec(q, args...); err != nil {
			t.Fatalf("seed exec %q: %v", q, err)
		}
	}
	exec(`INSERT INTO pathways (name, cascade_allowed, default_provider) VALUES ('chat', 0, 'openai-main')`)
	exec(`INSERT INTO providers (name, family, endpoint, models, default_model, secret_ref)
	      VALUES ('openai-main', 'openai', 'https://api.openai.com/v1', 'gpt-4o-mini', 'gpt-4o-mini', 'env:CONSOLE_LEAK_ENV')`)
	exec(`INSERT INTO pathway_providers (pathway, provider) VALUES ('chat', 'openai-main')`)
	// An advanced raw credential referencing the same env canary, so the
	// Keep one credentials row in store to ensure hidden endpoints never surface it.
	exec(`INSERT INTO credentials (name, secret_ref) VALUES ('legacy', 'env:CONSOLE_LEAK_ENV')`)
	exec(`INSERT INTO callers (key_id, caller_id, name, secret_hash) VALUES ('ck_demo', 'acme-app', 'Acme Demo App', ?)`, callerHash)
	exec(`INSERT INTO caller_allow (key_id, pathway) VALUES ('ck_demo', 'chat')`)
	exec(`INSERT INTO permeability (pathway, outbound_allow, inbound_default)
	      VALUES ('chat', 'messages` + "\n" + `temperature', 'message` + "\n" + `finish_reason` + "\n" + `usage')`)

	writeFile(t, auditPath, `{"schema_version":1,"timestamp":"2026-10-08T10:00:00Z","caller_id":"acme-app","pathway":"chat","outcome":"success","total_tokens":7}
`)

	// Secrets resolve to the canaries, so the console *could* leak them if it ever
	// dereferenced a value.
	t.Setenv("CONSOLE_LEAK_ENV", canarySecretValue)

	cfg := config.Config{
		Server:  config.ServerConfig{Address: ":8080", ShutdownTimeout: 30 * time.Second},
		DataDir: dir,
		Audit:   config.AuditConfig{Sink: "jsonlines", Path: auditPath},
		Console: config.ConsoleConfig{Enabled: true, Address: "127.0.0.1:7474", AuthTokenRef: "env:CONSOLE_ADMIN_ENV"},
	}

	var logbuf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logbuf, nil))

	adapter := consolestore.New(st, cfg, func() consolestore.ReloadOutcome {
		return consolestore.ReloadOutcome{Applied: true}
	})

	reg := metrics.NewRegistry()
	reg.RecordChat("chat", false, metrics.OutcomeSuccess, 20*time.Millisecond, 3, 4, 7)
	reg.ObserveAuthorization("chat", true)

	h := console.New(console.Config{
		Status:    stubStatus{ready: true},
		Metrics:   reg,
		Config:    adapter,
		Editor:    adapter,
		Audit:     consoleaudit.New(auditPath),
		AuthToken: canaryAdminToken,
		Logger:    logger,
	}).Handler()

	endpoints := []string{
		"/api/console/status",
		"/api/console/health",
		"/api/console/metrics",
		"/api/console/config/server",
		"/api/console/config/pathways",
		"/api/console/config/callers",
		"/api/console/config/permeability",
		"/api/console/edit/pathways",
		"/api/console/edit/callers",
		"/api/console/edit/permeability",
		"/api/console/audit/usage",
		"/api/console/audit/admin",
	}

	canaries := []string{canarySecretValue, canaryAdminToken, canaryCallerPlain}
	for _, path := range endpoints {
		rec := get(t, h, path, "127.0.0.1:1", nil)
		if rec.Code != 200 {
			t.Fatalf("%s = %d, want 200", path, rec.Code)
		}
		body := rec.Body.String()
		for _, canary := range canaries {
			if strings.Contains(body, canary) {
				t.Fatalf("SECRET LEAK: %s response contains canary %q:\n%s", path, canary, body)
			}
		}
	}

	for _, path := range []string{
		"/api/console/config/credentials",
		"/api/console/edit/credentials",
	} {
		rec := get(t, h, path, "127.0.0.1:1", nil)
		if rec.Code != http.StatusNotFound {
			t.Fatalf("%s = %d, want 404", path, rec.Code)
		}
	}

	// Logs must not leak any canary either.
	for _, canary := range canaries {
		if strings.Contains(logbuf.String(), canary) {
			t.Fatalf("SECRET LEAK in logs: canary %q present", canary)
		}
	}
}

func writeFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// TestConsoleSecretsEndpointsNeverLeakValue wires the LIVE sealed secret store
// (master key + SecretAdapter), stores a value equal to the canary, then drives
// the secrets endpoints (set + list) and asserts the plaintext value never
// appears in any response or in the logs. Values are write-only from the console.
func TestConsoleSecretsEndpointsNeverLeakValue(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, store.DBFileName))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	keeper, err := secrets.NewKeeper([]byte("a-sufficiently-long-master-key!!"), 1)
	if err != nil {
		t.Fatalf("keeper: %v", err)
	}
	mgr := secrets.NewManager(st, keeper)
	// Pre-store one secret whose value equals the canary.
	if err := mgr.Set("openai-main", canarySecretValue); err != nil {
		t.Fatalf("set secret: %v", err)
	}

	var logbuf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logbuf, nil))
	h := console.New(console.Config{
		Status:    stubStatus{ready: true},
		Metrics:   metrics.NewRegistry(),
		Secrets:   consolestore.NewSecretAdapter(st, mgr),
		AuthToken: canaryAdminToken,
		Logger:    logger,
	}).Handler()

	// List: metadata only, never the value.
	listBody := get(t, h, "/api/console/secrets", "127.0.0.1:1", nil).Body.String()
	if !strings.Contains(listBody, "openai-main") {
		t.Fatalf("list should show the secret name, got: %s", listBody)
	}
	if strings.Contains(listBody, canarySecretValue) {
		t.Fatalf("SECRET LEAK: list response contains the sealed value:\n%s", listBody)
	}

	// Set (rotate) with a fresh value equal to the canary: accepted, never echoed.
	setReq := httptest.NewRequest(http.MethodPut, "/api/console/secrets/openai-main",
		strings.NewReader(`{"value":"`+canarySecretValue+`"}`))
	setReq.RemoteAddr = "127.0.0.1:1"
	setRec := httptest.NewRecorder()
	h.ServeHTTP(setRec, setReq)
	if setRec.Code != http.StatusOK {
		t.Fatalf("set = %d, want 200", setRec.Code)
	}
	if strings.Contains(setRec.Body.String(), canarySecretValue) {
		t.Fatalf("SECRET LEAK: set response contains the value:\n%s", setRec.Body.String())
	}

	// Logs (incl. admin-action audit) must not carry the value either.
	if strings.Contains(logbuf.String(), canarySecretValue) {
		t.Fatalf("SECRET LEAK in logs: canary present")
	}
}

// TestConsoleProviderEndpointsNeverLeakKey wires the LIVE provider port (sealed
// store + ProviderAdapter) and drives the provider endpoints with a key equal to
// the canary, asserting the key never appears in any response or log. The atomic
// Set seals the key and writes the secret:<name> credential together.
func TestConsoleProviderEndpointsNeverLeakKey(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, store.DBFileName))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	keeper, err := secrets.NewKeeper([]byte("a-sufficiently-long-master-key!!"), 1)
	if err != nil {
		t.Fatalf("keeper: %v", err)
	}
	mgr := secrets.NewManager(st, keeper)

	var logbuf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logbuf, nil))
	h := console.New(console.Config{
		Status:    stubStatus{ready: true},
		Metrics:   metrics.NewRegistry(),
		Providers: consolestore.NewProviderAdapter(st, mgr),
		AuthToken: canaryAdminToken,
		Logger:    logger,
	}).Handler()

	// Create the provider with the canary key (atomic seal + credential).
	setReq := httptest.NewRequest(http.MethodPost, "/api/console/providers",
		strings.NewReader(`{"name":"openai-main","family":"openai","models":["gpt-4o"],"defaultModel":"gpt-4o","endpoint":"https://api.openai.com/v1","value":"`+canarySecretValue+`"}`))
	setReq.RemoteAddr = "127.0.0.1:1"
	setRec := httptest.NewRecorder()
	h.ServeHTTP(setRec, setReq)
	if setRec.Code != http.StatusOK {
		t.Fatalf("provider set = %d, want 200", setRec.Code)
	}
	if strings.Contains(setRec.Body.String(), canarySecretValue) {
		t.Fatalf("KEY LEAK: provider set response contains the key:\n%s", setRec.Body.String())
	}

	// List: references + status only, never the key.
	listBody := get(t, h, "/api/console/providers", "127.0.0.1:1", nil).Body.String()
	if !strings.Contains(listBody, "openai-main") || !strings.Contains(listBody, "secret:openai-main") {
		t.Fatalf("provider list missing expected references: %s", listBody)
	}
	if strings.Contains(listBody, canarySecretValue) {
		t.Fatalf("KEY LEAK: provider list response contains the key:\n%s", listBody)
	}

	// The sealed key must round-trip through the store (proving it was persisted),
	// but only via the keeper — never any console surface.
	if got, _ := mgr.Get("openai-main"); got != canarySecretValue {
		t.Fatalf("sealed key did not persist via provider set")
	}
	if strings.Contains(logbuf.String(), canarySecretValue) {
		t.Fatalf("KEY LEAK in logs: canary present")
	}
}
