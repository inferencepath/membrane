// Live-path Diffusion-leak tests for the image_generation operation.
// They drive the fully-wired images handler against the capturing fake Provider
// and assert the exact upstream request carries the Egress Identity and NONE of
// the caller/org identity, Caller Key, or Interior control fields.
package diffusionleak

import (
	"net/http/httptest"
	"testing"

	fakeoa "github.com/inferencepath/membrane/test/fakes/openai"
	"github.com/inferencepath/membrane/test/harness"
)

func TestLiveLeakImage(t *testing.T) {
	t.Setenv(harness.EgressEnv, harness.EgressToken)
	fake := fakeoa.New(fakeoa.Config{ImageURLs: []string{"https://host/img.png"}})
	defer fake.Close()
	h, err := harness.BuildImages(fake.URL(), nil)
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(h)
	defer ts.Close()

	sendWithIdentity(t, ts.URL+"/v1/images/generations", `{"pathway":"paint","reveal":true,"prompt":"a red cat"}`)
	assertUpstreamClean(t, fake.Last())
}

func TestLiveLeakImageErrorPath(t *testing.T) {
	t.Setenv(harness.EgressEnv, harness.EgressToken)
	fake := fakeoa.New(fakeoa.Config{Status: 500})
	defer fake.Close()
	h, _ := harness.BuildImages(fake.URL(), nil)
	ts := httptest.NewServer(h)
	defer ts.Close()

	sendWithIdentity(t, ts.URL+"/v1/images/generations", `{"pathway":"paint","prompt":"a red cat"}`)
	assertUpstreamClean(t, fake.Last())
}
