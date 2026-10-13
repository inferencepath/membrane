package console_test

import (
	"net/http"
	"strings"
	"testing"

	"github.com/inferencepath/membrane/internal/observability/metrics"
	"github.com/inferencepath/membrane/internal/server/console"
)

// stubBundle is a controllable ConfigBundle for the export/diff/apply handlers.
type stubBundle struct {
	export    console.BundleFile
	exportErr error
	diff      console.BundleDiff
	diffErr   error
	apply     console.ApplyResult
}

func (s stubBundle) ExportBundle() (console.BundleFile, error) {
	return s.export, s.exportErr
}

func (s stubBundle) DiffBundle(raw []byte) (console.BundleDiff, error) {
	return s.diff, s.diffErr
}

func (s stubBundle) ApplyBundle(raw []byte, version string) console.ApplyResult {
	return s.apply
}

func newConsoleWithBundle(b console.ConfigBundle) http.Handler {
	return console.New(console.Config{
		Status:  stubStatus{ready: true},
		Metrics: metrics.NewRegistry(),
		Config:  stubConfig{},
		Bundle:  b,
		Version: console.Version{Version: "9.9.9"},
	}).Handler()
}

func TestBundleExport(t *testing.T) {
	h := newConsoleWithBundle(stubBundle{export: console.BundleFile{Raw: "version: 1\npathways: []\n", Version: "abc123"}})
	rec := get(t, h, "/api/console/config/export", "127.0.0.1:1", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("export = %d", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/yaml") {
		t.Fatalf("export content-type = %q", ct)
	}
	if cd := rec.Header().Get("Content-Disposition"); !strings.Contains(cd, "membrane-config.yaml") {
		t.Fatalf("export missing download disposition: %q", cd)
	}
	if rec.Header().Get("X-Config-Version") != "abc123" {
		t.Fatalf("export version header = %q", rec.Header().Get("X-Config-Version"))
	}
	if !strings.Contains(rec.Body.String(), "pathways") {
		t.Fatalf("export body = %s", rec.Body.String())
	}
}

func TestBundleExportErrorSurfaced(t *testing.T) {
	h := newConsoleWithBundle(stubBundle{exportErr: errStub})
	rec := get(t, h, "/api/console/config/export", "127.0.0.1:1", nil)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("export error = %d, want 500", rec.Code)
	}
}

func TestBundleDiff(t *testing.T) {
	h := newConsoleWithBundle(stubBundle{diff: console.BundleDiff{
		Valid:   true,
		Changed: true,
		Kinds:   []console.BundleKindDiff{{Kind: "pathways", Changed: true}, {Kind: "callers", Changed: false}},
	}})
	rec := post(t, h, "/api/console/config/diff", "version: 1\n", "127.0.0.1:1")
	if rec.Code != http.StatusOK {
		t.Fatalf("diff = %d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, `"changed":true`) || !strings.Contains(body, `"kind":"pathways"`) {
		t.Fatalf("diff body = %s", body)
	}
}

func TestBundleDiffErrorSurfaced(t *testing.T) {
	h := newConsoleWithBundle(stubBundle{diffErr: errStub})
	rec := post(t, h, "/api/console/config/diff", "x", "127.0.0.1:1")
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("diff error = %d, want 500", rec.Code)
	}
}

func TestBundleApply(t *testing.T) {
	h := newConsoleWithBundle(stubBundle{apply: console.ApplyResult{Applied: true, Version: "v2"}})
	rec := post(t, h, "/api/console/config/apply", `{"raw":"version: 1\n","version":"v1"}`, "127.0.0.1:1")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"applied":true`) {
		t.Fatalf("apply = %d body=%s", rec.Code, rec.Body.String())
	}
	// The bundle apply must be recorded in the admin-action audit.
	admin := get(t, h, "/api/console/audit/admin", "127.0.0.1:1", nil)
	if !strings.Contains(admin.Body.String(), `"action":"apply:bundle"`) {
		t.Fatalf("bundle apply not audited: %s", admin.Body.String())
	}
}

func TestBundleApplyMalformedBody(t *testing.T) {
	h := newConsoleWithBundle(stubBundle{})
	rec := post(t, h, "/api/console/config/apply", `{not json`, "127.0.0.1:1")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("malformed apply = %d, want 400", rec.Code)
	}
}

func TestBundleUnavailableWhenNil(t *testing.T) {
	h := newConsoleWithConfig(stubConfig{}) // no Bundle
	for _, tc := range []struct {
		method, path, body string
	}{
		{http.MethodGet, "/api/console/config/export", ""},
		{http.MethodPost, "/api/console/config/diff", "x"},
		{http.MethodPost, "/api/console/config/apply", `{"raw":"","version":""}`},
	} {
		var rec = post(t, h, tc.path, tc.body, "127.0.0.1:1")
		if tc.method == http.MethodGet {
			rec = get(t, h, tc.path, "127.0.0.1:1", nil)
		}
		if rec.Code != http.StatusServiceUnavailable {
			t.Fatalf("%s %s without bundle = %d, want 503", tc.method, tc.path, rec.Code)
		}
	}
}
