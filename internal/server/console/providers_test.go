package console_test

import (
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/inferencepath/membrane/internal/observability/metrics"
	"github.com/inferencepath/membrane/internal/server/console"
)

// stubProviders is a controllable ProviderManager. It records the last (name,
// value) pair so a test can prove the key value is accepted but never echoed.
type stubProviders struct {
	list        console.ProvidersView
	listErr     error
	setErr      error
	deleteErr   error
	deleted     bool
	lastSetName string
	lastSetVal  string
}

func (s *stubProviders) List() (console.ProvidersView, error) { return s.list, s.listErr }

func (s *stubProviders) Set(in console.ProviderInput) error {
	s.lastSetName = in.Name
	s.lastSetVal = in.Value
	return s.setErr
}

func (s *stubProviders) Delete(name string) (bool, error) { return s.deleted, s.deleteErr }

func newConsoleWithProviders(pm console.ProviderManager) http.Handler {
	return console.New(console.Config{
		Status:    stubStatus{ready: true},
		Metrics:   metrics.NewRegistry(),
		Providers: pm,
		Version:   console.Version{Version: "9.9.9"},
	}).Handler()
}

func TestProvidersListReturnsReferencesOnly(t *testing.T) {
	pm := &stubProviders{list: console.ProvidersView{Providers: []console.ProviderView{
		{Name: "openai-main", Sealed: true, SecretRef: "secret:openai-main", UsedBy: []string{"chat"}},
	}}}
	h := newConsoleWithProviders(pm)
	rec := reqNoBody(t, h, http.MethodGet, "/api/console/providers", "127.0.0.1:1")
	if rec.Code != http.StatusOK {
		t.Fatalf("list = %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, `"openai-main"`) || !strings.Contains(body, `"sealed":true`) ||
		!strings.Contains(body, `"secret:openai-main"`) || !strings.Contains(body, `"chat"`) {
		t.Fatalf("list body missing expected fields: %s", body)
	}
}

func TestProviderSetAcceptsKeyButNeverEchoes(t *testing.T) {
	pm := &stubProviders{}
	h := newConsoleWithProviders(pm)
	const value = "sk-should-not-appear"
	rec := reqBody(t, h, http.MethodPost, "/api/console/providers",
		`{"name":"openai-main","value":"`+value+`"}`, "127.0.0.1:1")
	if rec.Code != http.StatusOK {
		t.Fatalf("set = %d, want 200", rec.Code)
	}
	if pm.lastSetName != "openai-main" || pm.lastSetVal != value {
		t.Fatalf("manager received (%q,%q)", pm.lastSetName, pm.lastSetVal)
	}
	if strings.Contains(rec.Body.String(), value) {
		t.Fatalf("SET response leaked the key: %s", rec.Body.String())
	}
}

func TestProviderSetMalformedBody(t *testing.T) {
	h := newConsoleWithProviders(&stubProviders{})
	rec := reqBody(t, h, http.MethodPost, "/api/console/providers", "{not json", "127.0.0.1:1")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("malformed set = %d, want 400", rec.Code)
	}
}

func TestProviderSetRejectedSurfacesError(t *testing.T) {
	h := newConsoleWithProviders(&stubProviders{setErr: errors.New("invalid name")})
	rec := reqBody(t, h, http.MethodPost, "/api/console/providers", `{"name":"BAD","value":"v"}`, "127.0.0.1:1")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("rejected set = %d, want 400", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "invalid name") {
		t.Fatalf("expected error surfaced: %s", rec.Body.String())
	}
}

func TestProviderDeleteReferencedConflict(t *testing.T) {
	h := newConsoleWithProviders(&stubProviders{deleteErr: errors.New("provider is used by pathway(s) chat")})
	rec := reqNoBody(t, h, http.MethodDelete, "/api/console/providers/openai-main", "127.0.0.1:1")
	if rec.Code != http.StatusConflict {
		t.Fatalf("referenced delete = %d, want 409", rec.Code)
	}
}

func TestProviderDeleteOK(t *testing.T) {
	h := newConsoleWithProviders(&stubProviders{deleted: true})
	rec := reqNoBody(t, h, http.MethodDelete, "/api/console/providers/openai-main", "127.0.0.1:1")
	if rec.Code != http.StatusOK {
		t.Fatalf("delete = %d, want 200", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `"deleted":true`) {
		t.Fatalf("delete body: %s", rec.Body.String())
	}
}

func TestProvidersUnavailableWhenNil(t *testing.T) {
	h := newConsoleWithConfig(stubConfig{}) // no Providers manager wired
	for _, tc := range []struct{ method, path string }{
		{http.MethodGet, "/api/console/providers"},
		{http.MethodPost, "/api/console/providers"},
		{http.MethodDelete, "/api/console/providers/x"},
	} {
		rec := reqNoBody(t, h, tc.method, tc.path, "127.0.0.1:1")
		if rec.Code != http.StatusServiceUnavailable {
			t.Fatalf("%s %s = %d, want 503", tc.method, tc.path, rec.Code)
		}
	}
}
