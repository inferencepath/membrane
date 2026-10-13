// Package console implements the Membrane Console: a built-in, self-hosted
// operator UI served on a SEPARATE admin listener, never on the data-plane
// (/v1/chat) mux. It is a read-mostly operability surface (status, health,
// metrics) in this slice (console phases C0–C1); later slices add config
// editing on top of the same validate → atomic write → reload pipeline.
//
// Isolation is structural: this package never constructs a SanitizedRequest,
// never mounts the egress handler, and never emits caller/org identity or any
// secret. Secrets (e.g. the admin token) exist ONLY by reference. Access binds
// to loopback by default; non-local requests require the configured admin token.
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

// StatusProvider reports the data-plane server's readiness. The console reads it
// from its own listener to render liveness/readiness without touching the egress
// path. *server.Server satisfies this.
type StatusProvider interface {
	Ready() (bool, string)
}

// MetricsProvider supplies an identity-free snapshot of the metrics registry.
// *metrics.Registry satisfies this.
type MetricsProvider interface {
	Snapshot() metrics.Snapshot
}

// Version carries build metadata surfaced on the console's status endpoint.
type Version struct {
	Version   string `json:"version"`
	Commit    string `json:"commit,omitempty"`
	BuildTime string `json:"buildTime,omitempty"`
}

// Config wires the console's (read-only) dependencies.
type Config struct {
	// Status is the data-plane server's readiness source (required).
	Status StatusProvider
	// Metrics is the identity-free metrics snapshot source (required).
	Metrics MetricsProvider
	// Config provides read-only, identity-free config views. Optional: when nil,
	// the config read endpoints return 503 (not available).
	Config ConfigSource
	// Editor applies config edits (validate → atomic write → reload). Optional:
	// when nil, the edit endpoints return 503 (e.g. not in reloadable mode).
	Editor ConfigEditor
	// Bundle provides declarative export/apply (the `deck`-equivalent). Optional:
	// when nil, the bundle endpoints return 503.
	Bundle ConfigBundle
	// PathwayCreator atomically creates a pathway + its permeability policy in one
	// transaction (the Pathways wizard). Optional: when nil, the endpoint returns
	// 503.
	PathwayCreator PathwayCreator
	// Operations supplies the read-only operations catalog (routes + per-operation
	// Permeability field palettes) that the Pathways wizard uses to filter
	// selectable fields by operation. Optional: when nil, the endpoint returns 503.
	Operations OperationCatalog
	// Callers creates/deletes Caller Keys (the app layer hashes the secret and
	// stores only the hash). Optional: when nil, the endpoints return 503.
	Callers CallerManager
	// Secrets manages the sealed secret store (set/rotate/delete values entered by
	// the operator, referenced from credentials as `secret:<name>`). Optional:
	// when nil, the secrets endpoints return 503 (sealed store disabled).
	Secrets SecretManager
	// Providers is the consolidated provider-setup port (seal a key + wire its
	// credential atomically). The primary Providers screen uses it. Optional: when
	// nil, the provider endpoints return 503 (sealed store disabled).
	Providers ProviderManager
	// Audit provides read-only access to the Interior-only usage audit records.
	// Optional: when nil, the usage-audit endpoint returns 503 (e.g. no file sink).
	Audit AuditSource
	// ChatBaseURL is the data-plane base URL (e.g. http://127.0.0.1:8080) the
	// Test Pathway action posts to. The console acts as a plain client of the
	// egress path (operator-supplied Caller Key); it never mounts the egress
	// handler. Empty disables the Test Pathway endpoint (503).
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

// New constructs a Console. The returned handler (via Handler) serves the
// embedded UI at "/" and the read-only control API under "/api/console/".
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
	mux.Handle("/", staticHandler())
	c.mux = mux
	return c
}

// Handler returns the console HTTP handler wrapped with admin authentication.
func (c *Console) Handler() http.Handler {
	return c.authMiddleware(c.mux)
}

// apiPrefix is the path prefix for the console's control API. Everything under
// it requires admin authorization; everything else (the embedded SPA shell +
// assets) is served publicly so the UI can render a login screen.
const apiPrefix = "/api/console/"

// authMiddleware enforces admin access on the control API: loopback clients are
// always allowed; any non-local client must present the configured admin token;
// with no token configured, non-local access is refused (fail closed).
//
// The static console UI (the login page, HTML, CSS, JS) is PUBLIC — it carries
// no secrets or data, and serving it unauthenticated lets the SPA present a
// login prompt when the API returns 401 (the Jenkins/Neo4j model). All sensitive
// data stays behind the authorized API under apiPrefix.
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

// discard is an io.Writer that drops all output (for the nil-logger default).
type discard struct{}

func (discard) Write(p []byte) (int, error) { return len(p), nil }
