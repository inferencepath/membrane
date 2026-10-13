package azureopenai_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/inferencepath/membrane/internal/exterior/providers"
	"github.com/inferencepath/membrane/internal/exterior/providers/azureopenai"
	"github.com/inferencepath/membrane/internal/types"
)

func TestID(t *testing.T) {
	if got := azureopenai.New("https://x.openai.azure.com", "dep", 0).ID(); got != "azure-openai" {
		t.Fatalf("ID = %q", got)
	}
}

func TestCompleteSuccess(t *testing.T) {
	var gotPath, gotQuery, gotKey string
	var gotBody map[string]any
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotQuery = r.URL.RawQuery
		gotKey = r.Header.Get("api-key")
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &gotBody)
		if r.Header.Get("Authorization") != "" {
			t.Error("Authorization leaked to provider")
		}
		_, _ = io.WriteString(w, `{"model":"gpt-4o","choices":[{"message":{"content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":1,"total_tokens":4}}`)
	}))
	defer ts.Close()

	a := azureopenai.New(ts.URL, "my-deploy", 0)
	resp, err := a.Complete(context.Background(), providers.Request{
		Model:   "gpt-4o",
		Payload: map[string]any{"messages": []any{}, "temperature": 0.2, "stream": false},
	}, providers.EgressIdentity{Secret: "azkey"})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}

	if !strings.Contains(gotPath, "/openai/deployments/my-deploy/chat/completions") {
		t.Errorf("path = %q", gotPath)
	}
	if !strings.Contains(gotQuery, "api-version=") {
		t.Errorf("query = %q", gotQuery)
	}
	if gotKey != "azkey" {
		t.Errorf("api-key = %q", gotKey)
	}
	if _, ok := gotBody["model"]; ok {
		t.Error("model must not be in Azure body (deployment is in path)")
	}
	if _, ok := gotBody["stream"]; ok {
		t.Error("stream must not be sent")
	}
	if resp.Message.Content != "ok" || resp.Usage.TotalTokens != 4 || resp.Provider != "azure-openai" {
		t.Errorf("resp = %+v", resp)
	}
}

func TestCompleteModelFallback(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"choices":[{"message":{"content":"x"},"finish_reason":"stop"}]}`)
	}))
	defer ts.Close()
	a := azureopenai.New(ts.URL, "dep", 0)
	resp, err := a.Complete(context.Background(), providers.Request{Model: "logical-model"}, providers.EgressIdentity{Secret: "k"})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Model != "logical-model" {
		t.Errorf("model = %q, want fallback to request model", resp.Model)
	}
}

func TestCompleteNoChoices(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"choices":[]}`)
	}))
	defer ts.Close()
	a := azureopenai.New(ts.URL, "dep", 0)
	if _, err := a.Complete(context.Background(), providers.Request{Model: "m"}, providers.EgressIdentity{Secret: "k"}); err == nil {
		t.Fatal("want no-choices error")
	}
}

func TestCompleteUpstreamError(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer ts.Close()
	a := azureopenai.New(ts.URL, "dep", 0)
	_, err := a.Complete(context.Background(), providers.Request{Model: "m"}, providers.EgressIdentity{Secret: "k"})
	var se providers.UpstreamStatusError
	if !errors.As(err, &se) || se.Status != 500 {
		t.Fatalf("err = %v", err)
	}
}

func TestCompleteBadJSON(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `nope`)
	}))
	defer ts.Close()
	a := azureopenai.New(ts.URL, "dep", 0)
	if _, err := a.Complete(context.Background(), providers.Request{Model: "m"}, providers.EgressIdentity{Secret: "k"}); err == nil {
		t.Fatal("want decode error")
	}
}

func TestCompleteTimeout(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(100 * time.Millisecond)
	}))
	defer ts.Close()
	ctx, cancel := context.WithTimeout(context.Background(), time.Millisecond)
	defer cancel()
	a := azureopenai.New(ts.URL, "dep", 0)
	_, err := a.Complete(ctx, providers.Request{Model: "m"}, providers.EgressIdentity{Secret: "k"})
	if !errors.Is(err, providers.ErrUpstreamTimeout) {
		t.Fatalf("err = %v, want ErrUpstreamTimeout", err)
	}
}

func TestCompleteBadURL(t *testing.T) {
	a := azureopenai.New("http://bad host with spaces", "dep", 0)
	if _, err := a.Complete(context.Background(), providers.Request{Model: "m"}, providers.EgressIdentity{Secret: "k"}); err == nil {
		t.Fatal("want request-construction error")
	}
}

func TestStreamSuccess(t *testing.T) {
	var gotAccept, gotKey string
	var gotBody map[string]any
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAccept = r.Header.Get("Accept")
		gotKey = r.Header.Get("api-key")
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &gotBody)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"Hel\"}}]}\n\n")
		_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"lo\"},\"finish_reason\":\"stop\"}]}\n\n")
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
	}))
	defer ts.Close()

	a := azureopenai.New(ts.URL, "dep", 0)
	var deltas []string
	var finish string
	var done bool
	err := a.Stream(context.Background(), providers.Request{Model: "m", Payload: map[string]any{"messages": []any{}}},
		providers.EgressIdentity{Secret: "azkey"}, func(c types.StreamChunk) error {
			switch {
			case c.Done:
				done = true
			default:
				if c.Delta != "" {
					deltas = append(deltas, c.Delta)
				}
				if c.FinishReason != "" {
					finish = c.FinishReason
				}
			}
			return nil
		})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	if gotAccept != "text/event-stream" || gotKey != "azkey" {
		t.Errorf("headers accept=%q key=%q", gotAccept, gotKey)
	}
	if gotBody["stream"] != true {
		t.Errorf("stream flag not set")
	}
	if strings.Join(deltas, "") != "Hello" || finish != "stop" || !done {
		t.Fatalf("deltas=%v finish=%q done=%v", deltas, finish, done)
	}
}

func TestStreamUpstreamError(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer ts.Close()
	a := azureopenai.New(ts.URL, "dep", 0)
	err := a.Stream(context.Background(), providers.Request{Model: "m"}, providers.EgressIdentity{Secret: "k"}, func(types.StreamChunk) error { return nil })
	var se providers.UpstreamStatusError
	if !errors.As(err, &se) || se.Status != http.StatusBadGateway {
		t.Fatalf("err = %v", err)
	}
}
