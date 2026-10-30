package pipeline

import (
	"context"
	"testing"
)

// TestObserveNoOpNeverPanics checks that every ObserveNoOp method is inert.
func TestObserveNoOpNeverPanics(t *testing.T) {
	var obs ObservationStage = ObserveNoOp{}
	ctx := context.Background()
	obs.OnRequest(ctx, RequestEvent{RequestID: "r1", PathwayID: "p", Operation: "chat"})
	obs.OnProviderExchange(ctx, ProviderExchangeEvent{RequestID: "r1"})
	obs.OnComplete(ctx, CompleteEvent{RequestID: "r1", Outcome: "success"})
}

// TestProtectionFromDefaultsUnprotected checks that a context without a marker
// reads as unprotected, so nothing is eligible for capture by default.
func TestProtectionFromDefaultsUnprotected(t *testing.T) {
	got := ProtectionFrom(context.Background())
	if got.Protected {
		t.Fatalf("default protection must be unprotected, got %+v", got)
	}
	if got.Level != "" {
		t.Fatalf("default protection level must be empty, got %q", got.Level)
	}
}

// TestWithProtectionRoundTrips checks that a marker attached to a context reads back.
func TestWithProtectionRoundTrips(t *testing.T) {
	in := ProtectionMarker{Protected: true, Level: "strict", PolicyVersion: "v1"}
	ctx := WithProtection(context.Background(), in)
	got := ProtectionFrom(ctx)
	if got != in {
		t.Fatalf("round-trip mismatch: got %+v want %+v", got, in)
	}
}

// TestRequestContextCarriesProtection checks that a stage can set the marker.
func TestRequestContextCarriesProtection(t *testing.T) {
	rc := &RequestContext{PathwayID: "p"}
	if rc.Protection.Protected {
		t.Fatalf("fresh RequestContext must be unprotected")
	}
	rc.Protection = ProtectionMarker{Protected: true, Level: "basic"}
	if !rc.Protection.Protected || rc.Protection.Level != "basic" {
		t.Fatalf("marker not retained: %+v", rc.Protection)
	}
}
