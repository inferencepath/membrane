package transporter

import (
	"context"
	"fmt"

	"github.com/inferencepath/membrane/internal/exterior/providers"
	"github.com/inferencepath/membrane/internal/membrane/scrub"
	"github.com/inferencepath/membrane/internal/types"
)

// RerankTransporter is the rerank-operation crossing. Like EmbeddingsTransporter
// it is a SEPARATE capability interface from
// Transporter so that adding an operation never forces chat-only Transporter
// implementations (or test mocks) to change. It accepts ONLY a
// scrub.SanitizedRequest, so a non-sanitized request cannot be transported.
type RerankTransporter interface {
	TransportRerank(ctx context.Context, req scrub.SanitizedRequest) (types.CanonicalRerank, error)
}

// TransportRerank performs a non-streaming rerank crossing with failover across
// the pool, mirroring Transport. It dispatches to each target's adapter via the
// optional providers.RerankProvider capability; a target whose adapter lacks that
// capability yields providers.ErrOperationUnsupported without failover (wiring
// such a target is already rejected fail-closed at config assembly).
func (m *Membrane) TransportRerank(ctx context.Context, sr scrub.SanitizedRequest) (types.CanonicalRerank, error) {
	capID := sr.Pathway()
	tries := m.attempts(ctx, capID)
	if len(tries) == 0 {
		return types.CanonicalRerank{}, ErrNoProvider
	}

	lastErr := error(ErrNoProvider)
	skipped := 0
	for _, at := range tries {
		t := at.t
		if !t.health.available(m.now(), m.backoff) {
			skipped++
			continue
		}

		rp, ok := t.adapter.(providers.RerankProvider)
		if !ok {
			// Adapter does not support rerank: a request-level configuration fault,
			// not a transient target failure. Do not fail over or trip the breaker.
			return types.CanonicalRerank{}, providers.ErrOperationUnsupported
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
		resp, err := rp.Rerank(cctx, req, id)
		if cancel != nil {
			cancel()
		}
		if err == nil {
			t.health.recordSuccess()
			m.onHealthy(capID, t)
			return resp, nil
		}
		if ctx.Err() != nil {
			return types.CanonicalRerank{}, ctx.Err()
		}
		switch classify(err) {
		case outcomeNoFailover:
			return types.CanonicalRerank{}, err
		default:
			t.health.recordFailure(m.now(), m.backoff)
			m.onFailover(capID, t)
			lastErr = err
		}
	}
	if skipped > 0 && lastErr == ErrNoProvider {
		return types.CanonicalRerank{}, fmt.Errorf("transporter: all targets unavailable (circuit open): %w", ErrNoProvider)
	}
	return types.CanonicalRerank{}, lastErr
}

// TransportRerank on the stub always returns ErrNotImplemented.
func (Stub) TransportRerank(_ context.Context, _ scrub.SanitizedRequest) (types.CanonicalRerank, error) {
	return types.CanonicalRerank{}, ErrNotImplemented
}
