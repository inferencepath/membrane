package providers_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/inferencepath/membrane/internal/exterior/providers"
)

func TestUpstreamStatusError(t *testing.T) {
	err := providers.UpstreamStatusError{Status: 503, Provider: "anthropic"}
	msg := err.Error()
	if !strings.Contains(msg, "503") {
		t.Fatalf("Error() = %q, want it to carry the status", msg)
	}
	// The message must stay provider-neutral: no provider body or identity, even
	// though the Provider family is retained on the struct for Reveal gating.
	for _, forbidden := range []string{"openai", "anthropic"} {
		if strings.Contains(strings.ToLower(msg), forbidden) {
			t.Fatalf("Error() leaked provider identity %q: %q", forbidden, msg)
		}
	}
}

func TestClassifyFailure(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want providers.FailureKind
	}{
		{"timeout", providers.ErrUpstreamTimeout, providers.FailureTimeout},
		{"deadline", context.DeadlineExceeded, providers.FailureTimeout},
		{"wrapped timeout", fmt.Errorf("x: %w", providers.ErrUpstreamTimeout), providers.FailureTimeout},
		{"503 unavailable", providers.UpstreamStatusError{Status: 503}, providers.FailureUnavailable},
		{"504 unavailable", providers.UpstreamStatusError{Status: 504}, providers.FailureUnavailable},
		{"500 provider", providers.UpstreamStatusError{Status: 500}, providers.FailureProvider},
		{"401 provider", providers.UpstreamStatusError{Status: 401}, providers.FailureProvider},
		{"429 provider", providers.UpstreamStatusError{Status: 429}, providers.FailureProvider},
		{"400 provider", providers.UpstreamStatusError{Status: 400}, providers.FailureProvider},
		{"unknown provider", errors.New("boom"), providers.FailureProvider},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := providers.ClassifyFailure(tc.err); got != tc.want {
				t.Fatalf("ClassifyFailure(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

func TestRevealableProvider(t *testing.T) {
	if got := providers.RevealableProvider(providers.UpstreamStatusError{Status: 500, Provider: "bedrock"}); got != "bedrock" {
		t.Fatalf("RevealableProvider = %q, want bedrock", got)
	}
	if got := providers.RevealableProvider(fmt.Errorf("w: %w", providers.UpstreamStatusError{Status: 500, Provider: "azure-openai"})); got != "azure-openai" {
		t.Fatalf("RevealableProvider (wrapped) = %q, want azure-openai", got)
	}
	if got := providers.RevealableProvider(providers.ErrUpstreamTimeout); got != "" {
		t.Fatalf("RevealableProvider(timeout) = %q, want empty", got)
	}
}
