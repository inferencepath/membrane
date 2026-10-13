package store

import (
	"database/sql"
	"fmt"
)

// migration is one forward, irreversible schema step. Steps are applied in order
// and each bumps schema_meta.version. A tiny hand-rolled stepper keeps the
// dependency surface minimal (no migration library) while still giving us a
// versioned, forward-only schema we can evolve safely.
type migration struct {
	version int
	stmts   []string
}

// migrations is the ordered schema history. Append new steps; never edit or
// reorder applied ones. Version N is reached after migrations[0..N-1] run.
var migrations = []migration{
	{
		version: 1,
		stmts: []string{
			// Routing: pathways and their prioritized targets. No secrets —
			// targets reference a credential by keyring_ref only.
			`CREATE TABLE pathways (
				name     TEXT PRIMARY KEY,
				strategy TEXT NOT NULL DEFAULT 'priority'
			)`,
			`CREATE TABLE targets (
				id            INTEGER PRIMARY KEY AUTOINCREMENT,
				pathway    TEXT    NOT NULL REFERENCES pathways(name) ON DELETE CASCADE,
				provider      TEXT    NOT NULL,
				model         TEXT    NOT NULL,
				endpoint      TEXT    NOT NULL DEFAULT '',
				deployment    TEXT    NOT NULL DEFAULT '',
				region        TEXT    NOT NULL DEFAULT '',
				keyring_ref   TEXT    NOT NULL,
				priority      INTEGER NOT NULL DEFAULT 0,
				timeout_total TEXT    NOT NULL DEFAULT '',
				timeout_idle  TEXT    NOT NULL DEFAULT ''
			)`,
			`CREATE INDEX idx_targets_pathway ON targets(pathway)`,
			// Credentials: references only (env:/file:/vault:) — never values.
			`CREATE TABLE credentials (
				name            TEXT PRIMARY KEY,
				secret_ref      TEXT NOT NULL,
				next_secret_ref TEXT NOT NULL DEFAULT ''
			)`,
			// Callers: identity + hashed secret (never the secret itself).
			`CREATE TABLE callers (
				key_id      TEXT PRIMARY KEY,
				caller_id   TEXT NOT NULL,
				name        TEXT NOT NULL DEFAULT '',
				secret_hash TEXT NOT NULL
			)`,
			// Deny-by-default allowlist: (key_id, pathway) grants.
			`CREATE TABLE caller_allow (
				key_id     TEXT NOT NULL REFERENCES callers(key_id) ON DELETE CASCADE,
				pathway TEXT NOT NULL,
				PRIMARY KEY (key_id, pathway)
			)`,
			// Permeability policy: per-pathway logical field allowlists, stored
			// as newline-delimited field lists (opaque to the store).
			`CREATE TABLE permeability (
				pathway        TEXT PRIMARY KEY,
				outbound_allow    TEXT NOT NULL DEFAULT '',
				inbound_default   TEXT NOT NULL DEFAULT '',
				inbound_on_reveal TEXT NOT NULL DEFAULT ''
			)`,
		},
	},
	{
		version: 2,
		stmts: []string{
			// Sealed secret store: ENCRYPTED values (ciphertext + nonce) produced
			// by internal/secrets. Unlike credentials (references) and callers
			// (hashes), this table holds opaque encrypted bytes — never plaintext.
			// The master key lives outside the database, so a stolen db file alone
			// does not reveal any secret. name matches credential/keyring naming so
			// a credential may reference a value as `secret:<name>`.
			`CREATE TABLE secrets (
				name        TEXT PRIMARY KEY,
				ciphertext  BLOB    NOT NULL,
				nonce       BLOB    NOT NULL,
				key_version INTEGER NOT NULL DEFAULT 1,
				version     INTEGER NOT NULL DEFAULT 1,
				updated_at  TEXT    NOT NULL DEFAULT ''
			)`,
		},
	},
	{
		version: 3,
		stmts: []string{
			// Providers are first-class: a provider owns everything needed to
			// reach a vendor (family + connection facts + the sealed-key
			// reference). Connection config no longer lives on the pathway target.
			// secret_ref is a reference only (secret:/env:/file:/vault:); the
			// sealed value lives in the secrets table.
			`CREATE TABLE providers (
				name       TEXT PRIMARY KEY,
				family     TEXT NOT NULL,
				endpoint   TEXT NOT NULL DEFAULT '',
				deployment TEXT NOT NULL DEFAULT '',
				region     TEXT NOT NULL DEFAULT '',
				model      TEXT NOT NULL,
				secret_ref TEXT NOT NULL
			)`,
			// A pathway target now only names the provider it routes to. No
			// override, no fallback: one pathway → one provider. (Dev-only
			// clean break: the old fat target table is dropped.)
			`DROP TABLE targets`,
			`CREATE TABLE targets (
				id       INTEGER PRIMARY KEY AUTOINCREMENT,
				pathway  TEXT NOT NULL REFERENCES pathways(name) ON DELETE CASCADE,
				provider TEXT NOT NULL REFERENCES providers(name)
			)`,
			`CREATE INDEX idx_targets_pathway ON targets(pathway)`,
		},
	},
	{
		version: 4,
		stmts: []string{
			// Providers gain an auth mode and an admin-controlled model policy.
			// auth_mode selects how the Exterior authenticates to the vendor:
			//   api_key              - static sealed key (secret_ref required)
			//   aws_default_chain    - ambient AWS IAM (role/instance/IRSA); no secret
			//   aws_assume_role      - AssumeRole(role_arn[, external_id]); no secret
			//   azure_managed_identity - Entra managed identity bearer token; no secret
			// models is the newline-delimited allowlist of models a developer may
			// use through this provider; default_model (one of them) is used when a
			// request names no model. The single `model` column is replaced.
			// (Dev-only clean break: the provider/target tables are recreated.)
			`DROP TABLE targets`,
			`DROP TABLE providers`,
			`CREATE TABLE providers (
				name          TEXT PRIMARY KEY,
				family        TEXT NOT NULL,
				auth_mode     TEXT NOT NULL DEFAULT 'api_key',
				role_arn      TEXT NOT NULL DEFAULT '',
				external_id   TEXT NOT NULL DEFAULT '',
				endpoint      TEXT NOT NULL DEFAULT '',
				deployment    TEXT NOT NULL DEFAULT '',
				region        TEXT NOT NULL DEFAULT '',
				models        TEXT NOT NULL DEFAULT '',
				default_model TEXT NOT NULL DEFAULT '',
				secret_ref    TEXT NOT NULL DEFAULT ''
			)`,
			`CREATE TABLE targets (
				id       INTEGER PRIMARY KEY AUTOINCREMENT,
				pathway  TEXT NOT NULL REFERENCES pathways(name) ON DELETE CASCADE,
				provider TEXT NOT NULL REFERENCES providers(name)
			)`,
			`CREATE INDEX idx_targets_pathway ON targets(pathway)`,
		},
	},
	{
		version: 5,
		stmts: []string{
			// A pathway is now an admin-attached SET of providers (one marked
			// default) plus a cascade_allowed flag. When cascade_allowed is true a
			// caller may supply its own ordered provider+model cascade at request
			// time, restricted to this allowlist; otherwise the default provider's
			// default model is used. The single-target `targets` table and the
			// vestigial `strategy` column are retired. (Dev-only clean break.)
			`DROP TABLE targets`,
			`DROP TABLE pathways`,
			`CREATE TABLE pathways (
				name             TEXT PRIMARY KEY,
				cascade_allowed  INTEGER NOT NULL DEFAULT 0,
				default_provider TEXT NOT NULL DEFAULT ''
			)`,
			`CREATE TABLE pathway_providers (
				pathway  TEXT NOT NULL REFERENCES pathways(name) ON DELETE CASCADE,
				provider TEXT NOT NULL REFERENCES providers(name),
				PRIMARY KEY (pathway, provider)
			)`,
			`CREATE INDEX idx_pathway_providers_pathway ON pathway_providers(pathway)`,
		},
	},
	{
		version: 6,
		stmts: []string{
			// A pathway now declares the OPERATION it serves: the kind of
			// egress work (chat, embeddings, rerank,
			// responses, image_generation, audio_transcription, audio_speech,
			// moderations). Existing rows default to 'chat', so no migration of
			// data is required and prior behavior is preserved.
			`ALTER TABLE pathways ADD COLUMN operation TEXT NOT NULL DEFAULT 'chat'`,
		},
	},
	{
		version: 7,
		stmts: []string{
			// Providers gain project/location for the Gemini Vertex AI surface:
			// the GCP project and region that scope the Vertex
			// endpoint, paired with the gcp_application_default (ambient OAuth)
			// auth mode. Existing rows default to '' (AI Studio / non-Gemini
			// families), so no data migration is required and prior behavior is
			// preserved.
			`ALTER TABLE providers ADD COLUMN project TEXT NOT NULL DEFAULT ''`,
			`ALTER TABLE providers ADD COLUMN location TEXT NOT NULL DEFAULT ''`,
		},
	},
}

// migrate brings the schema up to the latest version inside a single transaction per
// step. schema_meta holds the single-row current version.
func (s *Store) migrate() error {
	if _, err := s.db.Exec(
		`CREATE TABLE IF NOT EXISTS schema_meta (version INTEGER NOT NULL)`,
	); err != nil {
		return fmt.Errorf("ensure schema_meta: %w", err)
	}

	cur, err := s.schemaVersion()
	if err != nil {
		return err
	}

	for _, m := range migrations {
		if m.version <= cur {
			continue
		}
		if err := s.applyStep(m); err != nil {
			return fmt.Errorf("apply migration %d: %w", m.version, err)
		}
		cur = m.version
	}
	return nil
}

// schemaVersion returns the current schema version (0 when unset/fresh).
func (s *Store) schemaVersion() (int, error) {
	var v sql.NullInt64
	err := s.db.QueryRow(`SELECT version FROM schema_meta LIMIT 1`).Scan(&v)
	switch {
	case err == sql.ErrNoRows:
		return 0, nil
	case err != nil:
		return 0, fmt.Errorf("read schema version: %w", err)
	}
	return int(v.Int64), nil
}

// applyStep runs one migration's statements and records the new version
// atomically; any failure rolls the whole step back.
func (s *Store) applyStep(m migration) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	for _, stmt := range m.stmts {
		if _, err := tx.Exec(stmt); err != nil {
			return fmt.Errorf("statement: %w", err)
		}
	}
	// Single-row schema_meta: delete-then-insert keeps it to one row.
	if _, err := tx.Exec(`DELETE FROM schema_meta`); err != nil {
		return err
	}
	if _, err := tx.Exec(`INSERT INTO schema_meta (version) VALUES (?)`, m.version); err != nil {
		return err
	}
	return tx.Commit()
}

// Version returns the store's current schema version. Useful for health/readiness
// surfaces and tests.
func (s *Store) Version() (int, error) { return s.schemaVersion() }
