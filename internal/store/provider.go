package store

import "database/sql"

// A provider definition and its sealed secret are written in one transaction,
// so a provider never references a secret that has not been stored yet.

// ProviderUpsert holds the two rows written together for one provider: the
// sealed SecretRow and the ProviderRow that references it. The caller sets
// Provider.SecretRef to "secret:<Secret.Name>".
type ProviderUpsert struct {
	Secret   SecretRow
	Provider ProviderRow
}

// UpsertProvider upserts the sealed secret, bumping its version, and the
// provider definition in one transaction.
func (s *Store) UpsertProvider(p ProviderUpsert) error {
	return s.inTx(func(tx *sql.Tx) error {
		// Providers using ambient auth have no sealed secret.
		if p.Secret.Name != "" {
			if _, err := putSecretTx(tx, p.Secret); err != nil {
				return err
			}
		}
		return upsertProviderTx(tx, p.Provider)
	})
}

// upsertProviderTx inserts or updates one provider by name. Unlike
// writeProvidersTx, it leaves other providers untouched.
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

// DeleteProvider removes a provider and its sealed secret in one transaction
// and reports whether a provider was removed. It does not check whether a
// pathway still references the provider; callers must do that first.
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
