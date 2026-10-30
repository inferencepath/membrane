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

// An edit, whether raw YAML or a form, is validated with the daemon loaders,
// mapped to store rows, written in a transaction, and followed by a reload.
// Each kind has a content version (a hash of its canonical document), and an
// edit made against a stale version is rejected.

// Kinds returns the editable config kinds.
func (a *Adapter) Kinds() []string { return store.Kinds() }

// Read returns the current canonical document for a kind and its version.
func (a *Adapter) Read(kind string) (console.EditFile, error) {
	raw, err := a.st.Render(kind)
	if err != nil {
		return console.EditFile{}, err
	}
	return console.EditFile{Kind: kind, Raw: string(raw), Version: versionOf(raw)}, nil
}

// Validate parses raw with the loader for kind without writing anything.
func (a *Adapter) Validate(kind string, raw []byte) console.ValidationResult {
	if _, err := rowsFromRaw(kind, raw); err != nil {
		return console.ValidationResult{Valid: false, Error: err.Error()}
	}
	return console.ValidationResult{Valid: true}
}

// Apply validates raw, checks version against the current document, writes
// the rows and triggers a reload.
func (a *Adapter) Apply(kind string, raw []byte, version string) console.ApplyResult {
	rows, err := rowsFromRaw(kind, raw)
	if err != nil {
		return console.ApplyResult{Rejected: true, Reason: err.Error()}
	}
	return a.commit(kind, rows, version, raw)
}

// ValidateForm converts a form into rows and validates them with the loader
// for kind without writing anything.
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

// ApplyForm converts a form into rows and then behaves like Apply. Caller
// allowlist edits use SetCallerAllow and leave hashes untouched.
func (a *Adapter) ApplyForm(kind string, form []byte, version string) console.ApplyResult {
	rows, allowOnly, err := a.rowsFromForm(kind, form)
	if err != nil {
		return console.ApplyResult{Rejected: true, Reason: err.Error()}
	}
	raw, err := renderRows(kind, rows)
	if err != nil {
		return console.ApplyResult{Rejected: true, Reason: err.Error()}
	}
	if _, perr := rowsFromRaw(kind, raw); perr != nil {
		return console.ApplyResult{Rejected: true, Reason: perr.Error()}
	}

	if allowOnly != nil {
		// Update allow rows only; the callers document still supplies the
		// version.
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

// commit checks the version, writes rows and reloads. raw is the canonical
// document for rows and is compared with the current one to detect a no-op.
func (a *Adapter) commit(kind string, rows any, version string, raw []byte) console.ApplyResult {
	if res, stop := a.checkVersion(kind, version); stop {
		return res
	}
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

// checkVersion rejects the edit if version is set and differs from that of the
// current document. The bool reports whether to stop.
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

// finishReload runs the daemon reload and maps its outcome, including the new
// content version of the kind.
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
		// The reload was rejected and last-good keeps serving, so the store
		// differs from the active config until the next successful reload.
		return console.ApplyResult{Rejected: true, Reason: res.Reason, Version: newVer}
	}
}

// writeRows writes rows using the store writer for kind.
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

// rowsFromRaw parses raw YAML with the daemon loader for kind and maps the
// result to store rows. It applies the same rules as the serving path.
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

// callerRowsFromYAML maps raw YAML to caller rows, including hashes. The
// caller must have validated raw with admit.ParseStore first.
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
