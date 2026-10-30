package factory_test

import (
	"testing"

	"github.com/inferencepath/membrane/internal/config"
	"github.com/inferencepath/membrane/internal/exterior/providers/factory"
	"github.com/inferencepath/membrane/internal/types"
)

func TestBuildFamilies(t *testing.T) {
	tests := []struct {
		name   string
		target config.TargetConfig
		wantID types.ProviderID
	}{
		{"openai", config.TargetConfig{Provider: config.ProviderOpenAI, Endpoint: "http://x"}, "openai"},
		{"local", config.TargetConfig{Provider: config.ProviderLocal, Endpoint: "http://x"}, "local"},
		{"anthropic", config.TargetConfig{Provider: config.ProviderAnthropic, Endpoint: "http://x"}, "anthropic"},
		{"azure", config.TargetConfig{Provider: config.ProviderAzureOpenAI, Endpoint: "http://x", Deployment: "d"}, "azure-openai"},
		{"bedrock", config.TargetConfig{Provider: config.ProviderBedrock, Region: "us-east-1"}, "bedrock"},
		{"gemini", config.TargetConfig{Provider: config.ProviderGemini, Endpoint: "http://x"}, "gemini"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p, err := factory.Build(tc.target)
			if err != nil {
				t.Fatal(err)
			}
			if p.ID() != tc.wantID {
				t.Fatalf("ID = %q, want %q", p.ID(), tc.wantID)
			}
		})
	}
}

// TestAdapterSupports checks that AdapterSupports agrees with FamilySupports for
// every family and operation.
func TestAdapterSupports(t *testing.T) {
	allOps := []types.Operation{
		types.OperationChat,
		types.OperationEmbeddings,
		types.OperationRerank,
		types.OperationResponses,
		types.OperationImageGeneration,
		types.OperationAudioTranscription,
		types.OperationAudioSpeech,
		types.OperationModerations,
	}
	families := []struct {
		name   string
		family types.ProviderID
		target config.TargetConfig
	}{
		{"openai", config.ProviderOpenAI, config.TargetConfig{Provider: config.ProviderOpenAI, Endpoint: "http://x"}},
		{"local", config.ProviderLocal, config.TargetConfig{Provider: config.ProviderLocal, Endpoint: "http://x"}},
		{"anthropic", config.ProviderAnthropic, config.TargetConfig{Provider: config.ProviderAnthropic, Endpoint: "http://x"}},
		{"azure", config.ProviderAzureOpenAI, config.TargetConfig{Provider: config.ProviderAzureOpenAI, Endpoint: "http://x", Deployment: "d"}},
		{"bedrock", config.ProviderBedrock, config.TargetConfig{Provider: config.ProviderBedrock, Region: "us-east-1"}},
		{"gemini", config.ProviderGemini, config.TargetConfig{Provider: config.ProviderGemini, Endpoint: "http://x"}},
	}
	for _, f := range families {
		t.Run(f.name, func(t *testing.T) {
			adapter, berr := factory.Build(f.target)
			if berr != nil {
				t.Fatalf("build: %v", berr)
			}
			if !factory.AdapterSupports(adapter, types.OperationChat) {
				t.Error("chat must always be supported")
			}
			// An empty operation defaults to chat.
			if !factory.AdapterSupports(adapter, "") {
				t.Error("empty operation should default to chat and be supported")
			}
			for _, op := range allOps {
				declared := factory.FamilySupports(f.family, op)
				structural := factory.AdapterSupports(adapter, op)
				if declared != structural {
					t.Errorf("operation %q: FamilySupports=%v but AdapterSupports=%v (matrix and adapter disagree)", op, declared, structural)
				}
			}
		})
	}
}

func TestBuildUnknown(t *testing.T) {
	if _, err := factory.Build(config.TargetConfig{Provider: "mystery"}); err == nil {
		t.Fatal("want error for unknown provider")
	}
}

// TestBuildAmbientAuthModes checks that ambient auth modes build an adapter
// without a stored secret or network access.
func TestBuildAmbientAuthModes(t *testing.T) {
	tests := []struct {
		name   string
		target config.TargetConfig
		wantID types.ProviderID
	}{
		{"bedrock bearer token", config.TargetConfig{Provider: config.ProviderBedrock, Region: "us-east-1", AuthMode: config.AuthAWSBearerTokenBedrock}, "bedrock"},
		{"bedrock default chain", config.TargetConfig{Provider: config.ProviderBedrock, Region: "us-east-1", AuthMode: config.AuthAWSDefaultChain}, "bedrock"},
		{"bedrock assume role", config.TargetConfig{Provider: config.ProviderBedrock, Region: "us-east-1", AuthMode: config.AuthAWSAssumeRole, RoleARN: "arn:aws:iam::1:role/r", ExternalID: "ext"}, "bedrock"},
		{"azure managed identity", config.TargetConfig{Provider: config.ProviderAzureOpenAI, Endpoint: "http://x", Deployment: "d", AuthMode: config.AuthAzureManagedIdentity}, "azure-openai"},
		{"gemini vertex adc", config.TargetConfig{Provider: config.ProviderGemini, AuthMode: config.AuthGCPApplicationDefault, Project: "proj", Location: "us-central1"}, "gemini"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p, err := factory.Build(tc.target)
			if err != nil {
				t.Fatalf("build: %v", err)
			}
			if p.ID() != tc.wantID {
				t.Fatalf("ID = %q, want %q", p.ID(), tc.wantID)
			}
		})
	}
}

// TestBuildRejectsUnsupportedAuthMode checks that Build rejects an auth mode the
// family does not support.
func TestBuildRejectsUnsupportedAuthMode(t *testing.T) {
	_, err := factory.Build(config.TargetConfig{Provider: config.ProviderBedrock, Region: "us-east-1", AuthMode: "azure_managed_identity"})
	if err == nil {
		t.Fatal("want error for unsupported auth mode")
	}
}

// TestFamiliesMatchBuild checks that every family returned by Families builds
// and reports its own ID.
func TestFamiliesMatchBuild(t *testing.T) {
	fams := factory.Families()
	if len(fams) == 0 {
		t.Fatal("Families() is empty")
	}
	seen := map[string]bool{}
	for _, f := range fams {
		if seen[f] {
			t.Fatalf("duplicate family %q", f)
		}
		seen[f] = true

		tc := config.TargetConfig{Provider: types.ProviderID(f), Endpoint: "http://x"}
		switch f {
		case string(config.ProviderAzureOpenAI):
			tc.Deployment = "d"
		case string(config.ProviderBedrock):
			tc.Region = "us-east-1"
		}
		p, err := factory.Build(tc)
		if err != nil {
			t.Fatalf("family %q: Build error: %v", f, err)
		}
		if string(p.ID()) != f {
			t.Fatalf("family %q: adapter ID = %q", f, p.ID())
		}
	}
}
