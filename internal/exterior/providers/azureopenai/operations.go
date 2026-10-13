// This file extends the Azure OpenAI adapter with the operation-breadth
// methods. Azure is OpenAI-compatible on the wire, so request/response shapes
// mirror the openai adapter; only the URL (deployment path + api-version) and the
// auth (api-key / Entra bearer token) differ. Every method builds its request
// ONLY from the neutral providers.Request plus the injected EgressIdentity — no
// Caller identity, Caller Key, or Reveal flag is ever accessible here.
//
// Azure selects the model via the deployment in the path, so the body never
// carries a model field; the logical model surfaced on Reveal falls back to the
// request's resolved model when the upstream does not echo one.
package azureopenai

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/inferencepath/membrane/internal/exterior/providers"
	"github.com/inferencepath/membrane/internal/exterior/providers/streamhelper"
	"github.com/inferencepath/membrane/internal/types"
)

// opEndpoint builds the deployment-scoped URL for an operation sub-path such as
// "/embeddings" or "/audio/speech", appending the pinned api-version.
func (a *Adapter) opEndpoint(op string) string {
	u := fmt.Sprintf("%s/openai/deployments/%s%s", a.baseURL, url.PathEscape(a.deployment), op)
	return u + "?api-version=" + url.QueryEscape(a.apiVersion)
}

// applyAuth attaches only the Egress Identity (api-key or Entra bearer token).
// No caller/org identity, Caller Key, forwarding, or correlation headers.
func (a *Adapter) applyAuth(ctx context.Context, httpReq *http.Request, id providers.EgressIdentity) error {
	if a.tokenSource != nil {
		token, terr := a.tokenSource(ctx)
		if terr != nil {
			return fmt.Errorf("azureopenai: acquire token: %w", terr)
		}
		httpReq.Header.Set("Authorization", "Bearer "+token)
		return nil
	}
	httpReq.Header.Set("api-key", id.Secret)
	return nil
}

// opBody copies the allowlisted payload, dropping model (deployment selects it)
// and stream (set explicitly by streaming callers only).
func opBody(req providers.Request) ([]byte, error) {
	body := make(map[string]any, len(req.Payload)+1)
	for k, v := range req.Payload {
		body[k] = v
	}
	delete(body, "model")
	delete(body, "stream")
	return json.Marshal(body)
}

func (a *Adapter) newJSONRequest(ctx context.Context, op string, body []byte, id providers.EgressIdentity, accept string) (*http.Request, error) {
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, a.opEndpoint(op), bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	if err := a.applyAuth(ctx, httpReq, id); err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if accept != "" {
		httpReq.Header.Set("Accept", accept)
	}
	return httpReq, nil
}

func (a *Adapter) modelOrReq(upstream, reqModel string) string {
	if upstream == "" {
		return reqModel
	}
	return upstream
}

// Embeddings implements providers.EmbeddingsProvider.
func (a *Adapter) Embeddings(ctx context.Context, req providers.Request, id providers.EgressIdentity) (types.CanonicalEmbeddings, error) {
	body, err := opBody(req)
	if err != nil {
		return types.CanonicalEmbeddings{}, fmt.Errorf("azureopenai: marshal request: %w", err)
	}
	httpReq, err := a.newJSONRequest(ctx, "/embeddings", body, id, "application/json")
	if err != nil {
		return types.CanonicalEmbeddings{}, err
	}
	resp, err := a.client.Do(httpReq)
	if err != nil {
		return types.CanonicalEmbeddings{}, mapErr(ctx, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode/100 != 2 {
		return types.CanonicalEmbeddings{}, providers.UpstreamStatusError{Status: resp.StatusCode, Provider: a.id}
	}

	var oa struct {
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
	if err := json.NewDecoder(resp.Body).Decode(&oa); err != nil {
		return types.CanonicalEmbeddings{}, fmt.Errorf("azureopenai: decode response: %w", err)
	}
	data := make([]types.EmbeddingVector, 0, len(oa.Data))
	for _, d := range oa.Data {
		var emb any
		if len(d.Embedding) > 0 {
			if err := json.Unmarshal(d.Embedding, &emb); err != nil {
				return types.CanonicalEmbeddings{}, fmt.Errorf("azureopenai: decode embedding: %w", err)
			}
		}
		data = append(data, types.EmbeddingVector{Index: d.Index, Embedding: emb})
	}
	return types.CanonicalEmbeddings{
		Data:     data,
		Usage:    types.EmbeddingsUsage{PromptTokens: oa.Usage.PromptTokens, TotalTokens: oa.Usage.TotalTokens},
		Provider: a.id,
		Model:    a.modelOrReq(oa.Model, req.Model),
	}, nil
}

// Responses implements providers.ResponsesProvider for the non-streaming path.
func (a *Adapter) Responses(ctx context.Context, req providers.Request, id providers.EgressIdentity) (types.CanonicalResponses, error) {
	body, err := opBody(req)
	if err != nil {
		return types.CanonicalResponses{}, fmt.Errorf("azureopenai: marshal request: %w", err)
	}
	httpReq, err := a.newJSONRequest(ctx, "/responses", body, id, "application/json")
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
	var oa struct {
		Model  string           `json:"model"`
		Status string           `json:"status"`
		Output []map[string]any `json:"output"`
		Usage  struct {
			InputTokens  int `json:"input_tokens"`
			OutputTokens int `json:"output_tokens"`
			TotalTokens  int `json:"total_tokens"`
		} `json:"usage"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&oa); err != nil {
		return types.CanonicalResponses{}, fmt.Errorf("azureopenai: decode response: %w", err)
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
		Model:    a.modelOrReq(oa.Model, req.Model),
	}, nil
}

// ResponsesStream implements providers.ResponsesProvider for the streaming path.
func (a *Adapter) ResponsesStream(ctx context.Context, req providers.Request, id providers.EgressIdentity, emit func(types.ResponsesStreamChunk) error) error {
	body := make(map[string]any, len(req.Payload)+1)
	for k, v := range req.Payload {
		body[k] = v
	}
	delete(body, "model")
	body["stream"] = true
	raw, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("azureopenai: marshal request: %w", err)
	}

	streamCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	httpReq, err := a.newJSONRequest(streamCtx, "/responses", raw, id, "text/event-stream")
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

	perr := streamhelper.PumpSSE(streamCtx, cancel, resp.Body, a.idleTimeout, func(data string) (bool, error) {
		if data == "[DONE]" {
			return true, nil
		}
		var chunk struct {
			Type  string         `json:"type"`
			Delta string         `json:"delta"`
			Item  map[string]any `json:"item"`
		}
		if json.Unmarshal([]byte(data), &chunk) != nil {
			return false, nil
		}
		return false, emit(types.ResponsesStreamChunk{Type: chunk.Type, Delta: chunk.Delta, Item: chunk.Item})
	})
	if perr != nil {
		return perr
	}
	return emit(types.ResponsesStreamChunk{Done: true})
}

// Images implements providers.ImageProvider.
func (a *Adapter) Images(ctx context.Context, req providers.Request, id providers.EgressIdentity) (types.CanonicalImage, error) {
	body, err := opBody(req)
	if err != nil {
		return types.CanonicalImage{}, fmt.Errorf("azureopenai: marshal request: %w", err)
	}
	httpReq, err := a.newJSONRequest(ctx, "/images/generations", body, id, "application/json")
	if err != nil {
		return types.CanonicalImage{}, err
	}
	resp, err := a.client.Do(httpReq)
	if err != nil {
		return types.CanonicalImage{}, mapErr(ctx, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode/100 != 2 {
		return types.CanonicalImage{}, providers.UpstreamStatusError{Status: resp.StatusCode, Provider: a.id}
	}
	var oa struct {
		Data []struct {
			URL     string `json:"url"`
			B64JSON string `json:"b64_json"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&oa); err != nil {
		return types.CanonicalImage{}, fmt.Errorf("azureopenai: decode response: %w", err)
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

// Moderate implements providers.ModerationsProvider.
func (a *Adapter) Moderate(ctx context.Context, req providers.Request, id providers.EgressIdentity) (types.CanonicalModerations, error) {
	body, err := opBody(req)
	if err != nil {
		return types.CanonicalModerations{}, fmt.Errorf("azureopenai: marshal request: %w", err)
	}
	httpReq, err := a.newJSONRequest(ctx, "/moderations", body, id, "application/json")
	if err != nil {
		return types.CanonicalModerations{}, err
	}
	resp, err := a.client.Do(httpReq)
	if err != nil {
		return types.CanonicalModerations{}, mapErr(ctx, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode/100 != 2 {
		return types.CanonicalModerations{}, providers.UpstreamStatusError{Status: resp.StatusCode, Provider: a.id}
	}
	var oa struct {
		Model   string `json:"model"`
		Results []struct {
			Flagged        bool               `json:"flagged"`
			Categories     map[string]bool    `json:"categories"`
			CategoryScores map[string]float64 `json:"category_scores"`
		} `json:"results"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&oa); err != nil {
		return types.CanonicalModerations{}, fmt.Errorf("azureopenai: decode response: %w", err)
	}
	results := make([]types.ModerationResult, 0, len(oa.Results))
	for _, r := range oa.Results {
		results = append(results, types.ModerationResult{Flagged: r.Flagged, Categories: r.Categories, CategoryScores: r.CategoryScores})
	}
	return types.CanonicalModerations{Results: results, Provider: a.id, Model: a.modelOrReq(oa.Model, req.Model)}, nil
}

// Transcribe implements providers.TranscriptionProvider. It RECONSTRUCTS the
// outbound multipart from the allowlisted payload (the "file" bytes sent with a
// fixed generic filename — the client filename never crosses — plus allowlisted
// form fields). It never forwards the client's original multipart.
func (a *Adapter) Transcribe(ctx context.Context, req providers.Request, id providers.EgressIdentity) (types.CanonicalTranscription, error) {
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	for k, v := range req.Payload {
		if k == "file" {
			fw, ferr := mw.CreateFormFile("file", "audio")
			if ferr != nil {
				return types.CanonicalTranscription{}, ferr
			}
			b, _ := v.([]byte)
			if _, werr := fw.Write(b); werr != nil {
				return types.CanonicalTranscription{}, werr
			}
			continue
		}
		if werr := mw.WriteField(k, fmt.Sprint(v)); werr != nil {
			return types.CanonicalTranscription{}, werr
		}
	}
	if err := mw.Close(); err != nil {
		return types.CanonicalTranscription{}, err
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, a.opEndpoint("/audio/transcriptions"), &buf)
	if err != nil {
		return types.CanonicalTranscription{}, err
	}
	if err := a.applyAuth(ctx, httpReq, id); err != nil {
		return types.CanonicalTranscription{}, err
	}
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
	var oa struct {
		Text     string           `json:"text"`
		Duration float64          `json:"duration"`
		Segments []map[string]any `json:"segments"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&oa); err != nil {
		return types.CanonicalTranscription{}, fmt.Errorf("azureopenai: decode response: %w", err)
	}
	return types.CanonicalTranscription{
		Text:     oa.Text,
		Segments: oa.Segments,
		Usage:    types.TranscriptionUsage{Seconds: oa.Duration},
		Provider: a.id,
		Model:    req.Model,
	}, nil
}

// speechContentType maps a logical audio format to its MIME type.
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

// Speech implements providers.SpeechProvider. It POSTs the allowlisted payload and
// relays the binary audio body incrementally via emit (first a Head chunk with the
// Content-Type and Provider/Model), applying the same idle timeout as streaming.
func (a *Adapter) Speech(ctx context.Context, req providers.Request, id providers.EgressIdentity, emit func(types.SpeechChunk) error) error {
	body, err := opBody(req)
	if err != nil {
		return fmt.Errorf("azureopenai: marshal request: %w", err)
	}
	streamCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	httpReq, err := http.NewRequestWithContext(streamCtx, http.MethodPost, a.opEndpoint("/audio/speech"), bytes.NewReader(body))
	if err != nil {
		return err
	}
	if err := a.applyAuth(streamCtx, httpReq, id); err != nil {
		return err
	}
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
