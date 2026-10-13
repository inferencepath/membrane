// Package openai provides a capturing, OpenAI-compatible fake Provider for
// tests. It records the exact headers and body it receives so tests can assert
// that no caller/org identity, Caller Key, or control field crossed the Membrane
// (the backbone of the live Diffusion-leak suite). It can also emit SSE streams
// and simulate timeouts and error statuses.
package openai

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"time"
)

// ToolCall is a tool call the fake returns on an assistant completion message.
type ToolCall struct {
	ID        string
	Name      string
	Arguments string
}

// Capture is the exact upstream request the fake received.
type Capture struct {
	Method string
	Path   string
	Header http.Header
	Body   []byte
}

// Config controls the fake's behavior.
type Config struct {
	// Content is the assistant completion text for non-streaming responses.
	Content string
	// Model is the provider-reported model.
	Model string
	// FinishReason defaults to "stop" when empty.
	FinishReason string
	// Prompt/Completion/Total token counts reported in usage.
	PromptTokens     int
	CompletionTokens int
	TotalTokens      int
	// StreamDeltas, when set, are emitted as SSE content chunks for stream requests.
	StreamDeltas []string
	// ToolCalls, when set, are returned on the assistant message of a
	// non-streaming completion (OpenAI tool_calls wire shape).
	ToolCalls []ToolCall
	// EmbeddingVectors, when set, are returned (in order, index-tagged) by the
	// /v1/embeddings endpoint.
	EmbeddingVectors [][]float64
	// RerankResults, when set, are returned by the /v1/rerank endpoint. The
	// slice index is used as the result index and the value as relevance_score;
	// RerankDocuments (when set) supplies the echoed document text.
	RerankResults   []float64
	RerankDocuments []string
	// ResponsesOutput, when set, is returned (verbatim) as the output items of the
	// /v1/responses endpoint. ResponsesStatus sets the response status/finish.
	ResponsesOutput []map[string]any
	ResponsesStatus string
	// ImageURLs / ImageB64 supply the images returned by /v1/images/generations.
	ImageURLs []string
	ImageB64  []string
	// ModerationFlagged / ModerationCategories / ModerationScores shape the single
	// result returned by /v1/moderations.
	ModerationFlagged    bool
	ModerationCategories map[string]bool
	ModerationScores     map[string]float64
	// TranscriptionText / TranscriptionDuration shape the /v1/audio/transcriptions
	// response.
	TranscriptionText     string
	TranscriptionDuration float64
	// SpeechAudio is the binary audio body returned by /v1/audio/speech, with
	// SpeechContentType as its Content-Type (empty ⇒ audio/mpeg).
	SpeechAudio       []byte
	SpeechContentType string
	// Status, when set to a non-2xx value, is returned instead of a completion.
	Status int
	// Delay sleeps before responding (to exercise client-side timeouts).
	Delay time.Duration
}

// Fake is a running capturing fake Provider.
type Fake struct {
	server *httptest.Server
	mu     sync.Mutex
	cfg    Config
	last   Capture
}

// New starts a fake Provider with the given config.
func New(cfg Config) *Fake {
	f := &Fake{cfg: cfg}
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/chat/completions", f.handle)
	mux.HandleFunc("/v1/embeddings", f.handleEmbeddings)
	mux.HandleFunc("/v1/rerank", f.handleRerank)
	mux.HandleFunc("/v1/responses", f.handleResponses)
	mux.HandleFunc("/v1/images/generations", f.handleImages)
	mux.HandleFunc("/v1/moderations", f.handleModerations)
	mux.HandleFunc("/v1/audio/transcriptions", f.handleTranscription)
	mux.HandleFunc("/v1/audio/speech", f.handleSpeech)
	f.server = httptest.NewServer(mux)
	return f
}

// URL returns the fake's base URL (suitable as a Provider endpoint).
func (f *Fake) URL() string { return f.server.URL }

// Close shuts the fake down.
func (f *Fake) Close() { f.server.Close() }

// Last returns the most recently captured request.
func (f *Fake) Last() Capture {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.last
}

// SetConfig replaces the fake's behavior config.
func (f *Fake) SetConfig(cfg Config) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.cfg = cfg
}

func (f *Fake) handle(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	f.mu.Lock()
	f.last = Capture{Method: r.Method, Path: r.URL.Path, Header: r.Header.Clone(), Body: body}
	cfg := f.cfg
	f.mu.Unlock()

	if cfg.Delay > 0 {
		select {
		case <-time.After(cfg.Delay):
		case <-r.Context().Done():
			return
		}
	}

	if cfg.Status != 0 && cfg.Status/100 != 2 {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(cfg.Status)
		_, _ = io.WriteString(w, `{"error":{"message":"simulated upstream error"}}`)
		return
	}

	streamRequested := requestWantsStream(body)
	if streamRequested && len(cfg.StreamDeltas) > 0 {
		f.writeStream(w, r, cfg)
		return
	}
	f.writeCompletion(w, cfg)
}

func (f *Fake) handleEmbeddings(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	f.mu.Lock()
	f.last = Capture{Method: r.Method, Path: r.URL.Path, Header: r.Header.Clone(), Body: body}
	cfg := f.cfg
	f.mu.Unlock()

	if cfg.Delay > 0 {
		select {
		case <-time.After(cfg.Delay):
		case <-r.Context().Done():
			return
		}
	}

	if cfg.Status != 0 && cfg.Status/100 != 2 {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(cfg.Status)
		_, _ = io.WriteString(w, `{"error":{"message":"simulated upstream error"}}`)
		return
	}

	data := make([]any, 0, len(cfg.EmbeddingVectors))
	for i, vec := range cfg.EmbeddingVectors {
		data = append(data, map[string]any{"index": i, "object": "embedding", "embedding": vec})
	}
	resp := map[string]any{
		"object": "list",
		"model":  cfg.Model,
		"data":   data,
		"usage": map[string]any{
			"prompt_tokens": cfg.PromptTokens,
			"total_tokens":  cfg.TotalTokens,
		},
	}
	w.Header().Set("Content-Type", "application/json")
	// Identifying headers a real provider might send; the adapter must drop these.
	w.Header().Set("openai-organization", "org-fake")
	w.Header().Set("x-request-id", "req-fake-123")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(resp)
}

func (f *Fake) handleRerank(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	f.mu.Lock()
	f.last = Capture{Method: r.Method, Path: r.URL.Path, Header: r.Header.Clone(), Body: body}
	cfg := f.cfg
	f.mu.Unlock()

	if cfg.Delay > 0 {
		select {
		case <-time.After(cfg.Delay):
		case <-r.Context().Done():
			return
		}
	}

	if cfg.Status != 0 && cfg.Status/100 != 2 {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(cfg.Status)
		_, _ = io.WriteString(w, `{"error":{"message":"simulated upstream error"}}`)
		return
	}

	results := make([]any, 0, len(cfg.RerankResults))
	for i, score := range cfg.RerankResults {
		res := map[string]any{"index": i, "relevance_score": score}
		if i < len(cfg.RerankDocuments) {
			res["document"] = map[string]any{"text": cfg.RerankDocuments[i]}
		}
		results = append(results, res)
	}
	resp := map[string]any{
		"model":   cfg.Model,
		"results": results,
		"usage":   map[string]any{"total_tokens": cfg.TotalTokens},
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("openai-organization", "org-fake")
	w.Header().Set("x-request-id", "req-fake-123")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(resp)
}

func (f *Fake) handleResponses(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	f.mu.Lock()
	f.last = Capture{Method: r.Method, Path: r.URL.Path, Header: r.Header.Clone(), Body: body}
	cfg := f.cfg
	f.mu.Unlock()

	if cfg.Delay > 0 {
		select {
		case <-time.After(cfg.Delay):
		case <-r.Context().Done():
			return
		}
	}

	if cfg.Status != 0 && cfg.Status/100 != 2 {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(cfg.Status)
		_, _ = io.WriteString(w, `{"error":{"message":"simulated upstream error"}}`)
		return
	}

	if requestWantsStream(body) && len(cfg.StreamDeltas) > 0 {
		f.writeResponsesStream(w, r, cfg)
		return
	}

	status := cfg.ResponsesStatus
	if status == "" {
		status = "completed"
	}
	resp := map[string]any{
		"id":     "resp-fake",
		"model":  cfg.Model,
		"status": status,
		"output": cfg.ResponsesOutput,
		"usage": map[string]any{
			"input_tokens":  cfg.PromptTokens,
			"output_tokens": cfg.CompletionTokens,
			"total_tokens":  cfg.TotalTokens,
		},
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("openai-organization", "org-fake")
	w.Header().Set("x-request-id", "req-fake-123")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(resp)
}

func (f *Fake) writeResponsesStream(w http.ResponseWriter, r *http.Request, cfg Config) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.WriteHeader(http.StatusOK)

	for _, delta := range cfg.StreamDeltas {
		select {
		case <-r.Context().Done():
			return
		default:
		}
		chunk := map[string]any{"type": "response.output_text.delta", "delta": delta}
		b, _ := json.Marshal(chunk)
		_, _ = fmt.Fprintf(w, "data: %s\n\n", b)
		flusher.Flush()
	}
	done := map[string]any{"type": "response.completed"}
	b, _ := json.Marshal(done)
	_, _ = fmt.Fprintf(w, "data: %s\n\n", b)
	_, _ = io.WriteString(w, "data: [DONE]\n\n")
	flusher.Flush()
}

func (f *Fake) handleImages(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	f.mu.Lock()
	f.last = Capture{Method: r.Method, Path: r.URL.Path, Header: r.Header.Clone(), Body: body}
	cfg := f.cfg
	f.mu.Unlock()

	if cfg.Delay > 0 {
		select {
		case <-time.After(cfg.Delay):
		case <-r.Context().Done():
			return
		}
	}

	if cfg.Status != 0 && cfg.Status/100 != 2 {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(cfg.Status)
		_, _ = io.WriteString(w, `{"error":{"message":"simulated upstream error"}}`)
		return
	}

	data := make([]any, 0)
	for _, u := range cfg.ImageURLs {
		data = append(data, map[string]any{"url": u})
	}
	for _, b := range cfg.ImageB64 {
		data = append(data, map[string]any{"b64_json": b})
	}
	resp := map[string]any{"created": 0, "data": data}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("openai-organization", "org-fake")
	w.Header().Set("x-request-id", "req-fake-123")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(resp)
}

func (f *Fake) captureAndGate(w http.ResponseWriter, r *http.Request) (Config, bool) {
	body, _ := io.ReadAll(r.Body)
	f.mu.Lock()
	f.last = Capture{Method: r.Method, Path: r.URL.Path, Header: r.Header.Clone(), Body: body}
	cfg := f.cfg
	f.mu.Unlock()

	if cfg.Delay > 0 {
		select {
		case <-time.After(cfg.Delay):
		case <-r.Context().Done():
			return cfg, false
		}
	}
	if cfg.Status != 0 && cfg.Status/100 != 2 {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(cfg.Status)
		_, _ = io.WriteString(w, `{"error":{"message":"simulated upstream error"}}`)
		return cfg, false
	}
	return cfg, true
}

func (f *Fake) handleModerations(w http.ResponseWriter, r *http.Request) {
	cfg, ok := f.captureAndGate(w, r)
	if !ok {
		return
	}
	result := map[string]any{
		"flagged":         cfg.ModerationFlagged,
		"categories":      cfg.ModerationCategories,
		"category_scores": cfg.ModerationScores,
	}
	resp := map[string]any{"model": cfg.Model, "results": []any{result}}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("openai-organization", "org-fake")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(resp)
}

func (f *Fake) handleTranscription(w http.ResponseWriter, r *http.Request) {
	cfg, ok := f.captureAndGate(w, r)
	if !ok {
		return
	}
	resp := map[string]any{"text": cfg.TranscriptionText, "duration": cfg.TranscriptionDuration}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("openai-organization", "org-fake")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(resp)
}

func (f *Fake) handleSpeech(w http.ResponseWriter, r *http.Request) {
	cfg, ok := f.captureAndGate(w, r)
	if !ok {
		return
	}
	ct := cfg.SpeechContentType
	if ct == "" {
		ct = "audio/mpeg"
	}
	w.Header().Set("Content-Type", ct)
	w.Header().Set("openai-organization", "org-fake")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(cfg.SpeechAudio)
}

func requestWantsStream(body []byte) bool {
	var m struct {
		Stream bool `json:"stream"`
	}
	_ = json.Unmarshal(body, &m)
	return m.Stream
}

func (f *Fake) writeCompletion(w http.ResponseWriter, cfg Config) {
	finish := cfg.FinishReason
	if finish == "" {
		finish = "stop"
	}
	message := map[string]any{"role": "assistant", "content": cfg.Content}
	if len(cfg.ToolCalls) > 0 {
		tcs := make([]any, 0, len(cfg.ToolCalls))
		for _, tc := range cfg.ToolCalls {
			tcs = append(tcs, map[string]any{
				"id":       tc.ID,
				"type":     "function",
				"function": map[string]any{"name": tc.Name, "arguments": tc.Arguments},
			})
		}
		message["tool_calls"] = tcs
	}
	resp := map[string]any{
		"id":    "chatcmpl-fake",
		"model": cfg.Model,
		"choices": []any{
			map[string]any{
				"index":         0,
				"message":       message,
				"finish_reason": finish,
			},
		},
		"usage": map[string]any{
			"prompt_tokens":     cfg.PromptTokens,
			"completion_tokens": cfg.CompletionTokens,
			"total_tokens":      cfg.TotalTokens,
		},
	}
	w.Header().Set("Content-Type", "application/json")
	// Identifying headers a real provider might send; the adapter must drop these.
	w.Header().Set("openai-organization", "org-fake")
	w.Header().Set("x-request-id", "req-fake-123")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(resp)
}

func (f *Fake) writeStream(w http.ResponseWriter, r *http.Request, cfg Config) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.WriteHeader(http.StatusOK)

	finish := cfg.FinishReason
	if finish == "" {
		finish = "stop"
	}
	for _, delta := range cfg.StreamDeltas {
		select {
		case <-r.Context().Done():
			return
		default:
		}
		chunk := map[string]any{
			"choices": []any{
				map[string]any{"index": 0, "delta": map[string]any{"content": delta}},
			},
		}
		b, _ := json.Marshal(chunk)
		_, _ = fmt.Fprintf(w, "data: %s\n\n", b)
		flusher.Flush()
	}
	// Terminal content chunk carrying the finish reason, then [DONE].
	final := map[string]any{
		"choices": []any{
			map[string]any{"index": 0, "delta": map[string]any{}, "finish_reason": finish},
		},
	}
	b, _ := json.Marshal(final)
	_, _ = fmt.Fprintf(w, "data: %s\n\n", b)
	_, _ = io.WriteString(w, "data: [DONE]\n\n")
	flusher.Flush()
}
