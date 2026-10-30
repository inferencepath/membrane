// Package app is the composition root. It validates configuration, assembles
// an immutable chat.Snapshot (routing, credentials, authorization and
// Permeability) and hot-reloads it atomically, failing closed.
//
// It is the only package that wires the Interior, Membrane and Exterior
// together. No lower-level package may import it.
package app

import (
	"fmt"
	"strings"

	"github.com/inferencepath/membrane/internal/config"
	"github.com/inferencepath/membrane/internal/exterior/keyring"
	"github.com/inferencepath/membrane/internal/exterior/providers/factory"
	"github.com/inferencepath/membrane/internal/interior/admit"
	"github.com/inferencepath/membrane/internal/interior/authorize"
	"github.com/inferencepath/membrane/internal/interior/resolve"
	"github.com/inferencepath/membrane/internal/membrane/permeability"
	"github.com/inferencepath/membrane/internal/membrane/transporter"
	"github.com/inferencepath/membrane/internal/server/chat"
	"github.com/inferencepath/membrane/internal/types"
)

// secretRefPrefix marks a sealed-store credential reference (secret:<name>).
// It is duplicated here to avoid importing the secrets package.
const secretRefPrefix = "secret:"

// SecretResolver resolves a secret:<name> reference to its plaintext value,
// typically secrets.Manager.Resolve. Without one, any secret: credential fails
// closed when the Keyring is built.
type SecretResolver func(ref string) (string, error)

// keyringRefResolver sends secret: references to secretResolve and everything
// else to the Keyring's env: and file: resolver. A nil secretResolve returns
// nil so keyring.New keeps its default, which rejects secret: references.
func keyringRefResolver(secretResolve SecretResolver) keyring.RefResolver {
	if secretResolve == nil {
		return nil
	}
	return func(ref string) (string, error) {
		if strings.HasPrefix(ref, secretRefPrefix) {
			return secretResolve(ref)
		}
		return keyring.ResolveRef(ref)
	}
}

// assembleSnapshot builds the serving Snapshot from parsed documents. All
// cross-document checks (references, credential resolution, adapter builds)
// happen here, so serving and dry-run validation fail closed the same way.
func assembleSnapshot(spec permeability.Spec, store *admit.Store, creds config.KeyringFile, provsSpec config.ProvidersSpec, capsSpec config.PathwaysSpec, obs transporter.Observer, secretResolve SecretResolver) (*chat.Snapshot, *keyring.Keyring, error) {
	// Pathways reference providers by name; the provider holds the connection
	// config.
	provByName := make(map[string]config.ProviderConfig, len(provsSpec.Providers))
	for _, p := range provsSpec.Providers {
		provByName[p.Name] = p
	}

	// Pathways and Permeability are linked only by name. Every routed Pathway
	// needs a Permeability policy, or the Scrub would reject every request to
	// it; every policy must name a configured Pathway, since an orphan is
	// almost always a typo. Both are load errors rather than request failures.
	capNames := make(map[types.PathwayID]struct{}, len(capsSpec.Routes))
	for _, route := range capsSpec.Routes {
		capNames[route.Pathway] = struct{}{}
		ds, ok := spec.Pathways[route.Pathway]
		if !ok {
			return nil, nil, fmt.Errorf("pathway %q: no permeability policy (add a permeability entry named %q)", route.Pathway, route.Pathway)
		}
		// Policy field names must belong to the Operation's vocabulary. This
		// needs the route's Operation, so it cannot run at Permeability parse.
		if verr := permeability.ValidateVocabulary(route.Pathway, route.Operation, ds); verr != nil {
			return nil, nil, verr
		}
	}
	for name := range spec.Pathways {
		if _, ok := capNames[name]; !ok {
			return nil, nil, fmt.Errorf("permeability references unknown pathway %q (no such pathway is configured)", name)
		}
	}

	// Each secret-backed provider adds a Keyring entry keyed by its name.
	// Ambient IAM providers have no stored secret and are skipped. Raw
	// credential entries are added as well.
	entries := make([]keyring.Entry, 0, len(provsSpec.Providers)+len(creds.Credentials))
	for _, p := range provsSpec.Providers {
		if config.AuthModeUsesSecret(p.AuthMode) {
			entries = append(entries, keyring.Entry{Name: p.Name, SecretRef: p.SecretRef})
		}
	}
	for _, c := range creds.Credentials {
		entries = append(entries, keyring.Entry{Name: c.Name, SecretRef: c.SecretRef, NextSecretRef: c.NextSecretRef})
	}
	krOpts := []keyring.Option{}
	if creds.Rotation.Interval > 0 {
		krOpts = append(krOpts, keyring.WithRotationInterval(creds.Rotation.Interval))
	}
	if r := keyringRefResolver(secretResolve); r != nil {
		krOpts = append(krOpts, keyring.WithRefResolver(r))
	}
	kr, err := keyring.New(entries, krOpts...)
	if err != nil {
		return nil, nil, fmt.Errorf("keyring: %w", err)
	}

	pools := make(map[types.PathwayID]transporter.PoolSpec, len(capsSpec.Routes))
	bindings := make(map[types.PathwayID]resolve.Binding, len(capsSpec.Routes))
	policies := make(map[types.PathwayID]resolve.PathwayPolicy, len(capsSpec.Routes))
	for _, route := range capsSpec.Routes {
		// The pool holds every provider the Pathway allows, so the Transporter
		// can run a Caller cascade against any of them. Without a cascade, the
		// default provider has the highest priority.
		ps := transporter.PoolSpec{Strategy: transporter.StrategyPriority}
		policy := resolve.PathwayPolicy{
			Operation:       route.Operation,
			Providers:       make(map[string]resolve.ProviderPolicy, len(route.Providers)),
			DefaultProvider: route.DefaultProvider,
			CascadeAllowed:  route.CascadeAllowed,
		}
		for i, name := range route.Providers {
			pc, ok := provByName[name]
			if !ok {
				return nil, nil, fmt.Errorf("pathway %q provider %d: unknown provider %q", route.Pathway, i, name)
			}
			if !factory.FamilySupports(pc.Family, route.Operation) {
				return nil, nil, fmt.Errorf("pathway %q provider %d: provider %q (family %q) does not support operation %q", route.Pathway, i, pc.Name, pc.Family, route.Operation.String())
			}
			ambient := !config.AuthModeUsesSecret(pc.AuthMode)
			// A secret-backed credential must resolve at load time.
			if !ambient {
				if _, rerr := kr.Resolve(pc.Name); rerr != nil {
					return nil, nil, fmt.Errorf("pathway %q provider %d: provider %q: %w", route.Pathway, i, pc.Name, rerr)
				}
			}
			tc := config.TargetConfig{
				Provider:   pc.Family,
				Model:      pc.DefaultModel,
				Endpoint:   pc.Endpoint,
				Deployment: pc.Deployment,
				Region:     pc.Region,
				Project:    pc.Project,
				Location:   pc.Location,
				KeyringRef: pc.Name,
				AuthMode:   pc.AuthMode,
				RoleARN:    pc.RoleARN,
				ExternalID: pc.ExternalID,
			}
			adapter, berr := factory.Build(tc)
			if berr != nil {
				return nil, nil, fmt.Errorf("pathway %q provider %d: %w", route.Pathway, i, berr)
			}
			// FamilySupports is declarative; this checks that the built adapter
			// implements the Operation, so a gap fails at load rather than on a
			// live request.
			if !factory.AdapterSupports(adapter, route.Operation) {
				return nil, nil, fmt.Errorf("pathway %q provider %d: provider %q (family %q) adapter does not implement operation %q", route.Pathway, i, pc.Name, pc.Family, route.Operation.String())
			}
			priority := i + 1
			if pc.Name == route.DefaultProvider {
				priority = 0
			}
			ps.Targets = append(ps.Targets, transporter.TargetSpec{
				Adapter:      adapter,
				ProviderName: pc.Name,
				KeyringRef:   pc.Name,
				Model:        pc.DefaultModel,
				Priority:     priority,
				AmbientAuth:  ambient,
			})
			policy.Providers[pc.Name] = resolve.ProviderPolicy{
				Models:       pc.Models,
				DefaultModel: pc.DefaultModel,
			}
		}
		if len(ps.Targets) == 0 {
			// The loader already requires at least one provider per Pathway.
			return nil, nil, fmt.Errorf("pathway %q: no resolvable provider", route.Pathway)
		}
		pools[route.Pathway] = ps
		// An empty Binding makes Resolve an existence check only; the
		// Transporter selects the target.
		bindings[route.Pathway] = resolve.Binding{}
		policies[route.Pathway] = policy
	}

	trans := transporter.New(pools, kr, transporter.WithObserver(obs))
	resolver := resolve.New(bindings)
	resolver.SetPolicy(policies)
	snap := &chat.Snapshot{
		Permeability: spec,
		Callers:      store,
		Authorizer:   authorize.New(store),
		Resolver:     resolver,
		Transport:    trans,
	}
	return snap, kr, nil
}
