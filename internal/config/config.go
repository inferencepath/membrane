// Package config parses and validates Membrane configuration. It never
// contacts Providers or secret backends. Secrets appear only as references;
// inline secrets are rejected.
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
	// defaultConsoleAddress binds the Console to loopback, so it is not exposed
	// to the network unless the operator rebinds it and sets a token.
	defaultConsoleAddress = "127.0.0.1:8300"
)

// secretRefRe requires every secret reference to use a known scheme, so inline
// secret values cannot be configured.
var secretRefRe = regexp.MustCompile(`^(env|file|vault):.+`)

// Config is the bootstrap configuration: server, Console, audit and data
// directory settings. Pathways, credentials, callers and Permeability live in
// the embedded store under DataDir and are edited through the Console or a
// bundle.
type Config struct {
	Server ServerConfig
	// DataDir holds the embedded SQLite store. It is required.
	DataDir string
	// Audit configures the Interior usage audit sink.
	Audit AuditConfig
	// Console configures the Console, which runs on its own admin listener.
	Console ConsoleConfig
	// Secrets configures the master key for the sealed secret store.
	Secrets SecretsConfig
}

// SecretsConfig configures the sealed secret store. The master key is given as
// a reference and kept outside the database. If MasterKeyRef is empty the
// sealed store is disabled and any secret:<name> reference fails closed.
type SecretsConfig struct {
	// MasterKeyRef references the master key material (env:, file: or vault:).
	MasterKeyRef string
}

// ConsoleConfig configures the Console. It is served on a separate admin
// listener, never on the egress mux, and is disabled by default. Unless an
// AuthTokenRef is set, only local requests are accepted.
type ConsoleConfig struct {
	// Enabled turns the admin listener on.
	Enabled bool
	// Address is the admin listener bind address (default 127.0.0.1:8300).
	Address string
	// AuthTokenRef references the admin token (env:, file: or vault:). If
	// empty, non-local requests are refused.
	AuthTokenRef string
}

// AuditConfig selects the Interior usage audit sink. An empty Sink or
// "discard" disables auditing.
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

// Load reads and validates the Membrane config at path.
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
