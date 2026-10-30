package pipeline

import "context"

// NoOp is the default ContentStage. It leaves content unchanged.
type NoOp struct{}

// Ingress does nothing and never fails.
func (NoOp) Ingress(context.Context, *RequestContext) error { return nil }

// Egress does nothing and never fails.
func (NoOp) Egress(context.Context, *ResponseContext) error { return nil }

var _ ContentStage = NoOp{}
