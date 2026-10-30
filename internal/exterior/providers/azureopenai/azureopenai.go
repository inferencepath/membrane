// Package azureopenai implements a Provider adapter for Azure OpenAI. Azure
// selects the model by a deployment name in the URL path and authenticates with
// the api-key header or an Entra bearer token. Requests are built only from a
// providers.Request and the EgressIdentity; the adapter has no access to Caller
// identity, the Caller Key, or the Reveal flag.
package azureopenai

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
	"github.com/inferencepath/membrane/internal/exterior/providers/oaitools"
	"github.com/inferencepath/membrane/internal/exterior/providers/streamhelper"
	"github.com/inferencepath/membrane/internal/types"
)

const (
	// defaultAPIVersion pins the Azure OpenAI REST API version.
	defaultAPIVersion = "2024-02-01"
	// defaultIdleTimeout bounds inactivity on a streaming response when unset.
	defaultIdleTimeout = 30 * time.Second
)

// Adapter is an Azure OpenAI Provider adapter bound to a single deployment.
type Adapter struct {
	id          types.ProviderID
	baseURL     string
	deployment  string
	apiVersion  string
	idleTimeout time.Duration
	client      *http.Client
	// tokenSource, when set, supplies an Entra bearer token per request in place
	// of the api-key header.
	tokenSource func(context.Context) (string, error)
}

// Option configures an Azure OpenAI Adapter.
type Option func(*Adapter)

// WithTokenSource authenticates with an Entra bearer token from the given source
// (for example a managed identity) instead of the api-key header. The source is
// called per request so short-lived tokens can refresh.
func WithTokenSource(fn func(context.Context) (string, error)) Option {
	return func(a *Adapter) { a.tokenSource = fn }
}

// New returns an Azure OpenAI adapter for the given resource base URL and
// deployment name. idleTimeout bounds inactivity on streaming responses; a
// value <= 0 selects the default. The reported ID is "azure-openai".
func New(baseURL, deployment string, idleTimeout time.Duration, opts ...Option) *Adapter {
	if idleTimeout <= 0 {
		idleTimeout = defaultIdleTimeout
	}
	a := &Adapter{
		id:          "azure-openai",
		baseURL:     strings.TrimRight(baseURL, "/"),
		deployment:  deployment,
		apiVersion:  defaultAPIVersion,
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

func (a *Adapter) endpoint() string {
	u := fmt.Sprintf("%s/openai/deployments/%s/chat/completions", a.baseURL, url.PathEscape(a.deployment))
	return u + "?api-version=" + url.QueryEscape(a.apiVersion)
}

func (a *Adapter) buildBody(req providers.Request, stream bool) ([]byte, error) {
	body := make(map[string]any, len(req.Payload)+1)
	for k, v := range req.Payload {
		body[k] = v
	}
	// The deployment in the path selects the model.
	delete(body, "model")
	if stream {
		body["stream"] = true
	} else {
		delete(body, "stream")
	}
	// Azure uses the OpenAI tool format.
	if raw, ok := body["tools"]; ok {
		body["tools"] = oaitools.Tools(raw)
	}
	if raw, ok := body["messages"]; ok {
		body["messages"] = oaitools.Messages(raw)
	}
	return json.Marshal(body)
}

func (a *Adapter) newHTTPRequest(ctx context.Context, body []byte, id providers.EgressIdentity, stream bool) (*http.Request, error) {
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, a.endpoint(), bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	// Only the Egress Identity and content headers are sent: no Caller identity,
	// Caller Key, forwarding, or correlation headers.
	if a.tokenSource != nil {
		token, terr := a.tokenSource(ctx)
		if terr != nil {
			return nil, fmt.Errorf("azureopenai: acquire token: %w", terr)
		}
		httpReq.Header.Set("Authorization", "Bearer "+token)
	} else {
		httpReq.Header.Set("api-key", id.Secret)
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

type chatResponse struct {
	Model   string `json:"model"`
	Choices []struct {
		Message struct {
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
	body, err := a.buildBody(req, false)
	if err != nil {
		return types.CanonicalResponse{}, fmt.Errorf("azureopenai: marshal request: %w", err)
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

	var cr chatResponse
	if err := json.NewDecoder(resp.Body).Decode(&cr); err != nil {
		return types.CanonicalResponse{}, fmt.Errorf("azureopenai: decode response: %w", err)
	}
	if len(cr.Choices) == 0 {
		return types.CanonicalResponse{}, errors.New("azureopenai: response contained no choices")
	}
	c := cr.Choices[0]
	// Azure does not always return a model name. Fall back to the requested
	// model rather than exposing the deployment name.
	model := cr.Model
	if model == "" {
		model = req.Model
	}
	return types.CanonicalResponse{
		Message: types.Message{
			Role:      "assistant",
			Content:   c.Message.Content,
			ToolCalls: oaitools.ToCanonical(c.Message.ToolCalls),
		},
		FinishReason: c.FinishReason,
		Usage: types.Usage{
			PromptTokens:     cr.Usage.PromptTokens,
			CompletionTokens: cr.Usage.CompletionTokens,
			TotalTokens:      cr.Usage.TotalTokens,
		},
		Provider: a.id,
		Model:    model,
	}, nil
}

type streamChunk struct {
	Choices []struct {
		Delta struct {
			Content   string                       `json:"content"`
			ToolCalls []oaitools.WireToolCallDelta `json:"tool_calls"`
		} `json:"delta"`
		FinishReason *string `json:"finish_reason"`
	} `json:"choices"`
}

// Stream implements providers.Provider for the streaming (SSE) path. Azure
// sends OpenAI-compatible chunks terminated by "data: [DONE]".
func (a *Adapter) Stream(ctx context.Context, req providers.Request, id providers.EgressIdentity, emit func(types.StreamChunk) error) error {
	body, err := a.buildBody(req, true)
	if err != nil {
		return fmt.Errorf("azureopenai: marshal request: %w", err)
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

	err = streamhelper.PumpSSE(streamCtx, cancel, resp.Body, a.idleTimeout, func(data string) (bool, error) {
		if data == "[DONE]" {
			return true, nil
		}
		var chunk streamChunk
		if json.Unmarshal([]byte(data), &chunk) != nil || len(chunk.Choices) == 0 {
			return false, nil
		}
		sc := types.StreamChunk{
			Delta:     chunk.Choices[0].Delta.Content,
			ToolCalls: oaitools.DeltaToCanonical(chunk.Choices[0].Delta.ToolCalls),
		}
		if fr := chunk.Choices[0].FinishReason; fr != nil {
			sc.FinishReason = *fr
		}
		return false, emit(sc)
	})
	if err != nil {
		return err
	}
	return emit(types.StreamChunk{Done: true})
}
