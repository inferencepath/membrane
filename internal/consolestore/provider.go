package consolestore

import (
	"fmt"
	"sort"
	"strings"

	"github.com/inferencepath/membrane/internal/config"
	"github.com/inferencepath/membrane/internal/secrets"
	"github.com/inferencepath/membrane/internal/server/console"
	"github.com/inferencepath/membrane/internal/store"
)

// ProviderAdapter implements console.ProviderManager: the consolidated provider
// surface that pairs a sealed secret value with the provider definition (family +
// connection facts + the `secret:<name>` reference that routing resolves). It is
// a thin app-layer bridge over secrets.Manager (seal) and the store (atomic
// persist). Values are write-only — no method returns a plaintext value.
type ProviderAdapter struct {
	st  *store.Store
	mgr *secrets.Manager
}

var _ console.ProviderManager = (*ProviderAdapter)(nil)

// NewProviderAdapter returns a ProviderAdapter over the store and sealed-secret
// Manager. It is wired into the Console only when the sealed store is enabled (a
// master key is configured); otherwise the Console's Providers port stays nil and
// the endpoints report 503.
func NewProviderAdapter(st *store.Store, mgr *secrets.Manager) *ProviderAdapter {
	return &ProviderAdapter{st: st, mgr: mgr}
}

// List returns the configured providers, each annotated with whether its sealed
// key is present and which pathways use it. Never a sealed value.
func (a *ProviderAdapter) List() (console.ProvidersView, error) {
	provs, err := a.st.ReadProviders()
	if err != nil {
		return console.ProvidersView{}, err
	}
	metas, err := a.mgr.List()
	if err != nil {
		return console.ProvidersView{}, err
	}
	sealed := make(map[string]bool, len(metas))
	for _, m := range metas {
		sealed[m.Name] = true
	}
	usedBy, err := a.usedByPathways()
	if err != nil {
		return console.ProvidersView{}, err
	}
	out := console.ProvidersView{Providers: make([]console.ProviderView, 0, len(provs))}
	for _, p := range provs {
		name, _ := secretRefName(p.SecretRef)
		authMode := p.AuthMode
		if authMode == "" {
			authMode = config.AuthAPIKey
		}
		// Ambient-auth providers hold no sealed key; report them as satisfied so
		// the Console does not flag a "missing key".
		isSealed := sealed[name]
		if !config.AuthModeUsesSecret(authMode) {
			isSealed = true
		}
		out.Providers = append(out.Providers, console.ProviderView{
			Name:         p.Name,
			Family:       p.Family,
			AuthMode:     authMode,
			RoleARN:      p.RoleARN,
			ExternalID:   p.ExternalID,
			Models:       p.Models,
			DefaultModel: p.DefaultModel,
			Endpoint:     p.Endpoint,
			Deployment:   p.Deployment,
			Region:       p.Region,
			Project:      p.Project,
			Location:     p.Location,
			Sealed:       isSealed,
			SecretRef:    p.SecretRef,
			UsedBy:       usedBy[p.Name],
		})
	}
	return out, nil
}

// Set seals the provider's credential value and upserts its definition (with a
// `secret:<name>` reference) in one store transaction, so routing never sees a
// half-written provider. The candidate is validated with the daemon's own
// providers parser before any write.
func (a *ProviderAdapter) Set(in console.ProviderInput) error {
	authMode := in.AuthMode
	if authMode == "" {
		authMode = config.AuthAPIKey
	}
	row := store.ProviderRow{
		Name:         in.Name,
		Family:       in.Family,
		AuthMode:     authMode,
		RoleARN:      in.RoleARN,
		ExternalID:   in.ExternalID,
		Endpoint:     in.Endpoint,
		Deployment:   in.Deployment,
		Region:       in.Region,
		Project:      in.Project,
		Location:     in.Location,
		Models:       in.Models,
		DefaultModel: in.DefaultModel,
	}
	// Secret-backed auth modes (api_key, bedrock bearer token) reference a sealed
	// value; ambient IAM/managed-identity modes carry no stored secret.
	if config.AuthModeUsesSecret(authMode) {
		row.SecretRef = secrets.RefScheme + ":" + in.Name
	}
	if err := validateProvider(row); err != nil {
		return err
	}
	if !config.AuthModeUsesSecret(authMode) {
		return a.st.UpsertProvider(store.ProviderUpsert{Provider: row})
	}
	sealed, err := a.mgr.SealRow(in.Name, in.Value)
	if err != nil {
		return err
	}
	return a.st.UpsertProvider(store.ProviderUpsert{Secret: sealed, Provider: row})
}

// validateProvider runs the candidate provider row through the daemon's own
// providers parser (via a canonical render) so an invalid definition is rejected
// before any write.
func validateProvider(row store.ProviderRow) error {
	raw, err := store.RenderProvidersDoc([]store.ProviderRow{row})
	if err != nil {
		return err
	}
	_, err = config.ParseProviders(raw)
	return err
}

// Delete removes a provider (definition + sealed secret), refusing while a
// pathway target still references it so routing never dangles.
func (a *ProviderAdapter) Delete(name string) (bool, error) {
	usedBy, err := a.usedByPathways()
	if err != nil {
		return false, err
	}
	if users := usedBy[name]; len(users) > 0 {
		return false, fmt.Errorf("provider %q is used by pathway(s) %s; detach it first", name, strings.Join(users, ", "))
	}
	return a.st.DeleteProvider(name)
}

// usedByPathways maps a provider name to the sorted, de-duplicated list of
// pathways whose targets reference it.
func (a *ProviderAdapter) usedByPathways() (map[string][]string, error) {
	paths, err := a.st.ReadPathways()
	if err != nil {
		return nil, err
	}
	seen := make(map[string]map[string]bool)
	for _, p := range paths {
		for _, prov := range p.Providers {
			if prov == "" {
				continue
			}
			if seen[prov] == nil {
				seen[prov] = make(map[string]bool)
			}
			seen[prov][p.Name] = true
		}
	}
	out := make(map[string][]string, len(seen))
	for ref, names := range seen {
		list := make([]string, 0, len(names))
		for n := range names {
			list = append(list, n)
		}
		sort.Strings(list)
		out[ref] = list
	}
	return out, nil
}
