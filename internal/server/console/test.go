package console

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strconv"
)

// testChatRequest is the operator's Test Pathway payload: a Caller Key and
// the raw logical chat request (pathway/messages/…/reveal/stream). The Caller
// Key is operator-supplied and forwarded verbatim; the console never stores or
// logs it.
type testChatRequest struct {
	CallerKey string          `json:"callerKey"`
	Request   json.RawMessage `json:"request"`
}

// handleTestChat proxies a sample request to the data-plane /v1/chat using an
// operator-supplied Caller Key. The console acts purely as a client of
// the egress path — it never constructs a SanitizedRequest, never mounts the
// egress handler, and relays the upstream response verbatim (including the
// generic 403 for an unauthorized key and provider-neutral errors). Reveal and
// streaming are honored by the data plane; the console just streams bytes
// through. The admin-action audit records only who/what/when (never the key).
func (c *Console) handleTestChat(w http.ResponseWriter, r *http.Request) {
	if c.cfg.ChatBaseURL == "" {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{
			"error": "test pathway is not available (data-plane address unknown)",
		})
		return
	}
	body, ok := readEditBody(w, r)
	if !ok {
		return
	}
	var tr testChatRequest
	if err := json.Unmarshal(body, &tr); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "malformed test request"})
		return
	}
	if tr.CallerKey == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "a Caller Key is required"})
		return
	}
	if len(tr.Request) == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "a chat request is required"})
		return
	}

	up, err := http.NewRequestWithContext(r.Context(), http.MethodPost,
		c.cfg.ChatBaseURL+"/v1/chat", bytes.NewReader(tr.Request))
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not build upstream request"})
		return
	}
	up.Header.Set("Content-Type", "application/json")
	up.Header.Set("Authorization", "Bearer "+tr.CallerKey)

	// No client Timeout: streaming responses may be long-lived; cancellation is
	// bound to the request context instead.
	resp, err := http.DefaultClient.Do(up)
	if err != nil {
		c.recordAdmin(r, "test:chat", "error")
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "data-plane request failed"})
		return
	}
	defer resp.Body.Close()

	c.recordAdmin(r, "test:chat", "http_"+strconv.Itoa(resp.StatusCode))

	if ct := resp.Header.Get("Content-Type"); ct != "" {
		w.Header().Set("Content-Type", ct)
	}
	w.WriteHeader(resp.StatusCode)

	flusher, _ := w.(http.Flusher)
	buf := make([]byte, 4096)
	for {
		n, rerr := resp.Body.Read(buf)
		if n > 0 {
			if _, werr := w.Write(buf[:n]); werr != nil {
				return
			}
			if flusher != nil {
				flusher.Flush()
			}
		}
		if rerr != nil {
			return
		}
	}
}
