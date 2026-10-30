package console

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strconv"
)

// testChatRequest is the Test Pathway payload: a Caller Key and the raw logical
// chat request. The key is forwarded as given and never stored or logged.
type testChatRequest struct {
	CallerKey string          `json:"callerKey"`
	Request   json.RawMessage `json:"request"`
}

// handleTestChat proxies a sample request to the data-plane /v1/chat with an
// operator-supplied Caller Key. The console is an ordinary client of the
// egress path: it relays the response as is, including the generic 403 and
// streamed bodies. The admin audit records the action but not the key.
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

	// No client timeout, since streams may be long-lived; the request context
	// handles cancellation.
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
