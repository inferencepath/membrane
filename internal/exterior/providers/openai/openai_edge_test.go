package openai_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/inferencepath/membrane/internal/exterior/providers"
	"github.com/inferencepath/membrane/internal/exterior/providers/openai"
	"github.com/inferencepath/membrane/internal/types"
)

func rawServer(t *testing.T, h http.HandlerFunc) *httptest.Server {
	t.Helper()
	ts := httptest.NewServer(h)
	t.Cleanup(ts.Close)
	return ts
}

func TestCompleteNoChoices(t *testing.T) {
	ts := rawServer(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"model":"m","choices":[]}`)
	})
	a := openai.New("openai", ts.URL, 0)
	if _, err := a.Complete(context.Background(), req(), providers.EgressIdentity{}); err == nil {
		t.Fatal("expected error for empty choices")
	}
}

func TestCompleteDecodeError(t *testing.T) {
	ts := rawServer(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `not json at all`)
	})
	a := openai.New("openai", ts.URL, 0)
	if _, err := a.Complete(context.Background(), req(), providers.EgressIdentity{}); err == nil {
		t.Fatal("expected decode error")
	}
}

func TestCompleteBadURL(t *testing.T) {
	a := openai.New("openai", "http://bad host with spaces", 0)
	if _, err := a.Complete(context.Background(), req(), providers.EgressIdentity{}); err == nil {
		t.Fatal("expected request-construction error for malformed base URL")
	}
}

func TestCompleteConnError(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	url := ts.URL
	ts.Close() // nothing is listening now -> connection error (not a timeout)
	a := openai.New("openai", url, 0)
	_, err := a.Complete(context.Background(), req(), providers.EgressIdentity{})
	if err == nil || errors.Is(err, providers.ErrUpstreamTimeout) {
		t.Fatalf("err = %v, want a non-timeout transport error", err)
	}
}

func TestStreamEmitError(t *testing.T) {
	ts := rawServer(t, func(w http.ResponseWriter, _ *http.Request) {
		fl := w.(http.Flusher)
		_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"a\"}}]}\n\n")
		fl.Flush()
	})
	a := openai.New("openai", ts.URL, time.Second)
	r := req()
	r.Stream = true
	sentinel := errors.New("consumer stopped")
	err := a.Stream(context.Background(), r, providers.EgressIdentity{}, func(types.StreamChunk) error {
		return sentinel
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("err = %v, want the consumer error propagated", err)
	}
}

func TestStreamMalformedDataSkipped(t *testing.T) {
	ts := rawServer(t, func(w http.ResponseWriter, _ *http.Request) {
		fl := w.(http.Flusher)
		_, _ = io.WriteString(w, "data: {bad json\n\n")
		_, _ = io.WriteString(w, ": keepalive comment\n\n")
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
		fl.Flush()
	})
	a := openai.New("openai", ts.URL, time.Second)
	r := req()
	r.Stream = true
	var done bool
	var deltas int
	err := a.Stream(context.Background(), r, providers.EgressIdentity{}, func(c types.StreamChunk) error {
		if c.Done {
			done = true
		} else {
			deltas++
		}
		return nil
	})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	if deltas != 0 || !done {
		t.Fatalf("deltas = %d, done = %v (malformed frames must be skipped)", deltas, done)
	}
}

func TestStreamIdleTimeout(t *testing.T) {
	ts := rawServer(t, func(w http.ResponseWriter, r *http.Request) {
		fl := w.(http.Flusher)
		_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"a\"}}]}\n\n")
		fl.Flush()
		<-r.Context().Done() // stall: never send more data
	})
	a := openai.New("openai", ts.URL, 30*time.Millisecond)
	r := req()
	r.Stream = true
	err := a.Stream(context.Background(), r, providers.EgressIdentity{}, func(types.StreamChunk) error { return nil })
	if !errors.Is(err, providers.ErrUpstreamTimeout) {
		t.Fatalf("err = %v, want ErrUpstreamTimeout on idle", err)
	}
}

func TestStreamParentCancel(t *testing.T) {
	ts := rawServer(t, func(w http.ResponseWriter, r *http.Request) {
		fl := w.(http.Flusher)
		_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"a\"}}]}\n\n")
		fl.Flush()
		<-r.Context().Done()
	})
	a := openai.New("openai", ts.URL, 5*time.Second)
	r := req()
	r.Stream = true
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	err := a.Stream(ctx, r, providers.EgressIdentity{}, func(types.StreamChunk) error {
		cancel() // client disconnects after the first chunk
		return nil
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled on parent cancel", err)
	}
}
