package console

// The PathwayCreator port writes a pathway's routing and its permeability
// policy in one transaction. The loader fails closed on a pathway without a
// policy (and vice versa), so saving either kind alone would be rejected at
// reload.

// PathwayWizardInput is the create or update payload for one pathway and its
// permeability policy. Field lists hold logical field names.
type PathwayWizardInput struct {
	Pathway string `json:"pathway"`
	// Operation is the egress operation the pathway serves; empty means "chat".
	// It is validated against the operation set and its Permeability vocabulary.
	Operation       string   `json:"operation"`
	Providers       []string `json:"providers"`
	DefaultProvider string   `json:"defaultProvider"`
	CascadeAllowed  bool     `json:"cascadeAllowed"`
	OutboundAllow   []string `json:"outboundAllow"`
	InboundDefault  []string `json:"inboundDefault"`
	InboundOnReveal []string `json:"inboundOnReveal"`
}

// PathwayCreator atomically upserts a pathway and its permeability policy. A nil
// PathwayCreator disables the wizard endpoint (503).
type PathwayCreator interface {
	CreatePathway(in PathwayWizardInput) ApplyResult
}
