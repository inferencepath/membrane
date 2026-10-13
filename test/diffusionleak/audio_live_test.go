// Live-path Diffusion-leak tests for the audio operations:
// moderations (JSON), audio_speech (binary out), and audio_transcription
// (multipart in). Each drives the fully-wired handler against the capturing fake
// Provider and asserts the exact upstream request carries the Egress Identity and
// NONE of the caller/org identity, Caller Key, or Interior control fields.
package diffusionleak

import (
	"bytes"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	fakeoa "github.com/inferencepath/membrane/test/fakes/openai"
	"github.com/inferencepath/membrane/test/harness"
)

func TestLiveLeakModerations(t *testing.T) {
	t.Setenv(harness.EgressEnv, harness.EgressToken)
	fake := fakeoa.New(fakeoa.Config{Model: "backing", ModerationCategories: map[string]bool{}, ModerationScores: map[string]float64{}})
	defer fake.Close()
	h, err := harness.BuildModerations(fake.URL(), nil)
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(h)
	defer ts.Close()

	sendWithIdentity(t, ts.URL+"/v1/moderations", `{"pathway":"moderate","reveal":true,"input":"check me"}`)
	assertUpstreamClean(t, fake.Last())
}

func TestLiveLeakSpeech(t *testing.T) {
	t.Setenv(harness.EgressEnv, harness.EgressToken)
	fake := fakeoa.New(fakeoa.Config{Model: "backing", SpeechAudio: []byte("audio-bytes"), SpeechContentType: "audio/mpeg"})
	defer fake.Close()
	h, err := harness.BuildSpeech(fake.URL(), nil)
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(h)
	defer ts.Close()

	sendWithIdentity(t, ts.URL+"/v1/audio/speech", `{"pathway":"speak","reveal":true,"input":"say hi","voice":"alloy"}`)
	assertUpstreamClean(t, fake.Last())
}

func TestLiveLeakTranscription(t *testing.T) {
	t.Setenv(harness.EgressEnv, harness.EgressToken)
	fake := fakeoa.New(fakeoa.Config{Model: "backing", TranscriptionText: "hi"})
	defer fake.Close()
	h, err := harness.BuildTranscription(fake.URL(), nil)
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(h)
	defer ts.Close()

	// Build a multipart upload carrying a sensitive client filename and extra
	// identity markers in the body; none may cross the Membrane.
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	_ = mw.WriteField("pathway", "listen")
	_ = mw.WriteField("reveal", "true")
	fw, _ := mw.CreateFormFile("file", "evil-"+cookieVal+".wav")
	_, _ = fw.Write([]byte("RIFFfakeaudio"))
	_ = mw.Close()

	req, err := http.NewRequest(http.MethodPost, ts.URL+"/v1/audio/transcriptions", &buf)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+harness.CallerKey)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("X-Forwarded-For", fwdIP)
	req.Header.Set("User-Agent", callerUA)
	req.Header.Set("Cookie", cookieVal)
	req.Header.Set("X-Tenant", callerOrgID)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.ReadAll(resp.Body)
	_ = resp.Body.Close()

	assertUpstreamClean(t, fake.Last())
	// The client filename must not appear in the reconstructed upstream multipart.
	if strings.Contains(string(fake.Last().Body), "evil-") {
		t.Error("DIFFUSION LEAK: client filename crossed the Membrane")
	}
}
