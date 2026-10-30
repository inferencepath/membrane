// Package authorize enforces per-Caller access control on the Interior, between
// Admit (authentication) and Resolve (routing). Access is denied by default: a
// Caller may use a Pathway only if it is on the Caller's allowlist. An
// unauthorized request is rejected before any SanitizedRequest is built, so it
// never reaches the Membrane or a Provider.
//
// Every denial returns the same ErrForbidden and does not say whether the
// Pathway exists, so a Caller cannot enumerate Pathways.
package authorize

import (
	"errors"

	"github.com/inferencepath/membrane/internal/types"
)

// ErrForbidden is returned when a Caller is not authorized for a Pathway. It
// carries no detail.
var ErrForbidden = errors.New("authorize: forbidden")

// Policy reports whether a CallerID is authorized for a Pathway. The Caller
// store implements it.
type Policy interface {
	Allows(callerID string, pathway types.PathwayID) bool
}

// Authorizer applies a Policy, denying anything it does not allow.
type Authorizer struct {
	policy Policy
}

// New returns an Authorizer over the given Policy. A nil Policy denies everything
// (fail closed).
func New(policy Policy) *Authorizer {
	return &Authorizer{policy: policy}
}

// Authorize returns nil if the Caller may use the Pathway and ErrForbidden
// otherwise. A nil Authorizer or Policy denies every request.
func (a *Authorizer) Authorize(caller types.Caller, pathway types.PathwayID) error {
	if a == nil || a.policy == nil {
		return ErrForbidden
	}
	if !a.policy.Allows(caller.ID, pathway) {
		return ErrForbidden
	}
	return nil
}
