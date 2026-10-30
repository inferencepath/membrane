package types

// Pathway is a logical endpoint naming intent, never a vendor.
type Pathway struct {
	ID          PathwayID
	Description string
	// Operation is the kind of egress work this Pathway performs. The zero
	// value means DefaultOperation.
	Operation Operation
}

// Binding maps a Pathway to a concrete Provider and model. It stays on the
// Interior and is not disclosed to a Caller.
type Binding struct {
	PathwayID PathwayID
	Provider  ProviderID
	Model     string
}
