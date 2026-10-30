package permeability

import (
	"fmt"
	"sort"

	"github.com/inferencepath/membrane/internal/types"
)

// Per-operation field vocabularies. A Pathway's policy may list only fields
// valid for its Operation. The identity and control denylist applies to the
// outbound set of every operation on top of this.

// directionVocab holds the valid field names for each direction of one
// operation. Any other field is rejected at assembly.
type directionVocab struct {
	outbound map[string]struct{}
	// inbound covers both inbound.default and inbound.on_reveal, so it
	// includes the provider and model disclosure fields.
	inbound map[string]struct{}
}

func set(fields ...string) map[string]struct{} {
	m := make(map[string]struct{}, len(fields))
	for _, f := range fields {
		m[f] = struct{}{}
	}
	return m
}

// operationVocab is the field vocabulary for each non-chat operation, used by
// ValidateVocabulary.
//
// chat has no entry: any field not on the denylist is permitted outbound.
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

// Vocabulary returns the sorted outbound and inbound field names valid for op,
// for display in the Console field palette. It returns nil slices for chat and
// for unknown operations.
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

// ValidateVocabulary checks that ds lists only fields valid for op. It runs at
// assembly, once the Pathway's Operation is known; the denylist is checked
// earlier by Parse. An unknown operation or field is an error.
func ValidateVocabulary(pathway types.PathwayID, op types.Operation, ds DirectionSets) error {
	if op == "" {
		op = types.DefaultOperation
	}
	if !op.Valid() {
		return fmt.Errorf("permeability: pathway %q: unknown operation %q", pathway, op)
	}
	// chat is unrestricted; only the denylist checked by Parse applies.
	if op == types.OperationChat {
		return nil
	}
	v, ok := operationVocab[op]
	if !ok {
		// Every known non-chat operation has a vocabulary, so this is a
		// programming error rather than bad operator input.
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
