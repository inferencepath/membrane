package transporter

import (
	"context"
	"errors"
	"fmt"

	"github.com/inferencepath/membrane/internal/exterior/providers"
	"github.com/inferencepath/membrane/internal/membrane/scrub"
	"github.com/inferencepath/membrane/internal/types"
)

// MultimodalTransporter crosses a multimodal_chat request.
type MultimodalTransporter interface {
	TransportMultimodal(ctx context.Context, req scrub.SanitizedRequest) (types.CanonicalMultimodal, error)
}

// TransportMultimodal performs a non-streaming multimodal_chat crossing with
// failover, mirroring TransportConversation. A media or reply format a target
// cannot handle moves on to the next target without a health penalty.
func (m *Membrane) TransportMultimodal(ctx context.Context, sr scrub.SanitizedRequest) (types.CanonicalMultimodal, error) {
	capID := sr.Pathway()
	tries := m.attempts(ctx, capID)
	if len(tries) == 0 {
		return types.CanonicalMultimodal{}, ErrNoProvider
	}
	lastErr := error(ErrNoProvider)
	skipped := 0
	for _, at := range tries {
		t := at.t
		if !t.health.available(m.now(), m.backoff) {
			skipped++
			continue
		}
		mp, ok := t.adapter.(providers.MultimodalProvider)
		if !ok {
			return types.CanonicalMultimodal{}, providers.ErrOperationUnsupported
		}
		id, err := m.identity(t)
		if err != nil {
			t.health.recordFailure(m.now(), m.backoff)
			m.onFailover(capID, t)
			lastErr = err
			continue
		}
		cctx := ctx
		var cancel context.CancelFunc
		if t.timeoutTotal > 0 {
			cctx, cancel = context.WithTimeout(ctx, t.timeoutTotal)
		}
		req := providers.Request{Model: at.model, Payload: sr.Payload(), Stream: false}
		resp, err := mp.CompleteMultimodal(cctx, req, id)
		if cancel != nil {
			cancel()
		}
		if err == nil {
			t.health.recordSuccess()
			m.onHealthy(capID, t)
			return resp, nil
		}
		if ctx.Err() != nil {
			return types.CanonicalMultimodal{}, ctx.Err()
		}
		if errors.Is(err, providers.ErrFormatUnsupported) {
			lastErr = err
			continue
		}
		switch classify(err) {
		case outcomeNoFailover:
			return types.CanonicalMultimodal{}, err
		default:
			t.health.recordFailure(m.now(), m.backoff)
			m.onFailover(capID, t)
			lastErr = err
		}
	}
	if skipped > 0 && lastErr == ErrNoProvider {
		return types.CanonicalMultimodal{}, fmt.Errorf("transporter: all targets unavailable (circuit open): %w", ErrNoProvider)
	}
	return types.CanonicalMultimodal{}, lastErr
}

// TransportMultimodal on the Phase 0 stub always returns ErrNotImplemented.
func (Stub) TransportMultimodal(_ context.Context, _ scrub.SanitizedRequest) (types.CanonicalMultimodal, error) {
	return types.CanonicalMultimodal{}, ErrNotImplemented
}
