package console

// This file defines the Console's Provider port: the operator-facing consolidation
// of "a provider key" = a sealed secret value PLUS the credential reference that
// points at it. The Providers screen is the primary setup surface; Credentials and
// Secrets remain available under Advanced for low-level editing.
//
// Isolation holds: like every console port, values are WRITE-ONLY. No method or
// view field ever returns a plaintext secret value — only references and status.

// ProviderView is the secret-free projection of one provider: its name, vendor
// family and connection facts, whether a sealed key is present, the reference
// form, and the pathways that use it. Never a sealed value.
type ProviderView struct {
	Name         string   `json:"name"`
	Family       string   `json:"family"`
	AuthMode     string   `json:"authMode"`
	RoleARN      string   `json:"roleArn,omitempty"`
	ExternalID   string   `json:"externalId,omitempty"`
	Models       []string `json:"models"`
	DefaultModel string   `json:"defaultModel"`
	Endpoint     string   `json:"endpoint,omitempty"`
	Deployment   string   `json:"deployment,omitempty"`
	Region       string   `json:"region,omitempty"`
	Project      string   `json:"project,omitempty"`
	Location     string   `json:"location,omitempty"`
	Sealed       bool     `json:"sealed"`
	SecretRef    string   `json:"secretRef"`
	UsedBy       []string `json:"usedBy"`
}

// ProviderInput is the create/update payload for one provider: its name, family,
// auth mode (and role for aws_assume_role), connection facts, the admin-controlled
// allowed models with a default, and — for secret-backed auth modes (api_key and
// bedrock bearer-token) — the plaintext credential value to seal. The value is
// write-only and never echoed back; ambient IAM modes require no value.
type ProviderInput struct {
	Name         string   `json:"name"`
	Family       string   `json:"family"`
	AuthMode     string   `json:"authMode"`
	RoleARN      string   `json:"roleArn"`
	ExternalID   string   `json:"externalId"`
	Models       []string `json:"models"`
	DefaultModel string   `json:"defaultModel"`
	Endpoint     string   `json:"endpoint"`
	Deployment   string   `json:"deployment"`
	Region       string   `json:"region"`
	Project      string   `json:"project"`
	Location     string   `json:"location"`
	Value        string   `json:"value"`
}

// ProvidersView is the list payload for GET /api/console/providers.
type ProvidersView struct {
	Providers []ProviderView `json:"providers"`
}

// ProviderManager is the console's app-layer port for managing providers. It is
// implemented in the app layer (consolestore) over the sealed secret store and
// the credential store; the console package never imports those directly.
//
// Set atomically seals the value and upserts the provider definition (family +
// connection facts + the `secret:<name>` reference). Delete removes both,
// refusing while a pathway target still references it.
type ProviderManager interface {
	List() (ProvidersView, error)
	Set(in ProviderInput) error
	Delete(name string) (bool, error)
}
