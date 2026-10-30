package console

import (
	"net/http"
	"strings"
)

// PanelDescriptor describes one extension panel in the Console nav. The core
// knows nothing about what the panel manages.
type PanelDescriptor struct {
	// ID is the stable panel identifier used in the nav route and API path.
	ID string `json:"id"`
	// Title is the human label shown in the nav and panel header.
	Title string `json:"title"`
	// Group is the nav section header the panel appears under (e.g. "Extensions").
	Group string `json:"group"`
	// Order sorts panels within a group (ascending).
	Order int `json:"order"`
}

// PanelProvider contributes extension panels. The core lists their
// descriptors and forwards each panel's API under /api/console/ext/{id}/; the
// provider owns its HTML view, validation, persistence, and reload.
//
// A PanelProvider never receives a SanitizedRequest and, like the rest of the
// Console, must not expose request content or secrets.
type PanelProvider interface {
	// Panels returns the panels this provider contributes.
	Panels() []PanelDescriptor
	// ServePanel handles a request for panel id. rest is the sub-path after
	// /api/console/ext/{id}/, or "" for the panel root.
	ServePanel(id, rest string, w http.ResponseWriter, r *http.Request)
}

// handlePanels lists the extension panels. It always returns 200, possibly
// with an empty list, so the SPA can render unconditionally.
func (c *Console) handlePanels(w http.ResponseWriter, r *http.Request) {
	panels := []PanelDescriptor{}
	if c.cfg.Panels != nil {
		panels = c.cfg.Panels.Panels()
	}
	writeJSON(w, http.StatusOK, map[string]any{"panels": panels})
}

// handleExt forwards a request to the provider for a known panel ID. With no
// provider it returns 503, and an unknown ID returns 404. Non-GET requests are
// recorded in the admin audit.
func (c *Console) handleExt(w http.ResponseWriter, r *http.Request) {
	if c.cfg.Panels == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "no extension panels available"})
		return
	}
	id := r.PathValue("id")
	if !c.panelKnown(id) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "unknown panel"})
		return
	}
	rest := strings.Trim(r.PathValue("rest"), "/")
	if r.Method != http.MethodGet {
		c.recordAdmin(r, "panel:"+id+"/"+rest, r.Method)
	}
	c.cfg.Panels.ServePanel(id, rest, w, r)
}

func (c *Console) panelKnown(id string) bool {
	for _, p := range c.cfg.Panels.Panels() {
		if p.ID == id {
			return true
		}
	}
	return false
}
