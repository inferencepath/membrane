package console

import (
	"encoding/json"
	"io"
	"net/http"
)

// maxEditBytes caps the size of a config edit body.
const maxEditBytes = 1 << 20 // 1 MiB

// editGuard checks for a ConfigEditor and a known kind. It returns false after
// writing the error response when the request cannot proceed.
func (c *Console) editGuard(w http.ResponseWriter, r *http.Request) (ConfigEditor, string, bool) {
	if c.cfg.Editor == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{
			"error": "config editing is not available (requires config-as-code/reloadable mode)",
		})
		return nil, "", false
	}
	kind := r.PathValue("kind")
	if kind == "credentials" {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "credentials editing is not available in this console"})
		return nil, "", false
	}
	if !editorKnows(c.cfg.Editor, kind) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "unknown config kind"})
		return nil, "", false
	}
	return c.cfg.Editor, kind, true
}

func (c *Console) handleEditRead(w http.ResponseWriter, r *http.Request) {
	editor, kind, ok := c.editGuard(w, r)
	if !ok {
		return
	}
	f, err := editor.Read(kind)
	if err != nil {
		c.configError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, f)
}

func (c *Console) handleEditValidate(w http.ResponseWriter, r *http.Request) {
	editor, kind, ok := c.editGuard(w, r)
	if !ok {
		return
	}
	raw, ok := readEditBody(w, r)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, editor.Validate(kind, raw))
}

// applyBody is the apply request payload: the new raw content and the version
// token the operator loaded.
type applyBody struct {
	Raw     string `json:"raw"`
	Version string `json:"version"`
}

func (c *Console) handleEditApply(w http.ResponseWriter, r *http.Request) {
	editor, kind, ok := c.editGuard(w, r)
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
	res := editor.Apply(kind, []byte(ab.Raw), ab.Version)
	c.recordAdmin(r, "apply:"+kind, applyOutcome(res))
	writeJSON(w, http.StatusOK, res)
}

// handleEditFormValidate renders a structured form (JSON body) into the kind's
// YAML and validates it, without writing.
func (c *Console) handleEditFormValidate(w http.ResponseWriter, r *http.Request) {
	editor, kind, ok := c.editGuard(w, r)
	if !ok {
		return
	}
	form, ok := readEditBody(w, r)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, editor.ValidateForm(kind, form))
}

// formApplyBody is the form apply payload: the form model and the version
// token the operator loaded.
type formApplyBody struct {
	Form    json.RawMessage `json:"form"`
	Version string          `json:"version"`
}

// handleEditFormApply renders a structured form, validates it, checks the
// version, writes atomically, and reloads.
func (c *Console) handleEditFormApply(w http.ResponseWriter, r *http.Request) {
	editor, kind, ok := c.editGuard(w, r)
	if !ok {
		return
	}
	body, ok := readEditBody(w, r)
	if !ok {
		return
	}
	var fb formApplyBody
	if err := json.Unmarshal(body, &fb); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "malformed apply body"})
		return
	}
	res := editor.ApplyForm(kind, fb.Form, fb.Version)
	c.recordAdmin(r, "apply-form:"+kind, applyOutcome(res))
	writeJSON(w, http.StatusOK, res)
}

// readEditBody reads a size-capped request body. For validate the body is the
// raw YAML; for apply it is a JSON object.
func readEditBody(w http.ResponseWriter, r *http.Request) ([]byte, bool) {
	b, err := io.ReadAll(io.LimitReader(r.Body, maxEditBytes))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "failed to read request body"})
		return nil, false
	}
	return b, true
}
