package store_test

import (
	"path/filepath"
	"testing"

	"github.com/inferencepath/membrane/internal/store"
)

func TestSecretsTableExistsAndSchemaV2(t *testing.T) {
	s := openTemp(t)
	defer s.Close()

	v, err := s.Version()
	if err != nil {
		t.Fatalf("version: %v", err)
	}
	if v != 7 {
		t.Fatalf("schema version = %d, want 7", v)
	}
	var name string
	if err := s.DB().QueryRow(
		`SELECT name FROM sqlite_master WHERE type='table' AND name='secrets'`,
	).Scan(&name); err != nil {
		t.Fatalf("expected secrets table to exist: %v", err)
	}
}

func TestPutGetSecretRow(t *testing.T) {
	s := openTemp(t)
	defer s.Close()

	v, err := s.PutSecret(store.SecretRow{
		Name:       "openai-main",
		Ciphertext: []byte{0x01, 0x02, 0x03},
		Nonce:      []byte{0x09, 0x08},
		KeyVersion: 1,
		UpdatedAt:  "2026-01-02T03:04:05Z",
	})
	if err != nil {
		t.Fatalf("put: %v", err)
	}
	if v != 1 {
		t.Fatalf("first put version = %d, want 1", v)
	}

	row, ok, err := s.GetSecret("openai-main")
	if err != nil || !ok {
		t.Fatalf("get = (ok=%v, err=%v)", ok, err)
	}
	if string(row.Ciphertext) != "\x01\x02\x03" || string(row.Nonce) != "\x09\x08" {
		t.Fatalf("unexpected blob round trip: %+v", row)
	}
	if row.KeyVersion != 1 || row.Version != 1 {
		t.Fatalf("versions = key %d / value %d, want 1/1", row.KeyVersion, row.Version)
	}
}

func TestPutSecretBumpsVersionOnUpsert(t *testing.T) {
	s := openTemp(t)
	defer s.Close()

	if _, err := s.PutSecret(store.SecretRow{Name: "k", Ciphertext: []byte("a"), Nonce: []byte("n"), KeyVersion: 1}); err != nil {
		t.Fatalf("put1: %v", err)
	}
	v2, err := s.PutSecret(store.SecretRow{Name: "k", Ciphertext: []byte("b"), Nonce: []byte("n"), KeyVersion: 1})
	if err != nil {
		t.Fatalf("put2: %v", err)
	}
	if v2 != 2 {
		t.Fatalf("second put version = %d, want 2", v2)
	}
	row, _, _ := s.GetSecret("k")
	if string(row.Ciphertext) != "b" || row.Version != 2 {
		t.Fatalf("upsert row = %+v, want ciphertext b version 2", row)
	}

	// Still a single row.
	var n int
	if err := s.DB().QueryRow(`SELECT COUNT(*) FROM secrets`).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 1 {
		t.Fatalf("secrets rows = %d, want 1", n)
	}
}

func TestGetSecretMissing(t *testing.T) {
	s := openTemp(t)
	defer s.Close()
	_, ok, err := s.GetSecret("nope")
	if err != nil || ok {
		t.Fatalf("get missing = (ok=%v, err=%v), want (false, nil)", ok, err)
	}
}

func TestListSecretMetaOrderedAndValueFree(t *testing.T) {
	s := openTemp(t)
	defer s.Close()
	_, _ = s.PutSecret(store.SecretRow{Name: "b", Ciphertext: []byte("x"), Nonce: []byte("n"), KeyVersion: 1, UpdatedAt: "t2"})
	_, _ = s.PutSecret(store.SecretRow{Name: "a", Ciphertext: []byte("y"), Nonce: []byte("n"), KeyVersion: 1, UpdatedAt: "t1"})

	metas, err := s.ListSecretMeta()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(metas) != 2 || metas[0].Name != "a" || metas[1].Name != "b" {
		t.Fatalf("meta order = %+v, want [a b]", metas)
	}
}

func TestDeleteAndHasSecret(t *testing.T) {
	s := openTemp(t)
	defer s.Close()
	_, _ = s.PutSecret(store.SecretRow{Name: "k", Ciphertext: []byte("x"), Nonce: []byte("n"), KeyVersion: 1})

	has, err := s.HasSecret("k")
	if err != nil || !has {
		t.Fatalf("HasSecret = (%v, %v), want (true, nil)", has, err)
	}
	deleted, err := s.DeleteSecret("k")
	if err != nil || !deleted {
		t.Fatalf("delete = (%v, %v), want (true, nil)", deleted, err)
	}
	has, _ = s.HasSecret("k")
	if has {
		t.Fatal("HasSecret should be false after delete")
	}
	deleted, _ = s.DeleteSecret("k")
	if deleted {
		t.Fatal("second delete should report false")
	}
}

// TestFreshStoreIncludesSecretsTable checks that a new store has the secrets table.
func TestFreshStoreIncludesSecretsTable(t *testing.T) {
	s, err := store.Open(filepath.Join(t.TempDir(), store.DBFileName))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer s.Close()
	var name string
	if err := s.DB().QueryRow(
		`SELECT name FROM sqlite_master WHERE type='table' AND name='secrets'`,
	).Scan(&name); err != nil {
		t.Fatalf("secrets table missing on fresh store: %v", err)
	}
}
