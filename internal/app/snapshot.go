// Package app is the Membrane composition root for the config-as-code
// model: it parses and validates the configuration files, assembles an immutable
// chat.Snapshot (routing + credentials + authorization + Permeability), and
// provides a ConfigManager that hot-reloads them atomically and fail-closed.
//
// It is the one place allowed to span zones (it wires Interior, Membrane, and
// Exterior together); no lower-level package imports it, so the trust-zone import
// rules are unaffected.
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

// secretRefPrefix is the prefix of a sealed-store credential reference
// (secret:<name>). Kept local to avoid importing the secrets package here.
const secretRefPrefix = "secret:"

// SecretResolver resolves a `secret:<name>` reference to its plaintext value,
// typically secrets.Manager.Resolve. When nil, sealed references are unsupported
// and any `secret:` credential fails closed at keyring build (not-ready).
type SecretResolver func(ref string) (string, error)

// keyringRefResolver composes the sealed-store resolver (for `secret:` refs) with
// the exterior keyring's default env:/file: resolver for everything else. A nil
// secretResolve yields nil, so keyring.New keeps its built-in default resolver
// (which rejects `secret:` — the desired fail-closed behavior).
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

// assembleSnapshot builds the immutable serving Snapshot from already-parsed,
// already-validated artifacts. It is the single assembly used by both the
// file-backed and store-backed builders, so the fail-closed cross-file checks
// (credential resolution, per-target adapter build) live in exactly one place.
func assembleSnapshot(spec permeability.Spec, store *admit.Store, creds config.KeyringFile, provsSpec config.ProvidersSpec, capsSpec config.PathwaysSpec, obs transporter.Observer, secretResolve SecretResolver) (*chat.Snapshot, *keyring.Keyring, error) {
	// Providers index: pathways reference a provider by name; the provider owns
	// all connection config, resolved here at wiring time.
	provByName := make(map[string]config.ProviderConfig, len(provsSpec.Providers))
	for _, p := range provsSpec.Providers {
		provByName[p.Name] = p
	}

	// Cross-kind referential integrity (fail closed). Pathways and Permeability
	// are separate documents linked ONLY by pathway name, so this is the single
	// place that link is enforced:
	//   - every routable pathway MUST have a Permeability policy — otherwise the
	//     config would load as "ready" yet the Membrane would reject every
	//     request to it at the Scrub ("no permeability for pathway");
	//   - every Permeability entry MUST name a declared pathway — a policy for a
	//     non-existent pathway is dead config and almost always a typo.
	// Both are reported here, at load, with a precise message rather than
	// surfacing only as a per-request failure much later.
	capNames := make(map[types.PathwayID]struct{}, len(capsSpec.Routes))
	for _, route := range capsSpec.Routes {
		capNames[route.Pathway] = struct{}{}
		ds, ok := spec.Pathways[route.Pathway]
		if !ok {
			return nil, nil, fmt.Errorf("pathway %q: no permeability policy (add a permeability entry named %q)", route.Pathway, route.Pathway)
		}
		// The Permeability policy's field names must belong to the Pathway's
		// Operation vocabulary (fail-closed). The outbound identity/control
		// denylist is enforced earlier, at Permeability parse; this adds the
		// per-operation gate now that the Operation is known.
		if verr := permeability.ValidateVocabulary(route.Pathway, route.Operation, ds); verr != nil {
			return nil, nil, verr
		}
	}
	for name := range spec.Pathways {
		if _, ok := capNames[name]; !ok {
			return nil, nil, fmt.Errorf("permeability references unknown pathway %q (no such pathway is configured)", name)
		}
	}

	// Fail closed: resolve every Egress Identity now. Each secret-backed provider
	// auth mode contributes a keyring entry (its name → its sealed-key reference);
	// ambient IAM providers have no stored secret and are skipped here (their
	// adapter authenticates from an ambient source). Advanced raw credentials
	// remain supported alongside.
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
	// When a sealed-store resolver is configured, resolve `secret:` references
	// from it (delegating env:/file: to the keyring default). Without one,
	// `secret:` references fail closed via the keyring's built-in resolver.
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
		// A pathway now routes to a SET of providers (one default). The pool
		// holds every allowed provider so the Transporter can execute a caller
		// cascade (resolved by the Interior) against any of them; ordering within
		// the pool (used only when no caller cascade is supplied) puts the
		// default provider first.
		ps := transporter.PoolSpec{Strategy: transporter.StrategyPriority}
		policy := resolve.PathwayPolicy{
			Operation:       route.Operation,
			Providers:       make(map[string]resolve.ProviderPolicy, len(route.Providers)),
			DefaultProvider: route.DefaultProvider,
			CascadeAllowed:  route.CascadeAllowed,
		}
		for i, name := range route.Providers {
			// Cross-file fail-closed check: the pathway must reference a provider
			// that exists.
			pc, ok := provByName[name]
			if !ok {
				return nil, nil, fmt.Errorf("pathway %q provider %d: unknown provider %q", route.Pathway, i, name)
			}
			// Fail-closed capability check: the provider's adapter must support
			// the Pathway's Operation.
			if !factory.FamilySupports(pc.Family, route.Operation) {
				return nil, nil, fmt.Errorf("pathway %q provider %d: provider %q (family %q) does not support operation %q", route.Pathway, i, pc.Name, pc.Family, route.Operation.String())
			}
			ambient := !config.AuthModeUsesSecret(pc.AuthMode)
			// For secret-backed providers the sealed credential value must exist
			// and resolve now (fail closed). Ambient IAM providers have no stored
			// key.
			if !ambient {
				if _, rerr := kr.Resolve(pc.Name); rerr != nil {
					return nil, nil, fmt.Errorf("pathway %q provider %d: provider %q: %w", route.Pathway, i, pc.Name, rerr)
				}
			}
			// Hydrate the concrete target from the provider's connection config.
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
			// Authoritative, structural capability gate: the built
			// adapter must actually implement the Pathway's Operation. This
			// catches a family declared (FamilySupports) but not yet implemented
			// by its adapter, failing closed at config assembly rather than at a
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
			// Defensive: the loader already enforces >=1 provider per pathway.
			return nil, nil, fmt.Errorf("pathway %q: no resolvable provider", route.Pathway)
		}
		pools[route.Pathway] = ps
		// An empty Binding keeps resolve.Resolve an existence gate; the
		// Transporter performs the real target selection.
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
