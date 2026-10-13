package console

import (
	"encoding/json"
	"net/http"
)

// This file implements the console's sealed-secret endpoints over the
// SecretManager port (defined in secrets_views.go). Values are write-only: an
// operator may set/rotate or delete a secret, but no handler ever returns a
// plaintext value — reads surface metadata only.

// secretsGuard enforces an available SecretManager. A nil manager means the
// sealed store is disabled (no master key configured), so secret management from
// the Console is unavailable and every secrets endpoint returns 503.
func (c *Console) secretsGuard(w http.ResponseWriter) (SecretManager, bool) {
	if c.cfg.Secrets == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{
			"error": "secret management is not available (set secrets.master_key_ref)",
		})
		return nil, false
	}
	return c.cfg.Secrets, true
}

// handleSecretsList serves sealed-secret metadata (names, versions, timestamps,
// and whether a credential references each). It never returns a value.
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

// secretSetInput is the write-only body for setting/rotating a secret value.
type secretSetInput struct {
	Value string `json:"value"`
}

// handleSecretSet creates or rotates the sealed secret named by the path. The
// value is accepted but never echoed; the response is secret-free.
func (c *Console) handleSecretSet(w http.ResponseWriter, r *http.Request) {
	mgr, ok := c.secretsGuard(w)
	if !ok {
		return
	}
	raw, ok := readEditBody(w, r)
	if !ok {
		return
	}
	var body secretSetInput
	if err := json.Unmarshal(raw, &body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "malformed request body"})
		return
	}
	name := r.PathValue("name")
	if err := mgr.Set(name, body.Value); err != nil {
		c.recordAdmin(r, "secrets:set:"+name, "rejected")
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	c.recordAdmin(r, "secrets:set:"+name, "applied")
	writeJSON(w, http.StatusOK, map[string]any{"set": true, "name": name})
}

// handleSecretDelete removes a sealed secret. A secret still referenced by a
// credential is refused (409) so a credential's `secret:<name>` never dangles.
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
