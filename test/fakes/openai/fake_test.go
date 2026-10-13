package openai_test

import (
	"bufio"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	fakeoa "github.com/inferencepath/membrane/test/fakes/openai"
)

func postJSON(t *testing.T, url, body string) *http.Response {
	t.Helper()
	resp, err := http.Post(url+"/v1/chat/completions", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func TestFakeCompletionAndCapture(t *testing.T) {
	f := fakeoa.New(fakeoa.Config{Content: "hi", Model: "m", FinishReason: "stop", TotalTokens: 3})
	defer f.Close()

	resp := postJSON(t, f.URL(), `{"model":"m","messages":[]}`)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), `"content":"hi"`) {
		t.Fatalf("body = %s", body)
	}
	// Identifying headers a real provider emits.
	if resp.Header.Get("openai-organization") == "" {
		t.Fatal("expected provider-identifying header on the fake response")
	}
	cap := f.Last()
	if cap.Method != http.MethodPost || cap.Path != "/v1/chat/completions" {
		t.Fatalf("capture = %+v", cap)
	}
}

func TestFakeStream(t *testing.T) {
	f := fakeoa.New(fakeoa.Config{StreamDeltas: []string{"a", "b"}, FinishReason: "stop"})
	defer f.Close()

	resp := postJSON(t, f.URL(), `{"model":"m","stream":true,"messages":[]}`)
	defer resp.Body.Close()
	if ct := resp.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("content-type = %q", ct)
	}
	var deltas int
	var sawDone bool
	sc := bufio.NewScanner(resp.Body)
	for sc.Scan() {
		line := sc.Text()
		switch {
		case strings.Contains(line, `"content":"a"`), strings.Contains(line, `"content":"b"`):
			deltas++
		case line == "data: [DONE]":
			sawDone = true
		}
	}
	if deltas != 2 || !sawDone {
		t.Fatalf("deltas = %d, sawDone = %v", deltas, sawDone)
	}
}

func TestFakeErrorStatus(t *testing.T) {
	f := fakeoa.New(fakeoa.Config{Status: 500})
	defer f.Close()
	resp := postJSON(t, f.URL(), `{"model":"m","messages":[]}`)
	defer resp.Body.Close()
	if resp.StatusCode != 500 {
		t.Fatalf("status = %d, want 500", resp.StatusCode)
	}
}

func TestFakeDelayAndSetConfig(t *testing.T) {
	f := fakeoa.New(fakeoa.Config{Content: "x", Delay: 20 * time.Millisecond})
	defer f.Close()
	start := time.Now()
	resp := postJSON(t, f.URL(), `{"model":"m","messages":[]}`)
	resp.Body.Close()
	if time.Since(start) < 20*time.Millisecond {
		t.Fatal("expected the configured delay to apply")
	}

	// SetConfig swaps behavior for subsequent requests.
	f.SetConfig(fakeoa.Config{Status: 503})
	resp2 := postJSON(t, f.URL(), `{"model":"m","messages":[]}`)
	resp2.Body.Close()
	if resp2.StatusCode != 503 {
		t.Fatalf("status after SetConfig = %d, want 503", resp2.StatusCode)
	}
}
