package console_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/inferencepath/membrane/internal/observability/metrics"
	"github.com/inferencepath/membrane/internal/server/console"
)

// stubPathwayCreator is a controllable PathwayCreator for the wizard handler.
type stubPathwayCreator struct {
	result console.ApplyResult
	last   console.PathwayWizardInput
}

func (s *stubPathwayCreator) CreatePathway(in console.PathwayWizardInput) console.ApplyResult {
	s.last = in
	return s.result
}

func newConsoleWithPathwayCreator(pc console.PathwayCreator) http.Handler {
	return console.New(console.Config{
		Status:         stubStatus{ready: true},
		Metrics:        metrics.NewRegistry(),
		Config:         stubConfig{},
		PathwayCreator: pc,
		Version:        console.Version{Version: "9.9.9"},
	}).Handler()
}

func TestPathwayCreateApplied(t *testing.T) {
	pc := &stubPathwayCreator{result: console.ApplyResult{Applied: true, Version: "v2"}}
	h := newConsoleWithPathwayCreator(pc)
	body := `{"pathway":"reason","providers":["main"],"defaultProvider":"main","cascadeAllowed":true,` +
		`"outboundAllow":["messages"],"inboundDefault":["message"],"inboundOnReveal":["provider"]}`
	rec := post(t, h, "/api/console/pathways", body, "127.0.0.1:1")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"applied":true`) {
		t.Fatalf("create = %d body=%s", rec.Code, rec.Body.String())
	}
	if pc.last.Pathway != "reason" || len(pc.last.Providers) != 1 || !pc.last.CascadeAllowed {
		t.Fatalf("creator received wrong input: %+v", pc.last)
	}
	// The create must be recorded in the admin-action audit.
	admin := get(t, h, "/api/console/audit/admin", "127.0.0.1:1", nil)
	if !strings.Contains(admin.Body.String(), `"action":"pathways:create:reason"`) {
		t.Fatalf("pathway create not audited: %s", admin.Body.String())
	}
}

func TestPathwayCreateMalformedBody(t *testing.T) {
	h := newConsoleWithPathwayCreator(&stubPathwayCreator{})
	rec := post(t, h, "/api/console/pathways", `{not json`, "127.0.0.1:1")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("malformed create = %d, want 400", rec.Code)
	}
}

func TestPathwayCreateUnavailableWhenNil(t *testing.T) {
	h := newConsoleWithConfig(stubConfig{}) // no PathwayCreator
	rec := post(t, h, "/api/console/pathways", `{"pathway":"x","providers":["p"]}`, "127.0.0.1:1")
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("create without port = %d, want 503", rec.Code)
	}
}

// stubCallerManager is a controllable CallerManager for the caller handlers.
type stubCallerManager struct {
	result    console.ApplyResult
	last      console.CallerInput
	deleted   bool
	deleteErr error
}

func (s *stubCallerManager) CreateCaller(in console.CallerInput) console.ApplyResult {
	s.last = in
	return s.result
}

func (s *stubCallerManager) DeleteCaller(keyID string) (bool, error) {
	return s.deleted, s.deleteErr
}

func newConsoleWithCallers(m console.CallerManager) http.Handler {
	return console.New(console.Config{
		Status:  stubStatus{ready: true},
		Metrics: metrics.NewRegistry(),
		Config:  stubConfig{},
		Callers: m,
		Version: console.Version{Version: "9.9.9"},
	}).Handler()
}

func TestCallerCreateApplied(t *testing.T) {
	m := &stubCallerManager{result: console.ApplyResult{Applied: true}}
	h := newConsoleWithCallers(m)
	body := `{"keyId":"ck_acme","callerId":"acme","name":"Acme","allow":["chat"],"secret":"s3cr3t"}`
	rec := post(t, h, "/api/console/callers", body, "127.0.0.1:1")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"applied":true`) {
		t.Fatalf("create = %d body=%s", rec.Code, rec.Body.String())
	}
	if m.last.KeyID != "ck_acme" || m.last.Secret != "s3cr3t" {
		t.Fatalf("manager received wrong input: %+v", m.last)
	}
	// The response must never echo the plaintext secret.
	if strings.Contains(rec.Body.String(), "s3cr3t") {
		t.Fatalf("response leaked the secret: %s", rec.Body.String())
	}
	admin := get(t, h, "/api/console/audit/admin", "127.0.0.1:1", nil)
	if !strings.Contains(admin.Body.String(), `"action":"callers:create:ck_acme"`) {
		t.Fatalf("caller create not audited: %s", admin.Body.String())
	}
}

func TestCallerCreateMissingFields(t *testing.T) {
	h := newConsoleWithCallers(&stubCallerManager{})
	rec := post(t, h, "/api/console/callers", `{"keyId":"ck_acme"}`, "127.0.0.1:1")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("missing fields = %d, want 400", rec.Code)
	}
}

func TestCallerDelete(t *testing.T) {
	m := &stubCallerManager{deleted: true}
	h := newConsoleWithCallers(m)
	req := httptest.NewRequest(http.MethodDelete, "/api/console/callers/ck_acme", nil)
	req.RemoteAddr = "127.0.0.1:1"
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"deleted":true`) {
		t.Fatalf("delete = %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestCallerCreateUnavailableWhenNil(t *testing.T) {
	h := newConsoleWithConfig(stubConfig{}) // no CallerManager
	rec := post(t, h, "/api/console/callers", `{"keyId":"a","callerId":"b","secret":"c"}`, "127.0.0.1:1")
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("create without port = %d, want 503", rec.Code)
	}
}
