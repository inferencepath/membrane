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

// ScrubResponse applies the inbound Permeability to a provider response on the
// inward path. The provider-neutral fields (message, finish_reason, usage) always
// pass; the provider identity (provider/model) is included ONLY when the Caller
// set the Reveal flag AND the corresponding field is in the Pathway's inbound
// on_reveal set. Otherwise it is cleared, so the response is provider-neutral.
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

// revealed reports whether a given on_reveal disclosure field is permitted for a
// pathway when the Caller set the Reveal flag.
func revealed(onReveal []string, field string) bool {
	for _, f := range onReveal {
		if f == field {
			return true
		}
	}
	return false
}

// ScrubRerank applies the inbound Permeability to a rerank result on the inward
// path. The provider-neutral fields (results,
// usage) always pass; the provider identity (provider/model) is included ONLY
// when the Caller set the Reveal flag AND the corresponding field is in the
// Pathway's inbound on_reveal set.
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

// ScrubModerations applies the inbound Permeability to a moderations result on
// the inward path. The provider-neutral results
// always pass; the provider identity (provider/model) is included ONLY when the
// Caller set the Reveal flag AND the corresponding field is in the Pathway's
// inbound on_reveal set.
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

// ScrubTranscription applies the inbound Permeability to a transcription result on
// the inward path. The provider-neutral fields
// (text, segments, usage) always pass; the provider identity (provider/model) is
// included ONLY when the Caller set the Reveal flag AND the corresponding field is
// in the Pathway's inbound on_reveal set.
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

// RevealAllows reports whether a given on_reveal disclosure field (e.g. "provider"
// or "model") is permitted for a pathway. It lets handlers whose response is not a
// JSON body (e.g. audio_speech, which surfaces provider/model via headers) apply
// the same inbound on_reveal gating as the struct-based Scrub* helpers.
func RevealAllows(spec permeability.Spec, pathway types.PathwayID, field string) bool {
	ds, ok := spec.Pathways[pathway]
	if !ok {
		return false
	}
	return revealed(ds.Inbound.OnReveal, field)
}

// ScrubImage applies the inbound Permeability to an image-generation result on
// the inward path. The provider-neutral fields
// (data, usage) always pass; the provider identity (provider/model) is included
// ONLY when the Caller set the Reveal flag AND the corresponding field is in the
// Pathway's inbound on_reveal set.
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

// ScrubResponses applies the inbound Permeability to a responses result on the
// inward (non-streaming) path. The provider-neutral
// fields (output, finish_reason, usage) always pass; the provider identity
// (provider/model) is included ONLY when the Caller set the Reveal flag AND the
// corresponding field is in the Pathway's inbound on_reveal set.
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

// ScrubEmbeddings applies the inbound Permeability to an embeddings result on the
// inward path. The provider-neutral fields (data,
// usage) always pass; the provider identity (provider/model) is included ONLY
// when the Caller set the Reveal flag AND the corresponding field is in the
// Pathway's inbound on_reveal set. Otherwise it is cleared, so the response is
// provider-neutral.
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
