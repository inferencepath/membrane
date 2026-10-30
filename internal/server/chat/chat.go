// Package chat implements the /v1/chat handler. A request is admitted, resolved
// and scrubbed, sent through the Transporter with the Egress Identity, and the
// response is scrubbed inward, for both unary and streaming (SSE) responses.
// Caller identity, provider endpoints and credentials never appear on the egress
// side.
package chat

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

	"github.com/inferencepath/membrane/internal/interior/admit"
	"github.com/inferencepath/membrane/internal/interior/audit"
	"github.com/inferencepath/membrane/internal/interior/authorize"
	"github.com/inferencepath/membrane/internal/interior/resolve"
	"github.com/inferencepath/membrane/internal/membrane/permeability"
	"github.com/inferencepath/membrane/internal/membrane/scrub"
	"github.com/inferencepath/membrane/internal/membrane/transporter"
	"github.com/inferencepath/membrane/internal/observability/metrics"
	"github.com/inferencepath/membrane/internal/server/pipeline"
	"github.com/inferencepath/membrane/internal/types"
)

// Route is the path this handler serves.
const Route = "/v1/chat"

// Snapshot is the immutable config a request binds to for its whole lifetime.
// A hot reload swaps in a new Snapshot without affecting in-flight requests.
type Snapshot struct {
	// Permeability is the Membrane allowlist used to Scrub this request.
	Permeability permeability.Spec
	// Callers authenticates Caller Keys (Admit).
	Callers *admit.Store
	// Authorizer gates Pathway access, deny by default. Nil disables
	// authorization.
	Authorizer *authorize.Authorizer
	// Resolver resolves Pathways to bindings on the Interior.
	Resolver *resolve.Resolver
	// Transport is the Membrane crossing for this config generation.
	Transport transporter.Transporter
}

// Handler serves the chat Pathway. It reads the active Snapshot once per
// request.
type Handler struct {
	current func() *Snapshot
	logger  *slog.Logger
	metrics metrics.Recorder
	audit   audit.Sink
	now     func() time.Time
	// stage is the optional content stage; no-op by default.
	stage pipeline.ContentStage
	// obs is the optional observation stage; no-op by default.
	obs pipeline.ObservationStage
}

// HandlerOption configures process-level Handler dependencies, as opposed to
// per-config ones.
type HandlerOption func(*Handler)

// WithAudit installs the Interior-only usage audit Sink. By default audit
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

// WithContentStage installs an optional content stage. Its Ingress runs after
// Admit and before the Membrane crossing; its Egress runs after the inward
// Scrub. The default is a no-op.
func WithContentStage(stage pipeline.ContentStage) HandlerOption {
	return func(h *Handler) {
		if stage != nil {
			h.stage = stage
		}
	}
}

// WithObservationStage installs an optional observation stage called when a
// request is admitted, at the Provider boundary, and on completion. It is
// best-effort and cannot affect the response. The default is a no-op.
func WithObservationStage(obs pipeline.ObservationStage) HandlerOption {
	return func(h *Handler) {
		if obs != nil {
			h.obs = obs
		}
	}
}

// Option configures the fixed Snapshot built by New.
type Option func(*Snapshot)

// WithAuthorizer installs a deny-by-default authorization check between Admit
// and Resolve. Without it, no authorization is enforced.
func WithAuthorizer(a *authorize.Authorizer) Option {
	return func(s *Snapshot) { s.Authorizer = a }
}

// New returns a chat Handler over a single fixed Snapshot, with no hot reload.
// A nil Recorder is replaced with a no-op.
func New(spec permeability.Spec, store *admit.Store, resolver *resolve.Resolver, transport transporter.Transporter, logger *slog.Logger, rec metrics.Recorder, opts ...Option) *Handler {
	snap := &Snapshot{Permeability: spec, Callers: store, Resolver: resolver, Transport: transport}
	for _, opt := range opts {
		opt(snap)
	}
	return NewReloadable(func() *Snapshot { return snap }, logger, rec)
}

// NewReloadable returns a chat Handler that calls current on every request to
// get the active Snapshot. current should be a cheap atomic read. A nil Snapshot
// is answered with 503. A nil Recorder is replaced with a no-op.
func NewReloadable(current func() *Snapshot, logger *slog.Logger, rec metrics.Recorder, opts ...HandlerOption) *Handler {
	if rec == nil {
		rec = metrics.Nop{}
	}
	h := &Handler{current: current, logger: logger, metrics: rec, audit: audit.Discard{}, now: time.Now, stage: pipeline.NoOp{}, obs: pipeline.ObserveNoOp{}}
	for _, opt := range opts {
		opt(h)
	}
	return h
}

// observe records a served request to the identity-free metrics, the
// Interior-only audit Sink and the observation stage. None of these can affect
// the response.
func (h *Handler) observe(ctx context.Context, callerID string, capID types.PathwayID, stream bool, outcome metrics.Outcome, latency time.Duration, prompt, completion, total int, tc toolCounts, providerFamily, model string) {
	h.metrics.RecordChat(string(capID), stream, outcome, latency, prompt, completion, total)
	h.audit.Write(audit.Record{
		SchemaVersion:     audit.SchemaVersion,
		Timestamp:         h.now(),
		CallerID:          callerID,
		PathwayID:         capID,
		Outcome:           string(outcome),
		DurationMillis:    latency.Milliseconds(),
		PromptTokens:      prompt,
		CompletionTokens:  completion,
		TotalTokens:       total,
		Stream:            stream,
		ToolsOffered:      tc.Offered,
		ToolCallsReturned: tc.Returned,
		ToolTurnsSent:     tc.Turns,
	})
	h.obs.OnComplete(ctx, pipeline.CompleteEvent{
		RequestID:        pipeline.RequestIDFrom(ctx),
		CallerID:         callerID,
		PathwayID:        string(capID),
		Operation:        "chat",
		Protection:       pipeline.ProtectionFrom(ctx),
		Outcome:          string(outcome),
		DurationMillis:   latency.Milliseconds(),
		PromptTokens:     prompt,
		CompletionTokens: completion,
		TotalTokens:      total,
		Stream:           stream,
		ProviderFamily:   providerFamily,
		Model:            model,
	})
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// The request uses this Snapshot throughout, even if a reload happens.
	snap := h.current()

	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, codeMethodNotAllowed, "only POST is supported")
		return
	}

	// No active configuration yet, e.g. a new managed store awaiting setup.
	// Fail closed; the control plane stays up so setup can finish.
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

	var req types.ChatRequest
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, codeBadRequest, "malformed request body")
		return
	}
	if req.Pathway == "" || len(req.Messages) == 0 {
		writeError(w, http.StatusBadRequest, codeBadRequest, "pathway and messages are required")
		return
	}

	// Each tool-result turn must answer a tool_call from an earlier assistant
	// turn. Dangling results are rejected here, before any Membrane crossing.
	if err := validateToolLoop(req.Messages); err != nil {
		writeError(w, http.StatusBadRequest, codeBadRequest, err.Error())
		return
	}

	capID := types.PathwayID(req.Pathway)

	// Authorization runs before Resolve and does not consult routing, so an
	// unauthorized Caller gets the same 403 whether or not the Pathway exists.
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

	binding, ok := snap.Resolver.Resolve(capID)
	if !ok {
		writeError(w, http.StatusBadRequest, codeUnresolvedPathway, "no binding for the requested pathway")
		return
	}

	// Only chat Pathways are served here. Other operations get the same
	// unresolved_pathway error so operations cannot be enumerated.
	if op, ok := snap.Resolver.Operation(capID); ok && op != types.OperationChat {
		writeError(w, http.StatusBadRequest, codeUnresolvedPathway, "no binding for the requested pathway")
		return
	}

	// Caller targets are an Interior-only routing control, honored when the
	// Pathway sets cascade_allowed. They reach the Transporter through the
	// context, never through the SanitizedRequest payload.
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

	// Tool policy is enforced before the Scrub. Violations get a generic 400.
	policy := snap.Permeability.ToolPolicy(capID)
	// The request may route to any plan tier, or to the bound provider if
	// there is no plan.
	dests := make([]string, 0, len(plan)+1)
	for _, p := range plan {
		dests = append(dests, p.Provider)
	}
	if len(dests) == 0 {
		dests = append(dests, string(binding.Provider))
	}
	if err := enforceToolPolicy(req, policy, dests); err != nil {
		writeError(w, http.StatusBadRequest, codeBadRequest, err.Error())
		return
	}

	tc := toolCounts{Offered: len(req.Tools)}
	for _, m := range req.Messages {
		if m.Role == "tool" {
			tc.Turns++
		}
	}

	// The content stage's Ingress runs before buildCanonical and the Scrub, so
	// Providers only see its output. The request ID keys per-request stage state
	// and never crosses the Membrane.
	reqID := pipeline.NewRequestID()
	r = r.WithContext(pipeline.WithRequestID(r.Context(), reqID))
	h.obs.OnRequest(r.Context(), pipeline.RequestEvent{
		RequestID: reqID,
		CallerID:  caller.ID,
		PathwayID: string(capID),
		Operation: "chat",
		Stream:    req.Stream,
	})
	rc := &pipeline.RequestContext{
		RequestID: reqID,
		CallerID:  caller.ID,
		PathwayID: string(capID),
		Messages:  req.Messages,
	}
	ingressStart := time.Now()
	if err := h.stage.Ingress(r.Context(), rc); err != nil {
		// Fail closed. Nothing has crossed the Membrane, but the block is still
		// audited.
		r = r.WithContext(pipeline.WithProtection(r.Context(), rc.Protection))
		h.observe(r.Context(), caller.ID, capID, req.Stream, metrics.OutcomeFailedClosed, time.Since(ingressStart), 0, 0, 0, tc, "", "")
		h.logger.Error("content ingress failed", "pathway", string(capID), "error", err.Error())
		writeError(w, http.StatusInternalServerError, codeInternal, "internal error")
		return
	}
	// The ObservationStage uses the protection marker to decide what it may
	// capture.
	r = r.WithContext(pipeline.WithProtection(r.Context(), rc.Protection))
	// Ingress may have rewritten the turns in place.
	outbound := rc.Messages

	canonical := buildCanonical(capID, req, caller, policy)
	sr, err := scrub.Sanitize(canonical, snap.Permeability)
	if err != nil {
		h.logger.Error("scrub failed", "pathway", string(capID), "error", err.Error())
		writeError(w, http.StatusInternalServerError, codeInternal, "internal error")
		return
	}

	if sr.Stream() {
		h.serveStream(w, r, snap, sr, capID, caller.ID, req.Reveal, tiers, tc, outbound)
		return
	}
	h.serveUnary(w, r, snap, sr, capID, caller.ID, req.Reveal, tiers, tc, outbound)
}

// toolCounts holds tool-usage counts for the audit record. It carries no tool
// names, arguments or results.
type toolCounts struct {
	Offered  int
	Returned int
	Turns    int
}

// Tool policy rejections. The message does not say which tool or destination
// was refused.
var (
	errToolNotPermitted = errors.New("tool request not permitted for this pathway")
	errToolCap          = errors.New("tool request exceeds this pathway's limits")
)

// requestHasTools reports whether a request carries any tool payload (offered
// definitions, assistant tool_calls, or tool-role results).
func requestHasTools(req types.ChatRequest) bool {
	if len(req.Tools) > 0 {
		return true
	}
	for _, m := range req.Messages {
		if m.Role == "tool" || len(m.ToolCalls) > 0 {
			return true
		}
	}
	return false
}

// enforceToolPolicy applies the Pathway's tool policy. Requests without tools
// pass unchanged.
func enforceToolPolicy(req types.ChatRequest, policy permeability.ToolPolicy, destinations []string) error {
	if !requestHasTools(req) {
		return nil
	}
	// Every destination the request could reach must allow tools.
	for _, p := range destinations {
		if !policy.DestinationAllowed(p) {
			return errToolNotPermitted
		}
	}
	for _, t := range req.Tools {
		if !policy.ToolAllowed(t.Name) {
			return errToolNotPermitted
		}
	}
	if policy.MaxTools > 0 && len(req.Tools) > policy.MaxTools {
		return errToolCap
	}
	if policy.MaxToolResultBytes > 0 {
		for _, m := range req.Messages {
			if m.Role == "tool" && len(m.Content) > policy.MaxToolResultBytes {
				return errToolCap
			}
		}
	}
	return nil
}

// planTiers converts a resolve plan into a transporter plan. It lives here
// because the Interior must not import the Membrane.
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

func (h *Handler) serveUnary(w http.ResponseWriter, r *http.Request, snap *Snapshot, sr scrub.SanitizedRequest, capID types.PathwayID, callerID string, reveal bool, tiers []transporter.PlanTier, tc toolCounts, outbound []types.Message) {
	// Upstream timeouts are per target inside the Membrane; the request
	// context only carries client cancellation.
	ctx := transporter.WithPlan(r.Context(), tiers)

	start := time.Now()
	resp, err := snap.Transport.Transport(ctx, sr)
	if err != nil {
		h.observe(r.Context(), callerID, capID, false, outcomeFor(err), time.Since(start), 0, 0, 0, tc, "", "")
		h.writeTransportError(w, err, reveal)
		return
	}
	scrubbed, err := scrub.ScrubResponse(resp, snap.Permeability, capID, reveal)
	if err != nil {
		h.observe(r.Context(), callerID, capID, false, metrics.OutcomeInternalError, time.Since(start), 0, 0, 0, tc, "", "")
		h.logger.Error("inward scrub failed", "pathway", string(capID), "error", err.Error())
		writeError(w, http.StatusInternalServerError, codeInternal, "internal error")
		return
	}
	tc.Returned = len(scrubbed.Message.ToolCalls)
	// Called after the inward Scrub and before Egress, so the payloads are what
	// the Provider saw and returned.
	h.obs.OnProviderExchange(r.Context(), pipeline.ProviderExchangeEvent{
		RequestID:  pipeline.RequestIDFrom(r.Context()),
		CallerID:   callerID,
		PathwayID:  string(capID),
		Protection: pipeline.ProtectionFrom(r.Context()),
		Stream:     false,
		Outbound:   outbound,
		Inbound:    &scrubbed.Message,
	})
	// Egress may rewrite the message in place before the Caller sees it. An
	// error fails the response closed.
	if err := h.stage.Egress(r.Context(), &pipeline.ResponseContext{
		RequestID: pipeline.RequestIDFrom(r.Context()),
		Message:   &scrubbed.Message,
	}); err != nil {
		h.observe(r.Context(), callerID, capID, false, metrics.OutcomeInternalError, time.Since(start), 0, 0, 0, tc, "", "")
		h.logger.Error("content egress failed", "pathway", string(capID), "error", err.Error())
		writeError(w, http.StatusInternalServerError, codeInternal, "internal error")
		return
	}
	h.observe(r.Context(), callerID, capID, false, metrics.OutcomeSuccess, time.Since(start),
		scrubbed.Usage.PromptTokens, scrubbed.Usage.CompletionTokens, scrubbed.Usage.TotalTokens, tc,
		string(resp.Provider), resp.Model)
	out := types.ChatResponse{
		Pathway:      string(capID),
		Message:      scrubbed.Message,
		FinishReason: scrubbed.FinishReason,
		Usage:        scrubbed.Usage,
		Provider:     string(scrubbed.Provider),
		Model:        scrubbed.Model,
	}
	writeJSON(w, http.StatusOK, out)
}

type sseChunk struct {
	Delta        string        `json:"delta"`
	FinishReason string        `json:"finish_reason,omitempty"`
	ToolCalls    []sseToolCall `json:"tool_calls,omitempty"`
}

// sseToolCall is a streamed tool-call fragment. It mirrors types.ToolCallDelta;
// the Caller reassembles fragments by index.
type sseToolCall struct {
	Index     int    `json:"index"`
	ID        string `json:"id,omitempty"`
	Name      string `json:"name,omitempty"`
	Arguments string `json:"arguments,omitempty"`
}

func (h *Handler) serveStream(w http.ResponseWriter, r *http.Request, snap *Snapshot, sr scrub.SanitizedRequest, capID types.PathwayID, callerID string, reveal bool, tiers []transporter.PlanTier, tc toolCounts, outbound []types.Message) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, codeInternal, "streaming unsupported")
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	wroteAny := false
	// The pre-Egress response text, for the observation stage.
	var sanitized strings.Builder
	emit := func(chunk types.StreamChunk) error {
		if !chunk.Done {
			sanitized.WriteString(chunk.Delta)
		}
		// Egress also sees the Done chunk so a buffering stage can flush what
		// it held back.
		if err := h.stage.Egress(r.Context(), &pipeline.ResponseContext{
			RequestID: pipeline.RequestIDFrom(r.Context()),
			Chunk:     &chunk,
		}); err != nil {
			return err
		}
		if chunk.Done {
			// Send any flushed tail before [DONE].
			if chunk.Delta != "" {
				b, err := json.Marshal(sseChunk{Delta: chunk.Delta})
				if err != nil {
					return err
				}
				if _, err := fmt.Fprintf(w, "data: %s\n\n", b); err != nil {
					return err
				}
				wroteAny = true
			}
			if _, err := io.WriteString(w, "data: [DONE]\n\n"); err != nil {
				return err
			}
			flusher.Flush()
			return nil
		}
		var tcs []sseToolCall
		for _, d := range chunk.ToolCalls {
			tcs = append(tcs, sseToolCall{Index: d.Index, ID: d.ID, Name: d.Name, Arguments: d.ArgumentsFragment})
		}
		// A buffering stage may hold back a whole chunk. Skip the empty frame.
		if chunk.Delta == "" && chunk.FinishReason == "" && len(tcs) == 0 {
			return nil
		}
		b, err := json.Marshal(sseChunk{Delta: chunk.Delta, FinishReason: chunk.FinishReason, ToolCalls: tcs})
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

	// Streams use the adapter's idle timeout. The request context is canceled
	// when the client disconnects.
	ctx := transporter.WithPlan(r.Context(), tiers)
	start := time.Now()
	if err := snap.Transport.TransportStream(ctx, sr, emit); err != nil {
		h.observe(r.Context(), callerID, capID, true, outcomeFor(err), time.Since(start), 0, 0, 0, tc, "", "")
		if !wroteAny {
			h.writeTransportError(w, err, reveal)
			return
		}
		// Headers are already sent, so end the stream with an error event.
		h.logger.Warn("stream terminated with error", "pathway", string(capID), "error", err.Error())
		_, _ = fmt.Fprintf(w, "event: error\ndata: {\"error\":{\"code\":%q,\"message\":\"stream terminated\"}}\n\n", codeStreamError)
		flusher.Flush()
		return
	}
	// Inbound is the pre-Egress text. This is called before OnComplete so any
	// capture is recorded ahead of the completion event.
	assembled := types.Message{Role: "assistant", Content: sanitized.String()}
	h.obs.OnProviderExchange(r.Context(), pipeline.ProviderExchangeEvent{
		RequestID:  pipeline.RequestIDFrom(r.Context()),
		CallerID:   callerID,
		PathwayID:  string(capID),
		Protection: pipeline.ProtectionFrom(r.Context()),
		Stream:     true,
		Outbound:   outbound,
		Inbound:    &assembled,
	})
	// Streams carry no usage totals.
	h.observe(r.Context(), callerID, capID, true, metrics.OutcomeSuccess, time.Since(start), 0, 0, 0, tc, "", "")
}

// validateToolLoop checks that every tool-role turn references a tool_call id
// from an earlier assistant turn. Forward references are rejected.
func validateToolLoop(messages []types.Message) error {
	seen := make(map[string]struct{})
	for _, m := range messages {
		switch m.Role {
		case "assistant":
			for _, tc := range m.ToolCalls {
				if tc.ID != "" {
					seen[tc.ID] = struct{}{}
				}
			}
		case "tool":
			if m.ToolCallID == "" {
				return errors.New("tool-result turn is missing tool_call_id")
			}
			if _, ok := seen[m.ToolCallID]; !ok {
				return errors.New("tool-result turn does not reference a prior tool call")
			}
		}
	}
	return nil
}

func buildCanonical(capID types.PathwayID, req types.ChatRequest, caller types.Caller, policy permeability.ToolPolicy) types.CanonicalRequest {
	messages := make([]any, len(req.Messages))
	for i, m := range req.Messages {
		content := m.Content
		// Tool results are redacted before the Scrub, so only the redacted form
		// can cross the Membrane.
		if m.Role == "tool" {
			content = policy.RedactResult(content)
		}
		mm := map[string]any{"role": m.Role, "content": content}
		if len(m.ToolCalls) > 0 {
			tcs := make([]any, len(m.ToolCalls))
			for j, tc := range m.ToolCalls {
				tcm := map[string]any{"id": tc.ID, "name": tc.Name}
				if len(tc.Arguments) > 0 {
					tcm["arguments"] = tc.Arguments
				}
				tcs[j] = tcm
			}
			mm["tool_calls"] = tcs
		}
		if m.ToolCallID != "" {
			mm["tool_call_id"] = m.ToolCallID
		}
		if m.Name != "" {
			mm["name"] = m.Name
		}
		messages[i] = mm
	}
	payload := map[string]any{"messages": messages}
	// Tools and tool_choice only cross if the Pathway's outbound allowlist,
	// closed by default, permits them. Adapters map this neutral JSON to each
	// provider's tool schema.
	if len(req.Tools) > 0 {
		tools := make([]any, len(req.Tools))
		for i, t := range req.Tools {
			tm := map[string]any{"name": t.Name}
			if t.Description != "" {
				tm["description"] = t.Description
			}
			if len(t.Parameters) > 0 {
				tm["parameters"] = t.Parameters
			}
			tools[i] = tm
		}
		payload["tools"] = tools
	}
	if len(req.ToolChoice) > 0 {
		payload["tool_choice"] = req.ToolChoice
	}
	if req.Temperature != nil {
		payload["temperature"] = *req.Temperature
	}
	if req.MaxTokens != nil {
		payload["max_tokens"] = *req.MaxTokens
	}
	if req.TopP != nil {
		payload["top_p"] = *req.TopP
	}
	if len(req.Stop) > 0 {
		payload["stop"] = req.Stop
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
