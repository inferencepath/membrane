package pipeline

import "context"

// ObserveNoOp is the default ObservationStage. It records nothing.
type ObserveNoOp struct{}

// OnRequest does nothing.
func (ObserveNoOp) OnRequest(context.Context, RequestEvent) {}

// OnProviderExchange does nothing.
func (ObserveNoOp) OnProviderExchange(context.Context, ProviderExchangeEvent) {}

// OnComplete does nothing.
func (ObserveNoOp) OnComplete(context.Context, CompleteEvent) {}

var _ ObservationStage = ObserveNoOp{}
