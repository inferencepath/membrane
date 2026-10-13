package console

import (
	"encoding/json"
	"net/http"
)

// handlePathwayCreate atomically creates (or updates) a pathway together with its
// permeability policy in one transaction, so a new pathway is never left in an
// invalid intermediate state (pathway without policy, or policy without pathway).
// The response is secret-free.
func (c *Console) handlePathwayCreate(w http.ResponseWriter, r *http.Request) {
	if c.cfg.PathwayCreator == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{
			"error": "pathway creation is not available (requires managed/reloadable mode)",
		})
		return
	}
	raw, ok := readEditBody(w, r)
	if !ok {
		return
	}
	var body PathwayWizardInput
	if err := json.Unmarshal(raw, &body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "malformed request body"})
		return
	}
	res := c.cfg.PathwayCreator.CreatePathway(body)
	c.recordAdmin(r, "pathways:create:"+body.Pathway, applyOutcome(res))
	writeJSON(w, http.StatusOK, res)
}
