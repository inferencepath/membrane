package scrub

import (
	"fmt"

	"github.com/inferencepath/membrane/internal/membrane/permeability"
	"github.com/inferencepath/membrane/internal/types"
)

// streamField is the payload field that requests streaming. It has effect only
// when the Pathway's outbound allowlist includes it.
const streamField = "stream"

// Sanitize builds a SanitizedRequest from req using the Pathway's outbound
// Permeability allowlist. Only allowlisted fields present in the payload are
// copied; everything else, including Caller identity and Reveal, is dropped.
// Sanitize is the only way to build a populated SanitizedRequest.
func Sanitize(req types.CanonicalRequest, spec permeability.Spec) (SanitizedRequest, error) {
	ds, ok := spec.Pathways[req.PathwayID]
	if !ok {
		return SanitizedRequest{}, fmt.Errorf("scrub: no permeability for pathway %q", req.PathwayID)
	}

	payload := make(map[string]any, len(ds.Outbound.Allow))
	for _, key := range ds.Outbound.Allow {
		if v, present := req.Payload[key]; present {
			payload[key] = v
		}
	}

	stream := false
	if v, present := payload[streamField]; present {
		if b, ok := v.(bool); ok {
			stream = b
		}
	}

	return SanitizedRequest{pathway: req.PathwayID, payload: payload, stream: stream}, nil
}
