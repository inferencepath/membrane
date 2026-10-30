package scrub

import (
	"fmt"

	"github.com/inferencepath/membrane/internal/membrane/permeability"
	"github.com/inferencepath/membrane/internal/types"
)

// Field names gated by the inbound on_reveal set.
const (
	revealFieldProvider = "provider"
	revealFieldModel    = "model"
)

// ScrubResponse applies the inbound Permeability to a chat response. Message,
// finish reason, and usage always pass. Provider and model pass only when
// reveal is set and the field is in the Pathway's on_reveal set.
func ScrubResponse(resp types.CanonicalResponse, spec permeability.Spec, pathway types.PathwayID, reveal bool) (types.CanonicalResponse, error) {
	ds, ok := spec.Pathways[pathway]
	if !ok {
		return types.CanonicalResponse{}, fmt.Errorf("scrub: no permeability for pathway %q", pathway)
	}

	out := types.CanonicalResponse{
		PathwayID:    pathway,
		Message:      resp.Message,
		FinishReason: resp.FinishReason,
		Usage:        resp.Usage,
	}

	if reveal {
		onReveal := make(map[string]struct{}, len(ds.Inbound.OnReveal))
		for _, f := range ds.Inbound.OnReveal {
			onReveal[f] = struct{}{}
		}
		if _, ok := onReveal[revealFieldProvider]; ok {
			out.Provider = resp.Provider
		}
		if _, ok := onReveal[revealFieldModel]; ok {
			out.Model = resp.Model
		}
	}

	return out, nil
}

// revealed reports whether field is in the on_reveal set.
func revealed(onReveal []string, field string) bool {
	for _, f := range onReveal {
		if f == field {
			return true
		}
	}
	return false
}

// ScrubRerank applies the inbound Permeability to a rerank result. Results and
// usage always pass; provider and model are gated as in ScrubResponse.
func ScrubRerank(resp types.CanonicalRerank, spec permeability.Spec, pathway types.PathwayID, reveal bool) (types.CanonicalRerank, error) {
	ds, ok := spec.Pathways[pathway]
	if !ok {
		return types.CanonicalRerank{}, fmt.Errorf("scrub: no permeability for pathway %q", pathway)
	}

	out := types.CanonicalRerank{
		PathwayID: pathway,
		Results:   resp.Results,
		Usage:     resp.Usage,
	}

	if reveal {
		if revealed(ds.Inbound.OnReveal, revealFieldProvider) {
			out.Provider = resp.Provider
		}
		if revealed(ds.Inbound.OnReveal, revealFieldModel) {
			out.Model = resp.Model
		}
	}

	return out, nil
}

// ScrubModerations applies the inbound Permeability to a moderations result.
// Results always pass; provider and model are gated as in ScrubResponse.
func ScrubModerations(resp types.CanonicalModerations, spec permeability.Spec, pathway types.PathwayID, reveal bool) (types.CanonicalModerations, error) {
	ds, ok := spec.Pathways[pathway]
	if !ok {
		return types.CanonicalModerations{}, fmt.Errorf("scrub: no permeability for pathway %q", pathway)
	}
	out := types.CanonicalModerations{PathwayID: pathway, Results: resp.Results}
	if reveal {
		if revealed(ds.Inbound.OnReveal, revealFieldProvider) {
			out.Provider = resp.Provider
		}
		if revealed(ds.Inbound.OnReveal, revealFieldModel) {
			out.Model = resp.Model
		}
	}
	return out, nil
}

// ScrubTranscription applies the inbound Permeability to a transcription
// result. Text, segments, and usage always pass; provider and model are gated
// as in ScrubResponse.
func ScrubTranscription(resp types.CanonicalTranscription, spec permeability.Spec, pathway types.PathwayID, reveal bool) (types.CanonicalTranscription, error) {
	ds, ok := spec.Pathways[pathway]
	if !ok {
		return types.CanonicalTranscription{}, fmt.Errorf("scrub: no permeability for pathway %q", pathway)
	}
	out := types.CanonicalTranscription{
		PathwayID: pathway,
		Text:      resp.Text,
		Segments:  resp.Segments,
		Usage:     resp.Usage,
	}
	if reveal {
		if revealed(ds.Inbound.OnReveal, revealFieldProvider) {
			out.Provider = resp.Provider
		}
		if revealed(ds.Inbound.OnReveal, revealFieldModel) {
			out.Model = resp.Model
		}
	}
	return out, nil
}

// RevealAllows reports whether field ("provider" or "model") is in the
// Pathway's on_reveal set. Handlers that do not return a JSON body, such as
// audio_speech, which sends provider and model as headers, use it to apply the
// same gating as the Scrub functions.
func RevealAllows(spec permeability.Spec, pathway types.PathwayID, field string) bool {
	ds, ok := spec.Pathways[pathway]
	if !ok {
		return false
	}
	return revealed(ds.Inbound.OnReveal, field)
}

// ScrubImage applies the inbound Permeability to an image generation result.
// Data and usage always pass; provider and model are gated as in ScrubResponse.
func ScrubImage(resp types.CanonicalImage, spec permeability.Spec, pathway types.PathwayID, reveal bool) (types.CanonicalImage, error) {
	ds, ok := spec.Pathways[pathway]
	if !ok {
		return types.CanonicalImage{}, fmt.Errorf("scrub: no permeability for pathway %q", pathway)
	}

	out := types.CanonicalImage{
		PathwayID: pathway,
		Data:      resp.Data,
		Usage:     resp.Usage,
	}

	if reveal {
		if revealed(ds.Inbound.OnReveal, revealFieldProvider) {
			out.Provider = resp.Provider
		}
		if revealed(ds.Inbound.OnReveal, revealFieldModel) {
			out.Model = resp.Model
		}
	}

	return out, nil
}

// ScrubResponses applies the inbound Permeability to a non-streaming responses
// result. Output, finish reason, and usage always pass; provider and model are
// gated as in ScrubResponse.
func ScrubResponses(resp types.CanonicalResponses, spec permeability.Spec, pathway types.PathwayID, reveal bool) (types.CanonicalResponses, error) {
	ds, ok := spec.Pathways[pathway]
	if !ok {
		return types.CanonicalResponses{}, fmt.Errorf("scrub: no permeability for pathway %q", pathway)
	}

	out := types.CanonicalResponses{
		PathwayID:    pathway,
		Output:       resp.Output,
		FinishReason: resp.FinishReason,
		Usage:        resp.Usage,
	}

	if reveal {
		if revealed(ds.Inbound.OnReveal, revealFieldProvider) {
			out.Provider = resp.Provider
		}
		if revealed(ds.Inbound.OnReveal, revealFieldModel) {
			out.Model = resp.Model
		}
	}

	return out, nil
}

// ScrubEmbeddings applies the inbound Permeability to an embeddings result.
// Data and usage always pass; provider and model are gated as in ScrubResponse.
func ScrubEmbeddings(resp types.CanonicalEmbeddings, spec permeability.Spec, pathway types.PathwayID, reveal bool) (types.CanonicalEmbeddings, error) {
	ds, ok := spec.Pathways[pathway]
	if !ok {
		return types.CanonicalEmbeddings{}, fmt.Errorf("scrub: no permeability for pathway %q", pathway)
	}

	out := types.CanonicalEmbeddings{
		PathwayID: pathway,
		Data:      resp.Data,
		Usage:     resp.Usage,
	}

	if reveal {
		onReveal := make(map[string]struct{}, len(ds.Inbound.OnReveal))
		for _, f := range ds.Inbound.OnReveal {
			onReveal[f] = struct{}{}
		}
		if _, ok := onReveal[revealFieldProvider]; ok {
			out.Provider = resp.Provider
		}
		if _, ok := onReveal[revealFieldModel]; ok {
			out.Model = resp.Model
		}
	}

	return out, nil
}
