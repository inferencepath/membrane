package console

// A provider key is a sealed secret value together with the credential
// reference that points at it. The Providers screen manages both in one step;
// Credentials and Secrets stay available under Advanced for low-level editing.
//
// Values are write-only. No method or view field returns a plaintext secret,
// only references and status.

// ProviderView is the secret-free projection of one provider: its name, vendor
// family and connection facts, whether a sealed key is present, the reference
// form, and the pathways that use it.
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

// ProviderInput is the create/update payload for one provider. Secret-backed
// auth modes (api_key and the Bedrock bearer token) carry the plaintext value to
// seal, which is never echoed back. Ambient IAM modes need no value.
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

// ProviderManager manages providers over the sealed secret store and the
// credential store. It is implemented in internal/consolestore.
//
// Set seals the value and upserts the provider definition, including its
// secret:<name> reference, in one transaction. Delete removes both and refuses
// while a pathway target still references the provider.
type ProviderManager interface {
	List() (ProvidersView, error)
	Set(in ProviderInput) error
	Delete(name string) (bool, error)
}
