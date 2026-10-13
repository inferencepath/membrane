package config_test

import (
	"strings"
	"testing"

	"github.com/inferencepath/membrane/internal/config"
)

func TestParseSecretsMasterKeyRef(t *testing.T) {
	cfg, err := config.Parse([]byte(`
data_dir: "/var/lib/membrane"
secrets:
  master_key_ref: "env:MEMBRANE_SECRET_KEY"
`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Secrets.MasterKeyRef != "env:MEMBRANE_SECRET_KEY" {
		t.Fatalf("master_key_ref = %q", cfg.Secrets.MasterKeyRef)
	}
}

func TestParseSecretsDefaultEmpty(t *testing.T) {
	cfg, err := config.Parse([]byte(`data_dir: "/var/lib/membrane"`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Secrets.MasterKeyRef != "" {
		t.Fatalf("expected empty master_key_ref by default, got %q", cfg.Secrets.MasterKeyRef)
	}
}

func TestParseSecretsRejectsInlineMasterKey(t *testing.T) {
	_, err := config.Parse([]byte(`
data_dir: "/var/lib/membrane"
secrets:
  master_key_ref: "literally-a-key"
`))
	if err == nil || !strings.Contains(err.Error(), "master_key_ref") {
		t.Fatalf("expected inline-secret rejection, got %v", err)
	}
}

func TestParseSecretsAllowsFileAndVaultRefs(t *testing.T) {
	for _, ref := range []string{"file:/run/secrets/master", "vault:kv/membrane#master"} {
		cfg, err := config.Parse([]byte(`
data_dir: "/var/lib/membrane"
secrets:
  master_key_ref: "` + ref + `"
`))
		if err != nil {
			t.Fatalf("ref %q: unexpected error: %v", ref, err)
		}
		if cfg.Secrets.MasterKeyRef != ref {
			t.Fatalf("ref %q not preserved: %q", ref, cfg.Secrets.MasterKeyRef)
		}
	}
}
