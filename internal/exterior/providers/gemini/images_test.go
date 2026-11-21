package gemini_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/inferencepath/membrane/internal/exterior/providers"
	"github.com/inferencepath/membrane/internal/exterior/providers/gemini"
)

func TestImagesAIStudio(t *testing.T) {
	var gotPath, gotKey string
	var gotBody map[string]any
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotKey = r.URL.Path, r.Header.Get("x-goog-api-key")
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &gotBody)
		_, _ = io.WriteString(w, `{"predictions":[{"bytesBase64Encoded":"AAA","mimeType":"image/png"},{"bytesBase64Encoded":"BBB","mimeType":"image/png"}]}`)
	}))
	defer ts.Close()

	a := gemini.New(ts.URL, 0)
	res, err := a.Images(context.Background(), providers.Request{
		Model: "imagen-4.0-generate-001",
		Payload: map[string]any{
			"prompt": "a lighthouse", "n": 2, "size": "1792x1024",
			"quality": "hd", "style": "vivid", "response_format": "url",
		},
	}, providers.EgressIdentity{Secret: "k"})
	if err != nil {
		t.Fatalf("Images: %v", err)
	}
	if gotPath != "/v1beta/models/imagen-4.0-generate-001:predict" {
		t.Errorf("path = %q", gotPath)
	}
	if gotKey != "k" {
		t.Errorf("x-goog-api-key = %q", gotKey)
	}
	inst := gotBody["instances"].([]any)[0].(map[string]any)
	params := gotBody["parameters"].(map[string]any)
	if inst["prompt"] != "a lighthouse" || params["sampleCount"] != float64(2) || params["aspectRatio"] != "16:9" {
		t.Errorf("body = %v", gotBody)
	}
	for _, f := range []string{"quality", "style", "response_format"} {
		if _, ok := params[f]; ok {
			t.Errorf("%s must not be sent to Imagen", f)
		}
	}
	if len(res.Data) != 2 || res.Data[0].B64JSON != "AAA" || res.Usage.Images != 2 || res.Model != "imagen-4.0-generate-001" {
		t.Errorf("result = %+v", res)
	}
}

func TestImagesVertexPath(t *testing.T) {
	var gotPath, gotAuth string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotAuth = r.URL.Path, r.Header.Get("Authorization")
		_, _ = io.WriteString(w, `{"predictions":[{"bytesBase64Encoded":"AAA"}]}`)
	}))
	defer ts.Close()

	a := gemini.New(ts.URL, 0, gemini.WithVertex("proj", "us-central1"),
		gemini.WithTokenSource(func(context.Context) (string, error) { return "tok", nil }))
	if _, err := a.Images(context.Background(), providers.Request{
		Model: "imagen-4.0-generate-001", Payload: map[string]any{"prompt": "x"},
	}, providers.EgressIdentity{}); err != nil {
		t.Fatalf("Images: %v", err)
	}
	if gotPath != "/v1/projects/proj/locations/us-central1/publishers/google/models/imagen-4.0-generate-001:predict" {
		t.Errorf("path = %q", gotPath)
	}
	if gotAuth != "Bearer tok" {
		t.Errorf("Authorization = %q", gotAuth)
	}
}

func TestImagesGeminiImageModel(t *testing.T) {
	var calls int
	var gotPath string
	var gotBody map[string]any
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		gotPath = r.URL.Path
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &gotBody)
		_, _ = io.WriteString(w, `{"candidates":[{"content":{"parts":[{"text":"here"},{"inlineData":{"mimeType":"image/png","data":"IMG"}}]}}]}`)
	}))
	defer ts.Close()

	a := gemini.New(ts.URL, 0)
	res, err := a.Images(context.Background(), providers.Request{
		Model:   "gemini-3.1-flash-lite-image",
		Payload: map[string]any{"prompt": "a lighthouse", "n": 2, "size": "1024x1024"},
	}, providers.EgressIdentity{Secret: "k"})
	if err != nil {
		t.Fatalf("Images: %v", err)
	}
	if gotPath != "/v1beta/models/gemini-3.1-flash-lite-image:generateContent" || calls != 2 {
		t.Errorf("path = %q, calls = %d", gotPath, calls)
	}
	gen := gotBody["generationConfig"].(map[string]any)
	if gen["responseModalities"].([]any)[0] != "IMAGE" || gen["imageConfig"].(map[string]any)["aspectRatio"] != "1:1" {
		t.Errorf("generationConfig = %v", gen)
	}
	if len(res.Data) != 2 || res.Data[1].B64JSON != "IMG" || res.Usage.Images != 2 {
		t.Errorf("result = %+v", res)
	}
}

func TestImagesGeminiTextOnlyIsAnError(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"candidates":[{"content":{"parts":[{"text":"I can't draw that"}]}}]}`)
	}))
	defer ts.Close()
	a := gemini.New(ts.URL, 0)
	if _, err := a.Images(context.Background(), providers.Request{
		Model: "gemini-3.1-flash-lite-image", Payload: map[string]any{"prompt": "x"},
	}, providers.EgressIdentity{Secret: "k"}); err == nil {
		t.Fatal("want error when no image comes back")
	}
}

func TestImagesAllFilteredIsAnError(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"predictions":[{"raiFilteredReason":"filtered"}]}`)
	}))
	defer ts.Close()
	a := gemini.New(ts.URL, 0)
	if _, err := a.Images(context.Background(), providers.Request{
		Model: "imagen-4.0-generate-001", Payload: map[string]any{"prompt": "x"},
	}, providers.EgressIdentity{Secret: "k"}); err == nil {
		t.Fatal("want error when every image is filtered")
	}
}
