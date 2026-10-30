// Package store is the Membrane control-plane state store: a single-file
// SQLite database (pure Go, no CGO) holding the runtime configuration managed
// through the Membrane Console. That covers pathways and their providers,
// credential references, Caller Keys and their allowlists, and the
// Permeability policy.
//
// The daemon builds its immutable serving Snapshot from this store and reloads
// on change (fail closed, last-good).
//
// The store holds secret references (env:/file:/vault:), hashed caller
// secrets, and sealed ciphertext; never a plaintext secret. Nothing in this
// package resolves, reads, or logs a secret value.
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
// mode 0700 if missing, so a first run yields an empty, valid schema.
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
	// SQLite serializes writers; one connection avoids "database is locked"
	// errors.
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

// DB returns the underlying *sql.DB. Callers must not close it; use
// Store.Close.
func (s *Store) DB() *sql.DB { return s.db }

// Path returns the database file path.
func (s *Store) Path() string { return s.path }

// Close closes the database.
func (s *Store) Close() error { return s.db.Close() }

// dsn builds the modernc.org/sqlite DSN. busy_timeout waits out brief write
// contention, WAL keeps readers from blocking the writer, synchronous=NORMAL is
// safe with WAL, and foreign_keys enforces the schema's references.
func dsn(path string) string {
	q := url.Values{}
	q.Add("_pragma", "busy_timeout(5000)")
	q.Add("_pragma", "journal_mode(WAL)")
	q.Add("_pragma", "synchronous(NORMAL)")
	q.Add("_pragma", "foreign_keys(ON)")
	return "file:" + path + "?" + q.Encode()
}
