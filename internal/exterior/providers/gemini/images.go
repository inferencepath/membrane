package gemini

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/inferencepath/membrane/internal/exterior/providers"
	"github.com/inferencepath/membrane/internal/types"
)

// imagenAspectRatios maps the OpenAI-style sizes to the nearest aspect ratio
// Imagen accepts (1:1, 3:4, 4:3, 9:16, 16:9).
var imagenAspectRatios = map[string]string{
	"256x256": "1:1", "512x512": "1:1", "1024x1024": "1:1",
	"1536x1024": "4:3", "1024x1536": "3:4",
	"1792x1024": "16:9", "1024x1792": "9:16",
}

// imagesBody builds an Imagen predict request. Imagen has no quality or style
// parameter and always returns base64, so quality, style and response_format
// are not sent.
func imagesBody(req providers.Request) ([]byte, error) {
	prompt, _ := req.Payload["prompt"].(string)
	params := map[string]any{"sampleCount": 1}
	if n, ok := asInt(req.Payload["n"]); ok && n > 0 {
		params["sampleCount"] = n
	}
	if size, _ := req.Payload["size"].(string); size != "" {
		if ar, ok := imagenAspectRatios[size]; ok {
			params["aspectRatio"] = ar
		}
	}
	return json.Marshal(map[string]any{
		"instances":  []any{map[string]any{"prompt": prompt}},
		"parameters": params,
	})
}

// Images implements providers.ImageProvider. Imagen models use the predict
// method; Gemini image models use generateContent with an IMAGE response
// modality. AI Studio and Vertex AI serve both with the same shapes.
func (a *Adapter) Images(ctx context.Context, req providers.Request, id providers.EgressIdentity) (types.CanonicalImage, error) {
	if !strings.HasPrefix(req.Model, "imagen") {
		return a.geminiImages(ctx, req, id)
	}
	body, err := imagesBody(req)
	if err != nil {
		return types.CanonicalImage{}, fmt.Errorf("gemini: marshal request: %w", err)
	}
	httpReq, err := a.newHTTPRequest(ctx, a.generateURL(req.Model, "predict", ""), body, id, false)
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
	var out struct {
		Predictions []struct {
			BytesBase64Encoded string `json:"bytesBase64Encoded"`
		} `json:"predictions"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return types.CanonicalImage{}, fmt.Errorf("gemini: decode response: %w", err)
	}
	data := make([]types.Image, 0, len(out.Predictions))
	for _, p := range out.Predictions {
		if p.BytesBase64Encoded != "" {
			data = append(data, types.Image{B64JSON: p.BytesBase64Encoded})
		}
	}
	if len(data) == 0 {
		return types.CanonicalImage{}, fmt.Errorf("gemini: no image returned (all filtered by the provider's safety filter)")
	}
	return types.CanonicalImage{
		Data:     data,
		Usage:    types.ImageUsage{Images: len(data)},
		Provider: a.id,
		Model:    req.Model,
	}, nil
}

// geminiImages generates images with a Gemini image model. generateContent
// returns one image per call, so n images take n calls.
func (a *Adapter) geminiImages(ctx context.Context, req providers.Request, id providers.EgressIdentity) (types.CanonicalImage, error) {
	prompt, _ := req.Payload["prompt"].(string)
	gen := map[string]any{"responseModalities": []string{"IMAGE"}}
	if size, _ := req.Payload["size"].(string); size != "" {
		if ar, ok := imagenAspectRatios[size]; ok {
			gen["imageConfig"] = map[string]any{"aspectRatio": ar}
		}
	}
	body, err := json.Marshal(map[string]any{
		"contents":         []any{map[string]any{"role": "user", "parts": []any{map[string]any{"text": prompt}}}},
		"generationConfig": gen,
	})
	if err != nil {
		return types.CanonicalImage{}, fmt.Errorf("gemini: marshal request: %w", err)
	}
	n := 1
	if v, ok := asInt(req.Payload["n"]); ok && v > 1 {
		n = v
	}
	data := make([]types.Image, 0, n)
	for i := 0; i < n; i++ {
		img, err := a.geminiImageOnce(ctx, req.Model, body, id)
		if err != nil {
			return types.CanonicalImage{}, err
		}
		data = append(data, img)
	}
	return types.CanonicalImage{
		Data:     data,
		Usage:    types.ImageUsage{Images: len(data)},
		Provider: a.id,
		Model:    req.Model,
	}, nil
}

func (a *Adapter) geminiImageOnce(ctx context.Context, model string, body []byte, id providers.EgressIdentity) (types.Image, error) {
	httpReq, err := a.newHTTPRequest(ctx, a.generateURL(model, "generateContent", ""), body, id, false)
	if err != nil {
		return types.Image{}, err
	}
	resp, err := a.client.Do(httpReq)
	if err != nil {
		return types.Image{}, mapErr(ctx, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode/100 != 2 {
		return types.Image{}, providers.UpstreamStatusError{Status: resp.StatusCode, Provider: a.id}
	}
	var out struct {
		Candidates []struct {
			Content struct {
				Parts []struct {
					InlineData *struct {
						Data string `json:"data"`
					} `json:"inlineData"`
				} `json:"parts"`
			} `json:"content"`
		} `json:"candidates"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return types.Image{}, fmt.Errorf("gemini: decode response: %w", err)
	}
	for _, c := range out.Candidates {
		for _, p := range c.Content.Parts {
			if p.InlineData != nil && p.InlineData.Data != "" {
				return types.Image{B64JSON: p.InlineData.Data}, nil
			}
		}
	}
	return types.Image{}, fmt.Errorf("gemini: no image returned (filtered by the provider's safety filter or answered in text)")
}
