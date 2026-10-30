// Package speech implements the logical /v1/audio/speech (text-to-speech)
// handler: Admit, Authorize, Resolve, Scrub, Transporter with Egress Identity,
// then the inward relay. The binary audio body is relayed incrementally; on
// Reveal, provider and model go in response headers, never in the audio.
package speech

import (
	"encoding/json"
	"errors"
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
const Route = "/v1/audio/speech"

// Reveal response headers, set only when the Caller requests Reveal and the
// inbound on_reveal set permits the field.
const (
	headerProvider = "X-InferencePath-Provider"
	headerModel    = "X-InferencePath-Model"
)

const (
	codeMethodNotAllowed    = "method_not_allowed"
	codeUnauthorized        = "unauthorized"
	codeForbidden           = "forbidden"
	codeBadRequest          = "bad_request"
	codeUnresolvedPathway   = "unresolved_pathway"
	codeTargetNotAllowed    = "target_not_allowed"
	codeInternal            = "internal"
	codeNoProviderAvailable = "no_provider_available"
	codeUpstreamTimeout     = "upstream_timeout"
	codeUpstreamUnavailable = "upstream_unavailable"
	codeProviderError       = "provider_error"
	codeStreamError         = "stream_error"
	codeUnavailable         = "unavailable"
)

// Handler orchestrates the audio_speech Pathway over the active chat Snapshot.
type Handler struct {
	current func() *chat.Snapshot
	logger  *slog.Logger
	metrics metrics.Recorder
	audit   audit.Sink
	now     func() time.Time
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

// NewReloadable constructs a speech Handler that reads the active Snapshot from
// current on every request. A nil Recorder is replaced with a no-op.
func NewReloadable(current func() *chat.Snapshot, logger *slog.Logger, rec metrics.Recorder, opts ...HandlerOption) *Handler {
	if rec == nil {
		rec = metrics.Nop{}
	}
	h := &Handler{current: current, logger: logger, metrics: rec, audit: audit.Discard{}, now: time.Now}
	for _, opt := range opts {
		opt(h)
	}
	return h
}

func (h *Handler) observe(callerID string, capID types.PathwayID, outcome metrics.Outcome, latency time.Duration) {
	h.metrics.RecordChat(string(capID), true, outcome, latency, 0, 0, 0)
	h.audit.Write(audit.Record{
		SchemaVersion:  audit.SchemaVersion,
		Timestamp:      h.now(),
		CallerID:       callerID,
		PathwayID:      capID,
		Outcome:        string(outcome),
		DurationMillis: latency.Milliseconds(),
		Stream:         true,
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

	var req types.SpeechRequest
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, codeBadRequest, "malformed request body")
		return
	}
	if req.Pathway == "" || req.Input == "" || req.Voice == "" {
		writeError(w, http.StatusBadRequest, codeBadRequest, "pathway, input and voice are required")
		return
	}

	capID := types.PathwayID(req.Pathway)

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

	if op, ok := snap.Resolver.Operation(capID); !ok || op != types.OperationAudioSpeech {
		writeError(w, http.StatusBadRequest, codeUnresolvedPathway, "no binding for the requested pathway")
		return
	}

	plan, err := snap.Resolver.Plan(capID, req.Targets)
	if err != nil {
		if errors.Is(err, resolve.ErrTargetNotAllowed) {
			writeError(w, http.StatusBadRequest, codeTargetNotAllowed, err.Error())
			return
		}
		h.logger.Error("plan failed", "pathway", string(capID), "error", err.Error())
		writeError(w, http.StatusInternalServerError, codeInternal, "internal error")
		return
	}
	tiers := planTiers(plan)

	payload := map[string]any{"input": req.Input, "voice": req.Voice}
	if req.Format != "" {
		payload["format"] = req.Format
	}
	if req.Speed != nil {
		payload["speed"] = *req.Speed
	}
	canonical := types.CanonicalRequest{PathwayID: capID, Payload: payload, Reveal: req.Reveal, Caller: caller}
	sr, err := scrub.Sanitize(canonical, snap.Permeability)
	if err != nil {
		h.logger.Error("scrub failed", "pathway", string(capID), "error", err.Error())
		writeError(w, http.StatusInternalServerError, codeInternal, "internal error")
		return
	}

	st, ok := snap.Transport.(transporter.SpeechTransporter)
	if !ok {
		writeError(w, http.StatusServiceUnavailable, codeUnavailable, "speech synthesis is not available")
		return
	}

	flusher, _ := w.(http.Flusher)

	wroteHead := false
	emit := func(c types.SpeechChunk) error {
		switch {
		case c.Head:
			ct := c.ContentType
			if ct == "" {
				ct = "application/octet-stream"
			}
			w.Header().Set("Content-Type", ct)
			// Same Reveal gating as the Scrub helpers, but via headers because
			// the body is binary audio.
			if req.Reveal {
				if scrub.RevealAllows(snap.Permeability, capID, "provider") && c.Provider != "" {
					w.Header().Set(headerProvider, string(c.Provider))
				}
				if scrub.RevealAllows(snap.Permeability, capID, "model") && c.Model != "" {
					w.Header().Set(headerModel, c.Model)
				}
			}
			w.WriteHeader(http.StatusOK)
			wroteHead = true
			return nil
		case c.Done:
			if flusher != nil {
				flusher.Flush()
			}
			return nil
		default:
			if _, err := w.Write(c.Audio); err != nil {
				return err
			}
			if flusher != nil {
				flusher.Flush()
			}
			return nil
		}
	}

	ctx := transporter.WithPlan(r.Context(), tiers)
	start := time.Now()
	if err := st.TransportSpeech(ctx, sr, emit); err != nil {
		h.observe(caller.ID, capID, outcomeFor(err), time.Since(start))
		if !wroteHead {
			h.writeTransportError(w, err, req.Reveal)
			return
		}
		// The response has already started, so a JSON error cannot be sent.
		h.logger.Warn("speech stream terminated with error", "pathway", string(capID), "error", err.Error())
		return
	}
	h.observe(caller.ID, capID, metrics.OutcomeSuccess, time.Since(start))
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
