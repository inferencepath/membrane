// Package consolestore adapts the embedded state store into the Membrane
// Console's ports: the read-only ConfigSource views and the structured/raw
// ConfigEditor. It is an app-layer adapter (like consoleview/consoleedit): it
// may import config, Interior, Membrane, and store packages to read/validate the
// managed configuration, then maps them to the console's secret-free types. The
// console itself imports none of those — it depends only on the injected ports —
// so the console's isolation from the egress path holds.
//
// The store is the single source of truth. Reads project rows into views; edits
// validate with the daemon's own parsers, write rows transactionally, then
// trigger the fail-closed, last-good reload. Secrets stay by reference: the
// store holds references and hashes only, never a secret value.
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

// ReloadOutcome mirrors the daemon's reload result without importing app, so the
// adapter stays decoupled from the composition root.
type ReloadOutcome struct {
	Applied   bool
	Unchanged bool
	Reason    string
}

// Reload triggers the daemon's atomic, fail-closed reload and reports the result.
type Reload func() ReloadOutcome

// SpecValidator is the cross-kind dry-run validator (app.ValidateSpecs), injected
// so the adapter validates a candidate bundle identically to the serving path
// without importing the composition root. It receives the four canonical
// documents and returns the first secret-free error, or nil when the whole
// configuration would load.
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

// WithValidator injects the cross-kind validator used by the declarative
// bundle-apply path. When unset, bundle apply/diff falls back to the per-kind
// parsers only (no cross-kind credential/adapter checks).
func WithValidator(v SpecValidator) Option {
	return func(a *Adapter) { a.validate = v }
}

// New returns an Adapter over the store. cfg supplies the bootstrap server/audit/
// console settings for the Settings view; reload triggers the managed reload.
func New(st *store.Store, cfg config.Config, reload Reload, opts ...Option) *Adapter {
	a := &Adapter{st: st, cfg: cfg, reload: reload}
	for _, opt := range opts {
		opt(a)
	}
	return a
}

// --- ConfigSource (read views) -------------------------------------------

// Server returns the server/audit/console settings view, annotated with the
// managed mode so the console can show operators how the daemon is configured.
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

// Credentials returns named credential references plus a resolve indicator,
// never a value.
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

// Callers returns Caller public ids + allowlists (never a secret or hash).
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

// Operations returns the console's operations catalog: the closed operation enum
// (stable tier order) with each operation's canonical egress route and its
// per-operation Permeability field palette. `chat` is reported unrestricted (no
// closed vocabulary); every other operation carries its sorted outbound/inbound
// field sets from the single source of truth, permeability.Vocabulary.
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

// refResolves reports whether a secret reference currently resolves, WITHOUT
// surfacing its value. env:/file: are checked directly; vault: is not
// dereferenced in this read-only view.
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
