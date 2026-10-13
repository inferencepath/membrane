package config_test

import (
	"strings"
	"testing"

	"github.com/inferencepath/membrane/internal/config"
)

func TestParsePathwaysValid(t *testing.T) {
	y := `version: 1
pathways:
  - pathway: chat
    cascade_allowed: true
    default_provider: anthropic-main
    providers: [openai-main, anthropic-main]
  - pathway: summarize
    providers: [local-main]
`
	spec, err := config.ParsePathways([]byte(y))
	if err != nil {
		t.Fatal(err)
	}
	if spec.Version != 1 || len(spec.Routes) != 2 {
		t.Fatalf("unexpected spec: %+v", spec)
	}
	if spec.Routes[0].Pathway != "chat" || len(spec.Routes[0].Providers) != 2 {
		t.Fatalf("route[0] = %+v", spec.Routes[0])
	}
	if !spec.Routes[0].CascadeAllowed || spec.Routes[0].DefaultProvider != "anthropic-main" {
		t.Fatalf("route[0] cascade/default = %+v", spec.Routes[0])
	}
	if spec.Routes[1].CascadeAllowed {
		t.Fatalf("route[1] cascade should default false")
	}
}

func TestParsePathwaysOperation(t *testing.T) {
	y := `version: 1
pathways:
  - pathway: chat
    providers: [openai-main]
  - pathway: embed
    operation: embeddings
    providers: [openai-main]
`
	spec, err := config.ParsePathways([]byte(y))
	if err != nil {
		t.Fatal(err)
	}
	// Absent operation defaults to chat.
	if spec.Routes[0].Operation != "chat" {
		t.Fatalf("route[0].Operation = %q, want chat (default)", spec.Routes[0].Operation)
	}
	if spec.Routes[1].Operation != "embeddings" {
		t.Fatalf("route[1].Operation = %q, want embeddings", spec.Routes[1].Operation)
	}
}

func TestParsePathwaysRejectsUnknownOperation(t *testing.T) {
	y := `version: 1
pathways:
  - pathway: chat
    operation: teleport
    providers: [openai-main]
`
	_, err := config.ParsePathways([]byte(y))
	if err == nil || !strings.Contains(err.Error(), "unknown operation") {
		t.Fatalf("want unknown operation error, got %v", err)
	}
}

func TestParsePathwaysDefaultProvider(t *testing.T) {
	y := `version: 1
pathways:
  - pathway: chat
    providers: [openai-main, anthropic-main]
`
	spec, err := config.ParsePathways([]byte(y))
	if err != nil {
		t.Fatal(err)
	}
	// Default provider defaults to the first listed provider when omitted.
	if spec.Routes[0].DefaultProvider != "openai-main" {
		t.Fatalf("default provider = %q, want openai-main", spec.Routes[0].DefaultProvider)
	}
}

func TestParsePathwaysRejects(t *testing.T) {
	tests := []struct {
		name, yaml, want string
	}{
		{"bad version", "version: 2\npathways: [{pathway: chat, providers: [openai-main]}]", "version must be 1"},
		{"no pathways", "version: 1\npathways: []", "at least one pathway"},
		{"bad pathway id", "version: 1\npathways: [{pathway: Chat, providers: [openai-main]}]", "invalid pathway"},
		{"dup pathway", "version: 1\npathways: [{pathway: chat, providers: [openai-main]}, {pathway: chat, providers: [openai-main]}]", "duplicate pathway"},
		{"no providers", "version: 1\npathways: [{pathway: chat, providers: []}]", "at least one provider"},
		{"missing provider", "version: 1\npathways: [{pathway: chat, providers: ['']}]", "provider is required"},
		{"bad provider name", "version: 1\npathways: [{pathway: chat, providers: [BAD]}]", "invalid provider"},
		{"dup provider", "version: 1\npathways: [{pathway: chat, providers: [openai-main, openai-main]}]", "duplicate provider"},
		{"bad default", "version: 1\npathways: [{pathway: chat, default_provider: nope, providers: [openai-main]}]", "default_provider"},
		{"unknown field", "version: 1\nmystery: true\npathways: [{pathway: chat, providers: [openai-main]}]", "parse"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := config.ParsePathways([]byte(tc.yaml))
			if err == nil {
				t.Fatalf("want error containing %q, got nil", tc.want)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %q, want contains %q", err.Error(), tc.want)
			}
		})
	}
}

func TestParseCredentialsValid(t *testing.T) {
	y := `version: 1
keyring:
  - {name: openai-main, secret_ref: env:OPENAI_API_KEY}
  - {name: anthropic-main, secret_ref: file:/run/secrets/anthropic, next_secret_ref: env:ANTHROPIC_NEXT}
rotation:
  interval: 5m
  overlap: 30s
`
	kf, err := config.ParseCredentials([]byte(y))
	if err != nil {
		t.Fatal(err)
	}
	if len(kf.Credentials) != 2 {
		t.Fatalf("unexpected credentials file: %+v", kf)
	}
	if kf.Rotation.Interval.String() != "5m0s" {
		t.Fatalf("rotation interval = %v", kf.Rotation.Interval)
	}
}

func TestParseCredentialsAcceptsSecretScheme(t *testing.T) {
	y := `version: 1
keyring:
  - {name: openai-main, secret_ref: secret:openai-main}
  - {name: anthropic-main, secret_ref: secret:anthropic-main, next_secret_ref: secret:anthropic-next}
`
	kf, err := config.ParseCredentials([]byte(y))
	if err != nil {
		t.Fatalf("expected secret: scheme to be accepted, got %v", err)
	}
	if len(kf.Credentials) != 2 || kf.Credentials[0].SecretRef != "secret:openai-main" {
		t.Fatalf("unexpected credentials: %+v", kf.Credentials)
	}
}

func TestParseCredentialsRejects(t *testing.T) {
	tests := []struct {
		name, yaml, want string
	}{
		{"bad version", "version: 2\nkeyring: [{name: a, secret_ref: env:X}]", "version must be 1"},
		{"no entries", "version: 1\nkeyring: []", "at least one keyring entry"},
		{"inline secret", "version: 1\nkeyring: [{name: a, secret_ref: sk-inline}]", "inline secrets are not allowed"},
		{"dup name", "version: 1\nkeyring: [{name: a, secret_ref: env:X}, {name: a, secret_ref: env:Y}]", "duplicate name"},
		{"bad name", "version: 1\nkeyring: [{name: BAD, secret_ref: env:X}]", "invalid name"},
		// pools must NOT appear in a credentials-only file.
		{"pools rejected", "version: 1\nkeyring: [{name: a, secret_ref: env:X}]\npools: [{pathway: chat, targets: []}]", "parse"},
		{"bad next_secret_ref", "version: 1\nkeyring: [{name: a, secret_ref: env:X, next_secret_ref: inline}]", "next_secret_ref"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := config.ParseCredentials([]byte(tc.yaml))
			if err == nil {
				t.Fatalf("want error containing %q, got nil", tc.want)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %q, want contains %q", err.Error(), tc.want)
			}
		})
	}
}
