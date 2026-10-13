package harness_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	fakeoa "github.com/inferencepath/membrane/test/fakes/openai"
	"github.com/inferencepath/membrane/test/harness"
)

// TestBuildChatWiresASlice confirms the harness produces a working, fully-wired
// chat Handler against a Provider endpoint, using the shared test fixtures.
func TestBuildChatWiresASlice(t *testing.T) {
	t.Setenv(harness.EgressEnv, harness.EgressToken)
	fake := fakeoa.New(fakeoa.Config{Content: "pong", Model: "backing", FinishReason: "stop"})
	defer fake.Close()

	h, err := harness.BuildChat(fake.URL(), nil)
	if err != nil {
		t.Fatalf("BuildChat: %v", err)
	}
	ts := httptest.NewServer(h)
	defer ts.Close()

	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/v1/chat", strings.NewReader(`{"pathway":"chat","messages":[{"role":"user","content":"ping"}]}`))
	req.Header.Set("Authorization", "Bearer "+harness.CallerKey)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	var out struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&out)
	if out.Message.Content != "pong" {
		t.Fatalf("content = %q", out.Message.Content)
	}
}

// TestBuildChatMissingEgressFailsClosed asserts the harness surfaces a wiring
// error when the Egress Identity cannot be resolved.
func TestBuildChatMissingEgressFailsClosed(t *testing.T) {
	// EgressEnv deliberately unset.
	if _, err := harness.BuildChat("http://127.0.0.1:0", nil); err == nil {
		t.Fatal("expected BuildChat to fail closed without an Egress Identity")
	}
}
