package secrets_test

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/inferencepath/membrane/internal/secrets"
	"github.com/inferencepath/membrane/internal/store"
)

func newManager(t *testing.T) (*secrets.Manager, *store.Store) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), store.DBFileName))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	k, err := secrets.NewKeeper([]byte("a-sufficiently-long-master-key!!"), 1)
	if err != nil {
		t.Fatalf("new keeper: %v", err)
	}
	fixed := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	return secrets.NewManager(st, k, secrets.WithClock(func() time.Time { return fixed })), st
}

func TestManagerSetGet(t *testing.T) {
	m, _ := newManager(t)
	if err := m.Set("openai-main", "sk-12345"); err != nil {
		t.Fatalf("set: %v", err)
	}
	got, err := m.Get("openai-main")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got != "sk-12345" {
		t.Fatalf("get = %q, want sk-12345", got)
	}
}

func TestManagerGetMissing(t *testing.T) {
	m, _ := newManager(t)
	if _, err := m.Get("nope"); err != secrets.ErrNotFound {
		t.Fatalf("get missing err = %v, want ErrNotFound", err)
	}
}

func TestManagerSetBumpsVersion(t *testing.T) {
	m, st := newManager(t)
	if err := m.Set("k", "v1"); err != nil {
		t.Fatalf("set v1: %v", err)
	}
	if err := m.Set("k", "v2"); err != nil {
		t.Fatalf("set v2: %v", err)
	}
	got, err := m.Get("k")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got != "v2" {
		t.Fatalf("get = %q, want v2 (latest)", got)
	}
	metas, err := st.ListSecretMeta()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(metas) != 1 || metas[0].Version != 2 {
		t.Fatalf("meta = %+v, want single row version 2", metas)
	}
}

func TestManagerRejectsBadNameAndEmptyValue(t *testing.T) {
	m, _ := newManager(t)
	if err := m.Set("Bad Name", "v"); err == nil {
		t.Fatal("expected invalid-name error")
	}
	if err := m.Set("ok", ""); err == nil {
		t.Fatal("expected empty-value error")
	}
}

func TestManagerResolveScheme(t *testing.T) {
	m, _ := newManager(t)
	if err := m.Set("anthropic-main", "key-abc"); err != nil {
		t.Fatalf("set: %v", err)
	}
	got, err := m.Resolve("secret:anthropic-main")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if got != "key-abc" {
		t.Fatalf("resolve = %q, want key-abc", got)
	}
	if _, err := m.Resolve("env:FOO"); err == nil {
		t.Fatal("expected error resolving a non-secret scheme")
	}
}

func TestManagerListIsValueFree(t *testing.T) {
	m, _ := newManager(t)
	_ = m.Set("a", "secret-value-a")
	_ = m.Set("b", "secret-value-b")
	metas, err := m.List()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(metas) != 2 {
		t.Fatalf("len(metas) = %d, want 2", len(metas))
	}
	// Metadata must be ordered and carry no value-bearing fields (compile-time:
	// SecretMeta has no ciphertext/value). Sanity-check names/timestamps only.
	if metas[0].Name != "a" || metas[1].Name != "b" {
		t.Fatalf("unexpected order: %+v", metas)
	}
	if metas[0].UpdatedAt == "" {
		t.Fatal("expected updated_at to be recorded")
	}
}

func TestManagerDelete(t *testing.T) {
	m, _ := newManager(t)
	_ = m.Set("gone", "v")
	deleted, err := m.Delete("gone")
	if err != nil || !deleted {
		t.Fatalf("delete = (%v, %v), want (true, nil)", deleted, err)
	}
	if _, err := m.Get("gone"); err != secrets.ErrNotFound {
		t.Fatalf("get after delete err = %v, want ErrNotFound", err)
	}
	deleted, err = m.Delete("gone")
	if err != nil || deleted {
		t.Fatalf("second delete = (%v, %v), want (false, nil)", deleted, err)
	}
}
