// Live-path Diffusion-leak tests for the rerank operation. They drive
// the fully-wired rerank handler against the capturing fake Provider and assert
// the exact upstream request carries the Egress Identity and NONE of the
// caller/org identity, Caller Key, or Interior control fields.
package diffusionleak

import (
	"net/http/httptest"
	"testing"

	fakeoa "github.com/inferencepath/membrane/test/fakes/openai"
	"github.com/inferencepath/membrane/test/harness"
)

func TestLiveLeakRerank(t *testing.T) {
	t.Setenv(harness.EgressEnv, harness.EgressToken)
	fake := fakeoa.New(fakeoa.Config{Model: "backing", RerankResults: []float64{0.9, 0.2}, TotalTokens: 6})
	defer fake.Close()
	h, err := harness.BuildRerank(fake.URL(), nil)
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(h)
	defer ts.Close()

	sendWithIdentity(t, ts.URL+"/v1/rerank", `{"pathway":"rank","reveal":true,"query":"hello","documents":["a","b"]}`)
	assertUpstreamClean(t, fake.Last())
}

func TestLiveLeakRerankErrorPath(t *testing.T) {
	t.Setenv(harness.EgressEnv, harness.EgressToken)
	fake := fakeoa.New(fakeoa.Config{Status: 500})
	defer fake.Close()
	h, _ := harness.BuildRerank(fake.URL(), nil)
	ts := httptest.NewServer(h)
	defer ts.Close()

	sendWithIdentity(t, ts.URL+"/v1/rerank", `{"pathway":"rank","query":"hello","documents":["a","b"]}`)
	assertUpstreamClean(t, fake.Last())
}
