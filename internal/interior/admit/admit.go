// Package admit authenticates Callers by their Caller Key. It is Interior-only;
// the authenticated Caller context never crosses the Membrane.
//
// A Caller Key is presented as "<key_id>.<secret>". The store holds a hash of
// the secret, not the plaintext. Verification looks the key up by its public
// KeyID and compares the hash of the presented secret in constant time.
package admit

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/inferencepath/membrane/internal/types"
)

// ErrUnauthorized is returned for any authentication failure. It does not say
// which part failed.
var ErrUnauthorized = errors.New("admit: unauthorized")

// pathwayRe matches a logical Pathway ID.
var pathwayRe = regexp.MustCompile(`^[a-z][a-z0-9_.]*$`)

type record struct {
	hash     string
	callerID string
	name     string
}

// RateLimit is an optional per-Pathway fixed-window limit. This package parses
// and stores it but does not enforce it.
type RateLimit struct {
	Requests int
	Window   time.Duration
}

// Store holds Caller Key records, keyed by public KeyID, plus the deny-by-default
// authorization allowlist and optional rate limits, indexed by Interior CallerID.
type Store struct {
	byKeyID map[string]record
	// allow maps a CallerID to the Pathways it may use. A CallerID that is
	// absent or has an empty set is authorized for nothing.
	allow map[string]map[types.PathwayID]struct{}
	// limits maps a CallerID to its per-Pathway rate limits.
	limits map[string]map[types.PathwayID]RateLimit
}

type fileStore struct {
	Version int          `yaml:"version"`
	Callers []callerYAML `yaml:"callers"`
}

type callerYAML struct {
	KeyID    string                   `yaml:"key_id"`
	Hash     string                   `yaml:"hash"`
	CallerID string                   `yaml:"caller_id"`
	Name     string                   `yaml:"name"`
	Allow    []string                 `yaml:"allow"`
	Limits   map[string]rateLimitYAML `yaml:"limits"`
}

type rateLimitYAML struct {
	Requests int    `yaml:"requests"`
	Window   string `yaml:"window"`
}

// LoadStore reads and validates a Caller Key store from path.
func LoadStore(path string) (*Store, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("admit: read %s: %w", path, err)
	}
	return ParseStore(data)
}

// ParseStore validates raw YAML bytes into a Store. The store accepts secret
// hashes only, never plaintext secrets.
func ParseStore(data []byte) (*Store, error) {
	var fs fileStore
	dec := yaml.NewDecoder(strings.NewReader(string(data)))
	dec.KnownFields(true)
	if err := dec.Decode(&fs); err != nil {
		return nil, fmt.Errorf("admit: parse: %w", err)
	}
	// version is optional so older stores still load; if present it must be 1.
	if fs.Version != 0 && fs.Version != 1 {
		return nil, fmt.Errorf("admit: version must be 1, got %d", fs.Version)
	}
	// An empty caller store is valid. No key authenticates, so every request is
	// rejected, but operators can configure routing first and add Caller Keys
	// later.
	byKeyID := make(map[string]record, len(fs.Callers))
	allow := make(map[string]map[types.PathwayID]struct{}, len(fs.Callers))
	limits := make(map[string]map[types.PathwayID]RateLimit)
	for i, c := range fs.Callers {
		if c.KeyID == "" || c.Hash == "" || c.CallerID == "" {
			return nil, fmt.Errorf("admit: callers[%d]: key_id, hash, and caller_id are required", i)
		}
		if _, err := hex.DecodeString(c.Hash); err != nil || len(c.Hash) != sha256.Size*2 {
			return nil, fmt.Errorf("admit: callers[%d]: hash must be a hex-encoded sha256 digest", i)
		}
		if _, dup := byKeyID[c.KeyID]; dup {
			return nil, fmt.Errorf("admit: duplicate key_id %q", c.KeyID)
		}
		byKeyID[c.KeyID] = record{hash: strings.ToLower(c.Hash), callerID: c.CallerID, name: c.Name}

		// Keys that share a CallerID share the union of their allowlists.
		set := allow[c.CallerID]
		if set == nil {
			set = make(map[types.PathwayID]struct{})
			allow[c.CallerID] = set
		}
		for _, a := range c.Allow {
			if !pathwayRe.MatchString(a) {
				return nil, fmt.Errorf("admit: callers[%d] (%s): invalid allow pathway %q", i, c.KeyID, a)
			}
			set[types.PathwayID(a)] = struct{}{}
		}
		for capName, rl := range c.Limits {
			if !pathwayRe.MatchString(capName) {
				return nil, fmt.Errorf("admit: callers[%d] (%s): invalid limit pathway %q", i, c.KeyID, capName)
			}
			if rl.Requests < 1 {
				return nil, fmt.Errorf("admit: callers[%d] (%s): limit %q: requests must be >= 1", i, c.KeyID, capName)
			}
			d, err := time.ParseDuration(rl.Window)
			if err != nil {
				return nil, fmt.Errorf("admit: callers[%d] (%s): limit %q: window: %w", i, c.KeyID, capName, err)
			}
			if limits[c.CallerID] == nil {
				limits[c.CallerID] = make(map[types.PathwayID]RateLimit)
			}
			limits[c.CallerID][types.PathwayID(capName)] = RateLimit{Requests: rl.Requests, Window: d}
		}
	}
	return &Store{byKeyID: byKeyID, allow: allow, limits: limits}, nil
}

// CallerInfo is a view of a stored Caller Key for operator tooling such as the
// Membrane Console. It has the public KeyID, the Interior CallerID, the
// optional display name, and the allowlist, but no secret or hash.
type CallerInfo struct {
	KeyID    string
	CallerID string
	Name     string
	Allow    []types.PathwayID
}

// List returns the stored callers sorted by KeyID, each with the allowlist of
// its CallerID.
func (s *Store) List() []CallerInfo {
	out := make([]CallerInfo, 0, len(s.byKeyID))
	for keyID, rec := range s.byKeyID {
		allow := make([]types.PathwayID, 0, len(s.allow[rec.callerID]))
		for cap := range s.allow[rec.callerID] {
			allow = append(allow, cap)
		}
		sort.Slice(allow, func(i, j int) bool { return allow[i] < allow[j] })
		out = append(out, CallerInfo{KeyID: keyID, CallerID: rec.callerID, Name: rec.name, Allow: allow})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].KeyID < out[j].KeyID })
	return out
}

// Allows reports whether the given CallerID is authorized for the Pathway.
// An unknown CallerID, or one with no allowlist, is authorized for nothing.
func (s *Store) Allows(callerID string, pathway types.PathwayID) bool {
	set, ok := s.allow[callerID]
	if !ok {
		return false
	}
	_, ok = set[pathway]
	return ok
}

// Limit returns the per-Pathway rate limit for a CallerID and whether one is
// configured.
func (s *Store) Limit(callerID string, pathway types.PathwayID) (RateLimit, bool) {
	byCap, ok := s.limits[callerID]
	if !ok {
		return RateLimit{}, false
	}
	rl, ok := byCap[pathway]
	return rl, ok
}

// Verify authenticates a presented Caller Key ("<key_id>.<secret>") and returns
// the authenticated Caller, or ErrUnauthorized. The hash comparison is
// constant-time.
func (s *Store) Verify(presented string) (types.Caller, error) {
	keyID, secret, ok := strings.Cut(presented, ".")
	if !ok || keyID == "" || secret == "" {
		return types.Caller{}, ErrUnauthorized
	}
	rec, ok := s.byKeyID[keyID]
	if !ok {
		// Hash anyway so an unknown key takes about as long as a wrong secret.
		_ = HashSecret(secret)
		return types.Caller{}, ErrUnauthorized
	}
	got := HashSecret(secret)
	if subtle.ConstantTimeCompare([]byte(got), []byte(rec.hash)) != 1 {
		return types.Caller{}, ErrUnauthorized
	}
	return types.Caller{ID: rec.callerID, Name: rec.name}, nil
}

// HashSecret returns the lowercase hex-encoded SHA-256 of a Caller Key secret.
// Tooling uses it to generate store entries.
func HashSecret(secret string) string {
	sum := sha256.Sum256([]byte(secret))
	return hex.EncodeToString(sum[:])
}
