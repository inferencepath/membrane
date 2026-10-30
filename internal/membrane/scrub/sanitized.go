// Package scrub implements the Membrane's Scrub, which rebuilds an outbound
// request from the Permeability allowlist into a SanitizedRequest.
//
// SanitizedRequest has unexported fields and no setters, so only Sanitize can
// populate one. No other package can put data into it, and it has no field
// that could hold Caller identity.
package scrub

import "github.com/inferencepath/membrane/internal/types"

// SanitizedRequest is the only request allowed to cross the Membrane.
//
// It holds the Pathway, used to pick among that Pathway's pooled targets, and
// the allowlisted payload. It has no field for Caller identity (keys, headers,
// IPs, tenant tags, user metadata, cookies, user agents, correlation IDs) or
// for Reveal. Only Sanitize sets its fields.
type SanitizedRequest struct {
	pathway types.PathwayID
	payload map[string]any
	stream  bool
}

// Pathway returns the Pathway this request targets. It is not Caller identity;
// the Membrane uses it to select a pooled egress target.
func (s SanitizedRequest) Pathway() types.PathwayID { return s.pathway }

// Stream reports whether a streaming response was requested.
func (s SanitizedRequest) Stream() bool { return s.stream }

// Payload returns a copy of the allowlisted payload, so callers cannot modify
// the request after Sanitize.
func (s SanitizedRequest) Payload() map[string]any {
	out := make(map[string]any, len(s.payload))
	for k, v := range s.payload {
		out[k] = v
	}
	return out
}
