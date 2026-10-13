package store

import "database/sql"

// This file is the write path used by the console editor. Each writer replaces
// all rows for one config kind inside a single transaction, so a partially
// applied edit is never observable. Secrets are never written: credentials hold
// references, callers hold hashes. The serving Snapshot is rebuilt from the
// store by an explicit reload after a successful write.

// WritePathways replaces all pathways and their targets.
func (s *Store) WritePathways(rows []PathwayRow) error {
	return s.inTx(func(tx *sql.Tx) error { return writePathwaysTx(tx, rows) })
}

// WriteCredentials replaces all credential references.
func (s *Store) WriteCredentials(rows []CredentialRow) error {
	return s.inTx(func(tx *sql.Tx) error { return writeCredentialsTx(tx, rows) })
}

// WriteProviders replaces all provider definitions (references only; the sealed
// values in the secrets table are untouched).
func (s *Store) WriteProviders(rows []ProviderRow) error {
	return s.inTx(func(tx *sql.Tx) error { return writeProvidersTx(tx, rows) })
}

// WriteCallers replaces the entire Caller Key store (callers + allowlists). It
// writes hashes only (never a secret). Used by the raw-YAML editor path; the
// structured allow-only edit uses SetCallerAllow so hashes need never be typed.
func (s *Store) WriteCallers(rows []CallerRow) error {
	return s.inTx(func(tx *sql.Tx) error { return writeCallersTx(tx, rows) })
}

// WritePermeability replaces the entire permeability policy.
func (s *Store) WritePermeability(rows []PermeabilityRow) error {
	return s.inTx(func(tx *sql.Tx) error { return writePermeabilityTx(tx, rows) })
}

// WriteBundle replaces all four kinds in a single transaction, so a declarative
// apply is all-or-nothing: a partially applied bundle is never observable, and a
// failure leaves the store exactly as it was. The serving Snapshot is rebuilt by
// an explicit reload after a successful write.
func (s *Store) WriteBundle(b Bundle) error {
	return s.inTx(func(tx *sql.Tx) error {
		// Clear pathways first (cascades to targets) so replacing providers does
		// not trip the targets→providers foreign key, then write providers (a
		// target's provider FK must exist before the target is inserted).
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

// --- tx-scoped per-kind writers (shared by the single-kind and bundle paths) --

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

// SetCallerAllow replaces the allowlist for one existing caller (matched by
// key_id). It never touches the caller's hash, caller_id, or name — so no
// secret-derived value is required from or exposed to the editor. It returns
// ErrUnknownKeyID when the key_id does not exist.
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

// inTx runs fn in a transaction, committing on success and rolling back on error.
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

// defaultAuthMode defaults a blank auth mode to api_key (the legacy/static-key
// behavior), so older rows and callers that omit the field remain valid.
func defaultAuthMode(s string) string {
	if s == "" {
		return "api_key"
	}
	return s
}
