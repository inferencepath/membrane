package transporter

import (
	"context"
	"fmt"

	"github.com/inferencepath/membrane/internal/exterior/providers"
	"github.com/inferencepath/membrane/internal/membrane/scrub"
	"github.com/inferencepath/membrane/internal/types"
)

// ImageTransporter is the image-generation crossing. Like the other operation
// capabilities it is a SEPARATE interface from
// Transporter so adding an operation never forces chat-only implementations (or
// test mocks) to change. It accepts ONLY a scrub.SanitizedRequest.
type ImageTransporter interface {
	TransportImage(ctx context.Context, req scrub.SanitizedRequest) (types.CanonicalImage, error)
}

// TransportImage performs a non-streaming image-generation crossing with failover
// across the pool, mirroring Transport. It dispatches via the optional
// providers.ImageProvider capability; a target whose adapter lacks it yields
// providers.ErrOperationUnsupported without failover.
func (m *Membrane) TransportImage(ctx context.Context, sr scrub.SanitizedRequest) (types.CanonicalImage, error) {
	capID := sr.Pathway()
	tries := m.attempts(ctx, capID)
	if len(tries) == 0 {
		return types.CanonicalImage{}, ErrNoProvider
	}

	lastErr := error(ErrNoProvider)
	skipped := 0
	for _, at := range tries {
		t := at.t
		if !t.health.available(m.now(), m.backoff) {
			skipped++
			continue
		}

		ip, ok := t.adapter.(providers.ImageProvider)
		if !ok {
			return types.CanonicalImage{}, providers.ErrOperationUnsupported
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
		resp, err := ip.Images(cctx, req, id)
		if cancel != nil {
			cancel()
		}
		if err == nil {
			t.health.recordSuccess()
			m.onHealthy(capID, t)
			return resp, nil
		}
		if ctx.Err() != nil {
			return types.CanonicalImage{}, ctx.Err()
		}
		switch classify(err) {
		case outcomeNoFailover:
			return types.CanonicalImage{}, err
		default:
			t.health.recordFailure(m.now(), m.backoff)
			m.onFailover(capID, t)
			lastErr = err
		}
	}
	if skipped > 0 && lastErr == ErrNoProvider {
		return types.CanonicalImage{}, fmt.Errorf("transporter: all targets unavailable (circuit open): %w", ErrNoProvider)
	}
	return types.CanonicalImage{}, lastErr
}

// TransportImage on the stub always returns ErrNotImplemented.
func (Stub) TransportImage(_ context.Context, _ scrub.SanitizedRequest) (types.CanonicalImage, error) {
	return types.CanonicalImage{}, ErrNotImplemented
}
