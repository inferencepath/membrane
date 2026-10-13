package app

// ReloadResult is the outcome of a configuration reload attempt. Exactly one of
// Applied/Unchanged is true on success; Reason carries a secret-free explanation
// when a candidate is rejected (the last-good configuration keeps serving).
type ReloadResult struct {
	// Applied is true when a new, valid Snapshot was swapped in.
	Applied bool
	// Unchanged is true when the configuration was identical to the active one
	// (a no-op reload that preserves live health/breaker state).
	Unchanged bool
	// Reason is a secret-free explanation when a reload is rejected.
	Reason string
}

// nopWriter drops all output; used for the default no-op logger.
type nopWriter struct{}

func (nopWriter) Write(p []byte) (int, error) { return len(p), nil }
