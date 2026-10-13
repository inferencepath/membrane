package store_test

import (
	"strings"
	"testing"

	"github.com/inferencepath/membrane/internal/store"
)

func TestRenderBundleRoundTrip(t *testing.T) {
	s := seedValid(t)
	defer s.Close()

	raw, err := s.RenderBundle()
	if err != nil {
		t.Fatalf("render bundle: %v", err)
	}
	for _, want := range []string{"providers", "pathways", "credentials", "callers", "permeability", "env:TEST_STORE_KEY"} {
		if !strings.Contains(string(raw), want) {
			t.Errorf("bundle missing %q:\n%s", want, raw)
		}
	}

	b, err := store.ParseBundle(raw)
	if err != nil {
		t.Fatalf("parse bundle: %v", err)
	}
	if len(b.Providers) != 1 || len(b.Pathways) != 1 || len(b.Credentials) != 1 || len(b.Callers) != 1 || len(b.Permeability) != 1 {
		t.Fatalf("round-trip lost rows: %+v", b)
	}
	if b.Pathways[0].Name != "chat" || len(b.Pathways[0].Providers) != 1 {
		t.Errorf("pathways = %+v", b.Pathways)
	}
	if b.Callers[0].SecretHash == "" {
		t.Error("caller hash lost in round-trip")
	}
}

func TestBundleVersionOfStableAndSensitive(t *testing.T) {
	v1 := store.BundleVersionOf([]byte("a"))
	if v1 == "" || v1 != store.BundleVersionOf([]byte("a")) {
		t.Fatalf("version not stable: %q", v1)
	}
	if v1 == store.BundleVersionOf([]byte("b")) {
		t.Fatal("version should differ for different content")
	}
}

func TestRenderKindFromBundle(t *testing.T) {
	s := seedValid(t)
	defer s.Close()
	b, err := s.ReadBundle()
	if err != nil {
		t.Fatalf("read bundle: %v", err)
	}
	for _, kind := range store.Kinds() {
		doc, err := store.RenderKindFromBundle(kind, b)
		if err != nil || len(doc) == 0 {
			t.Fatalf("render kind %q = %q (err %v)", kind, doc, err)
		}
		// Must match the store's own single-kind render for the same data.
		want, _ := s.Render(kind)
		if string(doc) != string(want) {
			t.Fatalf("kind %q render mismatch:\nfrom bundle=%s\nfrom store =%s", kind, doc, want)
		}
	}
	if _, err := store.RenderKindFromBundle("bogus", b); err != store.ErrUnknownKind {
		t.Fatalf("expected ErrUnknownKind, got %v", err)
	}
}

func TestParseBundleRejectsUnknownField(t *testing.T) {
	if _, err := store.ParseBundle([]byte("version: 1\nbogus: true\n")); err == nil {
		t.Fatal("expected error for unknown field")
	}
}

func TestWriteBundleReplacesAllKinds(t *testing.T) {
	s := seedValid(t)
	defer s.Close()

	b := store.Bundle{
		Providers: []store.ProviderRow{{
			Name: "p1", Family: "openai", Endpoint: "https://api.openai.com",
			Models: []string{"text-embedding-3-small"}, DefaultModel: "text-embedding-3-small", SecretRef: "env:OTHER",
		}},
		Pathways: []store.PathwayRow{{
			Name:            "embed",
			DefaultProvider: "p1",
			Providers:       []string{"p1"},
		}},
		Credentials:  []store.CredentialRow{{Name: "k2", SecretRef: "env:OTHER"}},
		Callers:      []store.CallerRow{{KeyID: "ck2", CallerID: "team", SecretHash: "ff", Allow: []string{"embed"}}},
		Permeability: []store.PermeabilityRow{{Pathway: "embed", OutboundAllow: []string{"input"}, InboundDefault: []string{"usage"}}},
	}
	if err := s.WriteBundle(b); err != nil {
		t.Fatalf("write bundle: %v", err)
	}

	caps, _ := s.ReadPathways()
	if len(caps) != 1 || caps[0].Name != "embed" {
		t.Fatalf("pathways not replaced: %+v", caps)
	}
	creds, _ := s.ReadCredentials()
	if len(creds) != 1 || creds[0].Name != "k2" {
		t.Fatalf("credentials not replaced: %+v", creds)
	}
	callers, _ := s.ReadCallers()
	if len(callers) != 1 || callers[0].KeyID != "ck2" {
		t.Fatalf("callers not replaced: %+v", callers)
	}
	perm, _ := s.ReadPermeability()
	if len(perm) != 1 || perm[0].Pathway != "embed" {
		t.Fatalf("permeability not replaced: %+v", perm)
	}
}

func TestWriteBundleAtomicOnFailure(t *testing.T) {
	s := seedValid(t)
	defer s.Close()

	before, _ := s.RenderBundle()

	// A target referencing a missing pathway violates the foreign key, so the
	// whole bundle write must roll back — leaving the original state intact.
	bad := store.Bundle{
		Providers:   []store.ProviderRow{{Name: "p1", Family: "openai", Endpoint: "https://x", Models: []string{"m"}, DefaultModel: "m", SecretRef: "env:X"}},
		Pathways:    []store.PathwayRow{{Name: "ok", DefaultProvider: "p1", Providers: []string{"p1"}}},
		Credentials: []store.CredentialRow{{Name: "k", SecretRef: "env:X"}},
		Callers: []store.CallerRow{{
			KeyID: "ck", CallerID: "c", SecretHash: "ab",
			Allow: []string{"ok"},
		}},
	}
	// Corrupt an allow row to reference a non-existent caller by writing callers
	// with an allow for a key that will not exist — instead force a FK error via
	// a permeability row is not FK-constrained, so inject via a caller_allow with
	// a bogus key through a raw path is not possible here. Use a duplicate PK
	// instead: two pathways with the same name violates the primary key.
	bad.Pathways = append(bad.Pathways, store.PathwayRow{Name: "ok", DefaultProvider: "p1", Providers: []string{"p1"}})

	if err := s.WriteBundle(bad); err == nil {
		t.Fatal("expected write to fail on duplicate pathway name")
	}
	after, _ := s.RenderBundle()
	if string(before) != string(after) {
		t.Fatalf("store mutated after failed bundle write:\nbefore=%s\nafter=%s", before, after)
	}
}
