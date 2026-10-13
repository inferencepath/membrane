// Package config also loads the Keyring & Pools file (keyring.yaml):
// named credential references plus per-Pathway Provider target pools. Like
// the Membrane config it is parse-only and fails closed: secrets appear ONLY by
// reference (inline secrets are rejected), every target must name a keyring
// entry that exists, and Provider-specific requirements are enforced here so an
// invalid file never enables the egress path.
package config

import (
	"bytes"
	"fmt"
	"regexp"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/inferencepath/membrane/internal/types"
)

// Known Provider families (keyring.schema.json providerId enum).
const (
	ProviderOpenAI      types.ProviderID = "openai"
	ProviderLocal       types.ProviderID = "local"
	ProviderAnthropic   types.ProviderID = "anthropic"
	ProviderAzureOpenAI types.ProviderID = "azure-openai"
	ProviderBedrock     types.ProviderID = "bedrock"
	ProviderGemini      types.ProviderID = "gemini"
)

var (
	keyringNameRe = regexp.MustCompile(`^[a-z][a-z0-9_-]*$`)
	pathwayRe     = regexp.MustCompile(`^[a-z][a-z0-9_.]*$`)
	// credentialRefRe permits the base reference schemes plus `secret:`, which
	// resolves from the sealed secret store (a value the operator entered via the
	// Console). It is intentionally broader than config.secretRefRe (used for the
	// console admin token), which has no sealed-store dependency.
	credentialRefRe = regexp.MustCompile(`^(env|file|vault|secret):.+`)
	validProviders  = map[types.ProviderID]struct{}{
		ProviderOpenAI: {}, ProviderLocal: {}, ProviderAnthropic: {},
		ProviderAzureOpenAI: {}, ProviderBedrock: {}, ProviderGemini: {},
	}
)

// KeyringFile is the parsed credentials configuration: named secret references
// (and optional rotation). Routing/pools live in the pathways config.
type KeyringFile struct {
	Version     int
	Credentials []Credential
	Rotation    RotationConfig
}

// RotationConfig configures secret-value rotation (not config-file reload).
type RotationConfig struct {
	Interval time.Duration
	Overlap  time.Duration
}

// Credential is a named secret reference (never an inline value).
type Credential struct {
	Name          string
	SecretRef     string
	NextSecretRef string
}

// TargetConfig is a concrete egress destination. It is Exterior-only and never
// disclosed to a Caller.
type TargetConfig struct {
	Provider     types.ProviderID
	Model        string
	Endpoint     string
	Deployment   string
	Region       string
	Project      string
	Location     string
	KeyringRef   string
	Priority     int
	TimeoutTotal time.Duration
	TimeoutIdle  time.Duration
	// AuthMode selects how the Exterior authenticates (api_key or an ambient IAM
	// mode); RoleARN/ExternalID apply to aws_assume_role. Empty AuthMode means
	// api_key (static keyring-resolved secret).
	AuthMode   string
	RoleARN    string
	ExternalID string
}

type rotationYAML struct {
	Interval string `yaml:"interval"`
	Overlap  string `yaml:"overlap"`
}

type keyringEntryYAML struct {
	Name          string `yaml:"name"`
	SecretRef     string `yaml:"secret_ref"`
	NextSecretRef string `yaml:"next_secret_ref"`
}

// credentialsFileYAML is the credentials-only Keyring file: named
// secret references and nothing else. A `pools` field is rejected (KnownFields),
// enforcing the split (routing lives in pathways.yaml).
type credentialsFileYAML struct {
	Version  int                `yaml:"version"`
	Keyring  []keyringEntryYAML `yaml:"keyring"`
	Rotation *rotationYAML      `yaml:"rotation"`
}

// ParseCredentials validates a credentials-only Keyring file: version,
// named secret references (reference-only), and optional rotation. It carries no
// pools. The returned KeyringFile has Credentials (and Rotation) populated and an
// empty Pools slice.
func ParseCredentials(data []byte) (KeyringFile, error) {
	return parseCredentialsFile(data, false)
}

// ParseCredentialsOptional is ParseCredentials but tolerates an empty keyring.
// Credentials are now an optional, advanced-only surface (providers own the
// routing credential), so the managed store and the declarative bundle validate
// an empty credentials document as valid rather than fail-closed.
func ParseCredentialsOptional(data []byte) (KeyringFile, error) {
	return parseCredentialsFile(data, true)
}

func parseCredentialsFile(data []byte, allowEmpty bool) (KeyringFile, error) {
	var fc credentialsFileYAML
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&fc); err != nil {
		return KeyringFile{}, fmt.Errorf("keyring config: parse: %w", err)
	}
	if fc.Version != 1 {
		return KeyringFile{}, fmt.Errorf("keyring config: version must be 1, got %d", fc.Version)
	}
	if len(fc.Keyring) == 0 {
		if allowEmpty {
			return KeyringFile{Version: fc.Version}, nil
		}
		return KeyringFile{}, fmt.Errorf("keyring config: at least one keyring entry is required")
	}
	creds, _, err := parseCredentials(fc.Keyring)
	if err != nil {
		return KeyringFile{}, err
	}
	kf := KeyringFile{Version: fc.Version, Credentials: creds}
	if fc.Rotation != nil {
		for _, d := range []struct {
			raw string
			dst *time.Duration
			lbl string
		}{
			{fc.Rotation.Interval, &kf.Rotation.Interval, "rotation.interval"},
			{fc.Rotation.Overlap, &kf.Rotation.Overlap, "rotation.overlap"},
		} {
			if d.raw == "" {
				continue
			}
			v, perr := time.ParseDuration(d.raw)
			if perr != nil {
				return KeyringFile{}, fmt.Errorf("keyring config: %s: %w", d.lbl, perr)
			}
			*d.dst = v
		}
	}
	return kf, nil
}

// parseCredentials validates named secret references (reference-only, unique
// names) and returns the Credentials plus the set of known names.
func parseCredentials(entries []keyringEntryYAML) ([]Credential, map[string]struct{}, error) {
	names := make(map[string]struct{}, len(entries))
	creds := make([]Credential, 0, len(entries))
	for i, e := range entries {
		if !keyringNameRe.MatchString(e.Name) {
			return nil, nil, fmt.Errorf("keyring config: keyring[%d]: invalid name %q", i, e.Name)
		}
		if _, dup := names[e.Name]; dup {
			return nil, nil, fmt.Errorf("keyring config: keyring[%d]: duplicate name %q", i, e.Name)
		}
		if !credentialRefRe.MatchString(e.SecretRef) {
			return nil, nil, fmt.Errorf("keyring config: keyring[%d] (%s): inline secrets are not allowed; secret_ref must use env:/file:/vault:/secret:", i, e.Name)
		}
		if e.NextSecretRef != "" && !credentialRefRe.MatchString(e.NextSecretRef) {
			return nil, nil, fmt.Errorf("keyring config: keyring[%d] (%s): next_secret_ref must use env:/file:/vault:/secret:", i, e.Name)
		}
		names[e.Name] = struct{}{}
		creds = append(creds, Credential{Name: e.Name, SecretRef: e.SecretRef, NextSecretRef: e.NextSecretRef})
	}
	return creds, names, nil
}
