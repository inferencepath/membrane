// Package config also parses the pathways document, which maps each Pathway
// to the Providers it may route to. Parsing fails closed on unknown fields and
// on malformed or duplicate names. Provider names are resolved against the
// providers document later, at wiring time.
package config

import (
	"bytes"
	"fmt"

	"gopkg.in/yaml.v3"

	"github.com/inferencepath/membrane/internal/types"
)

// PathwaysSpec is the parsed routing configuration, one route per Pathway. It
// is never disclosed to a Caller.
type PathwaysSpec struct {
	Version int
	Routes  []PathwayRoute
}

// PathwayRoute maps one Pathway to the providers it may route to, the default
// provider used when the caller sends no cascade, and whether a caller may
// send its own provider and model cascade. Connection settings and allowed
// models belong to the provider.
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

// ParsePathways validates raw YAML into a PathwaysSpec. It rejects unknown
// fields, unknown operations, malformed or duplicate pathway and provider
// names, empty provider lists, and a default_provider outside the list.
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
			// Whether the provider exists is checked at wiring time.
			route.Providers = append(route.Providers, p)
		}
		// An explicit default must be in the list; otherwise the first
		// provider is the default.
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
