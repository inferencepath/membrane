package console

// This file defines the console's declarative config BUNDLE port and its result
// types — the `deck`-equivalent export/apply surface. As with the other config
// ports, the types live in the console package (so the console imports no
// Interior/Membrane/Exterior/store package); the concrete ConfigBundle is
// implemented in internal/consolestore and injected. A bundle is one canonical
// YAML document holding all four kinds; apply reconciles the whole configuration
// atomically through the daemon's own parsers. Secrets stay by reference; caller
// secrets are hashes only — nothing here carries a value.

// BundleFile is a rendered declarative bundle plus a content version token (a
// hash over the canonical bytes) for optimistic concurrency / drift detection.
type BundleFile struct {
	Raw     string `json:"raw"`
	Version string `json:"version"`
}

// BundleKindDiff reports whether one kind would change on apply.
type BundleKindDiff struct {
	Kind    string `json:"kind"`
	Changed bool   `json:"changed"`
}

// BundleDiff is the outcome of a dry-run comparison of a candidate bundle
// against the live store. Valid is false when the candidate fails to parse or
// validate (Error is the secret-free reason); otherwise Kinds reports the
// per-kind change set and Changed is true when any kind differs.
type BundleDiff struct {
	Valid   bool             `json:"valid"`
	Error   string           `json:"error,omitempty"`
	Changed bool             `json:"changed"`
	Kinds   []BundleKindDiff `json:"kinds,omitempty"`
}

// ConfigBundle is the declarative export/apply port. A nil ConfigBundle disables
// the bundle endpoints (they return 503).
type ConfigBundle interface {
	// ExportBundle renders the live configuration as one canonical bundle.
	ExportBundle() (BundleFile, error)
	// DiffBundle validates a candidate bundle and reports the per-kind change
	// set against the live store, without writing.
	DiffBundle(raw []byte) (BundleDiff, error)
	// ApplyBundle validates a candidate bundle, checks the optimistic-concurrency
	// version, writes all kinds in one transaction, and triggers a reload.
	ApplyBundle(raw []byte, version string) ApplyResult
}
