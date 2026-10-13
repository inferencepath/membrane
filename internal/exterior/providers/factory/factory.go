// Package factory constructs a concrete Provider adapter for a configured
// target. It lives in the Exterior zone and is the single place that maps a
// Provider family to its adapter, so wiring (and tests) stay DRY. It never sees
// Caller identity or credentials; credentials are resolved separately by the
// Keyring and attached at/after the Membrane.
package factory

import (
	"context"
	"fmt"
	"sync"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore/policy"
	"github.com/Azure/azure-sdk-for-go/sdk/azidentity"
	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials/stscreds"
	"github.com/aws/aws-sdk-go-v2/service/sts"
	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"

	"github.com/inferencepath/membrane/internal/config"
	"github.com/inferencepath/membrane/internal/exterior/providers"
	"github.com/inferencepath/membrane/internal/exterior/providers/anthropic"
	"github.com/inferencepath/membrane/internal/exterior/providers/azureopenai"
	"github.com/inferencepath/membrane/internal/exterior/providers/bedrock"
	"github.com/inferencepath/membrane/internal/exterior/providers/gemini"
	"github.com/inferencepath/membrane/internal/exterior/providers/openai"
)

// azureTokenScope is the Entra scope for Azure Cognitive Services (OpenAI).
const azureTokenScope = "https://cognitiveservices.azure.com/.default"

// gcpCloudPlatformScope is the OAuth scope for Vertex AI (and other GCP APIs).
const gcpCloudPlatformScope = "https://www.googleapis.com/auth/cloud-platform"

// Families returns the set of Provider families this factory can Build, as the
// stable family strings used in configuration. It is the single source of truth
// for "which families exist", so verification suites (e.g. the Diffusion-leak
// coverage guard) can enumerate the registry and fail CI when a new family is
// added without leak coverage.
func Families() []string {
	return []string{
		string(config.ProviderOpenAI),
		string(config.ProviderLocal),
		string(config.ProviderAnthropic),
		string(config.ProviderAzureOpenAI),
		string(config.ProviderBedrock),
		string(config.ProviderGemini),
	}
}

// Build returns the Provider adapter for a configured target. The adapter's
// reported ID is the real Provider family, which is what the Membrane may
// surface to the Caller on Reveal. Unknown families fail closed.
func Build(t config.TargetConfig) (providers.Provider, error) {
	switch t.Provider {
	case config.ProviderOpenAI, config.ProviderLocal:
		// local is OpenAI-compatible; reuse the OpenAI adapter but keep the
		// configured family as the reported (revealable) ID.
		return openai.New(t.Provider, t.Endpoint, t.TimeoutIdle), nil
	case config.ProviderAnthropic:
		return anthropic.New(t.Endpoint, t.TimeoutIdle), nil
	case config.ProviderGemini:
		opts, err := geminiOptions(t)
		if err != nil {
			return nil, err
		}
		// AI Studio (api_key) defaults to the public endpoint; Vertex (ambient
		// OAuth) targets the project/location host via the options.
		return gemini.New(t.Endpoint, t.TimeoutIdle, opts...), nil
	case config.ProviderAzureOpenAI:
		opts, err := azureOptions(t)
		if err != nil {
			return nil, err
		}
		return azureopenai.New(t.Endpoint, t.Deployment, t.TimeoutIdle, opts...), nil
	case config.ProviderBedrock:
		opts, err := bedrockOptions(t)
		if err != nil {
			return nil, err
		}
		return bedrock.New(t.Region, t.Endpoint, t.TimeoutIdle, opts...), nil
	default:
		return nil, fmt.Errorf("factory: unknown provider %q", t.Provider)
	}
}

// bedrockOptions builds the Bedrock adapter's auth options. api_key (the
// default) uses the keyring-resolved static secret and needs no option; the
// ambient modes attach an auto-refreshing AWS credentials provider.
func bedrockOptions(t config.TargetConfig) ([]bedrock.Option, error) {
	switch t.AuthMode {
	case "", config.AuthAPIKey:
		return nil, nil
	case config.AuthAWSBearerTokenBedrock:
		return []bedrock.Option{bedrock.WithBearerTokenAuth()}, nil
	case config.AuthAWSDefaultChain:
		cfg, err := awsconfig.LoadDefaultConfig(context.Background(), awsconfig.WithRegion(t.Region))
		if err != nil {
			return nil, fmt.Errorf("factory: bedrock aws default chain: %w", err)
		}
		return []bedrock.Option{bedrock.WithCredentialsProvider(cfg.Credentials)}, nil
	case config.AuthAWSAssumeRole:
		cfg, err := awsconfig.LoadDefaultConfig(context.Background(), awsconfig.WithRegion(t.Region))
		if err != nil {
			return nil, fmt.Errorf("factory: bedrock assume-role base config: %w", err)
		}
		prov := stscreds.NewAssumeRoleProvider(sts.NewFromConfig(cfg), t.RoleARN, func(o *stscreds.AssumeRoleOptions) {
			if t.ExternalID != "" {
				o.ExternalID = aws.String(t.ExternalID)
			}
		})
		// Cache so credentials are reused until near expiry rather than assumed
		// on every request.
		return []bedrock.Option{bedrock.WithCredentialsProvider(aws.NewCredentialsCache(prov))}, nil
	default:
		return nil, fmt.Errorf("factory: bedrock unsupported auth_mode %q", t.AuthMode)
	}
}

// geminiOptions builds the Gemini adapter's auth/surface options. api_key (the
// default) is AI Studio and needs no option; gcp_application_default is the
// Vertex AI surface — it selects the project/location endpoint and attaches an
// ambient OAuth bearer source (ADC). The token source is resolved lazily on first
// use so Build performs no credential discovery or network I/O.
func geminiOptions(t config.TargetConfig) ([]gemini.Option, error) {
	switch t.AuthMode {
	case "", config.AuthAPIKey:
		return nil, nil
	case config.AuthGCPApplicationDefault:
		var (
			once  sync.Once
			ts    oauth2.TokenSource
			tsErr error
		)
		src := func(ctx context.Context) (string, error) {
			once.Do(func() {
				ts, tsErr = google.DefaultTokenSource(ctx, gcpCloudPlatformScope)
			})
			if tsErr != nil {
				return "", tsErr
			}
			tok, terr := ts.Token()
			if terr != nil {
				return "", terr
			}
			return tok.AccessToken, nil
		}
		return []gemini.Option{gemini.WithVertex(t.Project, t.Location), gemini.WithTokenSource(src)}, nil
	default:
		return nil, fmt.Errorf("factory: gemini unsupported auth_mode %q", t.AuthMode)
	}
}

// azureOptions builds the Azure OpenAI adapter's auth options. api_key uses the
// keyring-resolved static key; azure_managed_identity attaches an Entra token
// source (DefaultAzureCredential: managed identity, workload identity, env, …).
func azureOptions(t config.TargetConfig) ([]azureopenai.Option, error) {
	switch t.AuthMode {
	case "", config.AuthAPIKey:
		return nil, nil
	case config.AuthAzureManagedIdentity:
		cred, err := azidentity.NewDefaultAzureCredential(nil)
		if err != nil {
			return nil, fmt.Errorf("factory: azure managed identity: %w", err)
		}
		src := func(ctx context.Context) (string, error) {
			tk, terr := cred.GetToken(ctx, policy.TokenRequestOptions{Scopes: []string{azureTokenScope}})
			if terr != nil {
				return "", terr
			}
			return tk.Token, nil
		}
		return []azureopenai.Option{azureopenai.WithTokenSource(src)}, nil
	default:
		return nil, fmt.Errorf("factory: azure-openai unsupported auth_mode %q", t.AuthMode)
	}
}
