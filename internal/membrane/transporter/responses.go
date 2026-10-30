package transporter

import (
	"context"
	"fmt"

	"github.com/inferencepath/membrane/internal/exterior/providers"
	"github.com/inferencepath/membrane/internal/membrane/scrub"
	"github.com/inferencepath/membrane/internal/types"
)

// ResponsesTransporter crosses a responses request. It is separate from
// Transporter so that adding an operation does not affect chat-only
// implementations or test mocks. Both methods accept only a
// scrub.SanitizedRequest.
type ResponsesTransporter interface {
	TransportResponses(ctx context.Context, req scrub.SanitizedRequest) (types.CanonicalResponses, error)
	TransportResponsesStream(ctx context.Context, req scrub.SanitizedRequest, emit func(types.ResponsesStreamChunk) error) error
}

// TransportResponses performs a non-streaming responses crossing with failover
// across the pool, mirroring Transport. It dispatches via the optional
// providers.ResponsesProvider capability; a target whose adapter lacks it yields
// providers.ErrOperationUnsupported without failover.
func (m *Membrane) TransportResponses(ctx context.Context, sr scrub.SanitizedRequest) (types.CanonicalResponses, error) {
	capID := sr.Pathway()
	tries := m.attempts(ctx, capID)
	if len(tries) == 0 {
		return types.CanonicalResponses{}, ErrNoProvider
	}

	lastErr := error(ErrNoProvider)
	skipped := 0
	for _, at := range tries {
		t := at.t
		if !t.health.available(m.now(), m.backoff) {
			skipped++
			continue
		}

		rp, ok := t.adapter.(providers.ResponsesProvider)
		if !ok {
			return types.CanonicalResponses{}, providers.ErrOperationUnsupported
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
		resp, err := rp.Responses(cctx, req, id)
		if cancel != nil {
			cancel()
		}
		if err == nil {
			t.health.recordSuccess()
			m.onHealthy(capID, t)
			return resp, nil
		}
		if ctx.Err() != nil {
			return types.CanonicalResponses{}, ctx.Err()
		}
		switch classify(err) {
		case outcomeNoFailover:
			return types.CanonicalResponses{}, err
		default:
			t.health.recordFailure(m.now(), m.backoff)
			m.onFailover(capID, t)
			lastErr = err
		}
	}
	if skipped > 0 && lastErr == ErrNoProvider {
		return types.CanonicalResponses{}, fmt.Errorf("transporter: all targets unavailable (circuit open): %w", ErrNoProvider)
	}
	return types.CanonicalResponses{}, lastErr
}

// TransportResponsesStream performs a streaming responses crossing. Like
// TransportStream, it fails over across the pool only before the first event is
// relayed; after that, a mid-stream failure ends the stream rather than
// switching providers.
func (m *Membrane) TransportResponsesStream(ctx context.Context, sr scrub.SanitizedRequest, emit func(types.ResponsesStreamChunk) error) error {
	capID := sr.Pathway()
	tries := m.attempts(ctx, capID)
	if len(tries) == 0 {
		return ErrNoProvider
	}

	lastErr := error(ErrNoProvider)
	for _, at := range tries {
		t := at.t
		if !t.health.available(m.now(), m.backoff) {
			continue
		}

		rp, ok := t.adapter.(providers.ResponsesProvider)
		if !ok {
			return providers.ErrOperationUnsupported
		}

		id, err := m.identity(t)
		if err != nil {
			t.health.recordFailure(m.now(), m.backoff)
			m.onFailover(capID, t)
			lastErr = err
			continue
		}

		wroteAny := false
		guard := func(c types.ResponsesStreamChunk) error {
			if !c.Done {
				wroteAny = true
			}
			return emit(c)
		}
		req := providers.Request{Model: at.model, Payload: sr.Payload(), Stream: true}
		err = rp.ResponsesStream(ctx, req, id, guard)
		if err == nil {
			t.health.recordSuccess()
			m.onHealthy(capID, t)
			return nil
		}
		if wroteAny || ctx.Err() != nil {
			return err
		}
		if classify(err) == outcomeNoFailover {
			return err
		}
		t.health.recordFailure(m.now(), m.backoff)
		m.onFailover(capID, t)
		lastErr = err
	}
	return lastErr
}

// TransportResponses on the stub always returns ErrNotImplemented.
func (Stub) TransportResponses(_ context.Context, _ scrub.SanitizedRequest) (types.CanonicalResponses, error) {
	return types.CanonicalResponses{}, ErrNotImplemented
}

// TransportResponsesStream on the stub always returns ErrNotImplemented.
func (Stub) TransportResponsesStream(_ context.Context, _ scrub.SanitizedRequest, _ func(types.ResponsesStreamChunk) error) error {
	return fmt.Errorf("stream: %w", ErrNotImplemented)
}
