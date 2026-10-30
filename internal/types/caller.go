package types

// Caller is a trusted application. It is Interior-only and never crosses the
// Membrane.
type Caller struct {
	// ID is the stable Caller identifier.
	ID string
	// Name is a human-readable label.
	Name string
	// AllowedPathways is filled in during authorization.
	AllowedPathways []PathwayID
}

// CallerKeyRecord is a stored Caller Key entry. It is Interior-only. Only the
// public KeyID and a hash of the secret are kept, never the plaintext key.
type CallerKeyRecord struct {
	// KeyID is the public identifier of the key.
	KeyID string
	// Hash is the hash of the secret.
	Hash string
	// CallerID is the owning Caller.
	CallerID string
}
