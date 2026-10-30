package console

// The SecretManager port is defined here so the console imports no store
// package; internal/consolestore implements it. Secret values are write-only:
// an operator may set or rotate one, but reads return metadata only.

// SecretMetaView is the metadata of one sealed secret. Referenced reports
// whether a credential uses it as secret:<name>, which blocks deletion.
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

// SecretManager lists, sets, and deletes sealed secrets. No method returns a
// plaintext value. A nil SecretManager, as when no master key is configured,
// makes the secrets endpoints return 503.
type SecretManager interface {
	// List returns metadata for all sealed secrets (never a value).
	List() (SecretsView, error)
	// Set creates or rotates the sealed secret named name with the given value.
	Set(name, value string) error
	// Delete removes a sealed secret, reporting whether a row was removed. It
	// returns an error when the secret is still referenced by a credential.
	Delete(name string) (bool, error)
}
