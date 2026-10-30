package store

import "database/sql"

// The secrets table holds ciphertext and nonces produced by internal/secrets.
// The store treats them as opaque bytes and never encrypts or decrypts.

// SecretRow is one sealed secret: the ciphertext, the nonce and key version
// needed to open it, an increasing value version, and an update timestamp.
type SecretRow struct {
	Name       string
	Ciphertext []byte
	Nonce      []byte
	KeyVersion int
	Version    int
	UpdatedAt  string
}

// SecretMeta is the listing view of a secret, without ciphertext or nonce.
type SecretMeta struct {
	Name       string
	KeyVersion int
	Version    int
	UpdatedAt  string
}

// PutSecret upserts a sealed secret, bumps its version in the same
// transaction, and returns the new version.
func (s *Store) PutSecret(row SecretRow) (int, error) {
	var version int
	err := s.inTx(func(tx *sql.Tx) error {
		v, err := putSecretTx(tx, row)
		version = v
		return err
	})
	if err != nil {
		return 0, err
	}
	return version, nil
}

// putSecretTx is PutSecret within an existing transaction, so UpsertProvider
// can write the secret and the provider together.
func putSecretTx(tx *sql.Tx, row SecretRow) (int, error) {
	var version int
	if err := tx.QueryRow(
		`SELECT COALESCE(MAX(version), 0) FROM secrets WHERE name = ?`, row.Name,
	).Scan(&version); err != nil {
		return 0, err
	}
	version++
	if _, err := tx.Exec(
		`INSERT INTO secrets (name, ciphertext, nonce, key_version, version, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?)
		 ON CONFLICT(name) DO UPDATE SET
		   ciphertext  = excluded.ciphertext,
		   nonce       = excluded.nonce,
		   key_version = excluded.key_version,
		   version     = excluded.version,
		   updated_at  = excluded.updated_at`,
		row.Name, row.Ciphertext, row.Nonce, row.KeyVersion, version, row.UpdatedAt); err != nil {
		return 0, err
	}
	return version, nil
}

// GetSecret returns the sealed row for a name and whether it exists.
func (s *Store) GetSecret(name string) (SecretRow, bool, error) {
	r := SecretRow{Name: name}
	err := s.db.QueryRow(
		`SELECT ciphertext, nonce, key_version, version, updated_at
		 FROM secrets WHERE name = ?`, name,
	).Scan(&r.Ciphertext, &r.Nonce, &r.KeyVersion, &r.Version, &r.UpdatedAt)
	switch {
	case err == sql.ErrNoRows:
		return SecretRow{}, false, nil
	case err != nil:
		return SecretRow{}, false, err
	}
	return r, true, nil
}

// ListSecretMeta returns secret metadata ordered by name.
func (s *Store) ListSecretMeta() ([]SecretMeta, error) {
	rows, err := s.db.Query(
		`SELECT name, key_version, version, updated_at FROM secrets ORDER BY name`)
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

// DeleteSecret removes a sealed secret, reporting whether a row was deleted.
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

// HasSecret reports whether a sealed secret exists under name.
func (s *Store) HasSecret(name string) (bool, error) {
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM secrets WHERE name = ?`, name).Scan(&n); err != nil {
		return false, err
	}
	return n > 0, nil
}
