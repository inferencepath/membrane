package console

// This file defines the console's PathwayCreator port: a single, atomic
// "create a pathway" operation that writes the pathway's routing (pathways kind)
// AND its Membrane policy (permeability kind) together in one transaction, then
// runs the standard validate → atomic write → reload pipeline.
//
// It exists because pathways and permeability are separate documents linked only
// by name, and the loader is fail-closed on that link: a pathway without a
// permeability policy (and vice versa) is rejected at reload. Saving either kind
// alone from the UI therefore hits an impossible intermediate state. The wizard
// submits both at once so a new pathway never lands half-configured.
//
// As with every console port, the types live in the console package (the console
// imports no store/app package); the concrete implementation is injected.

// PathwayWizardInput is the create/update payload for one pathway plus its
// permeability policy. Field lists are logical field names (no secrets).
type PathwayWizardInput struct {
	Pathway string `json:"pathway"`
	// Operation is the egress operation the pathway serves (default "chat" when
	// empty). Validated downstream against the closed operation enum and the
	// per-operation Permeability vocabulary.
	Operation       string   `json:"operation"`
	Providers       []string `json:"providers"`
	DefaultProvider string   `json:"defaultProvider"`
	CascadeAllowed  bool     `json:"cascadeAllowed"`
	OutboundAllow   []string `json:"outboundAllow"`
	InboundDefault  []string `json:"inboundDefault"`
	InboundOnReveal []string `json:"inboundOnReveal"`
}

// PathwayCreator atomically upserts a pathway and its permeability policy. A nil
// PathwayCreator disables the wizard endpoint (503). The returned ApplyResult
// reports the outcome of the single validate → write → reload pipeline.
type PathwayCreator interface {
	CreatePathway(in PathwayWizardInput) ApplyResult
}
