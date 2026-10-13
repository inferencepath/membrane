package consolestore

import (
	"encoding/json"
	"fmt"

	"github.com/inferencepath/membrane/internal/store"
)

// This file maps the console's structured form JSON (the same payloads the SPA
// posts) into store rows. Forms are the primary edit surface; they carry no
// secret values. The callers form is special: it edits only allowlists, so it
// returns an allowOnly map that the apply path writes via SetCallerAllow,
// leaving hashes untouched.

// --- form models (match the SPA payloads) --------------------------------

type callerForm struct {
	KeyID string   `json:"keyId"`
	Allow []string `json:"allow"`
}

type credentialForm struct {
	Name      string `json:"name"`
	Reference string `json:"reference"`
}

type pathwayForm struct {
	Pathway         string   `json:"pathway"`
	CascadeAllowed  bool     `json:"cascadeAllowed"`
	DefaultProvider string   `json:"defaultProvider"`
	Providers       []string `json:"providers"`
}

type permeabilityCapForm struct {
	Pathway         string   `json:"pathway"`
	OutboundAllow   []string `json:"outboundAllow"`
	InboundDefault  []string `json:"inboundDefault"`
	InboundOnReveal []string `json:"inboundOnReveal"`
}

// rowsFromForm converts a structured form into store rows. For callers it also
// returns an allowOnly map (keyID→allow) signalling the allowlist-only write
// path; for every other kind allowOnly is nil and rows carry the full desired
// state for that kind.
func (a *Adapter) rowsFromForm(kind string, form []byte) (rows any, allowOnly map[string][]string, err error) {
	switch kind {
	case store.KindPathways:
		var body struct {
			Pathways []pathwayForm `json:"pathways"`
		}
		if err := json.Unmarshal(form, &body); err != nil {
			return nil, nil, fmt.Errorf("invalid pathways form: %w", err)
		}
		out := make([]store.PathwayRow, 0, len(body.Pathways))
		for _, c := range body.Pathways {
			out = append(out, store.PathwayRow{
				Name:            c.Pathway,
				CascadeAllowed:  c.CascadeAllowed,
				DefaultProvider: c.DefaultProvider,
				Providers:       c.Providers,
			})
		}
		return out, nil, nil

	case store.KindCredentials:
		var body struct {
			Credentials []credentialForm `json:"credentials"`
		}
		if err := json.Unmarshal(form, &body); err != nil {
			return nil, nil, fmt.Errorf("invalid credentials form: %w", err)
		}
		// Preserve next_secret_ref for existing names (rotation is not edited here).
		next := a.currentNextRefs()
		out := make([]store.CredentialRow, 0, len(body.Credentials))
		for _, c := range body.Credentials {
			out = append(out, store.CredentialRow{
				Name: c.Name, SecretRef: c.Reference, NextSecretRef: next[c.Name],
			})
		}
		return out, nil, nil

	case store.KindPermeability:
		var body struct {
			Pathways []permeabilityCapForm `json:"pathways"`
		}
		if err := json.Unmarshal(form, &body); err != nil {
			return nil, nil, fmt.Errorf("invalid permeability form: %w", err)
		}
		out := make([]store.PermeabilityRow, 0, len(body.Pathways))
		for _, p := range body.Pathways {
			out = append(out, store.PermeabilityRow{
				Pathway:         p.Pathway,
				OutboundAllow:   p.OutboundAllow,
				InboundDefault:  p.InboundDefault,
				InboundOnReveal: p.InboundOnReveal,
			})
		}
		return out, nil, nil

	case store.KindCallers:
		var body struct {
			Callers []callerForm `json:"callers"`
		}
		if err := json.Unmarshal(form, &body); err != nil {
			return nil, nil, fmt.Errorf("invalid callers form: %w", err)
		}
		// Build the candidate full caller set (for validation) from current rows
		// with the submitted allowlists applied, plus an allowOnly map used by the
		// write path so hashes are never touched.
		current, rerr := a.st.ReadCallers()
		if rerr != nil {
			return nil, nil, rerr
		}
		byKey := make(map[string]*store.CallerRow, len(current))
		rowsOut := make([]store.CallerRow, len(current))
		copy(rowsOut, current)
		for i := range rowsOut {
			byKey[rowsOut[i].KeyID] = &rowsOut[i]
		}
		allow := make(map[string][]string, len(body.Callers))
		for _, cf := range body.Callers {
			row, ok := byKey[cf.KeyID]
			if !ok {
				return nil, nil, fmt.Errorf("callers: unknown key_id %q", cf.KeyID)
			}
			row.Allow = cf.Allow
			allow[cf.KeyID] = cf.Allow
		}
		return rowsOut, allow, nil

	default:
		return nil, nil, store.ErrUnknownKind
	}
}

// currentNextRefs returns name→next_secret_ref for existing credentials so a
// form edit preserves rotation settings it does not manage.
func (a *Adapter) currentNextRefs() map[string]string {
	out := map[string]string{}
	rows, err := a.st.ReadCredentials()
	if err != nil {
		return out
	}
	for _, r := range rows {
		if r.NextSecretRef != "" {
			out[r.Name] = r.NextSecretRef
		}
	}
	return out
}
