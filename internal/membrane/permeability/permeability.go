// Package permeability defines and loads the Permeability spec — the per-
// Pathway, per-direction allowlist that is the single source of truth for the
// Membrane. Loading fails closed: an invalid or unsafe spec is rejected and no
// Spec is returned.
package permeability

import (
	"bytes"
	"fmt"
	"os"
	"regexp"
	"sort"

	"gopkg.in/yaml.v3"

	"github.com/inferencepath/membrane/internal/types"
)

// SupportedVersion is the only Permeability spec version accepted.
const SupportedVersion = 1

// Spec is a validated Permeability specification.
type Spec struct {
	Version  int
	Pathways map[types.PathwayID]DirectionSets
}

// DirectionSets holds the per-direction allowlists for one Pathway, plus the
// optional per-Pathway tool-calling governance policy.
type DirectionSets struct {
	Outbound Outbound
	Inbound  Inbound
	Tool     ToolPolicy
}

// ToolPolicy is the operator-owned, per-Pathway governance for tool calling
// It is enforced on the Interior before any Membrane crossing.
// A zero value imposes no tool-name allow/deny, no caps, no redaction, and no
// destination restriction (tool calling is still gated by the outbound allowlist
// — closed by default). All fields are additive: absent sub-keys mean "no
// restriction" for that dimension.
type ToolPolicy struct {
	// Allow, when non-empty, is the exhaustive set of tool names a Caller may
	// offer; any other tool name is rejected on the Interior.
	Allow []string
	// Deny is a set of tool names that are always rejected (takes precedence over
	// Allow).
	Deny []string
	// MaxTools caps the number of tool definitions per request (0 = unlimited).
	MaxTools int
	// MaxToolResultBytes caps the byte length of each tool-role result content
	// (0 = unlimited).
	MaxToolResultBytes int
	// Redact is the set of compiled patterns applied to tool-role result content
	// before the Membrane crossing (empty = no redaction).
	Redact []*regexp.Regexp
	// Destinations, when non-empty, is the set of provider names for which tool
	// calling is enabled on this Pathway. A tool request that could route to any
	// provider outside this set is rejected pre-egress (destination-scoped
	// enablement — e.g. tools only on internal/self-hosted destinations).
	Destinations []string
}

// ToolPolicy returns the governance policy for a Pathway (zero value when the
// Pathway is unknown or declares no tool_policy).
func (s Spec) ToolPolicy(pathway types.PathwayID) ToolPolicy {
	return s.Pathways[pathway].Tool
}

// RedactionPlaceholder replaces each match of a configured redaction pattern in
// tool-role result content before egress.
const RedactionPlaceholder = "[REDACTED]"

// ToolAllowed reports whether a Caller may offer a tool of the given name under
// this policy. Deny takes precedence over Allow; an empty Allow permits any name
// not explicitly denied.
func (p ToolPolicy) ToolAllowed(name string) bool {
	for _, d := range p.Deny {
		if d == name {
			return false
		}
	}
	if len(p.Allow) == 0 {
		return true
	}
	for _, a := range p.Allow {
		if a == name {
			return true
		}
	}
	return false
}

// DestinationAllowed reports whether tool calling may cross to the named
// provider. An empty Destinations set imposes no restriction (the outbound
// allowlist already gates tool calling for the Pathway).
func (p ToolPolicy) DestinationAllowed(provider string) bool {
	if len(p.Destinations) == 0 {
		return true
	}
	for _, d := range p.Destinations {
		if d == provider {
			return true
		}
	}
	return false
}

// RedactResult applies the configured redaction patterns to tool-role result
// content before egress. With no patterns it returns the content unchanged.
func (p ToolPolicy) RedactResult(content string) string {
	for _, re := range p.Redact {
		content = re.ReplaceAllString(content, RedactionPlaceholder)
	}
	return content
}

// Outbound is the Interior->Exterior allowlist. It is strict: it must never list
// an identity or control field.
type Outbound struct {
	Allow []string
}

// Inbound is the Exterior->Interior allowlist. Default is provider-neutral;
// OnReveal adds fields only when the Caller set the Reveal flag.
type Inbound struct {
	Default  []string
	OnReveal []string
}

// denylist are logical field names that must never appear in an outbound
// allowlist (identity or Interior-only control fields).
var denylist = map[string]struct{}{
	"caller_key":      {},
	"authorization":   {},
	"ip":              {},
	"x_forwarded_for": {},
	"tenant":          {},
	"user":            {},
	"metadata":        {},
	"cookie":          {},
	"user_agent":      {},
	"reveal":          {},
}

// Denylist returns the sorted set of logical field names that may NEVER appear
// in an outbound allowlist (identity and Interior-only control fields). It is a
// hard-coded, immutable Membrane-wide floor enforced at load; this accessor
// exposes it read-only (e.g. for the Console to display why a save was
// rejected). The returned slice is a copy; mutating it does not affect the
// enforced denylist.
func Denylist() []string {
	out := make([]string, 0, len(denylist))
	for name := range denylist {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

var fieldNameRe = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)

type specYAML struct {
	Version  int                          `yaml:"version"`
	Pathways map[string]directionSetsYAML `yaml:"pathways"`
}

type directionSetsYAML struct {
	Outbound   outboundYAML    `yaml:"outbound"`
	Inbound    inboundYAML     `yaml:"inbound"`
	ToolPolicy *toolPolicyYAML `yaml:"tool_policy"`
}

type toolPolicyYAML struct {
	ToolsAllow         []string `yaml:"tools_allow"`
	ToolsDeny          []string `yaml:"tools_deny"`
	MaxTools           int      `yaml:"max_tools"`
	MaxToolResultBytes int      `yaml:"max_tool_result_bytes"`
	Redact             []string `yaml:"redact"`
	Destinations       []string `yaml:"destinations"`
}

type outboundYAML struct {
	Allow []string `yaml:"allow"`
}

type inboundYAML struct {
	Default  []string `yaml:"default"`
	OnReveal []string `yaml:"on_reveal"`
}

// Load reads and validates the Permeability spec at path. It fails closed: any
// error means no spec is returned and the egress path must not be enabled.
func Load(path string) (Spec, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Spec{}, fmt.Errorf("permeability: read %s: %w", path, err)
	}
	return Parse(data)
}

// Parse validates raw YAML bytes into a Spec, rejecting unknown fields.
func Parse(data []byte) (Spec, error) {
	var sy specYAML
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&sy); err != nil {
		return Spec{}, fmt.Errorf("permeability: parse: %w", err)
	}
	if sy.Version != SupportedVersion {
		return Spec{}, fmt.Errorf("permeability: unsupported version %d (want %d)", sy.Version, SupportedVersion)
	}
	if len(sy.Pathways) == 0 {
		return Spec{}, fmt.Errorf("permeability: at least one pathway is required")
	}

	spec := Spec{Version: sy.Version, Pathways: make(map[types.PathwayID]DirectionSets, len(sy.Pathways))}
	for name, ds := range sy.Pathways {
		out, err := validateOutbound(name, ds.Outbound.Allow)
		if err != nil {
			return Spec{}, err
		}
		def, err := validateFieldList(name, "inbound.default", ds.Inbound.Default, false)
		if err != nil {
			return Spec{}, err
		}
		if len(def) == 0 {
			return Spec{}, fmt.Errorf("permeability: pathway %q: inbound.default must not be empty", name)
		}
		rev, err := validateFieldList(name, "inbound.on_reveal", ds.Inbound.OnReveal, false)
		if err != nil {
			return Spec{}, err
		}
		tool, err := validateToolPolicy(name, ds.ToolPolicy)
		if err != nil {
			return Spec{}, err
		}
		spec.Pathways[types.PathwayID(name)] = DirectionSets{
			Outbound: Outbound{Allow: out},
			Inbound:  Inbound{Default: def, OnReveal: rev},
			Tool:     tool,
		}
	}
	return spec, nil
}

// validateToolPolicy validates and compiles an optional per-Pathway tool policy.
// It fails closed: non-negative caps are required and every redaction pattern
// must compile. A nil policy yields the permissive zero value.
func validateToolPolicy(pathway string, p *toolPolicyYAML) (ToolPolicy, error) {
	if p == nil {
		return ToolPolicy{}, nil
	}
	if p.MaxTools < 0 {
		return ToolPolicy{}, fmt.Errorf("permeability: pathway %q: tool_policy.max_tools must not be negative", pathway)
	}
	if p.MaxToolResultBytes < 0 {
		return ToolPolicy{}, fmt.Errorf("permeability: pathway %q: tool_policy.max_tool_result_bytes must not be negative", pathway)
	}
	redact := make([]*regexp.Regexp, 0, len(p.Redact))
	for i, pat := range p.Redact {
		re, err := regexp.Compile(pat)
		if err != nil {
			return ToolPolicy{}, fmt.Errorf("permeability: pathway %q: tool_policy.redact[%d]: invalid pattern: %w", pathway, i, err)
		}
		redact = append(redact, re)
	}
	return ToolPolicy{
		Allow:              p.ToolsAllow,
		Deny:               p.ToolsDeny,
		MaxTools:           p.MaxTools,
		MaxToolResultBytes: p.MaxToolResultBytes,
		Redact:             redact,
		Destinations:       p.Destinations,
	}, nil
}

func validateOutbound(pathway string, allow []string) ([]string, error) {
	if len(allow) == 0 {
		return nil, fmt.Errorf("permeability: pathway %q: outbound.allow must not be empty (fail-closed)", pathway)
	}
	return validateFieldList(pathway, "outbound.allow", allow, true)
}

func validateFieldList(pathway, field string, list []string, checkDenylist bool) ([]string, error) {
	seen := make(map[string]struct{}, len(list))
	for _, f := range list {
		if !fieldNameRe.MatchString(f) {
			return nil, fmt.Errorf("permeability: pathway %q: %s: invalid field name %q", pathway, field, f)
		}
		if _, dup := seen[f]; dup {
			return nil, fmt.Errorf("permeability: pathway %q: %s: duplicate field %q", pathway, field, f)
		}
		seen[f] = struct{}{}
		if checkDenylist {
			if _, bad := denylist[f]; bad {
				return nil, fmt.Errorf("permeability: pathway %q: outbound.allow must not contain identity/control field %q", pathway, f)
			}
		}
	}
	return list, nil
}
