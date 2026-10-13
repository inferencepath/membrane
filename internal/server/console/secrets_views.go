package console

// This file defines the console's sealed-secret management port and its view
// types. As with the other ports, the types live in the console package (so the
// console imports no store/secrets package) and the concrete SecretManager is
// implemented in internal/consolestore and injected. Secret VALUES are write-only
// from the console's perspective: an operator may set or rotate a value, but the
// API never returns one — reads surface metadata only (name, version, when, and
// whether a credential references it).

// SecretMetaView is the secret-free projection of one sealed secret: its name,
// versions, last-updated timestamp, and whether any credential references it as
// `secret:<name>` (used to guard deletes). It never carries a value.
type SecretMetaView struct {
	Name       string `json:"name"`
	Version    int    `json:"version"`
	KeyVersion int    `json:"keyVersion"`
	UpdatedAt  string `json:"updatedAt,omitempty"`
	Referenced bool   `json:"referenced"`
}

// SecretsView is the set of sealed secret metadata records (never values).
type SecretsView struct {
	Secrets []SecretMetaView `json:"secrets"`
}

// SecretManager is the sealed-secret management port: list metadata, set/rotate a
// value, and delete. A nil SecretManager disables the secrets endpoints (they
// return 503) — e.g. when no master key is configured (sealed store disabled).
// The port is deliberately write-only for values: no method returns a plaintext
// secret.
type SecretManager interface {
	// List returns metadata for all sealed secrets (never a value).
	List() (SecretsView, error)
	// Set creates or rotates the sealed secret named name with the given value.
	Set(name, value string) error
	// Delete removes a sealed secret, reporting whether a row was removed. It
	// returns an error when the secret is still referenced by a credential.
	Delete(name string) (bool, error)
}
