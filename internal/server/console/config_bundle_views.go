package console

// The ConfigBundle port is defined here so the console imports no Interior,
// Membrane, Exterior, or store package; internal/consolestore implements it. A
// bundle is one YAML document holding all four config kinds, and apply
// replaces the whole configuration atomically using the daemon's parsers.
// Bundles carry secret references and caller hashes, never values.

// BundleFile is a rendered bundle plus a version token (a hash of the
// canonical bytes) for optimistic concurrency and drift detection.
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
	// ApplyBundle validates a candidate bundle, checks the version, writes all
	// kinds in one transaction, and triggers a reload.
	ApplyBundle(raw []byte, version string) ApplyResult
}
