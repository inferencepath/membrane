package types

// This file defines the logical, provider-agnostic audio API DTOs for the
// `audio_speech` (text-to-speech) and `audio_transcription` (speech-to-text)
// operations. As with the other operations the Caller Key is carried in the
// request transport, never in the body, and the Reveal flag is Interior-only and
// never crosses the Membrane.

// ---- audio_speech (text-to-speech) ----

// SpeechRequest is the inbound logical text-to-speech request. The synthesized
// audio is returned as a binary response body (not JSON).
type SpeechRequest struct {
	Pathway string `json:"pathway"`
	Input   string `json:"input"`
	Voice   string `json:"voice"`
	// Format is the audio container (mp3 default, opus, aac, flac, wav, pcm).
	Format string `json:"format,omitempty"`
	// Speed optionally adjusts playback speed.
	Speed *float64 `json:"speed,omitempty"`
	// Reveal is the Interior-only opt-in to receive provider/model (via response
	// headers). It is consumed on the Interior and MUST NOT cross the Membrane.
	Reveal bool `json:"reveal,omitempty"`
	// Targets is the caller-supplied cascade (honored only when the pathway sets
	// cascade_allowed). Interior-only routing control; never crosses the Membrane.
	Targets []ChatTarget `json:"targets,omitempty"`
}

// SpeechChunk is one incremental unit relayed on the inward (Membrane→Interior)
// path for a speech synthesis. The FIRST chunk is a Head carrying the response
// Content-Type and the (ungated) Provider/Model; subsequent chunks carry Audio
// bytes; the final chunk has Done set. The handler gates Provider/Model by the
// Reveal flag before surfacing them as response headers. SpeechChunk is an inward
// relay type and never crosses the Membrane outward.
type SpeechChunk struct {
	Head        bool
	ContentType string
	Provider    ProviderID
	Model       string
	Audio       []byte
	Done        bool
}

// ---- audio_transcription (speech-to-text) ----

// TranscriptionRequest is the inbound logical speech-to-text request. The audio
// bytes arrive as a multipart upload; the handler extracts them (dropping the
// client filename and any extra parts) along with the allowlisted form fields.
type TranscriptionRequest struct {
	Pathway        string
	File           []byte
	Language       string
	Prompt         string
	ResponseFormat string
	Temperature    *float64
	Reveal         bool
	Targets        []ChatTarget
}

// TranscriptionUsage holds the audio duration processed, when reported.
type TranscriptionUsage struct {
	Seconds float64 `json:"seconds,omitempty"`
}

// CanonicalTranscription is the provider-agnostic transcription result assembled
// on the inward path. Provider and Model are surfaced only when the Reveal flag
// was set (gated by the inbound Permeability on_reveal set).
type CanonicalTranscription struct {
	PathwayID PathwayID
	Text      string
	Segments  []map[string]any
	Usage     TranscriptionUsage
	Provider  ProviderID
	Model     string
}

// TranscriptionResponse is the Caller-facing, provider-neutral transcription
// response. Provider and Model are populated only when the Caller set Reveal.
type TranscriptionResponse struct {
	Pathway  string             `json:"pathway"`
	Text     string             `json:"text"`
	Segments []map[string]any   `json:"segments,omitempty"`
	Usage    TranscriptionUsage `json:"usage"`
	Provider string             `json:"provider,omitempty"`
	Model    string             `json:"model,omitempty"`
}
