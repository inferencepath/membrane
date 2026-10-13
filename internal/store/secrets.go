package store

import "database/sql"

// This file is the sealed-secret repository: the typed read/write path over the
// `secrets` table (created in migration v2). The store persists ENCRYPTED bytes
// only — ciphertext + nonce produced by internal/secrets — never plaintext. A
// credential references a sealed value as `secret:<name>`; the Exterior resolves
// it through internal/secrets, which is the sole reader of these rows.

// SecretRow is one sealed secret as stored: the opaque ciphertext + nonce, the
// master-key version that sealed it, a monotonically increasing value Version
// bumped on each rotation, and the last-updated timestamp. It never carries
// plaintext.
type SecretRow struct {
	Name       string
	Ciphertext []byte
	Nonce      []byte
	KeyVersion int
	Version    int
	UpdatedAt  string
}

// SecretMeta is the value-free projection of a sealed secret: its name, key and
// value versions, and when it was last updated. It is what the Console lists so
// an operator can see what exists without ever reading a value.
type SecretMeta struct {
	Name       string
	KeyVersion int
	Version    int
	UpdatedAt  string
}

// PutSecret creates or rotates the sealed secret named row.Name, returning the
// resulting value version (1 on first insert, then bumped on each upsert). The
// supplied row.Version is ignored — the store owns version assignment.
func (s *Store) PutSecret(row SecretRow) (int, error) {
	var v int
	err := s.inTx(func(tx *sql.Tx) error {
		n, err := putSecretTx(tx, row)
		v = n
		return err
	})
	return v, err
}

// putSecretTx upserts a sealed secret within an existing transaction and returns
// the resulting value version. A first insert starts at version 1; a conflicting
// name updates the sealed bytes in place and bumps version by one, keeping a
// single logical row per name. Shared by PutSecret and the atomic provider path.
func putSecretTx(tx *sql.Tx, row SecretRow) (int, error) {
	if _, err := tx.Exec(
		`INSERT INTO secrets (name, ciphertext, nonce, key_version, version, updated_at)
		 VALUES (?, ?, ?, ?, 1, ?)
		 ON CONFLICT(name) DO UPDATE SET
		   ciphertext  = excluded.ciphertext,
		   nonce       = excluded.nonce,
		   key_version = excluded.key_version,
		   updated_at  = excluded.updated_at,
		   version     = secrets.version + 1`,
		row.Name, row.Ciphertext, row.Nonce, row.KeyVersion, row.UpdatedAt,
	); err != nil {
		return 0, err
	}
	var v int
	if err := tx.QueryRow(`SELECT version FROM secrets WHERE name = ?`, row.Name).Scan(&v); err != nil {
		return 0, err
	}
	return v, nil
}

// GetSecret returns the sealed secret named name. ok is false (with a nil error)
// when no such secret exists.
func (s *Store) GetSecret(name string) (SecretRow, bool, error) {
	row := SecretRow{Name: name}
	err := s.db.QueryRow(
		`SELECT ciphertext, nonce, key_version, version, updated_at
		 FROM secrets WHERE name = ?`, name,
	).Scan(&row.Ciphertext, &row.Nonce, &row.KeyVersion, &row.Version, &row.UpdatedAt)
	switch {
	case err == sql.ErrNoRows:
		return SecretRow{}, false, nil
	case err != nil:
		return SecretRow{}, false, err
	}
	return row, true, nil
}

// HasSecret reports whether a sealed secret exists under name.
func (s *Store) HasSecret(name string) (bool, error) {
	var one int
	err := s.db.QueryRow(`SELECT 1 FROM secrets WHERE name = ?`, name).Scan(&one)
	switch {
	case err == sql.ErrNoRows:
		return false, nil
	case err != nil:
		return false, err
	}
	return true, nil
}

// ListSecretMeta returns metadata for every sealed secret, ordered by name. It
// never selects or returns the ciphertext/nonce, so no value can leak through it.
func (s *Store) ListSecretMeta() ([]SecretMeta, error) {
	rows, err := s.db.Query(
		`SELECT name, key_version, version, updated_at FROM secrets ORDER BY name`,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []SecretMeta
	for rows.Next() {
		var m SecretMeta
		if err := rows.Scan(&m.Name, &m.KeyVersion, &m.Version, &m.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// DeleteSecret removes the sealed secret named name, reporting whether a row was
// removed (false when nothing matched).
func (s *Store) DeleteSecret(name string) (bool, error) {
	res, err := s.db.Exec(`DELETE FROM secrets WHERE name = ?`, name)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	return n > 0, nil
}
