package console

// The read-only config view types carry no identity and no secrets:
// credentials appear as references, callers as public IDs and allowlists, and
// routing as the server-side topology an operator may see. They and the
// ConfigSource port live here so the console imports no Interior, Membrane, or
// Exterior package; internal/consoleview implements the port.

// ServerView is the server/audit/console settings view.
type ServerView struct {
	Address         string `json:"address"`
	ShutdownTimeout string `json:"shutdownTimeout"`
	AuditSink       string `json:"auditSink"`
	AuditPath       string `json:"auditPath,omitempty"`
	ConsoleEnabled  bool   `json:"consoleEnabled"`
	ConsoleAddress  string `json:"consoleAddress"`
	ConsoleAuth     bool   `json:"consoleAuth"`
	// Mode is the config source, "managed" for the embedded store. DataDir is
	// the managed state directory. EditingEnabled reports whether live edits
	// are available.
	Mode           string `json:"mode,omitempty"`
	DataDir        string `json:"dataDir,omitempty"`
	EditingEnabled bool   `json:"editingEnabled"`
}

// TargetView is one Exterior routing target (server-side only).
type TargetView struct {
	Provider     string `json:"provider"`
	Model        string `json:"model"`
	Endpoint     string `json:"endpoint,omitempty"`
	Deployment   string `json:"deployment,omitempty"`
	Region       string `json:"region,omitempty"`
	KeyringRef   string `json:"keyringRef,omitempty"`
	Priority     int    `json:"priority"`
	TimeoutTotal string `json:"timeoutTotal,omitempty"`
	TimeoutIdle  string `json:"timeoutIdle,omitempty"`
}

// PathwayView maps one logical Pathway to its admin-attached provider set, the
// default provider, and whether a caller may supply its own cascade.
type PathwayView struct {
	Pathway         string   `json:"pathway"`
	Operation       string   `json:"operation"`
	CascadeAllowed  bool     `json:"cascadeAllowed"`
	DefaultProvider string   `json:"defaultProvider"`
	Providers       []string `json:"providers"`
}

// PathwaysView is the full routing view plus which file it came from.
type PathwaysView struct {
	Source   string        `json:"source"` // pathways.yaml | keyring.yaml | bindings
	Pathways []PathwayView `json:"pathways"`
}

// CredentialView is a named credential reference and whether it currently
// resolves. It never holds the secret value.
type CredentialView struct {
	Name      string `json:"name"`
	Scheme    string `json:"scheme"`    // env | file | vault
	Reference string `json:"reference"` // e.g. "env:OPENAI_API_KEY" (not the value)
	Resolves  bool   `json:"resolves"`
	HasNext   bool   `json:"hasNext,omitempty"`
}

// CredentialsView is the set of named credential references.
type CredentialsView struct {
	Credentials []CredentialView `json:"credentials"`
}

// CallerView is a Caller Key's public ID and allowlist, without secret or hash.
type CallerView struct {
	KeyID    string   `json:"keyId"`
	CallerID string   `json:"callerId"`
	Name     string   `json:"name,omitempty"`
	Allow    []string `json:"allow"`
}

// CallersView is the set of configured Callers (deny-by-default allowlists).
type CallersView struct {
	Callers []CallerView `json:"callers"`
}

// DirectionView is one Pathway's Permeability allowlists, per direction.
type DirectionView struct {
	Pathway         string   `json:"pathway"`
	OutboundAllow   []string `json:"outboundAllow"`
	InboundDefault  []string `json:"inboundDefault"`
	InboundOnReveal []string `json:"inboundOnReveal,omitempty"`
}

// PermeabilityView is the Membrane's allowlist spec. OutboundDenylist lists the
// hard-coded identity and control fields that can never be allowed outbound; it
// is informational.
type PermeabilityView struct {
	Version          int             `json:"version"`
	Pathways         []DirectionView `json:"pathways"`
	OutboundDenylist []string        `json:"outboundDenylist,omitempty"`
}

// ConfigSource provides read-only, identity-free views of the active
// configuration. Implementations read the config files or the active snapshot
// and must not expose secret values. A nil ConfigSource makes the config read
// endpoints return 503.
type ConfigSource interface {
	Server() ServerView
	Pathways() (PathwaysView, error)
	Credentials() (CredentialsView, error)
	Callers() (CallersView, error)
	Permeability() (PermeabilityView, error)
}
