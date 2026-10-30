package store

import "strings"

// The row types below mirror the stored state. They hold secret references and
// hashes, never secret values. The app package builds the serving Snapshot
// from them.

// PathwayRow is one routing pathway: the providers it may route to, the
// default provider used when the caller sends no cascade, and whether a caller
// may send its own provider and model cascade.
type PathwayRow struct {
	Name            string
	Operation       string
	CascadeAllowed  bool
	DefaultProvider string
	Providers       []string
}

// ProviderRow is one configured provider: its vendor family, auth mode
// (RoleARN and ExternalID apply to aws_assume_role), connection settings,
// allowed Models with a DefaultModel, and for api_key auth a SecretRef. The
// sealed value itself lives in the secrets table.
type ProviderRow struct {
	Name         string
	Family       string
	AuthMode     string
	RoleARN      string
	ExternalID   string
	Endpoint     string
	Deployment   string
	Region       string
	Project      string
	Location     string
	Models       []string
	DefaultModel string
	SecretRef    string
}

// CredentialRow is a named secret reference.
type CredentialRow struct {
	Name          string
	SecretRef     string
	NextSecretRef string
}

// CallerRow is a Caller Key record: identity, secret hash and allowlist.
type CallerRow struct {
	KeyID      string
	CallerID   string
	Name       string
	SecretHash string
	Allow      []string
}

// PermeabilityRow is one pathway's per-direction field allowlists.
type PermeabilityRow struct {
	Pathway         string
	OutboundAllow   []string
	InboundDefault  []string
	InboundOnReveal []string
}

// ReadPathways returns pathways ordered by name, each with its providers
// ordered by name.
func (s *Store) ReadPathways() ([]PathwayRow, error) {
	rows, err := s.db.Query(`SELECT name, operation, cascade_allowed, default_provider FROM pathways ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var caps []PathwayRow
	for rows.Next() {
		var (
			c       PathwayRow
			cascade int
		)
		if err := rows.Scan(&c.Name, &c.Operation, &cascade, &c.DefaultProvider); err != nil {
			return nil, err
		}
		c.CascadeAllowed = cascade != 0
		caps = append(caps, c)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	for i := range caps {
		ps, err := s.pathwayProvidersFor(caps[i].Name)
		if err != nil {
			return nil, err
		}
		caps[i].Providers = ps
	}
	return caps, nil
}

func (s *Store) pathwayProvidersFor(pathway string) ([]string, error) {
	rows, err := s.db.Query(
		`SELECT provider FROM pathway_providers WHERE pathway = ? ORDER BY provider`, pathway)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []string
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// ReadProviders returns configured providers ordered by name.
func (s *Store) ReadProviders() ([]ProviderRow, error) {
	rows, err := s.db.Query(
		`SELECT name, family, auth_mode, role_arn, external_id,
		        endpoint, deployment, region, project, location, models, default_model, secret_ref
		 FROM providers ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []ProviderRow
	for rows.Next() {
		var (
			p      ProviderRow
			models string
		)
		if err := rows.Scan(&p.Name, &p.Family, &p.AuthMode, &p.RoleARN, &p.ExternalID,
			&p.Endpoint, &p.Deployment, &p.Region, &p.Project, &p.Location, &models, &p.DefaultModel, &p.SecretRef); err != nil {
			return nil, err
		}
		p.Models = splitList(models)
		out = append(out, p)
	}
	return out, rows.Err()
}

// ReadCredentials returns credential references ordered by name.
func (s *Store) ReadCredentials() ([]CredentialRow, error) {
	rows, err := s.db.Query(
		`SELECT name, secret_ref, next_secret_ref FROM credentials ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []CredentialRow
	for rows.Next() {
		var c CredentialRow
		if err := rows.Scan(&c.Name, &c.SecretRef, &c.NextSecretRef); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// ReadCallers returns Caller Key records (ordered by key_id) with allowlists.
func (s *Store) ReadCallers() ([]CallerRow, error) {
	rows, err := s.db.Query(
		`SELECT key_id, caller_id, name, secret_hash FROM callers ORDER BY key_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []CallerRow
	for rows.Next() {
		var c CallerRow
		if err := rows.Scan(&c.KeyID, &c.CallerID, &c.Name, &c.SecretHash); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	for i := range out {
		allow, err := s.allowFor(out[i].KeyID)
		if err != nil {
			return nil, err
		}
		out[i].Allow = allow
	}
	return out, nil
}

func (s *Store) allowFor(keyID string) ([]string, error) {
	rows, err := s.db.Query(
		`SELECT pathway FROM caller_allow WHERE key_id = ? ORDER BY pathway`, keyID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []string
	for rows.Next() {
		var c string
		if err := rows.Scan(&c); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// ReadPermeability returns the permeability policy rows ordered by pathway.
func (s *Store) ReadPermeability() ([]PermeabilityRow, error) {
	rows, err := s.db.Query(
		`SELECT pathway, outbound_allow, inbound_default, inbound_on_reveal
		 FROM permeability ORDER BY pathway`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []PermeabilityRow
	for rows.Next() {
		var (
			cap               string
			outb, def, reveal string
		)
		if err := rows.Scan(&cap, &outb, &def, &reveal); err != nil {
			return nil, err
		}
		out = append(out, PermeabilityRow{
			Pathway:         cap,
			OutboundAllow:   splitList(outb),
			InboundDefault:  splitList(def),
			InboundOnReveal: splitList(reveal),
		})
	}
	return out, rows.Err()
}

// IsConfigured reports whether any pathway exists. The daemon uses it to tell
// an empty store from a configured one.
func (s *Store) IsConfigured() (bool, error) {
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM pathways`).Scan(&n); err != nil {
		return false, err
	}
	return n > 0, nil
}

// splitList parses the newline-delimited storage form, dropping empty
// entries. An empty value yields nil.
func splitList(s string) []string {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	parts := strings.Split(s, "\n")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// JoinList is the inverse of splitList: it joins items into the
// newline-delimited storage form.
func JoinList(items []string) string { return strings.Join(items, "\n") }
