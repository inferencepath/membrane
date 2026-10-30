// Package bedrock implements a Provider adapter for the AWS Bedrock Runtime.
// Chat uses the Converse API, which shares one schema across models, so
// switching models only changes the model ID. Requests are signed with AWS
// SigV4 or carry a Bedrock bearer token, and are built only from a
// providers.Request and the EgressIdentity. The adapter has no access to Caller
// identity, the Caller Key, or the Reveal flag.
//
// The EgressIdentity secret carries AWS credentials, encoded either as JSON
// ({"access_key_id":...,"secret_access_key":...,"session_token":...}) or as a
// colon-separated string (access_key_id:secret_access_key[:session_token]).
package bedrock

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	v4 "github.com/aws/aws-sdk-go-v2/aws/signer/v4"
	"github.com/aws/smithy-go/eventstream"

	"github.com/inferencepath/membrane/internal/exterior/providers"
	"github.com/inferencepath/membrane/internal/types"
)

const (
	// service is the SigV4 service name for the Bedrock Runtime.
	service          = "bedrock"
	defaultMaxTokens = 1024
	// defaultIdleTimeout bounds inactivity on a streaming response when unset.
	defaultIdleTimeout = 30 * time.Second
)

// Adapter is an AWS Bedrock Runtime Provider adapter.
type Adapter struct {
	id          types.ProviderID
	region      string
	baseURL     string
	idleTimeout time.Duration
	signer      *v4.Signer
	client      *http.Client
	nowFn       func() time.Time
	// creds, when set, supplies AWS credentials (ambient IAM role or AssumeRole)
	// instead of parsing them from the EgressIdentity secret.
	creds aws.CredentialsProvider
	// bearerAuth, when set, uses Authorization: Bearer <token> from the static
	// EgressIdentity secret instead of SigV4 credentials.
	bearerAuth bool
}

// Option configures a Bedrock Adapter.
type Option func(*Adapter)

// WithCredentialsProvider sources AWS credentials from the given provider
// (ambient default chain or AssumeRole) rather than the EgressIdentity secret.
// Credentials are retrieved per request, so temporary credentials can refresh.
func WithCredentialsProvider(p aws.CredentialsProvider) Option {
	return func(a *Adapter) { a.creds = p }
}

// WithBearerTokenAuth enables Bedrock bearer-token authentication from the
// static EgressIdentity secret instead of AWS SigV4 credentials.
func WithBearerTokenAuth() Option {
	return func(a *Adapter) { a.bearerAuth = true }
}

// New returns a Bedrock adapter for the given region. A non-empty baseURL
// overrides the regional https://bedrock-runtime.<region>.amazonaws.com
// endpoint. idleTimeout bounds inactivity on streaming responses; a value <= 0
// selects the default.
func New(region, baseURL string, idleTimeout time.Duration, opts ...Option) *Adapter {
	if idleTimeout <= 0 {
		idleTimeout = defaultIdleTimeout
	}
	a := &Adapter{
		id:          "bedrock",
		region:      region,
		baseURL:     strings.TrimRight(baseURL, "/"),
		idleTimeout: idleTimeout,
		signer:      v4.NewSigner(),
		client:      &http.Client{},
		nowFn:       time.Now,
	}
	for _, o := range opts {
		o(a)
	}
	return a
}

// ID implements providers.Provider.
func (a *Adapter) ID() types.ProviderID { return a.id }

func (a *Adapter) base() string {
	if a.baseURL == "" {
		return fmt.Sprintf("https://bedrock-runtime.%s.amazonaws.com", a.region)
	}
	return a.baseURL
}

// invokeURL is the raw InvokeModel endpoint, used by the non-chat operations
// (embeddings, rerank, image generation) that have no Converse equivalent.
func (a *Adapter) invokeURL(model string) string {
	return fmt.Sprintf("%s/model/%s/invoke", a.base(), url.PathEscape(model))
}

// converseURL and converseStreamURL are the Converse chat endpoints used by
// Complete and Stream.
func (a *Adapter) converseURL(model string) string {
	return fmt.Sprintf("%s/model/%s/converse", a.base(), url.PathEscape(model))
}

func (a *Adapter) converseStreamURL(model string) string {
	return fmt.Sprintf("%s/model/%s/converse-stream", a.base(), url.PathEscape(model))
}

// authRequest builds an authenticated POST for the given URL and body.
func (a *Adapter) authRequest(ctx context.Context, endpoint string, body []byte, id providers.EgressIdentity) (*http.Request, error) {
	if a.bearerAuth {
		token := strings.TrimSpace(id.Secret)
		if token == "" {
			return nil, errors.New("bedrock: empty bearer token")
		}
		httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
		if err != nil {
			return nil, err
		}
		httpReq.Header.Set("Authorization", "Bearer "+token)
		httpReq.Header.Set("Content-Type", "application/json")
		httpReq.Header.Set("Accept", "application/json")
		return httpReq, nil
	}
	creds, err := a.resolveCredentials(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("bedrock: %w", err)
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "application/json")
	sum := sha256.Sum256(body)
	payloadHash := hex.EncodeToString(sum[:])
	if err := a.signer.SignHTTP(ctx, creds, httpReq, payloadHash, service, a.region, a.nowFn()); err != nil {
		return nil, fmt.Errorf("bedrock: sign request: %w", err)
	}
	return httpReq, nil
}

// buildBody builds a Bedrock Converse request from the neutral payload. System
// turns move to the top-level "system" field, since Converse rejects a "system"
// role inside messages, and sampling parameters map into inferenceConfig.
func (a *Adapter) buildBody(req providers.Request) ([]byte, error) {
	system, messages := splitMessages(req.Payload["messages"])

	inference := map[string]any{"maxTokens": defaultMaxTokens}
	if v, ok := asInt(req.Payload["max_tokens"]); ok {
		inference["maxTokens"] = v
	}
	if v, ok := req.Payload["temperature"]; ok {
		inference["temperature"] = v
	}
	if v, ok := req.Payload["top_p"]; ok {
		inference["topP"] = v
	}
	if ss := stopSequences(req.Payload["stop"]); len(ss) > 0 {
		inference["stopSequences"] = ss
	}

	body := map[string]any{
		"messages":        messages,
		"inferenceConfig": inference,
	}
	if len(system) > 0 {
		body["system"] = system
	}
	if tc := toolConfig(req.Payload); tc != nil {
		body["toolConfig"] = tc
	}
	return json.Marshal(body)
}

// splitMessages converts the neutral messages array into Converse's shape: a
// top-level text-only system block plus user and assistant turns whose content
// is a list of content blocks. Assistant tool_calls become toolUse blocks. A
// tool-result turn (role "tool") becomes a user message with a toolResult block,
// which is where Converse expects tool results. Unknown roles are treated as
// user input.
func splitMessages(raw any) (system, messages []map[string]any) {
	arr, ok := raw.([]any)
	if !ok {
		return nil, []map[string]any{}
	}
	messages = make([]map[string]any, 0, len(arr))
	for _, m := range arr {
		mm, ok := m.(map[string]any)
		if !ok {
			continue
		}
		role, _ := mm["role"].(string)
		text, _ := mm["content"].(string)
		switch role {
		case "system":
			system = append(system, map[string]any{"text": text})
		case "tool":
			id, _ := mm["tool_call_id"].(string)
			block := map[string]any{"toolResult": map[string]any{
				"toolUseId": id,
				"content":   []any{map[string]any{"text": text}},
			}}
			messages = append(messages, map[string]any{"role": "user", "content": []any{block}})
		case "assistant":
			content := make([]any, 0, 2)
			if text != "" {
				content = append(content, map[string]any{"text": text})
			}
			if tcs, ok := mm["tool_calls"].([]any); ok {
				for _, tc := range tcs {
					tcm, ok := tc.(map[string]any)
					if !ok {
						continue
					}
					use := map[string]any{"toolUseId": tcm["id"], "name": tcm["name"]}
					if args, ok := tcm["arguments"]; ok {
						use["input"] = args
					} else {
						use["input"] = map[string]any{}
					}
					content = append(content, map[string]any{"toolUse": use})
				}
			}
			if len(content) == 0 {
				content = append(content, map[string]any{"text": ""})
			}
			messages = append(messages, map[string]any{"role": "assistant", "content": content})
		default: // user and unknown roles
			messages = append(messages, map[string]any{
				"role":    "user",
				"content": []any{map[string]any{"text": text}},
			})
		}
	}
	return system, messages
}

// toolConfig maps the neutral tools and tool_choice payload into a Converse
// toolConfig, or nil when the request offers no tools.
func toolConfig(payload map[string]any) map[string]any {
	raw, ok := payload["tools"]
	if !ok {
		return nil
	}
	arr, ok := raw.([]any)
	if !ok || len(arr) == 0 {
		return nil
	}
	tools := make([]any, 0, len(arr))
	for _, e := range arr {
		m, ok := e.(map[string]any)
		if !ok {
			continue
		}
		spec := map[string]any{"name": m["name"]}
		if d, ok := m["description"]; ok {
			spec["description"] = d
		}
		if p, ok := m["parameters"]; ok {
			spec["inputSchema"] = map[string]any{"json": p}
		}
		tools = append(tools, map[string]any{"toolSpec": spec})
	}
	tc := map[string]any{"tools": tools}
	if choice := toolChoice(payload["tool_choice"]); choice != nil {
		tc["toolChoice"] = choice
	}
	return tc
}

// toolChoice maps the neutral (OpenAI-style) tool_choice directive into the
// Converse toolChoice shape. "none" and unknown values return nil, which leaves
// the model default.
func toolChoice(raw any) map[string]any {
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
		v = r
	}
	switch c := v.(type) {
	case string:
		switch c {
		case "auto":
			return map[string]any{"auto": map[string]any{}}
		case "required", "any":
			return map[string]any{"any": map[string]any{}}
		}
	case map[string]any:
		if fn, ok := c["function"].(map[string]any); ok {
			if name, ok := fn["name"]; ok {
				return map[string]any{"tool": map[string]any{"name": name}}
			}
		}
	}
	return nil
}

// asInt coerces a JSON-decoded numeric payload value to an int.
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

// stopSequences normalizes the "stop" parameter (string or array) into the
// Converse stopSequences list.
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

func mapErr(ctx context.Context, err error) error {
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return providers.ErrUpstreamTimeout
	}
	return err
}

// converseResponse is the minimal shape of a Bedrock Converse reply. The schema
// is the same for every Converse model.
type converseResponse struct {
	Output struct {
		Message struct {
			Role    string `json:"role"`
			Content []struct {
				Text    string `json:"text"`
				ToolUse *struct {
					ToolUseID string          `json:"toolUseId"`
					Name      string          `json:"name"`
					Input     json.RawMessage `json:"input"`
				} `json:"toolUse"`
			} `json:"content"`
		} `json:"message"`
	} `json:"output"`
	StopReason string `json:"stopReason"`
	Usage      struct {
		InputTokens  int `json:"inputTokens"`
		OutputTokens int `json:"outputTokens"`
		TotalTokens  int `json:"totalTokens"`
	} `json:"usage"`
}

// Complete implements providers.Provider for the non-streaming path.
func (a *Adapter) Complete(ctx context.Context, req providers.Request, id providers.EgressIdentity) (types.CanonicalResponse, error) {
	body, err := a.buildBody(req)
	if err != nil {
		return types.CanonicalResponse{}, fmt.Errorf("bedrock: marshal request: %w", err)
	}
	httpReq, err := a.authRequest(ctx, a.converseURL(req.Model), body, id)
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

	var cr converseResponse
	if err := json.NewDecoder(resp.Body).Decode(&cr); err != nil {
		return types.CanonicalResponse{}, fmt.Errorf("bedrock: decode response: %w", err)
	}
	msg := types.Message{Role: "assistant"}
	var text strings.Builder
	for _, c := range cr.Output.Message.Content {
		if c.ToolUse != nil {
			msg.ToolCalls = append(msg.ToolCalls, types.ToolCall{
				ID:        c.ToolUse.ToolUseID,
				Name:      c.ToolUse.Name,
				Arguments: c.ToolUse.Input,
			})
			continue
		}
		text.WriteString(c.Text)
	}
	msg.Content = text.String()
	// Report tool use with the canonical "tool_calls" finish reason.
	finish := cr.StopReason
	if finish == "tool_use" {
		finish = "tool_calls"
	}
	total := cr.Usage.TotalTokens
	if total == 0 {
		total = cr.Usage.InputTokens + cr.Usage.OutputTokens
	}
	return types.CanonicalResponse{
		Message:      msg,
		FinishReason: finish,
		Usage: types.Usage{
			PromptTokens:     cr.Usage.InputTokens,
			CompletionTokens: cr.Usage.OutputTokens,
			TotalTokens:      total,
		},
		Provider: a.id,
		Model:    req.Model,
	}, nil
}

// converseStreamEvent is the minimal shape of a Bedrock Converse stream event.
// The event kind is in the frame's :event-type header and the payload is plain
// event JSON, unlike the base64-wrapped InvokeModel stream. A toolUse block
// opens with a contentBlockStart (start.toolUse carries id and name) and streams
// its arguments as contentBlockDelta.delta.toolUse.input fragments, both keyed
// by contentBlockIndex.
type converseStreamEvent struct {
	ContentBlockIndex int `json:"contentBlockIndex"`
	Start             struct {
		ToolUse *struct {
			ToolUseID string `json:"toolUseId"`
			Name      string `json:"name"`
		} `json:"toolUse"`
	} `json:"start"`
	Delta struct {
		Text    string `json:"text"`
		ToolUse *struct {
			Input string `json:"input"`
		} `json:"toolUse"`
	} `json:"delta"`
	StopReason string `json:"stopReason"`
}

// eventType extracts the Converse stream frame's :event-type header value.
func eventType(headers eventstream.Headers) string {
	if v := headers.Get(":event-type"); v != nil {
		return v.String()
	}
	return ""
}

// Stream implements providers.Provider for the streaming path. Bedrock replies
// with an AWS event stream of Converse events, which are mapped to canonical
// chunks. An idle timer cancels a stalled upstream.
func (a *Adapter) Stream(ctx context.Context, req providers.Request, id providers.EgressIdentity, emit func(types.StreamChunk) error) error {
	body, err := a.buildBody(req)
	if err != nil {
		return fmt.Errorf("bedrock: marshal request: %w", err)
	}

	streamCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	httpReq, err := a.authRequest(streamCtx, a.converseStreamURL(req.Model), body, id)
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
	timer := time.AfterFunc(a.idleTimeout, func() {
		idleFired = true
		cancel()
	})
	defer timer.Stop()

	dec := eventstream.NewDecoder()
	var payloadBuf []byte
	for {
		msg, derr := dec.Decode(resp.Body, payloadBuf)
		if derr != nil {
			if errors.Is(derr, io.EOF) {
				return emit(types.StreamChunk{Done: true})
			}
			if idleFired {
				return providers.ErrUpstreamTimeout
			}
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return mapErr(ctx, derr)
		}
		timer.Reset(a.idleTimeout)

		var ev converseStreamEvent
		if json.Unmarshal(msg.Payload, &ev) != nil {
			continue
		}
		switch eventType(msg.Headers) {
		case "contentBlockStart":
			if tu := ev.Start.ToolUse; tu != nil {
				chunk := types.StreamChunk{ToolCalls: []types.ToolCallDelta{{
					Index: ev.ContentBlockIndex,
					ID:    tu.ToolUseID,
					Name:  tu.Name,
				}}}
				if emitErr := emit(chunk); emitErr != nil {
					return emitErr
				}
			}
		case "contentBlockDelta":
			if ev.Delta.Text != "" {
				if emitErr := emit(types.StreamChunk{Delta: ev.Delta.Text}); emitErr != nil {
					return emitErr
				}
			}
			if tu := ev.Delta.ToolUse; tu != nil && tu.Input != "" {
				chunk := types.StreamChunk{ToolCalls: []types.ToolCallDelta{{
					Index:             ev.ContentBlockIndex,
					ArgumentsFragment: tu.Input,
				}}}
				if emitErr := emit(chunk); emitErr != nil {
					return emitErr
				}
			}
		case "messageStop":
			if ev.StopReason != "" {
				finish := ev.StopReason
				if finish == "tool_use" {
					finish = "tool_calls"
				}
				if emitErr := emit(types.StreamChunk{FinishReason: finish}); emitErr != nil {
					return emitErr
				}
			}
		}
	}
}

// resolveCredentials returns the AWS credentials to sign with. It uses the
// configured credentials provider if there is one, and otherwise parses the
// EgressIdentity secret (api_key auth).
func (a *Adapter) resolveCredentials(ctx context.Context, id providers.EgressIdentity) (aws.Credentials, error) {
	if a.creds != nil {
		return a.creds.Retrieve(ctx)
	}
	return parseCredentials(id.Secret)
}

type credsJSON struct {
	AccessKeyID     string `json:"access_key_id"`
	SecretAccessKey string `json:"secret_access_key"`
	SessionToken    string `json:"session_token"`
}

// parseCredentials decodes AWS credentials from the EgressIdentity secret. It
// accepts JSON or a colon-separated access_key_id:secret_access_key[:session].
func parseCredentials(secret string) (aws.Credentials, error) {
	secret = strings.TrimSpace(secret)
	if secret == "" {
		return aws.Credentials{}, errors.New("empty AWS credentials")
	}
	if strings.HasPrefix(secret, "{") {
		var c credsJSON
		if err := json.Unmarshal([]byte(secret), &c); err != nil {
			return aws.Credentials{}, fmt.Errorf("decode JSON credentials: %w", err)
		}
		if c.AccessKeyID == "" || c.SecretAccessKey == "" {
			return aws.Credentials{}, errors.New("JSON credentials missing access_key_id or secret_access_key")
		}
		return aws.Credentials{
			AccessKeyID:     c.AccessKeyID,
			SecretAccessKey: c.SecretAccessKey,
			SessionToken:    c.SessionToken,
		}, nil
	}
	parts := strings.Split(secret, ":")
	if len(parts) < 2 || parts[0] == "" || parts[1] == "" {
		return aws.Credentials{}, errors.New("malformed credentials (want access_key_id:secret_access_key[:session_token])")
	}
	creds := aws.Credentials{AccessKeyID: parts[0], SecretAccessKey: parts[1]}
	if len(parts) >= 3 {
		creds.SessionToken = parts[2]
	}
	return creds, nil
}
