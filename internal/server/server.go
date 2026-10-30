// Package server implements the HTTP server shell: liveness, readiness, and
// version endpoints, plus graceful shutdown. No endpoint accepts or emits caller
// identity or secrets.
package server

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"sync/atomic"
	"time"
)

// VersionInfo carries build metadata for the /version endpoint.
type VersionInfo struct {
	Version   string `json:"version"`
	Commit    string `json:"commit,omitempty"`
	BuildTime string `json:"buildTime,omitempty"`
}

// Server is the HTTP server shell.
type Server struct {
	httpSrv         *http.Server
	mux             *http.ServeMux
	ready           atomic.Bool
	notReadyReason  atomic.Value // string
	version         VersionInfo
	logger          *slog.Logger
	shutdownTimeout time.Duration
}

// New constructs a Server listening on addr.
func New(addr string, shutdownTimeout time.Duration, version VersionInfo, logger *slog.Logger) *Server {
	s := &Server{
		version:         version,
		logger:          logger,
		shutdownTimeout: shutdownTimeout,
	}
	s.notReadyReason.Store("starting")

	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", s.handleHealth)
	mux.HandleFunc("/readyz", s.handleReady)
	mux.HandleFunc("/version", s.handleVersion)
	s.mux = mux

	s.httpSrv = &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}
	return s
}

// Handle registers an additional handler (e.g. the logical API) on the server's
// mux. It must be called before Run.
func (s *Server) Handle(pattern string, h http.Handler) {
	s.mux.Handle(pattern, h)
}

// Handler returns the server's HTTP handler.
func (s *Server) Handler() http.Handler { return s.httpSrv.Handler }

// Addr returns the configured listen address.
func (s *Server) Addr() string { return s.httpSrv.Addr }

// Ready reports whether the server is ready and, if not, a secret-free reason.
// It is lock-free and safe for concurrent use, such as by the Console.
func (s *Server) Ready() (bool, string) {
	ready := s.ready.Load()
	reason, _ := s.notReadyReason.Load().(string)
	return ready, reason
}

// Version returns the build metadata reported on /version.
func (s *Server) Version() VersionInfo { return s.version }

// SetReady marks the server ready to serve.
func (s *Server) SetReady() {
	s.ready.Store(true)
	s.notReadyReason.Store("")
}

// SetNotReady marks the server not ready with a secret-free reason.
func (s *Server) SetNotReady(reason string) {
	s.ready.Store(false)
	s.notReadyReason.Store(reason)
}

// Run starts the server and blocks until ctx is cancelled, then shuts down
// gracefully within the configured timeout.
func (s *Server) Run(ctx context.Context) error {
	errCh := make(chan error, 1)
	go func() {
		err := s.httpSrv.ListenAndServe()
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
			return
		}
		errCh <- nil
	}()
	s.logger.Info("server started", "address", s.httpSrv.Addr)

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
		s.logger.Info("shutdown signal received")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), s.shutdownTimeout)
		defer cancel()
		if err := s.httpSrv.Shutdown(shutdownCtx); err != nil {
			return err
		}
		s.logger.Info("server stopped gracefully")
		return nil
	}
}

func (s *Server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) handleReady(w http.ResponseWriter, _ *http.Request) {
	if s.ready.Load() {
		writeJSON(w, http.StatusOK, map[string]any{"ready": true})
		return
	}
	reason, _ := s.notReadyReason.Load().(string)
	writeJSON(w, http.StatusServiceUnavailable, map[string]any{"ready": false, "reason": reason})
}

func (s *Server) handleVersion(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, s.version)
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}
