package permeability

import (
	"fmt"
	"sort"

	"github.com/inferencepath/membrane/internal/types"
)

// This file defines the per-Operation Permeability field vocabularies and the
// validation that a Pathway's policy only lists field names valid for its
// Operation. It complements the identity/control denylist (permeability.go),
// which is a Membrane-wide floor applied to the OUTBOUND set of EVERY operation.

// directionVocab is the allowable field-name set for one direction of one
// operation. A field not present here is rejected fail-closed at assembly.
type directionVocab struct {
	outbound map[string]struct{}
	// inbound covers both inbound.default and inbound.on_reveal; the on_reveal
	// disclosure fields (provider/model) are included so a policy may list them.
	inbound map[string]struct{}
}

func set(fields ...string) map[string]struct{} {
	m := make(map[string]struct{}, len(fields))
	for _, f := range fields {
		m[f] = struct{}{}
	}
	return m
}

// operationVocab is the authoritative per-operation field vocabulary for the
// non-chat operations. It is the single source of truth consulted by
// ValidateVocabulary.
//
// `chat` is deliberately ABSENT: it is left unrestricted to preserve the exact
// prior behavior (any non-denylisted field name is permitted outbound),
// so existing chat pathways need no migration. The identity/control
// denylist (permeability.go) still applies to chat's outbound set, as before.
var operationVocab = map[types.Operation]directionVocab{
	types.OperationEmbeddings: {
		outbound: set("input", "encoding_format", "dimensions"),
		inbound:  set("data", "usage", "provider", "model"),
	},
	types.OperationRerank: {
		outbound: set("query", "documents", "top_n", "return_documents"),
		inbound:  set("results", "usage", "provider", "model"),
	},
	types.OperationResponses: {
		outbound: set("input", "instructions", "tools", "tool_choice", "temperature", "max_output_tokens", "top_p", "stream"),
		inbound:  set("output", "finish_reason", "usage", "provider", "model"),
	},
	types.OperationImageGeneration: {
		outbound: set("prompt", "n", "size", "quality", "style", "response_format"),
		inbound:  set("data", "usage", "provider", "model"),
	},
	types.OperationAudioTranscription: {
		outbound: set("file", "language", "prompt", "response_format", "temperature"),
		inbound:  set("text", "segments", "usage", "provider", "model"),
	},
	types.OperationAudioSpeech: {
		outbound: set("input", "voice", "format", "speed"),
		inbound:  set("audio", "provider", "model"),
	},
	types.OperationModerations: {
		outbound: set("input"),
		inbound:  set("results", "provider", "model"),
	},
}

// Vocabulary returns the sorted allowable outbound and inbound field names for an
// operation, for display (e.g. the Console field palette). Unknown operations
// yield empty slices.
func Vocabulary(op types.Operation) (outbound, inbound []string) {
	v, ok := operationVocab[op]
	if !ok {
		return nil, nil
	}
	return sortedKeys(v.outbound), sortedKeys(v.inbound)
}

func sortedKeys(m map[string]struct{}) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// ValidateVocabulary checks that the DirectionSets for a Pathway only list field
// names valid for the given Operation. The outbound denylist is enforced
// elsewhere (at Parse); this adds the per-operation vocabulary gate used at
// assembly, once the Pathway's Operation is known. It fails closed on an unknown
// operation and on any out-of-vocabulary field name.
func ValidateVocabulary(pathway types.PathwayID, op types.Operation, ds DirectionSets) error {
	if op == "" {
		op = types.DefaultOperation
	}
	if !op.Valid() {
		return fmt.Errorf("permeability: pathway %q: unknown operation %q", pathway, op)
	}
	// `chat` is unrestricted (backward compatibility); only the denylist floor
	// enforced at Parse applies to it.
	if op == types.OperationChat {
		return nil
	}
	v, ok := operationVocab[op]
	if !ok {
		// A recognized non-chat operation must have a vocabulary; its absence is
		// a programming error, not operator input.
		return fmt.Errorf("permeability: pathway %q: no vocabulary defined for operation %q", pathway, op)
	}
	for _, f := range ds.Outbound.Allow {
		if _, ok := v.outbound[f]; !ok {
			return fmt.Errorf("permeability: pathway %q (operation %s): outbound.allow field %q is not valid for this operation", pathway, op, f)
		}
	}
	for _, f := range ds.Inbound.Default {
		if _, ok := v.inbound[f]; !ok {
			return fmt.Errorf("permeability: pathway %q (operation %s): inbound.default field %q is not valid for this operation", pathway, op, f)
		}
	}
	for _, f := range ds.Inbound.OnReveal {
		if _, ok := v.inbound[f]; !ok {
			return fmt.Errorf("permeability: pathway %q (operation %s): inbound.on_reveal field %q is not valid for this operation", pathway, op, f)
		}
	}
	return nil
}
