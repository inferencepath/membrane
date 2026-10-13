// Package config loads the Membrane configuration. It is parse-only:
// it validates structure and references but never contacts Providers or secrets
// backends. Secrets are represented only by reference; inline secrets are
// rejected.
package config

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"regexp"
	"time"

	"gopkg.in/yaml.v3"
)

const (
	defaultAddress         = ":8080"
	defaultShutdownTimeout = 30 * time.Second
	// defaultConsoleAddress binds the Membrane Console to loopback by default, so
	// it is reachable from the host without extra credentials but not exposed to
	// the network unless the operator explicitly rebinds it (and sets a token).
	defaultConsoleAddress = "127.0.0.1:7474"
)

// secretRefRe requires every secret reference to use a known scheme, so inline
// secret values cannot be configured.
var secretRefRe = regexp.MustCompile(`^(env|file|vault):.+`)

// Config is the parsed Membrane configuration. Membrane runs in a single,
// managed operating model: the daemon owns its runtime config as data in an
// embedded state store under DataDir. This top-level file carries only bootstrap
// settings (server, console, audit, data_dir) — all operational config
// (pathways, credentials, callers, permeability) lives in the store and is
// edited via the Console or a declarative bundle.
type Config struct {
	Server ServerConfig
	// DataDir is the directory for the daemon's managed control-plane state (the
	// embedded SQLite store). Required: it is the single source of truth for
	// runtime config.
	DataDir string
	// Audit configures the Interior-only usage audit sink.
	Audit AuditConfig
	// Console configures the Membrane Console operator UI (separate admin
	// listener, off the egress path). Disabled by default.
	Console ConsoleConfig
	// Secrets configures the sealed secret store (the master key that unseals
	// provider credentials stored via the Console as `secret:<name>` references).
	Secrets SecretsConfig
}

// SecretsConfig configures the sealed secret store. The master key is referenced
// (env:/file:/vault:), never inline, and lives OUTSIDE the embedded database so a
// stolen db file alone cannot reveal any sealed secret. When MasterKeyRef is
// empty, the sealed store is disabled and any `secret:<name>` credential
// reference fails closed (the egress path stays not-ready until configured).
type SecretsConfig struct {
	// MasterKeyRef references the master key material used to derive the AEAD key
	// for sealing/unsealing stored secrets. Reference-only (env:/file:/vault:).
	MasterKeyRef string
}

// ConsoleConfig configures the Membrane Console: a built-in operator UI served on
// a separate admin listener, never on the data-plane (egress) mux. The console is
// disabled by default; when enabled it binds loopback unless the operator rebinds
// Address, in which case AuthTokenRef (reference-only) gates non-local access.
type ConsoleConfig struct {
	// Enabled turns the admin listener on. When false, no console listener is
	// opened and the data plane behaves exactly as without the console.
	Enabled bool
	// Address is the admin listener bind address (default 127.0.0.1:7474).
	Address string
	// AuthTokenRef references the admin token (env:/file:/vault:), never inline.
	// Empty means localhost-only access (non-local requests are refused).
	AuthTokenRef string
}

// AuditConfig selects the Interior-only usage audit sink. The zero value
// (Sink == "" or "discard") disables auditing (records are discarded).
type AuditConfig struct {
	// Sink is "discard" (default) or "jsonlines".
	Sink string
	// Path is the jsonlines destination: "stdout" (default), "stderr", or a file
	// path. Ignored for the discard sink.
	Path string
}

// ServerConfig configures the HTTP server shell.
type ServerConfig struct {
	Address         string
	ShutdownTimeout time.Duration
}

type fileConfig struct {
	Server  *serverYAML  `yaml:"server"`
	DataDir string       `yaml:"data_dir"`
	Audit   *auditYAML   `yaml:"audit"`
	Console *consoleYAML `yaml:"console"`
	Secrets *secretsYAML `yaml:"secrets"`
}

type secretsYAML struct {
	MasterKeyRef string `yaml:"master_key_ref"`
}

type consoleYAML struct {
	Enabled      bool   `yaml:"enabled"`
	Address      string `yaml:"address"`
	AuthTokenRef string `yaml:"auth_token_ref"`
}

type auditYAML struct {
	Sink string `yaml:"sink"`
	Path string `yaml:"path"`
}

type serverYAML struct {
	Address         string `yaml:"address"`
	ShutdownTimeout string `yaml:"shutdown_timeout"`
}

// Load reads and validates the Membrane config at path. Parse-only.
func Load(path string) (Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Config{}, fmt.Errorf("config: read %s: %w", path, err)
	}
	return Parse(data)
}

// Parse validates raw YAML bytes into a Config, rejecting unknown fields and
// inline secrets.
func Parse(data []byte) (Config, error) {
	var fc fileConfig
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&fc); err != nil {
		return Config{}, fmt.Errorf("config: parse: %w", err)
	}

	// Managed mode is the only operating model: the daemon owns its runtime config
	// as data in the embedded store under data_dir, which is therefore required.
	if fc.DataDir == "" {
		return Config{}, errors.New("config: data_dir is required (the managed state store directory)")
	}

	cfg := Config{
		Server:  ServerConfig{Address: defaultAddress, ShutdownTimeout: defaultShutdownTimeout},
		DataDir: fc.DataDir,
	}

	if fc.Audit != nil {
		sink := fc.Audit.Sink
		switch sink {
		case "", "discard":
			cfg.Audit = AuditConfig{Sink: "discard"}
		case "jsonlines":
			path := fc.Audit.Path
			if path == "" {
				path = "stdout"
			}
			cfg.Audit = AuditConfig{Sink: "jsonlines", Path: path}
		default:
			return Config{}, fmt.Errorf("config: audit.sink %q is invalid (use discard or jsonlines)", sink)
		}
	}

	cfg.Console = ConsoleConfig{Address: defaultConsoleAddress}
	if fc.Console != nil {
		cfg.Console.Enabled = fc.Console.Enabled
		if fc.Console.Address != "" {
			cfg.Console.Address = fc.Console.Address
		}
		if fc.Console.AuthTokenRef != "" {
			if !secretRefRe.MatchString(fc.Console.AuthTokenRef) {
				return Config{}, fmt.Errorf("config: console.auth_token_ref: inline secrets are not allowed; use a reference scheme (env:/file:/vault:)")
			}
			cfg.Console.AuthTokenRef = fc.Console.AuthTokenRef
		}
	}

	if fc.Secrets != nil && fc.Secrets.MasterKeyRef != "" {
		if !secretRefRe.MatchString(fc.Secrets.MasterKeyRef) {
			return Config{}, fmt.Errorf("config: secrets.master_key_ref: inline secrets are not allowed; use a reference scheme (env:/file:/vault:)")
		}
		cfg.Secrets.MasterKeyRef = fc.Secrets.MasterKeyRef
	}

	if fc.Server != nil {
		if fc.Server.Address != "" {
			cfg.Server.Address = fc.Server.Address
		}
		if fc.Server.ShutdownTimeout != "" {
			d, err := time.ParseDuration(fc.Server.ShutdownTimeout)
			if err != nil {
				return Config{}, fmt.Errorf("config: server.shutdown_timeout: %w", err)
			}
			cfg.Server.ShutdownTimeout = d
		}
	}

	return cfg, nil
}
