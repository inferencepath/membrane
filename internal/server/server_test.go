package server_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/inferencepath/membrane/internal/server"
)

func newTestServer() *server.Server {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	return server.New("127.0.0.1:0", 2*time.Second,
		server.VersionInfo{Version: "1.2.3", Commit: "abc", BuildTime: "2026-10-02T00:00:00Z"}, logger)
}

func TestHandleMountsAdditionalRoute(t *testing.T) {
	srv := newTestServer()
	srv.Handle("/v1/chat", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTeapot)
	}))
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/v1/chat")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusTeapot {
		t.Fatalf("status = %d, want the mounted handler to respond", resp.StatusCode)
	}
}

func TestHealthEndpoint(t *testing.T) {
	ts := httptest.NewServer(newTestServer().Handler())
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/healthz")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	var body map[string]string
	_ = json.NewDecoder(resp.Body).Decode(&body)
	if body["status"] != "ok" {
		t.Fatalf("status body = %v", body)
	}
}

func TestReadinessTransitions(t *testing.T) {
	s := newTestServer()
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()

	// Initial: not ready, reason "starting".
	resp, err := http.Get(ts.URL + "/readyz")
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("initial readyz = %d, want 503", resp.StatusCode)
	}
	var body map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&body)
	resp.Body.Close()
	if body["ready"] != false || body["reason"] != "starting" {
		t.Fatalf("readyz body = %v", body)
	}

	// Ready.
	s.SetReady()
	resp, _ = http.Get(ts.URL + "/readyz")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("ready readyz = %d, want 200", resp.StatusCode)
	}
	resp.Body.Close()

	// Not ready with custom reason.
	s.SetNotReady("invalid or missing permeability spec")
	resp, _ = http.Get(ts.URL + "/readyz")
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("notready readyz = %d, want 503", resp.StatusCode)
	}
	_ = json.NewDecoder(resp.Body).Decode(&body)
	resp.Body.Close()
	if body["reason"] != "invalid or missing permeability spec" {
		t.Fatalf("reason = %v", body["reason"])
	}
}

func TestVersionEndpoint(t *testing.T) {
	ts := httptest.NewServer(newTestServer().Handler())
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/version")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var v server.VersionInfo
	_ = json.NewDecoder(resp.Body).Decode(&v)
	if v.Version != "1.2.3" || v.Commit != "abc" {
		t.Fatalf("version = %+v", v)
	}
}

func TestAddr(t *testing.T) {
	if got := newTestServer().Addr(); got != "127.0.0.1:0" {
		t.Fatalf("addr = %q", got)
	}
}

func TestReadyAndVersionGetters(t *testing.T) {
	s := newTestServer()

	// Initial: not ready with the "starting" reason.
	if ready, reason := s.Ready(); ready || reason != "starting" {
		t.Fatalf("initial Ready() = %v, %q", ready, reason)
	}

	s.SetReady()
	if ready, reason := s.Ready(); !ready || reason != "" {
		t.Fatalf("after SetReady Ready() = %v, %q", ready, reason)
	}

	s.SetNotReady("invalid configuration")
	if ready, reason := s.Ready(); ready || reason != "invalid configuration" {
		t.Fatalf("after SetNotReady Ready() = %v, %q", ready, reason)
	}

	if v := s.Version(); v.Version != "1.2.3" || v.Commit != "abc" {
		t.Fatalf("Version() = %+v", v)
	}
}

func TestRunListenError(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	// Invalid port forces ListenAndServe to fail immediately.
	s := server.New("127.0.0.1:999999", 2*time.Second, server.VersionInfo{Version: "x"}, logger)
	if err := s.Run(context.Background()); err == nil {
		t.Fatalf("expected listen error, got nil")
	}
}

func TestRunGracefulShutdown(t *testing.T) {
	s := newTestServer()
	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan error, 1)
	go func() { done <- s.Run(ctx) }()

	time.Sleep(100 * time.Millisecond) // allow bind
	cancel()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run returned error: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("Run did not shut down in time")
	}
}
