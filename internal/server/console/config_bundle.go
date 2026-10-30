package console

import (
	"encoding/json"
	"net/http"
)

// Declarative config endpoints:
//
//	GET  /api/console/config/export  live configuration as one bundle
//	POST /api/console/config/diff    per-kind change set, no write
//	POST /api/console/config/apply   validate, check version, write, reload
//
// All three require admin authorization and a non-nil ConfigBundle (503
// otherwise). Bundles carry secret references and caller hashes, never values.

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
	// Served as a download so the UI can save it; still a plain GET for curl.
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
