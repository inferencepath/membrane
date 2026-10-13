// Package config also loads the providers file (providers.yaml): the set of
// configured providers, each owning everything needed to reach a vendor — the
// family, connection facts (endpoint/deployment/region), a default model, and a
// reference to the sealed key. Pathways reference a provider by name; all
// connection config lives here, not on the pathway target. Like the other config
// files it is parse-only and fails closed: unknown fields, unknown families,
// duplicate/malformed names, inline secrets, and missing per-family requirements
// are all rejected.
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
	// AuthAzureManagedIdentity uses an Entra managed identity / workload identity
	// to mint a bearer token; no stored secret.
	AuthAzureManagedIdentity = "azure_managed_identity"
	// AuthGCPApplicationDefault uses ambient GCP credentials (Application Default
	// Credentials: service account, workload identity, gcloud) to mint an OAuth
	// bearer; no stored secret. Supported only by the gemini family (Vertex AI).
	AuthGCPApplicationDefault = "gcp_application_default"
)

// validAuthModes maps each family to the auth modes it accepts. Only Bedrock and
// Azure OpenAI support IAM-style ambient auth; the pure API-key vendors do not.
var validAuthModes = map[types.ProviderID]map[string]struct{}{
	ProviderOpenAI:      {AuthAPIKey: {}},
	ProviderAnthropic:   {AuthAPIKey: {}},
	ProviderGemini:      {AuthAPIKey: {}, AuthGCPApplicationDefault: {}},
	ProviderLocal:       {AuthAPIKey: {}},
	ProviderAzureOpenAI: {AuthAPIKey: {}, AuthAzureManagedIdentity: {}},
	ProviderBedrock:     {AuthAPIKey: {}, AuthAWSBearerTokenBedrock: {}, AuthAWSDefaultChain: {}, AuthAWSAssumeRole: {}},
}

// AuthModeUsesSecret reports whether an auth mode requires a stored secret_ref
// resolved from credentials/keyring (vs ambient host identity).
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

// ProviderConfig is one configured provider: the vendor family, how it
// authenticates (AuthMode plus RoleARN/ExternalID for aws_assume_role), the
// connection facts, the admin-controlled allowed Models with a DefaultModel, and
// (for api_key auth) the sealed-key reference. SecretRef is a reference only
// (secret:/env:/file:/vault:) — never an inline value — and is empty for the
// ambient IAM auth modes.
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

// ParseProviders validates raw YAML bytes into a ProvidersSpec, rejecting
// unknown fields, unknown families/auth modes, duplicate/malformed names, inline
// secrets, empty or inconsistent model policy, and missing per-family
// requirements (endpoint for openai/anthropic/local, endpoint+deployment for
// azure-openai, region for bedrock).
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

	// Auth mode (default api_key) must be supported by the family.
	authMode := p.AuthMode
	if authMode == "" {
		authMode = AuthAPIKey
	}
	if _, ok := validAuthModes[family][authMode]; !ok {
		return ProviderConfig{}, fmt.Errorf("family %q does not support auth_mode %q", family, authMode)
	}

	// Credential requirements per auth mode. api_key needs a sealed/ref secret;
	// the ambient IAM modes must NOT carry a secret_ref (nothing is stored).
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
	default: // aws_default_chain, azure_managed_identity
		if p.SecretRef != "" {
			return ProviderConfig{}, fmt.Errorf("secret_ref is not allowed for ambient auth_mode %q", authMode)
		}
		if p.RoleARN != "" || p.ExternalID != "" {
			return ProviderConfig{}, fmt.Errorf("role_arn/external_id are only valid for auth_mode aws_assume_role")
		}
	}

	// Model policy: a non-empty allowlist and a default drawn from it.
	models, derr := validateModels(p.Models, p.DefaultModel)
	if derr != nil {
		return ProviderConfig{}, derr
	}

	// Per-family connection requirements.
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
		// Vertex AI (gcp_application_default) is project/location-scoped; AI Studio
		// (api_key) uses the public/overridable endpoint and needs neither.
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

// inferenceProfileRegionPrefixes maps a Bedrock cross-region inference-profile
// geographic prefix (the model-id segment before the first dot) to the AWS
// region prefix it must be invoked from. "global." profiles are valid from any
// region and are intentionally absent; foundation-model ids (anthropic., amazon.,
// meta., mistral., cohere., …) never match a key here and are left unrestricted.
var inferenceProfileRegionPrefixes = map[string]string{
	"us-gov": "us-gov-",
	"us":     "us-",
	"eu":     "eu-",
	"apac":   "ap-",
}

// validateBedrockModelRegions rejects a cross-region inference-profile model id
// whose geographic prefix (us./eu./apac./us-gov.) is inconsistent with the
// provider's configured region, catching a common misconfiguration at parse time
// rather than as an opaque upstream error at request time. Plain foundation-model
// ids and "global." profiles are unrestricted.
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

// validateModels enforces a non-empty, duplicate-free allowed-model list whose
// default is one of the entries, trimming blanks. The admin controls exactly
// which models a developer may reach through the provider.
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
