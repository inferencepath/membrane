package resolve_test

import (
	"testing"
	"time"

	"github.com/inferencepath/membrane/internal/interior/resolve"
	"github.com/inferencepath/membrane/internal/types"
)

func TestResolveFound(t *testing.T) {
	r := resolve.New(map[types.PathwayID]resolve.Binding{
		"chat": {Provider: "openai", Model: "m", TimeoutTotal: 10 * time.Second, TimeoutIdle: 5 * time.Second},
	})
	b, ok := r.Resolve("chat")
	if !ok {
		t.Fatal("expected chat binding")
	}
	if b.Provider != "openai" || b.Model != "m" {
		t.Fatalf("binding = %+v", b)
	}
	if b.TimeoutTotal != 10*time.Second || b.TimeoutIdle != 5*time.Second {
		t.Fatalf("timeouts = %v / %v", b.TimeoutTotal, b.TimeoutIdle)
	}
}

func TestResolveMissing(t *testing.T) {
	r := resolve.New(nil)
	if _, ok := r.Resolve("nope"); ok {
		t.Fatal("expected no binding")
	}
}

func TestResolverOperation(t *testing.T) {
	r := resolve.New(map[types.PathwayID]resolve.Binding{
		"chat":   {Provider: "openai", Model: "m"},
		"embed":  {Provider: "openai", Model: "m"},
		"legacy": {Provider: "openai", Model: "m"},
	})
	r.SetPolicy(map[types.PathwayID]resolve.PathwayPolicy{
		"chat":   {Operation: types.OperationChat, DefaultProvider: "openai"},
		"embed":  {Operation: types.OperationEmbeddings, DefaultProvider: "openai"},
		"legacy": {DefaultProvider: "openai"}, // no operation set, so it defaults to chat
	})

	if op, ok := r.Operation("chat"); !ok || op != types.OperationChat {
		t.Fatalf("chat operation = %q ok=%v", op, ok)
	}
	if op, ok := r.Operation("embed"); !ok || op != types.OperationEmbeddings {
		t.Fatalf("embed operation = %q ok=%v", op, ok)
	}
	if op, ok := r.Operation("legacy"); !ok || op != types.OperationChat {
		t.Fatalf("legacy operation = %q ok=%v, want chat default", op, ok)
	}
	if _, ok := r.Operation("missing"); ok {
		t.Fatal("missing pathway should report no policy")
	}
}

func TestResolveAppliesDefaults(t *testing.T) {
	r := resolve.New(map[types.PathwayID]resolve.Binding{"chat": {Provider: "openai", Model: "m"}})
	b, _ := r.Resolve("chat")
	if b.TimeoutTotal != resolve.DefaultTimeoutTotal {
		t.Errorf("TimeoutTotal = %v, want default %v", b.TimeoutTotal, resolve.DefaultTimeoutTotal)
	}
	if b.TimeoutIdle != resolve.DefaultTimeoutIdle {
		t.Errorf("TimeoutIdle = %v, want default %v", b.TimeoutIdle, resolve.DefaultTimeoutIdle)
	}
}
