// Package chat implements the logical /v1/chat handler: Admit → Resolve → Scrub
// → Transporter (+ Egress Identity) → inward Scrub, for both non-streaming and
// streaming (SSE) responses. No endpoint here accepts or emits provider
// endpoints, credentials, or caller identity on the egress side.
package chat

import (
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
	"github.com/inferencepath/membrane/internal/types"
)

// Route is the path this handler serves.
const Route = "/v1/chat"

// Snapshot is the immutable config bundle a request binds to for its entire
// lifetime. The Handler loads exactly one Snapshot per request, so a concurrent
// hot reload (which swaps in a new Snapshot) never affects an in-flight request.
type Snapshot struct {
	// Permeability is the Membrane allowlist used to Scrub this request.
	Permeability permeability.Spec
	// Callers authenticates Caller Keys (Admit).
	Callers *admit.Store
	// Authorizer gates Pathway access (deny-by-default). May be nil (Phase
	// 1/2 behavior: no authorization enforced).
	Authorizer *authorize.Authorizer
	// Resolver is the Pathway existence/routing gate (Interior).
	Resolver *resolve.Resolver
	// Transport is the Membrane crossing for this config generation.
	Transport transporter.Transporter
}

// Handler orchestrates the chat Pathway. It reads the active Snapshot via
// current() once per request.
type Handler struct {
	current func() *Snapshot
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

// Option configures the fixed Snapshot built by New.
type Option func(*Snapshot)

// WithAuthorizer installs a deny-by-default authorization gate between Admit and
// Resolve. When unset, no authorization is enforced.
func WithAuthorizer(a *authorize.Authorizer) Option {
	return func(s *Snapshot) { s.Authorizer = a }
}

// New constructs a chat Handler over a single, fixed Snapshot (no hot reload).
// A nil Recorder is replaced with a no-op, so metrics are always safe to record.
func New(spec permeability.Spec, store *admit.Store, resolver *resolve.Resolver, transport transporter.Transporter, logger *slog.Logger, rec metrics.Recorder, opts ...Option) *Handler {
	snap := &Snapshot{Permeability: spec, Callers: store, Resolver: resolver, Transport: transport}
	for _, opt := range opts {
		opt(snap)
	}
	return NewReloadable(func() *Snapshot { return snap }, logger, rec)
}

// NewReloadable constructs a chat Handler that reads the active Snapshot from
// current on every request. current MUST return a non-nil Snapshot and is
// expected to be a lock-free atomic read (e.g. a ConfigManager). A nil Recorder
// is replaced with a no-op.
func NewReloadable(current func() *Snapshot, logger *slog.Logger, rec metrics.Recorder, opts ...HandlerOption) *Handler {
	if rec == nil {
		rec = metrics.Nop{}
	}
	h := &Handler{current: current, logger: logger, metrics: rec, audit: audit.Discard{}, now: time.Now}
	for _, opt := range opts {
		opt(h)
	}
	return h
}

// observe records one served request to both the identity-free metrics and the
// Interior-only audit Sink. The audit write is best-effort and never affects the
// response.
func (h *Handler) observe(callerID string, capID types.PathwayID, stream bool, outcome metrics.Outcome, latency time.Duration, prompt, completion, total int, tc toolCounts) {
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
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// Bind to exactly one config Snapshot for this request's lifetime. A
	// concurrent hot reload swaps in a new Snapshot for subsequent requests only.
	snap := h.current()

	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, codeMethodNotAllowed, "only POST is supported")
		return
	}

	// Fail closed: a nil Snapshot means the daemon has no active configuration
	// yet (e.g. a freshly bootstrapped managed store awaiting setup). Serve 503
	// rather than touch a nil Snapshot; the control plane stays up for setup.
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

	// Tool-loop integrity: a tool-result turn must reference a tool_call
	// emitted by a prior assistant turn in the same request. A dangling result is
	// a malformed agent loop and is rejected on the Interior (generic 400) before
	// any Membrane crossing — never forwarded as a partial egress.
	if err := validateToolLoop(req.Messages); err != nil {
		writeError(w, http.StatusBadRequest, codeBadRequest, err.Error())
		return
	}

	capID := types.PathwayID(req.Pathway)

	// Authorization (deny-by-default) runs after Admit and before Resolve/Scrub.
	// It never consults routing, so an unauthorized Caller receives the same
	// generic 403 whether or not the Pathway exists (no enumeration oracle),
	// and no SanitizedRequest is built — the request never reaches the Membrane.
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

	// Route-dispatch guard: the /v1/chat handler
	// serves only Pathways whose operation is `chat`. A Pathway configured for a
	// different operation is reported with the same generic unresolved_pathway
	// error (no operation/Pathway enumeration oracle).
	if op, ok := snap.Resolver.Operation(capID); ok && op != types.OperationChat {
		writeError(w, http.StatusBadRequest, codeUnresolvedPathway, "no binding for the requested pathway")
		return
	}

	// Resolve the caller cascade into an ordered routing plan. targets[] are an
	// Interior-only routing control (honored only when the Pathway sets
	// cascade_allowed) and are validated against the Pathway's provider/model
	// allowlist here; they never enter the SanitizedRequest payload and so never
	// cross the Membrane. The plan travels to the Transporter via the request
	// context.
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

	// Tool-calling governance, enforced on the Interior BEFORE
	// the Scrub: tool-name allow/deny, caps on tool count / result size, and
	// destination-scoped enablement. Violations are rejected with a generic 400
	// (no enumeration oracle) and no SanitizedRequest is built.
	policy := snap.Permeability.ToolPolicy(capID)
	// Destinations the request could route to: the cascade plan tiers when a
	// cascade policy is active, otherwise the single resolved binding provider.
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

	// Interior tool-usage counts (content-free) for the audit record.
	tc := toolCounts{Offered: len(req.Tools)}
	for _, m := range req.Messages {
		if m.Role == "tool" {
			tc.Turns++
		}
	}

	canonical := buildCanonical(capID, req, caller, policy)
	sr, err := scrub.Sanitize(canonical, snap.Permeability)
	if err != nil {
		h.logger.Error("scrub failed", "pathway", string(capID), "error", err.Error())
		writeError(w, http.StatusInternalServerError, codeInternal, "internal error")
		return
	}

	if sr.Stream() {
		h.serveStream(w, r, snap, sr, capID, caller.ID, req.Reveal, tiers, tc)
		return
	}
	h.serveUnary(w, r, snap, sr, capID, caller.ID, req.Reveal, tiers, tc)
}

// toolCounts holds content-free tool-usage counts for the audit record:
// tools offered outbound, tool_calls returned inward, and
// tool-role turns sent outbound. No tool names, arguments, or results.
type toolCounts struct {
	Offered  int
	Returned int
	Turns    int
}

// errToolNotPermitted and errToolCap are the Interior tool-governance rejections.
// Both carry provider-neutral, non-enumerating messages (the same message is
// returned regardless of which tool/destination triggered it).
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

// enforceToolPolicy applies the per-Pathway tool governance on the Interior,
// before any Membrane crossing. It is a no-op for non-tool requests.
func enforceToolPolicy(req types.ChatRequest, policy permeability.ToolPolicy, destinations []string) error {
	if !requestHasTools(req) {
		return nil
	}
	// Destination-scoped enablement: every destination the request could
	// route to (default + cascade tiers) must be tool-enabled; otherwise the tool
	// request is rejected rather than risk crossing to a non-enabled destination.
	for _, p := range destinations {
		if !policy.DestinationAllowed(p) {
			return errToolNotPermitted
		}
	}
	// Tool-name allow/deny.
	for _, t := range req.Tools {
		if !policy.ToolAllowed(t.Name) {
			return errToolNotPermitted
		}
	}
	// Caps.
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

// planTiers converts an Interior resolve plan into the Membrane's routing plan.
// This mapping lives in the server layer because the Interior (resolve) must not
// import the Membrane (transporter).
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

func (h *Handler) serveUnary(w http.ResponseWriter, r *http.Request, snap *Snapshot, sr scrub.SanitizedRequest, capID types.PathwayID, callerID string, reveal bool, tiers []transporter.PlanTier, tc toolCounts) {
	// Per-target upstream timeouts are applied inside the Membrane (each pooled
	// target carries its own deadline); here we only honor client cancellation.
	ctx := transporter.WithPlan(r.Context(), tiers)

	start := time.Now()
	resp, err := snap.Transport.Transport(ctx, sr)
	if err != nil {
		h.observe(callerID, capID, false, outcomeFor(err), time.Since(start), 0, 0, 0, tc)
		h.writeTransportError(w, err, reveal)
		return
	}
	scrubbed, err := scrub.ScrubResponse(resp, snap.Permeability, capID, reveal)
	if err != nil {
		h.observe(callerID, capID, false, metrics.OutcomeInternalError, time.Since(start), 0, 0, 0, tc)
		h.logger.Error("inward scrub failed", "pathway", string(capID), "error", err.Error())
		writeError(w, http.StatusInternalServerError, codeInternal, "internal error")
		return
	}
	tc.Returned = len(scrubbed.Message.ToolCalls)
	h.observe(callerID, capID, false, metrics.OutcomeSuccess, time.Since(start),
		scrubbed.Usage.PromptTokens, scrubbed.Usage.CompletionTokens, scrubbed.Usage.TotalTokens, tc)
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

// sseToolCall is the provider-neutral streaming tool-call fragment relayed to the
// Caller. It mirrors types.ToolCallDelta; the Caller reassembles by index.
type sseToolCall struct {
	Index     int    `json:"index"`
	ID        string `json:"id,omitempty"`
	Name      string `json:"name,omitempty"`
	Arguments string `json:"arguments,omitempty"`
}

func (h *Handler) serveStream(w http.ResponseWriter, r *http.Request, snap *Snapshot, sr scrub.SanitizedRequest, capID types.PathwayID, callerID string, reveal bool, tiers []transporter.PlanTier, tc toolCounts) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, codeInternal, "streaming unsupported")
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	wroteAny := false
	emit := func(chunk types.StreamChunk) error {
		if chunk.Done {
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

	// Streaming uses the adapter's idle timeout; the request context cancels on
	// client disconnect.
	ctx := transporter.WithPlan(r.Context(), tiers)
	start := time.Now()
	if err := snap.Transport.TransportStream(ctx, sr, emit); err != nil {
		h.observe(callerID, capID, true, outcomeFor(err), time.Since(start), 0, 0, 0, tc)
		if !wroteAny {
			h.writeTransportError(w, err, reveal)
			return
		}
		// Bytes already relayed: emit a terminal, provider-neutral error event.
		h.logger.Warn("stream terminated with error", "pathway", string(capID), "error", err.Error())
		_, _ = fmt.Fprintf(w, "event: error\ndata: {\"error\":{\"code\":%q,\"message\":\"stream terminated\"}}\n\n", codeStreamError)
		flusher.Flush()
		return
	}
	// Streaming responses do not carry a usage total; record latency/outcome only.
	h.observe(callerID, capID, true, metrics.OutcomeSuccess, time.Since(start), 0, 0, 0, tc)
}

// validateToolLoop enforces that every tool-result turn (role "tool") correlates
// to a tool_call id emitted by an earlier assistant turn. It walks the turns in
// order so a result may only reference calls already seen (no forward or
// self-reference). Parallel calls in one assistant turn each register their id.
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
		// Redaction: operator-configured patterns are applied to
		// tool-role result content BEFORE the Scrub, so only the redacted form is
		// eligible to cross the Membrane.
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
	// Tool definitions and tool-choice are made ELIGIBLE to cross here; whether
	// they actually cross is decided solely by the Pathway's outbound allowlist
	// (closed by default). The barrier carries them as opaque, provider-neutral
	// JSON — adapters normalize to each provider's native tool schema.
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
