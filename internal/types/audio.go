package types

// Audio API shapes for the audio_speech (text-to-speech) and
// audio_transcription (speech-to-text) operations.

// audio_speech

// SpeechRequest is the inbound text-to-speech request. The synthesized audio is
// returned as a binary response body.
type SpeechRequest struct {
	Pathway string `json:"pathway"`
	Input   string `json:"input"`
	Voice   string `json:"voice"`
	// Format is the audio container (mp3 default, opus, aac, flac, wav, pcm).
	Format string `json:"format,omitempty"`
	// Speed optionally adjusts playback speed.
	Speed *float64 `json:"speed,omitempty"`
	// Reveal asks for the provider and model in response headers. It is
	// consumed on the Interior and does not cross the Membrane.
	Reveal bool `json:"reveal,omitempty"`
	// Targets is the Caller's cascade, honored only when the Pathway sets
	// cascade_allowed. It does not cross the Membrane.
	Targets []ChatTarget `json:"targets,omitempty"`
}

// SpeechChunk is one unit of synthesized audio relayed on the inward path. The
// first chunk is a Head carrying the Content-Type and the ungated Provider and
// Model; later chunks carry Audio, and the last has Done set. The handler gates
// Provider and Model on Reveal before writing them as headers.
type SpeechChunk struct {
	Head        bool
	ContentType string
	Provider    ProviderID
	Model       string
	Audio       []byte
	Done        bool
}

// audio_transcription

// TranscriptionRequest is the inbound speech-to-text request. The handler reads
// the audio and the allowlisted form fields from a multipart upload, dropping
// the client filename and any extra parts.
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

// CanonicalTranscription is the transcription result assembled on the inward
// path. Provider and Model reach the Caller only on Reveal, gated by the inbound
// Permeability on_reveal set.
type CanonicalTranscription struct {
	PathwayID PathwayID
	Text      string
	Segments  []map[string]any
	Usage     TranscriptionUsage
	Provider  ProviderID
	Model     string
}

// TranscriptionResponse is the transcription response returned to the Caller.
// Provider and Model are set only when the Caller requested Reveal.
type TranscriptionResponse struct {
	Pathway  string             `json:"pathway"`
	Text     string             `json:"text"`
	Segments []map[string]any   `json:"segments,omitempty"`
	Usage    TranscriptionUsage `json:"usage"`
	Provider string             `json:"provider,omitempty"`
	Model    string             `json:"model,omitempty"`
}
