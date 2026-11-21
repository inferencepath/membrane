package gemini

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"

	"github.com/inferencepath/membrane/internal/exterior/providers"
	"github.com/inferencepath/membrane/internal/types"
)

// rankingHost serves the Vertex AI Ranking API (Discovery Engine). Ranking
// configs live in the global location.
const rankingHost = "https://discoveryengine.googleapis.com"

func rerankDocuments(raw any) ([]string, error) {
	arr, ok := raw.([]any)
	if !ok {
		if ss, ok := raw.([]string); ok {
			return ss, nil
		}
		return nil, fmt.Errorf("gemini: rerank documents must be a list")
	}
	out := make([]string, 0, len(arr))
	for _, d := range arr {
		switch v := d.(type) {
		case string:
			out = append(out, v)
		case map[string]any:
			t, _ := v["text"].(string)
			out = append(out, t)
		default:
			return nil, fmt.Errorf("gemini: rerank documents must be strings")
		}
	}
	return out, nil
}

// Rerank implements providers.RerankProvider with the Vertex AI Ranking API.
// It needs a GCP project, so it is available only on the Vertex AI surface;
// on AI Studio it fails with ErrOperationUnsupported before any egress.
func (a *Adapter) Rerank(ctx context.Context, req providers.Request, id providers.EgressIdentity) (types.CanonicalRerank, error) {
	if !a.vertex {
		return types.CanonicalRerank{}, fmt.Errorf("gemini: rerank needs Vertex AI (a GCP project): %w", providers.ErrOperationUnsupported)
	}
	query, _ := req.Payload["query"].(string)
	docs, err := rerankDocuments(req.Payload["documents"])
	if err != nil {
		return types.CanonicalRerank{}, err
	}
	records := make([]any, len(docs))
	for i, d := range docs {
		records[i] = map[string]any{"id": strconv.Itoa(i), "content": d}
	}
	body := map[string]any{"model": req.Model, "query": query, "records": records}
	if n, ok := asInt(req.Payload["top_n"]); ok && n > 0 {
		body["topN"] = n
	}
	returnDocs, _ := req.Payload["return_documents"].(bool)
	body["ignoreRecordDetailsInResponse"] = !returnDocs
	raw, err := json.Marshal(body)
	if err != nil {
		return types.CanonicalRerank{}, fmt.Errorf("gemini: marshal request: %w", err)
	}
	host := a.baseURL
	if host == "" {
		host = rankingHost
	}
	u := fmt.Sprintf("%s/v1/projects/%s/locations/global/rankingConfigs/default_ranking_config:rank", host, url.PathEscape(a.project))
	httpReq, err := a.newHTTPRequest(ctx, u, raw, id, false)
	if err != nil {
		return types.CanonicalRerank{}, err
	}
	resp, err := a.client.Do(httpReq)
	if err != nil {
		return types.CanonicalRerank{}, mapErr(ctx, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode/100 != 2 {
		return types.CanonicalRerank{}, providers.UpstreamStatusError{Status: resp.StatusCode, Provider: a.id}
	}
	var out struct {
		Records []struct {
			ID    string  `json:"id"`
			Score float64 `json:"score"`
		} `json:"records"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return types.CanonicalRerank{}, fmt.Errorf("gemini: decode response: %w", err)
	}
	results := make([]types.RerankResult, 0, len(out.Records))
	for _, r := range out.Records {
		i, err := strconv.Atoi(r.ID)
		if err != nil || i < 0 || i >= len(docs) {
			return types.CanonicalRerank{}, fmt.Errorf("gemini: rerank returned an unknown record id")
		}
		rr := types.RerankResult{Index: i, RelevanceScore: r.Score}
		if returnDocs {
			rr.Document = docs[i]
		}
		results = append(results, rr)
	}
	return types.CanonicalRerank{Results: results, Provider: a.id, Model: req.Model}, nil
}
