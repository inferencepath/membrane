package consolestore

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/inferencepath/membrane/internal/config"
	"github.com/inferencepath/membrane/internal/interior/admit"
	"github.com/inferencepath/membrane/internal/membrane/permeability"
	"github.com/inferencepath/membrane/internal/server/console"
	"github.com/inferencepath/membrane/internal/store"
)

// This file implements console.ConfigEditor over the store. Editing is fully
// structured: an edit (raw YAML or a form) is parsed/validated with the daemon's
// own loaders, mapped to store rows, written transactionally, and then the
// serving Snapshot is rebuilt by an explicit reload (fail-closed, last-good).
// Optimistic concurrency uses a per-kind content version (a hash of the kind's
// canonical document), so a stale edit is rejected rather than clobbering a
// concurrent change.

// Kinds returns the editable config kinds.
func (a *Adapter) Kinds() []string { return store.Kinds() }

// Read returns the current canonical document for a kind plus its version token.
func (a *Adapter) Read(kind string) (console.EditFile, error) {
	raw, err := a.st.Render(kind)
	if err != nil {
		return console.EditFile{}, err
	}
	return console.EditFile{Kind: kind, Raw: string(raw), Version: versionOf(raw)}, nil
}

// Validate parses raw bytes with the kind's loader (no write).
func (a *Adapter) Validate(kind string, raw []byte) console.ValidationResult {
	if _, err := rowsFromRaw(kind, raw); err != nil {
		return console.ValidationResult{Valid: false, Error: err.Error()}
	}
	return console.ValidationResult{Valid: true}
}

// Apply validates raw bytes, checks the optimistic-concurrency version, writes
// the mapped rows transactionally, and triggers a reload.
func (a *Adapter) Apply(kind string, raw []byte, version string) console.ApplyResult {
	rows, err := rowsFromRaw(kind, raw)
	if err != nil {
		return console.ApplyResult{Rejected: true, Reason: err.Error()}
	}
	return a.commit(kind, rows, version, raw)
}

// ValidateForm renders a structured form into the kind's rows and validates them
// with the kind's loader (no write).
func (a *Adapter) ValidateForm(kind string, form []byte) console.ValidationResult {
	rows, _, err := a.rowsFromForm(kind, form)
	if err != nil {
		return console.ValidationResult{Valid: false, Error: err.Error()}
	}
	if raw, rerr := renderRows(kind, rows); rerr != nil {
		return console.ValidationResult{Valid: false, Error: rerr.Error()}
	} else if _, perr := rowsFromRaw(kind, raw); perr != nil {
		return console.ValidationResult{Valid: false, Error: perr.Error()}
	}
	return console.ValidationResult{Valid: true}
}

// ApplyForm renders a structured form then runs the same validate → version
// check → write → reload pipeline as Apply. Caller allowlist edits take a
// dedicated path that never touches hashes.
func (a *Adapter) ApplyForm(kind string, form []byte, version string) console.ApplyResult {
	rows, allowOnly, err := a.rowsFromForm(kind, form)
	if err != nil {
		return console.ApplyResult{Rejected: true, Reason: err.Error()}
	}
	raw, err := renderRows(kind, rows)
	if err != nil {
		return console.ApplyResult{Rejected: true, Reason: err.Error()}
	}
	// Validate the candidate before any write.
	if _, perr := rowsFromRaw(kind, raw); perr != nil {
		return console.ApplyResult{Rejected: true, Reason: perr.Error()}
	}

	if allowOnly != nil {
		// Caller allowlist-only edit: update allow rows without replacing callers
		// (hashes untouched). Version check uses the callers document.
		if res, stop := a.checkVersion(kind, version); stop {
			return res
		}
		for keyID, allow := range allowOnly {
			if werr := a.st.SetCallerAllow(keyID, allow); werr != nil {
				return console.ApplyResult{Rejected: true, Reason: werr.Error()}
			}
		}
		return a.finishReload(kind)
	}

	return a.commit(kind, rows, version, raw)
}

// commit performs the version check, the transactional write for the kind, and
// the reload. raw is the canonical document used for the new version token.
func (a *Adapter) commit(kind string, rows any, version string, raw []byte) console.ApplyResult {
	if res, stop := a.checkVersion(kind, version); stop {
		return res
	}
	// No-op short-circuit.
	current, err := a.st.Render(kind)
	if err != nil {
		return console.ApplyResult{Rejected: true, Reason: err.Error()}
	}
	if versionOf(raw) == versionOf(current) {
		return console.ApplyResult{Unchanged: true, Reason: "no changes", Version: versionOf(current)}
	}
	if werr := a.writeRows(kind, rows); werr != nil {
		return console.ApplyResult{Rejected: true, Reason: werr.Error()}
	}
	return a.finishReload(kind)
}

// checkVersion enforces optimistic concurrency against the current document.
func (a *Adapter) checkVersion(kind, version string) (console.ApplyResult, bool) {
	if version == "" {
		return console.ApplyResult{}, false
	}
	current, err := a.st.Render(kind)
	if err != nil {
		return console.ApplyResult{Rejected: true, Reason: err.Error()}, true
	}
	if version != versionOf(current) {
		return console.ApplyResult{
			Rejected: true,
			Reason:   "configuration changed since it was loaded (refresh and retry)",
			Version:  versionOf(current),
		}, true
	}
	return console.ApplyResult{}, false
}

// finishReload triggers the daemon reload and maps its outcome, stamping the new
// content version of the kind's document.
func (a *Adapter) finishReload(kind string) console.ApplyResult {
	newVer := ""
	if raw, err := a.st.Render(kind); err == nil {
		newVer = versionOf(raw)
	}
	res := a.reload()
	switch {
	case res.Applied:
		return console.ApplyResult{Applied: true, Version: newVer}
	case res.Unchanged:
		return console.ApplyResult{Unchanged: true, Reason: res.Reason, Version: newVer}
	default:
		// Reload rejected the candidate: last-good keeps serving. The store now
		// differs from the active config; surface the secret-free reason.
		return console.ApplyResult{Rejected: true, Reason: res.Reason, Version: newVer}
	}
}

// writeRows dispatches the transactional write for a kind.
func (a *Adapter) writeRows(kind string, rows any) error {
	switch kind {
	case store.KindProviders:
		return a.st.WriteProviders(rows.([]store.ProviderRow))
	case store.KindPathways:
		return a.st.WritePathways(rows.([]store.PathwayRow))
	case store.KindCredentials:
		return a.st.WriteCredentials(rows.([]store.CredentialRow))
	case store.KindCallers:
		return a.st.WriteCallers(rows.([]store.CallerRow))
	case store.KindPermeability:
		return a.st.WritePermeability(rows.([]store.PermeabilityRow))
	default:
		return store.ErrUnknownKind
	}
}

// renderRows renders candidate rows to the canonical document for validation.
func renderRows(kind string, rows any) ([]byte, error) {
	switch kind {
	case store.KindProviders:
		return store.RenderProvidersDoc(rows.([]store.ProviderRow))
	case store.KindPathways:
		return store.RenderPathwaysDoc(rows.([]store.PathwayRow))
	case store.KindCredentials:
		return store.RenderCredentialsDoc(rows.([]store.CredentialRow))
	case store.KindCallers:
		return store.RenderCallersDoc(rows.([]store.CallerRow))
	case store.KindPermeability:
		return store.RenderPermeabilityDoc(rows.([]store.PermeabilityRow))
	default:
		return nil, store.ErrUnknownKind
	}
}

// --- raw YAML → rows (validated via the daemon's own parsers) ----------------

// rowsFromRaw parses raw YAML for a kind with the daemon's loader and maps the
// validated result to store rows. The parse enforces the same rules as the
// serving path, so an invalid document is rejected before any write.
func rowsFromRaw(kind string, raw []byte) (any, error) {
	switch kind {
	case store.KindProviders:
		spec, err := config.ParseProviders(raw)
		if err != nil {
			return nil, err
		}
		return providerRows(spec), nil
	case store.KindPathways:
		spec, err := config.ParsePathways(raw)
		if err != nil {
			return nil, err
		}
		return pathwayRows(spec), nil
	case store.KindCredentials:
		kf, err := config.ParseCredentialsOptional(raw)
		if err != nil {
			return nil, err
		}
		return credentialRows(kf), nil
	case store.KindCallers:
		if _, err := admit.ParseStore(raw); err != nil {
			return nil, err
		}
		return callerRowsFromYAML(raw)
	case store.KindPermeability:
		spec, err := permeability.Parse(raw)
		if err != nil {
			return nil, err
		}
		return permeabilityRows(spec), nil
	default:
		return nil, store.ErrUnknownKind
	}
}

func pathwayRows(spec config.PathwaysSpec) []store.PathwayRow {
	out := make([]store.PathwayRow, 0, len(spec.Routes))
	for _, r := range spec.Routes {
		out = append(out, store.PathwayRow{
			Name:            string(r.Pathway),
			CascadeAllowed:  r.CascadeAllowed,
			DefaultProvider: r.DefaultProvider,
			Providers:       r.Providers,
		})
	}
	return out
}

func providerRows(spec config.ProvidersSpec) []store.ProviderRow {
	out := make([]store.ProviderRow, 0, len(spec.Providers))
	for _, p := range spec.Providers {
		out = append(out, store.ProviderRow{
			Name:         p.Name,
			Family:       string(p.Family),
			AuthMode:     p.AuthMode,
			RoleARN:      p.RoleARN,
			ExternalID:   p.ExternalID,
			Models:       p.Models,
			DefaultModel: p.DefaultModel,
			Endpoint:     p.Endpoint,
			Deployment:   p.Deployment,
			Region:       p.Region,
			Project:      p.Project,
			Location:     p.Location,
			SecretRef:    p.SecretRef,
		})
	}
	return out
}

func credentialRows(kf config.KeyringFile) []store.CredentialRow {
	out := make([]store.CredentialRow, 0, len(kf.Credentials))
	for _, c := range kf.Credentials {
		out = append(out, store.CredentialRow{
			Name: c.Name, SecretRef: c.SecretRef, NextSecretRef: c.NextSecretRef,
		})
	}
	return out
}

func permeabilityRows(spec permeability.Spec) []store.PermeabilityRow {
	out := make([]store.PermeabilityRow, 0, len(spec.Pathways))
	for cap, ds := range spec.Pathways {
		out = append(out, store.PermeabilityRow{
			Pathway:         string(cap),
			OutboundAllow:   ds.Outbound.Allow,
			InboundDefault:  ds.Inbound.Default,
			InboundOnReveal: ds.Inbound.OnReveal,
		})
	}
	return out
}

// callerRowsFromYAML extracts caller rows (including hashes) from raw YAML. The
// admit parser has already validated the document; this read only maps fields.
func callerRowsFromYAML(raw []byte) ([]store.CallerRow, error) {
	var doc struct {
		Callers []struct {
			KeyID    string   `yaml:"key_id"`
			Hash     string   `yaml:"hash"`
			CallerID string   `yaml:"caller_id"`
			Name     string   `yaml:"name"`
			Allow    []string `yaml:"allow"`
		} `yaml:"callers"`
	}
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("callers: %w", err)
	}
	out := make([]store.CallerRow, 0, len(doc.Callers))
	for _, c := range doc.Callers {
		out = append(out, store.CallerRow{
			KeyID: c.KeyID, SecretHash: c.Hash, CallerID: c.CallerID,
			Name: c.Name, Allow: c.Allow,
		})
	}
	return out, nil
}

func durString(d time.Duration) string {
	if d <= 0 {
		return ""
	}
	return d.String()
}

func versionOf(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
