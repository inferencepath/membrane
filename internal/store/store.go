// Package store is the Membrane control-plane state store: an embedded,
// single-file SQLite database (pure-Go, no CGO) that holds the mutable runtime
// configuration the operator manages through the Membrane Console — pathways
// and their routing targets, credential REFERENCES, Caller Keys and their
// allowlists, and the Permeability policy.
//
// It is the single source of truth for config-as-data. The daemon builds its
// immutable serving Snapshot from this store and reloads atomically on change
// (fail-closed, last-good) — exactly as the file-backed model did, but with one
// embedded store instead of hand-authored YAML files.
//
// Security invariant: the store holds secret REFERENCES only (env:/file:/vault:)
// — never a secret value. Caller secrets are persisted solely as hashes. Nothing
// in this package resolves, reads, or logs a secret value.
package store

import (
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"

	_ "modernc.org/sqlite" // registers the pure-Go "sqlite" database/sql driver
)

// DBFileName is the canonical database filename inside the data directory.
const DBFileName = "membrane.db"

// ErrUnknownKind is returned for an unrecognized config kind.
var ErrUnknownKind = errors.New("store: unknown config kind")

// ErrUnknownKeyID is returned when a caller edit names a key_id that does not exist.
var ErrUnknownKeyID = errors.New("store: unknown key_id")

// Store owns the embedded SQLite connection and its schema lifecycle.
type Store struct {
	db   *sql.DB
	path string
}

// Open opens (creating if absent) the embedded state database at path, applies
// the connection pragmas suited to a single-file embedded store, and migrates
// the schema up to the latest version. The parent directory is created with
// 0700 if missing. First run therefore yields an empty-but-valid schema — the
// "it just works from docker compose" bootstrap.
func Open(path string) (*Store, error) {
	if path == "" {
		return nil, fmt.Errorf("store: open: empty path")
	}
	if dir := filepath.Dir(path); dir != "" {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, fmt.Errorf("store: create data dir: %w", err)
		}
	}

	db, err := sql.Open("sqlite", dsn(path))
	if err != nil {
		return nil, fmt.Errorf("store: open: %w", err)
	}
	// An embedded single-file DB must serialize writers; a single connection
	// avoids "database is locked" churn while WAL still allows concurrent reads.
	db.SetMaxOpenConns(1)

	if err := db.Ping(); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("store: ping: %w", err)
	}

	s := &Store{db: db, path: path}
	if err := s.migrate(); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("store: migrate: %w", err)
	}
	return s, nil
}

// DB exposes the underlying *sql.DB for repository packages built on top of the
// store. Callers must not close it; use Store.Close.
func (s *Store) DB() *sql.DB { return s.db }

// Path returns the database file path.
func (s *Store) Path() string { return s.path }

// Close closes the database.
func (s *Store) Close() error { return s.db.Close() }

// dsn builds the modernc.org/sqlite DSN with embedded-store pragmas:
//   - busy_timeout: wait out brief write contention instead of erroring.
//   - journal_mode=WAL: readers don't block the single writer.
//   - synchronous=NORMAL: safe with WAL, far faster than FULL.
//   - foreign_keys=ON: enforce the referential integrity the schema declares.
func dsn(path string) string {
	q := url.Values{}
	q.Add("_pragma", "busy_timeout(5000)")
	q.Add("_pragma", "journal_mode(WAL)")
	q.Add("_pragma", "synchronous(NORMAL)")
	q.Add("_pragma", "foreign_keys(ON)")
	return "file:" + path + "?" + q.Encode()
}
