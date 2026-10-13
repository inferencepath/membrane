// Live-path Diffusion-leak tests for the responses operation, covering
// both the non-streaming and streaming egress paths. They drive the fully-wired
// responses handler against the capturing fake Provider and assert the exact
// upstream request carries the Egress Identity and NONE of the caller/org
// identity, Caller Key, or Interior control fields.
package diffusionleak

import (
	"net/http/httptest"
	"testing"

	fakeoa "github.com/inferencepath/membrane/test/fakes/openai"
	"github.com/inferencepath/membrane/test/harness"
)

func TestLiveLeakResponsesNonStreaming(t *testing.T) {
	t.Setenv(harness.EgressEnv, harness.EgressToken)
	fake := fakeoa.New(fakeoa.Config{Model: "backing", ResponsesOutput: []map[string]any{{"type": "message", "content": "ok"}}, TotalTokens: 5})
	defer fake.Close()
	h, err := harness.BuildResponses(fake.URL(), nil)
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(h)
	defer ts.Close()

	sendWithIdentity(t, ts.URL+"/v1/responses", `{"pathway":"respond","reveal":true,"input":"hello"}`)
	assertUpstreamClean(t, fake.Last())
}

func TestLiveLeakResponsesStreaming(t *testing.T) {
	t.Setenv(harness.EgressEnv, harness.EgressToken)
	fake := fakeoa.New(fakeoa.Config{StreamDeltas: []string{"a", "b"}})
	defer fake.Close()
	h, _ := harness.BuildResponses(fake.URL(), nil)
	ts := httptest.NewServer(h)
	defer ts.Close()

	sendWithIdentity(t, ts.URL+"/v1/responses", `{"pathway":"respond","stream":true,"input":"hello"}`)
	assertUpstreamClean(t, fake.Last())
}

func TestLiveLeakResponsesErrorPath(t *testing.T) {
	t.Setenv(harness.EgressEnv, harness.EgressToken)
	fake := fakeoa.New(fakeoa.Config{Status: 500})
	defer fake.Close()
	h, _ := harness.BuildResponses(fake.URL(), nil)
	ts := httptest.NewServer(h)
	defer ts.Close()

	sendWithIdentity(t, ts.URL+"/v1/responses", `{"pathway":"respond","input":"hello"}`)
	assertUpstreamClean(t, fake.Last())
}
