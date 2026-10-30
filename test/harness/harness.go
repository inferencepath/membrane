// Package harness builds a fully-wired chat Handler against a given Provider
// endpoint, for integration and Diffusion-leak tests. It mirrors how
// cmd/membrane wires the slice, but in-process and with test fixtures.
package harness

import (
	"fmt"
	"io"
	"log/slog"
	"net/http"

	"github.com/inferencepath/membrane/internal/exterior/keyring"
	"github.com/inferencepath/membrane/internal/exterior/providers/openai"
	"github.com/inferencepath/membrane/internal/interior/admit"
	"github.com/inferencepath/membrane/internal/interior/resolve"
	"github.com/inferencepath/membrane/internal/membrane/permeability"
	"github.com/inferencepath/membrane/internal/membrane/transporter"
	"github.com/inferencepath/membrane/internal/observability/metrics"
	"github.com/inferencepath/membrane/internal/server/chat"
	"github.com/inferencepath/membrane/internal/server/embeddings"
	"github.com/inferencepath/membrane/internal/server/images"
	"github.com/inferencepath/membrane/internal/server/moderations"
	"github.com/inferencepath/membrane/internal/server/rerank"
	"github.com/inferencepath/membrane/internal/server/responses"
	"github.com/inferencepath/membrane/internal/server/speech"
	"github.com/inferencepath/membrane/internal/server/transcription"
	"github.com/inferencepath/membrane/internal/types"
)

// Test constants shared across tests.
const (
	// CallerKey is a valid Caller Key ("<key_id>.<secret>").
	CallerKey = "ck_demo.s3cr3t-demo-key"
	// EgressEnv is the env var the Keyring resolves the Egress Identity from.
	EgressEnv = "INFERENCEPATH_TEST_EGRESS"
	// EgressToken is the Egress Identity value tests set EgressEnv to.
	EgressToken = "egress-secret-token-xyz"
	// ProviderID is the bound Provider family id.
	ProviderID = types.ProviderID("openai")
	// Model is the bound logical model.
	Model = "test-model"
	// Pathway is the bound Pathway.
	Pathway = "chat"
)

// callersYAML stores only the hash of the CallerKey secret ("s3cr3t-demo-key").
const callersYAML = `callers:
  - key_id: ck_demo
    caller_id: acme-app
    name: Acme Demo App
    hash: 458d6534a760ed4c8c2f1c8f86a517572cb56fa6a0f8554efdda2cb8532a4816
`

const chatPermeability = `version: 1
pathways:
  chat:
    outbound:
      allow: [messages, temperature, max_tokens, top_p, stop, stream]
    inbound:
      default: [message, finish_reason, usage]
      on_reveal: [provider, model]
`

// chatToolsPermeability is the chat Pathway with tool calling opted in via the
// outbound allowlist (tools + tool_choice). Used by the tool-calling tests and
// the tool-path Diffusion-leak tests.
const chatToolsPermeability = `version: 1
pathways:
  chat:
    outbound:
      allow: [messages, tools, tool_choice, temperature, max_tokens, top_p, stop, stream]
    inbound:
      default: [message, finish_reason, usage]
      on_reveal: [provider, model]
`

// BuildChat wires a chat Handler whose single Binding targets endpoint. The
// Egress Identity is resolved from EgressEnv, which the caller must set (e.g.
// t.Setenv(harness.EgressEnv, harness.EgressToken)). A nil logger discards output.
func BuildChat(endpoint string, logger *slog.Logger, opts ...chat.Option) (http.Handler, error) {
	return buildChatSpec(endpoint, logger, chatPermeability, metrics.Nop{}, nil, opts...)
}

// BuildChatTools is like BuildChat but over a Pathway whose outbound allowlist
// opts into tool calling (tools + tool_choice).
func BuildChatTools(endpoint string, logger *slog.Logger, opts ...chat.Option) (http.Handler, error) {
	return buildChatSpec(endpoint, logger, chatToolsPermeability, metrics.Nop{}, nil, opts...)
}

// BuildChatPermeability wires a chat Handler over a caller-supplied Permeability
// spec YAML (e.g. to exercise a per-Pathway tool_policy). The pathway id must be
// "chat"; the single Binding targets endpoint. hopts configure the Handler
// (e.g. chat.WithAudit); opts configure the Snapshot.
func BuildChatPermeability(endpoint string, logger *slog.Logger, permYAML string, hopts []chat.HandlerOption, opts ...chat.Option) (http.Handler, error) {
	return buildChatSpec(endpoint, logger, permYAML, metrics.Nop{}, hopts, opts...)
}

// BuildChatHandler is like BuildChat but also accepts process-level Handler
// options (e.g. an audit Sink via chat.WithAudit). opts configure the fixed
// Snapshot (e.g. chat.WithAuthorizer); hopts configure the Handler.
func BuildChatHandler(endpoint string, logger *slog.Logger, hopts []chat.HandlerOption, opts ...chat.Option) (http.Handler, error) {
	return BuildChatHandlerRec(endpoint, logger, metrics.Nop{}, hopts, opts...)
}

// BuildChatHandlerRec is like BuildChatHandler but records to the supplied
// metrics.Recorder (which may also implement metrics.AuthzObserver), enabling
// observability assertions in tests.
func BuildChatHandlerRec(endpoint string, logger *slog.Logger, rec metrics.Recorder, hopts []chat.HandlerOption, opts ...chat.Option) (http.Handler, error) {
	return buildChatSpec(endpoint, logger, chatPermeability, rec, hopts, opts...)
}

// buildChatSpec is the shared chat-Handler builder, parametrized by the
// Permeability spec YAML so tool-enabled Pathways can be wired without altering
// the default chat spec.
func buildChatSpec(endpoint string, logger *slog.Logger, permYAML string, rec metrics.Recorder, hopts []chat.HandlerOption, opts ...chat.Option) (http.Handler, error) {
	if logger == nil {
		logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	spec, err := permeability.Parse([]byte(permYAML))
	if err != nil {
		return nil, fmt.Errorf("harness: permeability: %w", err)
	}
	store, err := admit.ParseStore([]byte(callersYAML))
	if err != nil {
		return nil, fmt.Errorf("harness: callers: %w", err)
	}
	kr, err := keyring.New([]keyring.Entry{{Name: string(ProviderID), SecretRef: "env:" + EgressEnv}})
	if err != nil {
		return nil, fmt.Errorf("harness: keyring: %w", err)
	}
	adapter := openai.New(ProviderID, endpoint, 0)
	trans := transporter.New(map[types.PathwayID]transporter.PoolSpec{
		Pathway: {Targets: []transporter.TargetSpec{{Adapter: adapter, KeyringRef: string(ProviderID), Model: Model}}},
	}, kr)
	resolver := resolve.New(map[types.PathwayID]resolve.Binding{
		Pathway: {Provider: ProviderID, Model: Model},
	})
	snap := &chat.Snapshot{Permeability: spec, Callers: store, Resolver: resolver, Transport: trans}
	for _, o := range opts {
		o(snap)
	}
	if rec == nil {
		rec = metrics.Nop{}
	}
	return chat.NewReloadable(func() *chat.Snapshot { return snap }, logger, rec, hopts...), nil
}

// EmbeddingsPathway is the bound embeddings Pathway name used by BuildEmbeddings.
const EmbeddingsPathway = "embed"

const embeddingsPermeability = `version: 1
pathways:
  embed:
    outbound:
      allow: [input, encoding_format, dimensions]
    inbound:
      default: [data, usage]
      on_reveal: [provider, model]
`

// BuildEmbeddings wires an embeddings Handler whose single target is endpoint,
// over a Pathway configured for the embeddings operation. The Egress Identity is
// resolved from EgressEnv, which the caller must set. A nil logger discards output.
func BuildEmbeddings(endpoint string, logger *slog.Logger, hopts ...embeddings.HandlerOption) (http.Handler, error) {
	return BuildEmbeddingsRec(endpoint, logger, metrics.Nop{}, hopts...)
}

// BuildEmbeddingsRec is like BuildEmbeddings but records to the supplied Recorder.
func BuildEmbeddingsRec(endpoint string, logger *slog.Logger, rec metrics.Recorder, hopts ...embeddings.HandlerOption) (http.Handler, error) {
	if logger == nil {
		logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	spec, err := permeability.Parse([]byte(embeddingsPermeability))
	if err != nil {
		return nil, fmt.Errorf("harness: permeability: %w", err)
	}
	store, err := admit.ParseStore([]byte(callersYAML))
	if err != nil {
		return nil, fmt.Errorf("harness: callers: %w", err)
	}
	kr, err := keyring.New([]keyring.Entry{{Name: string(ProviderID), SecretRef: "env:" + EgressEnv}})
	if err != nil {
		return nil, fmt.Errorf("harness: keyring: %w", err)
	}
	adapter := openai.New(ProviderID, endpoint, 0)
	pathwayID := types.PathwayID(EmbeddingsPathway)
	trans := transporter.New(map[types.PathwayID]transporter.PoolSpec{
		pathwayID: {Targets: []transporter.TargetSpec{{Adapter: adapter, ProviderName: string(ProviderID), KeyringRef: string(ProviderID), Model: Model}}},
	}, kr)
	resolver := resolve.New(map[types.PathwayID]resolve.Binding{
		pathwayID: {Provider: ProviderID, Model: Model},
	})
	resolver.SetPolicy(map[types.PathwayID]resolve.PathwayPolicy{
		pathwayID: {
			Operation:       types.OperationEmbeddings,
			DefaultProvider: string(ProviderID),
			Providers:       map[string]resolve.ProviderPolicy{string(ProviderID): {Models: []string{Model}, DefaultModel: Model}},
		},
	})
	snap := &chat.Snapshot{Permeability: spec, Callers: store, Resolver: resolver, Transport: trans}
	if rec == nil {
		rec = metrics.Nop{}
	}
	return embeddings.NewReloadable(func() *chat.Snapshot { return snap }, logger, rec, hopts...), nil
}

// RerankPathway is the bound rerank Pathway name used by BuildRerank.
const RerankPathway = "rank"

const rerankPermeability = `version: 1
pathways:
  rank:
    outbound:
      allow: [query, documents, top_n, return_documents]
    inbound:
      default: [results, usage]
      on_reveal: [provider, model]
`

// BuildRerank wires a rerank Handler whose single target is endpoint, over a
// Pathway configured for the rerank operation. The Egress Identity is resolved
// from EgressEnv, which the caller must set. A nil logger discards output.
func BuildRerank(endpoint string, logger *slog.Logger, hopts ...rerank.HandlerOption) (http.Handler, error) {
	return BuildRerankRec(endpoint, logger, metrics.Nop{}, hopts...)
}

// BuildRerankRec is like BuildRerank but records to the supplied Recorder.
func BuildRerankRec(endpoint string, logger *slog.Logger, rec metrics.Recorder, hopts ...rerank.HandlerOption) (http.Handler, error) {
	if logger == nil {
		logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	spec, err := permeability.Parse([]byte(rerankPermeability))
	if err != nil {
		return nil, fmt.Errorf("harness: permeability: %w", err)
	}
	store, err := admit.ParseStore([]byte(callersYAML))
	if err != nil {
		return nil, fmt.Errorf("harness: callers: %w", err)
	}
	kr, err := keyring.New([]keyring.Entry{{Name: string(ProviderID), SecretRef: "env:" + EgressEnv}})
	if err != nil {
		return nil, fmt.Errorf("harness: keyring: %w", err)
	}
	adapter := openai.New(ProviderID, endpoint, 0)
	pathwayID := types.PathwayID(RerankPathway)
	trans := transporter.New(map[types.PathwayID]transporter.PoolSpec{
		pathwayID: {Targets: []transporter.TargetSpec{{Adapter: adapter, ProviderName: string(ProviderID), KeyringRef: string(ProviderID), Model: Model}}},
	}, kr)
	resolver := resolve.New(map[types.PathwayID]resolve.Binding{
		pathwayID: {Provider: ProviderID, Model: Model},
	})
	resolver.SetPolicy(map[types.PathwayID]resolve.PathwayPolicy{
		pathwayID: {
			Operation:       types.OperationRerank,
			DefaultProvider: string(ProviderID),
			Providers:       map[string]resolve.ProviderPolicy{string(ProviderID): {Models: []string{Model}, DefaultModel: Model}},
		},
	})
	snap := &chat.Snapshot{Permeability: spec, Callers: store, Resolver: resolver, Transport: trans}
	if rec == nil {
		rec = metrics.Nop{}
	}
	return rerank.NewReloadable(func() *chat.Snapshot { return snap }, logger, rec, hopts...), nil
}

// ResponsesPathway is the bound responses Pathway name used by BuildResponses.
const ResponsesPathway = "respond"

const responsesPermeability = `version: 1
pathways:
  respond:
    outbound:
      allow: [input, instructions, tools, tool_choice, temperature, max_output_tokens, top_p, stream]
    inbound:
      default: [output, finish_reason, usage]
      on_reveal: [provider, model]
`

// BuildResponses wires a responses Handler whose single target is endpoint, over
// a Pathway configured for the responses operation. The Egress Identity is
// resolved from EgressEnv, which the caller must set. A nil logger discards output.
func BuildResponses(endpoint string, logger *slog.Logger, hopts ...responses.HandlerOption) (http.Handler, error) {
	return BuildResponsesRec(endpoint, logger, metrics.Nop{}, hopts...)
}

// BuildResponsesRec is like BuildResponses but records to the supplied Recorder.
func BuildResponsesRec(endpoint string, logger *slog.Logger, rec metrics.Recorder, hopts ...responses.HandlerOption) (http.Handler, error) {
	if logger == nil {
		logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	spec, err := permeability.Parse([]byte(responsesPermeability))
	if err != nil {
		return nil, fmt.Errorf("harness: permeability: %w", err)
	}
	store, err := admit.ParseStore([]byte(callersYAML))
	if err != nil {
		return nil, fmt.Errorf("harness: callers: %w", err)
	}
	kr, err := keyring.New([]keyring.Entry{{Name: string(ProviderID), SecretRef: "env:" + EgressEnv}})
	if err != nil {
		return nil, fmt.Errorf("harness: keyring: %w", err)
	}
	adapter := openai.New(ProviderID, endpoint, 0)
	pathwayID := types.PathwayID(ResponsesPathway)
	trans := transporter.New(map[types.PathwayID]transporter.PoolSpec{
		pathwayID: {Targets: []transporter.TargetSpec{{Adapter: adapter, ProviderName: string(ProviderID), KeyringRef: string(ProviderID), Model: Model}}},
	}, kr)
	resolver := resolve.New(map[types.PathwayID]resolve.Binding{
		pathwayID: {Provider: ProviderID, Model: Model},
	})
	resolver.SetPolicy(map[types.PathwayID]resolve.PathwayPolicy{
		pathwayID: {
			Operation:       types.OperationResponses,
			DefaultProvider: string(ProviderID),
			Providers:       map[string]resolve.ProviderPolicy{string(ProviderID): {Models: []string{Model}, DefaultModel: Model}},
		},
	})
	snap := &chat.Snapshot{Permeability: spec, Callers: store, Resolver: resolver, Transport: trans}
	if rec == nil {
		rec = metrics.Nop{}
	}
	return responses.NewReloadable(func() *chat.Snapshot { return snap }, logger, rec, hopts...), nil
}

// ImagePathway is the bound image-generation Pathway name used by BuildImages.
const ImagePathway = "paint"

const imagePermeability = `version: 1
pathways:
  paint:
    outbound:
      allow: [prompt, n, size, quality, style, response_format]
    inbound:
      default: [data, usage]
      on_reveal: [provider, model]
`

// BuildImages wires an image-generation Handler whose single target is endpoint,
// over a Pathway configured for the image_generation operation. The Egress
// Identity is resolved from EgressEnv, which the caller must set. A nil logger
// discards output.
func BuildImages(endpoint string, logger *slog.Logger, hopts ...images.HandlerOption) (http.Handler, error) {
	return BuildImagesRec(endpoint, logger, metrics.Nop{}, hopts...)
}

// BuildImagesRec is like BuildImages but records to the supplied Recorder.
func BuildImagesRec(endpoint string, logger *slog.Logger, rec metrics.Recorder, hopts ...images.HandlerOption) (http.Handler, error) {
	if logger == nil {
		logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	spec, err := permeability.Parse([]byte(imagePermeability))
	if err != nil {
		return nil, fmt.Errorf("harness: permeability: %w", err)
	}
	store, err := admit.ParseStore([]byte(callersYAML))
	if err != nil {
		return nil, fmt.Errorf("harness: callers: %w", err)
	}
	kr, err := keyring.New([]keyring.Entry{{Name: string(ProviderID), SecretRef: "env:" + EgressEnv}})
	if err != nil {
		return nil, fmt.Errorf("harness: keyring: %w", err)
	}
	adapter := openai.New(ProviderID, endpoint, 0)
	pathwayID := types.PathwayID(ImagePathway)
	trans := transporter.New(map[types.PathwayID]transporter.PoolSpec{
		pathwayID: {Targets: []transporter.TargetSpec{{Adapter: adapter, ProviderName: string(ProviderID), KeyringRef: string(ProviderID), Model: Model}}},
	}, kr)
	resolver := resolve.New(map[types.PathwayID]resolve.Binding{
		pathwayID: {Provider: ProviderID, Model: Model},
	})
	resolver.SetPolicy(map[types.PathwayID]resolve.PathwayPolicy{
		pathwayID: {
			Operation:       types.OperationImageGeneration,
			DefaultProvider: string(ProviderID),
			Providers:       map[string]resolve.ProviderPolicy{string(ProviderID): {Models: []string{Model}, DefaultModel: Model}},
		},
	})
	snap := &chat.Snapshot{Permeability: spec, Callers: store, Resolver: resolver, Transport: trans}
	if rec == nil {
		rec = metrics.Nop{}
	}
	return images.NewReloadable(func() *chat.Snapshot { return snap }, logger, rec, hopts...), nil
}

// opSnapshot wires a single-target chat.Snapshot for an operation Pathway from
// the given permeability YAML, pathway name, and operation. It is the shared
// scaffold behind the Tier-3 harness builders.
func opSnapshot(endpoint, permYAML, pathway string, op types.Operation) (*chat.Snapshot, error) {
	spec, err := permeability.Parse([]byte(permYAML))
	if err != nil {
		return nil, fmt.Errorf("harness: permeability: %w", err)
	}
	store, err := admit.ParseStore([]byte(callersYAML))
	if err != nil {
		return nil, fmt.Errorf("harness: callers: %w", err)
	}
	kr, err := keyring.New([]keyring.Entry{{Name: string(ProviderID), SecretRef: "env:" + EgressEnv}})
	if err != nil {
		return nil, fmt.Errorf("harness: keyring: %w", err)
	}
	adapter := openai.New(ProviderID, endpoint, 0)
	pathwayID := types.PathwayID(pathway)
	trans := transporter.New(map[types.PathwayID]transporter.PoolSpec{
		pathwayID: {Targets: []transporter.TargetSpec{{Adapter: adapter, ProviderName: string(ProviderID), KeyringRef: string(ProviderID), Model: Model}}},
	}, kr)
	resolver := resolve.New(map[types.PathwayID]resolve.Binding{
		pathwayID: {Provider: ProviderID, Model: Model},
	})
	resolver.SetPolicy(map[types.PathwayID]resolve.PathwayPolicy{
		pathwayID: {
			Operation:       op,
			DefaultProvider: string(ProviderID),
			Providers:       map[string]resolve.ProviderPolicy{string(ProviderID): {Models: []string{Model}, DefaultModel: Model}},
		},
	})
	return &chat.Snapshot{Permeability: spec, Callers: store, Resolver: resolver, Transport: trans}, nil
}

// ModerationsPathway, SpeechPathway, and TranscriptionPathway are the bound
// Pathway names for the Tier-3 harness builders.
const (
	ModerationsPathway   = "moderate"
	SpeechPathway        = "speak"
	TranscriptionPathway = "listen"
)

const moderationsPermeability = `version: 1
pathways:
  moderate:
    outbound:
      allow: [input]
    inbound:
      default: [results]
      on_reveal: [provider, model]
`

const speechPermeability = `version: 1
pathways:
  speak:
    outbound:
      allow: [input, voice, format, speed]
    inbound:
      default: [audio]
      on_reveal: [provider, model]
`

const transcriptionPermeability = `version: 1
pathways:
  listen:
    outbound:
      allow: [file, language, prompt, response_format, temperature]
    inbound:
      default: [text, segments, usage]
      on_reveal: [provider, model]
`

// BuildModerations wires a moderations Handler for the moderations operation.
func BuildModerations(endpoint string, logger *slog.Logger, hopts ...moderations.HandlerOption) (http.Handler, error) {
	if logger == nil {
		logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	snap, err := opSnapshot(endpoint, moderationsPermeability, ModerationsPathway, types.OperationModerations)
	if err != nil {
		return nil, err
	}
	return moderations.NewReloadable(func() *chat.Snapshot { return snap }, logger, metrics.Nop{}, hopts...), nil
}

// BuildSpeech wires a speech (TTS) Handler for the audio_speech operation.
func BuildSpeech(endpoint string, logger *slog.Logger, hopts ...speech.HandlerOption) (http.Handler, error) {
	if logger == nil {
		logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	snap, err := opSnapshot(endpoint, speechPermeability, SpeechPathway, types.OperationAudioSpeech)
	if err != nil {
		return nil, err
	}
	return speech.NewReloadable(func() *chat.Snapshot { return snap }, logger, metrics.Nop{}, hopts...), nil
}

// BuildTranscription wires a transcription Handler for the audio_transcription
// operation.
func BuildTranscription(endpoint string, logger *slog.Logger, hopts ...transcription.HandlerOption) (http.Handler, error) {
	if logger == nil {
		logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	snap, err := opSnapshot(endpoint, transcriptionPermeability, TranscriptionPathway, types.OperationAudioTranscription)
	if err != nil {
		return nil, err
	}
	return transcription.NewReloadable(func() *chat.Snapshot { return snap }, logger, metrics.Nop{}, hopts...), nil
}

// CallerStoreWithAllow builds a Caller store for the demo CallerKey whose Caller
// (acme-app) is authorized for exactly the given Pathways (deny-by-default
// otherwise). It is used to exercise the authorization gate.
func CallerStoreWithAllow(allow ...string) (*admit.Store, error) {
	y := callersYAML + "    allow:\n"
	for _, a := range allow {
		y += "      - " + a + "\n"
	}
	return admit.ParseStore([]byte(y))
}
