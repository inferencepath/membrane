package store_test

import (
	"path/filepath"
	"testing"

	"github.com/inferencepath/membrane/internal/store"
)

// seedValid opens a store holding a valid single-pathway configuration.
func seedValid(t *testing.T) *store.Store {
	t.Helper()
	s, err := store.Open(filepath.Join(t.TempDir(), store.DBFileName))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	db := s.DB()
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := db.Exec(q, args...); err != nil {
			t.Fatalf("exec %q: %v", q, err)
		}
	}
	exec(`INSERT INTO pathways (name, cascade_allowed, default_provider) VALUES ('chat', 0, 'main')`)
	exec(`INSERT INTO providers (name, family, endpoint, models, default_model, secret_ref)
	      VALUES ('main', 'openai', 'https://api.openai.com', 'gpt-4o-mini', 'gpt-4o-mini', 'env:TEST_STORE_KEY')`)
	exec(`INSERT INTO pathway_providers (pathway, provider) VALUES ('chat', 'main')`)
	exec(`INSERT INTO credentials (name, secret_ref) VALUES ('main', 'env:TEST_STORE_KEY')`)
	exec(`INSERT INTO callers (key_id, caller_id, name, secret_hash)
	      VALUES ('ck_demo', 'acme-app', 'demo', '458d6534a760ed4c8c2f1c8f86a517572cb56fa6a0f8554efdda2cb8532a4816')`)
	exec(`INSERT INTO caller_allow (key_id, pathway) VALUES ('ck_demo', 'chat')`)
	exec(`INSERT INTO permeability (pathway, outbound_allow, inbound_default, inbound_on_reveal)
	      VALUES ('chat', 'messages', 'message` + "\n" + `finish_reason` + "\n" + `usage', 'provider` + "\n" + `model')`)
	return s
}

func TestReadPathwaysWithTargets(t *testing.T) {
	s := seedValid(t)
	defer s.Close()

	caps, err := s.ReadPathways()
	if err != nil {
		t.Fatalf("read pathways: %v", err)
	}
	if len(caps) != 1 || caps[0].Name != "chat" || caps[0].DefaultProvider != "main" {
		t.Fatalf("caps = %+v", caps)
	}
	if len(caps[0].Providers) != 1 || caps[0].Providers[0] != "main" {
		t.Fatalf("providers = %+v", caps[0].Providers)
	}
}

func TestReadCredentialsAndCallers(t *testing.T) {
	s := seedValid(t)
	defer s.Close()

	creds, err := s.ReadCredentials()
	if err != nil || len(creds) != 1 || creds[0].SecretRef != "env:TEST_STORE_KEY" {
		t.Fatalf("creds = %+v (err %v)", creds, err)
	}
	callers, err := s.ReadCallers()
	if err != nil || len(callers) != 1 {
		t.Fatalf("callers = %+v (err %v)", callers, err)
	}
	if callers[0].KeyID != "ck_demo" || len(callers[0].Allow) != 1 || callers[0].Allow[0] != "chat" {
		t.Errorf("caller = %+v", callers[0])
	}
}

func TestReadPermeabilitySplitsLists(t *testing.T) {
	s := seedValid(t)
	defer s.Close()

	perm, err := s.ReadPermeability()
	if err != nil || len(perm) != 1 {
		t.Fatalf("perm = %+v (err %v)", perm, err)
	}
	p := perm[0]
	if len(p.OutboundAllow) != 1 || p.OutboundAllow[0] != "messages" {
		t.Errorf("outbound = %v", p.OutboundAllow)
	}
	if len(p.InboundDefault) != 3 {
		t.Errorf("inbound default = %v", p.InboundDefault)
	}
	if len(p.InboundOnReveal) != 2 {
		t.Errorf("inbound on_reveal = %v", p.InboundOnReveal)
	}
}

func TestJoinListRoundTrip(t *testing.T) {
	// Values stored with JoinList must read back unchanged.
	s := openTemp(t)
	defer s.Close()
	if _, err := s.DB().Exec(
		`INSERT INTO permeability (pathway, outbound_allow, inbound_default)
		 VALUES ('chat', ?, ?)`,
		store.JoinList([]string{"messages", "model"}), store.JoinList([]string{"message"}),
	); err != nil {
		t.Fatalf("insert: %v", err)
	}
	perm, err := s.ReadPermeability()
	if err != nil || len(perm) != 1 {
		t.Fatalf("read: %+v (err %v)", perm, err)
	}
	if len(perm[0].OutboundAllow) != 2 || perm[0].OutboundAllow[1] != "model" {
		t.Errorf("round-trip outbound = %v", perm[0].OutboundAllow)
	}
}

func TestIsConfigured(t *testing.T) {
	empty := openTemp(t)
	defer empty.Close()
	if ok, err := empty.IsConfigured(); err != nil || ok {
		t.Fatalf("fresh store IsConfigured = %v (err %v), want false", ok, err)
	}

	seeded := seedValid(t)
	defer seeded.Close()
	if ok, err := seeded.IsConfigured(); err != nil || !ok {
		t.Fatalf("seeded store IsConfigured = %v (err %v), want true", ok, err)
	}
}
