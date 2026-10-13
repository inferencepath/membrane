package console

// This file defines the console's config EDIT port and its result types. As with
// the read views, the types live in the console package (so the console imports
// no Interior/Membrane/Exterior package); the concrete ConfigEditor is
// implemented in internal/consoleedit and injected. Edits operate on raw file
// text (config-as-code): the editor validates with the daemon's own parsers,
// enforces an optimistic-concurrency version check, writes atomically, and
// triggers the existing fail-closed, last-good reload.

// EditFile is a raw config file plus a content version token (for optimistic
// concurrency / drift detection).
type EditFile struct {
	Kind    string `json:"kind"`
	Raw     string `json:"raw"`
	Version string `json:"version"`
}

// ValidationResult is the outcome of a parse-only dry run. On failure Error is
// the parser's exact, secret-free message; no write occurs.
type ValidationResult struct {
	Valid bool   `json:"valid"`
	Error string `json:"error,omitempty"`
}

// ApplyResult is the outcome of an apply (validate → version check → atomic
// write → reload). Exactly one of Applied/Unchanged/Rejected is true. Reason is
// a secret-free explanation; Version is the new content token after a write.
type ApplyResult struct {
	Applied   bool   `json:"applied"`
	Unchanged bool   `json:"unchanged"`
	Rejected  bool   `json:"rejected"`
	Reason    string `json:"reason,omitempty"`
	Version   string `json:"version,omitempty"`
}

// ConfigEditor edits the reloadable config files (pathways, credentials,
// callers, permeability). A nil ConfigEditor disables the edit endpoints (they
// return 503) — e.g. when the daemon is not in config-as-code (reloadable) mode.
type ConfigEditor interface {
	// Kinds returns the editable config kinds.
	Kinds() []string
	// Read returns the current raw content of a kind plus its version token.
	Read(kind string) (EditFile, error)
	// Validate parses raw bytes with the kind's loader (no write).
	Validate(kind string, raw []byte) ValidationResult
	// Apply validates, checks version (optimistic lock), writes atomically, and
	// triggers a reload. The returned ApplyResult reports the outcome.
	Apply(kind string, raw []byte, version string) ApplyResult

	// ValidateForm renders a structured form (JSON) into the kind's YAML —
	// merging into the current file so comments and untouched/secret fields
	// (hashes, rotation, next_secret_ref) are preserved — and validates it with
	// the kind's loader (no write).
	ValidateForm(kind string, form []byte) ValidationResult
	// ApplyForm renders a structured form (as ValidateForm) then runs the same
	// validate → version check → atomic write → reload pipeline as Apply.
	ApplyForm(kind string, form []byte, version string) ApplyResult
}

// editorKnows reports whether kind is one the editor handles.
func editorKnows(e ConfigEditor, kind string) bool {
	for _, k := range e.Kinds() {
		if k == kind {
			return true
		}
	}
	return false
}
