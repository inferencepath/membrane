package consolestore

import (
	"github.com/inferencepath/membrane/internal/interior/admit"
	"github.com/inferencepath/membrane/internal/server/console"
	"github.com/inferencepath/membrane/internal/store"
)

// Bundle export, diff and apply. Apply validates the bundle, checks its
// version, writes every kind in one transaction and then reloads.

// ExportBundle renders the live configuration as one bundle, with its content
// version.
func (a *Adapter) ExportBundle() (console.BundleFile, error) {
	raw, err := a.st.RenderBundle()
	if err != nil {
		return console.BundleFile{}, err
	}
	return console.BundleFile{Raw: string(raw), Version: store.BundleVersionOf(raw)}, nil
}

// DiffBundle validates a candidate bundle and reports which kinds differ from
// the live store. It does not write.
func (a *Adapter) DiffBundle(raw []byte) (console.BundleDiff, error) {
	b, err := store.ParseBundle(raw)
	if err != nil {
		return console.BundleDiff{Valid: false, Error: err.Error()}, nil
	}
	if verr := a.validateBundle(b); verr != nil {
		return console.BundleDiff{Valid: false, Error: verr.Error()}, nil
	}

	diff := console.BundleDiff{Valid: true}
	for _, kind := range store.Kinds() {
		candidate, rerr := store.RenderKindFromBundle(kind, b)
		if rerr != nil {
			return console.BundleDiff{Valid: false, Error: rerr.Error()}, nil
		}
		current, rerr := a.st.Render(kind)
		if rerr != nil {
			return console.BundleDiff{}, rerr
		}
		changed := versionOf(candidate) != versionOf(current)
		if changed {
			diff.Changed = true
		}
		diff.Kinds = append(diff.Kinds, console.BundleKindDiff{Kind: kind, Changed: changed})
	}
	return diff, nil
}

// ApplyBundle validates a candidate bundle, checks version against the live
// store, writes every kind in one transaction and triggers a reload. If any
// check fails nothing is written and last-good keeps serving.
func (a *Adapter) ApplyBundle(raw []byte, version string) console.ApplyResult {
	b, err := store.ParseBundle(raw)
	if err != nil {
		return console.ApplyResult{Rejected: true, Reason: err.Error()}
	}
	if verr := a.validateBundle(b); verr != nil {
		return console.ApplyResult{Rejected: true, Reason: verr.Error()}
	}

	current, err := a.st.RenderBundle()
	if err != nil {
		return console.ApplyResult{Rejected: true, Reason: err.Error()}
	}
	currentVer := store.BundleVersionOf(current)
	if version != "" && version != currentVer {
		return console.ApplyResult{
			Rejected: true,
			Reason:   "configuration changed since it was loaded (refresh and retry)",
			Version:  currentVer,
		}
	}

	// Skip an identical bundle so live health and breaker state are kept.
	candidate, err := store.RenderBundleDoc(b)
	if err != nil {
		return console.ApplyResult{Rejected: true, Reason: err.Error()}
	}
	if store.BundleVersionOf(candidate) == currentVer {
		return console.ApplyResult{Unchanged: true, Reason: "no changes", Version: currentVer}
	}

	if werr := a.st.WriteBundle(b); werr != nil {
		return console.ApplyResult{Rejected: true, Reason: werr.Error()}
	}
	return a.finishReloadBundle()
}

// validateBundle runs the per-kind daemon parsers, as single-kind apply does,
// and then the cross-kind validator if one was set.
func (a *Adapter) validateBundle(b store.Bundle) error {
	docs := make(map[string][]byte, len(store.Kinds()))
	for _, kind := range store.Kinds() {
		raw, err := store.RenderKindFromBundle(kind, b)
		if err != nil {
			return err
		}
		if _, perr := rowsFromRaw(kind, raw); perr != nil {
			return perr
		}
		docs[kind] = raw
	}
	if a.validate == nil {
		return nil
	}
	return a.validate(
		docs[store.KindPermeability],
		docs[store.KindCallers],
		docs[store.KindCredentials],
		docs[store.KindProviders],
		docs[store.KindPathways],
	)
}

// CreatePathway upserts a pathway and its Permeability policy in one bundle
// apply. The two are separate documents linked by name, and the loader fails
// closed if either is missing, so a new pathway is only valid once both are
// written together. Other kinds are left unchanged.
func (a *Adapter) CreatePathway(in console.PathwayWizardInput) console.ApplyResult {
	b, err := a.st.ReadBundle()
	if err != nil {
		return console.ApplyResult{Rejected: true, Reason: err.Error()}
	}

	pw := store.PathwayRow{
		Name:            in.Pathway,
		Operation:       in.Operation,
		CascadeAllowed:  in.CascadeAllowed,
		DefaultProvider: in.DefaultProvider,
		Providers:       in.Providers,
	}
	replaced := false
	for i := range b.Pathways {
		if b.Pathways[i].Name == in.Pathway {
			b.Pathways[i] = pw
			replaced = true
			break
		}
	}
	if !replaced {
		b.Pathways = append(b.Pathways, pw)
	}

	pr := store.PermeabilityRow{
		Pathway:         in.Pathway,
		OutboundAllow:   in.OutboundAllow,
		InboundDefault:  in.InboundDefault,
		InboundOnReveal: in.InboundOnReveal,
	}
	replaced = false
	for i := range b.Permeability {
		if b.Permeability[i].Pathway == in.Pathway {
			b.Permeability[i] = pr
			replaced = true
			break
		}
	}
	if !replaced {
		b.Permeability = append(b.Permeability, pr)
	}

	raw, err := store.RenderBundleDoc(b)
	if err != nil {
		return console.ApplyResult{Rejected: true, Reason: err.Error()}
	}
	// The bundle was just read, so skip the version check.
	return a.ApplyBundle(raw, "")
}

// CreateCaller upserts a Caller Key by key ID through a bundle apply. Only the
// SHA-256 hash of the secret is stored.
func (a *Adapter) CreateCaller(in console.CallerInput) console.ApplyResult {
	b, err := a.st.ReadBundle()
	if err != nil {
		return console.ApplyResult{Rejected: true, Reason: err.Error()}
	}
	row := store.CallerRow{
		KeyID:      in.KeyID,
		CallerID:   in.CallerID,
		Name:       in.Name,
		SecretHash: admit.HashSecret(in.Secret),
		Allow:      in.Allow,
	}
	replaced := false
	for i := range b.Callers {
		if b.Callers[i].KeyID == in.KeyID {
			b.Callers[i] = row
			replaced = true
			break
		}
	}
	if !replaced {
		b.Callers = append(b.Callers, row)
	}
	raw, err := store.RenderBundleDoc(b)
	if err != nil {
		return console.ApplyResult{Rejected: true, Reason: err.Error()}
	}
	return a.ApplyBundle(raw, "")
}

// DeleteCaller removes a Caller Key by key ID through a bundle apply and
// reports whether it existed.
func (a *Adapter) DeleteCaller(keyID string) (bool, error) {
	b, err := a.st.ReadBundle()
	if err != nil {
		return false, err
	}
	kept := b.Callers[:0]
	removed := false
	for _, c := range b.Callers {
		if c.KeyID == keyID {
			removed = true
			continue
		}
		kept = append(kept, c)
	}
	if !removed {
		return false, nil
	}
	b.Callers = kept
	raw, err := store.RenderBundleDoc(b)
	if err != nil {
		return false, err
	}
	res := a.ApplyBundle(raw, "")
	if res.Rejected {
		return false, errReason(res.Reason)
	}
	return true, nil
}

// errReason wraps a rejection reason as an error.
func errReason(reason string) error { return &applyError{reason: reason} }

type applyError struct{ reason string }

func (e *applyError) Error() string { return e.reason }

// finishReloadBundle runs the daemon reload after a bundle write and maps its
// outcome, including the new bundle version.
func (a *Adapter) finishReloadBundle() console.ApplyResult {
	newVer := ""
	if raw, err := a.st.RenderBundle(); err == nil {
		newVer = store.BundleVersionOf(raw)
	}
	res := a.reload()
	switch {
	case res.Applied:
		return console.ApplyResult{Applied: true, Version: newVer}
	case res.Unchanged:
		return console.ApplyResult{Unchanged: true, Reason: res.Reason, Version: newVer}
	default:
		return console.ApplyResult{Rejected: true, Reason: res.Reason, Version: newVer}
	}
}
