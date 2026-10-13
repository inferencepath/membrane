package types

import "testing"

func TestParseOperationDefaultsToChat(t *testing.T) {
	op, err := ParseOperation("")
	if err != nil {
		t.Fatalf("empty operation: unexpected error %v", err)
	}
	if op != DefaultOperation {
		t.Fatalf("empty operation = %q, want chat (default)", op)
	}
	if DefaultOperation != OperationChat {
		t.Fatalf("DefaultOperation = %q, want chat", DefaultOperation)
	}
}

func TestParseOperationKnown(t *testing.T) {
	for _, op := range AllOperations() {
		got, err := ParseOperation(string(op))
		if err != nil {
			t.Fatalf("ParseOperation(%q) error: %v", op, err)
		}
		if got != op {
			t.Fatalf("ParseOperation(%q) = %q", op, got)
		}
		if !got.Valid() {
			t.Fatalf("operation %q reported invalid", op)
		}
		if got.Route() == "" {
			t.Fatalf("operation %q has no route", op)
		}
	}
}

func TestParseOperationUnknownFailsClosed(t *testing.T) {
	if _, err := ParseOperation("teleport"); err == nil {
		t.Fatal("expected error for unknown operation, got nil")
	}
	if Operation("teleport").Valid() {
		t.Fatal("unknown operation reported valid")
	}
	if Operation("teleport").Route() != "" {
		t.Fatal("unknown operation returned a route")
	}
}

func TestOperationRoutesAreUnique(t *testing.T) {
	seen := map[string]Operation{}
	for _, op := range AllOperations() {
		r := op.Route()
		if prev, dup := seen[r]; dup {
			t.Fatalf("route %q shared by %q and %q", r, prev, op)
		}
		seen[r] = op
	}
	if OperationChat.Route() != "/v1/chat" {
		t.Fatalf("chat route = %q", OperationChat.Route())
	}
}
