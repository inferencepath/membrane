package secrets

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/inferencepath/membrane/internal/store"
)

// RefScheme is the secret-reference scheme backed by the sealed store. A
// credential whose secret_ref is "secret:<name>" resolves its value from here.
const RefScheme = "secret"

// ErrNotFound indicates no sealed secret exists under the given name.
var ErrNotFound = errors.New("secrets: not found")

// nameRe constrains secret names to the same shape as credential/keyring names,
// so a value can be referenced as `secret:<name>` without surprises.
var nameRe = regexp.MustCompile(`^[a-z][a-z0-9_-]*$`)

// Manager is the sealed secret store API: it encrypts values with a Keeper and
// persists only ciphertext via the embedded store. Plaintext is never written to
// disk or logged. It is the single place that turns a secret name into a value.
type Manager struct {
	st     *store.Store
	keeper *Keeper
	now    func() time.Time
}

// Option configures a Manager.
type Option func(*Manager)

// WithClock overrides the timestamp clock (for deterministic tests).
func WithClock(now func() time.Time) Option {
	return func(m *Manager) {
		if now != nil {
			m.now = now
		}
	}
}

// NewManager returns a Manager over the store, sealing with keeper.
func NewManager(st *store.Store, keeper *Keeper, opts ...Option) *Manager {
	m := &Manager{st: st, keeper: keeper, now: time.Now}
	for _, o := range opts {
		o(m)
	}
	return m
}

// Set creates or rotates the sealed secret named name with the given plaintext
// value. Each Set bumps the stored value version. The plaintext is sealed before
// it ever reaches the store.
func (m *Manager) Set(name, value string) error {
	row, err := m.SealRow(name, value)
	if err != nil {
		return err
	}
	_, err = m.st.PutSecret(row)
	return err
}

// SealRow validates and seals a value into a store.SecretRow WITHOUT persisting
// it. It lets a caller write the sealed secret together with another row (e.g. a
// provider's credential) in a single store transaction. The plaintext never
// leaves this call; only ciphertext + nonce are returned.
func (m *Manager) SealRow(name, value string) (store.SecretRow, error) {
	if !nameRe.MatchString(name) {
		return store.SecretRow{}, fmt.Errorf("secrets: invalid name %q (must match %s)", name, nameRe.String())
	}
	if value == "" {
		return store.SecretRow{}, errors.New("secrets: value must not be empty")
	}
	ct, nonce, err := m.keeper.Seal([]byte(value))
	if err != nil {
		return store.SecretRow{}, err
	}
	return store.SecretRow{
		Name:       name,
		Ciphertext: ct,
		Nonce:      nonce,
		KeyVersion: m.keeper.KeyVersion(),
		UpdatedAt:  m.now().UTC().Format(time.RFC3339),
	}, nil
}

// Get returns the plaintext value for a sealed secret, or ErrNotFound.
func (m *Manager) Get(name string) (string, error) {
	row, ok, err := m.st.GetSecret(name)
	if err != nil {
		return "", err
	}
	if !ok {
		return "", ErrNotFound
	}
	pt, err := m.keeper.Open(row.Ciphertext, row.Nonce)
	if err != nil {
		return "", err
	}
	return string(pt), nil
}

// Resolve resolves a "secret:<name>" reference to its plaintext value. It is the
// resolver the Exterior keyring is wired to for the secret: scheme.
func (m *Manager) Resolve(ref string) (string, error) {
	scheme, name, ok := strings.Cut(ref, ":")
	if !ok || scheme != RefScheme {
		return "", fmt.Errorf("secrets: not a %s: reference: %q", RefScheme, ref)
	}
	return m.Get(name)
}

// List returns secret metadata (names, versions, timestamps) — never a value.
func (m *Manager) List() ([]store.SecretMeta, error) {
	return m.st.ListSecretMeta()
}

// Delete removes a sealed secret. It reports whether a row was removed.
func (m *Manager) Delete(name string) (bool, error) {
	return m.st.DeleteSecret(name)
}
