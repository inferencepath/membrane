package consolestore

import (
	"fmt"

	"github.com/inferencepath/membrane/internal/secrets"
	"github.com/inferencepath/membrane/internal/server/console"
	"github.com/inferencepath/membrane/internal/store"
)

// SecretAdapter implements console.SecretManager over the sealed secret store.
// Values are write-only: no method returns plaintext.
type SecretAdapter struct {
	st  *store.Store
	mgr *secrets.Manager
}

var _ console.SecretManager = (*SecretAdapter)(nil)

// NewSecretAdapter returns a SecretAdapter over st and mgr. It is only wired
// in when a master key is configured; otherwise the Console secrets endpoints
// return 503.
func NewSecretAdapter(st *store.Store, mgr *secrets.Manager) *SecretAdapter {
	return &SecretAdapter{st: st, mgr: mgr}
}

// List returns secret metadata, marking each secret that a credential
// references as secret:<name>.
func (a *SecretAdapter) List() (console.SecretsView, error) {
	metas, err := a.mgr.List()
	if err != nil {
		return console.SecretsView{}, err
	}
	referenced, err := a.referencedNames()
	if err != nil {
		return console.SecretsView{}, err
	}
	out := console.SecretsView{Secrets: make([]console.SecretMetaView, 0, len(metas))}
	for _, m := range metas {
		out.Secrets = append(out.Secrets, console.SecretMetaView{
			Name:       m.Name,
			Version:    m.Version,
			KeyVersion: m.KeyVersion,
			UpdatedAt:  m.UpdatedAt,
			Referenced: referenced[m.Name],
		})
	}
	return out, nil
}

// Set creates or rotates a sealed secret value.
func (a *SecretAdapter) Set(name, value string) error {
	return a.mgr.Set(name, value)
}

// Delete removes a sealed secret and reports whether a row was removed. It
// refuses while a credential still references the secret.
func (a *SecretAdapter) Delete(name string) (bool, error) {
	referenced, err := a.referencedNames()
	if err != nil {
		return false, err
	}
	if referenced[name] {
		return false, fmt.Errorf("secret %q is referenced by a credential; remove the reference first", name)
	}
	return a.mgr.Delete(name)
}

// referencedNames returns the secret names that any credential references.
func (a *SecretAdapter) referencedNames() (map[string]bool, error) {
	creds, err := a.st.ReadCredentials()
	if err != nil {
		return nil, err
	}
	out := make(map[string]bool, len(creds))
	for _, c := range creds {
		for _, ref := range []string{c.SecretRef, c.NextSecretRef} {
			if name, ok := secretRefName(ref); ok {
				out[name] = true
			}
		}
	}
	return out, nil
}

// secretRefName returns name from a "secret:<name>" reference.
func secretRefName(ref string) (string, bool) {
	const prefix = secrets.RefScheme + ":"
	if len(ref) > len(prefix) && ref[:len(prefix)] == prefix {
		return ref[len(prefix):], true
	}
	return "", false
}
