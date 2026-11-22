// Package multimodal implements the logical /v1/multimodal/chat handler: a
// conversation whose turns may carry text, images, and audio, optionally answered
// with a spoken reply. It is a separate operation from chat, which stays
// text-only and unchanged.
//
// Admit → Authorize → Resolve (operation-gated) → Validate (typed parts, real
// media type from the bytes, size/count limits, pathway media permission) →
// content barrier Ingress on every text part → Scrub → Transporter (+ Egress
// Identity) → inward Scrub → content barrier Egress on the reply text and the
// spoken reply's transcript. A spoken reply without a transcript is never
// released. Media is held in memory for the one request only.
package multimodal

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/inferencepath/membrane/internal/exterior/providers"
	"github.com/inferencepath/membrane/internal/interior/audit"
	"github.com/inferencepath/membrane/internal/interior/resolve"
	"github.com/inferencepath/membrane/internal/membrane/permeability"
	"github.com/inferencepath/membrane/internal/membrane/scrub"
	"github.com/inferencepath/membrane/internal/membrane/transporter"
	"github.com/inferencepath/membrane/internal/observability/metrics"
	"github.com/inferencepath/membrane/internal/server/chat"
	"github.com/inferencepath/membrane/internal/server/pipeline"
	"github.com/inferencepath/membrane/internal/types"
)

// Route is the path this handler serves.
const Route = "/v1/multimodal/chat"

// DefaultMaxBodyBytes bounds the JSON request (media travels base64-encoded),
// rejected fail-closed before any Provider contact.
const DefaultMaxBodyBytes int64 = 48 << 20

// Request limits.
const (
	maxMessages     = 256
	maxParts        = 256
	maxImages       = 16
	maxAudioParts   = 8
	maxImageBytes   = 20 << 20
	maxAudioBytes   = 25 << 20
	maxTextRunes    = 1 << 20
	maxMaxTokens    = 1 << 20
	maxVoiceLen     = 64
	defaultReplyFmt = "wav"
)

// Pathway outbound permissions that open each media kind. Absent means closed.
const (
	permMessages    = "messages"
	permImageInput  = "image_input"
	permAudioInput  = "audio_input"
	permAudioOutput = "audio_output"
)

var replyFormats = map[string]bool{"wav": true, "mp3": true, "flac": true, "opus": true, "aac": true, "pcm": true}

var voicePattern = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

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

// Handler orchestrates the multimodal_chat Pathway over the active Snapshot.
type Handler struct {
	current  func() *chat.Snapshot
	logger   *slog.Logger
	metrics  metrics.Recorder
	audit    audit.Sink
	stage    pipeline.ContentStage
	obs      pipeline.ObservationStage
	now      func() time.Time
	maxBytes int64
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

// WithContentStage installs an optional content stage applied to every text
// part on ingress and to the reply text and transcript on egress.
// When unset, a no-op stage is used.
func WithContentStage(stage pipeline.ContentStage) HandlerOption {
	return func(h *Handler) {
		if stage != nil {
			h.stage = stage
		}
	}
}

// WithObservationStage installs the optional observability stage. Its trace
// carries text and media descriptors, never media bytes.
// It is best-effort and never affects the response. When unset, a no-op stage
// is used.
func WithObservationStage(obs pipeline.ObservationStage) HandlerOption {
	return func(h *Handler) {
		if obs != nil {
			h.obs = obs
		}
	}
}

// WithMaxBodyBytes overrides the default request size limit.
func WithMaxBodyBytes(n int64) HandlerOption {
	return func(h *Handler) {
		if n > 0 {
			h.maxBytes = n
		}
	}
}

// NewReloadable constructs a multimodal Handler that reads the active Snapshot
// from current on every request. A nil Recorder is replaced with a no-op.
func NewReloadable(current func() *chat.Snapshot, logger *slog.Logger, rec metrics.Recorder, opts ...HandlerOption) *Handler {
	if rec == nil {
		rec = metrics.Nop{}
	}
	h := &Handler{
		current: current, logger: logger, metrics: rec, audit: audit.Discard{},
		stage: pipeline.NoOp{}, obs: pipeline.ObserveNoOp{}, now: time.Now, maxBytes: DefaultMaxBodyBytes,
	}
	for _, opt := range opts {
		opt(h)
	}
	return h
}

// --- wire shapes ---

type wireRequest struct {
	Pathway     string        `json:"pathway"`
	Messages    []wireMessage `json:"messages"`
	Audio       *wireAudioOut `json:"audio,omitempty"`
	Modalities  []string      `json:"modalities,omitempty"`
	MaxTokens   *int          `json:"max_tokens,omitempty"`
	Temperature *float64      `json:"temperature,omitempty"`
	Reveal      bool          `json:"reveal,omitempty"`
}

type wireMessage struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
}

type wireAudioOut struct {
	Voice  string `json:"voice,omitempty"`
	Format string `json:"format,omitempty"`
}

type wirePart struct {
	Type     string `json:"type"`
	Text     string `json:"text,omitempty"`
	ImageURL *struct {
		URL string `json:"url"`
	} `json:"image_url,omitempty"`
	InputAudio *struct {
		Data   string `json:"data"`
		Format string `json:"format"`
	} `json:"input_audio,omitempty"`
}

// validationError is a Caller-facing rejection (status + neutral message).
type validationError struct {
	status  int
	code    string
	message string
}

func (e validationError) Error() string { return e.message }

func badRequest(format string, a ...any) error {
	return validationError{http.StatusBadRequest, codeBadRequest, fmt.Sprintf(format, a...)}
}

func forbidden(msg string) error {
	return validationError{http.StatusForbidden, codeForbidden, msg}
}

// parseMessages turns the wire turns into validated, typed parts. Media is
// accepted only as inline data whose real type (sniffed from the bytes) matches
// what the Caller declared; remote URLs are refused because the barrier cannot
// inspect what it does not hold.
func parseMessages(in []wireMessage) ([]types.MultimodalMessage, error) {
	if len(in) == 0 {
		return nil, badRequest("messages are required")
	}
	if len(in) > maxMessages {
		return nil, badRequest("too many messages (max %d)", maxMessages)
	}
	out := make([]types.MultimodalMessage, 0, len(in))
	parts, images, audios, runes := 0, 0, 0, 0
	for i, m := range in {
		switch m.Role {
		case "system", "user", "assistant":
		default:
			return nil, badRequest("messages[%d]: role must be system, user, or assistant", i)
		}
		var wps []wirePart
		var s string
		content := bytes.TrimSpace(m.Content)
		switch {
		case len(content) == 0 || string(content) == "null":
			return nil, badRequest("messages[%d]: content is required", i)
		case content[0] == '"':
			if err := json.Unmarshal(content, &s); err != nil {
				return nil, badRequest("messages[%d]: malformed content", i)
			}
			wps = []wirePart{{Type: "text", Text: s}}
		case content[0] == '[':
			dec := json.NewDecoder(bytes.NewReader(content))
			dec.DisallowUnknownFields()
			if err := dec.Decode(&wps); err != nil {
				return nil, badRequest("messages[%d]: malformed content parts", i)
			}
		default:
			return nil, badRequest("messages[%d]: content must be a string or an array of parts", i)
		}
		if len(wps) == 0 {
			return nil, badRequest("messages[%d]: content is required", i)
		}
		msg := types.MultimodalMessage{Role: m.Role, Parts: make([]types.ContentPart, 0, len(wps))}
		for j, p := range wps {
			parts++
			if parts > maxParts {
				return nil, badRequest("too many content parts (max %d)", maxParts)
			}
			switch p.Type {
			case "text":
				if p.ImageURL != nil || p.InputAudio != nil {
					return nil, badRequest("messages[%d].content[%d]: text part carries media", i, j)
				}
				if !utf8.ValidString(p.Text) {
					return nil, badRequest("messages[%d].content[%d]: text is not valid UTF-8", i, j)
				}
				runes += utf8.RuneCountInString(p.Text)
				if runes > maxTextRunes {
					return nil, badRequest("text exceeds %d characters", maxTextRunes)
				}
				msg.Parts = append(msg.Parts, types.ContentPart{Type: types.PartText, Text: p.Text})
			case "image_url":
				if m.Role != "user" {
					return nil, badRequest("messages[%d]: only user turns may carry images", i)
				}
				if p.ImageURL == nil || p.Text != "" || p.InputAudio != nil {
					return nil, badRequest("messages[%d].content[%d]: malformed image part", i, j)
				}
				images++
				if images > maxImages {
					return nil, badRequest("too many images (max %d)", maxImages)
				}
				part, err := parseImage(p.ImageURL.URL)
				if err != nil {
					return nil, badRequest("messages[%d].content[%d]: %s", i, j, err.Error())
				}
				msg.Parts = append(msg.Parts, part)
			case "input_audio":
				if m.Role != "user" {
					return nil, badRequest("messages[%d]: only user turns may carry audio", i)
				}
				if p.InputAudio == nil || p.Text != "" || p.ImageURL != nil {
					return nil, badRequest("messages[%d].content[%d]: malformed audio part", i, j)
				}
				audios++
				if audios > maxAudioParts {
					return nil, badRequest("too many audio parts (max %d)", maxAudioParts)
				}
				part, err := parseAudio(p.InputAudio.Data, p.InputAudio.Format)
				if err != nil {
					return nil, badRequest("messages[%d].content[%d]: %s", i, j, err.Error())
				}
				msg.Parts = append(msg.Parts, part)
			default:
				return nil, badRequest("messages[%d].content[%d]: unsupported part type %q", i, j, p.Type)
			}
		}
		out = append(out, msg)
	}
	return out, nil
}

func parseImage(url string) (types.ContentPart, error) {
	const prefix = "data:"
	if !strings.HasPrefix(url, prefix) {
		return types.ContentPart{}, errors.New("images must be inline data URLs (remote URLs are not accepted)")
	}
	meta, data, ok := strings.Cut(url[len(prefix):], ",")
	if !ok || !strings.HasSuffix(meta, ";base64") {
		return types.ContentPart{}, errors.New("image data URL must be base64")
	}
	declared := strings.TrimPrefix(strings.TrimSuffix(meta, ";base64"), "image/")
	if declared == "jpg" {
		declared = "jpeg"
	}
	raw, err := decodeB64(data, maxImageBytes)
	if err != nil {
		return types.ContentPart{}, fmt.Errorf("image %s", err.Error())
	}
	actual := sniffImage(raw)
	if actual == "" {
		return types.ContentPart{}, errors.New("image must be png, jpeg, gif, or webp")
	}
	if actual != declared {
		return types.ContentPart{}, errors.New("image content does not match its declared type")
	}
	return types.ContentPart{Type: types.PartImage, Data: raw, Format: actual}, nil
}

func parseAudio(data, declared string) (types.ContentPart, error) {
	if declared != "wav" && declared != "mp3" {
		return types.ContentPart{}, errors.New("audio format must be wav or mp3")
	}
	raw, err := decodeB64(data, maxAudioBytes)
	if err != nil {
		return types.ContentPart{}, fmt.Errorf("audio %s", err.Error())
	}
	if sniffAudio(raw) != declared {
		return types.ContentPart{}, errors.New("audio content does not match its declared format")
	}
	return types.ContentPart{Type: types.PartAudio, Data: raw, Format: declared}, nil
}

func decodeB64(s string, limit int) ([]byte, error) {
	if base64.StdEncoding.DecodedLen(len(s)) > limit+2 {
		return nil, fmt.Errorf("exceeds %d MiB", limit>>20)
	}
	raw, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		return nil, errors.New("is not valid base64")
	}
	if len(raw) == 0 {
		return nil, errors.New("is empty")
	}
	if len(raw) > limit {
		return nil, fmt.Errorf("exceeds %d MiB", limit>>20)
	}
	return raw, nil
}

func sniffImage(b []byte) string {
	switch {
	case bytes.HasPrefix(b, []byte("\x89PNG\r\n\x1a\n")):
		return "png"
	case bytes.HasPrefix(b, []byte{0xFF, 0xD8, 0xFF}):
		return "jpeg"
	case bytes.HasPrefix(b, []byte("GIF87a")), bytes.HasPrefix(b, []byte("GIF89a")):
		return "gif"
	case len(b) >= 12 && string(b[:4]) == "RIFF" && string(b[8:12]) == "WEBP":
		return "webp"
	}
	return ""
}

func sniffAudio(b []byte) string {
	switch {
	case len(b) >= 12 && string(b[:4]) == "RIFF" && string(b[8:12]) == "WAVE":
		return "wav"
	case bytes.HasPrefix(b, []byte("ID3")), len(b) >= 2 && b[0] == 0xFF && b[1]&0xE0 == 0xE0:
		return "mp3"
	}
	return ""
}

func allowed(ds permeability.DirectionSets, field string) bool {
	for _, f := range ds.Outbound.Allow {
		if f == field {
			return true
		}
	}
	return false
}

// checkPermissions enforces the Pathway's media permissions before anything
// crosses: a media kind the operator has not opened is refused, not dropped.
func checkPermissions(ds permeability.DirectionSets, msgs []types.MultimodalMessage, spoken bool) error {
	if !allowed(ds, permMessages) {
		return forbidden("this pathway does not permit conversation messages")
	}
	for _, m := range msgs {
		for _, p := range m.Parts {
			if p.Type == types.PartImage && !allowed(ds, permImageInput) {
				return forbidden("image input is not permitted for this pathway")
			}
			if p.Type == types.PartAudio && !allowed(ds, permAudioInput) {
				return forbidden("audio input is not permitted for this pathway")
			}
		}
	}
	if spoken && !allowed(ds, permAudioOutput) {
		return forbidden("spoken replies are not permitted for this pathway")
	}
	return nil
}

func spokenReply(req wireRequest) (types.MultimodalAudioOutput, bool, error) {
	wantAudio := req.Audio != nil
	for _, m := range req.Modalities {
		switch m {
		case "text":
		case "audio":
			wantAudio = true
		default:
			return types.MultimodalAudioOutput{}, false, badRequest("modalities may only contain text and audio")
		}
	}
	if !wantAudio {
		return types.MultimodalAudioOutput{}, false, nil
	}
	out := types.MultimodalAudioOutput{Format: defaultReplyFmt}
	if req.Audio != nil {
		if req.Audio.Format != "" {
			out.Format = req.Audio.Format
		}
		out.Voice = req.Audio.Voice
	}
	if !replyFormats[out.Format] {
		return out, false, badRequest("audio.format must be one of wav, mp3, flac, opus, aac, pcm")
	}
	if out.Voice != "" && (len(out.Voice) > maxVoiceLen || !voicePattern.MatchString(out.Voice)) {
		return out, false, badRequest("audio.voice is not a valid voice name")
	}
	return out, true, nil
}

// --- request flow ---

func (h *Handler) observe(ctx context.Context, callerID string, capID types.PathwayID, outcome metrics.Outcome, latency time.Duration, usage types.Usage, providerFamily, model string) {
	defer h.obs.OnComplete(ctx, pipeline.CompleteEvent{
		RequestID:        pipeline.RequestIDFrom(ctx),
		CallerID:         callerID,
		PathwayID:        string(capID),
		Operation:        string(types.OperationMultimodalChat),
		Protection:       pipeline.ProtectionFrom(ctx),
		Outcome:          string(outcome),
		DurationMillis:   latency.Milliseconds(),
		PromptTokens:     usage.PromptTokens,
		CompletionTokens: usage.CompletionTokens,
		TotalTokens:      usage.PromptTokens + usage.CompletionTokens,
		ProviderFamily:   providerFamily,
		Model:            model,
	})
	h.metrics.RecordChat(string(capID), false, outcome, latency, usage.PromptTokens, usage.CompletionTokens, usage.PromptTokens+usage.CompletionTokens)
	h.audit.Write(audit.Record{
		SchemaVersion:    audit.SchemaVersion,
		Timestamp:        h.now(),
		CallerID:         callerID,
		PathwayID:        capID,
		Outcome:          string(outcome),
		DurationMillis:   latency.Milliseconds(),
		PromptTokens:     usage.PromptTokens,
		CompletionTokens: usage.CompletionTokens,
		TotalTokens:      usage.PromptTokens + usage.CompletionTokens,
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
	body, err := io.ReadAll(r.Body)
	if err != nil {
		var mbErr *http.MaxBytesError
		if errors.As(err, &mbErr) {
			writeError(w, http.StatusRequestEntityTooLarge, codePayloadTooLarge, "request exceeds the maximum allowed size")
			return
		}
		writeError(w, http.StatusBadRequest, codeBadRequest, "unreadable request body")
		return
	}
	var req wireRequest
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, codeBadRequest, "malformed request body")
		return
	}
	body = nil
	if req.Pathway == "" {
		writeError(w, http.StatusBadRequest, codeBadRequest, "pathway is required")
		return
	}
	reveal := req.Reveal || r.URL.Query().Get("reveal") == "true"
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
	if op, ok := snap.Resolver.Operation(capID); !ok || op != types.OperationMultimodalChat {
		writeError(w, http.StatusBadRequest, codeUnresolvedPathway, "no binding for the requested pathway")
		return
	}
	ds, ok := snap.Permeability.Pathways[capID]
	if !ok {
		writeError(w, http.StatusBadRequest, codeUnresolvedPathway, "no binding for the requested pathway")
		return
	}

	msgs, err := parseMessages(req.Messages)
	if err == nil {
		err = validateParams(req)
	}
	var spoken types.MultimodalAudioOutput
	var wantSpoken bool
	if err == nil {
		spoken, wantSpoken, err = spokenReply(req)
	}
	if err == nil {
		err = checkPermissions(ds, msgs, wantSpoken)
	}
	if err != nil {
		var ve validationError
		if errors.As(err, &ve) {
			writeError(w, ve.status, ve.code, ve.message)
			return
		}
		writeError(w, http.StatusBadRequest, codeBadRequest, "invalid request")
		return
	}

	plan, err := snap.Resolver.Plan(capID, nil)
	if err != nil {
		h.logger.Error("plan failed", "pathway", string(capID), "error", err.Error())
		writeError(w, http.StatusInternalServerError, codeInternal, "internal error")
		return
	}

	reqID := pipeline.NewRequestID()
	ctx := pipeline.WithRequestID(r.Context(), reqID)
	h.obs.OnRequest(ctx, pipeline.RequestEvent{
		RequestID: reqID,
		CallerID:  caller.ID,
		PathwayID: string(capID),
		Operation: string(types.OperationMultimodalChat),
	})
	start := time.Now()
	protection, err := h.ingress(ctx, reqID, caller.ID, capID, r.Header.Get(pipeline.SessionHeader), msgs)
	ctx = pipeline.WithProtection(ctx, protection)
	if err != nil {
		h.observe(ctx, caller.ID, capID, metrics.OutcomeFailedClosed, time.Since(start), types.Usage{}, "", "")
		status, code, msg := pipeline.GuardFailure(err)
		pipeline.LogGuardFailure(h.logger, string(capID), status, err)
		writeError(w, status, code, msg)
		return
	}

	payload := map[string]any{"messages": msgs}
	if wantSpoken {
		payload["audio_output"] = spoken
	}
	if req.MaxTokens != nil {
		payload["max_tokens"] = *req.MaxTokens
	}
	if req.Temperature != nil {
		payload["temperature"] = *req.Temperature
	}
	canonical := types.CanonicalRequest{PathwayID: capID, Payload: payload, Reveal: reveal, Caller: caller}
	sr, err := scrub.Sanitize(canonical, snap.Permeability)
	if err != nil {
		h.logger.Error("scrub failed", "pathway", string(capID), "error", err.Error())
		writeError(w, http.StatusInternalServerError, codeInternal, "internal error")
		return
	}

	mt, ok := snap.Transport.(transporter.MultimodalTransporter)
	if !ok {
		writeError(w, http.StatusServiceUnavailable, codeUnavailable, "multimodal chat is not available")
		return
	}
	resp, err := mt.TransportMultimodal(transporter.WithPlan(ctx, planTiers(plan)), sr)
	if err != nil {
		h.observe(ctx, caller.ID, capID, outcomeFor(err), time.Since(start), types.Usage{}, "", "")
		h.writeTransportError(w, err, reveal)
		return
	}
	family, model := string(resp.Provider), resp.Model

	scrubbed, err := scrub.ScrubMultimodal(resp, snap.Permeability, capID, reveal)
	if err != nil {
		h.observe(ctx, caller.ID, capID, metrics.OutcomeInternalError, time.Since(start), resp.Usage, family, model)
		h.logger.Error("inward scrub failed", "pathway", string(capID), "error", err.Error())
		writeError(w, http.StatusInternalServerError, codeInternal, "internal error")
		return
	}

	if len(scrubbed.Audio) > 0 && strings.TrimSpace(scrubbed.Transcript) == "" {
		h.observe(ctx, caller.ID, capID, metrics.OutcomeFailedClosed, time.Since(start), scrubbed.Usage, family, model)
		writeError(w, http.StatusBadGateway, codeProviderError, "the spoken reply could not be verified")
		return
	}
	inbound := traceReply(scrubbed, hasMedia(msgs))
	h.obs.OnProviderExchange(ctx, pipeline.ProviderExchangeEvent{
		RequestID:  reqID,
		CallerID:   caller.ID,
		PathwayID:  string(capID),
		Operation:  string(types.OperationMultimodalChat),
		Protection: protection,
		Outbound:   traceTurns(msgs),
		Inbound:    &inbound,
	})
	if err := h.egress(ctx, reqID, &scrubbed); err != nil {
		h.observe(ctx, caller.ID, capID, metrics.OutcomeFailedClosed, time.Since(start), scrubbed.Usage, family, model)
		h.logger.Error("content egress failed", "pathway", string(capID), "error", err.Error())
		writeError(w, http.StatusInternalServerError, codeInternal, "internal error")
		return
	}

	h.observe(ctx, caller.ID, capID, metrics.OutcomeSuccess, time.Since(start), scrubbed.Usage, family, model)
	out := types.MultimodalResponse{
		Pathway:      string(capID),
		Message:      types.MultimodalReplyMessage{Role: "assistant", Content: scrubbed.Text},
		FinishReason: scrubbed.FinishReason,
		Usage:        scrubbed.Usage,
		Provider:     string(scrubbed.Provider),
		Model:        scrubbed.Model,
	}
	if len(scrubbed.Audio) > 0 {
		out.Audio = &types.MultimodalReplyAudio{Data: scrubbed.Audio, Format: scrubbed.AudioFormat, Transcript: scrubbed.Transcript}
		if out.Message.Content == "" {
			out.Message.Content = scrubbed.Transcript
		}
	}
	writeJSON(w, http.StatusOK, out)
}

func validateParams(req wireRequest) error {
	if req.MaxTokens != nil && (*req.MaxTokens < 1 || *req.MaxTokens > maxMaxTokens) {
		return badRequest("max_tokens must be between 1 and %d", maxMaxTokens)
	}
	if req.Temperature != nil && (*req.Temperature < 0 || *req.Temperature > 2) {
		return badRequest("temperature must be between 0 and 2")
	}
	return nil
}

// ingress runs the content barrier over every text part of every turn (the
// whole history, every request) and writes the barrier's output back in place.
func (h *Handler) ingress(ctx context.Context, reqID, callerID string, capID types.PathwayID, sessionID string, msgs []types.MultimodalMessage) (pipeline.ProtectionMarker, error) {
	type ref struct{ m, p int }
	var refs []ref
	var texts []types.Message
	for i, m := range msgs {
		for j, p := range m.Parts {
			if p.Type == types.PartText {
				refs = append(refs, ref{i, j})
				texts = append(texts, types.Message{Role: m.Role, Content: p.Text})
			}
		}
	}
	rc := &pipeline.RequestContext{RequestID: reqID, CallerID: callerID, PathwayID: string(capID), Operation: string(types.OperationMultimodalChat), Messages: texts, SessionID: sessionID}
	if err := h.stage.Ingress(ctx, rc); err != nil {
		return rc.Protection, err
	}
	if len(rc.Messages) != len(refs) {
		return rc.Protection, errors.New("content stage changed the number of turns")
	}
	for k, rf := range refs {
		msgs[rf.m].Parts[rf.p].Text = rc.Messages[k].Content
	}
	return rc.Protection, nil
}

// egressSep separates the reply text from the transcript during restoration.
// The barrier restores a unary response once (and then discards its token
// map), so both are restored in a single pass.
const egressSep = "\x00"

var errEgressShape = errors.New("restored reply does not match its parts")

// egress runs the content barrier over the reply text and the spoken reply's
// transcript together.
func (h *Handler) egress(ctx context.Context, reqID string, res *types.CanonicalMultimodal) error {
	if strings.Contains(res.Text, egressSep) || strings.Contains(res.Transcript, egressSep) {
		return errEgressShape
	}
	reply := types.Message{Role: "assistant", Content: res.Text + egressSep + res.Transcript}
	if err := h.stage.Egress(ctx, &pipeline.ResponseContext{RequestID: reqID, Message: &reply}); err != nil {
		return err
	}
	text, transcript, ok := strings.Cut(reply.Content, egressSep)
	if !ok || strings.Contains(transcript, egressSep) {
		return errEgressShape
	}
	res.Text, res.Transcript = text, transcript
	return nil
}

// traceTurns is the Provider-facing request for the observation trace: each turn's
// (sanitized) text parts in order, with every image or audio part replaced by
// a content-free descriptor.
func traceTurns(msgs []types.MultimodalMessage) []types.Message {
	out := make([]types.Message, len(msgs))
	for i, m := range msgs {
		var b strings.Builder
		for j, p := range m.Parts {
			if j > 0 {
				b.WriteString("\n")
			}
			switch p.Type {
			case types.PartText:
				b.WriteString(p.Text)
			case types.PartImage:
				b.WriteString(pipeline.MediaDescriptor("image", "image/"+p.Format, len(p.Data), 0))
			case types.PartAudio:
				b.WriteString(pipeline.MediaDescriptor("audio", "audio/"+p.Format, len(p.Data), 0))
			default:
				b.WriteString(pipeline.MediaDescriptor("media", p.Format, len(p.Data), 0))
			}
		}
		out[i] = types.Message{Role: m.Role, Content: b.String()}
	}
	return out
}

// traceReply is the (sanitized, pre-restoration) reply for the observation trace: the
// text, and for a spoken reply a descriptor of the audio and its transcript.
// When the request carried media, the model saw content the barrier could not
// read and may repeat it, so the reply text and transcript are recorded only
// as their lengths.
func traceReply(res types.CanonicalMultimodal, media bool) types.Message {
	content := res.Text
	if media && content != "" {
		content = fmt.Sprintf("[reply: %d chars]", len([]rune(content)))
	}
	if len(res.Audio) > 0 {
		if content != "" {
			content += "\n"
		}
		content += pipeline.MediaDescriptor("audio", "audio/"+res.AudioFormat, len(res.Audio), 0)
		if media {
			content += fmt.Sprintf(" [transcript: %d chars]", len([]rune(res.Transcript)))
		} else {
			content += " transcript: " + res.Transcript
		}
	}
	return types.Message{Role: "assistant", Content: content}
}

// hasMedia reports whether any turn carries a non-text part.
func hasMedia(msgs []types.MultimodalMessage) bool {
	for _, m := range msgs {
		for _, p := range m.Parts {
			if p.Type != types.PartText {
				return true
			}
		}
	}
	return false
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
			"the requested media or reply is not supported by this pathway's providers"}
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
