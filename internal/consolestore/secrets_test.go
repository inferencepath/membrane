package consolestore_test

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/inferencepath/membrane/internal/config"
	"github.com/inferencepath/membrane/internal/consolestore"
	"github.com/inferencepath/membrane/internal/secrets"
	"github.com/inferencepath/membrane/internal/store"
)

func newSecretAdapter(t *testing.T) (*consolestore.SecretAdapter, *store.Store) {
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
	return consolestore.NewSecretAdapter(st, mgr), st
}

func TestSecretAdapterSetAndListMetadata(t *testing.T) {
	a, _ := newSecretAdapter(t)
	if err := a.Set("openai-main", "sk-value"); err != nil {
		t.Fatalf("set: %v", err)
	}
	v, err := a.List()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(v.Secrets) != 1 || v.Secrets[0].Name != "openai-main" || v.Secrets[0].Version != 1 {
		t.Fatalf("unexpected list: %+v", v.Secrets)
	}
	if v.Secrets[0].Referenced {
		t.Fatal("secret should be unreferenced initially")
	}
	// The metadata view must not carry the value anywhere.
	if strings.Contains(v.Secrets[0].UpdatedAt, "sk-value") {
		t.Fatal("value leaked into metadata")
	}
}

func TestSecretAdapterReferencedFlagAndDeleteGuard(t *testing.T) {
	a, st := newSecretAdapter(t)
	if err := a.Set("openai-main", "sk-value"); err != nil {
		t.Fatalf("set: %v", err)
	}
	// Reference it from a credential.
	if _, err := st.DB().Exec(
		`INSERT INTO credentials (name, secret_ref) VALUES ('openai-main', 'secret:openai-main')`,
	); err != nil {
		t.Fatalf("seed credential: %v", err)
	}

	v, _ := a.List()
	if len(v.Secrets) != 1 || !v.Secrets[0].Referenced {
		t.Fatalf("expected referenced=true, got %+v", v.Secrets)
	}

	// Delete must be refused while referenced.
	if _, err := a.Delete("openai-main"); err == nil {
		t.Fatal("expected delete to be refused while referenced")
	}

	// Remove the reference, then delete succeeds.
	if _, err := st.DB().Exec(`DELETE FROM credentials WHERE name = 'openai-main'`); err != nil {
		t.Fatalf("remove credential: %v", err)
	}
	deleted, err := a.Delete("openai-main")
	if err != nil || !deleted {
		t.Fatalf("delete = (%v, %v), want (true, nil)", deleted, err)
	}
}

// TestBundleExportExcludesSealedValues proves the GitOps boundary: a declarative
// bundle export carries credential REFERENCES (incl. secret:<name>) but never the
// sealed plaintext value. Sealed values live only in membrane.db (encrypted) and
// are backed up separately from the version-controlled bundle.
func TestBundleExportExcludesSealedValues(t *testing.T) {
	const sealedValue = "sk-BUNDLE-LEAK-CANARY-123"
	sa, st := newSecretAdapter(t)
	if err := sa.Set("openai-main", sealedValue); err != nil {
		t.Fatalf("set secret: %v", err)
	}
	// A credential references the sealed secret, and a pathway uses it.
	exec := func(q string) {
		if _, err := st.DB().Exec(q); err != nil {
			t.Fatalf("seed %q: %v", q, err)
		}
	}
	exec(`INSERT INTO providers (name, family, endpoint, models, default_model, secret_ref)
	      VALUES ('openai-main', 'openai', 'https://api.openai.com', 'gpt-4o-mini', 'gpt-4o-mini', 'secret:openai-main')`)
	exec(`INSERT INTO pathways (name, cascade_allowed, default_provider) VALUES ('chat', 0, 'openai-main')`)
	exec(`INSERT INTO pathway_providers (pathway, provider) VALUES ('chat', 'openai-main')`)

	adapter := consolestore.New(st, config.Config{}, func() consolestore.ReloadOutcome {
		return consolestore.ReloadOutcome{Applied: true}
	})
	bundle, err := adapter.ExportBundle()
	if err != nil {
		t.Fatalf("export bundle: %v", err)
	}
	if strings.Contains(bundle.Raw, sealedValue) {
		t.Fatalf("SEALED VALUE LEAK: bundle export contains the plaintext value:\n%s", bundle.Raw)
	}
	// The reference itself must still be present (it is not a secret).
	if !strings.Contains(bundle.Raw, "secret:openai-main") {
		t.Fatalf("bundle should carry the secret: reference, got:\n%s", bundle.Raw)
	}
}

func TestSecretAdapterReferencedByNextSecretRef(t *testing.T) {
	a, st := newSecretAdapter(t)
	_ = a.Set("rotating", "v1")
	if _, err := st.DB().Exec(
		`INSERT INTO credentials (name, secret_ref, next_secret_ref)
		 VALUES ('main', 'env:X', 'secret:rotating')`,
	); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if _, err := a.Delete("rotating"); err == nil {
		t.Fatal("expected delete refused when referenced via next_secret_ref")
	}
}
