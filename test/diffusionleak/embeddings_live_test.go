// Live-path Diffusion-leak tests for the embeddings operation. They
// drive the fully-wired embeddings handler against the capturing fake Provider
// and assert the exact upstream request carries the Egress Identity and NONE of
// the caller/org identity, Caller Key, or Interior control fields.
package diffusionleak

import (
	"net/http/httptest"
	"testing"

	fakeoa "github.com/inferencepath/membrane/test/fakes/openai"
	"github.com/inferencepath/membrane/test/harness"
)

func TestLiveLeakEmbeddings(t *testing.T) {
	t.Setenv(harness.EgressEnv, harness.EgressToken)
	fake := fakeoa.New(fakeoa.Config{Model: "backing", EmbeddingVectors: [][]float64{{0.1, 0.2, 0.3}}, PromptTokens: 4, TotalTokens: 4})
	defer fake.Close()
	h, err := harness.BuildEmbeddings(fake.URL(), nil)
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(h)
	defer ts.Close()

	sendWithIdentity(t, ts.URL+"/v1/embeddings", `{"pathway":"embed","reveal":true,"input":"hello world"}`)
	assertUpstreamClean(t, fake.Last())
}

func TestLiveLeakEmbeddingsErrorPath(t *testing.T) {
	t.Setenv(harness.EgressEnv, harness.EgressToken)
	fake := fakeoa.New(fakeoa.Config{Status: 500})
	defer fake.Close()
	h, _ := harness.BuildEmbeddings(fake.URL(), nil)
	ts := httptest.NewServer(h)
	defer ts.Close()

	sendWithIdentity(t, ts.URL+"/v1/embeddings", `{"pathway":"embed","input":["a","b"]}`)
	assertUpstreamClean(t, fake.Last())
}
