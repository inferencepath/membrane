package app

// ReloadResult is the outcome of a reload attempt. A rejected candidate leaves
// Applied and Unchanged false and sets Reason; the last-good configuration
// keeps serving.
type ReloadResult struct {
	// Applied is true when a new, valid Snapshot was swapped in.
	Applied bool
	// Unchanged is true when the configuration matched the active one. Live
	// health and breaker state are kept.
	Unchanged bool
	// Reason is a secret-free explanation when a reload is rejected.
	Reason string
}

type nopWriter struct{}

func (nopWriter) Write(p []byte) (int, error) { return len(p), nil }
