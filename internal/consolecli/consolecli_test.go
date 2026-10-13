package consolecli

import (
	"bytes"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/inferencepath/membrane/internal/app"
	"github.com/inferencepath/membrane/internal/config"
	"github.com/inferencepath/membrane/internal/consolestore"
	"github.com/inferencepath/membrane/internal/observability/metrics"
	"github.com/inferencepath/membrane/internal/server/console"
	"github.com/inferencepath/membrane/internal/store"
)

const demoHash = "458d6534a760ed4c8c2f1c8f86a517572cb56fa6a0f8554efdda2cb8532a4816"

// newServer starts an in-process Console backed by a seeded store with the real
// bundle adapter (and cross-kind validator), returning its base URL.
func newServer(t *testing.T, withBundle bool) string {
	t.Helper()
	t.Setenv("TEST_CLI_KEY", "egress-token")
	s, err := store.Open(filepath.Join(t.TempDir(), store.DBFileName))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	exec := func(q string, args ...any) {
		if _, err := s.DB().Exec(q, args...); err != nil {
			t.Fatalf("seed %q: %v", q, err)
		}
	}
	exec(`INSERT INTO pathways (name, cascade_allowed, default_provider) VALUES ('chat', 0, 'main')`)
	exec(`INSERT INTO providers (name, family, endpoint, models, default_model, secret_ref)
	      VALUES ('main', 'openai', 'https://api.openai.com', 'gpt-4o-mini', 'gpt-4o-mini', 'env:TEST_CLI_KEY')`)
	exec(`INSERT INTO pathway_providers (pathway, provider) VALUES ('chat', 'main')`)
	exec(`INSERT INTO callers (key_id, caller_id, name, secret_hash) VALUES ('ck_demo', 'acme', 'demo', ?)`, demoHash)
	exec(`INSERT INTO caller_allow (key_id, pathway) VALUES ('ck_demo', 'chat')`)
	exec(`INSERT INTO permeability (pathway, outbound_allow, inbound_default, inbound_on_reveal)
	      VALUES ('chat', 'messages', 'message` + "\n" + `finish_reason', 'provider')`)

	var cfg config.Config
	cfg.DataDir = "/data"
	adapter := consolestore.New(s, cfg, func() consolestore.ReloadOutcome {
		return consolestore.ReloadOutcome{Applied: true}
	}, consolestore.WithValidator(app.ValidateSpecs))

	ccfg := console.Config{Status: readyStatus{}, Metrics: metrics.NewRegistry()}
	if withBundle {
		ccfg.Bundle = adapter
	}
	srv := httptest.NewServer(console.New(ccfg).Handler())
	t.Cleanup(srv.Close)
	return srv.URL
}

type readyStatus struct{}

func (readyStatus) Ready() (bool, string) { return true, "" }

func run(t *testing.T, args []string) (code int, stdout, stderr string) {
	t.Helper()
	var out, errb bytes.Buffer
	code = Run(args, &out, &errb)
	return code, out.String(), errb.String()
}

func TestExportToStdout(t *testing.T) {
	url := newServer(t, true)
	code, out, errOut := run(t, []string{"export", "--console-url", url})
	if code != 0 {
		t.Fatalf("export = %d, stderr=%s", code, errOut)
	}
	for _, want := range []string{"pathways", "env:TEST_CLI_KEY", demoHash} {
		if !strings.Contains(out, want) {
			t.Fatalf("export output missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "egress-token") {
		t.Fatalf("export leaked a secret value:\n%s", out)
	}
	if !strings.Contains(errOut, "exported configuration") {
		t.Fatalf("expected status on stderr, got %s", errOut)
	}
}

func TestExportToFile(t *testing.T) {
	url := newServer(t, true)
	outPath := filepath.Join(t.TempDir(), "bundle.yaml")
	code, _, errOut := run(t, []string{"export", "-f", outPath, "--console-url", url})
	if code != 0 {
		t.Fatalf("export = %d stderr=%s", code, errOut)
	}
	b, err := os.ReadFile(outPath)
	if err != nil || !strings.Contains(string(b), "pathways") {
		t.Fatalf("export file = %q (err %v)", b, err)
	}
}

func TestDiffNoChangeThenChanged(t *testing.T) {
	url := newServer(t, true)
	bundle := exportBundle(t, url)

	// Identical bundle → no change, exit 0.
	path := writeTemp(t, bundle)
	code, out, _ := run(t, []string{"diff", "-f", path, "--console-url", url})
	if code != 0 || !strings.Contains(out, "no changes") {
		t.Fatalf("diff identical = %d out=%s", code, out)
	}

	// Changed model → pathways listed as changed.
	changed := strings.ReplaceAll(bundle, "gpt-4o-mini", "gpt-4o")
	path = writeTemp(t, changed)
	code, out, _ = run(t, []string{"diff", "-f", path, "--console-url", url})
	if code != 0 || !strings.Contains(out, "pathways") || !strings.Contains(out, "changes:") {
		t.Fatalf("diff changed = %d out=%s", code, out)
	}
}

func TestDiffInvalidExitsNonZero(t *testing.T) {
	url := newServer(t, true)
	bundle := exportBundle(t, url)
	bad := strings.Replace(bundle, "provider: main", "provider: ghost", 1)
	path := writeTemp(t, bad)
	code, out, _ := run(t, []string{"diff", "-f", path, "--console-url", url})
	if code != 1 || !strings.Contains(out, "invalid bundle") {
		t.Fatalf("diff invalid = %d out=%s", code, out)
	}
}

func TestApplyChangeFromStdin(t *testing.T) {
	url := newServer(t, true)
	bundle := exportBundle(t, url)
	changed := strings.ReplaceAll(bundle, "gpt-4o-mini", "gpt-4o")

	old := stdin
	stdin = strings.NewReader(changed)
	defer func() { stdin = old }()

	code, out, errOut := run(t, []string{"apply", "--console-url", url})
	if code != 0 || !strings.Contains(out, "applied and reloaded") {
		t.Fatalf("apply = %d out=%s err=%s", code, out, errOut)
	}
}

func TestApplyUnchanged(t *testing.T) {
	url := newServer(t, true)
	bundle := exportBundle(t, url)
	path := writeTemp(t, bundle)
	code, out, _ := run(t, []string{"apply", "-f", path, "--console-url", url})
	if code != 0 || !strings.Contains(out, "no changes to apply") {
		t.Fatalf("apply unchanged = %d out=%s", code, out)
	}
}

func TestApplyDryRunDoesNotWrite(t *testing.T) {
	url := newServer(t, true)
	bundle := exportBundle(t, url)
	changed := strings.ReplaceAll(bundle, "gpt-4o-mini", "gpt-4o")
	path := writeTemp(t, changed)

	code, out, errOut := run(t, []string{"apply", "--dry-run", "-f", path, "--console-url", url})
	if code != 0 || !strings.Contains(out, "changes:") || !strings.Contains(errOut, "dry-run") {
		t.Fatalf("dry-run = %d out=%s err=%s", code, out, errOut)
	}
	// The live store must be unchanged: a subsequent diff of the ORIGINAL bundle
	// still reports no change.
	orig := writeTemp(t, bundle)
	code, out, _ = run(t, []string{"diff", "-f", orig, "--console-url", url})
	if code != 0 || !strings.Contains(out, "no changes") {
		t.Fatalf("dry-run wrote to store: diff = %d out=%s", code, out)
	}
}

func TestApplyInvalidRejected(t *testing.T) {
	url := newServer(t, true)
	bundle := exportBundle(t, url)
	bad := strings.Replace(bundle, "provider: main", "provider: ghost", 1)
	path := writeTemp(t, bad)
	code, _, errOut := run(t, []string{"apply", "-f", path, "--console-url", url})
	if code != 1 || !strings.Contains(errOut, "rejected") {
		t.Fatalf("apply invalid = %d err=%s", code, errOut)
	}
}

func TestBundleUnavailableSurfaces503(t *testing.T) {
	url := newServer(t, false) // no bundle port
	code, _, errOut := run(t, []string{"export", "--console-url", url})
	if code != 1 || !strings.Contains(errOut, "not available") {
		t.Fatalf("export 503 = %d err=%s", code, errOut)
	}
}

func TestUnreachableServer(t *testing.T) {
	code, _, errOut := run(t, []string{"export", "--console-url", "http://127.0.0.1:1"})
	if code != 1 || errOut == "" {
		t.Fatalf("unreachable = %d err=%s", code, errOut)
	}
}

func TestTokenRefUnresolvableFails(t *testing.T) {
	url := newServer(t, true)
	code, _, errOut := run(t, []string{"export", "--console-url", url, "--token-ref", "env:DEFINITELY_UNSET_XYZ"})
	if code != 1 || !strings.Contains(errOut, "unset") {
		t.Fatalf("bad token ref = %d err=%s", code, errOut)
	}
}

func TestTokenRefResolvesAndAuthorizes(t *testing.T) {
	t.Setenv("CLI_ADMIN_TOKEN", "s3cr3t")
	url := newServer(t, true)
	// Loopback is always authorized, but this exercises token resolution + header.
	code, _, errOut := run(t, []string{"export", "--console-url", url, "--token-ref", "env:CLI_ADMIN_TOKEN"})
	if code != 0 {
		t.Fatalf("token-ref export = %d err=%s", code, errOut)
	}
}

func TestUsageAndDispatch(t *testing.T) {
	if code, _, _ := run(t, []string{}); code != 2 {
		t.Fatalf("no args = %d, want 2", code)
	}
	if code, out, _ := run(t, []string{"help"}); code != 0 || !strings.Contains(out, "usage") {
		t.Fatalf("help = %d out=%s", code, out)
	}
	if code, _, errOut := run(t, []string{"bogus"}); code != 2 || !strings.Contains(errOut, "unknown subcommand") {
		t.Fatalf("bogus = %d err=%s", code, errOut)
	}
	if code := Run([]string{"export", "--bad-flag"}, &bytes.Buffer{}, &bytes.Buffer{}); code != 2 {
		t.Fatalf("bad flag = %d, want 2", code)
	}
}

func TestReadInputMissingFile(t *testing.T) {
	url := newServer(t, true)
	code, _, errOut := run(t, []string{"apply", "-f", "/no/such/file.yaml", "--console-url", url})
	if code != 1 || !strings.Contains(errOut, "read bundle") {
		t.Fatalf("missing file = %d err=%s", code, errOut)
	}
}

// --- helpers ---

func exportBundle(t *testing.T, url string) string {
	t.Helper()
	raw, _, err := NewClient(url, "").Export()
	if err != nil {
		t.Fatalf("export helper: %v", err)
	}
	return string(raw)
}

func writeTemp(t *testing.T, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "bundle.yaml")
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}
