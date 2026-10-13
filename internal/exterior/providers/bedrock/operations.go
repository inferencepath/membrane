// This file extends the Bedrock adapter with the operation-breadth
// methods its models natively support: embeddings (Titan/Cohere), rerank
// (Cohere), and image generation (Titan Image). Each builds its request ONLY
// from the neutral providers.Request plus the injected EgressIdentity (AWS
// SigV4 or Bedrock bearer token) — no Caller identity, Caller Key, or Reveal
// flag is accessible here. Bedrock selects the model via the path (/model/{id}
// /invoke), so the body carries neither a model name nor a stream flag, and the
// logical model surfaced on Reveal is the request's resolved model.
package bedrock

import (
	"context"
	"encoding/json"
	"fmt"
	"io"

	"github.com/inferencepath/membrane/internal/exterior/providers"
	"github.com/inferencepath/membrane/internal/types"
)

// opBody copies the allowlisted payload, dropping the path-selected model and the
// stream flag (these operations are non-streaming over /invoke).
func opBody(req providers.Request) ([]byte, error) {
	body := make(map[string]any, len(req.Payload))
	for k, v := range req.Payload {
		body[k] = v
	}
	delete(body, "model")
	delete(body, "stream")
	return json.Marshal(body)
}

func (a *Adapter) invoke(ctx context.Context, req providers.Request, id providers.EgressIdentity) ([]byte, error) {
	body, err := opBody(req)
	if err != nil {
		return nil, fmt.Errorf("bedrock: marshal request: %w", err)
	}
	httpReq, err := a.authRequest(ctx, a.invokeURL(req.Model), body, id)
	if err != nil {
		return nil, err
	}
	resp, err := a.client.Do(httpReq)
	if err != nil {
		return nil, mapErr(ctx, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode/100 != 2 {
		return nil, providers.UpstreamStatusError{Status: resp.StatusCode, Provider: a.id}
	}
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("bedrock: read response: %w", err)
	}
	return raw, nil
}

// Embeddings implements providers.EmbeddingsProvider. It accepts both the Titan
// shape (single "embedding" vector + "inputTextTokenCount") and the Cohere shape
// ("embeddings" as an array of vectors), passing vectors through as-is so the
// operation is encoding-agnostic.
func (a *Adapter) Embeddings(ctx context.Context, req providers.Request, id providers.EgressIdentity) (types.CanonicalEmbeddings, error) {
	raw, err := a.invoke(ctx, req, id)
	if err != nil {
		return types.CanonicalEmbeddings{}, err
	}
	var br struct {
		Embedding            json.RawMessage   `json:"embedding"`
		Embeddings           []json.RawMessage `json:"embeddings"`
		InputTextTokenCount  int               `json:"inputTextTokenCount"`
		InputTextTokenCount2 int               `json:"input_tokens"`
	}
	if err := json.Unmarshal(raw, &br); err != nil {
		return types.CanonicalEmbeddings{}, fmt.Errorf("bedrock: decode response: %w", err)
	}
	var data []types.EmbeddingVector
	switch {
	case len(br.Embeddings) > 0:
		data = make([]types.EmbeddingVector, 0, len(br.Embeddings))
		for i, e := range br.Embeddings {
			var vec any
			if err := json.Unmarshal(e, &vec); err != nil {
				return types.CanonicalEmbeddings{}, fmt.Errorf("bedrock: decode embedding: %w", err)
			}
			data = append(data, types.EmbeddingVector{Index: i, Embedding: vec})
		}
	case len(br.Embedding) > 0:
		var vec any
		if err := json.Unmarshal(br.Embedding, &vec); err != nil {
			return types.CanonicalEmbeddings{}, fmt.Errorf("bedrock: decode embedding: %w", err)
		}
		data = append(data, types.EmbeddingVector{Index: 0, Embedding: vec})
	}
	tokens := br.InputTextTokenCount
	if tokens == 0 {
		tokens = br.InputTextTokenCount2
	}
	return types.CanonicalEmbeddings{
		Data:     data,
		Usage:    types.EmbeddingsUsage{PromptTokens: tokens, TotalTokens: tokens},
		Provider: a.id,
		Model:    req.Model,
	}, nil
}

// Rerank implements providers.RerankProvider using the Cohere-on-Bedrock rerank
// response schema (results with index + relevance_score; optional document text).
func (a *Adapter) Rerank(ctx context.Context, req providers.Request, id providers.EgressIdentity) (types.CanonicalRerank, error) {
	raw, err := a.invoke(ctx, req, id)
	if err != nil {
		return types.CanonicalRerank{}, err
	}
	var br struct {
		Results []struct {
			Index          int     `json:"index"`
			RelevanceScore float64 `json:"relevance_score"`
			Document       *struct {
				Text string `json:"text"`
			} `json:"document"`
		} `json:"results"`
	}
	if err := json.Unmarshal(raw, &br); err != nil {
		return types.CanonicalRerank{}, fmt.Errorf("bedrock: decode response: %w", err)
	}
	results := make([]types.RerankResult, 0, len(br.Results))
	for _, r := range br.Results {
		rr := types.RerankResult{Index: r.Index, RelevanceScore: r.RelevanceScore}
		if r.Document != nil {
			rr.Document = r.Document.Text
		}
		results = append(results, rr)
	}
	return types.CanonicalRerank{Results: results, Provider: a.id, Model: req.Model}, nil
}

// Images implements providers.ImageProvider using the Titan Image response shape
// ("images" as an array of base64 strings).
func (a *Adapter) Images(ctx context.Context, req providers.Request, id providers.EgressIdentity) (types.CanonicalImage, error) {
	raw, err := a.invoke(ctx, req, id)
	if err != nil {
		return types.CanonicalImage{}, err
	}
	var br struct {
		Images []string `json:"images"`
	}
	if err := json.Unmarshal(raw, &br); err != nil {
		return types.CanonicalImage{}, fmt.Errorf("bedrock: decode response: %w", err)
	}
	data := make([]types.Image, 0, len(br.Images))
	for _, b64 := range br.Images {
		data = append(data, types.Image{B64JSON: b64})
	}
	return types.CanonicalImage{
		Data:     data,
		Usage:    types.ImageUsage{Images: len(data)},
		Provider: a.id,
		Model:    req.Model,
	}, nil
}
