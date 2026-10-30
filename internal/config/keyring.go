// Package config also parses the Keyring document of named credential
// references. Parsing fails closed: inline secrets are rejected, and an
// invalid document never enables the egress path.
package config

import (
	"bytes"
	"fmt"
	"regexp"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/inferencepath/membrane/internal/types"
)

// Known Provider families.
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
	// credentialRefRe also accepts secret:, which resolves from the sealed
	// store. secretRefRe does not, because the values it checks (the Console
	// token and the master key) must not depend on the sealed store.
	credentialRefRe = regexp.MustCompile(`^(env|file|vault|secret):.+`)
	validProviders  = map[types.ProviderID]struct{}{
		ProviderOpenAI: {}, ProviderLocal: {}, ProviderAnthropic: {},
		ProviderAzureOpenAI: {}, ProviderBedrock: {}, ProviderGemini: {},
	}
)

// KeyringFile is the parsed credentials document: named secret references and
// optional rotation settings.
type KeyringFile struct {
	Version     int
	Credentials []Credential
	Rotation    RotationConfig
}

// RotationConfig configures rotation of secret values.
type RotationConfig struct {
	Interval time.Duration
	Overlap  time.Duration
}

// Credential is a named secret reference.
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
	// AuthMode selects how the Exterior authenticates; empty means api_key.
	// RoleARN and ExternalID apply to aws_assume_role.
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

// credentialsFileYAML is the Keyring document. Unknown fields are rejected.
type credentialsFileYAML struct {
	Version  int                `yaml:"version"`
	Keyring  []keyringEntryYAML `yaml:"keyring"`
	Rotation *rotationYAML      `yaml:"rotation"`
}

// ParseCredentials validates a Keyring document: its version, named secret
// references and optional rotation. At least one entry is required.
func ParseCredentials(data []byte) (KeyringFile, error) {
	return parseCredentialsFile(data, false)
}

// ParseCredentialsOptional is like ParseCredentials but accepts an empty
// keyring. Providers carry their own credentials, so standalone credentials
// are optional in the store and in bundles.
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

// parseCredentials checks names, uniqueness and reference schemes, and returns
// the credentials and the set of names.
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
