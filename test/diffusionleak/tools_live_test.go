// Tool-path Diffusion-leak tests. These drive the
// fully-wired chat handler over a tool-enabled Pathway and assert that identity
// cannot ride inside tool definitions, assistant tool_calls, arguments, or
// tool-role results on the Membrane->Provider hop — across the non-streaming,
// streaming, and error paths.
package diffusionleak

import (
	"net/http/httptest"
	"strings"
	"testing"

	fakeoa "github.com/inferencepath/membrane/test/fakes/openai"
	"github.com/inferencepath/membrane/test/harness"
)

// toolRoundTripBody is a well-formed tool loop: tools offered, a prior assistant
// tool_call, and a tool-role result referencing it (so the Interior loop guard
// passes). The reveal flag is set to confirm it never crosses even on this path.
const toolRoundTripBody = `{"pathway":"chat","reveal":true,` +
	`"tools":[{"name":"get_weather","description":"w","parameters":{"type":"object"}}],` +
	`"tool_choice":"auto",` +
	`"messages":[` +
	`{"role":"user","content":"weather?"},` +
	`{"role":"assistant","content":"","tool_calls":[{"id":"call_1","name":"get_weather","arguments":{"city":"NYC"}}]},` +
	`{"role":"tool","tool_call_id":"call_1","name":"get_weather","content":"{\"tempF\":61}"}]}`

func TestLiveLeakToolsNonStreaming(t *testing.T) {
	t.Setenv(harness.EgressEnv, harness.EgressToken)
	fake := fakeoa.New(fakeoa.Config{Content: "ok", Model: "backing", FinishReason: "stop"})
	defer fake.Close()
	h, err := harness.BuildChatTools(fake.URL(), nil)
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(h)
	defer ts.Close()

	sendWithIdentity(t, ts.URL+"/v1/chat", toolRoundTripBody)
	cap := fake.Last()
	assertUpstreamClean(t, cap)
	// Sanity: tools actually crossed (the Pathway opted in), so the clean
	// assertion is meaningful rather than vacuous.
	if !strings.Contains(string(cap.Body), "get_weather") {
		t.Fatalf("tools did not cross on an opted-in Pathway: %s", cap.Body)
	}
}

func TestLiveLeakToolsStreaming(t *testing.T) {
	t.Setenv(harness.EgressEnv, harness.EgressToken)
	fake := fakeoa.New(fakeoa.Config{StreamDeltas: []string{"a", "b"}, FinishReason: "stop"})
	defer fake.Close()
	h, _ := harness.BuildChatTools(fake.URL(), nil)
	ts := httptest.NewServer(h)
	defer ts.Close()

	streamBody := strings.Replace(toolRoundTripBody, `"reveal":true,`, `"reveal":true,"stream":true,`, 1)
	sendWithIdentity(t, ts.URL+"/v1/chat", streamBody)
	assertUpstreamClean(t, fake.Last())
}

func TestLiveLeakToolsErrorPath(t *testing.T) {
	t.Setenv(harness.EgressEnv, harness.EgressToken)
	fake := fakeoa.New(fakeoa.Config{Status: 500})
	defer fake.Close()
	h, _ := harness.BuildChatTools(fake.URL(), nil)
	ts := httptest.NewServer(h)
	defer ts.Close()

	sendWithIdentity(t, ts.URL+"/v1/chat", toolRoundTripBody)
	assertUpstreamClean(t, fake.Last())
}
