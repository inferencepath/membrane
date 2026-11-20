package gemini_test

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/inferencepath/membrane/internal/exterior/providers"
	"github.com/inferencepath/membrane/internal/exterior/providers/gemini"
)

func tokenServer(t *testing.T, prefix string, calls *atomic.Int32) *httptest.Server {
	t.Helper()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		if r.Form.Get("grant_type") != "urn:ietf:params:oauth:grant-type:jwt-bearer" || r.Form.Get("assertion") == "" {
			http.Error(w, "bad grant", http.StatusBadRequest)
			return
		}
		n := calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token": prefix + "-" + string(rune('0'+n)), "token_type": "Bearer", "expires_in": 3600,
		})
	}))
	t.Cleanup(ts.Close)
	return ts
}

func serviceAccountKey(t *testing.T, tokenURI string) string {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(map[string]string{
		"type":           "service_account",
		"project_id":     "proj",
		"private_key_id": "kid",
		"private_key":    string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})),
		"client_email":   "membrane@proj.iam.gserviceaccount.com",
		"token_uri":      tokenURI,
	})
	return string(b)
}

func TestServiceAccountKeyMintsVertexBearer(t *testing.T) {
	var firstCalls, secondCalls atomic.Int32
	key1 := serviceAccountKey(t, tokenServer(t, "tok-a", &firstCalls).URL)
	key2 := serviceAccountKey(t, tokenServer(t, "tok-b", &secondCalls).URL)

	var gotAuth, gotPath, gotAPIKey string
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth, gotPath, gotAPIKey = r.Header.Get("Authorization"), r.URL.Path, r.Header.Get("x-goog-api-key")
		_, _ = io.WriteString(w, `{"candidates":[{"content":{"parts":[{"text":"ok"}]},"finishReason":"STOP"}]}`)
	}))
	defer api.Close()

	adapter := gemini.New(api.URL, 0, gemini.WithVertex("proj", "us-central1"), gemini.WithServiceAccountKey())
	call := func(key string) {
		t.Helper()
		_, err := adapter.Complete(context.Background(), providers.Request{
			Model: "gemini-2.5-flash", Payload: map[string]any{"messages": []any{map[string]any{"role": "user", "content": "hi"}}},
		}, providers.EgressIdentity{Secret: key})
		if err != nil {
			t.Fatalf("Complete: %v", err)
		}
	}

	call(key1)
	call(key1)
	if gotAuth != "Bearer tok-a-1" || firstCalls.Load() != 1 {
		t.Errorf("auth = %q after %d token calls, want cached token", gotAuth, firstCalls.Load())
	}
	if gotAPIKey != "" {
		t.Error("service-account key must not be sent as x-goog-api-key")
	}
	if gotPath != "/v1/projects/proj/locations/us-central1/publishers/google/models/gemini-2.5-flash:generateContent" {
		t.Errorf("path = %q", gotPath)
	}

	call(key2)
	if gotAuth != "Bearer tok-b-1" || secondCalls.Load() != 1 {
		t.Errorf("rotated key auth = %q after %d token calls", gotAuth, secondCalls.Load())
	}
}

func TestServiceAccountKeyRejectsOtherCredentialTypes(t *testing.T) {
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		t.Error("invalid key must not reach the provider")
	}))
	defer api.Close()
	adapter := gemini.New(api.URL, 0, gemini.WithVertex("proj", "us-central1"), gemini.WithServiceAccountKey())
	external := `{"type":"external_account","token_url":"http://169.254.169.254/","private_key":"SECRET-MATERIAL"}`
	_, err := adapter.Complete(context.Background(), providers.Request{
		Model: "gemini-2.5-flash", Payload: map[string]any{"messages": []any{map[string]any{"role": "user", "content": "hi"}}},
	}, providers.EgressIdentity{Secret: external})
	if err == nil {
		t.Fatal("want error for non-service-account credentials")
	}
	if strings.Contains(err.Error(), "SECRET-MATERIAL") {
		t.Errorf("error leaked key material: %v", err)
	}
}
