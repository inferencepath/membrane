// Package permeability defines and loads the Permeability spec: the
// per-Pathway, per-direction allowlists that govern what crosses the Membrane.
// Loading fails closed; an invalid or unsafe spec is rejected.
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

// DirectionSets holds the allowlists for one Pathway and its optional tool
// policy.
type DirectionSets struct {
	Outbound Outbound
	Inbound  Inbound
	Tool     ToolPolicy
}

// ToolPolicy is the operator's per-Pathway policy for tool calling, enforced on
// the Interior before the request reaches the Membrane. Each field left unset
// imposes no restriction; tool fields still cross only when the outbound
// allowlist names them.
type ToolPolicy struct {
	// Allow, if non-empty, lists the only tool names a Caller may offer.
	Allow []string
	// Deny lists tool names that are always rejected. It overrides Allow.
	Deny []string
	// MaxTools caps the number of tool definitions per request; 0 means no
	// limit.
	MaxTools int
	// MaxToolResultBytes caps the length of each tool result; 0 means no limit.
	MaxToolResultBytes int
	// Redact holds patterns masked in tool results before egress.
	Redact []*regexp.Regexp
	// Destinations, if non-empty, lists the providers for which tool calling
	// is enabled on this Pathway, for example only self-hosted ones. A tool
	// request that could route elsewhere is rejected before egress.
	Destinations []string
}

// ToolPolicy returns the tool policy for pathway, or the zero value if the
// Pathway is unknown or has no tool_policy.
func (s Spec) ToolPolicy(pathway types.PathwayID) ToolPolicy {
	return s.Pathways[pathway].Tool
}

// RedactionPlaceholder replaces each match of a Redact pattern in a tool
// result.
const RedactionPlaceholder = "[REDACTED]"

// ToolAllowed reports whether a Caller may offer the named tool. Deny wins over
// Allow, and an empty Allow permits any name not denied.
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

// DestinationAllowed reports whether tool calling may go to the named provider.
// An empty Destinations list allows any provider.
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

// RedactResult masks every Redact match in a tool result. With no patterns it
// returns content unchanged.
func (p ToolPolicy) RedactResult(content string) string {
	for _, re := range p.Redact {
		content = re.ReplaceAllString(content, RedactionPlaceholder)
	}
	return content
}

// Outbound is the allowlist for fields leaving the Interior. It may not list an
// identity or control field.
type Outbound struct {
	Allow []string
}

// Inbound is the allowlist for fields returning to the Interior. Default holds
// provider-neutral fields; OnReveal adds fields when the Caller requests Reveal.
type Inbound struct {
	Default  []string
	OnReveal []string
}

// denylist holds identity and Interior control fields that may not appear in
// an outbound allowlist.
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

// Denylist returns, sorted, the field names that may not appear in any outbound
// allowlist. The list is fixed and enforced at load; the Console uses it to
// explain rejected saves. The result is a copy.
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

// Load reads and validates the Permeability spec at path. On error the egress
// path must stay disabled.
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

// validateToolPolicy validates and compiles an optional tool policy. Caps must
// be non-negative and every redaction pattern must compile. A nil policy yields
// the zero value.
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
