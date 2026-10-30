// Package consolestore implements the Membrane Console ports (ConfigSource,
// ConfigEditor and related interfaces) on top of the embedded store. It may
// import the config, Membrane and store packages; the Console itself depends
// only on the ports, which keeps it isolated from the egress path.
//
// Reads turn store rows into views. Edits are validated with the daemon
// parsers, written in a transaction, and followed by a fail closed, last-good
// reload. The store holds secret references and hashes, never secret values.
package consolestore

import (
	"os"
	"sort"
	"strings"

	"github.com/inferencepath/membrane/internal/config"
	"github.com/inferencepath/membrane/internal/membrane/permeability"
	"github.com/inferencepath/membrane/internal/server/console"
	"github.com/inferencepath/membrane/internal/store"
	"github.com/inferencepath/membrane/internal/types"
)

// ReloadOutcome mirrors the daemon reload result so this package need not
// import app.
type ReloadOutcome struct {
	Applied   bool
	Unchanged bool
	Reason    string
}

// Reload runs the daemon's fail closed reload and reports the result.
type Reload func() ReloadOutcome

// SpecValidator dry-runs a full configuration across kinds, as
// app.ValidateSpecs does for the serving path. It returns nil if the documents
// would load, or else the first error, which carries no secret material.
type SpecValidator func(permBytes, callerBytes, credBytes, provBytes, capsBytes []byte) error

// Adapter implements console.ConfigSource, console.ConfigEditor, and
// console.ConfigBundle over the store.
type Adapter struct {
	st       *store.Store
	cfg      config.Config
	reload   Reload
	validate SpecValidator
}

var (
	_ console.ConfigSource   = (*Adapter)(nil)
	_ console.ConfigEditor   = (*Adapter)(nil)
	_ console.ConfigBundle   = (*Adapter)(nil)
	_ console.PathwayCreator = (*Adapter)(nil)
	_ console.CallerManager  = (*Adapter)(nil)
)

// Option configures an Adapter.
type Option func(*Adapter)

// WithValidator sets the cross-kind validator used by bundle apply and diff.
// Without it, only the per-kind parsers run.
func WithValidator(v SpecValidator) Option {
	return func(a *Adapter) { a.validate = v }
}

// New returns an Adapter over st. cfg supplies the bootstrap settings shown in
// the Settings view, and reload is called after each successful write.
func New(st *store.Store, cfg config.Config, reload Reload, opts ...Option) *Adapter {
	a := &Adapter{st: st, cfg: cfg, reload: reload}
	for _, opt := range opts {
		opt(a)
	}
	return a
}

// ConfigSource read views.

// Server returns the server, audit and Console settings view.
func (a *Adapter) Server() console.ServerView {
	return console.ServerView{
		Address:         a.cfg.Server.Address,
		ShutdownTimeout: a.cfg.Server.ShutdownTimeout.String(),
		AuditSink:       auditSinkOr(a.cfg.Audit.Sink),
		AuditPath:       a.cfg.Audit.Path,
		ConsoleEnabled:  a.cfg.Console.Enabled,
		ConsoleAddress:  a.cfg.Console.Address,
		ConsoleAuth:     a.cfg.Console.AuthTokenRef != "",
		Mode:            "managed",
		DataDir:         a.cfg.DataDir,
		EditingEnabled:  true,
	}
}

// Pathways returns the routing view from the store.
func (a *Adapter) Pathways() (console.PathwaysView, error) {
	rows, err := a.st.ReadPathways()
	if err != nil {
		return console.PathwaysView{}, err
	}
	view := console.PathwaysView{Source: "store"}
	for _, c := range rows {
		op := c.Operation
		if op == "" {
			op = "chat"
		}
		view.Pathways = append(view.Pathways, console.PathwayView{
			Pathway:         c.Name,
			Operation:       op,
			CascadeAllowed:  c.CascadeAllowed,
			DefaultProvider: c.DefaultProvider,
			Providers:       c.Providers,
		})
	}
	return view, nil
}

// Credentials returns credential references and whether each one resolves.
func (a *Adapter) Credentials() (console.CredentialsView, error) {
	rows, err := a.st.ReadCredentials()
	if err != nil {
		return console.CredentialsView{}, err
	}
	view := console.CredentialsView{}
	for _, c := range rows {
		view.Credentials = append(view.Credentials, console.CredentialView{
			Name:      c.Name,
			Scheme:    schemeOf(c.SecretRef),
			Reference: c.SecretRef,
			Resolves:  refResolves(c.SecretRef),
			HasNext:   c.NextSecretRef != "",
		})
	}
	return view, nil
}

// Callers returns Caller public IDs and allowlists, without hashes.
func (a *Adapter) Callers() (console.CallersView, error) {
	rows, err := a.st.ReadCallers()
	if err != nil {
		return console.CallersView{}, err
	}
	view := console.CallersView{}
	for _, c := range rows {
		view.Callers = append(view.Callers, console.CallerView{
			KeyID:    c.KeyID,
			CallerID: c.CallerID,
			Name:     c.Name,
			Allow:    append([]string(nil), c.Allow...),
		})
	}
	return view, nil
}

// Permeability returns the Membrane allowlist spec, sorted by pathway.
func (a *Adapter) Permeability() (console.PermeabilityView, error) {
	rows, err := a.st.ReadPermeability()
	if err != nil {
		return console.PermeabilityView{}, err
	}
	view := console.PermeabilityView{Version: 1, OutboundDenylist: permeability.Denylist()}
	for _, p := range rows {
		view.Pathways = append(view.Pathways, console.DirectionView{
			Pathway:         p.Pathway,
			OutboundAllow:   p.OutboundAllow,
			InboundDefault:  p.InboundDefault,
			InboundOnReveal: p.InboundOnReveal,
		})
	}
	sort.Slice(view.Pathways, func(i, j int) bool {
		return view.Pathways[i].Pathway < view.Pathways[j].Pathway
	})
	return view, nil
}

// Operations returns every operation with its egress route and Permeability
// fields. Chat has no fixed field vocabulary and is reported unrestricted;
// other operations list their fields from permeability.Vocabulary.
func (a *Adapter) Operations() console.OperationsView {
	var view console.OperationsView
	for _, op := range types.AllOperations() {
		ov := console.OperationView{
			Operation: op.String(),
			Route:     op.Route(),
		}
		if op == types.OperationChat {
			ov.Restricted = false
		} else {
			ov.Restricted = true
			outbound, inbound := permeability.Vocabulary(op)
			ov.OutboundAllow = outbound
			ov.InboundFields = inbound
		}
		view.Operations = append(view.Operations, ov)
	}
	return view
}

func auditSinkOr(s string) string {
	if s == "" {
		return "discard"
	}
	return s
}

func schemeOf(ref string) string {
	scheme, _, ok := strings.Cut(ref, ":")
	if !ok {
		return ""
	}
	return scheme
}

// refResolves reports whether an env: or file: reference currently has a
// value. Other schemes, including vault:, are not dereferenced here.
func refResolves(ref string) bool {
	scheme, rest, ok := strings.Cut(ref, ":")
	if !ok {
		return false
	}
	switch scheme {
	case "env":
		return os.Getenv(rest) != ""
	case "file":
		b, err := os.ReadFile(rest)
		return err == nil && strings.TrimSpace(string(b)) != ""
	default:
		return false
	}
}
