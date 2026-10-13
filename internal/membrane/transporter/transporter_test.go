package transporter_test

import (
	"context"
	"errors"
	"testing"

	"github.com/inferencepath/membrane/internal/membrane/permeability"
	"github.com/inferencepath/membrane/internal/membrane/scrub"
	"github.com/inferencepath/membrane/internal/membrane/transporter"
	"github.com/inferencepath/membrane/internal/types"
)

// Stub must satisfy the Transporter interface.
var _ transporter.Transporter = transporter.Stub{}

func TestStubReturnsNotImplemented(t *testing.T) {
	spec, err := permeability.Parse([]byte(`
version: 1
pathways:
  chat:
    outbound:
      allow: [messages]
    inbound:
      default: [message]
`))
	if err != nil {
		t.Fatal(err)
	}
	sr, err := scrub.Sanitize(types.CanonicalRequest{
		PathwayID: "chat",
		Payload:   map[string]any{"messages": []any{}},
	}, spec)
	if err != nil {
		t.Fatal(err)
	}

	resp, err := transporter.NewStub().Transport(context.Background(), sr)
	if !errors.Is(err, transporter.ErrNotImplemented) {
		t.Fatalf("err = %v, want ErrNotImplemented", err)
	}
	if resp.Message.Content != "" || resp.FinishReason != "" || resp.Model != "" {
		t.Fatalf("resp = %+v, want zero value", resp)
	}

	streamErr := transporter.NewStub().TransportStream(context.Background(), sr, func(types.StreamChunk) error { return nil })
	if !errors.Is(streamErr, transporter.ErrNotImplemented) {
		t.Fatalf("stream err = %v, want ErrNotImplemented", streamErr)
	}
}
