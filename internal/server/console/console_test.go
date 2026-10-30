package console_test

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/inferencepath/membrane/internal/observability/metrics"
	"github.com/inferencepath/membrane/internal/server/console"
)

var errStub = errors.New("boom")

// stubStatus is a controllable StatusProvider.
type stubStatus struct {
	ready  bool
	reason string
}

func (s stubStatus) Ready() (bool, string) { return s.ready, s.reason }

func newConsole(status console.StatusProvider, reg *metrics.Registry, token string) http.Handler {
	return console.New(console.Config{
		Status:    status,
		Metrics:   reg,
		Version:   console.Version{Version: "9.9.9", Commit: "deadbeef", BuildTime: "2026-10-08T00:00:00Z"},
		AuthToken: token,
	}).Handler()
}

func get(t *testing.T, h http.Handler, path, remoteAddr string, header map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.RemoteAddr = remoteAddr
	for k, v := range header {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestStatusReflectsReadiness(t *testing.T) {
	h := newConsole(stubStatus{ready: true}, metrics.NewRegistry(), "")
	rec := get(t, h, "/api/console/status", "127.0.0.1:5000", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status code = %d", rec.Code)
	}
	var body struct {
		Live          bool   `json:"live"`
		Ready         bool   `json:"ready"`
		EgressEnabled bool   `json:"egressEnabled"`
		Reason        string `json:"reason"`
		Version       struct {
			Version string `json:"version"`
		} `json:"version"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if !body.Live || !body.Ready || !body.EgressEnabled {
		t.Fatalf("expected live+ready+egress, got %+v", body)
	}
	if body.Version.Version != "9.9.9" {
		t.Fatalf("version = %q", body.Version.Version)
	}
}

func TestStatusNotReadySurfacesReason(t *testing.T) {
	h := newConsole(stubStatus{ready: false, reason: "invalid or missing permeability spec"}, metrics.NewRegistry(), "")
	rec := get(t, h, "/api/console/status", "127.0.0.1:5000", nil)
	var body struct {
		Ready         bool   `json:"ready"`
		EgressEnabled bool   `json:"egressEnabled"`
		Reason        string `json:"reason"`
	}
	_ = json.NewDecoder(rec.Body).Decode(&body)
	if body.Ready || body.EgressEnabled {
		t.Fatalf("expected not ready, got %+v", body)
	}
	if body.Reason != "invalid or missing permeability spec" {
		t.Fatalf("reason = %q", body.Reason)
	}
}

func TestHealthAndMetricsReflectRegistry(t *testing.T) {
	reg := metrics.NewRegistry()
	reg.RecordChat("chat", false, metrics.OutcomeSuccess, 20*time.Millisecond, 3, 4, 7)
	reg.ObserveHealth("chat", "openai", true)
	reg.ObserveFailover("chat", "openai")
	reg.ObserveAuthorization("chat", false)

	h := newConsole(stubStatus{ready: true}, reg, "")

	hrec := get(t, h, "/api/console/health", "127.0.0.1:1", nil)
	if hrec.Code != http.StatusOK {
		t.Fatalf("health code = %d", hrec.Code)
	}
	if !strings.Contains(hrec.Body.String(), `"provider":"openai"`) {
		t.Fatalf("health missing target: %s", hrec.Body.String())
	}

	mrec := get(t, h, "/api/console/metrics", "127.0.0.1:1", nil)
	body := mrec.Body.String()
	for _, want := range []string{`"pathway":"chat"`, `"decision":"deny"`, `"total":7`} {
		if !strings.Contains(body, want) {
			t.Fatalf("metrics missing %q: %s", want, body)
		}
	}
}

func TestLoopbackAllowedWithoutToken(t *testing.T) {
	h := newConsole(stubStatus{ready: true}, metrics.NewRegistry(), "")
	for _, addr := range []string{"127.0.0.1:40000", "[::1]:40000"} {
		rec := get(t, h, "/api/console/status", addr, nil)
		if rec.Code != http.StatusOK {
			t.Fatalf("loopback %s denied: %d", addr, rec.Code)
		}
	}
}

func TestRemoteDeniedWithoutToken(t *testing.T) {
	h := newConsole(stubStatus{ready: true}, metrics.NewRegistry(), "")
	rec := get(t, h, "/api/console/status", "10.0.0.5:40000", nil)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("remote without token = %d, want 401", rec.Code)
	}
}

func TestRemoteAllowedWithToken(t *testing.T) {
	h := newConsole(stubStatus{ready: true}, metrics.NewRegistry(), "s3cr3t-admin")

	// Wrong token is denied.
	bad := get(t, h, "/api/console/status", "10.0.0.5:1", map[string]string{"Authorization": "Bearer nope"})
	if bad.Code != http.StatusUnauthorized {
		t.Fatalf("bad token = %d, want 401", bad.Code)
	}

	// Correct token via Authorization: Bearer.
	okBearer := get(t, h, "/api/console/status", "10.0.0.5:1", map[string]string{"Authorization": "Bearer s3cr3t-admin"})
	if okBearer.Code != http.StatusOK {
		t.Fatalf("bearer token = %d, want 200", okBearer.Code)
	}

	// Correct token via X-Console-Token.
	okHeader := get(t, h, "/api/console/status", "10.0.0.5:1", map[string]string{"X-Console-Token": "s3cr3t-admin"})
	if okHeader.Code != http.StatusOK {
		t.Fatalf("header token = %d, want 200", okHeader.Code)
	}
}

func TestServesEmbeddedUI(t *testing.T) {
	h := newConsole(stubStatus{ready: true}, metrics.NewRegistry(), "")
	rec := get(t, h, "/", "127.0.0.1:1", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("index code = %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "Membrane Console") {
		t.Fatalf("index did not render the console shell")
	}
	// Unknown client-routed path falls back to the SPA shell.
	spa := get(t, h, "/some/client/route", "127.0.0.1:1", nil)
	if spa.Code != http.StatusOK || !strings.Contains(spa.Body.String(), "Membrane Console") {
		t.Fatalf("SPA fallback failed: %d", spa.Code)
	}
}

func TestLoginOverlayCanBeHidden(t *testing.T) {
	// .login-overlay sets display:flex, which overrides the UA [hidden] rule;
	// without an explicit [hidden] rule the login screen cannot be dismissed.
	h := newConsole(stubStatus{ready: true}, metrics.NewRegistry(), "")
	shell := get(t, h, "/", "127.0.0.1:1", nil)
	if !strings.Contains(shell.Body.String(), ".login-overlay[hidden]") {
		t.Fatal("login overlay missing the [hidden] display:none guard; the login screen could not be dismissed")
	}
}

func TestStaticShellIsPublicButAPIIsProtected(t *testing.T) {
	// With a token configured, a non-local client can load the UI shell but not
	// the control API until it presents the token.
	h := newConsole(stubStatus{ready: true}, metrics.NewRegistry(), "s3cr3t-admin")

	shell := get(t, h, "/", "10.0.0.5:5000", nil)
	if shell.Code != http.StatusOK || !strings.Contains(shell.Body.String(), "Membrane Console") {
		t.Fatalf("remote shell load = %d (want 200 + UI); body: %s", shell.Code, shell.Body.String())
	}

	denied := get(t, h, "/api/console/status", "10.0.0.5:5000", nil)
	if denied.Code != http.StatusUnauthorized {
		t.Fatalf("remote API without token = %d, want 401", denied.Code)
	}

	ok := get(t, h, "/api/console/status", "10.0.0.5:5000", map[string]string{"X-Console-Token": "s3cr3t-admin"})
	if ok.Code != http.StatusOK {
		t.Fatalf("remote API with token = %d, want 200", ok.Code)
	}
}

func TestConsoleDoesNotServeChat(t *testing.T) {
	h := newConsole(stubStatus{ready: true}, metrics.NewRegistry(), "")
	// /v1/chat is not a console route, so it falls back to the SPA shell.
	rec := get(t, h, "/v1/chat", "127.0.0.1:1", nil)
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Fatalf("/v1/chat content-type = %q, want the static SPA (no chat handler)", ct)
	}
}

func TestResolveTokenRef(t *testing.T) {
	t.Setenv("MEMBRANE_CONSOLE_TOKEN_TEST", "abc123")
	v, err := console.ResolveTokenRef("env:MEMBRANE_CONSOLE_TOKEN_TEST")
	if err != nil || v != "abc123" {
		t.Fatalf("env ref = %q, %v", v, err)
	}

	if _, err := console.ResolveTokenRef("env:DEFINITELY_UNSET_VAR_XYZ"); err == nil {
		t.Fatalf("expected error for unset env ref")
	}
	if _, err := console.ResolveTokenRef("literal-token"); err == nil {
		t.Fatalf("expected error for inline (schemeless) token")
	}
	if _, err := console.ResolveTokenRef("vault:secret/x"); err == nil {
		t.Fatalf("expected error for unsupported scheme in console resolver")
	}

	f, err := os.CreateTemp(t.TempDir(), "tok")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = f.WriteString("  file-token\n")
	_ = f.Close()
	v, err = console.ResolveTokenRef("file:" + f.Name())
	if err != nil || v != "file-token" {
		t.Fatalf("file ref = %q, %v", v, err)
	}
}

// stubConfig is a controllable ConfigSource.
type stubConfig struct {
	err error
}

func (s stubConfig) Server() console.ServerView {
	return console.ServerView{Address: ":8080", ShutdownTimeout: "30s", AuditSink: "discard", ConsoleEnabled: true, ConsoleAddress: "127.0.0.1:8300"}
}

func (s stubConfig) Pathways() (console.PathwaysView, error) {
	if s.err != nil {
		return console.PathwaysView{}, s.err
	}
	return console.PathwaysView{Source: "pathways.yaml", Pathways: []console.PathwayView{
		{Pathway: "chat", CascadeAllowed: true, DefaultProvider: "openai-main", Providers: []string{"openai-main"}},
	}}, nil
}

func (s stubConfig) Credentials() (console.CredentialsView, error) {
	if s.err != nil {
		return console.CredentialsView{}, s.err
	}
	return console.CredentialsView{Credentials: []console.CredentialView{
		{Name: "openai-main", Scheme: "env", Reference: "env:OPENAI_API_KEY", Resolves: true},
	}}, nil
}

func (s stubConfig) Callers() (console.CallersView, error) {
	if s.err != nil {
		return console.CallersView{}, s.err
	}
	return console.CallersView{Callers: []console.CallerView{
		{KeyID: "ck_demo", CallerID: "demo", Name: "Demo", Allow: []string{"chat"}},
	}}, nil
}

func (s stubConfig) Permeability() (console.PermeabilityView, error) {
	if s.err != nil {
		return console.PermeabilityView{}, s.err
	}
	return console.PermeabilityView{Version: 1, Pathways: []console.DirectionView{
		{Pathway: "chat", OutboundAllow: []string{"messages"}, InboundDefault: []string{"message"}},
	}}, nil
}

func newConsoleWithConfig(cfg console.ConfigSource) http.Handler {
	return console.New(console.Config{
		Status:  stubStatus{ready: true},
		Metrics: metrics.NewRegistry(),
		Config:  cfg,
		Version: console.Version{Version: "9.9.9"},
	}).Handler()
}

func TestConfigEndpointsReturnViews(t *testing.T) {
	h := newConsoleWithConfig(stubConfig{})
	cases := map[string]string{
		"/api/console/config/server":       `":8080"`,
		"/api/console/config/pathways":     `"pathways.yaml"`,
		"/api/console/config/callers":      `"ck_demo"`,
		"/api/console/config/permeability": `"messages"`,
	}
	for path, want := range cases {
		rec := get(t, h, path, "127.0.0.1:1", nil)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s code = %d", path, rec.Code)
		}
		if !strings.Contains(rec.Body.String(), want) {
			t.Fatalf("%s missing %q: %s", path, want, rec.Body.String())
		}
	}
}

func TestCredentialsConfigEndpointHidden(t *testing.T) {
	h := newConsoleWithConfig(stubConfig{})
	rec := get(t, h, "/api/console/config/credentials", "127.0.0.1:1", nil)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("config/credentials = %d, want 404", rec.Code)
	}
}

func TestConfigUnavailableWhenNil(t *testing.T) {
	h := newConsole(stubStatus{ready: true}, metrics.NewRegistry(), "") // no Config
	rec := get(t, h, "/api/console/config/pathways", "127.0.0.1:1", nil)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("config without source = %d, want 503", rec.Code)
	}
}

func TestConfigErrorSurfaced(t *testing.T) {
	h := newConsoleWithConfig(stubConfig{err: errStub})
	for _, path := range []string{
		"/api/console/config/pathways",
		"/api/console/config/callers",
		"/api/console/config/permeability",
	} {
		rec := get(t, h, path, "127.0.0.1:1", nil)
		if rec.Code != http.StatusInternalServerError {
			t.Fatalf("%s error = %d, want 500", path, rec.Code)
		}
		if !strings.Contains(rec.Body.String(), "boom") {
			t.Fatalf("%s expected error surfaced, got %s", path, rec.Body.String())
		}
	}
}

func TestEditCredentialsEndpointHidden(t *testing.T) {
	h := newConsoleWithEditor(stubEditor{})
	for _, path := range []string{
		"/api/console/edit/credentials",
		"/api/console/edit/credentials/validate",
		"/api/console/edit/credentials/apply",
	} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		if strings.HasSuffix(path, "/validate") || strings.HasSuffix(path, "/apply") {
			req = httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{}`))
		}
		req.RemoteAddr = "127.0.0.1:1"
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusNotFound {
			t.Fatalf("%s = %d, want 404", path, rec.Code)
		}
	}
}

func TestConfigMethodNotAllowed(t *testing.T) {
	h := newConsoleWithConfig(stubConfig{})
	req := httptest.NewRequest(http.MethodPost, "/api/console/config/server", nil)
	req.RemoteAddr = "127.0.0.1:1"
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST config/server = %d, want 405", rec.Code)
	}
}

func TestMethodNotAllowed(t *testing.T) {
	h := newConsole(stubStatus{ready: true}, metrics.NewRegistry(), "")
	for _, path := range []string{"/api/console/status", "/api/console/health", "/api/console/metrics"} {
		req := httptest.NewRequest(http.MethodPost, path, nil)
		req.RemoteAddr = "127.0.0.1:1"
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusMethodNotAllowed {
			t.Fatalf("POST %s = %d, want 405", path, rec.Code)
		}
	}
}

func TestLoopbackRemoteWithoutPort(t *testing.T) {
	// A bare loopback RemoteAddr without a port is still allowed.
	h := newConsole(stubStatus{ready: true}, metrics.NewRegistry(), "")
	rec := get(t, h, "/api/console/status", "127.0.0.1", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("bare loopback addr = %d, want 200", rec.Code)
	}
}

// stubEditor is a controllable ConfigEditor for the edit-pipeline handlers.
type stubEditor struct {
	readErr  error
	validate console.ValidationResult
	apply    console.ApplyResult
}

func (s stubEditor) Kinds() []string { return []string{"permeability", "callers"} }

func (s stubEditor) Read(kind string) (console.EditFile, error) {
	if s.readErr != nil {
		return console.EditFile{}, s.readErr
	}
	return console.EditFile{Kind: kind, Raw: "version: 1\n", Version: "v1"}, nil
}

func (s stubEditor) Validate(kind string, raw []byte) console.ValidationResult { return s.validate }

func (s stubEditor) Apply(kind string, raw []byte, version string) console.ApplyResult {
	return s.apply
}

func (s stubEditor) ValidateForm(kind string, form []byte) console.ValidationResult {
	return s.validate
}

func (s stubEditor) ApplyForm(kind string, form []byte, version string) console.ApplyResult {
	return s.apply
}

func newConsoleWithEditor(ed console.ConfigEditor) http.Handler {
	return console.New(console.Config{
		Status:  stubStatus{ready: true},
		Metrics: metrics.NewRegistry(),
		Config:  stubConfig{},
		Editor:  ed,
		Version: console.Version{Version: "9.9.9"},
	}).Handler()
}

func post(t *testing.T, h http.Handler, path, body, remoteAddr string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	req.RemoteAddr = remoteAddr
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestEditReadReturnsRawAndVersion(t *testing.T) {
	h := newConsoleWithEditor(stubEditor{})
	rec := get(t, h, "/api/console/edit/permeability", "127.0.0.1:1", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("edit read = %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `"version":"v1"`) {
		t.Fatalf("edit read missing version: %s", rec.Body.String())
	}
}

func TestEditReadErrorSurfaced(t *testing.T) {
	h := newConsoleWithEditor(stubEditor{readErr: errStub})
	rec := get(t, h, "/api/console/edit/permeability", "127.0.0.1:1", nil)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("edit read error = %d, want 500", rec.Code)
	}
}

func TestEditUnavailableWhenEditorNil(t *testing.T) {
	h := newConsoleWithConfig(stubConfig{}) // no Editor
	rec := get(t, h, "/api/console/edit/permeability", "127.0.0.1:1", nil)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("edit without editor = %d, want 503", rec.Code)
	}
}

func TestEditUnknownKind(t *testing.T) {
	h := newConsoleWithEditor(stubEditor{})
	rec := get(t, h, "/api/console/edit/bogus", "127.0.0.1:1", nil)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("unknown kind = %d, want 404", rec.Code)
	}
}

func TestEditValidate(t *testing.T) {
	h := newConsoleWithEditor(stubEditor{validate: console.ValidationResult{Valid: true}})
	rec := post(t, h, "/api/console/edit/permeability/validate", "version: 1\n", "127.0.0.1:1")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"valid":true`) {
		t.Fatalf("validate = %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestEditApply(t *testing.T) {
	h := newConsoleWithEditor(stubEditor{apply: console.ApplyResult{Applied: true, Version: "v2"}})
	rec := post(t, h, "/api/console/edit/permeability/apply", `{"raw":"version: 2\n","version":"v1"}`, "127.0.0.1:1")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"applied":true`) {
		t.Fatalf("apply = %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestEditApplyMalformedBody(t *testing.T) {
	h := newConsoleWithEditor(stubEditor{})
	rec := post(t, h, "/api/console/edit/permeability/apply", `{not json`, "127.0.0.1:1")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("malformed apply = %d, want 400", rec.Code)
	}
}

func TestEditFormValidate(t *testing.T) {
	h := newConsoleWithEditor(stubEditor{validate: console.ValidationResult{Valid: true}})
	rec := post(t, h, "/api/console/edit/callers/form/validate", `{"callers":[]}`, "127.0.0.1:1")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"valid":true`) {
		t.Fatalf("form validate = %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestEditFormApply(t *testing.T) {
	h := newConsoleWithEditor(stubEditor{apply: console.ApplyResult{Applied: true, Version: "v2"}})
	rec := post(t, h, "/api/console/edit/callers/form/apply", `{"form":{"callers":[]},"version":"v1"}`, "127.0.0.1:1")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"applied":true`) {
		t.Fatalf("form apply = %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestEditFormApplyMalformedBody(t *testing.T) {
	h := newConsoleWithEditor(stubEditor{})
	rec := post(t, h, "/api/console/edit/callers/form/apply", `{not json`, "127.0.0.1:1")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("malformed form apply = %d, want 400", rec.Code)
	}
}

func TestEditFormUnavailableWhenEditorNil(t *testing.T) {
	h := newConsoleWithConfig(stubConfig{}) // no Editor
	rec := post(t, h, "/api/console/edit/callers/form/apply", `{"form":{},"version":""}`, "127.0.0.1:1")
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("form apply without editor = %d, want 503", rec.Code)
	}
}

// stubAudit is a controllable AuditSource.
type stubAudit struct {
	records []console.UsageRecord
	err     error
}

func (s stubAudit) Recent(limit int) ([]console.UsageRecord, error) {
	return s.records, s.err
}

func TestAuditUsageUnavailableWhenNil(t *testing.T) {
	h := newConsole(stubStatus{ready: true}, metrics.NewRegistry(), "") // no Audit
	rec := get(t, h, "/api/console/audit/usage", "127.0.0.1:1", nil)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("usage audit without source = %d, want 503", rec.Code)
	}
}

func TestAuditUsageReturnsRecords(t *testing.T) {
	src := stubAudit{records: []console.UsageRecord{
		{CallerID: "acme-app", Pathway: "chat", Outcome: "success", TotalTokens: 7},
	}}
	h := console.New(console.Config{
		Status: stubStatus{ready: true}, Metrics: metrics.NewRegistry(), Audit: src,
	}).Handler()
	rec := get(t, h, "/api/console/audit/usage", "127.0.0.1:1", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("usage audit = %d", rec.Code)
	}
	for _, want := range []string{`"callerId":"acme-app"`, `"pathway":"chat"`, `"totalTokens":7`} {
		if !strings.Contains(rec.Body.String(), want) {
			t.Fatalf("usage audit missing %q: %s", want, rec.Body.String())
		}
	}
}

func TestAuditUsageErrorSurfaced(t *testing.T) {
	h := console.New(console.Config{
		Status: stubStatus{ready: true}, Metrics: metrics.NewRegistry(), Audit: stubAudit{err: errStub},
	}).Handler()
	rec := get(t, h, "/api/console/audit/usage", "127.0.0.1:1", nil)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("usage audit error = %d, want 500", rec.Code)
	}
}

func TestAdminAuditRecordsApplies(t *testing.T) {
	h := newConsoleWithEditor(stubEditor{apply: console.ApplyResult{Applied: true, Version: "v2"}})
	// An apply must be recorded in the admin-action audit.
	post(t, h, "/api/console/edit/permeability/apply", `{"raw":"version: 1\n","version":"v1"}`, "127.0.0.1:1")
	post(t, h, "/api/console/edit/callers/form/apply", `{"form":{"callers":[]},"version":"v1"}`, "127.0.0.1:1")

	rec := get(t, h, "/api/console/audit/admin", "127.0.0.1:1", nil)
	body := rec.Body.String()
	for _, want := range []string{`"action":"apply:permeability"`, `"action":"apply-form:callers"`, `"result":"applied"`, `"remote":"localhost"`} {
		if !strings.Contains(body, want) {
			t.Fatalf("admin audit missing %q: %s", want, body)
		}
	}
}

func TestTestPathwayUnavailableWhenNoBaseURL(t *testing.T) {
	h := newConsoleWithEditor(stubEditor{}) // no ChatBaseURL
	rec := post(t, h, "/api/console/test/chat", `{"callerKey":"k","request":{}}`, "127.0.0.1:1")
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("test without base URL = %d, want 503", rec.Code)
	}
}

func TestTestPathwayValidation(t *testing.T) {
	h := console.New(console.Config{
		Status: stubStatus{ready: true}, Metrics: metrics.NewRegistry(), ChatBaseURL: "http://127.0.0.1:9",
	}).Handler()
	// Missing caller key.
	rec := post(t, h, "/api/console/test/chat", `{"request":{"pathway":"chat"}}`, "127.0.0.1:1")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("missing key = %d, want 400", rec.Code)
	}
	// Malformed body.
	bad := post(t, h, "/api/console/test/chat", `{not json`, "127.0.0.1:1")
	if bad.Code != http.StatusBadRequest {
		t.Fatalf("malformed test = %d, want 400", bad.Code)
	}
}

func TestTestPathwayProxiesToDataPlane(t *testing.T) {
	// A fake data plane that echoes the Authorization header and the request.
	var gotAuth, gotBody string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"pathway":"chat","message":{"role":"assistant","content":"hi"}}`))
	}))
	defer upstream.Close()

	h := console.New(console.Config{
		Status: stubStatus{ready: true}, Metrics: metrics.NewRegistry(), ChatBaseURL: upstream.URL,
	}).Handler()
	rec := post(t, h, "/api/console/test/chat",
		`{"callerKey":"ck_demo.secret","request":{"pathway":"chat","messages":[{"role":"user","content":"hi"}]}}`,
		"127.0.0.1:1")
	if rec.Code != http.StatusOK {
		t.Fatalf("proxy = %d, want 200", rec.Code)
	}
	if gotAuth != "Bearer ck_demo.secret" {
		t.Fatalf("upstream Authorization = %q", gotAuth)
	}
	if !strings.Contains(gotBody, `"pathway":"chat"`) {
		t.Fatalf("upstream body = %q", gotBody)
	}
	if !strings.Contains(rec.Body.String(), `"content":"hi"`) {
		t.Fatalf("relayed body missing: %s", rec.Body.String())
	}
}

func TestTestPathwayUpstreamError(t *testing.T) {
	// Port 1 refuses connections, so the proxy reports a bad gateway and records
	// an "error" admin action.
	h := console.New(console.Config{
		Status: stubStatus{ready: true}, Metrics: metrics.NewRegistry(), ChatBaseURL: "http://127.0.0.1:1",
	}).Handler()
	rec := post(t, h, "/api/console/test/chat",
		`{"callerKey":"k","request":{"pathway":"chat"}}`, "127.0.0.1:1")
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("upstream error = %d, want 502", rec.Code)
	}
	admin := get(t, h, "/api/console/audit/admin", "127.0.0.1:1", nil)
	if !strings.Contains(admin.Body.String(), `"action":"test:chat"`) ||
		!strings.Contains(admin.Body.String(), `"result":"error"`) {
		t.Fatalf("test error not audited: %s", admin.Body.String())
	}
}

func TestTestPathwayRelaysStream(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("data: {\"delta\":\"hel\"}\n\ndata: {\"delta\":\"lo\"}\n\ndata: [DONE]\n\n"))
	}))
	defer upstream.Close()
	h := console.New(console.Config{
		Status: stubStatus{ready: true}, Metrics: metrics.NewRegistry(), ChatBaseURL: upstream.URL,
	}).Handler()
	rec := post(t, h, "/api/console/test/chat",
		`{"callerKey":"k","request":{"pathway":"chat","stream":true}}`, "127.0.0.1:1")
	if rec.Code != http.StatusOK {
		t.Fatalf("stream relay = %d", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Fatalf("relay content-type = %q", ct)
	}
	if !strings.Contains(rec.Body.String(), "[DONE]") {
		t.Fatalf("stream not relayed: %s", rec.Body.String())
	}
}

func TestAdminAuditRecordsRemoteHostAndOutcomes(t *testing.T) {
	// A token-authorized non-local client: the admin audit records the bare host
	// and the rejected/unchanged outcomes.
	h := console.New(console.Config{
		Status:    stubStatus{ready: true},
		Metrics:   metrics.NewRegistry(),
		Editor:    stubEditor{apply: console.ApplyResult{Rejected: true, Reason: "drift"}},
		AuthToken: "adm",
	}).Handler()
	req := httptest.NewRequest(http.MethodPost, "/api/console/edit/callers/apply",
		strings.NewReader(`{"raw":"x","version":"v1"}`))
	req.RemoteAddr = "10.1.2.3:5555"
	req.Header.Set("X-Console-Token", "adm")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	admin := httptest.NewRequest(http.MethodGet, "/api/console/audit/admin", nil)
	admin.RemoteAddr = "127.0.0.1:1"
	arec := httptest.NewRecorder()
	h.ServeHTTP(arec, admin)
	body := arec.Body.String()
	if !strings.Contains(body, `"result":"rejected"`) || !strings.Contains(body, `"remote":"10.1.2.3"`) {
		t.Fatalf("admin audit remote/outcome wrong: %s", body)
	}
}

// TestNilLoggerIsSafe exercises the no-op logger default.
func TestNilLoggerIsSafe(t *testing.T) {
	c := console.New(console.Config{Status: stubStatus{ready: true}, Metrics: metrics.NewRegistry()})
	if c.Handler() == nil {
		t.Fatal("handler should not be nil")
	}
}
