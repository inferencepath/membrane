package store

import "database/sql"

// Each writer replaces all rows of one kind in a single transaction, so a
// partial edit is never visible. Credentials hold references and callers hold
// hashes; no secret value is written here. The serving Snapshot picks up a
// write only on an explicit reload.

// WritePathways replaces all pathways and their provider sets.
func (s *Store) WritePathways(rows []PathwayRow) error {
	return s.inTx(func(tx *sql.Tx) error { return writePathwaysTx(tx, rows) })
}

// WriteCredentials replaces all credential references.
func (s *Store) WriteCredentials(rows []CredentialRow) error {
	return s.inTx(func(tx *sql.Tx) error { return writeCredentialsTx(tx, rows) })
}

// WriteProviders replaces all provider definitions. Sealed values in the
// secrets table are left alone.
func (s *Store) WriteProviders(rows []ProviderRow) error {
	return s.inTx(func(tx *sql.Tx) error { return writeProvidersTx(tx, rows) })
}

// WriteCallers replaces all Caller Keys and their allowlists. To change only
// an allowlist without supplying hashes, use SetCallerAllow.
func (s *Store) WriteCallers(rows []CallerRow) error {
	return s.inTx(func(tx *sql.Tx) error { return writeCallersTx(tx, rows) })
}

// WritePermeability replaces the entire permeability policy.
func (s *Store) WritePermeability(rows []PermeabilityRow) error {
	return s.inTx(func(tx *sql.Tx) error { return writePermeabilityTx(tx, rows) })
}

// WriteBundle replaces every kind in a single transaction, so a bundle apply
// either succeeds completely or leaves the store unchanged.
func (s *Store) WriteBundle(b Bundle) error {
	return s.inTx(func(tx *sql.Tx) error {
		// Pathways reference providers by foreign key. Delete pathways (and,
		// by cascade, pathway_providers) before replacing providers, and write
		// providers before pathways.
		if _, err := tx.Exec(`DELETE FROM pathways`); err != nil {
			return err
		}
		if err := writeProvidersTx(tx, b.Providers); err != nil {
			return err
		}
		if err := writePathwaysTx(tx, b.Pathways); err != nil {
			return err
		}
		if err := writeCredentialsTx(tx, b.Credentials); err != nil {
			return err
		}
		if err := writeCallersTx(tx, b.Callers); err != nil {
			return err
		}
		return writePermeabilityTx(tx, b.Permeability)
	})
}

// Per-kind writers shared by the single-kind and bundle paths.

func writePathwaysTx(tx *sql.Tx, rows []PathwayRow) error {
	if _, err := tx.Exec(`DELETE FROM pathways`); err != nil { // cascades to pathway_providers
		return err
	}
	for _, c := range rows {
		cascade := 0
		if c.CascadeAllowed {
			cascade = 1
		}
		op := c.Operation
		if op == "" {
			op = "chat"
		}
		if _, err := tx.Exec(
			`INSERT INTO pathways (name, operation, cascade_allowed, default_provider) VALUES (?, ?, ?, ?)`,
			c.Name, op, cascade, c.DefaultProvider); err != nil {
			return err
		}
		for _, p := range c.Providers {
			if _, err := tx.Exec(
				`INSERT INTO pathway_providers (pathway, provider) VALUES (?, ?)`,
				c.Name, p); err != nil {
				return err
			}
		}
	}
	return nil
}

func writeProvidersTx(tx *sql.Tx, rows []ProviderRow) error {
	if _, err := tx.Exec(`DELETE FROM providers`); err != nil {
		return err
	}
	for _, p := range rows {
		if _, err := tx.Exec(
			`INSERT INTO providers
			 (name, family, auth_mode, role_arn, external_id,
			  endpoint, deployment, region, project, location, models, default_model, secret_ref)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			p.Name, p.Family, defaultAuthMode(p.AuthMode), p.RoleARN, p.ExternalID,
			p.Endpoint, p.Deployment, p.Region, p.Project, p.Location, JoinList(p.Models), p.DefaultModel, p.SecretRef); err != nil {
			return err
		}
	}
	return nil
}

func writeCredentialsTx(tx *sql.Tx, rows []CredentialRow) error {
	if _, err := tx.Exec(`DELETE FROM credentials`); err != nil {
		return err
	}
	for _, c := range rows {
		if _, err := tx.Exec(
			`INSERT INTO credentials (name, secret_ref, next_secret_ref) VALUES (?, ?, ?)`,
			c.Name, c.SecretRef, c.NextSecretRef); err != nil {
			return err
		}
	}
	return nil
}

func writeCallersTx(tx *sql.Tx, rows []CallerRow) error {
	if _, err := tx.Exec(`DELETE FROM callers`); err != nil { // cascades to caller_allow
		return err
	}
	for _, c := range rows {
		if _, err := tx.Exec(
			`INSERT INTO callers (key_id, caller_id, name, secret_hash) VALUES (?, ?, ?, ?)`,
			c.KeyID, c.CallerID, c.Name, c.SecretHash); err != nil {
			return err
		}
		for _, a := range c.Allow {
			if _, err := tx.Exec(
				`INSERT INTO caller_allow (key_id, pathway) VALUES (?, ?)`, c.KeyID, a); err != nil {
				return err
			}
		}
	}
	return nil
}

func writePermeabilityTx(tx *sql.Tx, rows []PermeabilityRow) error {
	if _, err := tx.Exec(`DELETE FROM permeability`); err != nil {
		return err
	}
	for _, p := range rows {
		if _, err := tx.Exec(
			`INSERT INTO permeability
			 (pathway, outbound_allow, inbound_default, inbound_on_reveal)
			 VALUES (?, ?, ?, ?)`,
			p.Pathway, JoinList(p.OutboundAllow),
			JoinList(p.InboundDefault), JoinList(p.InboundOnReveal)); err != nil {
			return err
		}
	}
	return nil
}

// SetCallerAllow replaces the allowlist of the caller with the given key_id,
// leaving its hash, caller_id and name unchanged. It returns ErrUnknownKeyID
// if no such caller exists.
func (s *Store) SetCallerAllow(keyID string, allow []string) error {
	return s.inTx(func(tx *sql.Tx) error {
		var exists int
		if err := tx.QueryRow(`SELECT COUNT(*) FROM callers WHERE key_id = ?`, keyID).Scan(&exists); err != nil {
			return err
		}
		if exists == 0 {
			return ErrUnknownKeyID
		}
		if _, err := tx.Exec(`DELETE FROM caller_allow WHERE key_id = ?`, keyID); err != nil {
			return err
		}
		for _, c := range allow {
			if _, err := tx.Exec(
				`INSERT INTO caller_allow (key_id, pathway) VALUES (?, ?)`, keyID, c); err != nil {
				return err
			}
		}
		return nil
	})
}

// inTx runs fn in a transaction and commits if fn returns nil.
func (s *Store) inTx(fn func(*sql.Tx) error) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}

// defaultAuthMode maps a blank auth mode to api_key, so rows and callers that
// omit the field stay valid.
func defaultAuthMode(s string) string {
	if s == "" {
		return "api_key"
	}
	return s
}
