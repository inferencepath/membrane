// This file extends the Gemini adapter with operation-breadth support:
// embeddings via the Gemini `embedContent` / `batchEmbedContents` endpoints. As
// with chat, the request is built ONLY from the neutral providers.Request plus
// the injected EgressIdentity — no Caller identity, Caller Key, or Reveal flag is
// ever accessible here.
package gemini

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/inferencepath/membrane/internal/exterior/providers"
	"github.com/inferencepath/membrane/internal/types"
)

// embedInputs normalizes the allowlisted `input` (a string or an array of
// strings) into an ordered list of texts to embed.
func embedInputs(raw any) []string {
	switch v := raw.(type) {
	case string:
		return []string{v}
	case []any:
		out := make([]string, 0, len(v))
		for _, e := range v {
			if s, ok := e.(string); ok {
				out = append(out, s)
			}
		}
		return out
	case []string:
		return v
	}
	return nil
}

func contentPart(text string) map[string]any {
	return map[string]any{"parts": []any{map[string]any{"text": text}}}
}

// Embeddings implements providers.EmbeddingsProvider using Gemini's
// embedContent (single input) / batchEmbedContents (multiple) endpoints, and
// normalizes the vectors to the provider-neutral CanonicalEmbeddings.
func (a *Adapter) Embeddings(ctx context.Context, req providers.Request, id providers.EgressIdentity) (types.CanonicalEmbeddings, error) {
	inputs := embedInputs(req.Payload["input"])
	if len(inputs) == 0 {
		return types.CanonicalEmbeddings{}, fmt.Errorf("gemini: embeddings input is empty")
	}
	modelRef := "models/" + req.Model

	var urlStr string
	var body []byte
	var err error
	if len(inputs) == 1 {
		urlStr = a.generateURL(req.Model, "embedContent", "")
		body, err = json.Marshal(map[string]any{"model": modelRef, "content": contentPart(inputs[0])})
	} else {
		urlStr = a.generateURL(req.Model, "batchEmbedContents", "")
		requests := make([]any, 0, len(inputs))
		for _, in := range inputs {
			requests = append(requests, map[string]any{"model": modelRef, "content": contentPart(in)})
		}
		body, err = json.Marshal(map[string]any{"requests": requests})
	}
	if err != nil {
		return types.CanonicalEmbeddings{}, fmt.Errorf("gemini: marshal request: %w", err)
	}

	httpReq, err := a.newHTTPRequest(ctx, urlStr, body, id, false)
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

	// Both endpoints return embeddings as {values:[...]}: embedContent under the
	// singular "embedding", batchEmbedContents under the plural "embeddings".
	var er struct {
		Embedding struct {
			Values json.RawMessage `json:"values"`
		} `json:"embedding"`
		Embeddings []struct {
			Values json.RawMessage `json:"values"`
		} `json:"embeddings"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&er); err != nil {
		return types.CanonicalEmbeddings{}, fmt.Errorf("gemini: decode response: %w", err)
	}

	raws := make([]json.RawMessage, 0, len(inputs))
	if len(er.Embeddings) > 0 {
		for _, e := range er.Embeddings {
			raws = append(raws, e.Values)
		}
	} else if len(er.Embedding.Values) > 0 {
		raws = append(raws, er.Embedding.Values)
	}

	data := make([]types.EmbeddingVector, 0, len(raws))
	for i, r := range raws {
		var emb any
		if len(r) > 0 {
			if err := json.Unmarshal(r, &emb); err != nil {
				return types.CanonicalEmbeddings{}, fmt.Errorf("gemini: decode embedding: %w", err)
			}
		}
		data = append(data, types.EmbeddingVector{Index: i, Embedding: emb})
	}

	return types.CanonicalEmbeddings{
		Data:     data,
		Usage:    types.EmbeddingsUsage{},
		Provider: a.id,
		Model:    req.Model,
	}, nil
}
