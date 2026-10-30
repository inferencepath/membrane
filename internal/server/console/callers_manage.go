package console

import (
	"encoding/json"
	"net/http"
)

// callersGuard enforces an available CallerManager, else writes 503.
func (c *Console) callersGuard(w http.ResponseWriter) (CallerManager, bool) {
	if c.cfg.Callers == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{
			"error": "caller management is not available (requires managed/reloadable mode)",
		})
		return nil, false
	}
	return c.cfg.Callers, true
}

// handleCallerCreate creates or updates a Caller Key. Only the secret's hash is
// persisted, and the response carries no secret.
func (c *Console) handleCallerCreate(w http.ResponseWriter, r *http.Request) {
	mgr, ok := c.callersGuard(w)
	if !ok {
		return
	}
	raw, ok := readEditBody(w, r)
	if !ok {
		return
	}
	var body CallerInput
	if err := json.Unmarshal(raw, &body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "malformed request body"})
		return
	}
	if body.KeyID == "" || body.CallerID == "" || body.Secret == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "keyId, callerId, and secret are required"})
		return
	}
	res := mgr.CreateCaller(body)
	c.recordAdmin(r, "callers:create:"+body.KeyID, applyOutcome(res))
	writeJSON(w, http.StatusOK, res)
}

// handleCallerDelete removes a Caller Key by its public key id.
func (c *Console) handleCallerDelete(w http.ResponseWriter, r *http.Request) {
	mgr, ok := c.callersGuard(w)
	if !ok {
		return
	}
	keyID := r.PathValue("keyId")
	deleted, err := mgr.DeleteCaller(keyID)
	if err != nil {
		c.recordAdmin(r, "callers:delete:"+keyID, "rejected")
		writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
		return
	}
	c.recordAdmin(r, "callers:delete:"+keyID, "applied")
	writeJSON(w, http.StatusOK, map[string]any{"deleted": deleted, "keyId": keyID})
}
