package scrub

import (
	"fmt"

	"github.com/inferencepath/membrane/internal/membrane/permeability"
	"github.com/inferencepath/membrane/internal/types"
)

// streamField is the well-known logical field controlling streaming. It only
// takes effect if present in the Pathway's outbound allowlist.
const streamField = "stream"

// Sanitize rebuilds an outbound request from the Pathway's outbound
// Permeability allowlist, producing a SanitizedRequest for the request's
// Pathway.
//
// It copies ONLY the fields named in the allowlist that are present in the
// request payload. Everything else — including caller/org identity and the
// Reveal flag — is dropped by construction. Sanitize is the sole constructor of
// a populated SanitizedRequest.
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
