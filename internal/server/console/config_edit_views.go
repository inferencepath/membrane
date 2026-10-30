package console

// The edit port is defined here so the console imports no Interior, Membrane,
// or Exterior package; internal/consoleedit implements it. Edits work on raw
// file text: the editor validates with the daemon's parsers, checks the
// version token, writes atomically, and triggers the fail-closed, last-good
// reload.

// EditFile is a raw config file plus a content version token used for
// optimistic concurrency.
type EditFile struct {
	Kind    string `json:"kind"`
	Raw     string `json:"raw"`
	Version string `json:"version"`
}

// ValidationResult is the outcome of a parse-only dry run. On failure Error
// holds the parser's message, which contains no secrets.
type ValidationResult struct {
	Valid bool   `json:"valid"`
	Error string `json:"error,omitempty"`
}

// ApplyResult is the outcome of an apply. One of Applied, Unchanged, or
// Rejected is true. Reason contains no secrets. Version is the new content
// token after a write.
type ApplyResult struct {
	Applied   bool   `json:"applied"`
	Unchanged bool   `json:"unchanged"`
	Rejected  bool   `json:"rejected"`
	Reason    string `json:"reason,omitempty"`
	Version   string `json:"version,omitempty"`
}

// ConfigEditor edits the reloadable config files (pathways, credentials,
// callers, permeability). A nil ConfigEditor makes the edit endpoints return
// 503, as when the daemon's config is not reloadable.
type ConfigEditor interface {
	// Kinds returns the editable config kinds.
	Kinds() []string
	// Read returns the current raw content of a kind and its version token.
	Read(kind string) (EditFile, error)
	// Validate parses raw with the kind's loader without writing.
	Validate(kind string, raw []byte) ValidationResult
	// Apply validates raw, checks the version token, writes atomically, and
	// triggers a reload.
	Apply(kind string, raw []byte, version string) ApplyResult

	// ValidateForm renders a JSON form into the kind's YAML and validates it
	// without writing. The form is merged into the current file so comments and
	// fields the form does not carry (hashes, rotation, next_secret_ref) are
	// kept.
	ValidateForm(kind string, form []byte) ValidationResult
	// ApplyForm renders a form as ValidateForm does, then applies it as Apply
	// does.
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
