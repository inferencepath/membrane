package types

// Pathway is a logical endpoint naming intent, never a vendor.
type Pathway struct {
	ID          PathwayID
	Description string
	// Operation is the kind of egress work this Pathway performs. The zero value
	// ("") is treated as DefaultOperation (chat) for backward compatibility.
	Operation Operation
}

// Binding maps a Pathway to a concrete Provider + model. Interior-only: its
// rules are never disclosed to a Caller.
type Binding struct {
	PathwayID PathwayID
	Provider  ProviderID
	Model     string
}
