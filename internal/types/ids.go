package types

// PathwayID is the logical identifier of a Pathway (e.g. "chat"). It names
// intent, never a vendor, and may be surfaced to a Caller.
type PathwayID string

// ProviderID identifies an external Provider (e.g. "openai"). Exterior-visible.
type ProviderID string
