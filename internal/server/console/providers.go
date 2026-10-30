package console

import (
	"encoding/json"
	"net/http"
)

// providersGuard returns the ProviderManager, or writes 503 and returns false
// when the sealed store is disabled.
func (c *Console) providersGuard(w http.ResponseWriter) (ProviderManager, bool) {
	if c.cfg.Providers == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{
			"error": "provider setup is not available (set secrets.master_key_ref)",
		})
		return nil, false
	}
	return c.cfg.Providers, true
}

// handleProvidersList serves each provider's reference, sealed status, and the
// pathways that use it.
func (c *Console) handleProvidersList(w http.ResponseWriter, _ *http.Request) {
	mgr, ok := c.providersGuard(w)
	if !ok {
		return
	}
	v, err := mgr.List()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	if v.Providers == nil {
		v.Providers = []ProviderView{}
	}
	writeJSON(w, http.StatusOK, v)
}

// handleProviderSet seals a provider's key and upserts its definition, including
// the secret:<name> reference routing resolves, in one transaction.
func (c *Console) handleProviderSet(w http.ResponseWriter, r *http.Request) {
	mgr, ok := c.providersGuard(w)
	if !ok {
		return
	}
	raw, ok := readEditBody(w, r)
	if !ok {
		return
	}
	var body ProviderInput
	if err := json.Unmarshal(raw, &body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "malformed request body"})
		return
	}
	if err := mgr.Set(body); err != nil {
		c.recordAdmin(r, "providers:set:"+body.Name, "rejected")
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	c.recordAdmin(r, "providers:set:"+body.Name, "applied")
	writeJSON(w, http.StatusOK, map[string]any{"applied": true, "name": body.Name})
}

// handleProviderDelete removes a provider's credential and sealed secret. A
// provider still referenced by a pathway target is refused with 409.
func (c *Console) handleProviderDelete(w http.ResponseWriter, r *http.Request) {
	mgr, ok := c.providersGuard(w)
	if !ok {
		return
	}
	name := r.PathValue("name")
	deleted, err := mgr.Delete(name)
	if err != nil {
		c.recordAdmin(r, "providers:delete:"+name, "rejected")
		writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
		return
	}
	c.recordAdmin(r, "providers:delete:"+name, "applied")
	writeJSON(w, http.StatusOK, map[string]any{"deleted": deleted, "name": name})
}
