// Package gemini implements a Provider adapter for the Google Gemini
// generateContent API. Chat maps the canonical conversation to Gemini's
// contents, systemInstruction, and generationConfig fields and to its function
// calling. Requests are built only from a providers.Request and the
// EgressIdentity; the AI Studio API key is sent as the x-goog-api-key header.
// The adapter has no access to Caller identity, the Caller Key, or the Reveal
// flag.
package gemini

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/inferencepath/membrane/internal/exterior/providers"
	"github.com/inferencepath/membrane/internal/exterior/providers/streamhelper"
	"github.com/inferencepath/membrane/internal/types"
)

const (
	// defaultBaseURL is the public AI Studio endpoint.
	defaultBaseURL = "https://generativelanguage.googleapis.com"
	// apiVersion pins the Gemini REST API version.
	apiVersion = "v1beta"
	// defaultIdleTimeout bounds inactivity on a streaming response when unset.
	defaultIdleTimeout = 30 * time.Second
)

// Adapter is a Google Gemini Provider adapter. It supports AI Studio (API key,
// the default) and Vertex AI (GCP project and location, OAuth bearer token),
// which differ only in endpoint and auth.
type Adapter struct {
	id          types.ProviderID
	baseURL     string
	idleTimeout time.Duration
	client      *http.Client

	// vertex selects the Vertex AI endpoint with a project and location path.
	// Otherwise the adapter targets AI Studio.
	vertex   bool
	project  string
	location string
	// tokenSource, when set, supplies an OAuth bearer token per request in place
	// of the x-goog-api-key header.
	tokenSource func(context.Context) (string, error)
}

// Option configures a Gemini Adapter.
type Option func(*Adapter)

// WithVertex selects Vertex AI for the given GCP project and location, using
// the regional aiplatform host. Neither value is disclosed to a Caller.
func WithVertex(project, location string) Option {
	return func(a *Adapter) {
		a.vertex = true
		a.project = project
		a.location = location
	}
}

// WithTokenSource sets an OAuth bearer token source, such as Application
// Default Credentials, called per request so tokens can refresh. It replaces
// x-goog-api-key auth.
func WithTokenSource(src func(context.Context) (string, error)) Option {
	return func(a *Adapter) { a.tokenSource = src }
}

// New returns a Gemini adapter. An empty baseURL selects the public AI Studio
// endpoint, or the regional Vertex host with WithVertex. idleTimeout bounds
// inactivity on streaming responses; a value <= 0 selects the default. The
// reported ID is "gemini".
func New(baseURL string, idleTimeout time.Duration, opts ...Option) *Adapter {
	if idleTimeout <= 0 {
		idleTimeout = defaultIdleTimeout
	}
	a := &Adapter{
		id:          "gemini",
		baseURL:     strings.TrimRight(baseURL, "/"),
		idleTimeout: idleTimeout,
		client:      &http.Client{},
	}
	for _, o := range opts {
		o(a)
	}
	return a
}

// ID implements providers.Provider.
func (a *Adapter) ID() types.ProviderID { return a.id }

// base returns the endpoint host. A configured baseURL takes precedence over
// the AI Studio or regional Vertex host.
func (a *Adapter) base() string {
	if a.baseURL != "" {
		return a.baseURL
	}
	if a.vertex {
		return fmt.Sprintf("https://%s-aiplatform.googleapis.com", a.location)
	}
	return defaultBaseURL
}

func (a *Adapter) generateURL(model, method, query string) string {
	var path string
	if a.vertex {
		path = fmt.Sprintf("/v1/projects/%s/locations/%s/publishers/google/models/%s:%s",
			url.PathEscape(a.project), url.PathEscape(a.location), url.PathEscape(model), method)
	} else {
		path = fmt.Sprintf("/%s/models/%s:%s", apiVersion, url.PathEscape(model), method)
	}
	u := a.base() + path
	if query != "" {
		u += "?" + query
	}
	return u
}

// buildBody maps the neutral chat payload to a Gemini generateContent request.
// System turns move to systemInstruction, assistant tool_calls become
// functionCall parts, tool results become functionResponse parts, and sampling
// parameters map into generationConfig.
func (a *Adapter) buildBody(req providers.Request) ([]byte, error) {
	system, contents := geminiContents(req.Payload["messages"])

	body := map[string]any{"contents": contents}
	if system != "" {
		body["systemInstruction"] = map[string]any{"parts": []any{map[string]any{"text": system}}}
	}

	gen := map[string]any{}
	if v, ok := asInt(req.Payload["max_tokens"]); ok {
		gen["maxOutputTokens"] = v
	}
	if v, ok := req.Payload["temperature"]; ok {
		gen["temperature"] = v
	}
	if v, ok := req.Payload["top_p"]; ok {
		gen["topP"] = v
	}
	if ss := stopSequences(req.Payload["stop"]); len(ss) > 0 {
		gen["stopSequences"] = ss
	}
	if len(gen) > 0 {
		body["generationConfig"] = gen
	}

	if tools := geminiTools(req.Payload["tools"]); tools != nil {
		body["tools"] = tools
		if tc := toolConfig(req.Payload["tool_choice"]); tc != nil {
			body["toolConfig"] = tc
		}
	}
	return json.Marshal(body)
}

// geminiContents converts the neutral messages array into Gemini contents and a
// system instruction string. "assistant" maps to Gemini's "model" role, a
// tool-result turn (role "tool") becomes a user turn with a functionResponse
// part, and assistant tool_calls become functionCall parts.
func geminiContents(raw any) (system string, contents []any) {
	arr, ok := raw.([]any)
	if !ok {
		return "", []any{}
	}
	var sys strings.Builder
	contents = make([]any, 0, len(arr))
	for _, m := range arr {
		mm, ok := m.(map[string]any)
		if !ok {
			continue
		}
		role, _ := mm["role"].(string)
		text, _ := mm["content"].(string)
		switch role {
		case "system":
			if sys.Len() > 0 {
				sys.WriteString("\n\n")
			}
			sys.WriteString(text)
		case "tool":
			name, _ := mm["name"].(string)
			part := map[string]any{"functionResponse": map[string]any{
				"name":     name,
				"response": toolResponse(text),
			}}
			contents = append(contents, map[string]any{"role": "user", "parts": []any{part}})
		case "assistant":
			parts := make([]any, 0, 2)
			if text != "" {
				parts = append(parts, map[string]any{"text": text})
			}
			if tcs, ok := mm["tool_calls"].([]any); ok {
				for _, tc := range tcs {
					tcm, ok := tc.(map[string]any)
					if !ok {
						continue
					}
					call := map[string]any{"name": tcm["name"]}
					if args, ok := tcm["arguments"]; ok {
						call["args"] = asObject(args)
					} else {
						call["args"] = map[string]any{}
					}
					parts = append(parts, map[string]any{"functionCall": call})
				}
			}
			if len(parts) == 0 {
				parts = append(parts, map[string]any{"text": ""})
			}
			contents = append(contents, map[string]any{"role": "model", "parts": parts})
		default: // user and unknown roles
			contents = append(contents, map[string]any{
				"role":  "user",
				"parts": []any{map[string]any{"text": text}},
			})
		}
	}
	return sys.String(), contents
}

// geminiTools maps the neutral tools payload into a single Gemini tools entry
// carrying functionDeclarations, or nil when the request offers no tools.
func geminiTools(raw any) any {
	arr, ok := raw.([]any)
	if !ok || len(arr) == 0 {
		return nil
	}
	decls := make([]any, 0, len(arr))
	for _, e := range arr {
		m, ok := e.(map[string]any)
		if !ok {
			continue
		}
		decl := map[string]any{"name": m["name"]}
		if d, ok := m["description"]; ok {
			decl["description"] = d
		}
		if p, ok := m["parameters"]; ok {
			decl["parameters"] = p
		}
		decls = append(decls, decl)
	}
	return []any{map[string]any{"functionDeclarations": decls}}
}

// toolConfig maps the neutral (OpenAI-style) tool_choice directive into the
// Gemini functionCallingConfig. "none", "auto", and "required" or "any" map to
// NONE, AUTO, and ANY; a named function maps to ANY with allowedFunctionNames.
// A nil result leaves the model default.
func toolConfig(raw any) map[string]any {
	if raw == nil {
		return nil
	}
	var v any
	switch r := raw.(type) {
	case json.RawMessage:
		if json.Unmarshal(r, &v) != nil {
			return nil
		}
	case []byte:
		if json.Unmarshal(r, &v) != nil {
			return nil
		}
	default:
		v = raw
	}
	fcc := map[string]any{}
	switch c := v.(type) {
	case string:
		switch c {
		case "auto":
			fcc["mode"] = "AUTO"
		case "none":
			fcc["mode"] = "NONE"
		case "required", "any":
			fcc["mode"] = "ANY"
		default:
			return nil
		}
	case map[string]any:
		if fn, ok := c["function"].(map[string]any); ok {
			if name, ok := fn["name"]; ok {
				fcc["mode"] = "ANY"
				fcc["allowedFunctionNames"] = []any{name}
			}
		}
		if len(fcc) == 0 {
			return nil
		}
	default:
		return nil
	}
	return map[string]any{"functionCallingConfig": fcc}
}

// toolResponse renders tool-role result content as the Struct Gemini expects for
// a functionResponse. A JSON object is used as-is; anything else is wrapped.
func toolResponse(content string) map[string]any {
	trimmed := strings.TrimSpace(content)
	if strings.HasPrefix(trimmed, "{") {
		var obj map[string]any
		if json.Unmarshal([]byte(trimmed), &obj) == nil {
			return obj
		}
	}
	return map[string]any{"result": content}
}

// asObject coerces an opaque arguments value into a JSON object for Gemini's
// functionCall.args (which must be a Struct).
func asObject(v any) any {
	switch x := v.(type) {
	case json.RawMessage:
		var obj any
		if json.Unmarshal(x, &obj) == nil {
			return obj
		}
		return map[string]any{}
	case []byte:
		var obj any
		if json.Unmarshal(x, &obj) == nil {
			return obj
		}
		return map[string]any{}
	case string:
		var obj any
		if json.Unmarshal([]byte(x), &obj) == nil {
			return obj
		}
		return map[string]any{}
	default:
		return v
	}
}

func asInt(v any) (int, bool) {
	switch n := v.(type) {
	case float64:
		return int(n), true
	case int:
		return n, true
	case int64:
		return int(n), true
	case json.Number:
		if i, err := n.Int64(); err == nil {
			return int(i), true
		}
	}
	return 0, false
}

func stopSequences(v any) []string {
	switch s := v.(type) {
	case string:
		if s == "" {
			return nil
		}
		return []string{s}
	case []any:
		out := make([]string, 0, len(s))
		for _, e := range s {
			if str, ok := e.(string); ok && str != "" {
				out = append(out, str)
			}
		}
		return out
	case []string:
		return s
	}
	return nil
}

func (a *Adapter) newHTTPRequest(ctx context.Context, urlStr string, body []byte, id providers.EgressIdentity, stream bool) (*http.Request, error) {
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, urlStr, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	// Only the Egress Identity and content negotiation headers are sent: no
	// Caller identity, Caller Key, forwarding, or correlation headers. Vertex uses
	// an OAuth bearer token; AI Studio uses the API key.
	if a.tokenSource != nil {
		token, terr := a.tokenSource(ctx)
		if terr != nil {
			return nil, fmt.Errorf("gemini: acquire token: %w", terr)
		}
		httpReq.Header.Set("Authorization", "Bearer "+token)
	} else {
		httpReq.Header.Set("x-goog-api-key", id.Secret)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if stream {
		httpReq.Header.Set("Accept", "text/event-stream")
	} else {
		httpReq.Header.Set("Accept", "application/json")
	}
	return httpReq, nil
}

func mapErr(ctx context.Context, err error) error {
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return providers.ErrUpstreamTimeout
	}
	return err
}

// generateResponse is the minimal shape of a Gemini generateContent reply.
type generateResponse struct {
	Candidates []struct {
		Content struct {
			Parts []part `json:"parts"`
		} `json:"content"`
		FinishReason string `json:"finishReason"`
	} `json:"candidates"`
	UsageMetadata struct {
		PromptTokenCount     int `json:"promptTokenCount"`
		CandidatesTokenCount int `json:"candidatesTokenCount"`
		TotalTokenCount      int `json:"totalTokenCount"`
	} `json:"usageMetadata"`
}

type part struct {
	Text         string `json:"text"`
	FunctionCall *struct {
		Name string          `json:"name"`
		Args json.RawMessage `json:"args"`
	} `json:"functionCall"`
}

// normalizeFinish maps a Gemini finishReason to the canonical finish_reason.
func normalizeFinish(reason string, hasToolCalls bool) string {
	if hasToolCalls {
		return "tool_calls"
	}
	switch reason {
	case "STOP":
		return "stop"
	case "MAX_TOKENS":
		return "length"
	case "":
		return ""
	default:
		return strings.ToLower(reason)
	}
}

// Complete implements providers.Provider for the non-streaming path.
func (a *Adapter) Complete(ctx context.Context, req providers.Request, id providers.EgressIdentity) (types.CanonicalResponse, error) {
	body, err := a.buildBody(req)
	if err != nil {
		return types.CanonicalResponse{}, fmt.Errorf("gemini: marshal request: %w", err)
	}
	httpReq, err := a.newHTTPRequest(ctx, a.generateURL(req.Model, "generateContent", ""), body, id, false)
	if err != nil {
		return types.CanonicalResponse{}, err
	}

	resp, err := a.client.Do(httpReq)
	if err != nil {
		return types.CanonicalResponse{}, mapErr(ctx, err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode/100 != 2 {
		return types.CanonicalResponse{}, providers.UpstreamStatusError{Status: resp.StatusCode, Provider: a.id}
	}

	var gr generateResponse
	if err := json.NewDecoder(resp.Body).Decode(&gr); err != nil {
		return types.CanonicalResponse{}, fmt.Errorf("gemini: decode response: %w", err)
	}

	msg := types.Message{Role: "assistant"}
	var text strings.Builder
	var finishReason string
	if len(gr.Candidates) > 0 {
		c := gr.Candidates[0]
		finishReason = c.FinishReason
		for _, p := range c.Content.Parts {
			if p.FunctionCall != nil {
				msg.ToolCalls = append(msg.ToolCalls, types.ToolCall{
					Name:      p.FunctionCall.Name,
					Arguments: p.FunctionCall.Args,
				})
				continue
			}
			text.WriteString(p.Text)
		}
	}
	msg.Content = text.String()

	total := gr.UsageMetadata.TotalTokenCount
	if total == 0 {
		total = gr.UsageMetadata.PromptTokenCount + gr.UsageMetadata.CandidatesTokenCount
	}
	return types.CanonicalResponse{
		Message:      msg,
		FinishReason: normalizeFinish(finishReason, len(msg.ToolCalls) > 0),
		Usage: types.Usage{
			PromptTokens:     gr.UsageMetadata.PromptTokenCount,
			CompletionTokens: gr.UsageMetadata.CandidatesTokenCount,
			TotalTokens:      total,
		},
		Provider: a.id,
		Model:    req.Model,
	}, nil
}

// streamResponse is the minimal shape of one streamed generateContent chunk.
type streamResponse struct {
	Candidates []struct {
		Content struct {
			Parts []part `json:"parts"`
		} `json:"content"`
		FinishReason string `json:"finishReason"`
	} `json:"candidates"`
}

// Stream implements providers.Provider for the streaming (SSE) path. Text parts
// become Delta, each functionCall part becomes one tool-call delta with the full
// arguments (Gemini does not split them), and finishReason sets FinishReason.
func (a *Adapter) Stream(ctx context.Context, req providers.Request, id providers.EgressIdentity, emit func(types.StreamChunk) error) error {
	body, err := a.buildBody(req)
	if err != nil {
		return fmt.Errorf("gemini: marshal request: %w", err)
	}

	streamCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	httpReq, err := a.newHTTPRequest(streamCtx, a.generateURL(req.Model, "streamGenerateContent", "alt=sse"), body, id, true)
	if err != nil {
		return err
	}
	resp, err := a.client.Do(httpReq)
	if err != nil {
		return mapErr(ctx, err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode/100 != 2 {
		return providers.UpstreamStatusError{Status: resp.StatusCode, Provider: a.id}
	}

	toolIndex := 0
	err = streamhelper.PumpSSE(streamCtx, cancel, resp.Body, a.idleTimeout, func(data string) (bool, error) {
		var chunk streamResponse
		if json.Unmarshal([]byte(data), &chunk) != nil || len(chunk.Candidates) == 0 {
			return false, nil
		}
		cand := chunk.Candidates[0]
		for _, p := range cand.Content.Parts {
			if p.FunctionCall != nil {
				delta := types.StreamChunk{ToolCalls: []types.ToolCallDelta{{
					Index:             toolIndex,
					Name:              p.FunctionCall.Name,
					ArgumentsFragment: string(p.FunctionCall.Args),
				}}}
				toolIndex++
				if emitErr := emit(delta); emitErr != nil {
					return false, emitErr
				}
				continue
			}
			if p.Text != "" {
				if emitErr := emit(types.StreamChunk{Delta: p.Text}); emitErr != nil {
					return false, emitErr
				}
			}
		}
		if cand.FinishReason != "" {
			if emitErr := emit(types.StreamChunk{FinishReason: normalizeFinish(cand.FinishReason, false)}); emitErr != nil {
				return false, emitErr
			}
		}
		return false, nil
	})
	if err != nil {
		return err
	}
	return emit(types.StreamChunk{Done: true})
}
