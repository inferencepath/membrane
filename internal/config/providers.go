// Package config also parses the providers document. A provider holds what is
// needed to reach a vendor: family, auth mode, connection settings, allowed
// models and a secret reference. Pathways refer to providers by name. Parsing
// fails closed on unknown fields or families, bad names, inline secrets and
// missing per-family settings.
package config

import (
	"bytes"
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/inferencepath/membrane/internal/types"
)

// Auth modes select how the Exterior authenticates to a provider's vendor.
const (
	// AuthAPIKey uses a static sealed key resolved from SecretRef.
	AuthAPIKey = "api_key"
	// AuthAWSBearerTokenBedrock uses a static Bedrock bearer token resolved from
	// SecretRef. Supported only by the bedrock family.
	AuthAWSBearerTokenBedrock = "aws_bearer_token_bedrock"
	// AuthAWSDefaultChain uses ambient AWS credentials (env, instance profile,
	// IRSA, SSO) via the SDK default chain; no stored secret.
	AuthAWSDefaultChain = "aws_default_chain"
	// AuthAWSAssumeRole assumes RoleARN (optionally with ExternalID) on top of
	// the ambient chain; no stored secret.
	AuthAWSAssumeRole = "aws_assume_role"
	// AuthAzureManagedIdentity gets a bearer token from an Entra managed or
	// workload identity; no stored secret.
	AuthAzureManagedIdentity = "azure_managed_identity"
	// AuthGCPApplicationDefault gets an OAuth token from GCP Application
	// Default Credentials; no stored secret. Supported only by the gemini
	// family, for Vertex AI.
	AuthGCPApplicationDefault = "gcp_application_default"
)

// validAuthModes maps each family to the auth modes it accepts.
var validAuthModes = map[types.ProviderID]map[string]struct{}{
	ProviderOpenAI:      {AuthAPIKey: {}},
	ProviderAnthropic:   {AuthAPIKey: {}},
	ProviderGemini:      {AuthAPIKey: {}, AuthGCPApplicationDefault: {}},
	ProviderLocal:       {AuthAPIKey: {}},
	ProviderAzureOpenAI: {AuthAPIKey: {}, AuthAzureManagedIdentity: {}},
	ProviderBedrock:     {AuthAPIKey: {}, AuthAWSBearerTokenBedrock: {}, AuthAWSDefaultChain: {}, AuthAWSAssumeRole: {}},
}

// AuthModeUsesSecret reports whether mode needs a secret_ref, as opposed to
// ambient host identity. An empty mode means api_key.
func AuthModeUsesSecret(mode string) bool {
	switch mode {
	case "", AuthAPIKey, AuthAWSBearerTokenBedrock:
		return true
	default:
		return false
	}
}

// ProvidersSpec is the parsed set of configured providers.
type ProvidersSpec struct {
	Version   int
	Providers []ProviderConfig
}

// ProviderConfig is one configured provider. RoleARN and ExternalID apply to
// aws_assume_role, and Project and Location to Vertex AI. SecretRef is a
// reference (secret:, env:, file: or vault:) and is empty for ambient auth
// modes.
type ProviderConfig struct {
	Name         string
	Family       types.ProviderID
	AuthMode     string
	RoleARN      string
	ExternalID   string
	Models       []string
	DefaultModel string
	Endpoint     string
	Deployment   string
	Region       string
	Project      string
	Location     string
	SecretRef    string
}

type providersFileYAML struct {
	Version   int                 `yaml:"version"`
	Providers []providerEntryYAML `yaml:"providers"`
}

type providerEntryYAML struct {
	Name         string   `yaml:"name"`
	Family       string   `yaml:"family"`
	AuthMode     string   `yaml:"auth_mode"`
	RoleARN      string   `yaml:"role_arn"`
	ExternalID   string   `yaml:"external_id"`
	Models       []string `yaml:"models"`
	DefaultModel string   `yaml:"default_model"`
	Endpoint     string   `yaml:"endpoint"`
	Deployment   string   `yaml:"deployment"`
	Region       string   `yaml:"region"`
	Project      string   `yaml:"project"`
	Location     string   `yaml:"location"`
	SecretRef    string   `yaml:"secret_ref"`
}

// ParseProviders validates raw YAML into a ProvidersSpec. Besides unknown
// fields, families and auth modes, bad names, inline secrets and an invalid
// model list, it enforces per-family settings: endpoint for openai, anthropic
// and local; endpoint and deployment for azure-openai; region for bedrock.
func ParseProviders(data []byte) (ProvidersSpec, error) {
	var fc providersFileYAML
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&fc); err != nil {
		return ProvidersSpec{}, fmt.Errorf("providers config: parse: %w", err)
	}
	if fc.Version != 1 {
		return ProvidersSpec{}, fmt.Errorf("providers config: version must be 1, got %d", fc.Version)
	}

	spec := ProvidersSpec{Version: fc.Version}
	seen := make(map[string]struct{}, len(fc.Providers))
	for i, p := range fc.Providers {
		pc, err := parseProvider(p)
		if err != nil {
			return ProvidersSpec{}, fmt.Errorf("providers config: providers[%d]: %w", i, err)
		}
		if _, dup := seen[pc.Name]; dup {
			return ProvidersSpec{}, fmt.Errorf("providers config: providers[%d]: duplicate name %q", i, pc.Name)
		}
		seen[pc.Name] = struct{}{}
		spec.Providers = append(spec.Providers, pc)
	}
	return spec, nil
}

func parseProvider(p providerEntryYAML) (ProviderConfig, error) {
	if !keyringNameRe.MatchString(p.Name) {
		return ProviderConfig{}, fmt.Errorf("invalid name %q", p.Name)
	}
	family := types.ProviderID(p.Family)
	if _, ok := validProviders[family]; !ok {
		return ProviderConfig{}, fmt.Errorf("unknown family %q", p.Family)
	}

	authMode := p.AuthMode
	if authMode == "" {
		authMode = AuthAPIKey
	}
	if _, ok := validAuthModes[family][authMode]; !ok {
		return ProviderConfig{}, fmt.Errorf("family %q does not support auth_mode %q", family, authMode)
	}

	// Secret-backed modes need a secret_ref; ambient modes must not have one.
	switch authMode {
	case AuthAPIKey, AuthAWSBearerTokenBedrock:
		if p.SecretRef == "" {
			return ProviderConfig{}, fmt.Errorf("secret_ref is required for auth_mode %s", authMode)
		}
		if !credentialRefRe.MatchString(p.SecretRef) {
			return ProviderConfig{}, fmt.Errorf("inline secrets are not allowed; secret_ref must use env:/file:/vault:/secret:")
		}
		if p.RoleARN != "" || p.ExternalID != "" {
			return ProviderConfig{}, fmt.Errorf("role_arn/external_id are only valid for auth_mode aws_assume_role")
		}
	case AuthAWSAssumeRole:
		if p.SecretRef != "" {
			return ProviderConfig{}, fmt.Errorf("secret_ref is not allowed for ambient auth_mode %q", authMode)
		}
		if p.RoleARN == "" {
			return ProviderConfig{}, fmt.Errorf("role_arn is required for auth_mode aws_assume_role")
		}
	default: // aws_default_chain, azure_managed_identity, gcp_application_default
		if p.SecretRef != "" {
			return ProviderConfig{}, fmt.Errorf("secret_ref is not allowed for ambient auth_mode %q", authMode)
		}
		if p.RoleARN != "" || p.ExternalID != "" {
			return ProviderConfig{}, fmt.Errorf("role_arn/external_id are only valid for auth_mode aws_assume_role")
		}
	}

	models, derr := validateModels(p.Models, p.DefaultModel)
	if derr != nil {
		return ProviderConfig{}, derr
	}

	switch family {
	case ProviderAzureOpenAI:
		if p.Endpoint == "" {
			return ProviderConfig{}, fmt.Errorf("azure-openai requires endpoint (resource URL)")
		}
		if p.Deployment == "" {
			return ProviderConfig{}, fmt.Errorf("azure-openai requires deployment")
		}
	case ProviderBedrock:
		if p.Region == "" {
			return ProviderConfig{}, fmt.Errorf("bedrock requires region")
		}
		if err := validateBedrockModelRegions(models, p.Region); err != nil {
			return ProviderConfig{}, err
		}
	case ProviderOpenAI, ProviderLocal, ProviderAnthropic:
		if p.Endpoint == "" {
			return ProviderConfig{}, fmt.Errorf("%s requires endpoint", family)
		}
	case ProviderGemini:
		// Vertex AI is scoped to a project and location. AI Studio (api_key)
		// uses its public endpoint and needs neither.
		if authMode == AuthGCPApplicationDefault {
			if p.Project == "" || p.Location == "" {
				return ProviderConfig{}, fmt.Errorf("gemini vertex (gcp_application_default) requires project and location")
			}
		} else if p.Project != "" || p.Location != "" {
			return ProviderConfig{}, fmt.Errorf("gemini project/location are only valid with auth_mode gcp_application_default (Vertex AI)")
		}
	}

	return ProviderConfig{
		Name:         p.Name,
		Family:       family,
		AuthMode:     authMode,
		RoleARN:      p.RoleARN,
		ExternalID:   p.ExternalID,
		Models:       models,
		DefaultModel: p.DefaultModel,
		Endpoint:     p.Endpoint,
		Deployment:   p.Deployment,
		Region:       p.Region,
		Project:      p.Project,
		Location:     p.Location,
		SecretRef:    p.SecretRef,
	}, nil
}

// inferenceProfileRegionPrefixes maps the geography prefix of a Bedrock
// cross-region inference profile (the model ID up to the first dot) to the AWS
// region prefix it must be called from. "global." profiles work from any
// region and are absent. Foundation model IDs such as "anthropic." match no
// key and are unrestricted.
var inferenceProfileRegionPrefixes = map[string]string{
	"us-gov": "us-gov-",
	"us":     "us-",
	"eu":     "eu-",
	"apac":   "ap-",
}

// validateBedrockModelRegions rejects an inference profile model ID whose
// geography prefix does not match region. This would otherwise surface as an
// unclear upstream error at request time.
func validateBedrockModelRegions(models []string, region string) error {
	for _, m := range models {
		seg := m
		if i := strings.IndexByte(m, '.'); i >= 0 {
			seg = m[:i]
		}
		want, known := inferenceProfileRegionPrefixes[seg]
		if !known {
			continue
		}
		if !strings.HasPrefix(region, want) {
			return fmt.Errorf("model %q is a %s. inference profile but configured region %q is not in that geography (expected a %s* region)", m, seg, region, want)
		}
	}
	return nil
}

// validateModels trims the allowed models and requires the list to be
// non-empty, free of blanks and duplicates, and to contain def.
func validateModels(models []string, def string) ([]string, error) {
	if len(models) == 0 {
		return nil, fmt.Errorf("at least one allowed model is required")
	}
	seen := make(map[string]struct{}, len(models))
	out := make([]string, 0, len(models))
	for _, m := range models {
		m = strings.TrimSpace(m)
		if m == "" {
			return nil, fmt.Errorf("models must not contain empty entries")
		}
		if _, dup := seen[m]; dup {
			return nil, fmt.Errorf("duplicate model %q", m)
		}
		seen[m] = struct{}{}
		out = append(out, m)
	}
	if def == "" {
		return nil, fmt.Errorf("default_model is required")
	}
	if _, ok := seen[def]; !ok {
		return nil, fmt.Errorf("default_model %q must be one of the allowed models", def)
	}
	return out, nil
}
