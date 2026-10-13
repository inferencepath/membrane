package store

import (
	"crypto/sha256"

	"gopkg.in/yaml.v3"
)

// This file renders stored rows into the canonical config documents (the exact
// YAML shapes the daemon's parsers accept). Rendering lives here so there is a
// single source of truth for row→document mapping, shared by the snapshot
// builder (app) and the console editor (consolestore). Secrets remain by
// reference; nothing here resolves a secret value.

// Editable kinds, named once.
const (
	KindProviders    = "providers"
	KindPathways     = "pathways"
	KindCredentials  = "credentials"
	KindCallers      = "callers"
	KindPermeability = "permeability"
)

type permDocYAML struct {
	Version  int                     `yaml:"version"`
	Pathways map[string]permDirsYAML `yaml:"pathways"`
}

type permDirsYAML struct {
	Outbound permOutYAML `yaml:"outbound"`
	Inbound  permInYAML  `yaml:"inbound"`
}

type permOutYAML struct {
	Allow []string `yaml:"allow"`
}

type permInYAML struct {
	Default  []string `yaml:"default"`
	OnReveal []string `yaml:"on_reveal,omitempty"`
}

// RenderPermeability renders the stored permeability policy as canonical YAML.
func (s *Store) RenderPermeability() ([]byte, error) {
	rows, err := s.ReadPermeability()
	if err != nil {
		return nil, err
	}
	return RenderPermeabilityDoc(rows)
}

// RenderPermeabilityDoc renders the given permeability rows as canonical YAML.
// It operates on candidate rows (not the stored ones) so the editor can validate
// a proposed edit with the daemon's parser before committing it.
func RenderPermeabilityDoc(rows []PermeabilityRow) ([]byte, error) {
	doc := permDocYAML{Version: 1, Pathways: make(map[string]permDirsYAML, len(rows))}
	for _, r := range rows {
		doc.Pathways[r.Pathway] = permDirsYAML{
			Outbound: permOutYAML{Allow: r.OutboundAllow},
			Inbound:  permInYAML{Default: r.InboundDefault, OnReveal: r.InboundOnReveal},
		}
	}
	return yaml.Marshal(doc)
}

type callerDocYAML struct {
	Version int               `yaml:"version"`
	Callers []callerEntryYAML `yaml:"callers"`
}

type callerEntryYAML struct {
	KeyID    string   `yaml:"key_id"`
	Hash     string   `yaml:"hash"`
	CallerID string   `yaml:"caller_id"`
	Name     string   `yaml:"name,omitempty"`
	Allow    []string `yaml:"allow,omitempty"`
}

// RenderCallers renders the stored Caller Key store as canonical YAML.
func (s *Store) RenderCallers() ([]byte, error) {
	rows, err := s.ReadCallers()
	if err != nil {
		return nil, err
	}
	return RenderCallersDoc(rows)
}

// RenderCallersDoc renders the given caller rows as canonical YAML (hashes only).
func RenderCallersDoc(rows []CallerRow) ([]byte, error) {
	doc := callerDocYAML{Version: 1}
	for _, r := range rows {
		doc.Callers = append(doc.Callers, callerEntryYAML{
			KeyID: r.KeyID, Hash: r.SecretHash, CallerID: r.CallerID,
			Name: r.Name, Allow: r.Allow,
		})
	}
	return yaml.Marshal(doc)
}

type credDocYAML struct {
	Version int             `yaml:"version"`
	Keyring []credEntryYAML `yaml:"keyring"`
}

type credEntryYAML struct {
	Name          string `yaml:"name"`
	SecretRef     string `yaml:"secret_ref"`
	NextSecretRef string `yaml:"next_secret_ref,omitempty"`
}

// RenderCredentials renders stored credential references as canonical YAML.
func (s *Store) RenderCredentials() ([]byte, error) {
	rows, err := s.ReadCredentials()
	if err != nil {
		return nil, err
	}
	return RenderCredentialsDoc(rows)
}

// RenderCredentialsDoc renders the given credential rows as canonical YAML (no
// values — references only).
func RenderCredentialsDoc(rows []CredentialRow) ([]byte, error) {
	doc := credDocYAML{Version: 1}
	for _, r := range rows {
		doc.Keyring = append(doc.Keyring, credEntryYAML{
			Name: r.Name, SecretRef: r.SecretRef, NextSecretRef: r.NextSecretRef,
		})
	}
	return yaml.Marshal(doc)
}

type capsDocYAML struct {
	Version  int             `yaml:"version"`
	Pathways []capsRouteYAML `yaml:"pathways"`
}

type capsRouteYAML struct {
	Pathway         string   `yaml:"pathway"`
	Operation       string   `yaml:"operation,omitempty"`
	CascadeAllowed  bool     `yaml:"cascade_allowed"`
	DefaultProvider string   `yaml:"default_provider"`
	Providers       []string `yaml:"providers"`
}

// capsRouteFromRow / capsRowFromRoute map between the stored PathwayRow and its
// canonical YAML element, shared by the pathways doc and the bundle. The
// operation is omitted from the rendered YAML when it is the default ("chat" or
// empty), keeping existing bundles byte-identical and migration-free.
func capsRouteFromRow(c PathwayRow) capsRouteYAML {
	op := c.Operation
	if op == "chat" {
		op = ""
	}
	return capsRouteYAML{
		Pathway: c.Name, Operation: op, CascadeAllowed: c.CascadeAllowed,
		DefaultProvider: c.DefaultProvider, Providers: c.Providers,
	}
}

func capsRowFromRoute(r capsRouteYAML) PathwayRow {
	op := r.Operation
	if op == "" {
		op = "chat"
	}
	return PathwayRow{
		Name: r.Pathway, Operation: op, CascadeAllowed: r.CascadeAllowed,
		DefaultProvider: r.DefaultProvider, Providers: r.Providers,
	}
}

type provDocYAML struct {
	Version   int             `yaml:"version"`
	Providers []provEntryYAML `yaml:"providers"`
}

type provEntryYAML struct {
	Name         string   `yaml:"name"`
	Family       string   `yaml:"family"`
	AuthMode     string   `yaml:"auth_mode,omitempty"`
	RoleARN      string   `yaml:"role_arn,omitempty"`
	ExternalID   string   `yaml:"external_id,omitempty"`
	Models       []string `yaml:"models"`
	DefaultModel string   `yaml:"default_model"`
	Endpoint     string   `yaml:"endpoint,omitempty"`
	Deployment   string   `yaml:"deployment,omitempty"`
	Region       string   `yaml:"region,omitempty"`
	Project      string   `yaml:"project,omitempty"`
	Location     string   `yaml:"location,omitempty"`
	SecretRef    string   `yaml:"secret_ref,omitempty"`
}

// provEntryFromRow maps a stored ProviderRow to its canonical YAML element,
// shared by the stand-alone providers doc and the bundle.
func provEntryFromRow(p ProviderRow) provEntryYAML {
	return provEntryYAML{
		Name: p.Name, Family: p.Family, AuthMode: p.AuthMode,
		RoleARN: p.RoleARN, ExternalID: p.ExternalID,
		Models: p.Models, DefaultModel: p.DefaultModel,
		Endpoint: p.Endpoint, Deployment: p.Deployment, Region: p.Region,
		Project: p.Project, Location: p.Location,
		SecretRef: p.SecretRef,
	}
}

// provRowFromEntry is the inverse of provEntryFromRow.
func provRowFromEntry(p provEntryYAML) ProviderRow {
	return ProviderRow{
		Name: p.Name, Family: p.Family, AuthMode: p.AuthMode,
		RoleARN: p.RoleARN, ExternalID: p.ExternalID,
		Models: p.Models, DefaultModel: p.DefaultModel,
		Endpoint: p.Endpoint, Deployment: p.Deployment, Region: p.Region,
		Project: p.Project, Location: p.Location,
		SecretRef: p.SecretRef,
	}
}

// RenderProviders renders stored provider definitions as canonical YAML.
func (s *Store) RenderProviders() ([]byte, error) {
	rows, err := s.ReadProviders()
	if err != nil {
		return nil, err
	}
	return RenderProvidersDoc(rows)
}

// RenderProvidersDoc renders the given provider rows as canonical YAML
// (references only — never a sealed value).
func RenderProvidersDoc(rows []ProviderRow) ([]byte, error) {
	doc := provDocYAML{Version: 1}
	for _, p := range rows {
		doc.Providers = append(doc.Providers, provEntryFromRow(p))
	}
	return yaml.Marshal(doc)
}

// RenderPathways renders stored routing pathways as canonical YAML.
func (s *Store) RenderPathways() ([]byte, error) {
	rows, err := s.ReadPathways()
	if err != nil {
		return nil, err
	}
	return RenderPathwaysDoc(rows)
}

// RenderPathwaysDoc renders the given pathway rows as canonical YAML.
func RenderPathwaysDoc(rows []PathwayRow) ([]byte, error) {
	doc := capsDocYAML{Version: 1}
	for _, c := range rows {
		doc.Pathways = append(doc.Pathways, capsRouteFromRow(c))
	}
	return yaml.Marshal(doc)
}

// Render returns the canonical document for one kind.
func (s *Store) Render(kind string) ([]byte, error) {
	switch kind {
	case KindPermeability:
		return s.RenderPermeability()
	case KindCallers:
		return s.RenderCallers()
	case KindCredentials:
		return s.RenderCredentials()
	case KindProviders:
		return s.RenderProviders()
	case KindPathways:
		return s.RenderPathways()
	default:
		return nil, ErrUnknownKind
	}
}

// Kinds returns the editable kinds in a stable order. Providers come first so a
// bundle apply writes provider definitions before the targets that reference them.
func Kinds() []string {
	return []string{KindProviders, KindPathways, KindCredentials, KindCallers, KindPermeability}
}

// ContentHash returns a stable hash over all four rendered documents, used to
// detect no-op reloads (preserving live health/breaker state).
func (s *Store) ContentHash() ([sha256.Size]byte, error) {
	h := sha256.New()
	for _, kind := range Kinds() {
		b, err := s.Render(kind)
		if err != nil {
			return [sha256.Size]byte{}, err
		}
		_, _ = h.Write(b)
		_, _ = h.Write([]byte{0})
	}
	var out [sha256.Size]byte
	copy(out[:], h.Sum(nil))
	return out, nil
}
