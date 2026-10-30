// Package transcription implements the logical /v1/audio/transcriptions
// (speech-to-text) handler: Admit, Authorize, Resolve, Scrub, Transporter with
// Egress Identity, then the inward Scrub. From the multipart request it keeps
// only the audio bytes and allowlisted form fields, dropping the client
// filename, extra parts, and part headers, and it enforces an upload size
// limit before any Provider is contacted.
package transcription

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/inferencepath/membrane/internal/exterior/providers"
	"github.com/inferencepath/membrane/internal/interior/audit"
	"github.com/inferencepath/membrane/internal/interior/resolve"
	"github.com/inferencepath/membrane/internal/membrane/scrub"
	"github.com/inferencepath/membrane/internal/membrane/transporter"
	"github.com/inferencepath/membrane/internal/observability/metrics"
	"github.com/inferencepath/membrane/internal/server/chat"
	"github.com/inferencepath/membrane/internal/types"
)

// Route is the path this handler serves.
const Route = "/v1/audio/transcriptions"

// DefaultMaxUploadBytes bounds the multipart upload size (25 MiB). Larger
// uploads are rejected before any Provider is contacted.
const DefaultMaxUploadBytes int64 = 25 << 20

// partMaxMemory is the in-memory multipart parse budget; larger parts spill to
// temp files, still bounded by the MaxBytesReader limit.
const partMaxMemory = 1 << 20

const (
	codeMethodNotAllowed    = "method_not_allowed"
	codeUnauthorized        = "unauthorized"
	codeForbidden           = "forbidden"
	codeBadRequest          = "bad_request"
	codeUnresolvedPathway   = "unresolved_pathway"
	codeTargetNotAllowed    = "target_not_allowed"
	codePayloadTooLarge     = "payload_too_large"
	codeInternal            = "internal"
	codeNoProviderAvailable = "no_provider_available"
	codeUpstreamTimeout     = "upstream_timeout"
	codeUpstreamUnavailable = "upstream_unavailable"
	codeProviderError       = "provider_error"
	codeUnavailable         = "unavailable"
)

// Handler orchestrates the audio_transcription Pathway over the active chat
// Snapshot.
type Handler struct {
	current  func() *chat.Snapshot
	logger   *slog.Logger
	metrics  metrics.Recorder
	audit    audit.Sink
	now      func() time.Time
	maxBytes int64
}

// HandlerOption configures a Handler's process-level dependencies.
type HandlerOption func(*Handler)

// WithAudit installs the Interior-only usage audit Sink.
func WithAudit(sink audit.Sink) HandlerOption {
	return func(h *Handler) {
		if sink != nil {
			h.audit = sink
		}
	}
}

// WithClock overrides the audit timestamp clock, for tests.
func WithClock(now func() time.Time) HandlerOption {
	return func(h *Handler) {
		if now != nil {
			h.now = now
		}
	}
}

// WithMaxUploadBytes overrides the default upload size limit.
func WithMaxUploadBytes(n int64) HandlerOption {
	return func(h *Handler) {
		if n > 0 {
			h.maxBytes = n
		}
	}
}

// NewReloadable constructs a transcription Handler that reads the active Snapshot
// from current on every request. A nil Recorder is replaced with a no-op.
func NewReloadable(current func() *chat.Snapshot, logger *slog.Logger, rec metrics.Recorder, opts ...HandlerOption) *Handler {
	if rec == nil {
		rec = metrics.Nop{}
	}
	h := &Handler{current: current, logger: logger, metrics: rec, audit: audit.Discard{}, now: time.Now, maxBytes: DefaultMaxUploadBytes}
	for _, opt := range opts {
		opt(h)
	}
	return h
}

func (h *Handler) observe(callerID string, capID types.PathwayID, outcome metrics.Outcome, latency time.Duration) {
	h.metrics.RecordChat(string(capID), false, outcome, latency, 0, 0, 0)
	h.audit.Write(audit.Record{
		SchemaVersion:  audit.SchemaVersion,
		Timestamp:      h.now(),
		CallerID:       callerID,
		PathwayID:      capID,
		Outcome:        string(outcome),
		DurationMillis: latency.Milliseconds(),
		Stream:         false,
	})
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	snap := h.current()

	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, codeMethodNotAllowed, "only POST is supported")
		return
	}
	if snap == nil {
		writeError(w, http.StatusServiceUnavailable, codeUnavailable, "configuration not ready")
		return
	}

	key, ok := bearerToken(r.Header.Get("Authorization"))
	if !ok {
		writeError(w, http.StatusUnauthorized, codeUnauthorized, "missing or malformed credentials")
		return
	}
	caller, err := snap.Callers.Verify(key)
	if err != nil {
		writeError(w, http.StatusUnauthorized, codeUnauthorized, "invalid credentials")
		return
	}

	// Oversize uploads fail closed with 413 before any Provider is contacted.
	r.Body = http.MaxBytesReader(w, r.Body, h.maxBytes)
	if err := r.ParseMultipartForm(partMaxMemory); err != nil {
		var mbErr *http.MaxBytesError
		if errors.As(err, &mbErr) {
			writeError(w, http.StatusRequestEntityTooLarge, codePayloadTooLarge, "upload exceeds the maximum allowed size")
			return
		}
		writeError(w, http.StatusBadRequest, codeBadRequest, "malformed multipart request")
		return
	}

	pathway := r.FormValue("pathway")
	if pathway == "" {
		writeError(w, http.StatusBadRequest, codeBadRequest, "pathway is required")
		return
	}
	reveal := r.FormValue("reveal") == "true" || r.URL.Query().Get("reveal") == "true"

	file, _, ferr := r.FormFile("file")
	if ferr != nil {
		writeError(w, http.StatusBadRequest, codeBadRequest, "file is required")
		return
	}
	defer func() { _ = file.Close() }()
	fileBytes, rerr := io.ReadAll(file)
	if rerr != nil {
		var mbErr *http.MaxBytesError
		if errors.As(rerr, &mbErr) {
			writeError(w, http.StatusRequestEntityTooLarge, codePayloadTooLarge, "upload exceeds the maximum allowed size")
			return
		}
		writeError(w, http.StatusBadRequest, codeBadRequest, "unreadable file part")
		return
	}
	if len(fileBytes) == 0 {
		writeError(w, http.StatusBadRequest, codeBadRequest, "file is required")
		return
	}

	capID := types.PathwayID(pathway)

	if snap.Authorizer != nil {
		authErr := snap.Authorizer.Authorize(caller, capID)
		if az, ok := h.metrics.(metrics.AuthzObserver); ok {
			az.ObserveAuthorization(string(capID), authErr == nil)
		}
		if authErr != nil {
			writeError(w, http.StatusForbidden, codeForbidden, "not authorized for the requested pathway")
			return
		}
	}

	if _, ok := snap.Resolver.Resolve(capID); !ok {
		writeError(w, http.StatusBadRequest, codeUnresolvedPathway, "no binding for the requested pathway")
		return
	}

	if op, ok := snap.Resolver.Operation(capID); !ok || op != types.OperationAudioTranscription {
		writeError(w, http.StatusBadRequest, codeUnresolvedPathway, "no binding for the requested pathway")
		return
	}

	plan, err := snap.Resolver.Plan(capID, nil)
	if err != nil {
		h.logger.Error("plan failed", "pathway", string(capID), "error", err.Error())
		writeError(w, http.StatusInternalServerError, codeInternal, "internal error")
		return
	}
	tiers := planTiers(plan)

	// The canonical payload holds the audio bytes and any allowlisted form
	// fields. The Scrub rebuilds the egress multipart from the Pathway's
	// outbound allowlist, so the client filename never crosses.
	payload := map[string]any{"file": fileBytes}
	for _, field := range []string{"language", "prompt", "response_format"} {
		if v := r.FormValue(field); v != "" {
			payload[field] = v
		}
	}
	if v := r.FormValue("temperature"); v != "" {
		payload["temperature"] = v
	}
	canonical := types.CanonicalRequest{PathwayID: capID, Payload: payload, Reveal: reveal, Caller: caller}
	sr, err := scrub.Sanitize(canonical, snap.Permeability)
	if err != nil {
		h.logger.Error("scrub failed", "pathway", string(capID), "error", err.Error())
		writeError(w, http.StatusInternalServerError, codeInternal, "internal error")
		return
	}

	tt, ok := snap.Transport.(transporter.TranscriptionTransporter)
	if !ok {
		writeError(w, http.StatusServiceUnavailable, codeUnavailable, "transcription is not available")
		return
	}

	ctx := transporter.WithPlan(r.Context(), tiers)
	start := time.Now()
	resp, err := tt.TransportTranscription(ctx, sr)
	if err != nil {
		h.observe(caller.ID, capID, outcomeFor(err), time.Since(start))
		h.writeTransportError(w, err, reveal)
		return
	}

	scrubbed, err := scrub.ScrubTranscription(resp, snap.Permeability, capID, reveal)
	if err != nil {
		h.observe(caller.ID, capID, metrics.OutcomeInternalError, time.Since(start))
		h.logger.Error("inward scrub failed", "pathway", string(capID), "error", err.Error())
		writeError(w, http.StatusInternalServerError, codeInternal, "internal error")
		return
	}

	h.observe(caller.ID, capID, metrics.OutcomeSuccess, time.Since(start))
	out := types.TranscriptionResponse{
		Pathway:  string(capID),
		Text:     scrubbed.Text,
		Segments: scrubbed.Segments,
		Usage:    scrubbed.Usage,
		Provider: string(scrubbed.Provider),
		Model:    scrubbed.Model,
	}
	writeJSON(w, http.StatusOK, out)
}

func planTiers(plan []resolve.PlanTarget) []transporter.PlanTier {
	if len(plan) == 0 {
		return nil
	}
	tiers := make([]transporter.PlanTier, len(plan))
	for i, p := range plan {
		tiers[i] = transporter.PlanTier{Provider: p.Provider, Model: p.Model}
	}
	return tiers
}

func bearerToken(header string) (string, bool) {
	const prefix = "Bearer "
	if len(header) <= len(prefix) || !strings.EqualFold(header[:len(prefix)], prefix) {
		return "", false
	}
	token := strings.TrimSpace(header[len(prefix):])
	if token == "" {
		return "", false
	}
	return token, true
}

// Provider-neutral error mapping, as in the chat handler.

type neutralError struct {
	status  int
	code    string
	message string
}

func mapError(err error, reveal bool) neutralError {
	if errors.Is(err, transporter.ErrNoProvider) {
		return neutralError{http.StatusServiceUnavailable, codeNoProviderAvailable,
			"no provider is currently available for this pathway"}
	}
	if errors.Is(err, providers.ErrOperationUnsupported) {
		return neutralError{http.StatusInternalServerError, codeInternal, "internal error"}
	}
	var ge neutralError
	switch providers.ClassifyFailure(err) {
	case providers.FailureTimeout:
		ge = neutralError{http.StatusGatewayTimeout, codeUpstreamTimeout, "the upstream provider did not respond in time"}
	case providers.FailureUnavailable:
		ge = neutralError{http.StatusGatewayTimeout, codeUpstreamUnavailable, "the upstream provider is temporarily unavailable"}
	default:
		ge = neutralError{http.StatusBadGateway, codeProviderError, "the upstream provider returned an error"}
	}
	if reveal {
		if p := providers.RevealableProvider(err); p != "" {
			ge.message += " (provider: " + string(p) + ")"
		}
	}
	return ge
}

func outcomeFor(err error) metrics.Outcome {
	if errors.Is(err, transporter.ErrNoProvider) || errors.Is(err, providers.ErrOperationUnsupported) {
		return metrics.OutcomeInternalError
	}
	switch providers.ClassifyFailure(err) {
	case providers.FailureTimeout, providers.FailureUnavailable:
		return metrics.OutcomeTimeout
	default:
		return metrics.OutcomeUpstreamError
	}
}

func (h *Handler) writeTransportError(w http.ResponseWriter, err error, reveal bool) {
	ge := mapError(err, reveal)
	if ge.status >= 500 && ge.code != codeProviderError && ge.code != codeUpstreamUnavailable {
		h.logger.Error("transport error", "code", ge.code, "error", err.Error())
	}
	writeError(w, ge.status, ge.code, ge.message)
}

type errorBody struct {
	Error errorDetail `json:"error"`
}

type errorDetail struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, errorBody{Error: errorDetail{Code: code, Message: message}})
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}
