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

// BuildSnapshotFromStore assembles the serving Snapshot from the state store.
// The stored rows are rendered to the canonical config documents and run
// through the regular parsers, so validation matches every other path. Secrets
// stay as references, and secret: references fail closed; use
// BuildSnapshotFromStoreWithSecrets to resolve them.
func BuildSnapshotFromStore(st *store.Store, obs transporter.Observer) (*chat.Snapshot, *keyring.Keyring, error) {
	return BuildSnapshotFromStoreWithSecrets(st, obs, nil)
}

// BuildSnapshotFromStoreWithSecrets is BuildSnapshotFromStore with a resolver
// for secret:<name> references. A nil resolver behaves like
// BuildSnapshotFromStore.
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

// ValidateSpecs is a dry run of a full load: it parses the canonical documents,
// assembles a Snapshot and discards it. It returns the first secret-free error,
// or nil if the configuration would load. Declarative apply uses it so a bundle
// is validated the same way as the serving config. secret: references fail
// closed; use ValidateSpecsWith to resolve them.
func ValidateSpecs(permBytes, callerBytes, credBytes, provBytes, capsBytes []byte) error {
	return ValidateSpecsWith(nil, permBytes, callerBytes, credBytes, provBytes, capsBytes)
}

// ValidateSpecsWith is ValidateSpecs with a resolver for secret: references, as
// used when serving. A nil resolver behaves like ValidateSpecs.
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
