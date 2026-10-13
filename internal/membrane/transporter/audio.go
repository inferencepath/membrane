package transporter

import (
	"context"
	"fmt"

	"github.com/inferencepath/membrane/internal/exterior/providers"
	"github.com/inferencepath/membrane/internal/membrane/scrub"
	"github.com/inferencepath/membrane/internal/types"
)

// ModerationsTransporter, TranscriptionTransporter, and SpeechTransporter are
// operation crossings. Each is a SEPARATE
// capability interface from Transporter so adding an operation never forces
// chat-only implementations (or test mocks) to change. All accept ONLY a
// scrub.SanitizedRequest.
type ModerationsTransporter interface {
	TransportModerations(ctx context.Context, req scrub.SanitizedRequest) (types.CanonicalModerations, error)
}

// TranscriptionTransporter crosses an audio_transcription request.
type TranscriptionTransporter interface {
	TransportTranscription(ctx context.Context, req scrub.SanitizedRequest) (types.CanonicalTranscription, error)
}

// SpeechTransporter crosses an audio_speech request, relaying binary audio
// incrementally via emit (first chunk is the Head).
type SpeechTransporter interface {
	TransportSpeech(ctx context.Context, req scrub.SanitizedRequest, emit func(types.SpeechChunk) error) error
}

// TransportModerations performs a non-streaming moderations crossing with
// failover, mirroring Transport. Dispatch is via the optional
// providers.ModerationsProvider capability.
func (m *Membrane) TransportModerations(ctx context.Context, sr scrub.SanitizedRequest) (types.CanonicalModerations, error) {
	capID := sr.Pathway()
	tries := m.attempts(ctx, capID)
	if len(tries) == 0 {
		return types.CanonicalModerations{}, ErrNoProvider
	}
	lastErr := error(ErrNoProvider)
	skipped := 0
	for _, at := range tries {
		t := at.t
		if !t.health.available(m.now(), m.backoff) {
			skipped++
			continue
		}
		mp, ok := t.adapter.(providers.ModerationsProvider)
		if !ok {
			return types.CanonicalModerations{}, providers.ErrOperationUnsupported
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
		resp, err := mp.Moderate(cctx, req, id)
		if cancel != nil {
			cancel()
		}
		if err == nil {
			t.health.recordSuccess()
			m.onHealthy(capID, t)
			return resp, nil
		}
		if ctx.Err() != nil {
			return types.CanonicalModerations{}, ctx.Err()
		}
		switch classify(err) {
		case outcomeNoFailover:
			return types.CanonicalModerations{}, err
		default:
			t.health.recordFailure(m.now(), m.backoff)
			m.onFailover(capID, t)
			lastErr = err
		}
	}
	if skipped > 0 && lastErr == ErrNoProvider {
		return types.CanonicalModerations{}, fmt.Errorf("transporter: all targets unavailable (circuit open): %w", ErrNoProvider)
	}
	return types.CanonicalModerations{}, lastErr
}

// TransportTranscription performs a non-streaming transcription crossing with
// failover, mirroring Transport. Dispatch is via the optional
// providers.TranscriptionProvider capability.
func (m *Membrane) TransportTranscription(ctx context.Context, sr scrub.SanitizedRequest) (types.CanonicalTranscription, error) {
	capID := sr.Pathway()
	tries := m.attempts(ctx, capID)
	if len(tries) == 0 {
		return types.CanonicalTranscription{}, ErrNoProvider
	}
	lastErr := error(ErrNoProvider)
	skipped := 0
	for _, at := range tries {
		t := at.t
		if !t.health.available(m.now(), m.backoff) {
			skipped++
			continue
		}
		tp, ok := t.adapter.(providers.TranscriptionProvider)
		if !ok {
			return types.CanonicalTranscription{}, providers.ErrOperationUnsupported
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
		resp, err := tp.Transcribe(cctx, req, id)
		if cancel != nil {
			cancel()
		}
		if err == nil {
			t.health.recordSuccess()
			m.onHealthy(capID, t)
			return resp, nil
		}
		if ctx.Err() != nil {
			return types.CanonicalTranscription{}, ctx.Err()
		}
		switch classify(err) {
		case outcomeNoFailover:
			return types.CanonicalTranscription{}, err
		default:
			t.health.recordFailure(m.now(), m.backoff)
			m.onFailover(capID, t)
			lastErr = err
		}
	}
	if skipped > 0 && lastErr == ErrNoProvider {
		return types.CanonicalTranscription{}, fmt.Errorf("transporter: all targets unavailable (circuit open): %w", ErrNoProvider)
	}
	return types.CanonicalTranscription{}, lastErr
}

// TransportSpeech performs a streaming speech crossing. Mirroring TransportStream,
// it fails over across the pool ONLY before the first chunk is relayed; once the
// Head (or any audio) has been emitted, a mid-stream failure terminates cleanly.
func (m *Membrane) TransportSpeech(ctx context.Context, sr scrub.SanitizedRequest, emit func(types.SpeechChunk) error) error {
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
		sp, ok := t.adapter.(providers.SpeechProvider)
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
		guard := func(c types.SpeechChunk) error {
			if !c.Done {
				wroteAny = true
			}
			return emit(c)
		}
		req := providers.Request{Model: at.model, Payload: sr.Payload(), Stream: true}
		err = sp.Speech(ctx, req, id, guard)
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

// --- Stub implementations ---

// TransportModerations on the stub always returns ErrNotImplemented.
func (Stub) TransportModerations(_ context.Context, _ scrub.SanitizedRequest) (types.CanonicalModerations, error) {
	return types.CanonicalModerations{}, ErrNotImplemented
}

// TransportTranscription on the stub always returns ErrNotImplemented.
func (Stub) TransportTranscription(_ context.Context, _ scrub.SanitizedRequest) (types.CanonicalTranscription, error) {
	return types.CanonicalTranscription{}, ErrNotImplemented
}

// TransportSpeech on the stub always returns ErrNotImplemented.
func (Stub) TransportSpeech(_ context.Context, _ scrub.SanitizedRequest, _ func(types.SpeechChunk) error) error {
	return fmt.Errorf("stream: %w", ErrNotImplemented)
}
