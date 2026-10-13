package store

import "database/sql"

// This file is the atomic "provider" write path used by the Console's Providers
// screen. A provider pairs a sealed secret value with the provider definition
// (family + connection facts + the `secret:<name>` reference that points at the
// sealed value). Both land in a SINGLE transaction so an operator never observes
// a provider row that references a not-yet-sealed value (or a sealed value with
// no provider). The store still stores ciphertext only — sealing happens in
// internal/secrets before the row reaches here.

// ProviderUpsert carries the two rows written atomically for one provider: the
// sealed SecretRow (ciphertext + nonce, never plaintext) and the ProviderRow
// that references it. The caller guarantees Provider.SecretRef is
// "secret:<Secret.Name>".
type ProviderUpsert struct {
	Secret   SecretRow
	Provider ProviderRow
}

// UpsertProvider seals-in a provider: it upserts the sealed secret (version bump)
// and the provider definition in one transaction. A failure leaves the store
// exactly as it was.
func (s *Store) UpsertProvider(p ProviderUpsert) error {
	return s.inTx(func(tx *sql.Tx) error {
		// Ambient-auth providers (aws_default_chain / aws_assume_role /
		// azure_managed_identity) carry no sealed value; only seal when a secret
		// is present (api_key auth).
		if p.Secret.Name != "" {
			if _, err := putSecretTx(tx, p.Secret); err != nil {
				return err
			}
		}
		return upsertProviderTx(tx, p.Provider)
	})
}

// upsertProviderTx inserts or updates a single provider definition by name,
// within an existing transaction. Unlike writeProvidersTx (replace-all), this is
// a targeted merge used by the Console provider path so other providers are left
// untouched.
func upsertProviderTx(tx *sql.Tx, p ProviderRow) error {
	_, err := tx.Exec(
		`INSERT INTO providers
		 (name, family, auth_mode, role_arn, external_id,
		  endpoint, deployment, region, project, location, models, default_model, secret_ref)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(name) DO UPDATE SET
		   family        = excluded.family,
		   auth_mode     = excluded.auth_mode,
		   role_arn      = excluded.role_arn,
		   external_id   = excluded.external_id,
		   endpoint      = excluded.endpoint,
		   deployment    = excluded.deployment,
		   region        = excluded.region,
		   project       = excluded.project,
		   location      = excluded.location,
		   models        = excluded.models,
		   default_model = excluded.default_model,
		   secret_ref    = excluded.secret_ref`,
		p.Name, p.Family, defaultAuthMode(p.AuthMode), p.RoleARN, p.ExternalID,
		p.Endpoint, p.Deployment, p.Region, p.Project, p.Location, JoinList(p.Models), p.DefaultModel, p.SecretRef)
	return err
}

// DeleteProvider removes a provider's definition and its sealed secret in one
// transaction, reporting whether anything was removed. It does NOT check for
// dangling target references; callers (the adapter) refuse the delete first when
// a pathway target still references the provider, so routing never dangles.
func (s *Store) DeleteProvider(name string) (bool, error) {
	var removed bool
	err := s.inTx(func(tx *sql.Tx) error {
		res, err := tx.Exec(`DELETE FROM providers WHERE name = ?`, name)
		if err != nil {
			return err
		}
		n, err := res.RowsAffected()
		if err != nil {
			return err
		}
		removed = n > 0
		if _, err := tx.Exec(`DELETE FROM secrets WHERE name = ?`, name); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		return false, err
	}
	return removed, nil
}
