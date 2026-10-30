package console

import (
	"encoding/json"
	"net/http"
)

// secretsGuard returns the SecretManager, or writes 503 and returns false when
// the sealed store is disabled.
func (c *Console) secretsGuard(w http.ResponseWriter) (SecretManager, bool) {
	if c.cfg.Secrets == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{
			"error": "sealed secret store is not available (set secrets.master_key_ref)",
		})
		return nil, false
	}
	return c.cfg.Secrets, true
}

// handleSecretsList serves sealed-secret metadata (never a value).
func (c *Console) handleSecretsList(w http.ResponseWriter, _ *http.Request) {
	mgr, ok := c.secretsGuard(w)
	if !ok {
		return
	}
	v, err := mgr.List()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	if v.Secrets == nil {
		v.Secrets = []SecretMetaView{}
	}
	writeJSON(w, http.StatusOK, v)
}

// secretSetBody holds the plaintext value to seal. It is never echoed back.
type secretSetBody struct {
	Value string `json:"value"`
}

// handleSecretSet creates or rotates a sealed secret. The name comes from the
// path and the value from the size-capped JSON body.
func (c *Console) handleSecretSet(w http.ResponseWriter, r *http.Request) {
	mgr, ok := c.secretsGuard(w)
	if !ok {
		return
	}
	name := r.PathValue("name")
	raw, ok := readEditBody(w, r)
	if !ok {
		return
	}
	var body secretSetBody
	if err := json.Unmarshal(raw, &body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "malformed request body"})
		return
	}
	if err := mgr.Set(name, body.Value); err != nil {
		c.recordAdmin(r, "secrets:set:"+name, "rejected")
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	c.recordAdmin(r, "secrets:set:"+name, "applied")
	writeJSON(w, http.StatusOK, map[string]any{"applied": true, "name": name})
}

// handleSecretDelete removes a sealed secret. A secret still referenced by a
// credential is refused with 409.
func (c *Console) handleSecretDelete(w http.ResponseWriter, r *http.Request) {
	mgr, ok := c.secretsGuard(w)
	if !ok {
		return
	}
	name := r.PathValue("name")
	deleted, err := mgr.Delete(name)
	if err != nil {
		c.recordAdmin(r, "secrets:delete:"+name, "rejected")
		writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
		return
	}
	c.recordAdmin(r, "secrets:delete:"+name, "applied")
	writeJSON(w, http.StatusOK, map[string]any{"deleted": deleted, "name": name})
}
