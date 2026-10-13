// Package keyring holds pooled Provider credentials and resolves them to an
// Egress Identity by reference (the credential's name). It fails closed — an
// unresolvable primary reference at startup means that credential's egress path
// is not enabled.
//
// The Keyring supports zero-downtime secret-VALUE rotation (not config-file
// reload): it periodically (and on demand, e.g. SIGHUP) re-resolves each reference
// from its backend. When a `next_secret_ref` is configured and resolvable, its
// value becomes active (rotation forward); otherwise the primary reference's
// current value is used. Resolution returns a copy, so in-flight requests are
// never disturbed by a rotation.
//
// Secrets exist ONLY by reference (env:/file:/vault:); a resolved secret value
// is never logged. This package is Exterior and must not import Interior
// packages.
package keyring

import (
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/inferencepath/membrane/internal/exterior/providers"
)

// Entry is a single named credential: the reference used to resolve its current
// secret value, plus an optional pre-staged next reference for rotation.
type Entry struct {
	Name          string
	SecretRef     string
	NextSecretRef string
}

// RefResolver resolves a secret reference to its current value. It is injectable
// so tests can supply a fake backend with controllable, rotating values.
type RefResolver func(ref string) (string, error)

type ringEntry struct {
	name       string
	primaryRef string
	nextRef    string

	mu          sync.RWMutex
	active      providers.EgressIdentity
	loaded      bool
	lastRefresh time.Time
}

// Keyring resolves Egress Identities by credential name, with value rotation.
type Keyring struct {
	resolver RefResolver
	now      func() time.Time
	interval time.Duration
	entries  map[string]*ringEntry
}

// Option configures a Keyring.
type Option func(*Keyring)

// WithRefResolver overrides the secret-reference resolver (for tests/backends).
func WithRefResolver(fn RefResolver) Option { return func(k *Keyring) { k.resolver = fn } }

// WithClock overrides the clock (for deterministic rotation tests).
func WithClock(now func() time.Time) Option { return func(k *Keyring) { k.now = now } }

// WithRotationInterval sets how often a reference is re-resolved on access. Zero
// disables periodic rotation (references are resolved once at startup).
func WithRotationInterval(d time.Duration) Option { return func(k *Keyring) { k.interval = d } }

// New resolves every entry's primary (or rotated) reference and returns a
// Keyring. It fails closed: if any entry cannot be resolved at startup, an error
// is returned and no Keyring is produced, so the egress path is never enabled
// with a missing credential.
func New(entries []Entry, opts ...Option) (*Keyring, error) {
	k := &Keyring{
		resolver: resolveRef,
		now:      time.Now,
		entries:  make(map[string]*ringEntry, len(entries)),
	}
	for _, o := range opts {
		o(k)
	}
	for _, e := range entries {
		if e.Name == "" {
			return nil, fmt.Errorf("keyring: entry has empty name")
		}
		if _, dup := k.entries[e.Name]; dup {
			return nil, fmt.Errorf("keyring: duplicate entry name %q", e.Name)
		}
		re := &ringEntry{name: e.Name, primaryRef: e.SecretRef, nextRef: e.NextSecretRef}
		if err := k.refreshEntry(re); err != nil {
			return nil, fmt.Errorf("keyring: %q: %w", e.Name, err)
		}
		k.entries[e.Name] = re
	}
	return k, nil
}

// Resolve returns the current Egress Identity for a credential name, or an error
// if none is held (fail closed). If periodic rotation is enabled and the
// refresh interval has elapsed, the reference is re-resolved first; a failed
// re-resolution keeps the last-good value so requests do not fail during a
// transient backend outage.
func (k *Keyring) Resolve(name string) (providers.EgressIdentity, error) {
	re, ok := k.entries[name]
	if !ok {
		return providers.EgressIdentity{}, fmt.Errorf("keyring: no egress identity for %q", name)
	}
	if k.interval > 0 {
		re.mu.RLock()
		due := k.now().Sub(re.lastRefresh) >= k.interval
		re.mu.RUnlock()
		if due {
			_ = k.refreshEntry(re) // keep last-good on error
		}
	}
	re.mu.RLock()
	defer re.mu.RUnlock()
	if !re.loaded {
		return providers.EgressIdentity{}, fmt.Errorf("keyring: no egress identity for %q", name)
	}
	return re.active, nil
}

// Refresh re-resolves every credential immediately (e.g. on SIGHUP). It returns
// the first error encountered but still refreshes the remaining entries; entries
// that fail keep their last-good value.
func (k *Keyring) Refresh() error {
	var firstErr error
	for _, re := range k.entries {
		if err := k.refreshEntry(re); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// refreshEntry re-resolves a single entry, preferring a resolvable next_secret_ref
// (rotation forward) over the primary reference.
func (k *Keyring) refreshEntry(re *ringEntry) error {
	val, err := k.resolver(re.primaryRef)
	if re.nextRef != "" {
		if nv, nerr := k.resolver(re.nextRef); nerr == nil && nv != "" {
			val, err = nv, nil
		}
	}
	if err != nil {
		return err
	}
	re.mu.Lock()
	re.active = providers.EgressIdentity{Secret: val}
	re.loaded = true
	re.lastRefresh = k.now()
	re.mu.Unlock()
	return nil
}

// ResolveRef resolves a base secret reference (env:/file:) to its value. It is
// the default resolver and is exported so the composition root can compose it
// with the sealed-store resolver for the `secret:` scheme (delegating non-secret
// schemes here) without duplicating env/file handling.
func ResolveRef(ref string) (string, error) { return resolveRef(ref) }

// resolveRef resolves a secret reference to its value. Supported schemes:
//   - env:VAR_NAME   → the value of the environment variable
//   - file:/path     → the trimmed contents of the file
//
// Inline secret values and unknown schemes are rejected. (vault: is validated by
// the config loader but not dereferenced here.)
func resolveRef(ref string) (string, error) {
	scheme, rest, ok := strings.Cut(ref, ":")
	if !ok {
		return "", fmt.Errorf("invalid secret reference (must be env:/file:)")
	}
	switch scheme {
	case "env":
		v := os.Getenv(rest)
		if v == "" {
			return "", fmt.Errorf("env reference %q is empty or unset", rest)
		}
		return v, nil
	case "file":
		b, err := os.ReadFile(rest)
		if err != nil {
			return "", fmt.Errorf("read file reference: %w", err)
		}
		v := strings.TrimSpace(string(b))
		if v == "" {
			return "", fmt.Errorf("file reference %q is empty", rest)
		}
		return v, nil
	default:
		return "", fmt.Errorf("unsupported secret reference scheme %q (use env:/file:)", scheme)
	}
}
