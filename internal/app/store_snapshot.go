package app

import (
	"fmt"

	"github.com/inferencepath/membrane/internal/config"
	"github.com/inferencepath/membrane/internal/exterior/keyring"
	"github.com/inferencepath/membrane/internal/interior/admit"
	"github.com/inferencepath/membrane/internal/membrane/permeability"
	"github.com/inferencepath/membrane/internal/membrane/transporter"
	"github.com/inferencepath/membrane/internal/server/chat"
	"github.com/inferencepath/membrane/internal/store"
)

// BuildSnapshotFromStore assembles the serving Snapshot from the embedded state
// store. It renders the stored rows into the canonical config documents (via the
// store's own renderers) and runs them through the daemon's own parsers, so the
// store-backed path enforces the identical validation (and fail-closed
// semantics) as the file-backed path — there is no second, weaker validator.
// Secrets remain by reference throughout.
//
// This is the no-sealed-store variant: `secret:` credential references fail
// closed. Use BuildSnapshotFromStoreWithSecrets to enable the sealed store.
func BuildSnapshotFromStore(st *store.Store, obs transporter.Observer) (*chat.Snapshot, *keyring.Keyring, error) {
	return BuildSnapshotFromStoreWithSecrets(st, obs, nil)
}

// BuildSnapshotFromStoreWithSecrets is BuildSnapshotFromStore with a sealed-store
// resolver for `secret:<name>` credential references (secrets.Manager.Resolve).
// A nil resolver reproduces BuildSnapshotFromStore (sealed references fail closed).
func BuildSnapshotFromStoreWithSecrets(st *store.Store, obs transporter.Observer, secretResolve SecretResolver) (*chat.Snapshot, *keyring.Keyring, error) {
	permBytes, err := st.RenderPermeability()
	if err != nil {
		return nil, nil, fmt.Errorf("permeability: render: %w", err)
	}
	spec, err := permeability.Parse(permBytes)
	if err != nil {
		return nil, nil, fmt.Errorf("permeability: %w", err)
	}

	callerBytes, err := st.RenderCallers()
	if err != nil {
		return nil, nil, fmt.Errorf("callers: render: %w", err)
	}
	callerStore, err := admit.ParseStore(callerBytes)
	if err != nil {
		return nil, nil, fmt.Errorf("callers: %w", err)
	}

	credBytes, err := st.RenderCredentials()
	if err != nil {
		return nil, nil, fmt.Errorf("credentials: render: %w", err)
	}
	creds, err := config.ParseCredentialsOptional(credBytes)
	if err != nil {
		return nil, nil, fmt.Errorf("credentials: %w", err)
	}

	provBytes, err := st.RenderProviders()
	if err != nil {
		return nil, nil, fmt.Errorf("providers: render: %w", err)
	}
	provsSpec, err := config.ParseProviders(provBytes)
	if err != nil {
		return nil, nil, fmt.Errorf("providers: %w", err)
	}

	capsBytes, err := st.RenderPathways()
	if err != nil {
		return nil, nil, fmt.Errorf("pathways: render: %w", err)
	}
	capsSpec, err := config.ParsePathways(capsBytes)
	if err != nil {
		return nil, nil, fmt.Errorf("pathways: %w", err)
	}

	return assembleSnapshot(spec, callerStore, creds, provsSpec, capsSpec, obs, secretResolve)
}

// ValidateSpecs is a pure, cross-kind dry-run validator: it parses the four
// canonical documents with the daemon's own parsers and runs the full
// cross-kind assembly (keyring resolution, per-target adapter build), then
// discards the resulting snapshot. It returns the first secret-free error, or
// nil when the whole configuration would load. The declarative export/apply
// path injects this so a bundle is validated identically to the serving path —
// there is no second, weaker validator. No observer is wired (assembly only).
//
// This is the no-sealed-store variant: `secret:` credential references fail
// closed. Use ValidateSpecsWith to validate identically to a daemon that has the
// sealed store enabled.
func ValidateSpecs(permBytes, callerBytes, credBytes, provBytes, capsBytes []byte) error {
	return ValidateSpecsWith(nil, permBytes, callerBytes, credBytes, provBytes, capsBytes)
}

// ValidateSpecsWith is ValidateSpecs with a sealed-store resolver so `secret:`
// credential references are resolved exactly as the serving path would. A nil
// resolver reproduces ValidateSpecs.
func ValidateSpecsWith(secretResolve SecretResolver, permBytes, callerBytes, credBytes, provBytes, capsBytes []byte) error {
	spec, err := permeability.Parse(permBytes)
	if err != nil {
		return fmt.Errorf("permeability: %w", err)
	}
	callerStore, err := admit.ParseStore(callerBytes)
	if err != nil {
		return fmt.Errorf("callers: %w", err)
	}
	creds, err := config.ParseCredentialsOptional(credBytes)
	if err != nil {
		return fmt.Errorf("credentials: %w", err)
	}
	provsSpec, err := config.ParseProviders(provBytes)
	if err != nil {
		return fmt.Errorf("providers: %w", err)
	}
	capsSpec, err := config.ParsePathways(capsBytes)
	if err != nil {
		return fmt.Errorf("pathways: %w", err)
	}
	if _, _, err := assembleSnapshot(spec, callerStore, creds, provsSpec, capsSpec, nil, secretResolve); err != nil {
		return err
	}
	return nil
}
