package store_test

import (
	"path/filepath"
	"testing"

	"github.com/inferencepath/membrane/internal/store"
)

func openStore(t *testing.T) *store.Store {
	t.Helper()
	s, err := store.Open(filepath.Join(t.TempDir(), store.DBFileName))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func sampleProvider() store.ProviderRow {
	return store.ProviderRow{
		Name:         "openai-main",
		Family:       "openai",
		Endpoint:     "https://api.openai.com/v1",
		Models:       []string{"gpt-4o"},
		DefaultModel: "gpt-4o",
		SecretRef:    "secret:openai-main",
	}
}

// TestUpsertProviderWritesBothRows proves the sealed secret and its provider
// definition are written together, and that re-running Set updates in place
// (version bump) rather than duplicating.
func TestUpsertProviderWritesBothRows(t *testing.T) {
	s := openStore(t)
	p := store.ProviderUpsert{
		Secret:   store.SecretRow{Name: "openai-main", Ciphertext: []byte("ct"), Nonce: []byte("n"), KeyVersion: 1, UpdatedAt: "t0"},
		Provider: sampleProvider(),
	}
	if err := s.UpsertProvider(p); err != nil {
		t.Fatalf("upsert: %v", err)
	}

	row, ok, err := s.GetSecret("openai-main")
	if err != nil || !ok {
		t.Fatalf("get secret: ok=%v err=%v", ok, err)
	}
	if row.Version != 1 {
		t.Fatalf("version = %d, want 1", row.Version)
	}
	provs, err := s.ReadProviders()
	if err != nil {
		t.Fatalf("read providers: %v", err)
	}
	if len(provs) != 1 || provs[0].SecretRef != "secret:openai-main" || provs[0].Family != "openai" {
		t.Fatalf("unexpected providers: %+v", provs)
	}

	// Rotate: same name updates in place and bumps the secret version.
	p.Secret.Ciphertext = []byte("ct2")
	if err := s.UpsertProvider(p); err != nil {
		t.Fatalf("re-upsert: %v", err)
	}
	row, _, _ = s.GetSecret("openai-main")
	if row.Version != 2 {
		t.Fatalf("version after rotate = %d, want 2", row.Version)
	}
	provs, _ = s.ReadProviders()
	if len(provs) != 1 {
		t.Fatalf("rotate should not duplicate provider: %+v", provs)
	}
}

// TestUpsertProviderPreservesOtherProviders proves the targeted upsert does not
// wipe unrelated providers (unlike the replace-all WriteProviders path).
func TestUpsertProviderPreservesOtherProviders(t *testing.T) {
	s := openStore(t)
	if err := s.WriteProviders([]store.ProviderRow{{
		Name: "legacy", Family: "openai", Endpoint: "https://x", Models: []string{"m"}, DefaultModel: "m", SecretRef: "env:LEGACY",
	}}); err != nil {
		t.Fatalf("seed provider: %v", err)
	}
	if err := s.UpsertProvider(store.ProviderUpsert{
		Secret:   store.SecretRow{Name: "openai-main", Ciphertext: []byte("ct"), Nonce: []byte("n"), KeyVersion: 1},
		Provider: sampleProvider(),
	}); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	provs, _ := s.ReadProviders()
	if len(provs) != 2 {
		t.Fatalf("want 2 providers (legacy + new), got %+v", provs)
	}
}

func TestDeleteProviderRemovesBothRows(t *testing.T) {
	s := openStore(t)
	if err := s.UpsertProvider(store.ProviderUpsert{
		Secret:   store.SecretRow{Name: "openai-main", Ciphertext: []byte("ct"), Nonce: []byte("n"), KeyVersion: 1},
		Provider: sampleProvider(),
	}); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	removed, err := s.DeleteProvider("openai-main")
	if err != nil {
		t.Fatalf("delete: %v", err)
	}
	if !removed {
		t.Fatal("expected a provider row to be removed")
	}
	if _, ok, _ := s.GetSecret("openai-main"); ok {
		t.Fatal("sealed secret should be gone")
	}
	provs, _ := s.ReadProviders()
	if len(provs) != 0 {
		t.Fatalf("provider should be gone, got %+v", provs)
	}
}
