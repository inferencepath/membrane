package console

import (
	"encoding/json"
	"net/http"
)

// This file serves the declarative export/apply API (the `deck`-equivalent):
//   GET  /api/console/config/export  → the live configuration as one bundle
//   POST /api/console/config/diff    → dry-run per-kind change set (no write)
//   POST /api/console/config/apply   → validate → version check → atomic write → reload
//
// All three require admin authorization (enforced by the mux middleware) and a
// non-nil ConfigBundle; otherwise they return 503. Secrets stay by reference and
// caller secrets are hashes only — the bundle carries no values.

// bundleGuard returns the ConfigBundle when available, else writes 503.
func (c *Console) bundleGuard(w http.ResponseWriter) (ConfigBundle, bool) {
	if c.cfg.Bundle == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{
			"error": "declarative config is not available",
		})
		return nil, false
	}
	return c.cfg.Bundle, true
}

func (c *Console) handleConfigExport(w http.ResponseWriter, r *http.Request) {
	bundle, ok := c.bundleGuard(w)
	if !ok {
		return
	}
	f, err := bundle.ExportBundle()
	if err != nil {
		c.configError(w, err)
		return
	}
	c.recordAdmin(r, "export", "ok")
	// Serve as a downloadable YAML file so the SPA can save it directly, while
	// still being a plain GET usable by the CLI/curl.
	w.Header().Set("Content-Type", "application/yaml; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="membrane-config.yaml"`)
	w.Header().Set("X-Config-Version", f.Version)
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(f.Raw))
}

func (c *Console) handleConfigDiff(w http.ResponseWriter, r *http.Request) {
	bundle, ok := c.bundleGuard(w)
	if !ok {
		return
	}
	raw, ok := readEditBody(w, r)
	if !ok {
		return
	}
	diff, err := bundle.DiffBundle(raw)
	if err != nil {
		c.configError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, diff)
}

func (c *Console) handleConfigApply(w http.ResponseWriter, r *http.Request) {
	bundle, ok := c.bundleGuard(w)
	if !ok {
		return
	}
	body, ok := readEditBody(w, r)
	if !ok {
		return
	}
	var ab applyBody
	if err := json.Unmarshal(body, &ab); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "malformed apply body"})
		return
	}
	res := bundle.ApplyBundle([]byte(ab.Raw), ab.Version)
	c.recordAdmin(r, "apply:bundle", applyOutcome(res))
	writeJSON(w, http.StatusOK, res)
}
