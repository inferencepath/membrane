package consolestore_test

import (
	"path/filepath"
	"testing"

	"github.com/inferencepath/membrane/internal/consolestore"
	"github.com/inferencepath/membrane/internal/secrets"
	"github.com/inferencepath/membrane/internal/server/console"
	"github.com/inferencepath/membrane/internal/store"
)

func newProviderAdapter(t *testing.T) (*consolestore.ProviderAdapter, *secrets.Manager, *store.Store) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), store.DBFileName))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	keeper, err := secrets.NewKeeper([]byte("a-sufficiently-long-master-key!!"), 1)
	if err != nil {
		t.Fatalf("keeper: %v", err)
	}
	mgr := secrets.NewManager(st, keeper)
	return consolestore.NewProviderAdapter(st, mgr), mgr, st
}

func sampleInput(value string) console.ProviderInput {
	return console.ProviderInput{
		Name:         "openai-main",
		Family:       "openai",
		Models:       []string{"gpt-4o"},
		DefaultModel: "gpt-4o",
		Endpoint:     "https://api.openai.com/v1",
		Value:        value,
	}
}

// TestProviderAdapterSetWritesSecretAndProviderAtomically proves one Set seals
// the value AND creates the provider definition — both present after a single
// call — and that the value round-trips only through the sealed store, never the
// provider row (which holds a reference).
func TestProviderAdapterSetWritesSecretAndProviderAtomically(t *testing.T) {
	a, mgr, st := newProviderAdapter(t)
	const value = "sk-atomic-canary"
	if err := a.Set(sampleInput(value)); err != nil {
		t.Fatalf("set: %v", err)
	}

	// Sealed secret exists and decrypts to the value.
	got, err := mgr.Get("openai-main")
	if err != nil {
		t.Fatalf("get sealed: %v", err)
	}
	if got != value {
		t.Fatalf("sealed value = %q, want %q", got, value)
	}

	// Provider exists as a reference only (never the plaintext).
	provs, err := st.ReadProviders()
	if err != nil {
		t.Fatalf("read providers: %v", err)
	}
	if len(provs) != 1 || provs[0].Name != "openai-main" || provs[0].SecretRef != "secret:openai-main" {
		t.Fatalf("unexpected provider rows: %+v", provs)
	}
	if provs[0].SecretRef == value {
		t.Fatal("provider leaked the plaintext value")
	}
}

func TestProviderAdapterListShowsSealedAndUsedBy(t *testing.T) {
	a, _, st := newProviderAdapter(t)
	if err := a.Set(sampleInput("sk-value")); err != nil {
		t.Fatalf("set: %v", err)
	}
	// Wire a pathway target that uses the provider.
	exec := func(q string) {
		if _, err := st.DB().Exec(q); err != nil {
			t.Fatalf("seed %q: %v", q, err)
		}
	}
	exec(`INSERT INTO pathways (name, cascade_allowed, default_provider) VALUES ('chat', 0, 'openai-main')`)
	exec(`INSERT INTO pathway_providers (pathway, provider) VALUES ('chat', 'openai-main')`)

	v, err := a.List()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(v.Providers) != 1 {
		t.Fatalf("want 1 provider, got %+v", v.Providers)
	}
	p := v.Providers[0]
	if p.Name != "openai-main" || !p.Sealed || p.SecretRef != "secret:openai-main" {
		t.Fatalf("unexpected provider view: %+v", p)
	}
	if p.Family != "openai" || p.DefaultModel != "gpt-4o" {
		t.Fatalf("provider view missing connection facts: %+v", p)
	}
	if len(p.UsedBy) != 1 || p.UsedBy[0] != "chat" {
		t.Fatalf("usedBy = %v, want [chat]", p.UsedBy)
	}
}

func TestProviderAdapterDeleteGuardAndSuccess(t *testing.T) {
	a, mgr, st := newProviderAdapter(t)
	if err := a.Set(sampleInput("sk-value")); err != nil {
		t.Fatalf("set: %v", err)
	}
	exec := func(q string) {
		if _, err := st.DB().Exec(q); err != nil {
			t.Fatalf("seed %q: %v", q, err)
		}
	}
	exec(`INSERT INTO pathways (name, cascade_allowed, default_provider) VALUES ('chat', 0, 'openai-main')`)
	exec(`INSERT INTO pathway_providers (pathway, provider) VALUES ('chat', 'openai-main')`)

	// Referenced ⇒ refused.
	if _, err := a.Delete("openai-main"); err == nil {
		t.Fatal("expected delete to be refused while referenced by a pathway")
	}

	// Detach the target, then delete succeeds and removes both secret + provider.
	exec(`DELETE FROM pathway_providers WHERE provider = 'openai-main'`)
	ok, err := a.Delete("openai-main")
	if err != nil {
		t.Fatalf("delete: %v", err)
	}
	if !ok {
		t.Fatal("expected a row to be removed")
	}
	if _, err := mgr.Get("openai-main"); err == nil {
		t.Fatal("sealed secret should be gone after delete")
	}
	provs, err := st.ReadProviders()
	if err != nil {
		t.Fatalf("read providers: %v", err)
	}
	if len(provs) != 0 {
		t.Fatalf("provider should be gone, got %+v", provs)
	}
}
