package factory

import (
	"github.com/inferencepath/membrane/internal/config"
	"github.com/inferencepath/membrane/internal/exterior/providers"
	"github.com/inferencepath/membrane/internal/types"
)

// This file is the single, Exterior-zone declaration of which egress Operations
// each Provider family's adapter supports. The
// assembly layer consults it to reject, fail-closed at config validation, a
// Pathway wired to a provider that cannot perform the Pathway's Operation.
//
// The OpenAI-compatible families (openai/local) are the reference and support
// every modelled operation; other families support the subset they offer
// natively. "model-dependent" support is represented as supported here (the
// provider's admin-controlled model allowlist gates the actual model).

func opSet(ops ...types.Operation) map[types.Operation]struct{} {
	m := make(map[types.Operation]struct{}, len(ops))
	for _, o := range ops {
		m[o] = struct{}{}
	}
	return m
}

// familyOperations maps a Provider family to the set of Operations its adapter
// supports. A family absent from this map supports only chat (fail-safe default).
var familyOperations = map[types.ProviderID]map[types.Operation]struct{}{
	config.ProviderOpenAI: opSet(
		types.OperationChat, types.OperationEmbeddings, types.OperationRerank,
		types.OperationResponses, types.OperationImageGeneration,
		types.OperationAudioTranscription, types.OperationAudioSpeech,
		types.OperationModerations,
	),
	config.ProviderLocal: opSet(
		types.OperationChat, types.OperationEmbeddings, types.OperationRerank,
		types.OperationResponses, types.OperationImageGeneration,
		types.OperationAudioTranscription, types.OperationAudioSpeech,
		types.OperationModerations,
	),
	config.ProviderAnthropic: opSet(
		types.OperationChat, types.OperationResponses,
	),
	config.ProviderAzureOpenAI: opSet(
		types.OperationChat, types.OperationEmbeddings, types.OperationResponses,
		types.OperationImageGeneration, types.OperationAudioTranscription,
		types.OperationAudioSpeech, types.OperationModerations,
	),
	config.ProviderBedrock: opSet(
		types.OperationChat, types.OperationEmbeddings, types.OperationRerank,
		types.OperationImageGeneration,
	),
	// Gemini: chat + embeddings (embedContent).
	config.ProviderGemini: opSet(
		types.OperationChat, types.OperationEmbeddings,
	),
}

// FamilySupports reports whether a Provider family's adapter can perform the
// given Operation. An empty operation is treated as the default (chat). An
// unknown family supports only chat.
//
// This is the DECLARED (family-level) capability matrix — a fast pre-check and
// the source of truth for documentation/UI. The AUTHORITATIVE runtime gate is
// AdapterSupports, which verifies a concretely-built adapter actually implements
// the operation's capability interface; assembly enforces both so a Pathway can
// never be wired to an adapter that cannot perform its operation.
func FamilySupports(family types.ProviderID, op types.Operation) bool {
	if op == "" {
		op = types.DefaultOperation
	}
	ops, ok := familyOperations[family]
	if !ok {
		return op == types.OperationChat
	}
	_, ok = ops[op]
	return ok
}

// AdapterSupports reports whether a concretely-built Provider adapter actually
// implements the capability required to perform the given Operation. Chat is the
// core capability (every Provider implements Complete/Stream); each new operation
// is an OPTIONAL interface the adapter must satisfy by type assertion. This is the
// structural, fail-closed gate consulted at config assembly — independent of the
// declared FamilySupports matrix — so a declared-but-unimplemented operation is
// rejected at validation time rather than failing a live request.
func AdapterSupports(adapter providers.Provider, op types.Operation) bool {
	if op == "" {
		op = types.DefaultOperation
	}
	switch op {
	case types.OperationChat:
		return true
	case types.OperationEmbeddings:
		_, ok := adapter.(providers.EmbeddingsProvider)
		return ok
	case types.OperationRerank:
		_, ok := adapter.(providers.RerankProvider)
		return ok
	case types.OperationResponses:
		_, ok := adapter.(providers.ResponsesProvider)
		return ok
	case types.OperationImageGeneration:
		_, ok := adapter.(providers.ImageProvider)
		return ok
	case types.OperationAudioTranscription:
		_, ok := adapter.(providers.TranscriptionProvider)
		return ok
	case types.OperationAudioSpeech:
		_, ok := adapter.(providers.SpeechProvider)
		return ok
	case types.OperationModerations:
		_, ok := adapter.(providers.ModerationsProvider)
		return ok
	default:
		return false
	}
}
