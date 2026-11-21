// Package conversation implements the logical /v1/audio/conversations
// (speech-to-speech) handler: Admit → Authorize → Resolve (operation-gated) →
// Scrub → Transporter (+ Egress Identity) → inward Scrub. The request is
// multipart/form-data carrying the spoken request; the response is JSON with the
// spoken reply (base64) and its transcript. Audio is held in memory for the one
// request only; it is never stored or logged.
package conversation

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/inferencepath/membrane/internal/exterior/providers"
	"github.com/inferencepath/membrane/internal/interior/audit"
	"github.com/inferencepath/membrane/internal/interior/resolve"
	"github.com/inferencepath/membrane/internal/membrane/scrub"
	"github.com/inferencepath/membrane/internal/membrane/transporter"
	"github.com/inferencepath/membrane/internal/observability/metrics"
	"github.com/inferencepath/membrane/internal/server/chat"
	"github.com/inferencepath/membrane/internal/server/pipeline"
	"github.com/inferencepath/membrane/internal/types"
)

// Route is the path this handler serves.
const Route = "/v1/audio/conversations"

// DefaultMaxUploadBytes bounds the multipart upload size (25 MiB), rejected
// fail-closed before any Provider contact.
const DefaultMaxUploadBytes int64 = 25 << 20

const partMaxMemory = 1 << 20

// maxInstructionChars bounds the optional system instructions.
const maxInstructionChars = 4096

// inputFormats are the spoken-request formats accepted (wav and mp3 are the
// formats audio-capable chat models take as input).
var inputFormats = map[string]bool{"wav": true, "mp3": true}

// replyFormats are the spoken-reply formats a caller may request.
var replyFormats = map[string]bool{"wav": true, "mp3": true, "flac": true, "opus": true, "aac": true, "pcm": true}

const (
	codeMethodNotAllowed    = "method_not_allowed"
	codeUnauthorized        = "unauthorized"
	codeForbidden           = "forbidden"
	codeBadRequest          = "bad_request"
	codeUnresolvedPathway   = "unresolved_pathway"
	codePayloadTooLarge     = "payload_too_large"
	codeInternal            = "internal"
	codeNoProviderAvailable = "no_provider_available"
	codeUpstreamTimeout     = "upstream_timeout"
	codeUpstreamUnavailable = "upstream_unavailable"
	codeProviderError       = "provider_error"
	codeInvalidParameter    = "invalid_parameter"
	codeUnavailable         = "unavailable"
)

// Handler orchestrates the audio_conversation Pathway over the active Snapshot.
type Handler struct {
	current  func() *chat.Snapshot
	logger   *slog.Logger
	metrics  metrics.Recorder
	audit    audit.Sink
	now      func() time.Time
	maxBytes int64
	stage    pipeline.ContentStage
	obs      pipeline.ObservationStage
}

// HandlerOption configures process-level Handler dependencies (not per-config).
type HandlerOption func(*Handler)

// WithAudit installs the Interior-only usage audit Sink.
func WithAudit(sink audit.Sink) HandlerOption {
	return func(h *Handler) {
		if sink != nil {
			h.audit = sink
		}
	}
}

// WithContentStage installs the content barrier (Enterprise Immunity). The
// instructions are passed to it on Ingress and the reply transcript is
// restored on Egress. The audio crosses the Membrane unsanitized and nothing
// can be restored inside the spoken reply, so Immunity allows only redact
// here. When unset, a no-op stage is used.
func WithContentStage(stage pipeline.ContentStage) HandlerOption {
	return func(h *Handler) {
		if stage != nil {
			h.stage = stage
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

// WithObservationStage installs the optional observability stage (Enterprise
// LabReport). Its trace never carries audio bytes or transcripts: audio is
// recorded as a descriptor. When unset, a no-op stage is used.
func WithObservationStage(obs pipeline.ObservationStage) HandlerOption {
	return func(h *Handler) {
		if obs != nil {
			h.obs = obs
		}
	}
}

// NewReloadable constructs a conversation Handler that reads the active Snapshot
// from current on every request. A nil Recorder is replaced with a no-op.
func NewReloadable(current func() *chat.Snapshot, logger *slog.Logger, rec metrics.Recorder, opts ...HandlerOption) *Handler {
	if rec == nil {
		rec = metrics.Nop{}
	}
	h := &Handler{current: current, logger: logger, metrics: rec, audit: audit.Discard{}, now: time.Now, maxBytes: DefaultMaxUploadBytes, stage: pipeline.NoOp{}, obs: pipeline.ObserveNoOp{}}
	for _, opt := range opts {
		opt(h)
	}
	return h
}

func (h *Handler) observe(ctx context.Context, callerID string, capID types.PathwayID, outcome metrics.Outcome, latency time.Duration, usage types.ConversationUsage, providerFamily, model string) {
	defer h.obs.OnComplete(ctx, pipeline.CompleteEvent{
		RequestID:        pipeline.RequestIDFrom(ctx),
		CallerID:         callerID,
		PathwayID:        string(capID),
		Operation:        string(types.OperationAudioConversation),
		Protection:       pipeline.ProtectionFrom(ctx),
		Outcome:          string(outcome),
		DurationMillis:   latency.Milliseconds(),
		PromptTokens:     usage.InputTokens,
		CompletionTokens: usage.OutputTokens,
		TotalTokens:      usage.InputTokens + usage.OutputTokens,
		ProviderFamily:   providerFamily,
		Model:            model,
	})
	h.metrics.RecordChat(string(capID), false, outcome, latency, usage.InputTokens, usage.OutputTokens, usage.InputTokens+usage.OutputTokens)
	h.audit.Write(audit.Record{
		SchemaVersion:    audit.SchemaVersion,
		Timestamp:        h.now(),
		CallerID:         callerID,
		PathwayID:        capID,
		Outcome:          string(outcome),
		DurationMillis:   latency.Milliseconds(),
		PromptTokens:     usage.InputTokens,
		CompletionTokens: usage.OutputTokens,
		TotalTokens:      usage.InputTokens + usage.OutputTokens,
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
	defer func() { _ = r.MultipartForm.RemoveAll() }()

	pathway := r.FormValue("pathway")
	if pathway == "" {
		writeError(w, http.StatusBadRequest, codeBadRequest, "pathway is required")
		return
	}
	reveal := r.FormValue("reveal") == "true" || r.URL.Query().Get("reveal") == "true"

	file, fh, ferr := r.FormFile("file")
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
	ext := types.AudioExt(fh.Filename)
	if !inputFormats[ext] {
		writeError(w, http.StatusBadRequest, codeBadRequest, "file must be a .wav or .mp3 recording")
		return
	}
	format := r.FormValue("response_format")
	if format == "" {
		format = "wav"
	}
	if !replyFormats[format] {
		writeError(w, http.StatusBadRequest, codeBadRequest, "response_format must be one of wav, mp3, flac, opus, aac, pcm")
		return
	}
	instructions := r.FormValue("instructions")
	if utf8.RuneCountInString(instructions) > maxInstructionChars {
		writeError(w, http.StatusBadRequest, codeBadRequest, "instructions exceed 4096 characters")
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
	if op, ok := snap.Resolver.Operation(capID); !ok || op != types.OperationAudioConversation {
		writeError(w, http.StatusBadRequest, codeUnresolvedPathway, "no binding for the requested pathway")
		return
	}

	plan, err := snap.Resolver.Plan(capID, nil)
	if err != nil {
		h.logger.Error("plan failed", "pathway", string(capID), "error", err.Error())
		writeError(w, http.StatusInternalServerError, codeInternal, "internal error")
		return
	}

	// Content barrier — Ingress. Only the instructions are text the barrier can
	// read; it runs even without them so an unenforceable policy fails closed.
	reqID := pipeline.NewRequestID()
	r = r.WithContext(pipeline.WithRequestID(r.Context(), reqID))
	h.obs.OnRequest(r.Context(), pipeline.RequestEvent{
		RequestID: reqID,
		CallerID:  caller.ID,
		PathwayID: string(capID),
		Operation: string(types.OperationAudioConversation),
	})
	rc := &pipeline.RequestContext{
		RequestID: reqID,
		CallerID:  caller.ID,
		PathwayID: string(capID),
		Operation: string(types.OperationAudioConversation),
	}
	if instructions != "" {
		rc.Messages = []types.Message{{Role: "system", Content: instructions}}
	}
	ingressStart := time.Now()
	err = h.stage.Ingress(r.Context(), rc)
	r = r.WithContext(pipeline.WithProtection(r.Context(), rc.Protection))
	if err == nil && len(rc.Messages) != boolInt(instructions != "") {
		err = pipeline.ErrTurnCountChanged
	}
	if err != nil {
		h.observe(r.Context(), caller.ID, capID, metrics.OutcomeFailedClosed, time.Since(ingressStart), types.ConversationUsage{}, "", "")
		status, code, msg := pipeline.GuardFailure(err)
		pipeline.LogGuardFailure(h.logger, string(capID), status, err)
		writeError(w, status, code, msg)
		return
	}
	if instructions != "" {
		instructions = rc.Messages[0].Content
	}
	outbound := append(append([]types.Message(nil), rc.Messages...),
		types.Message{Role: "user", Content: pipeline.MediaDescriptor("audio", ext, len(fileBytes), 0)})

	payload := map[string]any{
		"file":   types.AudioUpload{Ext: ext, Data: fileBytes},
		"format": format,
	}
	if v := r.FormValue("voice"); v != "" {
		payload["voice"] = v
	}
	if instructions != "" {
		payload["instructions"] = instructions
	}
	canonical := types.CanonicalRequest{PathwayID: capID, Payload: payload, Reveal: reveal, Caller: caller}
	sr, err := scrub.Sanitize(canonical, snap.Permeability)
	if err != nil {
		h.logger.Error("scrub failed", "pathway", string(capID), "error", err.Error())
		writeError(w, http.StatusInternalServerError, codeInternal, "internal error")
		return
	}

	ct, ok := snap.Transport.(transporter.ConversationTransporter)
	if !ok {
		writeError(w, http.StatusServiceUnavailable, codeUnavailable, "audio conversations are not available")
		return
	}

	ctx := transporter.WithPlan(r.Context(), planTiers(plan))
	start := time.Now()
	resp, err := ct.TransportConversation(ctx, sr)
	if err != nil {
		h.observe(r.Context(), caller.ID, capID, outcomeFor(err), time.Since(start), types.ConversationUsage{}, "", "")
		h.writeTransportError(w, err, reveal)
		return
	}

	scrubbed, err := scrub.ScrubConversation(resp, snap.Permeability, capID, reveal)
	if err != nil {
		h.observe(r.Context(), caller.ID, capID, metrics.OutcomeInternalError, time.Since(start), resp.Usage, string(resp.Provider), resp.Model)
		h.logger.Error("inward scrub failed", "pathway", string(capID), "error", err.Error())
		writeError(w, http.StatusInternalServerError, codeInternal, "internal error")
		return
	}

	// Content barrier — Egress. The reply transcript is restored before it
	// reaches the Caller.
	if rc.Protection.Protected {
		msg := types.Message{Role: "assistant", Content: scrubbed.Transcript}
		if err := h.stage.Egress(r.Context(), &pipeline.ResponseContext{RequestID: reqID, Message: &msg}); err != nil {
			h.observe(r.Context(), caller.ID, capID, metrics.OutcomeFailedClosed, time.Since(start), scrubbed.Usage, string(resp.Provider), resp.Model)
			h.logger.Error("content egress failed", "pathway", string(capID), "error", err.Error())
			writeError(w, http.StatusInternalServerError, codeInternal, "internal error")
			return
		}
		scrubbed.Transcript = msg.Content
	}

	inbound := types.Message{Role: "assistant", Content: pipeline.MediaDescriptor("audio", scrubbed.Format, len(scrubbed.Audio), 0) +
		fmt.Sprintf(" [transcript: %d chars]", len([]rune(scrubbed.Transcript)))}
	h.obs.OnProviderExchange(r.Context(), pipeline.ProviderExchangeEvent{
		RequestID:  reqID,
		CallerID:   caller.ID,
		PathwayID:  string(capID),
		Operation:  string(types.OperationAudioConversation),
		Protection: pipeline.ProtectionFrom(r.Context()),
		Outbound:   outbound,
		Inbound:    &inbound,
	})
	h.observe(r.Context(), caller.ID, capID, metrics.OutcomeSuccess, time.Since(start), scrubbed.Usage, string(resp.Provider), resp.Model)
	writeJSON(w, http.StatusOK, types.ConversationResponse{
		Pathway:    string(capID),
		Audio:      types.ConversationAudio{Data: scrubbed.Audio, Format: scrubbed.Format},
		Transcript: scrubbed.Transcript,
		Usage:      scrubbed.Usage,
		Provider:   string(scrubbed.Provider),
		Model:      scrubbed.Model,
	})
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
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
	if errors.Is(err, providers.ErrFormatUnsupported) {
		return neutralError{http.StatusBadRequest, codeBadRequest,
			"the audio format is not supported by this pathway's providers"}
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
	if errors.Is(err, transporter.ErrNoProvider) {
		return metrics.OutcomeUpstreamError
	}
	if errors.Is(err, providers.ErrOperationUnsupported) {
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
