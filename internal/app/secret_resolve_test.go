package app_test

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/inferencepath/membrane/internal/app"
	"github.com/inferencepath/membrane/internal/secrets"
	"github.com/inferencepath/membrane/internal/store"
)

// sealedStore opens a store seeded with a complete config whose credential uses a
// `secret:<name>` reference, and returns the store plus a sealed-store resolver
// backed by a Manager holding that secret.
func sealedStore(t *testing.T) (*store.Store, app.SecretResolver) {
	t.Helper()
	s, err := store.Open(filepath.Join(t.TempDir(), store.DBFileName))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := s.DB().Exec(q, args...); err != nil {
			t.Fatalf("seed %q: %v", q, err)
		}
	}
	exec(`INSERT INTO pathways (name, cascade_allowed, default_provider) VALUES ('chat', 0, 'openai-main')`)
	exec(`INSERT INTO providers (name, family, endpoint, models, default_model, secret_ref)
	      VALUES ('openai-main', 'openai', 'https://api.openai.com', 'gpt-4o-mini', 'gpt-4o-mini', 'secret:openai-main')`)
	exec(`INSERT INTO pathway_providers (pathway, provider) VALUES ('chat', 'openai-main')`)
	exec(`INSERT INTO callers (key_id, caller_id, name, secret_hash)
	      VALUES ('ck_demo', 'acme-app', 'demo', '458d6534a760ed4c8c2f1c8f86a517572cb56fa6a0f8554efdda2cb8532a4816')`)
	exec(`INSERT INTO caller_allow (key_id, pathway) VALUES ('ck_demo', 'chat')`)
	exec(`INSERT INTO permeability (pathway, outbound_allow, inbound_default, inbound_on_reveal)
	      VALUES ('chat', 'messages', 'message` + "\n" + `finish_reason` + "\n" + `usage', 'provider` + "\n" + `model')`)

	keeper, err := secrets.NewKeeper([]byte("a-sufficiently-long-master-key!!"), 1)
	if err != nil {
		t.Fatalf("keeper: %v", err)
	}
	mgr := secrets.NewManager(s, keeper, secrets.WithClock(func() time.Time { return time.Unix(0, 0).UTC() }))
	if err := mgr.Set("openai-main", "sk-sealed-value"); err != nil {
		t.Fatalf("set secret: %v", err)
	}
	return s, mgr.Resolve
}

func TestBuildSnapshotResolvesSealedSecret(t *testing.T) {
	s, resolve := sealedStore(t)
	snap, kr, err := app.BuildSnapshotFromStoreWithSecrets(s, nil, resolve)
	if err != nil {
		t.Fatalf("build with sealed resolver: %v", err)
	}
	if snap == nil || kr == nil {
		t.Fatal("nil snapshot or keyring")
	}
}

func TestBuildSnapshotSealedSecretFailsClosedWithoutResolver(t *testing.T) {
	s, _ := sealedStore(t)
	// No resolver: the `secret:` reference cannot be resolved → fail closed.
	if _, _, err := app.BuildSnapshotFromStore(s, nil); err == nil {
		t.Fatal("expected fail-closed error for secret: reference without a resolver")
	}
}

func TestBuildSnapshotSealedSecretMissingValueFailsClosed(t *testing.T) {
	s, resolve := sealedStore(t)
	// Point the provider at a sealed name that was never stored.
	if _, err := s.DB().Exec(`UPDATE providers SET secret_ref = 'secret:missing'`); err != nil {
		t.Fatalf("mutate: %v", err)
	}
	if _, _, err := app.BuildSnapshotFromStoreWithSecrets(s, nil, resolve); err == nil {
		t.Fatal("expected fail-closed error for missing sealed value")
	}
}

func TestStoreManagerWithSecretResolver(t *testing.T) {
	s, resolve := sealedStore(t)
	mgr, err := app.NewStoreManager(s, nil, nil, app.WithSecretResolver(resolve))
	if err != nil {
		t.Fatalf("new store manager with resolver: %v", err)
	}
	if mgr.Current() == nil {
		t.Fatal("Current() nil after construction")
	}
}

func TestValidateSpecsWithSealedResolver(t *testing.T) {
	s, resolve := sealedStore(t)
	perm, _ := s.RenderPermeability()
	callers, _ := s.RenderCallers()
	creds, _ := s.RenderCredentials()
	prov, _ := s.RenderProviders()
	caps, _ := s.RenderPathways()

	// With the resolver, the sealed provider validates.
	if err := app.ValidateSpecsWith(resolve, perm, callers, creds, prov, caps); err != nil {
		t.Fatalf("validate with resolver: %v", err)
	}
	// Without it, the same specs fail closed.
	if err := app.ValidateSpecs(perm, callers, creds, prov, caps); err == nil {
		t.Fatal("expected fail-closed validation for secret: reference without a resolver")
	} else if !strings.Contains(err.Error(), "secret") {
		// The error should reference the unresolved scheme/credential.
		t.Logf("fail-closed error (ok): %v", err)
	}
}
