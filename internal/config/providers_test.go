package config_test

import (
	"strings"
	"testing"

	"github.com/inferencepath/membrane/internal/config"
)

func TestParseProvidersValid(t *testing.T) {
	y := `version: 1
providers:
  - {name: openai-main, family: openai, models: [gpt-4o-mini, gpt-4o], default_model: gpt-4o-mini, endpoint: https://api.openai.com/v1, secret_ref: secret:openai-main}
  - {name: azure-main, family: azure-openai, models: [gpt-4o], default_model: gpt-4o, endpoint: https://x.openai.azure.com, deployment: gpt4o, secret_ref: env:AZURE_KEY}
  - {name: bedrock-main, family: bedrock, models: [anthropic.claude-3], default_model: anthropic.claude-3, region: us-east-1, secret_ref: secret:bedrock-main}
`
	spec, err := config.ParseProviders([]byte(y))
	if err != nil {
		t.Fatal(err)
	}
	if spec.Version != 1 || len(spec.Providers) != 3 {
		t.Fatalf("unexpected spec: %+v", spec)
	}
	p0 := spec.Providers[0]
	if p0.Name != "openai-main" || p0.Family != config.ProviderOpenAI {
		t.Fatalf("provider[0] = %+v", p0)
	}
	if p0.AuthMode != config.AuthAPIKey {
		t.Fatalf("provider[0] auth mode = %q, want api_key", p0.AuthMode)
	}
	if len(p0.Models) != 2 || p0.DefaultModel != "gpt-4o-mini" {
		t.Fatalf("provider[0] model policy = %+v", p0)
	}
}

func TestParseProvidersGeminiServiceAccountKey(t *testing.T) {
	y := `version: 1
providers:
  - {name: vertex, family: gemini, auth_mode: gcp_service_account_key, project: demo-project, location: us-central1, models: [gemini-2.5-flash], default_model: gemini-2.5-flash, secret_ref: secret:vertex}
`
	spec, err := config.ParseProviders([]byte(y))
	if err != nil {
		t.Fatal(err)
	}
	p := spec.Providers[0]
	if p.AuthMode != config.AuthGCPServiceAccountKey || p.Project != "demo-project" || p.SecretRef != "secret:vertex" {
		t.Fatalf("provider = %+v", p)
	}
	for name, entry := range map[string]string{
		"missing secret":  `{name: vertex, family: gemini, auth_mode: gcp_service_account_key, project: p, location: l, models: [m], default_model: m}`,
		"missing project": `{name: vertex, family: gemini, auth_mode: gcp_service_account_key, location: l, models: [m], default_model: m, secret_ref: secret:vertex}`,
		"wrong family":    `{name: vertex, family: openai, auth_mode: gcp_service_account_key, endpoint: https://x, models: [m], default_model: m, secret_ref: secret:vertex}`,
	} {
		if _, err := config.ParseProviders([]byte("version: 1\nproviders:\n  - " + entry + "\n")); err == nil {
			t.Errorf("%s: expected validation failure", name)
		}
	}
}

func TestValidateGCPServiceAccountKeyAndAzureEndpoint(t *testing.T) {
	good := `{"type":"service_account","client_email":"service@example.iam.gserviceaccount.com","private_key":"-----BEGIN PRIVATE KEY-----\nprivate\n-----END PRIVATE KEY-----\n"}`
	if err := config.ValidateGCPServiceAccountKey(good); err != nil {
		t.Fatalf("valid key: %v", err)
	}
	for _, key := range []string{"not json", `{"type":"external_account"}`, `{"type":"service_account","client_email":"a"}`} {
		if err := config.ValidateGCPServiceAccountKey(key); err == nil {
			t.Errorf("invalid service-account key %q accepted", key)
		} else if strings.Contains(err.Error(), key) {
			t.Errorf("error leaked key value: %v", err)
		}
	}
	fixed, changed, err := config.NormalizeAzureEndpoint("https://demo.openai.azure.com/openai/deployments/chat?api-version=2024-10-21")
	if err != nil || fixed != "https://demo.openai.azure.com" || !changed {
		t.Fatalf("normalization = (%q, %v, %v)", fixed, changed, err)
	}
}

func TestParseProvidersGeminiAIStudio(t *testing.T) {
	y := `version: 1
providers:
  - {name: gemini-main, family: gemini, auth_mode: api_key, models: [gemini-1.5-pro], default_model: gemini-1.5-pro, secret_ref: env:GEMINI_API_KEY}
`
	spec, err := config.ParseProviders([]byte(y))
	if err != nil {
		t.Fatal(err)
	}
	p := spec.Providers[0]
	if p.Family != config.ProviderGemini || p.AuthMode != config.AuthAPIKey {
		t.Fatalf("gemini provider = %+v", p)
	}
	if p.Project != "" || p.Location != "" {
		t.Fatalf("AI Studio must not carry project/location: %+v", p)
	}
}

func TestParseProvidersGeminiVertex(t *testing.T) {
	y := `version: 1
providers:
  - {name: gemini-vertex, family: gemini, auth_mode: gcp_application_default, project: my-proj, location: us-central1, models: [gemini-1.5-pro], default_model: gemini-1.5-pro}
`
	spec, err := config.ParseProviders([]byte(y))
	if err != nil {
		t.Fatal(err)
	}
	p := spec.Providers[0]
	if p.AuthMode != config.AuthGCPApplicationDefault || p.Project != "my-proj" || p.Location != "us-central1" {
		t.Fatalf("vertex provider = %+v", p)
	}
	if p.SecretRef != "" {
		t.Fatalf("ambient Vertex auth must store no secret: %+v", p)
	}
}

func TestParseProvidersGeminiVertexRejectsMissingProjectLocation(t *testing.T) {
	y := `version: 1
providers:
  - {name: gemini-vertex, family: gemini, auth_mode: gcp_application_default, models: [m], default_model: m}
`
	if _, err := config.ParseProviders([]byte(y)); err == nil {
		t.Fatal("want error: Vertex requires project and location")
	}
}

func TestParseProvidersAnthropicVertex(t *testing.T) {
	y := `version: 1
providers:
  - {name: claude-vertex, family: anthropic, auth_mode: gcp_application_default, project: my-proj, location: us-east5, models: [claude-sonnet-5-5], default_model: claude-sonnet-5-5}
  - {name: claude-vertex-sa, family: anthropic, auth_mode: gcp_service_account_key, project: my-proj, location: global, models: [claude-sonnet-5-5], default_model: claude-sonnet-5-5, secret_ref: secret:claude-vertex-sa}
`
	spec, err := config.ParseProviders([]byte(y))
	if err != nil {
		t.Fatal(err)
	}
	if p := spec.Providers[0]; p.Project != "my-proj" || p.Location != "us-east5" || p.SecretRef != "" || p.Endpoint != "" {
		t.Fatalf("ambient Claude on Vertex = %+v", p)
	}
	if p := spec.Providers[1]; p.SecretRef != "secret:claude-vertex-sa" {
		t.Fatalf("service-account Claude on Vertex = %+v", p)
	}
}

func TestParseProvidersAnthropicVertexRejects(t *testing.T) {
	cases := map[string]string{
		"missing project/location":   `{name: c, family: anthropic, auth_mode: gcp_application_default, models: [m], default_model: m}`,
		"api.anthropic.com endpoint": `{name: c, family: anthropic, auth_mode: gcp_application_default, project: p, location: us-east5, endpoint: "https://api.anthropic.com", models: [m], default_model: m}`,
		"project on api_key":         `{name: c, family: anthropic, auth_mode: api_key, endpoint: "https://api.anthropic.com", project: p, location: l, models: [m], default_model: m, secret_ref: env:K}`,
		"api_key without endpoint":   `{name: c, family: anthropic, auth_mode: api_key, models: [m], default_model: m, secret_ref: env:K}`,
	}
	for name, entry := range cases {
		if _, err := config.ParseProviders([]byte("version: 1\nproviders:\n  - " + entry + "\n")); err == nil {
			t.Errorf("%s: want error", name)
		}
	}
}

func TestParseProvidersLocalVertex(t *testing.T) {
	ok := `version: 1
providers:
  - {name: vertex-oai, family: local, auth_mode: gcp_service_account_key, project: p, location: global, models: [meta/llama], default_model: meta/llama, secret_ref: secret:vertex-oai}
`
	if _, err := config.ParseProviders([]byte(ok)); err != nil {
		t.Fatal(err)
	}
	for name, entry := range map[string]string{
		"missing project/location": `{name: c, family: local, auth_mode: gcp_application_default, models: [m], default_model: m}`,
		"project on api_key":       `{name: c, family: local, auth_mode: api_key, endpoint: "http://x", project: p, location: l, models: [m], default_model: m, secret_ref: env:K}`,
		"openai with gcp":          `{name: c, family: openai, auth_mode: gcp_application_default, project: p, location: l, endpoint: "http://x", models: [m], default_model: m}`,
	} {
		if _, err := config.ParseProviders([]byte("version: 1\nproviders:\n  - " + entry + "\n")); err == nil {
			t.Errorf("%s: want error", name)
		}
	}
}

func TestParseProvidersGeminiAIStudioRejectsProjectLocation(t *testing.T) {
	y := `version: 1
providers:
  - {name: gemini-main, family: gemini, auth_mode: api_key, project: p, location: l, models: [m], default_model: m, secret_ref: env:K}
`
	if _, err := config.ParseProviders([]byte(y)); err == nil {
		t.Fatal("want error: project/location only valid for Vertex")
	}
}

func TestParseProvidersAmbientAuth(t *testing.T) {
	y := `version: 1
providers:
  - {name: bedrock-bearer, family: bedrock, auth_mode: aws_bearer_token_bedrock, models: [m], default_model: m, region: us-east-1, secret_ref: env:AWS_BEARER_TOKEN_BEDROCK}
  - {name: bedrock-chain, family: bedrock, auth_mode: aws_default_chain, models: [m], default_model: m, region: us-east-1}
  - {name: bedrock-role, family: bedrock, auth_mode: aws_assume_role, role_arn: "arn:aws:iam::1:role/r", external_id: ext, models: [m], default_model: m, region: us-east-1}
  - {name: azure-mi, family: azure-openai, auth_mode: azure_managed_identity, models: [m], default_model: m, endpoint: https://x.openai.azure.com, deployment: d}
`
	spec, err := config.ParseProviders([]byte(y))
	if err != nil {
		t.Fatal(err)
	}
	if len(spec.Providers) != 4 {
		t.Fatalf("want 4 providers, got %d", len(spec.Providers))
	}
	if spec.Providers[0].AuthMode != config.AuthAWSBearerTokenBedrock || spec.Providers[0].SecretRef == "" {
		t.Fatalf("bedrock-bearer = %+v", spec.Providers[0])
	}
	if spec.Providers[1].AuthMode != config.AuthAWSDefaultChain || spec.Providers[1].SecretRef != "" {
		t.Fatalf("bedrock-chain = %+v", spec.Providers[1])
	}
	if spec.Providers[2].RoleARN == "" || spec.Providers[2].ExternalID != "ext" {
		t.Fatalf("bedrock-role = %+v", spec.Providers[2])
	}
	if spec.Providers[3].AuthMode != config.AuthAzureManagedIdentity {
		t.Fatalf("azure-mi = %+v", spec.Providers[3])
	}
}

func TestParseProvidersBedrockInferenceProfiles(t *testing.T) {
	// us. profiles from a us-* region, global. profiles and plain foundation
	// models parse; only a geography mismatch fails.
	y := `version: 1
providers:
  - {name: bedrock-ip, family: bedrock, auth_mode: aws_default_chain, region: us-east-1,
     models: [us.anthropic.claude-3-5-sonnet-20241022-v2:0, global.anthropic.claude-sonnet-4-20250514-v1:0, amazon.titan-text-express-v1],
     default_model: us.anthropic.claude-3-5-sonnet-20241022-v2:0}
`
	spec, err := config.ParseProviders([]byte(y))
	if err != nil {
		t.Fatalf("valid inference-profile config rejected: %v", err)
	}
	if got := len(spec.Providers[0].Models); got != 3 {
		t.Fatalf("models = %d, want 3", got)
	}
}

func TestParseProvidersRejects(t *testing.T) {
	tests := []struct {
		name, yaml, want string
	}{
		{"bad version", "version: 2\nproviders: [{name: a, family: openai, models: [m], default_model: m, endpoint: http://x, secret_ref: env:X}]", "version must be 1"},
		{"bad name", "version: 1\nproviders: [{name: BAD, family: openai, models: [m], default_model: m, endpoint: http://x, secret_ref: env:X}]", "invalid name"},
		{"unknown family", "version: 1\nproviders: [{name: a, family: mystery, models: [m], default_model: m, endpoint: http://x, secret_ref: env:X}]", "unknown family"},
		{"no models", "version: 1\nproviders: [{name: a, family: openai, default_model: m, endpoint: http://x, secret_ref: env:X}]", "at least one allowed model is required"},
		{"no default_model", "version: 1\nproviders: [{name: a, family: openai, models: [m], endpoint: http://x, secret_ref: env:X}]", "default_model is required"},
		{"default not in models", "version: 1\nproviders: [{name: a, family: openai, models: [m], default_model: other, endpoint: http://x, secret_ref: env:X}]", "must be one of the allowed models"},
		{"dup model", "version: 1\nproviders: [{name: a, family: openai, models: [m, m], default_model: m, endpoint: http://x, secret_ref: env:X}]", "duplicate model"},
		{"no secret_ref", "version: 1\nproviders: [{name: a, family: openai, models: [m], default_model: m, endpoint: http://x}]", "secret_ref is required"},
		{"inline secret", "version: 1\nproviders: [{name: a, family: openai, models: [m], default_model: m, endpoint: http://x, secret_ref: sk-inline}]", "inline secrets are not allowed"},
		{"dup name", "version: 1\nproviders: [{name: a, family: openai, models: [m], default_model: m, endpoint: http://x, secret_ref: env:X}, {name: a, family: openai, models: [m], default_model: m, endpoint: http://x, secret_ref: env:Y}]", "duplicate name"},
		{"openai no endpoint", "version: 1\nproviders: [{name: a, family: openai, models: [m], default_model: m, secret_ref: env:X}]", "requires endpoint"},
		{"azure no deployment", "version: 1\nproviders: [{name: a, family: azure-openai, models: [m], default_model: m, endpoint: http://x, secret_ref: env:X}]", "requires deployment"},
		{"bedrock no region", "version: 1\nproviders: [{name: a, family: bedrock, models: [m], default_model: m, secret_ref: env:X}]", "requires region"},
		{"bad auth for family", "version: 1\nproviders: [{name: a, family: openai, auth_mode: aws_default_chain, models: [m], default_model: m, endpoint: http://x}]", "does not support auth_mode"},
		{"assume role needs arn", "version: 1\nproviders: [{name: a, family: bedrock, auth_mode: aws_assume_role, models: [m], default_model: m, region: us-east-1}]", "role_arn is required"},
		{"bedrock bearer needs secret", "version: 1\nproviders: [{name: a, family: bedrock, auth_mode: aws_bearer_token_bedrock, models: [m], default_model: m, region: us-east-1}]", "secret_ref is required"},
		{"ambient with secret", "version: 1\nproviders: [{name: a, family: bedrock, auth_mode: aws_default_chain, models: [m], default_model: m, region: us-east-1, secret_ref: env:X}]", "secret_ref is not allowed"},
		{"bedrock profile region mismatch", "version: 1\nproviders: [{name: a, family: bedrock, auth_mode: aws_default_chain, models: [us.anthropic.claude-3-5-sonnet-20241022-v2:0], default_model: us.anthropic.claude-3-5-sonnet-20241022-v2:0, region: eu-west-1}]", "not in that geography"},
		{"unknown field", "version: 1\nmystery: true\nproviders: []", "parse"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := config.ParseProviders([]byte(tc.yaml))
			if err == nil {
				t.Fatalf("want error containing %q, got nil", tc.want)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %q, want contains %q", err.Error(), tc.want)
			}
		})
	}
}
