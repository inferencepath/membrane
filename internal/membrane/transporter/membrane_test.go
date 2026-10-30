package transporter_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/inferencepath/membrane/internal/exterior/providers"
	"github.com/inferencepath/membrane/internal/membrane/permeability"
	"github.com/inferencepath/membrane/internal/membrane/scrub"
	"github.com/inferencepath/membrane/internal/membrane/transporter"
	"github.com/inferencepath/membrane/internal/types"
)

type fakeProvider struct {
	id        types.ProviderID
	gotID     providers.EgressIdentity
	gotReq    providers.Request
	calls     int
	resp      types.CanonicalResponse
	chunks    []types.StreamChunk
	returnErr error
	// streamEmitThenErr relays these chunks and then returns returnErr, to test
	// that a mid-stream failure does not fail over.
	streamEmitThenErr []types.StreamChunk
}

func (f *fakeProvider) ID() types.ProviderID { return f.id }

func (f *fakeProvider) Complete(_ context.Context, req providers.Request, id providers.EgressIdentity) (types.CanonicalResponse, error) {
	f.calls++
	f.gotReq, f.gotID = req, id
	return f.resp, f.returnErr
}

func (f *fakeProvider) Stream(_ context.Context, req providers.Request, id providers.EgressIdentity, emit func(types.StreamChunk) error) error {
	f.calls++
	f.gotReq, f.gotID = req, id
	for _, c := range f.streamEmitThenErr {
		if err := emit(c); err != nil {
			return err
		}
	}
	if f.returnErr != nil {
		return f.returnErr
	}
	for _, c := range f.chunks {
		if err := emit(c); err != nil {
			return err
		}
	}
	return nil
}

type fakeKeyring struct {
	byRef map[string]providers.EgressIdentity
	def   providers.EgressIdentity
	err   error
}

func (f fakeKeyring) Resolve(ref string) (providers.EgressIdentity, error) {
	if f.err != nil {
		return providers.EgressIdentity{}, f.err
	}
	if id, ok := f.byRef[ref]; ok {
		return id, nil
	}
	return f.def, nil
}

func sanitize(t *testing.T, stream bool) scrub.SanitizedRequest {
	t.Helper()
	spec, err := permeability.Parse([]byte("version: 1\npathways:\n  chat:\n    outbound:\n      allow: [messages, stream]\n    inbound:\n      default: [message]\n"))
	if err != nil {
		t.Fatal(err)
	}
	sr, err := scrub.Sanitize(types.CanonicalRequest{
		PathwayID: "chat",
		Payload:   map[string]any{"messages": []any{}, "stream": stream},
	}, spec)
	if err != nil {
		t.Fatal(err)
	}
	return sr
}

func pool(strategy transporter.Strategy, targets ...transporter.TargetSpec) map[types.PathwayID]transporter.PoolSpec {
	return map[types.PathwayID]transporter.PoolSpec{"chat": {Strategy: strategy, Targets: targets}}
}

func target(a providers.Provider, ref string, priority int) transporter.TargetSpec {
	return transporter.TargetSpec{Adapter: a, KeyringRef: ref, Model: "m", Priority: priority}
}

type recordingObserver struct {
	failovers []string // "pathway/provider"
	health    []string // "pathway/provider=healthy|unhealthy"
}

func (o *recordingObserver) ObserveFailover(pathway, provider string) {
	o.failovers = append(o.failovers, pathway+"/"+provider)
}

func (o *recordingObserver) ObserveHealth(pathway, provider string, healthy bool) {
	state := "unhealthy"
	if healthy {
		state = "healthy"
	}
	o.health = append(o.health, pathway+"/"+provider+"="+state)
}

func contains(ss []string, want string) bool {
	for _, s := range ss {
		if s == want {
			return true
		}
	}
	return false
}

// TestMembraneObserverFailoverAndHealth checks that failing over from the
// primary marks it unhealthy and the serving secondary healthy.
func TestMembraneObserverFailoverAndHealth(t *testing.T) {
	primary := &fakeProvider{id: "openai", returnErr: providers.UpstreamStatusError{Status: 500}}
	secondary := &fakeProvider{id: "anthropic", resp: types.CanonicalResponse{FinishReason: "stop"}}
	obs := &recordingObserver{}
	m := transporter.New(
		pool(transporter.StrategyPriority, target(primary, "p", 0), target(secondary, "s", 1)),
		fakeKeyring{def: providers.EgressIdentity{Secret: "tok"}},
		transporter.WithObserver(obs),
	)
	if _, err := m.Transport(context.Background(), sanitize(t, false)); err != nil {
		t.Fatalf("Transport: %v", err)
	}
	if !contains(obs.failovers, "chat/openai") {
		t.Fatalf("failovers = %v, want chat/openai", obs.failovers)
	}
	if !contains(obs.health, "chat/openai=unhealthy") {
		t.Fatalf("health = %v, want chat/openai=unhealthy", obs.health)
	}
	if !contains(obs.health, "chat/anthropic=healthy") {
		t.Fatalf("health = %v, want chat/anthropic=healthy", obs.health)
	}
}

// TestMembraneObserverStreamHealth checks health and failover signals on the
// streaming path.
func TestMembraneObserverStreamHealth(t *testing.T) {
	primary := &fakeProvider{id: "openai", returnErr: providers.UpstreamStatusError{Status: 503}}
	secondary := &fakeProvider{id: "bedrock", chunks: []types.StreamChunk{{Delta: "a"}, {Done: true}}}
	obs := &recordingObserver{}
	m := transporter.New(
		pool(transporter.StrategyPriority, target(primary, "p", 0), target(secondary, "s", 1)),
		fakeKeyring{def: providers.EgressIdentity{Secret: "tok"}},
		transporter.WithObserver(obs),
	)
	if err := m.TransportStream(context.Background(), sanitize(t, true), func(types.StreamChunk) error { return nil }); err != nil {
		t.Fatalf("TransportStream: %v", err)
	}
	if !contains(obs.failovers, "chat/openai") {
		t.Fatalf("failovers = %v, want chat/openai", obs.failovers)
	}
	if !contains(obs.health, "chat/bedrock=healthy") {
		t.Fatalf("health = %v, want chat/bedrock=healthy", obs.health)
	}
}

// TestMembraneObserverFailoverOnKeyringError checks that a Keyring failure
// records a failover on both the unary and streaming paths.
func TestMembraneObserverFailoverOnKeyringError(t *testing.T) {
	fp := &fakeProvider{id: "anthropic"}
	obs := &recordingObserver{}
	m := transporter.New(
		pool(transporter.StrategyPriority, target(fp, "p", 0), target(fp, "s", 1)),
		fakeKeyring{err: errorString("keyring: resolve failed")},
		transporter.WithObserver(obs),
	)
	if _, err := m.Transport(context.Background(), sanitize(t, false)); err == nil {
		t.Fatal("expected keyring failure error")
	}
	if err := m.TransportStream(context.Background(), sanitize(t, true), func(types.StreamChunk) error { return nil }); err == nil {
		t.Fatal("expected keyring failure error (stream)")
	}
	if !contains(obs.failovers, "chat/anthropic") {
		t.Fatalf("failovers = %v, want chat/anthropic (keyring failure)", obs.failovers)
	}
}

type errorString string

func (e errorString) Error() string { return string(e) }

func TestMembraneTransport(t *testing.T) {
	fp := &fakeProvider{id: "openai", resp: types.CanonicalResponse{FinishReason: "stop"}}
	m := transporter.New(pool("", target(fp, "openai-main", 0)),
		fakeKeyring{def: providers.EgressIdentity{Secret: "tok"}})
	resp, err := m.Transport(context.Background(), sanitize(t, false))
	if err != nil {
		t.Fatalf("Transport: %v", err)
	}
	if resp.FinishReason != "stop" {
		t.Fatalf("resp = %+v", resp)
	}
	if fp.gotID.Secret != "tok" || fp.gotReq.Model != "m" || fp.gotReq.Stream {
		t.Fatalf("id=%+v req=%+v", fp.gotID, fp.gotReq)
	}
}

func TestMembraneTransportStream(t *testing.T) {
	fp := &fakeProvider{id: "openai", chunks: []types.StreamChunk{{Delta: "a"}, {Done: true}}}
	m := transporter.New(pool("", target(fp, "openai-main", 0)),
		fakeKeyring{def: providers.EgressIdentity{Secret: "tok"}})
	var got []types.StreamChunk
	err := m.TransportStream(context.Background(), sanitize(t, true), func(c types.StreamChunk) error {
		got = append(got, c)
		return nil
	})
	if err != nil {
		t.Fatalf("TransportStream: %v", err)
	}
	if len(got) != 2 || got[0].Delta != "a" || !got[1].Done || !fp.gotReq.Stream {
		t.Fatalf("chunks = %+v req=%+v", got, fp.gotReq)
	}
}

func TestMembraneNoProvider(t *testing.T) {
	m := transporter.New(map[types.PathwayID]transporter.PoolSpec{}, fakeKeyring{})
	if _, err := m.Transport(context.Background(), sanitize(t, false)); !errors.Is(err, transporter.ErrNoProvider) {
		t.Fatalf("err = %v, want ErrNoProvider", err)
	}
	if err := m.TransportStream(context.Background(), sanitize(t, true), func(types.StreamChunk) error { return nil }); !errors.Is(err, transporter.ErrNoProvider) {
		t.Fatalf("stream err = %v", err)
	}
}

func TestMembraneKeyringError(t *testing.T) {
	fp := &fakeProvider{id: "openai"}
	m := transporter.New(pool("", target(fp, "x", 0)), fakeKeyring{err: errors.New("unresolvable")})
	if _, err := m.Transport(context.Background(), sanitize(t, false)); err == nil {
		t.Fatal("expected keyring error to propagate")
	}
	if fp.calls != 0 {
		t.Fatal("adapter must not be called when identity is unresolvable")
	}
}

func TestPrioritySelection(t *testing.T) {
	primary := &fakeProvider{id: "openai", resp: types.CanonicalResponse{Provider: "openai"}}
	secondary := &fakeProvider{id: "anthropic", resp: types.CanonicalResponse{Provider: "anthropic"}}
	m := transporter.New(pool(transporter.StrategyPriority,
		target(secondary, "a", 1), target(primary, "o", 0)),
		fakeKeyring{def: providers.EgressIdentity{Secret: "t"}})
	resp, _ := m.Transport(context.Background(), sanitize(t, false))
	if resp.Provider != "openai" || primary.calls != 1 || secondary.calls != 0 {
		t.Fatalf("priority did not prefer lowest number: resp=%+v", resp)
	}
}

func TestRoundRobin(t *testing.T) {
	a := &fakeProvider{id: "a", resp: types.CanonicalResponse{Provider: "a"}}
	b := &fakeProvider{id: "b", resp: types.CanonicalResponse{Provider: "b"}}
	m := transporter.New(pool(transporter.StrategyRoundRobin,
		target(a, "a", 0), target(b, "b", 1)),
		fakeKeyring{def: providers.EgressIdentity{Secret: "t"}})
	var seq []types.ProviderID
	for i := 0; i < 4; i++ {
		resp, _ := m.Transport(context.Background(), sanitize(t, false))
		seq = append(seq, resp.Provider)
	}
	if seq[0] != "a" || seq[1] != "b" || seq[2] != "a" || seq[3] != "b" {
		t.Fatalf("round-robin sequence = %v", seq)
	}
}

func TestFailoverOnRetryableError(t *testing.T) {
	bad := &fakeProvider{id: "openai", returnErr: providers.UpstreamStatusError{Status: 503}}
	good := &fakeProvider{id: "anthropic", resp: types.CanonicalResponse{Provider: "anthropic"}}
	m := transporter.New(pool(transporter.StrategyPriority,
		target(bad, "o", 0), target(good, "a", 1)),
		fakeKeyring{def: providers.EgressIdentity{Secret: "t"}})
	resp, err := m.Transport(context.Background(), sanitize(t, false))
	if err != nil || resp.Provider != "anthropic" {
		t.Fatalf("resp=%+v err=%v, want failover to anthropic", resp, err)
	}
	if bad.calls != 1 || good.calls != 1 {
		t.Fatalf("calls bad=%d good=%d", bad.calls, good.calls)
	}
}

func TestNoFailoverOnClient4xx(t *testing.T) {
	bad := &fakeProvider{id: "openai", returnErr: providers.UpstreamStatusError{Status: 400}}
	good := &fakeProvider{id: "anthropic", resp: types.CanonicalResponse{Provider: "anthropic"}}
	m := transporter.New(pool(transporter.StrategyPriority,
		target(bad, "o", 0), target(good, "a", 1)),
		fakeKeyring{def: providers.EgressIdentity{Secret: "t"}})
	_, err := m.Transport(context.Background(), sanitize(t, false))
	var se providers.UpstreamStatusError
	if !errors.As(err, &se) || se.Status != 400 {
		t.Fatalf("err = %v, want 400 (no failover)", err)
	}
	if good.calls != 0 {
		t.Fatal("must not fail over on a request-level 4xx")
	}
}

func TestCircuitBreakerOpensAndRecovers(t *testing.T) {
	now := time.Unix(0, 0)
	fp := &fakeProvider{id: "openai", returnErr: providers.UpstreamStatusError{Status: 503}}
	m := transporter.New(pool("", target(fp, "o", 0)),
		fakeKeyring{def: providers.EgressIdentity{Secret: "t"}},
		transporter.WithClock(func() time.Time { return now }),
		transporter.WithBackoff(transporter.BackoffPolicy{BreakerThreshold: 2, BreakerCooldown: time.Minute}),
	)
	req := func() error { _, e := m.Transport(context.Background(), sanitize(t, false)); return e }

	// Two consecutive failures open the breaker.
	_ = req()
	_ = req()
	callsAtOpen := fp.calls
	// While open, the target is skipped.
	if err := req(); !errors.Is(err, transporter.ErrNoProvider) {
		t.Fatalf("open-breaker err = %v, want ErrNoProvider", err)
	}
	if fp.calls != callsAtOpen {
		t.Fatalf("breaker did not skip: calls went %d -> %d", callsAtOpen, fp.calls)
	}

	// After cooldown, a single half-open probe is allowed; make it succeed.
	now = now.Add(2 * time.Minute)
	fp.returnErr = nil
	fp.resp = types.CanonicalResponse{Provider: "openai"}
	if err := req(); err != nil {
		t.Fatalf("half-open probe err = %v, want success", err)
	}
	// Breaker closed: subsequent calls flow normally.
	if err := req(); err != nil {
		t.Fatalf("post-recovery err = %v", err)
	}
}

func TestTransportReturnsOnClientCancel(t *testing.T) {
	fp := &fakeProvider{id: "openai", returnErr: providers.UpstreamStatusError{Status: 503}}
	good := &fakeProvider{id: "anthropic", resp: types.CanonicalResponse{Provider: "anthropic"}}
	m := transporter.New(pool(transporter.StrategyPriority,
		target(fp, "o", 0), target(good, "a", 1)),
		fakeKeyring{def: providers.EgressIdentity{Secret: "t"}})
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // client already gone
	_, err := m.Transport(ctx, sanitize(t, false))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if good.calls != 0 {
		t.Fatal("must not fail over after client cancellation")
	}
}

func TestStreamKeyringErrorFailsClosed(t *testing.T) {
	fp := &fakeProvider{id: "openai"}
	m := transporter.New(pool("", target(fp, "x", 0)), fakeKeyring{err: errors.New("unresolvable")})
	err := m.TransportStream(context.Background(), sanitize(t, true), func(types.StreamChunk) error { return nil })
	if err == nil {
		t.Fatal("want keyring error")
	}
	if fp.calls != 0 {
		t.Fatal("adapter must not stream when identity is unresolvable")
	}
}

func TestStreamNoFailoverOn4xx(t *testing.T) {
	bad := &fakeProvider{id: "openai", returnErr: providers.UpstreamStatusError{Status: 404}}
	good := &fakeProvider{id: "anthropic", chunks: []types.StreamChunk{{Done: true}}}
	m := transporter.New(pool(transporter.StrategyPriority,
		target(bad, "o", 0), target(good, "a", 1)),
		fakeKeyring{def: providers.EgressIdentity{Secret: "t"}})
	err := m.TransportStream(context.Background(), sanitize(t, true), func(types.StreamChunk) error { return nil })
	var se providers.UpstreamStatusError
	if !errors.As(err, &se) || se.Status != 404 {
		t.Fatalf("err = %v, want 404", err)
	}
	if good.calls != 0 {
		t.Fatal("must not fail over on request-level 4xx")
	}
}

// blockingProvider waits for context cancellation, returning its error.
type blockingProvider struct{ id types.ProviderID }

func (b blockingProvider) ID() types.ProviderID { return b.id }
func (b blockingProvider) Complete(ctx context.Context, _ providers.Request, _ providers.EgressIdentity) (types.CanonicalResponse, error) {
	<-ctx.Done()
	return types.CanonicalResponse{}, ctx.Err()
}
func (b blockingProvider) Stream(context.Context, providers.Request, providers.EgressIdentity, func(types.StreamChunk) error) error {
	return nil
}

func TestTransportPerTargetTimeout(t *testing.T) {
	spec := transporter.TargetSpec{
		Adapter:      blockingProvider{id: "slow"},
		KeyringRef:   "o",
		Model:        "m",
		TimeoutTotal: 5 * time.Millisecond,
	}
	m := transporter.New(map[types.PathwayID]transporter.PoolSpec{"chat": {Targets: []transporter.TargetSpec{spec}}},
		fakeKeyring{def: providers.EgressIdentity{Secret: "t"}})
	_, err := m.Transport(context.Background(), sanitize(t, false))
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want DeadlineExceeded from per-target timeout", err)
	}
}

func TestStreamSkipsOpenBreaker(t *testing.T) {
	fp := &fakeProvider{id: "openai", returnErr: providers.UpstreamStatusError{Status: 503}}
	m := transporter.New(pool("", target(fp, "o", 0)),
		fakeKeyring{def: providers.EgressIdentity{Secret: "t"}},
		transporter.WithBackoff(transporter.BackoffPolicy{BreakerThreshold: 1, BreakerCooldown: time.Hour}),
	)
	noop := func(types.StreamChunk) error { return nil }
	// First attempt fails and opens the breaker (threshold 1).
	_ = m.TransportStream(context.Background(), sanitize(t, true), noop)
	callsAfterOpen := fp.calls
	// The second attempt skips the open target.
	if err := m.TransportStream(context.Background(), sanitize(t, true), noop); !errors.Is(err, transporter.ErrNoProvider) {
		t.Fatalf("err = %v, want ErrNoProvider while breaker open", err)
	}
	if fp.calls != callsAfterOpen {
		t.Fatalf("breaker did not skip stream: calls %d -> %d", callsAfterOpen, fp.calls)
	}
}

func TestStreamAllTargetsFail(t *testing.T) {
	a := &fakeProvider{id: "openai", returnErr: providers.UpstreamStatusError{Status: 503}}
	b := &fakeProvider{id: "anthropic", returnErr: providers.UpstreamStatusError{Status: 502}}
	m := transporter.New(pool(transporter.StrategyPriority,
		target(a, "o", 0), target(b, "a", 1)),
		fakeKeyring{def: providers.EgressIdentity{Secret: "t"}})
	err := m.TransportStream(context.Background(), sanitize(t, true), func(types.StreamChunk) error { return nil })
	var se providers.UpstreamStatusError
	if !errors.As(err, &se) || se.Status != 502 {
		t.Fatalf("err = %v, want last error 502 after exhausting pool", err)
	}
	if a.calls != 1 || b.calls != 1 {
		t.Fatalf("calls a=%d b=%d", a.calls, b.calls)
	}
}

func TestStreamFailoverBeforeFirstByte(t *testing.T) {
	bad := &fakeProvider{id: "openai", returnErr: providers.UpstreamStatusError{Status: 503}}
	good := &fakeProvider{id: "anthropic", chunks: []types.StreamChunk{{Delta: "hi"}, {Done: true}}}
	m := transporter.New(pool(transporter.StrategyPriority,
		target(bad, "o", 0), target(good, "a", 1)),
		fakeKeyring{def: providers.EgressIdentity{Secret: "t"}})
	var got []types.StreamChunk
	err := m.TransportStream(context.Background(), sanitize(t, true), func(c types.StreamChunk) error {
		got = append(got, c)
		return nil
	})
	if err != nil || len(got) != 2 || got[0].Delta != "hi" {
		t.Fatalf("got=%+v err=%v, want failover to good", got, err)
	}
}

func TestStreamNoFailoverAfterFirstByte(t *testing.T) {
	bad := &fakeProvider{
		id:                "openai",
		streamEmitThenErr: []types.StreamChunk{{Delta: "par"}},
		returnErr:         providers.UpstreamStatusError{Status: 503},
	}
	good := &fakeProvider{id: "anthropic", chunks: []types.StreamChunk{{Delta: "hi"}, {Done: true}}}
	m := transporter.New(pool(transporter.StrategyPriority,
		target(bad, "o", 0), target(good, "a", 1)),
		fakeKeyring{def: providers.EgressIdentity{Secret: "t"}})
	var got []types.StreamChunk
	err := m.TransportStream(context.Background(), sanitize(t, true), func(c types.StreamChunk) error {
		got = append(got, c)
		return nil
	})
	if err == nil {
		t.Fatal("want error after mid-stream failure (no silent failover)")
	}
	if good.calls != 0 {
		t.Fatal("must not fail over once bytes are relayed")
	}
	if len(got) != 1 || got[0].Delta != "par" {
		t.Fatalf("got = %+v", got)
	}
}
