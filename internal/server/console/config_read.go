package console

import "net/http"

// configGuard enforces GET + an available ConfigSource. It returns the source
// and true when the request may proceed; otherwise it writes the response and
// returns false.
func (c *Console) configGuard(w http.ResponseWriter, r *http.Request) (ConfigSource, bool) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "only GET is supported"})
		return nil, false
	}
	if c.cfg.Config == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "configuration views are not available"})
		return nil, false
	}
	return c.cfg.Config, true
}

func (c *Console) handleConfigServer(w http.ResponseWriter, r *http.Request) {
	src, ok := c.configGuard(w, r)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, src.Server())
}

func (c *Console) handleConfigPathways(w http.ResponseWriter, r *http.Request) {
	src, ok := c.configGuard(w, r)
	if !ok {
		return
	}
	v, err := src.Pathways()
	if err != nil {
		c.configError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

func (c *Console) handleConfigCredentials(w http.ResponseWriter, r *http.Request) {
	_, ok := c.configGuard(w, r)
	if !ok {
		return
	}
	// Community UI keeps credential-reference editing off the console surface.
	// Keep this endpoint explicit (404) rather than falling through to static.
	writeJSON(w, http.StatusNotFound, map[string]string{"error": "credentials view is not available in this console edition"})
}

func (c *Console) handleConfigCallers(w http.ResponseWriter, r *http.Request) {
	src, ok := c.configGuard(w, r)
	if !ok {
		return
	}
	v, err := src.Callers()
	if err != nil {
		c.configError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

func (c *Console) handleConfigPermeability(w http.ResponseWriter, r *http.Request) {
	src, ok := c.configGuard(w, r)
	if !ok {
		return
	}
	v, err := src.Permeability()
	if err != nil {
		c.configError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

// configError reports a config read failure. The ConfigSource errors are
// parse/validation errors from the daemon's own loaders — already secret-free —
// so they are surfaced to help the operator fix the file.
func (c *Console) configError(w http.ResponseWriter, err error) {
	c.cfg.Logger.Warn("console config read failed", "error", err.Error())
	writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
}
