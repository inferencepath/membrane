package types

// Caller is a trusted enterprise application. Interior-only: a Caller never
// crosses the Membrane.
type Caller struct {
	// ID is the stable Caller identifier.
	ID string
	// Name is a human-readable label.
	Name string
	// AllowedPathways is populated by later phases (Authorize).
	AllowedPathways []PathwayID
}

// CallerKeyRecord is a stored Caller Key entry. Interior-only. The plaintext key
// is never stored; only the public KeyID and a hash of the secret are kept.
type CallerKeyRecord struct {
	// KeyID is the public identifier of the key.
	KeyID string
	// Hash is the hash of the secret; the plaintext is never persisted at rest.
	Hash string
	// CallerID is the owning Caller.
	CallerID string
}
