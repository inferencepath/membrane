package console_test

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/inferencepath/membrane/internal/observability/metrics"
	"github.com/inferencepath/membrane/internal/server/console"
)

// stubSecrets is a SecretManager that records the last value it was given.
type stubSecrets struct {
	list       console.SecretsView
	listErr    error
	setErr     error
	deleteErr  error
	deleted    bool
	lastSetVal string
}

func (s *stubSecrets) List() (console.SecretsView, error) { return s.list, s.listErr }

func (s *stubSecrets) Set(name, value string) error {
	s.lastSetVal = value
	return s.setErr
}

func (s *stubSecrets) Delete(name string) (bool, error) { return s.deleted, s.deleteErr }

func newConsoleWithSecrets(sm console.SecretManager) http.Handler {
	return console.New(console.Config{
		Status:  stubStatus{ready: true},
		Metrics: metrics.NewRegistry(),
		Secrets: sm,
		Version: console.Version{Version: "9.9.9"},
	}).Handler()
}

func reqNoBody(t *testing.T, h http.Handler, method, path, remoteAddr string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, nil)
	req.RemoteAddr = remoteAddr
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func reqBody(t *testing.T, h http.Handler, method, path, body, remoteAddr string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.RemoteAddr = remoteAddr
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestSecretsListReturnsMetadataOnly(t *testing.T) {
	sm := &stubSecrets{list: console.SecretsView{Secrets: []console.SecretMetaView{
		{Name: "openai-main", Version: 2, KeyVersion: 1, UpdatedAt: "2026-01-02T03:04:05Z", Referenced: true},
	}}}
	h := newConsoleWithSecrets(sm)
	rec := reqNoBody(t, h, http.MethodGet, "/api/console/secrets", "127.0.0.1:1")
	if rec.Code != http.StatusOK {
		t.Fatalf("list = %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, `"openai-main"`) || !strings.Contains(body, `"referenced":true`) {
		t.Fatalf("list body missing metadata: %s", body)
	}
}

func TestSecretsSetAcceptsValueButNeverEchoes(t *testing.T) {
	sm := &stubSecrets{}
	h := newConsoleWithSecrets(sm)
	const value = "sk-should-not-appear-in-response"
	rec := reqBody(t, h, http.MethodPut, "/api/console/secrets/openai-main", `{"value":"`+value+`"}`, "127.0.0.1:1")
	if rec.Code != http.StatusOK {
		t.Fatalf("set = %d, want 200", rec.Code)
	}
	if sm.lastSetVal != value {
		t.Fatalf("manager received %q, want %q", sm.lastSetVal, value)
	}
	if strings.Contains(rec.Body.String(), value) {
		t.Fatalf("SET response leaked the value: %s", rec.Body.String())
	}
}

func TestSecretsSetMalformedBody(t *testing.T) {
	h := newConsoleWithSecrets(&stubSecrets{})
	rec := reqBody(t, h, http.MethodPut, "/api/console/secrets/x", "{not json", "127.0.0.1:1")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("malformed set = %d, want 400", rec.Code)
	}
}

func TestSecretsSetRejectedSurfacesError(t *testing.T) {
	h := newConsoleWithSecrets(&stubSecrets{setErr: errors.New("invalid name")})
	rec := reqBody(t, h, http.MethodPut, "/api/console/secrets/BAD", `{"value":"v"}`, "127.0.0.1:1")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("rejected set = %d, want 400", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "invalid name") {
		t.Fatalf("expected error surfaced: %s", rec.Body.String())
	}
}

func TestSecretsDeleteReferencedConflict(t *testing.T) {
	h := newConsoleWithSecrets(&stubSecrets{deleteErr: errors.New("secret is referenced by a credential")})
	rec := reqNoBody(t, h, http.MethodDelete, "/api/console/secrets/openai-main", "127.0.0.1:1")
	if rec.Code != http.StatusConflict {
		t.Fatalf("referenced delete = %d, want 409", rec.Code)
	}
}

func TestSecretsDeleteOK(t *testing.T) {
	h := newConsoleWithSecrets(&stubSecrets{deleted: true})
	rec := reqNoBody(t, h, http.MethodDelete, "/api/console/secrets/openai-main", "127.0.0.1:1")
	if rec.Code != http.StatusOK {
		t.Fatalf("delete = %d, want 200", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `"deleted":true`) {
		t.Fatalf("delete body: %s", rec.Body.String())
	}
}

func TestSecretsUnavailableWhenNil(t *testing.T) {
	h := newConsoleWithConfig(stubConfig{}) // no Secrets manager wired
	for _, tc := range []struct{ method, path string }{
		{http.MethodGet, "/api/console/secrets"},
		{http.MethodPut, "/api/console/secrets/x"},
		{http.MethodDelete, "/api/console/secrets/x"},
	} {
		rec := reqNoBody(t, h, tc.method, tc.path, "127.0.0.1:1")
		if rec.Code != http.StatusServiceUnavailable {
			t.Fatalf("%s %s = %d, want 503", tc.method, tc.path, rec.Code)
		}
	}
}

func TestSecretsRequireAuthNonLocal(t *testing.T) {
	h := newConsoleWithSecrets(&stubSecrets{})
	// A non-local client with no token configured fails closed with 401.
	rec := reqNoBody(t, h, http.MethodGet, "/api/console/secrets", "10.0.0.5:1")
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("non-local secrets = %d, want 401", rec.Code)
	}
}
