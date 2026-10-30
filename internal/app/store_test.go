package app_test

import (
	"context"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/inferencepath/membrane/internal/app"
	"github.com/inferencepath/membrane/internal/store"
)

// newStore opens a fresh store and optionally seeds a complete valid config.
func newStore(t *testing.T, seed bool) *store.Store {
	t.Helper()
	s, err := store.Open(filepath.Join(t.TempDir(), store.DBFileName))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	if !seed {
		return s
	}
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := s.DB().Exec(q, args...); err != nil {
			t.Fatalf("seed %q: %v", q, err)
		}
	}
	exec(`INSERT INTO pathways (name, cascade_allowed, default_provider) VALUES ('chat', 0, 'main')`)
	exec(`INSERT INTO providers (name, family, endpoint, models, default_model, secret_ref)
	      VALUES ('main', 'openai', 'https://api.openai.com', 'gpt-4o-mini', 'gpt-4o-mini', 'env:TEST_STORE_KEY')`)
	exec(`INSERT INTO pathway_providers (pathway, provider) VALUES ('chat', 'main')`)
	exec(`INSERT INTO callers (key_id, caller_id, name, secret_hash)
	      VALUES ('ck_demo', 'acme-app', 'demo', '458d6534a760ed4c8c2f1c8f86a517572cb56fa6a0f8554efdda2cb8532a4816')`)
	exec(`INSERT INTO caller_allow (key_id, pathway) VALUES ('ck_demo', 'chat')`)
	exec(`INSERT INTO permeability (pathway, outbound_allow, inbound_default, inbound_on_reveal)
	      VALUES ('chat', 'messages', 'message` + "\n" + `finish_reason` + "\n" + `usage', 'provider` + "\n" + `model')`)
	return s
}

func TestBuildSnapshotFromStoreValid(t *testing.T) {
	t.Setenv("TEST_STORE_KEY", "secret-value")
	s := newStore(t, true)

	snap, kr, err := app.BuildSnapshotFromStore(s, nil)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if snap == nil || kr == nil {
		t.Fatal("nil snapshot or keyring")
	}
	if snap.Resolver == nil || snap.Transport == nil || snap.Authorizer == nil {
		t.Fatalf("snapshot not fully assembled: %+v", snap)
	}
}

func TestBuildSnapshotRejectsUnsupportedOperation(t *testing.T) {
	t.Setenv("TEST_STORE_KEY", "secret-value")
	s := newStore(t, true)
	// Anthropic does not support embeddings, so an embeddings Pathway bound to
	// it must fail assembly.
	exec := func(q string, args ...any) {
		if _, err := s.DB().Exec(q, args...); err != nil {
			t.Fatalf("seed %q: %v", q, err)
		}
	}
	exec(`INSERT INTO providers (name, family, endpoint, models, default_model, secret_ref)
	      VALUES ('anthro', 'anthropic', 'https://api.anthropic.com', 'claude', 'claude', 'env:TEST_STORE_KEY')`)
	exec(`UPDATE pathways SET operation = 'embeddings', default_provider = 'anthro' WHERE name = 'chat'`)
	exec(`UPDATE pathway_providers SET provider = 'anthro' WHERE pathway = 'chat'`)
	// The permeability policy must also match the embeddings vocabulary.
	exec(`UPDATE permeability SET outbound_allow = 'input', inbound_default = 'data` + "\n" + `usage' WHERE pathway = 'chat'`)

	_, _, err := app.BuildSnapshotFromStore(s, nil)
	if err == nil || !strings.Contains(err.Error(), "does not support operation") {
		t.Fatalf("want capability failure, got %v", err)
	}
}

func TestBuildSnapshotRejectsVocabularyMismatch(t *testing.T) {
	t.Setenv("TEST_STORE_KEY", "secret-value")
	s := newStore(t, true)
	exec := func(q string, args ...any) {
		if _, err := s.DB().Exec(q, args...); err != nil {
			t.Fatalf("seed %q: %v", q, err)
		}
	}
	// An embeddings Pathway whose policy still lists chat's messages field
	// fails assembly, even though openai supports embeddings.
	exec(`UPDATE pathways SET operation = 'embeddings' WHERE name = 'chat'`)
	_, _, err := app.BuildSnapshotFromStore(s, nil)
	if err == nil || !strings.Contains(err.Error(), "not valid for this operation") {
		t.Fatalf("want vocabulary failure, got %v", err)
	}
}

func TestBuildSnapshotFromStoreEmptyFailsClosed(t *testing.T) {
	s := newStore(t, false) // fresh, no pathways
	if _, _, err := app.BuildSnapshotFromStore(s, nil); err == nil {
		t.Fatal("expected fail-closed error for empty store")
	}
}

func TestBuildSnapshotFromStoreDanglingKeyringRef(t *testing.T) {
	t.Setenv("TEST_STORE_KEY", "secret-value")
	s := newStore(t, true)
	// Point the target at a provider that does not exist.
	if _, err := s.DB().Exec(`INSERT INTO providers (name, family, endpoint, models, default_model, secret_ref) VALUES ('ghost', 'openai', 'https://x', 'm', 'm', 'env:NONE')`); err != nil {
		t.Fatalf("seed ghost: %v", err)
	}
	if _, err := s.DB().Exec(`UPDATE pathway_providers SET provider = 'ghost'`); err != nil {
		t.Fatalf("mutate: %v", err)
	}
	if _, _, err := app.BuildSnapshotFromStore(s, nil); err == nil {
		t.Fatal("expected fail-closed error for dangling provider ref")
	}
}

func TestValidateSpecs(t *testing.T) {
	t.Setenv("TEST_STORE_KEY", "secret-value")
	s := newStore(t, true)

	// The store's rendered documents validate.
	perm, _ := s.RenderPermeability()
	callers, _ := s.RenderCallers()
	creds, _ := s.RenderCredentials()
	prov, _ := s.RenderProviders()
	caps, _ := s.RenderPathways()
	if err := app.ValidateSpecs(perm, callers, creds, prov, caps); err != nil {
		t.Fatalf("expected valid specs, got %v", err)
	}

	// A parse failure is prefixed with the document kind.
	cases := []struct {
		name                             string
		perm, callers, creds, prov, caps []byte
		wantSub                          string
	}{
		{"bad permeability", []byte("version: 1\npathways: bad\n"), callers, creds, prov, caps, "permeability"},
		{"bad callers", perm, []byte("callers:\n  - key_id: x\n"), creds, prov, caps, "callers"},
		{"bad credentials", perm, callers, []byte("keyring:\n  - name: x\n"), prov, caps, "credentials"},
		{"bad providers", perm, callers, creds, []byte("version: 1\nproviders: [{name: a}]"), caps, "providers"},
		{"bad pathways", perm, callers, creds, prov, []byte("pathways: [ : ]"), "pathways"},
	}
	for _, tc := range cases {
		err := app.ValidateSpecs(tc.perm, tc.callers, tc.creds, tc.prov, tc.caps)
		if err == nil || !strings.Contains(err.Error(), tc.wantSub) {
			t.Fatalf("%s: err = %v, want containing %q", tc.name, err, tc.wantSub)
		}
	}

	// A Pathway naming an unknown provider parses but fails assembly.
	badCaps := strings.Replace(string(caps), "provider: main", "provider: ghost", 1)
	if err := app.ValidateSpecs(perm, callers, creds, prov, []byte(badCaps)); err == nil {
		t.Fatal("expected cross-kind assembly failure for dangling provider ref")
	}

	// A Pathway with no Permeability policy is rejected at load.
	emptyPerm := []byte("version: 1\npathways:\n  other:\n    outbound:\n      allow: [messages]\n    inbound:\n      default: [message]\n")
	err := app.ValidateSpecs(emptyPerm, callers, creds, prov, caps)
	if err == nil || !strings.Contains(err.Error(), "no permeability policy") {
		t.Fatalf("expected 'no permeability policy' error, got %v", err)
	}

	// A Permeability entry for an unconfigured Pathway is rejected.
	extraPerm := []byte("version: 1\npathways:\n  chat:\n    outbound:\n      allow: [messages]\n    inbound:\n      default: [message]\n  ghostcap:\n    outbound:\n      allow: [messages]\n    inbound:\n      default: [message]\n")
	err = app.ValidateSpecs(extraPerm, callers, creds, prov, caps)
	if err == nil || !strings.Contains(err.Error(), "unknown pathway") {
		t.Fatalf("expected 'unknown pathway' error, got %v", err)
	}
}

func TestStoreManagerReloadLifecycle(t *testing.T) {
	t.Setenv("TEST_STORE_KEY", "secret-value")
	s := newStore(t, true)

	mgr, err := app.NewStoreManager(s, nil, nil)
	if err != nil {
		t.Fatalf("new store manager: %v", err)
	}
	if mgr.Current() == nil {
		t.Fatal("Current() is nil after construction")
	}

	// Same content hash, so the reload is Unchanged.
	if r := mgr.Reload(); !r.Unchanged {
		t.Fatalf("expected Unchanged, got %+v", r)
	}

	// Adding a second Pathway and target is Applied.
	exec := func(q string) {
		if _, err := s.DB().Exec(q); err != nil {
			t.Fatalf("mutate: %v", err)
		}
	}
	exec(`INSERT INTO pathways (name, cascade_allowed, default_provider) VALUES ('embed', 0, 'main')`)
	exec(`INSERT INTO pathway_providers (pathway, provider) VALUES ('embed', 'main')`)
	exec(`INSERT INTO permeability (pathway, outbound_allow, inbound_default)
	      VALUES ('embed', 'input', 'embedding')`)

	if r := mgr.Reload(); !r.Applied {
		t.Fatalf("expected Applied after change, got %+v", r)
	}
}

func TestStoreManagerIncompleteFailsClosed(t *testing.T) {
	s := newStore(t, false)
	if _, err := app.NewStoreManager(s, nil, nil); err == nil {
		t.Fatal("expected error building manager over empty store")
	}
}

func TestStoreManagerObserverAndWatch(t *testing.T) {
	t.Setenv("TEST_STORE_KEY", "secret-value")
	s := newStore(t, true)

	var mu sync.Mutex
	var results []app.ReloadResult
	obs := func(r app.ReloadResult) {
		mu.Lock()
		results = append(results, r)
		mu.Unlock()
	}

	mgr, err := app.NewStoreManager(s, nil, nil, app.WithStoreReloadObserver(obs))
	if err != nil {
		t.Fatalf("new: %v", err)
	}

	// Trigger one reload through Watch, then cancel it.
	ctx, cancel := context.WithCancel(context.Background())
	trigger := make(chan struct{}, 1)
	done := make(chan struct{})
	go func() { mgr.Watch(ctx, trigger); close(done) }()

	// Change the store so the reload is Applied rather than Unchanged.
	if _, err := s.DB().Exec(`UPDATE pathways SET cascade_allowed = 1 WHERE name = 'chat'`); err != nil {
		t.Fatalf("mutate: %v", err)
	}
	trigger <- struct{}{}

	deadline := time.After(2 * time.Second)
	for {
		mu.Lock()
		n := len(results)
		mu.Unlock()
		if n >= 1 {
			break
		}
		select {
		case <-deadline:
			t.Fatal("observer was not called after Watch trigger")
		case <-time.After(10 * time.Millisecond):
		}
	}
	cancel()
	<-done
}
