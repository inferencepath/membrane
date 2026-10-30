package authorize_test

import (
	"errors"
	"testing"

	"github.com/inferencepath/membrane/internal/interior/authorize"
	"github.com/inferencepath/membrane/internal/types"
)

// stubPolicy allows a fixed set of (callerID, pathway) pairs.
type stubPolicy map[string]map[types.PathwayID]bool

func (p stubPolicy) Allows(callerID string, pathway types.PathwayID) bool {
	return p[callerID][pathway]
}

func TestAuthorizeAllowed(t *testing.T) {
	a := authorize.New(stubPolicy{"acme": {"chat": true}})
	if err := a.Authorize(types.Caller{ID: "acme"}, "chat"); err != nil {
		t.Fatalf("allowed caller must pass: %v", err)
	}
}

func TestAuthorizeDeniedNotAllowed(t *testing.T) {
	a := authorize.New(stubPolicy{"acme": {"chat": true}})
	err := a.Authorize(types.Caller{ID: "acme"}, "summarize")
	if !errors.Is(err, authorize.ErrForbidden) {
		t.Fatalf("not-allowed pathway must be forbidden, got %v", err)
	}
}

func TestAuthorizeDenyByDefault(t *testing.T) {
	// A caller with an empty policy is authorized for nothing.
	a := authorize.New(stubPolicy{"acme": {}})
	if err := a.Authorize(types.Caller{ID: "acme"}, "chat"); !errors.Is(err, authorize.ErrForbidden) {
		t.Fatalf("empty allowlist must deny, got %v", err)
	}
	// An unknown caller is denied.
	if err := a.Authorize(types.Caller{ID: "ghost"}, "chat"); !errors.Is(err, authorize.ErrForbidden) {
		t.Fatalf("unknown caller must deny, got %v", err)
	}
}

func TestAuthorizeNilPolicyAndNilAuthorizer(t *testing.T) {
	// Nil policy fails closed.
	if err := authorize.New(nil).Authorize(types.Caller{ID: "acme"}, "chat"); !errors.Is(err, authorize.ErrForbidden) {
		t.Fatalf("nil policy must deny, got %v", err)
	}
	// Nil authorizer fails closed.
	var a *authorize.Authorizer
	if err := a.Authorize(types.Caller{ID: "acme"}, "chat"); !errors.Is(err, authorize.ErrForbidden) {
		t.Fatalf("nil authorizer must deny, got %v", err)
	}
}
