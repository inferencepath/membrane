package store

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"

	"gopkg.in/yaml.v3"
)

// A bundle is one canonical YAML document holding every editable kind. It is
// the unit of export and apply: operators export the live state, keep it in
// version control, and apply it back in a single transaction. As in the
// per-kind documents, secrets appear only as references and caller secrets
// only as hashes.

// BundleVersion is the schema version embedded in a rendered bundle.
const BundleVersion = 1

// Bundle holds the rows of every editable kind, using the same row types as
// the Read methods.
type Bundle struct {
	Providers    []ProviderRow
	Pathways     []PathwayRow
	Credentials  []CredentialRow
	Callers      []CallerRow
	Permeability []PermeabilityRow
}

// bundleDocYAML is the canonical bundle shape. It reuses the per-kind element
// types from render.go, so each section matches the body of the stand-alone
// document for that kind.
type bundleDocYAML struct {
	Version      int                     `yaml:"version"`
	Providers    []provEntryYAML         `yaml:"providers"`
	Pathways     []capsRouteYAML         `yaml:"pathways"`
	Credentials  []credEntryYAML         `yaml:"credentials"`
	Callers      []callerEntryYAML       `yaml:"callers"`
	Permeability map[string]permDirsYAML `yaml:"permeability"`
}

// ReadBundle reads the whole store into a Bundle, in the order returned by the
// Read methods.
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

// RenderBundleDoc renders b as canonical YAML. The output is stable, so it is
// used both for export and for computing the content version.
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

// ParseBundle decodes a bundle document, rejecting unknown fields. It does no
// semantic validation; apply runs the daemon parsers on each rendered kind.
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

// BundleVersionOf returns the content version of a rendered bundle, a hash of
// its bytes used for optimistic concurrency on apply.
func BundleVersionOf(raw []byte) string {
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

// RenderKindFromBundle renders the stand-alone document for one kind from b,
// so a bundle apply validates each kind the same way a single-kind apply does.
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
