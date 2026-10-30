package config_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/inferencepath/membrane/internal/config"
)

const validConfig = `
server:
  address: "127.0.0.1:9090"
  shutdown_timeout: "15s"
data_dir: "/var/lib/membrane"
`

func TestParseValid(t *testing.T) {
	cfg, err := config.Parse([]byte(validConfig))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Server.Address != "127.0.0.1:9090" {
		t.Errorf("address = %q", cfg.Server.Address)
	}
	if cfg.Server.ShutdownTimeout != 15*time.Second {
		t.Errorf("shutdown = %v", cfg.Server.ShutdownTimeout)
	}
	if cfg.DataDir != "/var/lib/membrane" {
		t.Errorf("data_dir = %q", cfg.DataDir)
	}
}

func TestParseConsoleDefaultsDisabled(t *testing.T) {
	cfg, err := config.Parse([]byte(validConfig))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Console.Enabled {
		t.Errorf("console should be disabled by default")
	}
	if cfg.Console.Address != "127.0.0.1:8300" {
		t.Errorf("default console address = %q", cfg.Console.Address)
	}
}

func TestParseConsoleEnabled(t *testing.T) {
	cfg, err := config.Parse([]byte(validConfig + "" +
		"console:\n" +
		"  enabled: true\n" +
		"  address: \"0.0.0.0:9000\"\n" +
		"  auth_token_ref: \"env:MEMBRANE_CONSOLE_TOKEN\"\n"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !cfg.Console.Enabled {
		t.Errorf("console should be enabled")
	}
	if cfg.Console.Address != "0.0.0.0:9000" {
		t.Errorf("console address = %q", cfg.Console.Address)
	}
	if cfg.Console.AuthTokenRef != "env:MEMBRANE_CONSOLE_TOKEN" {
		t.Errorf("console auth_token_ref = %q", cfg.Console.AuthTokenRef)
	}
}

func TestParseConsoleRejectsInlineToken(t *testing.T) {
	_, err := config.Parse([]byte(validConfig + "" +
		"console:\n" +
		"  enabled: true\n" +
		"  auth_token_ref: \"super-secret-literal\"\n"))
	if err == nil || !strings.Contains(err.Error(), "console.auth_token_ref") {
		t.Fatalf("expected inline-secret rejection, got %v", err)
	}
}

func TestParseRequiresDataDir(t *testing.T) {
	// A missing data_dir is an error that names the field.
	_, err := config.Parse([]byte("server:\n  address: \":8080\"\n"))
	if err == nil || !strings.Contains(err.Error(), "data_dir") {
		t.Fatalf("expected data_dir requirement error, got %v", err)
	}
}

func TestParseDefaults(t *testing.T) {
	cfg, err := config.Parse([]byte("data_dir: \"/data\"\n"))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Server.Address != ":8080" {
		t.Errorf("default address = %q", cfg.Server.Address)
	}
	if cfg.Server.ShutdownTimeout != 30*time.Second {
		t.Errorf("default shutdown = %v", cfg.Server.ShutdownTimeout)
	}
}

func TestParseInvalid(t *testing.T) {
	cases := []struct {
		name    string
		yaml    string
		wantErr string
	}{
		{"missing data_dir", "server:\n  address: \":1\"\n", "data_dir is required"},
		{"bad shutdown", "data_dir: /d\nserver:\n  shutdown_timeout: notaduration\n", "shutdown_timeout"},
		{"unknown field", "data_dir: /d\nbogus: 1\n", "parse"},
		{"malformed", "data_dir: /d\naudit: [unclosed\n", "parse"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := config.Parse([]byte(tc.yaml))
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", tc.wantErr)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("error = %q, want substring %q", err.Error(), tc.wantErr)
			}
		})
	}
}

func TestLoad(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "membrane.yaml")
	if err := os.WriteFile(path, []byte(validConfig), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := config.Load(path); err != nil {
		t.Fatalf("Load valid: %v", err)
	}
	if _, err := config.Load(filepath.Join(dir, "nope.yaml")); err == nil {
		t.Fatalf("expected read error")
	}
}
