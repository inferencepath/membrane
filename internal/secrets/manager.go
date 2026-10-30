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

// nameRe matches the shape used for credential and keyring names, so any valid
// name can appear in a secret:<name> reference.
var nameRe = regexp.MustCompile(`^[a-z][a-z0-9_-]*$`)

// Manager encrypts secret values with a Keeper and stores only the ciphertext.
// It is the one place that turns a secret name into a plaintext value.
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

// Set creates or rotates the secret called name. Each call bumps the stored
// version.
func (m *Manager) Set(name, value string) error {
	row, err := m.SealRow(name, value)
	if err != nil {
		return err
	}
	_, err = m.st.PutSecret(row)
	return err
}

// SealRow validates and seals a value into a store.SecretRow without storing
// it, so the caller can write it in the same transaction as another row, such
// as a provider definition.
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

// Resolve returns the plaintext value for a "secret:<name>" reference. The
// Exterior Keyring uses it for the secret: scheme.
func (m *Manager) Resolve(ref string) (string, error) {
	scheme, name, ok := strings.Cut(ref, ":")
	if !ok || scheme != RefScheme {
		return "", fmt.Errorf("secrets: not a %s: reference: %q", RefScheme, ref)
	}
	return m.Get(name)
}

// List returns secret metadata (names, versions, timestamps) but no values.
func (m *Manager) List() ([]store.SecretMeta, error) {
	return m.st.ListSecretMeta()
}

// Delete removes a sealed secret. It reports whether a row was removed.
func (m *Manager) Delete(name string) (bool, error) {
	return m.st.DeleteSecret(name)
}
