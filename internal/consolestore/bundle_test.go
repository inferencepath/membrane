package consolestore_test

import (
	"strings"
	"testing"

	"github.com/inferencepath/membrane/internal/app"
	"github.com/inferencepath/membrane/internal/consolestore"
	"github.com/inferencepath/membrane/internal/store"
)

// newBundleAdapter returns an Adapter using the real cross-kind validator,
// app.ValidateSpecs.
func newBundleAdapter(t *testing.T, calls *int, outcome consolestore.ReloadOutcome) *consolestore.Adapter {
	t.Helper()
	// The validator resolves credentials, and the seed uses env:TEST_STORE_KEY.
	t.Setenv("TEST_STORE_KEY", "sk-test-resolves")
	return consolestore.New(newSeeded(t), testCfg(), recordReload(outcome, calls),
		consolestore.WithValidator(app.ValidateSpecs))
}

func TestExportBundleIsReferenceOnly(t *testing.T) {
	t.Setenv("TEST_STORE_KEY", "sk-should-never-appear")
	a := consolestore.New(newSeeded(t), testCfg(),
		recordReload(consolestore.ReloadOutcome{Applied: true}, new(int)),
		consolestore.WithValidator(app.ValidateSpecs))

	f, err := a.ExportBundle()
	if err != nil || f.Version == "" {
		t.Fatalf("export = %+v (err %v)", f, err)
	}
	// Only the reference and the hash may appear, not the secret value.
	if strings.Contains(f.Raw, "sk-should-never-appear") {
		t.Fatalf("exported bundle leaked a secret value:\n%s", f.Raw)
	}
	for _, want := range []string{"env:TEST_STORE_KEY", demoHash, "pathways", "permeability"} {
		if !strings.Contains(f.Raw, want) {
			t.Errorf("export missing %q", want)
		}
	}
}

func TestDiffBundleUnchangedThenChanged(t *testing.T) {
	a := newBundleAdapter(t, new(int), consolestore.ReloadOutcome{Applied: true})
	f, _ := a.ExportBundle()

	// The live config diffed against itself is valid and unchanged.
	diff, err := a.DiffBundle([]byte(f.Raw))
	if err != nil {
		t.Fatalf("diff: %v", err)
	}
	if !diff.Valid || diff.Changed {
		t.Fatalf("expected valid/no-change, got %+v", diff)
	}

	// Models belong to providers, so changing one marks the providers kind changed.
	changed := strings.ReplaceAll(f.Raw, "gpt-4o-mini", "gpt-4o")
	diff, err = a.DiffBundle([]byte(changed))
	if err != nil {
		t.Fatalf("diff: %v", err)
	}
	if !diff.Valid || !diff.Changed {
		t.Fatalf("expected changed, got %+v", diff)
	}
	var provChanged bool
	for _, k := range diff.Kinds {
		if k.Kind == store.KindProviders {
			provChanged = k.Changed
		}
	}
	if !provChanged {
		t.Fatalf("expected providers changed, got %+v", diff.Kinds)
	}
}

func TestDiffBundleInvalid(t *testing.T) {
	a := newBundleAdapter(t, new(int), consolestore.ReloadOutcome{Applied: true})
	// A dangling keyring ref parses per kind but fails cross-kind validation.
	f, _ := a.ExportBundle()
	bad := strings.Replace(f.Raw, "provider: main", "provider: ghost", 1)
	diff, err := a.DiffBundle([]byte(bad))
	if err != nil {
		t.Fatalf("diff: %v", err)
	}
	if diff.Valid {
		t.Fatalf("expected invalid for dangling provider ref, got %+v", diff)
	}
}

func TestApplyBundleWritesAllKindsAndReloads(t *testing.T) {
	calls := 0
	a := newBundleAdapter(t, &calls, consolestore.ReloadOutcome{Applied: true})
	f, _ := a.ExportBundle()

	changed := strings.ReplaceAll(f.Raw, "gpt-4o-mini", "gpt-4o")
	res := a.ApplyBundle([]byte(changed), f.Version)
	if !res.Applied || calls != 1 || res.Version == "" {
		t.Fatalf("apply bundle = %+v calls=%d", res, calls)
	}
	// Re-export and confirm the provider model was stored.
	f2, _ := a.ExportBundle()
	if !strings.Contains(f2.Raw, "default_model: gpt-4o\n") {
		t.Fatalf("expected persisted provider default_model gpt-4o, got:\n%s", f2.Raw)
	}
}

func TestApplyBundleUnchanged(t *testing.T) {
	calls := 0
	a := newBundleAdapter(t, &calls, consolestore.ReloadOutcome{Applied: true})
	f, _ := a.ExportBundle()
	res := a.ApplyBundle([]byte(f.Raw), f.Version)
	if !res.Unchanged || calls != 0 {
		t.Fatalf("expected unchanged no-reload, got %+v calls=%d", res, calls)
	}
}

func TestApplyBundleVersionConflict(t *testing.T) {
	a := newBundleAdapter(t, new(int), consolestore.ReloadOutcome{Applied: true})
	f, _ := a.ExportBundle()
	changed := strings.ReplaceAll(f.Raw, "gpt-4o-mini", "gpt-4o")
	res := a.ApplyBundle([]byte(changed), "stale-version")
	if !res.Rejected || !strings.Contains(res.Reason, "changed") {
		t.Fatalf("expected version conflict, got %+v", res)
	}
}

func TestApplyBundleInvalidRejected(t *testing.T) {
	a := newBundleAdapter(t, new(int), consolestore.ReloadOutcome{Applied: true})
	f, _ := a.ExportBundle()
	bad := strings.Replace(f.Raw, "provider: main", "provider: ghost", 1)
	res := a.ApplyBundle([]byte(bad), f.Version)
	if !res.Rejected {
		t.Fatalf("expected rejection for dangling provider ref, got %+v", res)
	}
}

func TestApplyBundleParseErrorRejected(t *testing.T) {
	a := newBundleAdapter(t, new(int), consolestore.ReloadOutcome{Applied: true})
	res := a.ApplyBundle([]byte("version: 1\nbogus: true\n"), "")
	if !res.Rejected {
		t.Fatalf("expected rejection for unknown field, got %+v", res)
	}
}

// TestApplyBundleNoValidatorSkipsCrossKind checks that without WithValidator
// no cross-kind check runs.
func TestApplyBundleNoValidatorSkipsCrossKind(t *testing.T) {
	a := consolestore.New(newSeeded(t), testCfg(), recordReload(consolestore.ReloadOutcome{Applied: true}, new(int)))
	f, _ := a.ExportBundle()
	// An unresolvable secret parses and satisfies the foreign keys; only the
	// cross-kind validator would reject it.
	bad := strings.Replace(f.Raw, "env:TEST_STORE_KEY", "env:DEFINITELY_MISSING", 1)
	res := a.ApplyBundle([]byte(bad), f.Version)
	if !res.Applied {
		t.Fatalf("expected apply to succeed without cross-kind validator, got %+v", res)
	}
}
