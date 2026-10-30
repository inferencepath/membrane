package console

import (
	"fmt"
	"net/http"
	"os"
	"strings"
)

// statusResponse is the Console status payload: liveness, data-plane readiness
// and its reason, build metadata, and whether egress is enabled. It carries no
// identity or secrets.
type statusResponse struct {
	Live          bool    `json:"live"`
	Ready         bool    `json:"ready"`
	Reason        string  `json:"reason,omitempty"`
	EgressEnabled bool    `json:"egressEnabled"`
	Version       Version `json:"version"`
}

func (c *Console) handleStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "only GET is supported"})
		return
	}
	ready, reason := c.cfg.Status.Ready()
	writeJSON(w, http.StatusOK, statusResponse{
		Live:          true,
		Ready:         ready,
		Reason:        reason,
		EgressEnabled: ready,
		Version:       c.cfg.Version,
	})
}

// handleHealth returns the identity-free per-target health and failover series.
func (c *Console) handleHealth(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "only GET is supported"})
		return
	}
	snap := c.cfg.Metrics.Snapshot()
	writeJSON(w, http.StatusOK, map[string]any{
		"targets":   snap.Health,
		"failovers": snap.Failovers,
	})
}

// handleMetrics returns the identity-free metrics snapshot.
func (c *Console) handleMetrics(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "only GET is supported"})
		return
	}
	writeJSON(w, http.StatusOK, c.cfg.Metrics.Snapshot())
}

// ResolveTokenRef resolves the console admin token from a reference. Supported
// schemes mirror the Keyring: env:VAR and file:/path. Inline values and unknown
// schemes are rejected, so an admin token is never configured inline.
func ResolveTokenRef(ref string) (string, error) {
	scheme, rest, ok := strings.Cut(ref, ":")
	if !ok {
		return "", fmt.Errorf("console: invalid auth_token_ref (must be env:/file:)")
	}
	switch scheme {
	case "env":
		v := os.Getenv(rest)
		if v == "" {
			return "", fmt.Errorf("console: env reference %q is empty or unset", rest)
		}
		return v, nil
	case "file":
		b, err := os.ReadFile(rest)
		if err != nil {
			return "", fmt.Errorf("console: read file reference: %w", err)
		}
		v := strings.TrimSpace(string(b))
		if v == "" {
			return "", fmt.Errorf("console: file reference %q is empty", rest)
		}
		return v, nil
	default:
		return "", fmt.Errorf("console: unsupported auth_token_ref scheme %q (use env:/file:)", scheme)
	}
}
