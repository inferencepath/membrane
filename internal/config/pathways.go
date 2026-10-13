// Package config also loads the routing file (pathways.yaml): the
// config-as-code mapping from each logical Pathway to its pool of Provider
// targets and selection strategy. It is parse-only and fails closed: unknown
// fields, unknown providers/strategies, duplicate or malformed Pathway ids,
// missing targets, and inline secrets are all rejected. Credentials never appear
// here — each target references one by keyring_ref into the credentials file;
// cross-file resolution happens at wiring time.
package config

import (
	"bytes"
	"fmt"

	"gopkg.in/yaml.v3"

	"github.com/inferencepath/membrane/internal/types"
)

// PathwaysSpec is the parsed routing configuration: one route per logical
// Pathway. Server-side only; never disclosed to a Caller.
type PathwaysSpec struct {
	Version int
	Routes  []PathwayRoute
}

// PathwayRoute maps one Pathway to the admin-attached set of providers it may
// route to, which one is the default (used when the caller supplies no cascade),
// and whether a caller may supply its own provider+model cascade at request time.
// All connection config and the allowed-model policy live on the provider,
// resolved at wiring time; the pathway only selects providers by name.
type PathwayRoute struct {
	Pathway         types.PathwayID
	Operation       types.Operation
	Providers       []string
	DefaultProvider string
	CascadeAllowed  bool
}

type pathwaysFileYAML struct {
	Version  int                `yaml:"version"`
	Pathways []pathwayRouteYAML `yaml:"pathways"`
}

type pathwayRouteYAML struct {
	Pathway         string   `yaml:"pathway"`
	Operation       string   `yaml:"operation"`
	CascadeAllowed  bool     `yaml:"cascade_allowed"`
	DefaultProvider string   `yaml:"default_provider"`
	Providers       []string `yaml:"providers"`
}

// ParsePathways validates raw YAML bytes into a PathwaysSpec, rejecting
// unknown fields, unknown providers/strategies, duplicate/malformed Pathway
// ids, empty target lists, and inline secrets. keyring_ref values are validated
// structurally; their existence is checked against the credentials file at
// wiring time.
func ParsePathways(data []byte) (PathwaysSpec, error) {
	var fc pathwaysFileYAML
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&fc); err != nil {
		return PathwaysSpec{}, fmt.Errorf("pathways config: parse: %w", err)
	}

	if fc.Version != 1 {
		return PathwaysSpec{}, fmt.Errorf("pathways config: version must be 1, got %d", fc.Version)
	}
	if len(fc.Pathways) == 0 {
		return PathwaysSpec{}, fmt.Errorf("pathways config: at least one pathway is required")
	}

	spec := PathwaysSpec{Version: fc.Version}
	seen := make(map[string]struct{}, len(fc.Pathways))
	for ci, c := range fc.Pathways {
		if !pathwayRe.MatchString(c.Pathway) {
			return PathwaysSpec{}, fmt.Errorf("pathways config: pathways[%d]: invalid pathway %q", ci, c.Pathway)
		}
		if _, dup := seen[c.Pathway]; dup {
			return PathwaysSpec{}, fmt.Errorf("pathways config: pathways[%d]: duplicate pathway %q", ci, c.Pathway)
		}
		seen[c.Pathway] = struct{}{}
		op, operr := types.ParseOperation(c.Operation)
		if operr != nil {
			return PathwaysSpec{}, fmt.Errorf("pathways config: pathways[%d] (%s): %w", ci, c.Pathway, operr)
		}
		if len(c.Providers) == 0 {
			return PathwaysSpec{}, fmt.Errorf("pathways config: pathways[%d] (%s): at least one provider is required", ci, c.Pathway)
		}
		route := PathwayRoute{Pathway: types.PathwayID(c.Pathway), Operation: op, CascadeAllowed: c.CascadeAllowed}
		provSet := make(map[string]struct{}, len(c.Providers))
		for pi, p := range c.Providers {
			if p == "" {
				return PathwaysSpec{}, fmt.Errorf("pathways config: pathways[%d] (%s): providers[%d]: provider is required", ci, c.Pathway, pi)
			}
			if !keyringNameRe.MatchString(p) {
				return PathwaysSpec{}, fmt.Errorf("pathways config: pathways[%d] (%s): providers[%d]: invalid provider %q", ci, c.Pathway, pi, p)
			}
			if _, dup := provSet[p]; dup {
				return PathwaysSpec{}, fmt.Errorf("pathways config: pathways[%d] (%s): duplicate provider %q", ci, c.Pathway, p)
			}
			provSet[p] = struct{}{}
			// Provider existence is resolved against the providers config at
			// wiring time, not here.
			route.Providers = append(route.Providers, p)
		}
		// Default provider: explicit must be in the set; otherwise the first
		// listed provider is the default.
		def := c.DefaultProvider
		if def == "" {
			def = route.Providers[0]
		} else if _, ok := provSet[def]; !ok {
			return PathwaysSpec{}, fmt.Errorf("pathways config: pathways[%d] (%s): default_provider %q is not in the provider list", ci, c.Pathway, def)
		}
		route.DefaultProvider = def
		spec.Routes = append(spec.Routes, route)
	}

	return spec, nil
}
