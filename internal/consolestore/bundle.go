package consolestore

import (
	"github.com/inferencepath/membrane/internal/interior/admit"
	"github.com/inferencepath/membrane/internal/server/console"
	"github.com/inferencepath/membrane/internal/store"
)

// This file implements console.ConfigBundle over the store: the declarative
// export/apply surface (the `deck`-equivalent). Export renders the live store as
// one canonical bundle; diff validates a candidate and reports the per-kind
// change set without writing; apply validates (per-kind parsers + the injected
// cross-kind validator), checks an optimistic whole-bundle version, writes all
// kinds in one transaction, and triggers the fail-closed, last-good reload.
// Secrets stay by reference; caller secrets are hashes only.

// ExportBundle renders the live configuration as one canonical bundle plus its
// content version token.
func (a *Adapter) ExportBundle() (console.BundleFile, error) {
	raw, err := a.st.RenderBundle()
	if err != nil {
		return console.BundleFile{}, err
	}
	return console.BundleFile{Raw: string(raw), Version: store.BundleVersionOf(raw)}, nil
}

// DiffBundle parses and validates a candidate bundle, then reports the per-kind
// change set against the live store. No write occurs.
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

// ApplyBundle validates a candidate bundle, enforces the optimistic-concurrency
// version against the live store, writes all four kinds in one transaction, and
// triggers a reload. On any failure before the write, no change is made; the
// last-good configuration keeps serving.
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

	// No-op short-circuit: identical bundle leaves live state (and breaker/health)
	// untouched.
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

// validateBundle runs the per-kind daemon parsers over the candidate (so each
// document is rejected exactly as on single-kind apply) and then the injected
// cross-kind validator (keyring resolution, per-target adapter build). The
// cross-kind step is skipped only when no validator was injected.
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

// CreatePathway atomically upserts a pathway (routing) together with its
// permeability policy, writing both kinds in one transaction via the bundle
// apply path. Pathways and permeability are separate documents linked only by
// name, and the loader is fail-closed on that link; submitting them together is
// the only way a new pathway lands in a valid, servable state in one step.
//
// It reads the live bundle, upserts the pathway and permeability rows by name
// (leaving every other kind untouched), re-renders the canonical bundle, and
// runs the identical validate → atomic write → reload pipeline as ApplyBundle.
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
	// Version "" skips the optimistic check: the bundle was just read here, and
	// the write is a single-pathway upsert that preserves all other kinds.
	return a.ApplyBundle(raw, "")
}

// CreateCaller upserts a Caller Key by its key id, hashing the plaintext secret
// to SHA-256 so only the hash is ever stored (never the secret). It writes via
// the bundle apply path (atomic, single reload) and leaves every other kind
// untouched. The daemon's own admit parser validates the result on apply.
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

// DeleteCaller removes a Caller Key by its key id via the bundle apply path. It
// reports whether a caller was removed; deleting an absent key is a no-op.
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

// errReason wraps a secret-free reason string as an error for the delete path.
func errReason(reason string) error { return &applyError{reason: reason} }

type applyError struct{ reason string }

func (e *applyError) Error() string { return e.reason }

// finishReloadBundle triggers the daemon reload after a bundle write and maps
// its outcome, stamping the new whole-bundle content version.
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
