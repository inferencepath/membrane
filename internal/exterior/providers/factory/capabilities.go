package factory

import (
	"github.com/inferencepath/membrane/internal/config"
	"github.com/inferencepath/membrane/internal/exterior/providers"
	"github.com/inferencepath/membrane/internal/types"
)

// The tables below declare which egress Operations each Provider family's
// adapter supports. Config assembly uses them to reject a Pathway whose Provider
// cannot perform the Pathway's Operation.
//
// The OpenAI-compatible families (openai, local) support every modelled
// operation; other families support the subset they offer natively. Support
// that depends on the model is listed as supported, since the Provider's model
// allowlist decides which models can be used.

func opSet(ops ...types.Operation) map[types.Operation]struct{} {
	m := make(map[types.Operation]struct{}, len(ops))
	for _, o := range ops {
		m[o] = struct{}{}
	}
	return m
}

// familyOperations maps a Provider family to the set of Operations its adapter
// supports. A family absent from this map supports only chat.
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
	// Gemini embeddings use embedContent.
	config.ProviderGemini: opSet(
		types.OperationChat, types.OperationEmbeddings,
	),
}

// FamilySupports reports whether a Provider family's adapter can perform the
// given Operation. An empty operation is treated as the default (chat). An
// unknown family supports only chat.
//
// This is the declared, family-level matrix. AdapterSupports checks what a
// built adapter implements; assembly enforces both.
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

// AdapterSupports reports whether a built Provider adapter implements the
// capability interface for the given Operation. Every Provider supports chat;
// other operations are optional interfaces. Config assembly checks this so an
// operation that is declared but not implemented fails validation instead of a
// live request.
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
