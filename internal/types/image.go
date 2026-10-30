package types

// ImageRequest is the inbound image generation request.
type ImageRequest struct {
	Pathway string `json:"pathway"`
	Prompt  string `json:"prompt"`
	// N optionally requests multiple images.
	N *int `json:"n,omitempty"`
	// Size, Quality, and Style are generation controls, used where the model
	// supports them.
	Size    string `json:"size,omitempty"`
	Quality string `json:"quality,omitempty"`
	Style   string `json:"style,omitempty"`
	// ResponseFormat is "url" (default) or "b64_json".
	ResponseFormat string `json:"response_format,omitempty"`
	// Reveal asks for the provider and model in the response. It does not
	// cross the Membrane.
	Reveal bool `json:"reveal,omitempty"`
	// Targets is the Caller's cascade, honored only when the Pathway sets
	// cascade_allowed. It does not cross the Membrane.
	Targets []ChatTarget `json:"targets,omitempty"`
}

// Image is one generated image, as a URL or an opaque base64 payload.
type Image struct {
	URL     string `json:"url,omitempty"`
	B64JSON string `json:"b64_json,omitempty"`
}

// ImageUsage holds the count of images produced.
type ImageUsage struct {
	Images int `json:"images"`
}

// CanonicalImage is the image result assembled on the inward path. Provider and
// Model reach the Caller only on Reveal, gated by the inbound Permeability
// on_reveal set.
type CanonicalImage struct {
	PathwayID PathwayID
	Data      []Image
	Usage     ImageUsage
	Provider  ProviderID
	Model     string
}

// ImageResponse is the image response returned to the Caller. Provider and
// Model are set only when the Caller requested Reveal.
type ImageResponse struct {
	Pathway  string     `json:"pathway"`
	Data     []Image    `json:"data"`
	Usage    ImageUsage `json:"usage"`
	Provider string     `json:"provider,omitempty"`
	Model    string     `json:"model,omitempty"`
}
