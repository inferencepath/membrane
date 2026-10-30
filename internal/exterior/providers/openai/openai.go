// Package openai implements a Provider adapter for OpenAI and OpenAI-compatible
// servers such as vLLM and Ollama. Requests are built only from a
// providers.Request and the EgressIdentity; the adapter has no access to Caller
// identity, the Caller Key, or the Reveal flag.
package openai

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"strings"
	"time"

	"github.com/inferencepath/membrane/internal/exterior/providers"
	"github.com/inferencepath/membrane/internal/exterior/providers/oaitools"
	"github.com/inferencepath/membrane/internal/types"
)

const chatCompletionsPath = "/chat/completions"
const embeddingsPath = "/embeddings"
const rerankPath = "/rerank"
const responsesPath = "/responses"
const imagesGenerationsPath = "/images/generations"
const moderationsPath = "/moderations"
const audioTranscriptionsPath = "/audio/transcriptions"
const audioSpeechPath = "/audio/speech"

// defaultIdleTimeout bounds inactivity on a streaming response when none is set.
const defaultIdleTimeout = 30 * time.Second

// Adapter is an OpenAI-compatible Provider adapter.
type Adapter struct {
	id          types.ProviderID
	baseURL     string
	idleTimeout time.Duration
	client      *http.Client
}

// New returns an adapter for the given Provider id and base URL. idleTimeout
// bounds inactivity on streaming responses; a value <= 0 selects the default.
func New(id types.ProviderID, baseURL string, idleTimeout time.Duration) *Adapter {
	if idleTimeout <= 0 {
		idleTimeout = defaultIdleTimeout
	}
	return &Adapter{
		id:          id,
		baseURL:     strings.TrimRight(baseURL, "/"),
		idleTimeout: idleTimeout,
		client:      &http.Client{},
	}
}

// ID implements providers.Provider.
func (a *Adapter) ID() types.ProviderID { return a.id }

// buildBody builds the request body from the allowlisted payload plus the
// resolved model and stream flag. No other fields are added.
func (a *Adapter) buildBody(req providers.Request) ([]byte, error) {
	body := make(map[string]any, len(req.Payload)+2)
	for k, v := range req.Payload {
		body[k] = v
	}
	body["model"] = req.Model
	body["stream"] = req.Stream
	return json.Marshal(body)
}

// buildChatBody is buildBody plus mapping of tools and assistant tool_calls to
// the OpenAI schema. Without tools its output matches buildBody.
func (a *Adapter) buildChatBody(req providers.Request) ([]byte, error) {
	body := make(map[string]any, len(req.Payload)+2)
	for k, v := range req.Payload {
		body[k] = v
	}
	body["model"] = req.Model
	body["stream"] = req.Stream
	if raw, ok := body["tools"]; ok {
		body["tools"] = oaitools.Tools(raw)
	}
	if raw, ok := body["messages"]; ok {
		body["messages"] = oaitools.Messages(raw)
	}
	return json.Marshal(body)
}

func (a *Adapter) newHTTPRequest(ctx context.Context, body []byte, id providers.EgressIdentity, stream bool) (*http.Request, error) {
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, chatCompletionsURL(a.baseURL), bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	// Only the Egress Identity and content negotiation headers are sent: no
	// Caller identity, Caller Key, forwarding, or correlation headers.
	httpReq.Header.Set("Authorization", "Bearer "+id.Secret)
	httpReq.Header.Set("Content-Type", "application/json")
	if stream {
		httpReq.Header.Set("Accept", "text/event-stream")
	} else {
		httpReq.Header.Set("Accept", "application/json")
	}
	return httpReq, nil
}

// chatCompletionsURL accepts a base URL with or without a trailing /v1, such as
// https://api.openai.com or https://api.openai.com/v1, and does not duplicate
// /v1.
func chatCompletionsURL(base string) string {
	b := strings.TrimRight(base, "/")
	if strings.HasSuffix(b, "/v1") {
		return b + chatCompletionsPath
	}
	return b + "/v1" + chatCompletionsPath
}

// embeddingsURL is chatCompletionsURL for the /embeddings endpoint.
func embeddingsURL(base string) string {
	b := strings.TrimRight(base, "/")
	if strings.HasSuffix(b, "/v1") {
		return b + embeddingsPath
	}
	return b + "/v1" + embeddingsPath
}

// buildEmbeddingsBody builds a request body from the allowlisted payload plus
// the resolved model. Unlike buildBody it does not set "stream".
func (a *Adapter) buildEmbeddingsBody(req providers.Request) ([]byte, error) {
	body := make(map[string]any, len(req.Payload)+1)
	for k, v := range req.Payload {
		body[k] = v
	}
	body["model"] = req.Model
	return json.Marshal(body)
}

type oaEmbeddingsResponse struct {
	Model string `json:"model"`
	Data  []struct {
		Index     int             `json:"index"`
		Embedding json.RawMessage `json:"embedding"`
	} `json:"data"`
	Usage struct {
		PromptTokens int `json:"prompt_tokens"`
		TotalTokens  int `json:"total_tokens"`
	} `json:"usage"`
}

// Embeddings implements providers.EmbeddingsProvider. Each embedding passes
// through as-is: a float array or a base64 string, depending on encoding_format.
func (a *Adapter) Embeddings(ctx context.Context, req providers.Request, id providers.EgressIdentity) (types.CanonicalEmbeddings, error) {
	body, err := a.buildEmbeddingsBody(req)
	if err != nil {
		return types.CanonicalEmbeddings{}, fmt.Errorf("openai: marshal request: %w", err)
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, embeddingsURL(a.baseURL), bytes.NewReader(body))
	if err != nil {
		return types.CanonicalEmbeddings{}, err
	}
	httpReq.Header.Set("Authorization", "Bearer "+id.Secret)
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "application/json")

	resp, err := a.client.Do(httpReq)
	if err != nil {
		return types.CanonicalEmbeddings{}, mapErr(ctx, err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode/100 != 2 {
		return types.CanonicalEmbeddings{}, providers.UpstreamStatusError{Status: resp.StatusCode, Provider: a.id}
	}

	var oa oaEmbeddingsResponse
	if err := json.NewDecoder(resp.Body).Decode(&oa); err != nil {
		return types.CanonicalEmbeddings{}, fmt.Errorf("openai: decode response: %w", err)
	}

	data := make([]types.EmbeddingVector, 0, len(oa.Data))
	for _, d := range oa.Data {
		var emb any
		if len(d.Embedding) > 0 {
			if err := json.Unmarshal(d.Embedding, &emb); err != nil {
				return types.CanonicalEmbeddings{}, fmt.Errorf("openai: decode embedding: %w", err)
			}
		}
		data = append(data, types.EmbeddingVector{Index: d.Index, Embedding: emb})
	}

	return types.CanonicalEmbeddings{
		Data: data,
		Usage: types.EmbeddingsUsage{
			PromptTokens: oa.Usage.PromptTokens,
			TotalTokens:  oa.Usage.TotalTokens,
		},
		Provider: a.id,
		Model:    oa.Model,
	}, nil
}

// rerankURL is chatCompletionsURL for the /rerank endpoint.
func rerankURL(base string) string {
	b := strings.TrimRight(base, "/")
	if strings.HasSuffix(b, "/v1") {
		return b + rerankPath
	}
	return b + "/v1" + rerankPath
}

type oaRerankResponse struct {
	Model   string `json:"model"`
	Results []struct {
		Index          int     `json:"index"`
		RelevanceScore float64 `json:"relevance_score"`
		Document       *struct {
			Text string `json:"text"`
		} `json:"document"`
	} `json:"results"`
	Usage struct {
		TotalTokens int `json:"total_tokens"`
	} `json:"usage"`
}

// Rerank implements providers.RerankProvider. The response follows the
// Cohere and Jina rerank schema that OpenAI-compatible servers use.
func (a *Adapter) Rerank(ctx context.Context, req providers.Request, id providers.EgressIdentity) (types.CanonicalRerank, error) {
	body, err := a.buildEmbeddingsBody(req) // payload and model, no stream
	if err != nil {
		return types.CanonicalRerank{}, fmt.Errorf("openai: marshal request: %w", err)
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, rerankURL(a.baseURL), bytes.NewReader(body))
	if err != nil {
		return types.CanonicalRerank{}, err
	}
	httpReq.Header.Set("Authorization", "Bearer "+id.Secret)
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "application/json")

	resp, err := a.client.Do(httpReq)
	if err != nil {
		return types.CanonicalRerank{}, mapErr(ctx, err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode/100 != 2 {
		return types.CanonicalRerank{}, providers.UpstreamStatusError{Status: resp.StatusCode, Provider: a.id}
	}

	var oa oaRerankResponse
	if err := json.NewDecoder(resp.Body).Decode(&oa); err != nil {
		return types.CanonicalRerank{}, fmt.Errorf("openai: decode response: %w", err)
	}

	results := make([]types.RerankResult, 0, len(oa.Results))
	for _, r := range oa.Results {
		rr := types.RerankResult{Index: r.Index, RelevanceScore: r.RelevanceScore}
		if r.Document != nil {
			rr.Document = r.Document.Text
		}
		results = append(results, rr)
	}

	return types.CanonicalRerank{
		Results:  results,
		Usage:    types.RerankUsage{TotalTokens: oa.Usage.TotalTokens},
		Provider: a.id,
		Model:    oa.Model,
	}, nil
}

// responsesURL is chatCompletionsURL for the /responses endpoint.
func responsesURL(base string) string {
	b := strings.TrimRight(base, "/")
	if strings.HasSuffix(b, "/v1") {
		return b + responsesPath
	}
	return b + "/v1" + responsesPath
}

type oaResponsesResponse struct {
	Model  string           `json:"model"`
	Status string           `json:"status"`
	Output []map[string]any `json:"output"`
	Usage  struct {
		InputTokens  int `json:"input_tokens"`
		OutputTokens int `json:"output_tokens"`
		TotalTokens  int `json:"total_tokens"`
	} `json:"usage"`
}

// Responses implements providers.ResponsesProvider for the non-streaming path.
// Output items pass through unchanged.
func (a *Adapter) Responses(ctx context.Context, req providers.Request, id providers.EgressIdentity) (types.CanonicalResponses, error) {
	body, err := a.buildBody(providers.Request{Model: req.Model, Payload: req.Payload, Stream: false})
	if err != nil {
		return types.CanonicalResponses{}, fmt.Errorf("openai: marshal request: %w", err)
	}
	httpReq, err := a.newResponsesRequest(ctx, body, id, false)
	if err != nil {
		return types.CanonicalResponses{}, err
	}

	resp, err := a.client.Do(httpReq)
	if err != nil {
		return types.CanonicalResponses{}, mapErr(ctx, err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode/100 != 2 {
		return types.CanonicalResponses{}, providers.UpstreamStatusError{Status: resp.StatusCode, Provider: a.id}
	}

	var oa oaResponsesResponse
	if err := json.NewDecoder(resp.Body).Decode(&oa); err != nil {
		return types.CanonicalResponses{}, fmt.Errorf("openai: decode response: %w", err)
	}
	return types.CanonicalResponses{
		Output:       oa.Output,
		FinishReason: oa.Status,
		Usage: types.ResponsesUsage{
			InputTokens:  oa.Usage.InputTokens,
			OutputTokens: oa.Usage.OutputTokens,
			TotalTokens:  oa.Usage.TotalTokens,
		},
		Provider: a.id,
		Model:    oa.Model,
	}, nil
}

func (a *Adapter) newResponsesRequest(ctx context.Context, body []byte, id providers.EgressIdentity, stream bool) (*http.Request, error) {
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, responsesURL(a.baseURL), bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Authorization", "Bearer "+id.Secret)
	httpReq.Header.Set("Content-Type", "application/json")
	if stream {
		httpReq.Header.Set("Accept", "text/event-stream")
	} else {
		httpReq.Header.Set("Accept", "application/json")
	}
	return httpReq, nil
}

type oaResponsesStreamChunk struct {
	Type  string         `json:"type"`
	Delta string         `json:"delta"`
	Item  map[string]any `json:"item"`
}

// ResponsesStream implements providers.ResponsesProvider for the streaming (SSE)
// path. It relays each event through emit. If no data arrives within
// idleTimeout, it cancels the upstream request and returns ErrUpstreamTimeout.
func (a *Adapter) ResponsesStream(ctx context.Context, req providers.Request, id providers.EgressIdentity, emit func(types.ResponsesStreamChunk) error) error {
	body, err := a.buildBody(providers.Request{Model: req.Model, Payload: req.Payload, Stream: true})
	if err != nil {
		return fmt.Errorf("openai: marshal request: %w", err)
	}

	streamCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	httpReq, err := a.newResponsesRequest(streamCtx, body, id, true)
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

	idleFired := false
	idle := time.AfterFunc(a.idleTimeout, func() {
		idleFired = true
		cancel()
	})
	defer idle.Stop()

	reader := bufio.NewReader(resp.Body)
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			if errors.Is(err, io.EOF) && strings.TrimSpace(line) == "" {
				return emit(types.ResponsesStreamChunk{Done: true})
			}
			if idleFired {
				return providers.ErrUpstreamTimeout
			}
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if !errors.Is(err, io.EOF) {
				return mapErr(ctx, err)
			}
		}
		idle.Reset(a.idleTimeout)

		data, ok := parseSSELine(line)
		if !ok {
			if errors.Is(err, io.EOF) {
				return emit(types.ResponsesStreamChunk{Done: true})
			}
			continue
		}
		if data == "[DONE]" {
			return emit(types.ResponsesStreamChunk{Done: true})
		}

		var chunk oaResponsesStreamChunk
		if jsonErr := json.Unmarshal([]byte(data), &chunk); jsonErr != nil {
			continue
		}
		if emitErr := emit(types.ResponsesStreamChunk{Type: chunk.Type, Delta: chunk.Delta, Item: chunk.Item}); emitErr != nil {
			return emitErr
		}
	}
}

// imagesGenerationsURL is chatCompletionsURL for the /images/generations
// endpoint.
func imagesGenerationsURL(base string) string {
	b := strings.TrimRight(base, "/")
	if strings.HasSuffix(b, "/v1") {
		return b + imagesGenerationsPath
	}
	return b + "/v1" + imagesGenerationsPath
}

type oaImageResponse struct {
	Data []struct {
		URL     string `json:"url"`
		B64JSON string `json:"b64_json"`
	} `json:"data"`
}

// Images implements providers.ImageProvider. Image URLs and base64 data pass
// through unchanged; usage reports the number of images.
func (a *Adapter) Images(ctx context.Context, req providers.Request, id providers.EgressIdentity) (types.CanonicalImage, error) {
	body, err := a.buildEmbeddingsBody(req) // payload + model, no stream
	if err != nil {
		return types.CanonicalImage{}, fmt.Errorf("openai: marshal request: %w", err)
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, imagesGenerationsURL(a.baseURL), bytes.NewReader(body))
	if err != nil {
		return types.CanonicalImage{}, err
	}
	httpReq.Header.Set("Authorization", "Bearer "+id.Secret)
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "application/json")

	resp, err := a.client.Do(httpReq)
	if err != nil {
		return types.CanonicalImage{}, mapErr(ctx, err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode/100 != 2 {
		return types.CanonicalImage{}, providers.UpstreamStatusError{Status: resp.StatusCode, Provider: a.id}
	}

	var oa oaImageResponse
	if err := json.NewDecoder(resp.Body).Decode(&oa); err != nil {
		return types.CanonicalImage{}, fmt.Errorf("openai: decode response: %w", err)
	}

	data := make([]types.Image, 0, len(oa.Data))
	for _, d := range oa.Data {
		data = append(data, types.Image{URL: d.URL, B64JSON: d.B64JSON})
	}

	return types.CanonicalImage{
		Data:     data,
		Usage:    types.ImageUsage{Images: len(data)},
		Provider: a.id,
		Model:    req.Model,
	}, nil
}

func opURL(base, path string) string {
	b := strings.TrimRight(base, "/")
	if strings.HasSuffix(b, "/v1") {
		return b + path
	}
	return b + "/v1" + path
}

type oaModerationsResponse struct {
	Model   string `json:"model"`
	Results []struct {
		Flagged        bool               `json:"flagged"`
		Categories     map[string]bool    `json:"categories"`
		CategoryScores map[string]float64 `json:"category_scores"`
	} `json:"results"`
}

// Moderate implements providers.ModerationsProvider.
func (a *Adapter) Moderate(ctx context.Context, req providers.Request, id providers.EgressIdentity) (types.CanonicalModerations, error) {
	body, err := a.buildEmbeddingsBody(req) // payload + model, no stream
	if err != nil {
		return types.CanonicalModerations{}, fmt.Errorf("openai: marshal request: %w", err)
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, opURL(a.baseURL, moderationsPath), bytes.NewReader(body))
	if err != nil {
		return types.CanonicalModerations{}, err
	}
	httpReq.Header.Set("Authorization", "Bearer "+id.Secret)
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "application/json")

	resp, err := a.client.Do(httpReq)
	if err != nil {
		return types.CanonicalModerations{}, mapErr(ctx, err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode/100 != 2 {
		return types.CanonicalModerations{}, providers.UpstreamStatusError{Status: resp.StatusCode, Provider: a.id}
	}

	var oa oaModerationsResponse
	if err := json.NewDecoder(resp.Body).Decode(&oa); err != nil {
		return types.CanonicalModerations{}, fmt.Errorf("openai: decode response: %w", err)
	}
	results := make([]types.ModerationResult, 0, len(oa.Results))
	for _, r := range oa.Results {
		results = append(results, types.ModerationResult{Flagged: r.Flagged, Categories: r.Categories, CategoryScores: r.CategoryScores})
	}
	return types.CanonicalModerations{Results: results, Provider: a.id, Model: oa.Model}, nil
}

type oaTranscriptionResponse struct {
	Text     string           `json:"text"`
	Duration float64          `json:"duration"`
	Segments []map[string]any `json:"segments"`
}

// Transcribe implements providers.TranscriptionProvider. It builds a new
// multipart body from the model and the allowlisted payload. The file is sent
// under a fixed generic filename, so the client's filename, multipart headers,
// and extra parts are never forwarded.
func (a *Adapter) Transcribe(ctx context.Context, req providers.Request, id providers.EgressIdentity) (types.CanonicalTranscription, error) {
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	if err := mw.WriteField("model", req.Model); err != nil {
		return types.CanonicalTranscription{}, err
	}
	for k, v := range req.Payload {
		if k == "file" {
			fw, err := mw.CreateFormFile("file", "audio")
			if err != nil {
				return types.CanonicalTranscription{}, err
			}
			b, _ := v.([]byte)
			if _, err := fw.Write(b); err != nil {
				return types.CanonicalTranscription{}, err
			}
			continue
		}
		if err := mw.WriteField(k, fmt.Sprint(v)); err != nil {
			return types.CanonicalTranscription{}, err
		}
	}
	if err := mw.Close(); err != nil {
		return types.CanonicalTranscription{}, err
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, opURL(a.baseURL, audioTranscriptionsPath), &buf)
	if err != nil {
		return types.CanonicalTranscription{}, err
	}
	httpReq.Header.Set("Authorization", "Bearer "+id.Secret)
	httpReq.Header.Set("Content-Type", mw.FormDataContentType())
	httpReq.Header.Set("Accept", "application/json")

	resp, err := a.client.Do(httpReq)
	if err != nil {
		return types.CanonicalTranscription{}, mapErr(ctx, err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode/100 != 2 {
		return types.CanonicalTranscription{}, providers.UpstreamStatusError{Status: resp.StatusCode, Provider: a.id}
	}

	var oa oaTranscriptionResponse
	if err := json.NewDecoder(resp.Body).Decode(&oa); err != nil {
		return types.CanonicalTranscription{}, fmt.Errorf("openai: decode response: %w", err)
	}
	return types.CanonicalTranscription{
		Text:     oa.Text,
		Segments: oa.Segments,
		Usage:    types.TranscriptionUsage{Seconds: oa.Duration},
		Provider: a.id,
		Model:    req.Model,
	}, nil
}

// speechContentType maps a logical audio format to its MIME type, defaulting to
// audio/mpeg (mp3) when unknown.
func speechContentType(format string) string {
	switch format {
	case "opus":
		return "audio/ogg"
	case "aac":
		return "audio/aac"
	case "flac":
		return "audio/flac"
	case "wav":
		return "audio/wav"
	case "pcm":
		return "audio/pcm"
	default:
		return "audio/mpeg"
	}
}

// Speech implements providers.SpeechProvider. It relays the audio body through
// emit and applies the streaming idle timeout. The first chunk is a Head
// carrying the Content-Type, Provider, and Model; the handler shows Provider and
// Model only on Reveal.
func (a *Adapter) Speech(ctx context.Context, req providers.Request, id providers.EgressIdentity, emit func(types.SpeechChunk) error) error {
	body, err := a.buildEmbeddingsBody(req) // payload and model, no stream
	if err != nil {
		return fmt.Errorf("openai: marshal request: %w", err)
	}

	streamCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	httpReq, err := http.NewRequestWithContext(streamCtx, http.MethodPost, opURL(a.baseURL, audioSpeechPath), bytes.NewReader(body))
	if err != nil {
		return err
	}
	httpReq.Header.Set("Authorization", "Bearer "+id.Secret)
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := a.client.Do(httpReq)
	if err != nil {
		return mapErr(ctx, err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode/100 != 2 {
		return providers.UpstreamStatusError{Status: resp.StatusCode, Provider: a.id}
	}

	ct := resp.Header.Get("Content-Type")
	if ct == "" || strings.HasPrefix(ct, "application/json") {
		format, _ := req.Payload["format"].(string)
		ct = speechContentType(format)
	}
	if err := emit(types.SpeechChunk{Head: true, ContentType: ct, Provider: a.id, Model: req.Model}); err != nil {
		return err
	}

	idleFired := false
	idle := time.AfterFunc(a.idleTimeout, func() {
		idleFired = true
		cancel()
	})
	defer idle.Stop()

	bufr := make([]byte, 32*1024)
	for {
		n, rerr := resp.Body.Read(bufr)
		if n > 0 {
			idle.Reset(a.idleTimeout)
			chunk := make([]byte, n)
			copy(chunk, bufr[:n])
			if eerr := emit(types.SpeechChunk{Audio: chunk}); eerr != nil {
				return eerr
			}
		}
		if rerr != nil {
			if errors.Is(rerr, io.EOF) {
				return emit(types.SpeechChunk{Done: true})
			}
			if idleFired {
				return providers.ErrUpstreamTimeout
			}
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return mapErr(ctx, rerr)
		}
	}
}

// mapErr normalizes transport errors to provider-neutral sentinels.
func mapErr(ctx context.Context, err error) error {
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return providers.ErrUpstreamTimeout
	}
	return err
}

type oaResponse struct {
	Model   string `json:"model"`
	Choices []struct {
		Message struct {
			Role      string                  `json:"role"`
			Content   string                  `json:"content"`
			ToolCalls []oaitools.WireToolCall `json:"tool_calls"`
		} `json:"message"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Usage struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
		TotalTokens      int `json:"total_tokens"`
	} `json:"usage"`
}

// Complete implements providers.Provider for the non-streaming path.
func (a *Adapter) Complete(ctx context.Context, req providers.Request, id providers.EgressIdentity) (types.CanonicalResponse, error) {
	body, err := a.buildChatBody(providers.Request{Model: req.Model, Payload: req.Payload, Stream: false})
	if err != nil {
		return types.CanonicalResponse{}, fmt.Errorf("openai: marshal request: %w", err)
	}
	httpReq, err := a.newHTTPRequest(ctx, body, id, false)
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

	var oa oaResponse
	if err := json.NewDecoder(resp.Body).Decode(&oa); err != nil {
		return types.CanonicalResponse{}, fmt.Errorf("openai: decode response: %w", err)
	}
	if len(oa.Choices) == 0 {
		return types.CanonicalResponse{}, errors.New("openai: response contained no choices")
	}
	c := oa.Choices[0]
	return types.CanonicalResponse{
		Message: types.Message{
			Role:      "assistant",
			Content:   c.Message.Content,
			ToolCalls: oaitools.ToCanonical(c.Message.ToolCalls),
		},
		FinishReason: c.FinishReason,
		Usage: types.Usage{
			PromptTokens:     oa.Usage.PromptTokens,
			CompletionTokens: oa.Usage.CompletionTokens,
			TotalTokens:      oa.Usage.TotalTokens,
		},
		// The Membrane decides whether to show Provider and Model to the Caller
		// (Reveal).
		Provider: a.id,
		Model:    oa.Model,
	}, nil
}

type oaStreamChunk struct {
	Choices []struct {
		Delta struct {
			Content   string                       `json:"content"`
			ToolCalls []oaitools.WireToolCallDelta `json:"tool_calls"`
		} `json:"delta"`
		FinishReason *string `json:"finish_reason"`
	} `json:"choices"`
}

// Stream implements providers.Provider for the streaming (SSE) path. It relays
// each chunk through emit. If no data arrives within idleTimeout, it cancels the
// upstream request and returns ErrUpstreamTimeout.
func (a *Adapter) Stream(ctx context.Context, req providers.Request, id providers.EgressIdentity, emit func(types.StreamChunk) error) error {
	body, err := a.buildChatBody(providers.Request{Model: req.Model, Payload: req.Payload, Stream: true})
	if err != nil {
		return fmt.Errorf("openai: marshal request: %w", err)
	}

	streamCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	httpReq, err := a.newHTTPRequest(streamCtx, body, id, true)
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

	idleFired := false
	idle := time.AfterFunc(a.idleTimeout, func() {
		idleFired = true
		cancel()
	})
	defer idle.Stop()

	reader := bufio.NewReader(resp.Body)
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			if errors.Is(err, io.EOF) && strings.TrimSpace(line) == "" {
				// Clean end of stream without an explicit [DONE].
				return emit(types.StreamChunk{Done: true})
			}
			if idleFired {
				return providers.ErrUpstreamTimeout
			}
			if ctx.Err() != nil {
				// Parent cancelled, for example by a client disconnect.
				return ctx.Err()
			}
			if errors.Is(err, io.EOF) {
				// Process the final partial line below if it carries data.
			} else {
				return mapErr(ctx, err)
			}
		}
		idle.Reset(a.idleTimeout)

		data, ok := parseSSELine(line)
		if !ok {
			if errors.Is(err, io.EOF) {
				return emit(types.StreamChunk{Done: true})
			}
			continue
		}
		if data == "[DONE]" {
			return emit(types.StreamChunk{Done: true})
		}

		var chunk oaStreamChunk
		if jsonErr := json.Unmarshal([]byte(data), &chunk); jsonErr != nil {
			// Skip malformed and keepalive frames.
			continue
		}
		if len(chunk.Choices) == 0 {
			continue
		}
		sc := types.StreamChunk{
			Delta:     chunk.Choices[0].Delta.Content,
			ToolCalls: oaitools.DeltaToCanonical(chunk.Choices[0].Delta.ToolCalls),
		}
		if fr := chunk.Choices[0].FinishReason; fr != nil {
			sc.FinishReason = *fr
		}
		if emitErr := emit(sc); emitErr != nil {
			return emitErr
		}
	}
}

// parseSSELine extracts the payload of a `data:` SSE line. It returns ok=false
// for non-data lines (comments, event:, blank).
func parseSSELine(line string) (string, bool) {
	line = strings.TrimRight(line, "\r\n")
	rest, ok := strings.CutPrefix(line, "data:")
	if !ok {
		return "", false
	}
	return strings.TrimSpace(rest), true
}
