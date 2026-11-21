package types

// Content part kinds of a multimodal_chat turn.
const (
	PartText  = "text"
	PartImage = "image"
	PartAudio = "audio"
)

// ContentPart is one validated piece of a multimodal turn. Text carries Text;
// image and audio carry the decoded bytes in Data and their verified Format
// (png, jpeg, gif, webp for images; wav, mp3 for audio).
type ContentPart struct {
	Type   string
	Text   string
	Data   []byte
	Format string
}

// MultimodalMessage is one validated conversation turn of a multimodal_chat
// request.
type MultimodalMessage struct {
	Role  string
	Parts []ContentPart
}

// MultimodalAudioOutput asks for a spoken reply in the given voice and format.
type MultimodalAudioOutput struct {
	Voice  string
	Format string
}

// CanonicalMultimodal is the provider-agnostic multimodal_chat result: the reply
// text, and the spoken reply with its transcript when audio output was asked for.
type CanonicalMultimodal struct {
	PathwayID    PathwayID
	Text         string
	Audio        []byte
	AudioFormat  string
	Transcript   string
	FinishReason string
	Usage        Usage
	Provider     ProviderID
	Model        string
}

// MultimodalReplyMessage is the assistant turn in the Caller-facing response.
type MultimodalReplyMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// MultimodalReplyAudio is the spoken reply (base64) and its transcript.
type MultimodalReplyAudio struct {
	Data       []byte `json:"data"`
	Format     string `json:"format"`
	Transcript string `json:"transcript"`
}

// MultimodalResponse is the Caller-facing, provider-neutral multimodal_chat
// response body.
type MultimodalResponse struct {
	Pathway      string                 `json:"pathway"`
	Message      MultimodalReplyMessage `json:"message"`
	Audio        *MultimodalReplyAudio  `json:"audio,omitempty"`
	FinishReason string                 `json:"finish_reason,omitempty"`
	Usage        Usage                  `json:"usage"`
	Provider     string                 `json:"provider,omitempty"`
	Model        string                 `json:"model,omitempty"`
}
