// Package scrub implements the Membrane's Scrub: it rebuilds an outbound request
// from the Permeability allowlist into a SanitizedRequest.
//
// SanitizedRequest is defined here with unexported fields and no exported setter,
// so it can be constructed ONLY by Sanitize in this package. This makes an
// identity leak unrepresentable: no other package can place data into a
// SanitizedRequest, and the type has no field capable of holding caller/org
// identity.
package scrub

import "github.com/inferencepath/membrane/internal/types"

// SanitizedRequest is the only request permitted to cross the Membrane.
//
// It carries the logical Pathway (so the Membrane can select among that
// Pathway's pooled targets) and an allowlisted payload only. It has no field
// capable of holding caller/org identity (keys, headers, IPs, tenant tags,
// user/metadata, cookies, user-agents, correlation IDs) or the Reveal flag. All
// fields are unexported and are populated solely by Sanitize.
type SanitizedRequest struct {
	pathway types.PathwayID
	payload map[string]any
	stream  bool
}

// Pathway returns the logical Pathway this request targets. It is not
// caller/org identity; the Membrane uses it to select a pooled egress target.
func (s SanitizedRequest) Pathway() types.PathwayID { return s.pathway }

// Stream reports whether a streaming response was requested.
func (s SanitizedRequest) Stream() bool { return s.stream }

// Payload returns a copy of the allowlisted payload. A copy is returned so that
// callers cannot mutate the sanitized state after construction.
func (s SanitizedRequest) Payload() map[string]any {
	out := make(map[string]any, len(s.payload))
	for k, v := range s.payload {
		out[k] = v
	}
	return out
}
