package transporter

import (
	"context"
	"fmt"

	"github.com/inferencepath/membrane/internal/exterior/providers"
	"github.com/inferencepath/membrane/internal/membrane/scrub"
	"github.com/inferencepath/membrane/internal/types"
)

// EmbeddingsTransporter is the embeddings-operation crossing. It is a SEPARATE
// capability interface from Transporter so that adding
// an operation never forces chat-only Transporter implementations (or test mocks)
// to change. Like Transporter it accepts ONLY a scrub.SanitizedRequest, so a
// non-sanitized request cannot be transported.
type EmbeddingsTransporter interface {
	TransportEmbeddings(ctx context.Context, req scrub.SanitizedRequest) (types.CanonicalEmbeddings, error)
}

// TransportEmbeddings performs a non-streaming embeddings crossing with failover
// across the pool, mirroring Transport. It dispatches to each target's adapter
// via the optional providers.EmbeddingsProvider capability; a target whose
// adapter lacks that capability yields providers.ErrOperationUnsupported without
// failover (wiring such a target is already rejected fail-closed at config
// assembly, so this is a defensive runtime guard).
func (m *Membrane) TransportEmbeddings(ctx context.Context, sr scrub.SanitizedRequest) (types.CanonicalEmbeddings, error) {
	capID := sr.Pathway()
	tries := m.attempts(ctx, capID)
	if len(tries) == 0 {
		return types.CanonicalEmbeddings{}, ErrNoProvider
	}

	lastErr := error(ErrNoProvider)
	skipped := 0
	for _, at := range tries {
		t := at.t
		if !t.health.available(m.now(), m.backoff) {
			skipped++
			continue
		}

		ep, ok := t.adapter.(providers.EmbeddingsProvider)
		if !ok {
			// Adapter does not support embeddings. This is a request-level
			// configuration fault, not a transient target failure: do not fail
			// over or trip the breaker.
			return types.CanonicalEmbeddings{}, providers.ErrOperationUnsupported
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
		resp, err := ep.Embeddings(cctx, req, id)
		if cancel != nil {
			cancel()
		}
		if err == nil {
			t.health.recordSuccess()
			m.onHealthy(capID, t)
			return resp, nil
		}
		if ctx.Err() != nil {
			return types.CanonicalEmbeddings{}, ctx.Err()
		}
		switch classify(err) {
		case outcomeNoFailover:
			return types.CanonicalEmbeddings{}, err
		default:
			t.health.recordFailure(m.now(), m.backoff)
			m.onFailover(capID, t)
			lastErr = err
		}
	}
	if skipped > 0 && lastErr == ErrNoProvider {
		return types.CanonicalEmbeddings{}, fmt.Errorf("transporter: all targets unavailable (circuit open): %w", ErrNoProvider)
	}
	return types.CanonicalEmbeddings{}, lastErr
}

// TransportEmbeddings on the stub always returns ErrNotImplemented.
func (Stub) TransportEmbeddings(_ context.Context, _ scrub.SanitizedRequest) (types.CanonicalEmbeddings, error) {
	return types.CanonicalEmbeddings{}, ErrNotImplemented
}
