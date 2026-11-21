package store_test

import (
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/inferencepath/membrane/internal/store"
)

func TestMigrationV5DefaultsLegacyPathwaysToChat(t *testing.T) {
	path := filepath.Join(t.TempDir(), store.DBFileName)
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`
		CREATE TABLE schema_meta (version INTEGER NOT NULL);
		INSERT INTO schema_meta (version) VALUES (5);
		CREATE TABLE pathways (
			name TEXT PRIMARY KEY,
			cascade_allowed INTEGER NOT NULL DEFAULT 0,
			default_provider TEXT NOT NULL DEFAULT ''
		);
		CREATE TABLE providers (name TEXT PRIMARY KEY);
		INSERT INTO pathways (name) VALUES ('legacy-chat');
	`)
	if err != nil {
		t.Fatalf("seed v5 database: %v", err)
	}
	_ = db.Close()

	s, err := store.Open(path)
	if err != nil {
		t.Fatalf("migrate v5 database: %v", err)
	}
	defer s.Close()

	var operation string
	if err := s.DB().QueryRow(`SELECT operation FROM pathways WHERE name='legacy-chat'`).Scan(&operation); err != nil {
		t.Fatal(err)
	}
	if operation != "chat" {
		t.Fatalf("legacy operation = %q, want chat", operation)
	}
}
