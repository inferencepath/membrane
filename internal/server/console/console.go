// Package console implements the Membrane Console, a self-hosted operator UI
// and control API served on a separate admin listener, never on the data-plane
// mux. Config edits go through validation, an atomic write, and a reload.
//
// The package never constructs a SanitizedRequest, never mounts the egress
// handler, and never emits caller or org identity or any secret. Secrets such
// as the admin token are held by reference only. Loopback clients are always
// allowed; other clients need the configured admin token.
package console

import (
	"crypto/subtle"
	"encoding/json"
	"log/slog"
	"net"
	"net/http"
	"strings"

	"github.com/inferencepath/membrane/internal/observability/metrics"
)

// StatusProvider reports the data-plane server's readiness without touching the
// egress path. *server.Server satisfies it.
type StatusProvider interface {
	Ready() (bool, string)
}

// MetricsProvider supplies an identity-free snapshot of the metrics registry.
// *metrics.Registry satisfies it.
type MetricsProvider interface {
	Snapshot() metrics.Snapshot
}

// Version carries build metadata surfaced on the console's status endpoint.
type Version struct {
	Version   string `json:"version"`
	Commit    string `json:"commit,omitempty"`
	BuildTime string `json:"buildTime,omitempty"`
}

// Config holds the console's dependencies. Every optional dependency that is
// nil makes its endpoints return 503.
type Config struct {
	// Status is the data-plane readiness source. Required.
	Status StatusProvider
	// Metrics is the metrics snapshot source. Required.
	Metrics MetricsProvider
	// Config provides read-only, identity-free config views.
	Config ConfigSource
	// Editor applies config edits. It is nil when config is not reloadable.
	Editor ConfigEditor
	// Bundle provides declarative config export, diff, and apply.
	Bundle ConfigBundle
	// PathwayCreator creates a pathway and its permeability policy in one
	// transaction for the Pathways wizard.
	PathwayCreator PathwayCreator
	// Operations supplies the operations catalog the Pathways wizard uses to
	// filter Permeability fields by operation.
	Operations OperationCatalog
	// Callers creates and deletes Caller Keys. Only the secret's hash is stored.
	Callers CallerManager
	// Secrets manages the sealed secret store, whose values credentials
	// reference as secret:<name>. It is nil when the sealed store is disabled.
	Secrets SecretManager
	// Providers seals a provider key and wires its credential in one step. It
	// is nil when the sealed store is disabled.
	Providers ProviderManager
	// Audit reads the Interior-only usage audit records.
	Audit AuditSource
	// Panels contributes extension panels. When nil, the panel list is empty.
	Panels PanelProvider
	// ChatBaseURL is the data-plane base URL (e.g. http://127.0.0.1:8080) that
	// Test Pathway posts to with an operator-supplied Caller Key. The console
	// acts as an ordinary client and never mounts the egress handler. Empty
	// disables Test Pathway.
	ChatBaseURL string
	// Version is the build metadata to surface.
	Version Version
	// AuthToken is the resolved admin token. Empty means localhost-only access.
	AuthToken string
	// Logger is optional; a no-op logger is used when nil.
	Logger *slog.Logger
}

// Console is the Membrane Console HTTP surface (control API + embedded UI).
type Console struct {
	cfg   Config
	mux   *http.ServeMux
	admin *adminAudit
}

// New constructs a Console. Its handler serves the embedded UI at "/" and the
// control API under "/api/console/".
func New(cfg Config) *Console {
	if cfg.Logger == nil {
		cfg.Logger = slog.New(slog.NewTextHandler(discard{}, nil))
	}
	c := &Console{cfg: cfg, admin: newAdminAudit(adminAuditCap)}

	mux := http.NewServeMux()
	mux.HandleFunc("/api/console/status", c.handleStatus)
	mux.HandleFunc("/api/console/health", c.handleHealth)
	mux.HandleFunc("/api/console/metrics", c.handleMetrics)
	mux.HandleFunc("/api/console/config/server", c.handleConfigServer)
	mux.HandleFunc("/api/console/config/pathways", c.handleConfigPathways)
	mux.HandleFunc("/api/console/config/credentials", c.handleConfigCredentials)
	mux.HandleFunc("/api/console/config/callers", c.handleConfigCallers)
	mux.HandleFunc("/api/console/config/permeability", c.handleConfigPermeability)
	mux.HandleFunc("GET /api/console/config/export", c.handleConfigExport)
	mux.HandleFunc("POST /api/console/config/diff", c.handleConfigDiff)
	mux.HandleFunc("POST /api/console/config/apply", c.handleConfigApply)
	mux.HandleFunc("GET /api/console/edit/{kind}", c.handleEditRead)
	mux.HandleFunc("POST /api/console/edit/{kind}/validate", c.handleEditValidate)
	mux.HandleFunc("POST /api/console/edit/{kind}/apply", c.handleEditApply)
	mux.HandleFunc("POST /api/console/edit/{kind}/form/validate", c.handleEditFormValidate)
	mux.HandleFunc("POST /api/console/edit/{kind}/form/apply", c.handleEditFormApply)
	mux.HandleFunc("GET /api/console/operations", c.handleOperations)
	mux.HandleFunc("POST /api/console/pathways", c.handlePathwayCreate)
	mux.HandleFunc("POST /api/console/callers", c.handleCallerCreate)
	mux.HandleFunc("DELETE /api/console/callers/{keyId}", c.handleCallerDelete)
	mux.HandleFunc("POST /api/console/test/chat", c.handleTestChat)
	mux.HandleFunc("GET /api/console/secrets", c.handleSecretsList)
	mux.HandleFunc("PUT /api/console/secrets/{name}", c.handleSecretSet)
	mux.HandleFunc("DELETE /api/console/secrets/{name}", c.handleSecretDelete)
	mux.HandleFunc("GET /api/console/providers", c.handleProvidersList)
	mux.HandleFunc("POST /api/console/providers", c.handleProviderSet)
	mux.HandleFunc("DELETE /api/console/providers/{name}", c.handleProviderDelete)
	mux.HandleFunc("GET /api/console/audit/usage", c.handleAuditUsage)
	mux.HandleFunc("GET /api/console/audit/admin", c.handleAuditAdmin)
	mux.HandleFunc("GET /api/console/panels", c.handlePanels)
	mux.HandleFunc("/api/console/ext/{id}", c.handleExt)
	mux.HandleFunc("/api/console/ext/{id}/{rest...}", c.handleExt)
	mux.Handle("/", staticHandler())
	c.mux = mux
	return c
}

// Handler returns the console HTTP handler wrapped with admin authentication.
func (c *Console) Handler() http.Handler {
	return c.authMiddleware(c.mux)
}

// apiPrefix is the control API path prefix. Paths under it require admin
// authorization; the SPA shell and assets are public so the UI can show a
// login screen.
const apiPrefix = "/api/console/"

// authMiddleware enforces admin access on the control API. Loopback clients are
// allowed; other clients must present the admin token, and with no token
// configured they are refused (fail closed).
//
// The static UI holds no secrets or data. Serving it unauthenticated lets the
// SPA show a login prompt when the API returns 401.
func (c *Console) authMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, apiPrefix) {
			next.ServeHTTP(w, r) // public static UI
			return
		}
		if c.authorized(r) {
			next.ServeHTTP(w, r)
			return
		}
		w.Header().Set("WWW-Authenticate", "Bearer")
		writeJSON(w, http.StatusUnauthorized, map[string]string{
			"error": "console authentication required",
		})
	})
}

func (c *Console) authorized(r *http.Request) bool {
	if isLoopbackRemote(r.RemoteAddr) {
		return true
	}
	if c.cfg.AuthToken == "" {
		return false // fail closed: no token configured, non-local denied
	}
	return tokenMatches(r, c.cfg.AuthToken)
}

// tokenMatches checks the admin token in either the Authorization: Bearer header
// or the X-Console-Token header, using a constant-time comparison.
func tokenMatches(r *http.Request, want string) bool {
	if got := bearer(r.Header.Get("Authorization")); got != "" {
		if subtle.ConstantTimeCompare([]byte(got), []byte(want)) == 1 {
			return true
		}
	}
	if got := strings.TrimSpace(r.Header.Get("X-Console-Token")); got != "" {
		if subtle.ConstantTimeCompare([]byte(got), []byte(want)) == 1 {
			return true
		}
	}
	return false
}

func bearer(header string) string {
	const prefix = "Bearer "
	if len(header) <= len(prefix) || !strings.EqualFold(header[:len(prefix)], prefix) {
		return ""
	}
	return strings.TrimSpace(header[len(prefix):])
}

// isLoopbackRemote reports whether a request's RemoteAddr is a loopback address.
func isLoopbackRemote(remoteAddr string) bool {
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		host = remoteAddr
	}
	ip := net.ParseIP(strings.TrimSpace(host))
	return ip != nil && ip.IsLoopback()
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

// discard is an io.Writer that drops all output.
type discard struct{}

func (discard) Write(p []byte) (int, error) { return len(p), nil }
