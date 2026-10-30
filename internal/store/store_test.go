package store_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/inferencepath/membrane/internal/store"
)

func TestOpenCreatesAndMigratesFreshDB(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sub", store.DBFileName) // sub/ must be created

	s, err := store.Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer s.Close()

	if _, err := os.Stat(path); err != nil {
		t.Fatalf("db file not created: %v", err)
	}
	if s.Path() != path {
		t.Errorf("Path() = %q, want %q", s.Path(), path)
	}

	v, err := s.Version()
	if err != nil {
		t.Fatalf("version: %v", err)
	}
	if v != 7 {
		t.Fatalf("schema version = %d, want 7", v)
	}
}

func TestOpenCreatesExpectedTables(t *testing.T) {
	s := openTemp(t)
	defer s.Close()

	want := []string{
		"pathways", "pathway_providers", "credentials",
		"callers", "caller_allow", "permeability", "schema_meta",
	}
	for _, tbl := range want {
		var name string
		err := s.DB().QueryRow(
			`SELECT name FROM sqlite_master WHERE type='table' AND name=?`, tbl,
		).Scan(&name)
		if err != nil {
			t.Errorf("expected table %q to exist: %v", tbl, err)
		}
	}
}

func TestMigrateIsIdempotent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, store.DBFileName)

	s1, err := store.Open(path)
	if err != nil {
		t.Fatalf("first open: %v", err)
	}
	// Reopening must not wipe data or re-run migrations.
	if _, err := s1.DB().Exec(
		`INSERT INTO pathways (name, cascade_allowed, default_provider) VALUES ('chat', 1, 'openai-main')`,
	); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := s1.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	s2, err := store.Open(path) // reopen same file
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer s2.Close()

	v, err := s2.Version()
	if err != nil || v != 7 {
		t.Fatalf("version after reopen = %d (err %v), want 7", v, err)
	}
	var n int
	if err := s2.DB().QueryRow(`SELECT COUNT(*) FROM pathways`).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 1 {
		t.Fatalf("pathways rows = %d, want 1 (data must survive reopen)", n)
	}
}

func TestForeignKeysEnforced(t *testing.T) {
	s := openTemp(t)
	defer s.Close()

	// The DSN enables foreign keys, so a pathway_providers row for a missing
	// pathway is rejected.
	_, err := s.DB().Exec(
		`INSERT INTO pathway_providers (pathway, provider) VALUES ('ghost', 'default')`,
	)
	if err == nil {
		t.Fatal("expected foreign key violation inserting pathway_providers for missing pathway")
	}
}

func TestOpenEmptyPathErrors(t *testing.T) {
	if _, err := store.Open(""); err == nil {
		t.Fatal("expected error opening empty path")
	}
}

func openTemp(t *testing.T) *store.Store {
	t.Helper()
	s, err := store.Open(filepath.Join(t.TempDir(), store.DBFileName))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	return s
}
