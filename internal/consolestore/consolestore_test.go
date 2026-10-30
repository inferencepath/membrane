package consolestore_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/inferencepath/membrane/internal/config"
	"github.com/inferencepath/membrane/internal/consolestore"
	"github.com/inferencepath/membrane/internal/interior/admit"
	"github.com/inferencepath/membrane/internal/server/console"
	"github.com/inferencepath/membrane/internal/store"
)

// demoHash is a valid 64-hex (sha256) placeholder caller secret hash.
const demoHash = "458d6534a760ed4c8c2f1c8f86a517572cb56fa6a0f8554efdda2cb8532a4816"

// TestOperationsCatalog checks that chat is unrestricted and every other
// operation has a route and a non-empty field list.
func TestOperationsCatalog(t *testing.T) {
	a := consolestore.New(newSeeded(t), testCfg(), recordReload(consolestore.ReloadOutcome{Applied: true}, new(int)))
	view := a.Operations()
	if len(view.Operations) != 8 {
		t.Fatalf("operations = %d, want 8 (closed enum)", len(view.Operations))
	}
	byName := map[string]console.OperationView{}
	for _, op := range view.Operations {
		if op.Route == "" {
			t.Errorf("operation %q has empty route", op.Operation)
		}
		byName[op.Operation] = op
	}
	chat, ok := byName["chat"]
	if !ok || chat.Restricted || chat.Route != "/v1/chat" {
		t.Errorf("chat view wrong: %+v", chat)
	}
	if len(chat.OutboundAllow) != 0 || len(chat.InboundFields) != 0 {
		t.Errorf("chat must be unrestricted (no palette): %+v", chat)
	}
	emb, ok := byName["embeddings"]
	if !ok || !emb.Restricted || emb.Route != "/v1/embeddings" {
		t.Errorf("embeddings view wrong: %+v", emb)
	}
	if len(emb.OutboundAllow) == 0 || len(emb.InboundFields) == 0 {
		t.Errorf("embeddings must carry a field palette: %+v", emb)
	}
	// Field lists are sorted.
	for i := 1; i < len(emb.OutboundAllow); i++ {
		if emb.OutboundAllow[i-1] > emb.OutboundAllow[i] {
			t.Errorf("outbound palette not sorted: %v", emb.OutboundAllow)
		}
	}
}

// newSeeded opens a temp store holding a valid single-pathway config.
func newSeeded(t *testing.T) *store.Store {
	t.Helper()
	s, err := store.Open(filepath.Join(t.TempDir(), store.DBFileName))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := s.DB().Exec(q, args...); err != nil {
			t.Fatalf("exec %q: %v", q, err)
		}
	}
	exec(`INSERT INTO pathways (name, cascade_allowed, default_provider) VALUES ('chat', 0, 'main')`)
	exec(`INSERT INTO providers (name, family, endpoint, models, default_model, secret_ref)
	      VALUES ('main', 'openai', 'https://api.openai.com', 'gpt-4o-mini', 'gpt-4o-mini', 'env:TEST_STORE_KEY')`)
	exec(`INSERT INTO pathway_providers (pathway, provider) VALUES ('chat', 'main')`)
	// A standalone credential for the Credentials view tests.
	exec(`INSERT INTO credentials (name, secret_ref) VALUES ('legacy', 'env:TEST_STORE_KEY')`)
	exec(`INSERT INTO callers (key_id, caller_id, name, secret_hash) VALUES ('ck_demo', 'acme', 'demo', ?)`, demoHash)
	exec(`INSERT INTO caller_allow (key_id, pathway) VALUES ('ck_demo', 'chat')`)
	exec(`INSERT INTO permeability (pathway, outbound_allow, inbound_default, inbound_on_reveal)
	      VALUES ('chat', 'messages', 'message` + "\n" + `finish_reason', 'provider')`)
	return s
}

func writeFile(t *testing.T, path, content string) error {
	t.Helper()
	return os.WriteFile(path, []byte(content), 0o600)
}

func testCfg() config.Config {
	var cfg config.Config
	cfg.Server.Address = ":8080"
	cfg.Server.ShutdownTimeout = 10 * time.Second
	cfg.Audit.Sink = "jsonlines"
	cfg.Audit.Path = "/var/log/membrane/audit.jsonl"
	cfg.Console.Enabled = true
	cfg.Console.Address = ":8300"
	cfg.Console.AuthTokenRef = "env:MEMBRANE_CONSOLE_TOKEN"
	cfg.DataDir = "/data"
	return cfg
}

// recordReload returns a Reload that records invocations and yields outcome.
func recordReload(outcome consolestore.ReloadOutcome, calls *int) consolestore.Reload {
	return func() consolestore.ReloadOutcome {
		*calls++
		return outcome
	}
}

func TestServerViewAnnotatesManagedMode(t *testing.T) {
	a := consolestore.New(newSeeded(t), testCfg(), recordReload(consolestore.ReloadOutcome{Applied: true}, new(int)))
	v := a.Server()
	if v.Mode != "managed" || v.DataDir != "/data" || !v.EditingEnabled {
		t.Fatalf("server view = %+v", v)
	}
	if v.Address != ":8080" || !v.ConsoleAuth || v.AuditSink != "jsonlines" {
		t.Fatalf("server view fields = %+v", v)
	}
}

func TestReadViews(t *testing.T) {
	a := consolestore.New(newSeeded(t), testCfg(), recordReload(consolestore.ReloadOutcome{Applied: true}, new(int)))

	caps, err := a.Pathways()
	if err != nil || len(caps.Pathways) != 1 || caps.Pathways[0].Pathway != "chat" {
		t.Fatalf("pathways = %+v (err %v)", caps, err)
	}
	if len(caps.Pathways[0].Providers) != 1 || caps.Pathways[0].Providers[0] != "main" {
		t.Fatalf("providers = %+v", caps.Pathways[0].Providers)
	}

	creds, err := a.Credentials()
	if err != nil || len(creds.Credentials) != 1 {
		t.Fatalf("credentials = %+v (err %v)", creds, err)
	}
	if creds.Credentials[0].Scheme != "env" || creds.Credentials[0].Reference != "env:TEST_STORE_KEY" {
		t.Fatalf("credential = %+v", creds.Credentials[0])
	}

	callers, err := a.Callers()
	if err != nil || len(callers.Callers) != 1 || callers.Callers[0].KeyID != "ck_demo" {
		t.Fatalf("callers = %+v (err %v)", callers, err)
	}

	perm, err := a.Permeability()
	if err != nil || len(perm.Pathways) != 1 || perm.Pathways[0].Pathway != "chat" {
		t.Fatalf("permeability = %+v (err %v)", perm, err)
	}
	// The outbound denylist is included for display.
	if len(perm.OutboundDenylist) == 0 {
		t.Fatal("expected outbound denylist to be surfaced")
	}
	found := false
	for _, f := range perm.OutboundDenylist {
		if f == "authorization" {
			found = true
		}
	}
	if !found {
		t.Fatalf("denylist should include authorization: %v", perm.OutboundDenylist)
	}
}

func TestCredentialResolveReflectsEnv(t *testing.T) {
	a := consolestore.New(newSeeded(t), testCfg(), recordReload(consolestore.ReloadOutcome{Applied: true}, new(int)))
	// An unset variable does not resolve.
	creds, _ := a.Credentials()
	if creds.Credentials[0].Resolves {
		t.Fatalf("expected unresolved without env var")
	}
	t.Setenv("TEST_STORE_KEY", "sk-live")
	creds, _ = a.Credentials()
	if !creds.Credentials[0].Resolves {
		t.Fatalf("expected resolved with env var set")
	}
}

func TestReadAndValidateRaw(t *testing.T) {
	a := consolestore.New(newSeeded(t), testCfg(), recordReload(consolestore.ReloadOutcome{Applied: true}, new(int)))
	ef, err := a.Read(store.KindPathways)
	if err != nil || ef.Version == "" || !strings.Contains(ef.Raw, "chat") {
		t.Fatalf("read = %+v (err %v)", ef, err)
	}
	if res := a.Validate(store.KindPathways, []byte(ef.Raw)); !res.Valid {
		t.Fatalf("validate valid doc = %+v", res)
	}
	if res := a.Validate(store.KindPathways, []byte("pathways: [ : ]")); res.Valid {
		t.Fatalf("expected invalid for malformed doc")
	}
	if res := a.Validate("nope", []byte("x")); res.Valid {
		t.Fatalf("expected invalid for unknown kind")
	}
}

func TestApplyRawWritesAndReloads(t *testing.T) {
	calls := 0
	a := consolestore.New(newSeeded(t), testCfg(), recordReload(consolestore.ReloadOutcome{Applied: true}, &calls))

	doc := "version: 1\npathways:\n  - pathway: chat\n    providers: [main]\n  - pathway: embed\n    providers: [main]\n"
	res := a.Apply(store.KindPathways, []byte(doc), "")
	if !res.Applied || calls != 1 {
		t.Fatalf("apply = %+v calls=%d", res, calls)
	}
	// The new embed pathway was stored.
	ef, _ := a.Read(store.KindPathways)
	if !strings.Contains(ef.Raw, "embed") {
		t.Fatalf("expected persisted pathway, got %s", ef.Raw)
	}
}

func TestApplyNoOpUnchanged(t *testing.T) {
	calls := 0
	a := consolestore.New(newSeeded(t), testCfg(), recordReload(consolestore.ReloadOutcome{Applied: true}, &calls))
	ef, _ := a.Read(store.KindPermeability)
	res := a.Apply(store.KindPermeability, []byte(ef.Raw), "")
	if !res.Unchanged || calls != 0 {
		t.Fatalf("expected no-op unchanged with no reload, got %+v calls=%d", res, calls)
	}
}

func TestApplyVersionConflict(t *testing.T) {
	a := consolestore.New(newSeeded(t), testCfg(), recordReload(consolestore.ReloadOutcome{Applied: true}, new(int)))
	doc := "version: 1\npathways:\n  - pathway: chat\n    providers: [main]\n"
	res := a.Apply(store.KindPathways, []byte(doc), "stale-version")
	if !res.Rejected || !strings.Contains(res.Reason, "changed") {
		t.Fatalf("expected version conflict rejection, got %+v", res)
	}
}

func TestApplyInvalidRejected(t *testing.T) {
	a := consolestore.New(newSeeded(t), testCfg(), recordReload(consolestore.ReloadOutcome{Applied: true}, new(int)))
	res := a.Apply(store.KindPathways, []byte("pathways: [ : ]"), "")
	if !res.Rejected {
		t.Fatalf("expected rejection for invalid doc, got %+v", res)
	}
}

func TestApplyFormPathways(t *testing.T) {
	calls := 0
	a := consolestore.New(newSeeded(t), testCfg(), recordReload(consolestore.ReloadOutcome{Applied: true}, &calls))
	form := `{"pathways":[{"pathway":"chat","cascadeAllowed":true,"defaultProvider":"main","providers":["main"]}]}`
	if res := a.ValidateForm(store.KindPathways, []byte(form)); !res.Valid {
		t.Fatalf("validate form = %+v", res)
	}
	res := a.ApplyForm(store.KindPathways, []byte(form), "")
	if !res.Applied || calls != 1 {
		t.Fatalf("apply form = %+v calls=%d", res, calls)
	}
	caps, _ := a.Pathways()
	if len(caps.Pathways[0].Providers) != 1 || !caps.Pathways[0].CascadeAllowed {
		t.Fatalf("expected 1 provider with cascade, got %+v", caps.Pathways[0])
	}
}

func TestApplyFormCallerAllowOnly(t *testing.T) {
	calls := 0
	a := consolestore.New(newSeeded(t), testCfg(), recordReload(consolestore.ReloadOutcome{Applied: true}, &calls))
	// Add a second pathway so the caller can be allowed for it.
	addCaps := `{"pathways":[{"pathway":"chat","providers":["main"]},{"pathway":"embed","providers":["main"]}]}`
	if res := a.ApplyForm(store.KindPathways, []byte(addCaps), ""); !res.Applied {
		t.Fatalf("seed caps = %+v", res)
	}

	form := `{"callers":[{"keyId":"ck_demo","allow":["chat","embed"]}]}`
	res := a.ApplyForm(store.KindCallers, []byte(form), "")
	if !res.Applied {
		t.Fatalf("apply caller allow = %+v", res)
	}
	callers, _ := a.Callers()
	if len(callers.Callers[0].Allow) != 2 {
		t.Fatalf("expected 2 allows, got %+v", callers.Callers[0].Allow)
	}
	// The rendered document must still carry the hash.
	ef, _ := a.Read(store.KindCallers)
	if !strings.Contains(ef.Raw, demoHash) {
		t.Fatalf("caller hash must be preserved: %s", ef.Raw)
	}
}

func TestApplyFormCallerUnknownKey(t *testing.T) {
	a := consolestore.New(newSeeded(t), testCfg(), recordReload(consolestore.ReloadOutcome{Applied: true}, new(int)))
	form := `{"callers":[{"keyId":"ck_nope","allow":["chat"]}]}`
	res := a.ApplyForm(store.KindCallers, []byte(form), "")
	if !res.Rejected || !strings.Contains(res.Reason, "unknown key_id") {
		t.Fatalf("expected unknown key_id rejection, got %+v", res)
	}
}

func TestApplyFormCredentialsAndPermeability(t *testing.T) {
	a := consolestore.New(newSeeded(t), testCfg(), recordReload(consolestore.ReloadOutcome{Applied: true}, new(int)))

	credForm := `{"credentials":[{"name":"main","reference":"env:TEST_STORE_KEY"},{"name":"backup","reference":"file:/etc/key"}]}`
	if res := a.ApplyForm(store.KindCredentials, []byte(credForm), ""); !res.Applied {
		t.Fatalf("apply credentials = %+v", res)
	}
	creds, _ := a.Credentials()
	if len(creds.Credentials) != 2 {
		t.Fatalf("expected 2 credentials, got %+v", creds.Credentials)
	}

	permForm := `{"pathways":[{"pathway":"chat","outboundAllow":["messages"],"inboundDefault":["message","usage"],"inboundOnReveal":["provider"]}]}`
	if res := a.ApplyForm(store.KindPermeability, []byte(permForm), ""); !res.Applied {
		t.Fatalf("apply permeability = %+v", res)
	}
	perm, _ := a.Permeability()
	if len(perm.Pathways[0].InboundDefault) != 2 {
		t.Fatalf("expected 2 inbound defaults, got %+v", perm.Pathways[0].InboundDefault)
	}
}

func TestCreatePathwayAtomic(t *testing.T) {
	calls := 0
	a := consolestore.New(newSeeded(t), testCfg(), recordReload(consolestore.ReloadOutcome{Applied: true}, &calls))

	res := a.CreatePathway(console.PathwayWizardInput{
		Pathway:         "reason",
		Providers:       []string{"main"},
		DefaultProvider: "main",
		CascadeAllowed:  true,
		OutboundAllow:   []string{"messages", "temperature"},
		InboundDefault:  []string{"message", "usage"},
		InboundOnReveal: []string{"provider", "model"},
	})
	if !res.Applied || calls != 1 {
		t.Fatalf("CreatePathway = %+v calls=%d", res, calls)
	}

	// Both the pathway and its permeability policy must exist for "reason".
	paths, _ := a.Pathways()
	var foundPath bool
	for _, p := range paths.Pathways {
		if p.Pathway == "reason" && p.CascadeAllowed && p.DefaultProvider == "main" {
			foundPath = true
		}
	}
	if !foundPath {
		t.Fatalf("pathway 'reason' not created: %+v", paths.Pathways)
	}
	perm, _ := a.Permeability()
	var foundPerm bool
	for _, d := range perm.Pathways {
		if d.Pathway == "reason" && len(d.OutboundAllow) == 2 {
			foundPerm = true
		}
	}
	if !foundPerm {
		t.Fatalf("permeability for 'reason' not created: %+v", perm.Pathways)
	}
	// The existing pathway must be kept.
	if len(paths.Pathways) != 2 {
		t.Fatalf("expected 2 pathways after create, got %+v", paths.Pathways)
	}
}

func TestCreatePathwayWithOperation(t *testing.T) {
	calls := 0
	a := consolestore.New(newSeeded(t), testCfg(), recordReload(consolestore.ReloadOutcome{Applied: true}, &calls))

	// An embeddings pathway on the openai provider, with an embeddings
	// permeability policy, must apply.
	res := a.CreatePathway(console.PathwayWizardInput{
		Pathway:         "embed",
		Operation:       "embeddings",
		Providers:       []string{"main"},
		DefaultProvider: "main",
		OutboundAllow:   []string{"input", "dimensions"},
		InboundDefault:  []string{"data", "usage"},
		InboundOnReveal: []string{"provider", "model"},
	})
	if !res.Applied || calls != 1 {
		t.Fatalf("CreatePathway(embeddings) = %+v calls=%d", res, calls)
	}

	// The operation must be stored. Vocabulary checks happen at Snapshot build.
	paths, _ := a.Pathways()
	var found bool
	for _, p := range paths.Pathways {
		if p.Pathway == "embed" {
			found = true
			if p.Operation != "embeddings" {
				t.Fatalf("embed operation = %q, want embeddings", p.Operation)
			}
		}
	}
	if !found {
		t.Fatalf("pathway 'embed' not created: %+v", paths.Pathways)
	}
}

func TestCreatePathwayUpsertReplaces(t *testing.T) {
	a := consolestore.New(newSeeded(t), testCfg(), recordReload(consolestore.ReloadOutcome{Applied: true}, new(int)))
	// Re-creating "chat" with a different cascade flag updates it in place.
	res := a.CreatePathway(console.PathwayWizardInput{
		Pathway:         "chat",
		Providers:       []string{"main"},
		DefaultProvider: "main",
		CascadeAllowed:  true,
		OutboundAllow:   []string{"messages"},
		InboundDefault:  []string{"message"},
	})
	if !res.Applied {
		t.Fatalf("CreatePathway upsert = %+v", res)
	}
	paths, _ := a.Pathways()
	if len(paths.Pathways) != 1 {
		t.Fatalf("expected 1 pathway after upsert, got %+v", paths.Pathways)
	}
}

func TestCreateCallerHashesAndStores(t *testing.T) {
	st := newSeeded(t)
	a := consolestore.New(st, testCfg(), recordReload(consolestore.ReloadOutcome{Applied: true}, new(int)))

	res := a.CreateCaller(console.CallerInput{
		KeyID:    "ck_acme",
		CallerID: "acme-app",
		Name:     "Acme",
		Allow:    []string{"chat"},
		Secret:   "s3cr3t-demo-key",
	})
	if !res.Applied {
		t.Fatalf("CreateCaller = %+v", res)
	}
	back, _ := a.Callers()
	var found *struct {
		keyID, callerID string
		allow           int
	}
	for _, c := range back.Callers {
		if c.KeyID == "ck_acme" {
			found = &struct {
				keyID, callerID string
				allow           int
			}{c.KeyID, c.CallerID, len(c.Allow)}
		}
	}
	if found == nil || found.callerID != "acme-app" || found.allow != 1 {
		t.Fatalf("caller not created correctly: %+v", back.Callers)
	}
	// The stored hash is the SHA-256 of the secret; the plaintext must not
	// appear in the rendered document.
	raw, _ := st.RenderCallers()
	wantHash := admit.HashSecret("s3cr3t-demo-key")
	if !strings.Contains(string(raw), wantHash) {
		t.Fatalf("stored hash missing; got: %s", raw)
	}
	if strings.Contains(string(raw), "s3cr3t-demo-key") {
		t.Fatalf("plaintext secret leaked into store: %s", raw)
	}
}

func TestDeleteCaller(t *testing.T) {
	a := consolestore.New(newSeeded(t), testCfg(), recordReload(consolestore.ReloadOutcome{Applied: true}, new(int)))
	// Seeded store has ck_demo; add a second so deleting one leaves a valid store.
	if res := a.CreateCaller(console.CallerInput{KeyID: "ck_two", CallerID: "beta", Allow: []string{"chat"}, Secret: "x"}); !res.Applied {
		t.Fatalf("seed second caller = %+v", res)
	}
	removed, err := a.DeleteCaller("ck_two")
	if err != nil || !removed {
		t.Fatalf("DeleteCaller = %v, %v", removed, err)
	}
	// Deleting an absent key is not an error.
	removed, err = a.DeleteCaller("ck_absent")
	if err != nil || removed {
		t.Fatalf("DeleteCaller(absent) = %v, %v", removed, err)
	}
}

func TestApplyFormInvalidJSON(t *testing.T) {
	a := consolestore.New(newSeeded(t), testCfg(), recordReload(consolestore.ReloadOutcome{Applied: true}, new(int)))
	if res := a.ValidateForm(store.KindPathways, []byte("{not json")); res.Valid {
		t.Fatalf("expected invalid json rejected")
	}
	if res := a.ApplyForm(store.KindPathways, []byte("{not json"), ""); !res.Rejected {
		t.Fatalf("expected apply rejection for invalid json, got %+v", res)
	}
}

func TestReloadRejectionSurfaces(t *testing.T) {
	a := consolestore.New(newSeeded(t), testCfg(),
		recordReload(consolestore.ReloadOutcome{Reason: "dangling keyring ref"}, new(int)))
	doc := "version: 1\npathways:\n  - pathway: chat\n    providers: [main]\n"
	res := a.Apply(store.KindPathways, []byte(doc), "")
	if !res.Rejected || res.Reason != "dangling keyring ref" {
		t.Fatalf("expected reload rejection surfaced, got %+v", res)
	}
}

func TestApplyWithMatchingVersion(t *testing.T) {
	calls := 0
	a := consolestore.New(newSeeded(t), testCfg(), recordReload(consolestore.ReloadOutcome{Applied: true}, &calls))
	ef, _ := a.Read(store.KindPathways)
	// Add a second pathway so the applied doc differs from the seeded state.
	doc := "version: 1\npathways:\n  - pathway: chat\n    providers: [main]\n  - pathway: embed\n    providers: [main]\n"
	res := a.Apply(store.KindPathways, []byte(doc), ef.Version)
	if !res.Applied || calls != 1 || res.Version == "" {
		t.Fatalf("apply with matching version = %+v calls=%d", res, calls)
	}
}

func TestApplyRawCallersAllKinds(t *testing.T) {
	a := consolestore.New(newSeeded(t), testCfg(), recordReload(consolestore.ReloadOutcome{Applied: true}, new(int)))

	callers := "version: 1\ncallers:\n  - key_id: ck_demo\n    hash: " + demoHash + "\n    caller_id: acme\n    name: demo\n    allow: [chat]\n  - key_id: ck_two\n    hash: " + demoHash + "\n    caller_id: beta\n    allow: [chat]\n"
	if res := a.Apply(store.KindCallers, []byte(callers), ""); !res.Applied {
		t.Fatalf("apply raw callers = %+v", res)
	}
	back, _ := a.Callers()
	if len(back.Callers) != 2 {
		t.Fatalf("expected 2 callers, got %+v", back.Callers)
	}

	creds := "version: 1\nkeyring:\n  - name: main\n    secret_ref: env:TEST_STORE_KEY\n"
	if res := a.Apply(store.KindCredentials, []byte(creds), ""); !res.Applied {
		t.Fatalf("apply raw credentials = %+v", res)
	}

	perm := "version: 1\npathways:\n  chat:\n    outbound:\n      allow: [messages]\n    inbound:\n      default: [message]\n"
	if res := a.Apply(store.KindPermeability, []byte(perm), ""); !res.Applied {
		t.Fatalf("apply raw permeability = %+v", res)
	}
}

func TestReadUnknownKind(t *testing.T) {
	a := consolestore.New(newSeeded(t), testCfg(), recordReload(consolestore.ReloadOutcome{Applied: true}, new(int)))
	if _, err := a.Read("nope"); err == nil {
		t.Fatalf("expected error for unknown kind")
	}
}

func TestFormWithTimeoutsAndNextRefPreserved(t *testing.T) {
	s := newSeeded(t)
	// A form edit must keep the credential's next_secret_ref.
	if _, err := s.DB().Exec(`UPDATE credentials SET next_secret_ref = 'env:NEXT' WHERE name = 'legacy'`); err != nil {
		t.Fatalf("seed next ref: %v", err)
	}
	a := consolestore.New(s, testCfg(), recordReload(consolestore.ReloadOutcome{Applied: true}, new(int)))

	// Pathways form round-trips through render/parse.
	capForm := `{"pathways":[{"pathway":"chat","providers":["main"]}]}`
	if res := a.ApplyForm(store.KindPathways, []byte(capForm), ""); !res.Applied {
		t.Fatalf("apply caps form = %+v", res)
	}

	credForm := `{"credentials":[{"name":"legacy","reference":"env:TEST_STORE_KEY"},{"name":"backup","reference":"env:BACKUP"}]}`
	if res := a.ApplyForm(store.KindCredentials, []byte(credForm), ""); !res.Applied {
		t.Fatalf("apply cred form = %+v", res)
	}
	ef, _ := a.Read(store.KindCredentials)
	if !strings.Contains(ef.Raw, "env:NEXT") {
		t.Fatalf("expected next_secret_ref preserved, got %s", ef.Raw)
	}
}

func TestReloadUnchangedOutcome(t *testing.T) {
	a := consolestore.New(newSeeded(t), testCfg(), recordReload(consolestore.ReloadOutcome{Unchanged: true, Reason: "no change"}, new(int)))
	doc := "version: 1\npathways:\n  - pathway: chat\n    providers: [main]\n"
	res := a.Apply(store.KindPathways, []byte(doc), "")
	if !res.Unchanged {
		t.Fatalf("expected unchanged from reload, got %+v", res)
	}
}

func TestCredentialFileSchemeResolves(t *testing.T) {
	s := newSeeded(t)
	f := filepath.Join(t.TempDir(), "key")
	if err := writeFile(t, f, "sk-file"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB().Exec(`UPDATE credentials SET secret_ref = ? WHERE name = 'legacy'`, "file:"+f); err != nil {
		t.Fatalf("update: %v", err)
	}
	a := consolestore.New(s, testCfg(), recordReload(consolestore.ReloadOutcome{Applied: true}, new(int)))
	creds, _ := a.Credentials()
	if creds.Credentials[0].Scheme != "file" || !creds.Credentials[0].Resolves {
		t.Fatalf("file credential = %+v", creds.Credentials[0])
	}
}

func TestServerViewAuditDefault(t *testing.T) {
	cfg := testCfg()
	cfg.Audit.Sink = ""
	a := consolestore.New(newSeeded(t), cfg, recordReload(consolestore.ReloadOutcome{Applied: true}, new(int)))
	if a.Server().AuditSink != "discard" {
		t.Fatalf("expected discard default audit sink")
	}
}

func TestApplyFormNoOpUnchanged(t *testing.T) {
	calls := 0
	a := consolestore.New(newSeeded(t), testCfg(), recordReload(consolestore.ReloadOutcome{Applied: true}, &calls))
	// A permeability form identical to the seeded state must be a no-op.
	form := `{"pathways":[{"pathway":"chat","outboundAllow":["messages"],"inboundDefault":["message","finish_reason"],"inboundOnReveal":["provider"]}]}`
	res := a.ApplyForm(store.KindPermeability, []byte(form), "")
	if !res.Unchanged || calls != 0 {
		t.Fatalf("expected form no-op unchanged, got %+v calls=%d", res, calls)
	}
}

func TestApplyRawCallersInvalidHashRejected(t *testing.T) {
	a := consolestore.New(newSeeded(t), testCfg(), recordReload(consolestore.ReloadOutcome{Applied: true}, new(int)))
	// The admit parser rejects a short hash before anything is written.
	callers := "version: 1\ncallers:\n  - key_id: ck_demo\n    hash: deadbeef\n    caller_id: acme\n    allow: [chat]\n"
	res := a.Apply(store.KindCallers, []byte(callers), "")
	if !res.Rejected {
		t.Fatalf("expected rejection for invalid hash, got %+v", res)
	}
}

func TestValidateFormUnknownKind(t *testing.T) {
	a := consolestore.New(newSeeded(t), testCfg(), recordReload(consolestore.ReloadOutcome{Applied: true}, new(int)))
	if res := a.ValidateForm("nope", []byte("{}")); res.Valid {
		t.Fatalf("expected invalid for unknown form kind")
	}
	if res := a.ApplyForm("nope", []byte("{}"), ""); !res.Rejected {
		t.Fatalf("expected rejection for unknown form kind")
	}
}

func TestReadAllKinds(t *testing.T) {
	a := consolestore.New(newSeeded(t), testCfg(), recordReload(consolestore.ReloadOutcome{Applied: true}, new(int)))
	for _, k := range a.Kinds() {
		ef, err := a.Read(k)
		if err != nil || ef.Version == "" || ef.Kind != k {
			t.Fatalf("read %q = %+v (err %v)", k, ef, err)
		}
	}
}

func TestValidateInvalidForEachKind(t *testing.T) {
	a := consolestore.New(newSeeded(t), testCfg(), recordReload(consolestore.ReloadOutcome{Applied: true}, new(int)))
	bad := map[string][]byte{
		store.KindCredentials:  []byte("keyring:\n  - name: x\n"),     // missing secret_ref
		store.KindPermeability: []byte("version: 1\npathways: bad\n"), // wrong shape
		store.KindCallers:      []byte("callers:\n  - key_id: x\n"),   // missing hash/caller_id
	}
	for kind, doc := range bad {
		if res := a.Validate(kind, doc); res.Valid {
			t.Fatalf("expected %q invalid", kind)
		}
	}
}

func TestKinds(t *testing.T) {
	a := consolestore.New(newSeeded(t), testCfg(), recordReload(consolestore.ReloadOutcome{Applied: true}, new(int)))
	if got := a.Kinds(); len(got) != 5 {
		t.Fatalf("kinds = %v", got)
	}
}
