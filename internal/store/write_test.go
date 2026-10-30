package store_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/inferencepath/membrane/internal/store"
)

func TestWriteAndRenderPathways(t *testing.T) {
	s := openTemp(t)
	defer s.Close()

	if err := s.WriteProviders([]store.ProviderRow{
		{Name: "main", Family: "openai", Endpoint: "https://api.openai.com", Models: []string{"gpt-4o"}, DefaultModel: "gpt-4o", SecretRef: "env:A"},
		{Name: "backup", Family: "openai", Endpoint: "https://api.openai.com", Models: []string{"gpt-4o"}, DefaultModel: "gpt-4o", SecretRef: "env:B"},
	}); err != nil {
		t.Fatalf("seed providers: %v", err)
	}
	rows := []store.PathwayRow{{
		Name:            "chat",
		CascadeAllowed:  true,
		DefaultProvider: "main",
		Providers:       []string{"main", "backup"},
	}}
	if err := s.WritePathways(rows); err != nil {
		t.Fatalf("write: %v", err)
	}
	back, err := s.ReadPathways()
	if err != nil || len(back) != 1 || len(back[0].Providers) != 2 {
		t.Fatalf("read back = %+v (err %v)", back, err)
	}
	if !back[0].CascadeAllowed || back[0].DefaultProvider != "main" {
		t.Errorf("cascade/default = %+v", back[0])
	}
	doc, err := s.RenderPathways()
	if err != nil || !strings.Contains(string(doc), "default_provider: main") {
		t.Fatalf("render caps = %s (err %v)", doc, err)
	}
	// Writing fewer rows removes the rest.
	if err := s.WritePathways(rows[:0]); err != nil {
		t.Fatalf("write empty: %v", err)
	}
	if back, _ := s.ReadPathways(); len(back) != 0 {
		t.Fatalf("expected empty after replace, got %+v", back)
	}
}

func TestPathwayOperationRoundTrip(t *testing.T) {
	s := openTemp(t)
	defer s.Close()

	if err := s.WriteProviders([]store.ProviderRow{
		{Name: "main", Family: "openai", Endpoint: "https://api.openai.com", Models: []string{"m"}, DefaultModel: "m", SecretRef: "env:A"},
	}); err != nil {
		t.Fatalf("seed providers: %v", err)
	}
	rows := []store.PathwayRow{
		{Name: "chat", DefaultProvider: "main", Providers: []string{"main"}}, // no operation means chat
		{Name: "embed", Operation: "embeddings", DefaultProvider: "main", Providers: []string{"main"}},
	}
	if err := s.WritePathways(rows); err != nil {
		t.Fatalf("write: %v", err)
	}
	back, err := s.ReadPathways()
	if err != nil || len(back) != 2 {
		t.Fatalf("read back = %+v (err %v)", back, err)
	}
	byName := map[string]store.PathwayRow{}
	for _, r := range back {
		byName[r.Name] = r
	}
	if byName["chat"].Operation != "chat" {
		t.Errorf("chat operation = %q, want chat (defaulted)", byName["chat"].Operation)
	}
	if byName["embed"].Operation != "embeddings" {
		t.Errorf("embed operation = %q, want embeddings", byName["embed"].Operation)
	}
	// The default operation (chat) is omitted from the YAML; embeddings is not.
	doc, err := s.RenderPathways()
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if !strings.Contains(string(doc), "operation: embeddings") {
		t.Errorf("render should include 'operation: embeddings': %s", doc)
	}
}

func TestWriteAndRenderCredentials(t *testing.T) {
	s := openTemp(t)
	defer s.Close()
	rows := []store.CredentialRow{
		{Name: "main", SecretRef: "env:A"},
		{Name: "next", SecretRef: "file:/k", NextSecretRef: "env:B"},
	}
	if err := s.WriteCredentials(rows); err != nil {
		t.Fatalf("write: %v", err)
	}
	doc, err := s.RenderCredentials()
	if err != nil || !strings.Contains(string(doc), "env:A") || !strings.Contains(string(doc), "next_secret_ref") {
		t.Fatalf("render creds = %s (err %v)", doc, err)
	}
}

func TestWriteAndRenderCallers(t *testing.T) {
	s := openTemp(t)
	defer s.Close()
	hash := "458d6534a760ed4c8c2f1c8f86a517572cb56fa6a0f8554efdda2cb8532a4816"
	rows := []store.CallerRow{{KeyID: "ck1", CallerID: "acme", Name: "demo", SecretHash: hash, Allow: []string{"chat", "embed"}}}
	if err := s.WriteCallers(rows); err != nil {
		t.Fatalf("write: %v", err)
	}
	back, err := s.ReadCallers()
	if err != nil || len(back) != 1 || len(back[0].Allow) != 2 {
		t.Fatalf("read back = %+v (err %v)", back, err)
	}
	doc, err := s.RenderCallers()
	if err != nil || !strings.Contains(string(doc), hash) {
		t.Fatalf("render callers = %s (err %v)", doc, err)
	}
}

func TestWriteAndRenderPermeability(t *testing.T) {
	s := openTemp(t)
	defer s.Close()
	rows := []store.PermeabilityRow{{
		Pathway: "chat", OutboundAllow: []string{"messages"},
		InboundDefault: []string{"message", "usage"}, InboundOnReveal: []string{"provider"},
	}}
	if err := s.WritePermeability(rows); err != nil {
		t.Fatalf("write: %v", err)
	}
	doc, err := s.RenderPermeability()
	if err != nil || !strings.Contains(string(doc), "messages") {
		t.Fatalf("render perm = %s (err %v)", doc, err)
	}
}

func TestSetCallerAllow(t *testing.T) {
	s := seedValid(t)
	defer s.Close()

	if err := s.SetCallerAllow("ck_demo", []string{"chat", "embed"}); err != nil {
		t.Fatalf("set allow: %v", err)
	}
	back, _ := s.ReadCallers()
	if len(back) != 1 || len(back[0].Allow) != 2 {
		t.Fatalf("allow = %+v", back)
	}

	if err := s.SetCallerAllow("ck_missing", []string{"chat"}); !errors.Is(err, store.ErrUnknownKeyID) {
		t.Fatalf("expected ErrUnknownKeyID, got %v", err)
	}
}

func TestRenderDispatchAndKinds(t *testing.T) {
	s := seedValid(t)
	defer s.Close()
	for _, k := range store.Kinds() {
		if _, err := s.Render(k); err != nil {
			t.Fatalf("render %q: %v", k, err)
		}
	}
	if _, err := s.Render("bogus"); !errors.Is(err, store.ErrUnknownKind) {
		t.Fatalf("expected ErrUnknownKind, got %v", err)
	}
}

func TestContentHashStableAndSensitive(t *testing.T) {
	s := seedValid(t)
	defer s.Close()
	h1, err := s.ContentHash()
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	h2, _ := s.ContentHash()
	if h1 != h2 {
		t.Fatalf("hash not stable")
	}
	if err := s.SetCallerAllow("ck_demo", []string{"chat", "embed"}); err != nil {
		t.Fatalf("mutate: %v", err)
	}
	h3, _ := s.ContentHash()
	if h3 == h1 {
		t.Fatalf("hash should change after mutation")
	}
}
