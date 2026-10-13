package store

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"

	"gopkg.in/yaml.v3"
)

// This file defines the declarative config Bundle: a single canonical YAML
// document holding all four editable kinds (pathways, credentials, callers,
// permeability) at once. The bundle is the unit of GitOps-style export/apply
// (the `deck`-equivalent): operators export the live state to a file, keep it in
// version control, and apply it back — reconciled through the daemon's own
// parsers and written in one transaction. Secrets stay by reference; caller
// secrets are hashes only — exactly as in the per-kind documents.

// BundleVersion is the schema version embedded in a rendered bundle.
const BundleVersion = 1

// Bundle is the in-memory projection of all editable kinds. It mirrors the read
// row types so the snapshot builder, console editor, and export/apply path share
// one representation.
type Bundle struct {
	Providers    []ProviderRow
	Pathways     []PathwayRow
	Credentials  []CredentialRow
	Callers      []CallerRow
	Permeability []PermeabilityRow
}

// bundleDocYAML is the canonical bundle shape. It reuses the per-kind inner YAML
// element types (defined in render.go) so a bundle section is byte-identical to
// the corresponding stand-alone document's body.
type bundleDocYAML struct {
	Version      int                     `yaml:"version"`
	Providers    []provEntryYAML         `yaml:"providers"`
	Pathways     []capsRouteYAML         `yaml:"pathways"`
	Credentials  []credEntryYAML         `yaml:"credentials"`
	Callers      []callerEntryYAML       `yaml:"callers"`
	Permeability map[string]permDirsYAML `yaml:"permeability"`
}

// ReadBundle projects the entire store into a Bundle (ordered deterministically
// by the underlying Read* queries). References and hashes only — never a value.
func (s *Store) ReadBundle() (Bundle, error) {
	provs, err := s.ReadProviders()
	if err != nil {
		return Bundle{}, err
	}
	caps, err := s.ReadPathways()
	if err != nil {
		return Bundle{}, err
	}
	creds, err := s.ReadCredentials()
	if err != nil {
		return Bundle{}, err
	}
	callers, err := s.ReadCallers()
	if err != nil {
		return Bundle{}, err
	}
	perm, err := s.ReadPermeability()
	if err != nil {
		return Bundle{}, err
	}
	return Bundle{Providers: provs, Pathways: caps, Credentials: creds, Callers: callers, Permeability: perm}, nil
}

// RenderBundle renders the live store as one canonical bundle document.
func (s *Store) RenderBundle() ([]byte, error) {
	b, err := s.ReadBundle()
	if err != nil {
		return nil, err
	}
	return RenderBundleDoc(b)
}

// RenderBundleDoc renders the given Bundle as canonical YAML. The output is the
// stable, round-trippable form used for export and for the content version.
func RenderBundleDoc(b Bundle) ([]byte, error) {
	doc := bundleDocYAML{Version: BundleVersion}
	for _, p := range b.Providers {
		doc.Providers = append(doc.Providers, provEntryFromRow(p))
	}
	for _, c := range b.Pathways {
		doc.Pathways = append(doc.Pathways, capsRouteFromRow(c))
	}
	for _, c := range b.Credentials {
		doc.Credentials = append(doc.Credentials, credEntryYAML{
			Name: c.Name, SecretRef: c.SecretRef, NextSecretRef: c.NextSecretRef,
		})
	}
	for _, c := range b.Callers {
		doc.Callers = append(doc.Callers, callerEntryYAML{
			KeyID: c.KeyID, Hash: c.SecretHash, CallerID: c.CallerID,
			Name: c.Name, Allow: c.Allow,
		})
	}
	if len(b.Permeability) > 0 {
		doc.Permeability = make(map[string]permDirsYAML, len(b.Permeability))
		for _, p := range b.Permeability {
			doc.Permeability[p.Pathway] = permDirsYAML{
				Outbound: permOutYAML{Allow: p.OutboundAllow},
				Inbound:  permInYAML{Default: p.InboundDefault, OnReveal: p.InboundOnReveal},
			}
		}
	}
	return yaml.Marshal(doc)
}

// ParseBundle maps a bundle document into a Bundle. It is a structural decode
// only (unknown fields rejected); semantic validation is the daemon parsers'
// job, run via the per-kind renders during apply.
func ParseBundle(raw []byte) (Bundle, error) {
	var doc bundleDocYAML
	dec := yaml.NewDecoder(bytes.NewReader(raw))
	dec.KnownFields(true)
	if err := dec.Decode(&doc); err != nil {
		return Bundle{}, fmt.Errorf("bundle: parse: %w", err)
	}

	var b Bundle
	for _, p := range doc.Providers {
		b.Providers = append(b.Providers, provRowFromEntry(p))
	}
	for _, r := range doc.Pathways {
		b.Pathways = append(b.Pathways, capsRowFromRoute(r))
	}
	for _, c := range doc.Credentials {
		b.Credentials = append(b.Credentials, CredentialRow{
			Name: c.Name, SecretRef: c.SecretRef, NextSecretRef: c.NextSecretRef,
		})
	}
	for _, c := range doc.Callers {
		b.Callers = append(b.Callers, CallerRow{
			KeyID: c.KeyID, SecretHash: c.Hash, CallerID: c.CallerID,
			Name: c.Name, Allow: c.Allow,
		})
	}
	for capName, dirs := range doc.Permeability {
		b.Permeability = append(b.Permeability, PermeabilityRow{
			Pathway:         capName,
			OutboundAllow:   dirs.Outbound.Allow,
			InboundDefault:  dirs.Inbound.Default,
			InboundOnReveal: dirs.Inbound.OnReveal,
		})
	}
	return b, nil
}

// BundleVersionOf returns the content version token for a rendered bundle (a
// hash of its canonical bytes) used for optimistic concurrency on apply.
func BundleVersionOf(raw []byte) string {
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

// RenderKindFromBundle renders one kind's stand-alone canonical document from a
// Bundle, reusing the per-kind renderers so bundle-apply validation is identical
// to single-kind apply. Used by the apply path to run each daemon parser.
func RenderKindFromBundle(kind string, b Bundle) ([]byte, error) {
	switch kind {
	case KindProviders:
		return RenderProvidersDoc(b.Providers)
	case KindPathways:
		return RenderPathwaysDoc(b.Pathways)
	case KindCredentials:
		return RenderCredentialsDoc(b.Credentials)
	case KindCallers:
		return RenderCallersDoc(b.Callers)
	case KindPermeability:
		return RenderPermeabilityDoc(b.Permeability)
	default:
		return nil, ErrUnknownKind
	}
}
