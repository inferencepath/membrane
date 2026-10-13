package consolestore

import (
	"fmt"
	"strings"

	"github.com/inferencepath/membrane/internal/secrets"
	"github.com/inferencepath/membrane/internal/server/console"
	"github.com/inferencepath/membrane/internal/store"
)

// SecretAdapter implements console.SecretManager: the app-layer bridge the
// Console uses to set/rotate, list, and delete sealed secrets. It is a thin shim
// over secrets.Manager (seal/persist) and the store (reference lookups). Values
// are write-only — List returns metadata only, never a plaintext value.
type SecretAdapter struct {
	st  *store.Store
	mgr *secrets.Manager
}

var _ console.SecretManager = (*SecretAdapter)(nil)

// NewSecretAdapter returns a SecretAdapter over the store and sealed-secret
// Manager. It is wired into the Console only when the sealed store is enabled (a
// master key is configured); otherwise the Console's Secrets port stays nil and
// the endpoints report 503.
func NewSecretAdapter(st *store.Store, mgr *secrets.Manager) *SecretAdapter {
	return &SecretAdapter{st: st, mgr: mgr}
}

// List returns sealed-secret metadata, annotating each with whether a credential
// references it as `secret:<name>` (via secret_ref or next_secret_ref) so the
// Console can guard deletes. It never returns a value.
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

// Set creates or rotates the sealed secret named name with the given value. The
// plaintext is sealed before it reaches the store and is never persisted or
// returned in clear.
func (a *SecretAdapter) Set(name, value string) error {
	return a.mgr.Set(name, value)
}

// Delete removes a sealed secret, refusing while any credential still references
// it (by secret_ref or next_secret_ref) so a `secret:<name>` reference never
// dangles. It reports whether a row was removed.
func (a *SecretAdapter) Delete(name string) (bool, error) {
	referenced, err := a.referencedNames()
	if err != nil {
		return false, err
	}
	if referenced[name] {
		return false, fmt.Errorf("secret %q is referenced by a credential; detach it first", name)
	}
	return a.mgr.Delete(name)
}

// referencedNames returns the set of secret names referenced by any credential,
// through either its primary secret_ref or its rotation next_secret_ref.
func (a *SecretAdapter) referencedNames() (map[string]bool, error) {
	rows, err := a.st.DB().Query(`SELECT secret_ref, next_secret_ref FROM credentials`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make(map[string]bool)
	for rows.Next() {
		var ref, next string
		if err := rows.Scan(&ref, &next); err != nil {
			return nil, err
		}
		if name, ok := secretRefName(ref); ok {
			out[name] = true
		}
		if name, ok := secretRefName(next); ok {
			out[name] = true
		}
	}
	return out, rows.Err()
}

// secretRefName extracts the <name> from a `secret:<name>` reference, reporting
// whether ref is a secret-scheme reference. Non-secret references (env:/file:/
// vault:/empty) return ("", false).
func secretRefName(ref string) (string, bool) {
	scheme, name, ok := strings.Cut(ref, ":")
	if !ok || scheme != secrets.RefScheme || name == "" {
		return "", false
	}
	return name, true
}
