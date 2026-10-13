// Package authorize enforces per-Caller access control on the Interior, between
// Admit (authentication) and Resolve (routing). It is deny-by-default: a Caller
// is permitted a Pathway only if that Pathway appears on the Caller's
// allowlist. An unauthorized request is rejected before any SanitizedRequest is
// built, so it never reaches the Membrane and never contacts a Provider.
//
// Denials are intentionally generic (ErrForbidden) and never reveal whether a
// Pathway exists, so an unauthorized Caller cannot enumerate the Pathway
// namespace.
package authorize

import (
	"errors"

	"github.com/inferencepath/membrane/internal/types"
)

// ErrForbidden is returned when a Caller is not authorized for a Pathway. It
// is deliberately generic (no existence oracle, no detail).
var ErrForbidden = errors.New("authorize: forbidden")

// Policy reports whether a CallerID is authorized for a Pathway. It is
// implemented by the Caller store (deny-by-default).
type Policy interface {
	Allows(callerID string, pathway types.PathwayID) bool
}

// Authorizer applies a Policy as a deny-by-default access-control gate.
type Authorizer struct {
	policy Policy
}

// New returns an Authorizer over the given Policy. A nil Policy denies everything
// (fail closed).
func New(policy Policy) *Authorizer {
	return &Authorizer{policy: policy}
}

// Authorize permits the Caller to use the Pathway, or returns ErrForbidden.
// Deny-by-default: a nil Authorizer or Policy, or a Caller without the Pathway
// on its allowlist, is denied.
func (a *Authorizer) Authorize(caller types.Caller, pathway types.PathwayID) error {
	if a == nil || a.policy == nil {
		return ErrForbidden
	}
	if !a.policy.Allows(caller.ID, pathway) {
		return ErrForbidden
	}
	return nil
}
