// Package responses implements the logical /v1/responses handler: Admit →
// Authorize → Resolve (operation-gated) → Scrub → Transporter (+ Egress
// Identity) → inward Scrub, for both non-streaming and streaming (SSE) responses.
// Like the chat handler, no endpoint here accepts or emits provider endpoints,
// credentials, or caller identity on the egress side.
//
// It reuses the chat Snapshot (the per-request, immutable config bundle) since
// that bundle is operation-agnostic; only the request/response shape and the
// responses-specific transport capability differ.
package responses

import (
	"encoding/json"
	"errors"
	"fmt"
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
const Route = "/v1/responses"

// Stable, machine-handleable error codes, mirroring the chat handler's
// provider-neutral error contract.
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

// Handler orchestrates the responses Pathway over the active chat Snapshot.
type Handler struct {
	current func() *chat.Snapshot
	logger  *slog.Logger
	metrics metrics.Recorder
	audit   audit.Sink
	now     func() time.Time
}

// HandlerOption configures process-level Handler dependencies (not per-config).
type HandlerOption func(*Handler)

// WithAudit installs the Interior-only usage audit Sink. When unset, audit
// records are discarded.
func WithAudit(sink audit.Sink) HandlerOption {
	return func(h *Handler) {
		if sink != nil {
			h.audit = sink
		}
	}
}

// WithClock overrides the audit timestamp clock (for deterministic tests).
func WithClock(now func() time.Time) HandlerOption {
	return func(h *Handler) {
		if now != nil {
			h.now = now
		}
	}
}

// NewReloadable constructs a responses Handler that reads the active Snapshot
// from current on every request. A nil Recorder is replaced with a no-op.
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

// observe records one served request to the identity-free metrics and the
// Interior-only audit Sink. Responses reuse the Pathway-keyed chat recorder; the
// stream flag distinguishes streaming requests.
func (h *Handler) observe(callerID string, capID types.PathwayID, stream bool, outcome metrics.Outcome, latency time.Duration, input, output, total int) {
	h.metrics.RecordChat(string(capID), stream, outcome, latency, input, output, total)
	h.audit.Write(audit.Record{
		SchemaVersion:    audit.SchemaVersion,
		Timestamp:        h.now(),
		CallerID:         callerID,
		PathwayID:        capID,
		Outcome:          string(outcome),
		DurationMillis:   latency.Milliseconds(),
		PromptTokens:     input,
		CompletionTokens: output,
		TotalTokens:      total,
		Stream:           stream,
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

	var req types.ResponsesRequest
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, codeBadRequest, "malformed request body")
		return
	}
	if req.Pathway == "" || !hasInput(req.Input) {
		writeError(w, http.StatusBadRequest, codeBadRequest, "pathway and input are required")
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

	// Route-dispatch guard: /v1/responses serves only Pathways whose
	// operation is `responses`. Any other operation is reported with the same
	// generic unresolved_pathway error — no operation or Pathway enumeration oracle.
	if op, ok := snap.Resolver.Operation(capID); !ok || op != types.OperationResponses {
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

	canonical := buildCanonical(capID, req, caller)
	sr, err := scrub.Sanitize(canonical, snap.Permeability)
	if err != nil {
		h.logger.Error("scrub failed", "pathway", string(capID), "error", err.Error())
		writeError(w, http.StatusInternalServerError, codeInternal, "internal error")
		return
	}

	rt, ok := snap.Transport.(transporter.ResponsesTransporter)
	if !ok {
		writeError(w, http.StatusServiceUnavailable, codeUnavailable, "responses are not available")
		return
	}

	if sr.Stream() {
		h.serveStream(w, r, snap, rt, sr, capID, caller.ID, req.Reveal, tiers)
		return
	}
	h.serveUnary(w, r, snap, rt, sr, capID, caller.ID, req.Reveal, tiers)
}

func (h *Handler) serveUnary(w http.ResponseWriter, r *http.Request, snap *chat.Snapshot, rt transporter.ResponsesTransporter, sr scrub.SanitizedRequest, capID types.PathwayID, callerID string, reveal bool, tiers []transporter.PlanTier) {
	ctx := transporter.WithPlan(r.Context(), tiers)
	start := time.Now()
	resp, err := rt.TransportResponses(ctx, sr)
	if err != nil {
		h.observe(callerID, capID, false, outcomeFor(err), time.Since(start), 0, 0, 0)
		h.writeTransportError(w, err, reveal)
		return
	}
	scrubbed, err := scrub.ScrubResponses(resp, snap.Permeability, capID, reveal)
	if err != nil {
		h.observe(callerID, capID, false, metrics.OutcomeInternalError, time.Since(start), 0, 0, 0)
		h.logger.Error("inward scrub failed", "pathway", string(capID), "error", err.Error())
		writeError(w, http.StatusInternalServerError, codeInternal, "internal error")
		return
	}
	h.observe(callerID, capID, false, metrics.OutcomeSuccess, time.Since(start),
		scrubbed.Usage.InputTokens, scrubbed.Usage.OutputTokens, scrubbed.Usage.TotalTokens)
	out := types.ResponsesResponse{
		Pathway:      string(capID),
		Output:       scrubbed.Output,
		FinishReason: scrubbed.FinishReason,
		Usage:        scrubbed.Usage,
		Provider:     string(scrubbed.Provider),
		Model:        scrubbed.Model,
	}
	if out.Output == nil {
		out.Output = []map[string]any{}
	}
	writeJSON(w, http.StatusOK, out)
}

// sseEvent is the Caller-facing streaming event shape.
type sseEvent struct {
	Type  string         `json:"type"`
	Delta string         `json:"delta,omitempty"`
	Item  map[string]any `json:"item,omitempty"`
}

func (h *Handler) serveStream(w http.ResponseWriter, r *http.Request, snap *chat.Snapshot, rt transporter.ResponsesTransporter, sr scrub.SanitizedRequest, capID types.PathwayID, callerID string, reveal bool, tiers []transporter.PlanTier) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, codeInternal, "streaming unsupported")
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	wroteAny := false
	emit := func(chunk types.ResponsesStreamChunk) error {
		if chunk.Done {
			if _, err := io.WriteString(w, "data: [DONE]\n\n"); err != nil {
				return err
			}
			flusher.Flush()
			return nil
		}
		b, err := json.Marshal(sseEvent{Type: chunk.Type, Delta: chunk.Delta, Item: chunk.Item})
		if err != nil {
			return err
		}
		if _, err := fmt.Fprintf(w, "data: %s\n\n", b); err != nil {
			return err
		}
		wroteAny = true
		flusher.Flush()
		return nil
	}

	ctx := transporter.WithPlan(r.Context(), tiers)
	start := time.Now()
	if err := rt.TransportResponsesStream(ctx, sr, emit); err != nil {
		h.observe(callerID, capID, true, outcomeFor(err), time.Since(start), 0, 0, 0)
		if !wroteAny {
			h.writeTransportError(w, err, reveal)
			return
		}
		h.logger.Warn("stream terminated with error", "pathway", string(capID), "error", err.Error())
		_, _ = fmt.Fprintf(w, "event: error\ndata: {\"error\":{\"code\":%q,\"message\":\"stream terminated\"}}\n\n", codeStreamError)
		flusher.Flush()
		return
	}
	h.observe(callerID, capID, true, metrics.OutcomeSuccess, time.Since(start), 0, 0, 0)
}

// hasInput reports whether a decoded responses input is present and non-empty.
// Input may be a string prompt or an array of structured input items.
func hasInput(in any) bool {
	switch v := in.(type) {
	case nil:
		return false
	case string:
		return v != ""
	case []any:
		return len(v) > 0
	default:
		return true
	}
}

// planTiers converts an Interior resolve plan into the Membrane's routing plan.
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

// buildCanonical assembles the provider-agnostic CanonicalRequest from the
// responses request. Only the outbound-permeable fields are carried; the Scrub
// then rebuilds the egress payload from the Pathway's outbound allowlist (which
// drops stateful fields like store/previous_response_id and the metadata
// denylist unless explicitly allowlisted).
func buildCanonical(capID types.PathwayID, req types.ResponsesRequest, caller types.Caller) types.CanonicalRequest {
	payload := map[string]any{"input": req.Input}
	if req.Instructions != "" {
		payload["instructions"] = req.Instructions
	}
	if len(req.Tools) > 0 {
		tools := make([]any, len(req.Tools))
		for i, t := range req.Tools {
			tools[i] = t
		}
		payload["tools"] = tools
	}
	if req.ToolChoice != nil {
		payload["tool_choice"] = req.ToolChoice
	}
	if req.Temperature != nil {
		payload["temperature"] = *req.Temperature
	}
	if req.MaxOutputTokens != nil {
		payload["max_output_tokens"] = *req.MaxOutputTokens
	}
	if req.TopP != nil {
		payload["top_p"] = *req.TopP
	}
	payload["stream"] = req.Stream
	return types.CanonicalRequest{
		PathwayID: capID,
		Payload:   payload,
		Reveal:    req.Reveal,
		Caller:    caller,
	}
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

// --- provider-neutral error mapping (mirrors the chat handler) ---

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
		ge = neutralError{http.StatusGatewayTimeout, codeUpstreamTimeout,
			"the upstream provider did not respond in time"}
	case providers.FailureUnavailable:
		ge = neutralError{http.StatusGatewayTimeout, codeUpstreamUnavailable,
			"the upstream provider is temporarily unavailable"}
	default:
		ge = neutralError{http.StatusBadGateway, codeProviderError,
			"the upstream provider returned an error"}
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
