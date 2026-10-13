package admit_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/inferencepath/membrane/internal/interior/admit"
)

func storeYAML(t *testing.T) []byte {
	t.Helper()
	hash := admit.HashSecret("s3cr3t-demo-key")
	return []byte("callers:\n  - key_id: ck_demo\n    caller_id: acme-app\n    name: Acme\n    hash: " + hash + "\n")
}

func TestListReturnsPublicInfoNoSecrets(t *testing.T) {
	hash := admit.HashSecret("s3cr3t-demo-key")
	data := []byte("version: 1\n" +
		"callers:\n" +
		"  - key_id: ck_b\n    caller_id: beta\n    hash: " + hash + "\n    allow: [chat, summarize]\n" +
		"  - key_id: ck_a\n    caller_id: alpha\n    name: Alpha\n    hash: " + hash + "\n")
	store, err := admit.ParseStore(data)
	if err != nil {
		t.Fatalf("ParseStore: %v", err)
	}
	list := store.List()
	if len(list) != 2 {
		t.Fatalf("list len = %d", len(list))
	}
	// Sorted by KeyID: ck_a before ck_b.
	if list[0].KeyID != "ck_a" || list[1].KeyID != "ck_b" {
		t.Fatalf("order = %q, %q", list[0].KeyID, list[1].KeyID)
	}
	if list[0].CallerID != "alpha" || list[0].Name != "Alpha" {
		t.Fatalf("entry[0] = %+v", list[0])
	}
	// Allow list is sorted and complete.
	if len(list[1].Allow) != 2 || list[1].Allow[0] != "chat" || list[1].Allow[1] != "summarize" {
		t.Fatalf("allow = %+v", list[1].Allow)
	}
}

func TestVerifyValid(t *testing.T) {
	store, err := admit.ParseStore(storeYAML(t))
	if err != nil {
		t.Fatalf("ParseStore: %v", err)
	}
	caller, err := store.Verify("ck_demo.s3cr3t-demo-key")
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if caller.ID != "acme-app" || caller.Name != "Acme" {
		t.Fatalf("caller = %+v", caller)
	}
}

func TestVerifyRejections(t *testing.T) {
	store, err := admit.ParseStore(storeYAML(t))
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name, key string
	}{
		{"empty", ""},
		{"no separator", "ck_demosecret"},
		{"empty key id", ".secret"},
		{"empty secret", "ck_demo."},
		{"unknown key id", "ck_other.s3cr3t-demo-key"},
		{"wrong secret", "ck_demo.wrong"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := store.Verify(tc.key); !errors.Is(err, admit.ErrUnauthorized) {
				t.Fatalf("Verify(%q) err = %v, want ErrUnauthorized", tc.key, err)
			}
		})
	}
}

func TestParseStoreInvalid(t *testing.T) {
	cases := []struct {
		name, yaml, want string
	}{
		{"missing fields", "callers:\n  - key_id: a\n", "key_id, hash, and caller_id are required"},
		{"bad hash", "callers:\n  - key_id: a\n    caller_id: c\n    hash: nothex\n", "hex-encoded sha256"},
		{"duplicate", "callers:\n  - key_id: a\n    caller_id: c\n    hash: " + admit.HashSecret("x") + "\n  - key_id: a\n    caller_id: d\n    hash: " + admit.HashSecret("y") + "\n", "duplicate key_id"},
		{"unknown field", "callers:\n  - key_id: a\n    bogus: 1\n", "parse"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := admit.ParseStore([]byte(tc.yaml))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want substring %q", err, tc.want)
			}
		})
	}
}

func TestParseStoreEmptyIsDenyAll(t *testing.T) {
	// An empty caller store is valid (deny-by-default); it authenticates nobody
	// so routing can be configured before any Caller Key exists.
	for _, y := range []string{"callers: []\n", "version: 1\ncallers: []\n", "version: 1\n"} {
		store, err := admit.ParseStore([]byte(y))
		if err != nil {
			t.Fatalf("ParseStore(%q) empty store should be valid, got %v", y, err)
		}
		if _, err := store.Verify("ck_demo.s3cr3t-demo-key"); !errors.Is(err, admit.ErrUnauthorized) {
			t.Fatalf("empty store must reject every key, got %v", err)
		}
		if store.Allows("acme-app", "chat") {
			t.Fatalf("empty store must authorize nothing")
		}
	}
}

func TestLoadStore(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "callers.yaml")
	if err := os.WriteFile(path, storeYAML(t), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := admit.LoadStore(path); err != nil {
		t.Fatalf("LoadStore: %v", err)
	}
	if _, err := admit.LoadStore(filepath.Join(dir, "missing.yaml")); err == nil {
		t.Fatalf("expected error for missing file")
	}
}

func TestHashSecretStable(t *testing.T) {
	if admit.HashSecret("abc") != admit.HashSecret("abc") {
		t.Fatal("hash not stable")
	}
	if len(admit.HashSecret("abc")) != 64 {
		t.Fatalf("hash length = %d, want 64", len(admit.HashSecret("abc")))
	}
}

func TestAllowDenyByDefault(t *testing.T) {
	hash := admit.HashSecret("s3cr3t-demo-key")
	// Caller with an explicit allowlist.
	y := "version: 1\ncallers:\n  - key_id: ck_demo\n    caller_id: acme-app\n    hash: " + hash +
		"\n    allow: [chat, summarize]\n"
	store, err := admit.ParseStore([]byte(y))
	if err != nil {
		t.Fatalf("ParseStore: %v", err)
	}
	if !store.Allows("acme-app", "chat") || !store.Allows("acme-app", "summarize") {
		t.Fatalf("allowed pathways must pass")
	}
	if store.Allows("acme-app", "embeddings") {
		t.Fatalf("non-allowed pathway must be denied")
	}
	if store.Allows("ghost", "chat") {
		t.Fatalf("unknown caller must be denied")
	}
}

func TestAllowEmptyDeniesEverything(t *testing.T) {
	hash := admit.HashSecret("s3cr3t-demo-key")
	// No allow key => deny-by-default.
	y := "callers:\n  - key_id: ck_demo\n    caller_id: acme-app\n    hash: " + hash + "\n"
	store, err := admit.ParseStore([]byte(y))
	if err != nil {
		t.Fatalf("ParseStore: %v", err)
	}
	if store.Allows("acme-app", "chat") {
		t.Fatalf("absent allowlist must deny every pathway")
	}
}

func TestLimitsParsing(t *testing.T) {
	hash := admit.HashSecret("s3cr3t-demo-key")
	y := "version: 1\ncallers:\n  - key_id: ck_demo\n    caller_id: acme-app\n    hash: " + hash +
		"\n    allow: [summarize]\n    limits:\n      summarize: {requests: 60, window: 1m}\n"
	store, err := admit.ParseStore([]byte(y))
	if err != nil {
		t.Fatalf("ParseStore: %v", err)
	}
	rl, ok := store.Limit("acme-app", "summarize")
	if !ok || rl.Requests != 60 || rl.Window.String() != "1m0s" {
		t.Fatalf("limit = %+v ok=%v", rl, ok)
	}
	if _, ok := store.Limit("acme-app", "chat"); ok {
		t.Fatalf("unconfigured pathway must have no limit")
	}
}

func TestParseStoreAuthzRejects(t *testing.T) {
	hash := admit.HashSecret("s3cr3t-demo-key")
	base := "callers:\n  - key_id: ck_demo\n    caller_id: acme-app\n    hash: " + hash
	cases := []struct{ name, yaml, want string }{
		{"bad version", "version: 2\n" + base + "\n", "version must be 1"},
		{"bad allow id", base + "\n    allow: [Chat]\n", "invalid allow pathway"},
		{"bad limit cap", base + "\n    limits:\n      Chat: {requests: 1, window: 1m}\n", "invalid limit pathway"},
		{"zero requests", base + "\n    limits:\n      chat: {requests: 0, window: 1m}\n", "requests must be >= 1"},
		{"bad window", base + "\n    limits:\n      chat: {requests: 1, window: nope}\n", "window"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := admit.ParseStore([]byte(tc.yaml)); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want contains %q", err, tc.want)
			}
		})
	}
}
