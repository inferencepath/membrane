package pipeline

import (
	"context"
	"errors"
	"testing"

	"github.com/inferencepath/membrane/internal/types"
)

func TestNoOpIngressLeavesContentUnchanged(t *testing.T) {
	msgs := []types.Message{
		{Role: "user", Content: "Hi, I'm John Smith"},
		{Role: "assistant", Content: "Hello"},
	}
	rc := &RequestContext{RequestID: "r1", CallerID: "c1", PathwayID: "p1", Messages: msgs}
	if err := (NoOp{}).Ingress(context.Background(), rc); err != nil {
		t.Fatalf("NoOp.Ingress returned error: %v", err)
	}
	if msgs[0].Content != "Hi, I'm John Smith" || msgs[1].Content != "Hello" {
		t.Fatalf("NoOp.Ingress mutated content: %+v", msgs)
	}
}

func TestNoOpEgressLeavesResponseUnchanged(t *testing.T) {
	msg := &types.Message{Role: "assistant", Content: "answer"}
	if err := (NoOp{}).Egress(context.Background(), &ResponseContext{RequestID: "r1", Message: msg}); err != nil {
		t.Fatalf("NoOp.Egress returned error: %v", err)
	}
	if msg.Content != "answer" {
		t.Fatalf("NoOp.Egress mutated message: %q", msg.Content)
	}

	chunk := &types.StreamChunk{Delta: "ans"}
	if err := (NoOp{}).Egress(context.Background(), &ResponseContext{RequestID: "r1", Chunk: chunk}); err != nil {
		t.Fatalf("NoOp.Egress (chunk) returned error: %v", err)
	}
	if chunk.Delta != "ans" {
		t.Fatalf("NoOp.Egress mutated chunk: %q", chunk.Delta)
	}
}

func TestRequestIDRoundTrip(t *testing.T) {
	id := NewRequestID()
	if id == "" {
		t.Fatal("NewRequestID returned empty")
	}
	if len(id) != 32 { // 16 random bytes hex-encoded
		t.Fatalf("unexpected request ID length %d: %q", len(id), id)
	}
	if got := RequestIDFrom(WithRequestID(context.Background(), id)); got != id {
		t.Fatalf("RequestIDFrom = %q, want %q", got, id)
	}
}

func TestRequestIDUnique(t *testing.T) {
	seen := make(map[string]struct{}, 1000)
	for i := 0; i < 1000; i++ {
		id := NewRequestID()
		if _, dup := seen[id]; dup {
			t.Fatalf("duplicate request ID generated: %q", id)
		}
		seen[id] = struct{}{}
	}
}

func TestRequestIDFromEmptyContext(t *testing.T) {
	if got := RequestIDFrom(context.Background()); got != "" {
		t.Fatalf("RequestIDFrom(empty) = %q, want empty", got)
	}
}

type errReader struct{}

func (errReader) Read([]byte) (int, error) { return 0, errors.New("rng failure") }

func TestNewRequestIDRNGFailureReturnsEmpty(t *testing.T) {
	if got := newRequestID(errReader{}); got != "" {
		t.Fatalf("newRequestID(failing rng) = %q, want empty", got)
	}
}

// stageSpy is a ContentStage that counts calls and rewrites content in place.
type stageSpy struct {
	ingressCalls int
	egressCalls  int
}

func (s *stageSpy) Ingress(_ context.Context, rc *RequestContext) error {
	s.ingressCalls++
	for i := range rc.Messages {
		rc.Messages[i].Content = "[redacted]"
	}
	return nil
}

func (s *stageSpy) Egress(_ context.Context, rc *ResponseContext) error {
	s.egressCalls++
	if rc.Message != nil {
		rc.Message.Content = "restored"
	}
	if rc.Chunk != nil {
		rc.Chunk.Delta = "restored"
	}
	return nil
}

func TestContentStageMutatesInPlace(t *testing.T) {
	var _ ContentStage = (*stageSpy)(nil)

	spy := &stageSpy{}
	msgs := []types.Message{{Role: "user", Content: "secret"}}
	if err := spy.Ingress(context.Background(), &RequestContext{Messages: msgs}); err != nil {
		t.Fatalf("Ingress error: %v", err)
	}
	if msgs[0].Content != "[redacted]" {
		t.Fatalf("in-place ingress mutation not visible: %q", msgs[0].Content)
	}

	msg := &types.Message{Content: "token"}
	if err := spy.Egress(context.Background(), &ResponseContext{Message: msg}); err != nil {
		t.Fatalf("Egress error: %v", err)
	}
	if msg.Content != "restored" {
		t.Fatalf("in-place egress mutation not visible: %q", msg.Content)
	}
	if spy.ingressCalls != 1 || spy.egressCalls != 1 {
		t.Fatalf("unexpected call counts: ingress=%d egress=%d", spy.ingressCalls, spy.egressCalls)
	}
}
