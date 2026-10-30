package keyring_test

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/inferencepath/membrane/internal/exterior/keyring"
)

func TestResolveEnvRef(t *testing.T) {
	t.Setenv("KR_TEST_SECRET", "token-123")
	kr, err := keyring.New([]keyring.Entry{{Name: "openai-main", SecretRef: "env:KR_TEST_SECRET"}})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	id, err := kr.Resolve("openai-main")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if id.Secret != "token-123" {
		t.Fatalf("secret = %q", id.Secret)
	}
}

func TestResolveFileRef(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "secret")
	if err := os.WriteFile(path, []byte("  file-token\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	kr, err := keyring.New([]keyring.Entry{{Name: "anthropic-main", SecretRef: "file:" + path}})
	if err != nil {
		t.Fatal(err)
	}
	id, _ := kr.Resolve("anthropic-main")
	if id.Secret != "file-token" {
		t.Fatalf("secret = %q (want trimmed)", id.Secret)
	}
}

func TestResolveEmptyFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "empty")
	if err := os.WriteFile(path, []byte("   \n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := keyring.New([]keyring.Entry{{Name: "openai-main", SecretRef: "file:" + path}}); err == nil ||
		!strings.Contains(err.Error(), "is empty") {
		t.Fatalf("err = %v, want file-is-empty", err)
	}
}

func TestResolveUnknownName(t *testing.T) {
	kr, err := keyring.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := kr.Resolve("openai-main"); err == nil {
		t.Fatal("expected error for unknown name (fail closed)")
	}
}

func TestNewFailClosed(t *testing.T) {
	cases := []struct {
		name, ref, want string
	}{
		{"empty env", "env:DEFINITELY_UNSET_VAR_XYZ", "empty or unset"},
		{"missing file", "file:/no/such/file", "read file reference"},
		{"inline secret", "sk-inline-value", "invalid secret reference"},
		{"unknown scheme", "vault:kv/x", "unsupported secret reference scheme"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := keyring.New([]keyring.Entry{{Name: "openai-main", SecretRef: tc.ref}})
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want substring %q", err, tc.want)
			}
		})
	}
}

func TestNewEmptyName(t *testing.T) {
	if _, err := keyring.New([]keyring.Entry{{Name: "", SecretRef: "env:X"}}); err == nil {
		t.Fatal("expected error for empty name")
	}
}

func TestNewDuplicateName(t *testing.T) {
	t.Setenv("KR_DUP", "v")
	_, err := keyring.New([]keyring.Entry{
		{Name: "dup", SecretRef: "env:KR_DUP"},
		{Name: "dup", SecretRef: "env:KR_DUP"},
	})
	if err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Fatalf("err = %v, want duplicate", err)
	}
}

// fakeBackend is a concurrency-safe secret backend whose values tests can change.
type fakeBackend struct {
	mu   sync.Mutex
	vals map[string]string
}

func (b *fakeBackend) set(ref, val string) {
	b.mu.Lock()
	b.vals[ref] = val
	b.mu.Unlock()
}

func (b *fakeBackend) resolve(ref string) (string, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	v, ok := b.vals[ref]
	if !ok || v == "" {
		return "", fmt.Errorf("backend: %q unset", ref)
	}
	return v, nil
}

func TestRotationWithinInterval(t *testing.T) {
	be := &fakeBackend{vals: map[string]string{"vault:k": "secret-v1"}}
	now := time.Unix(0, 0)
	kr, err := keyring.New(
		[]keyring.Entry{{Name: "main", SecretRef: "vault:k"}},
		keyring.WithRefResolver(be.resolve),
		keyring.WithClock(func() time.Time { return now }),
		keyring.WithRotationInterval(30*time.Second),
	)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	id, _ := kr.Resolve("main")
	if id.Secret != "secret-v1" {
		t.Fatalf("initial secret = %q", id.Secret)
	}

	// Operator rotates the backend value; before the interval elapses the old
	// value is still served (in-flight requests unaffected).
	be.set("vault:k", "secret-v2")
	now = now.Add(10 * time.Second)
	if id, _ := kr.Resolve("main"); id.Secret != "secret-v1" {
		t.Fatalf("pre-interval secret = %q, want v1", id.Secret)
	}

	// After the interval, new requests pick up the rotated value.
	now = now.Add(30 * time.Second)
	if id, _ := kr.Resolve("main"); id.Secret != "secret-v2" {
		t.Fatalf("post-interval secret = %q, want v2", id.Secret)
	}
}

func TestRotationPrefersNextRef(t *testing.T) {
	be := &fakeBackend{vals: map[string]string{"vault:cur": "cur-v1"}}
	kr, err := keyring.New(
		[]keyring.Entry{{Name: "main", SecretRef: "vault:cur", NextSecretRef: "vault:next"}},
		keyring.WithRefResolver(be.resolve),
	)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if id, _ := kr.Resolve("main"); id.Secret != "cur-v1" {
		t.Fatalf("secret = %q, want cur-v1 (next not yet available)", id.Secret)
	}

	// Stage the next secret and force a refresh (e.g. SIGHUP): it takes over.
	be.set("vault:next", "next-v2")
	if err := kr.Refresh(); err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	if id, _ := kr.Resolve("main"); id.Secret != "next-v2" {
		t.Fatalf("secret = %q, want next-v2", id.Secret)
	}
}

func TestRefreshKeepsLastGoodOnBackendOutage(t *testing.T) {
	be := &fakeBackend{vals: map[string]string{"vault:k": "good"}}
	kr, err := keyring.New(
		[]keyring.Entry{{Name: "main", SecretRef: "vault:k"}},
		keyring.WithRefResolver(be.resolve),
	)
	if err != nil {
		t.Fatal(err)
	}
	// Backend goes dark; Refresh reports an error but the last-good value stands.
	be.set("vault:k", "")
	if err := kr.Refresh(); err == nil {
		t.Fatal("want refresh error on backend outage")
	}
	if id, _ := kr.Resolve("main"); id.Secret != "good" {
		t.Fatalf("secret = %q, want last-good 'good'", id.Secret)
	}
}
