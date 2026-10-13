package types

// This file defines the logical, provider-agnostic `image_generation` API DTOs
// and the canonical inward representation. As with the other operations these
// are Interior-only shapes: the Caller Key is carried in the request transport
// (Authorization header), never in the body, and the Reveal flag is an
// Interior-only control field that never crosses the Membrane.

// ImageRequest is the inbound logical image generation request.
type ImageRequest struct {
	Pathway string `json:"pathway"`
	Prompt  string `json:"prompt"`
	// N optionally requests multiple images.
	N *int `json:"n,omitempty"`
	// Size, Quality, Style are provider/model-permitting generation controls.
	Size    string `json:"size,omitempty"`
	Quality string `json:"quality,omitempty"`
	Style   string `json:"style,omitempty"`
	// ResponseFormat is "url" (default) or "b64_json".
	ResponseFormat string `json:"response_format,omitempty"`
	// Reveal is the Interior-only opt-in to receive provider/model.
	Reveal bool `json:"reveal,omitempty"`
	// Targets is the caller-supplied cascade (honored only when the pathway sets
	// cascade_allowed). Interior-only routing control; never crosses the Membrane.
	Targets []ChatTarget `json:"targets,omitempty"`
}

// Image is one generated image, carried as a URL or opaque base64 payload.
type Image struct {
	URL     string `json:"url,omitempty"`
	B64JSON string `json:"b64_json,omitempty"`
}

// ImageUsage holds the count of images produced.
type ImageUsage struct {
	Images int `json:"images"`
}

// CanonicalImage is the provider-agnostic image result assembled on the inward
// path. Provider and Model are surfaced only when the Reveal flag was set (gated
// by the inbound Permeability on_reveal set).
type CanonicalImage struct {
	PathwayID PathwayID
	Data      []Image
	Usage     ImageUsage
	Provider  ProviderID
	Model     string
}

// ImageResponse is the Caller-facing, provider-neutral image response. Provider
// and Model are populated only when the Caller set the Reveal flag.
type ImageResponse struct {
	Pathway  string     `json:"pathway"`
	Data     []Image    `json:"data"`
	Usage    ImageUsage `json:"usage"`
	Provider string     `json:"provider,omitempty"`
	Model    string     `json:"model,omitempty"`
}
