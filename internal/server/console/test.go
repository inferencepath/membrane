package console

import (
	"net/http"
	"strconv"
)

const (
	testCallerKeyHeader = "X-Membrane-Console-Caller-Key"
	maxTestBodyBytes    = 52 << 20 // accommodates base64-encoded multimodal media
)

// testRoutes is deliberately closed: Console test requests can reach only a
// Community data-plane operation, never an arbitrary local URL or path.
var testRoutes = map[string]string{
	"chat":                "/v1/chat",
	"embeddings":          "/v1/embeddings",
	"rerank":              "/v1/rerank",
	"responses":           "/v1/responses",
	"image_generation":    "/v1/images/generations",
	"audio_transcription": "/v1/audio/transcriptions",
	"audio_speech":        "/v1/audio/speech",
	"audio_conversation":  "/v1/audio/conversations",
	"moderations":         "/v1/moderations",
	"multimodal_chat":     "/v1/multimodal/chat",
}

// handleTestPathway proxies a raw operation request to the corresponding
// Community data-plane route. The Caller Key is supplied in a Console-only
// header and is never forwarded as a request field, stored, or logged. This
// supports both JSON operations and the multipart transcription upload.
func (c *Console) handleTestPathway(w http.ResponseWriter, r *http.Request) {
	if c.cfg.ChatBaseURL == "" {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{
			"error": "test pathway is not available (data-plane address unknown)",
		})
		return
	}
	operation := r.PathValue("operation")
	route, ok := testRoutes[operation]
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "unknown Community operation"})
		return
	}
	callerKey := r.Header.Get(testCallerKeyHeader)
	if callerKey == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "a Caller Key is required"})
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxTestBodyBytes)

	up, err := http.NewRequestWithContext(r.Context(), http.MethodPost,
		c.cfg.ChatBaseURL+route, r.Body)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not build upstream request"})
		return
	}
	if contentType := r.Header.Get("Content-Type"); contentType != "" {
		up.Header.Set("Content-Type", contentType)
	}
	up.Header.Set("Authorization", "Bearer "+callerKey)

	// No client timeout, since streams may be long-lived; the request context
	// handles cancellation.
	resp, err := http.DefaultClient.Do(up)
	if err != nil {
		c.recordAdmin(r, "test:"+operation, "error")
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "data-plane request failed"})
		return
	}
	defer resp.Body.Close()

	c.recordAdmin(r, "test:"+operation, "http_"+strconv.Itoa(resp.StatusCode))

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
