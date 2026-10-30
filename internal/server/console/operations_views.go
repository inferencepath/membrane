package console

import "net/http"

// The operations catalog lists the egress operations a Pathway may serve, with
// each operation's route and Permeability field palette. The concrete
// OperationCatalog is injected so the console imports no Interior, Membrane, or
// Exterior package.

// OperationView describes one egress operation for the Pathways wizard.
//
// Restricted reports whether the operation has a closed Permeability
// vocabulary. It is true for the non-chat operations, where the wizard offers
// only OutboundAllow and InboundFields. It is false for chat, which permits any
// field name not on the outbound denylist.
type OperationView struct {
	Operation     string   `json:"operation"`
	Route         string   `json:"route"`
	Restricted    bool     `json:"restricted"`
	OutboundAllow []string `json:"outboundAllow,omitempty"`
	InboundFields []string `json:"inboundFields,omitempty"`
}

// OperationsView is the full operations catalog in a stable order.
type OperationsView struct {
	Operations []OperationView `json:"operations"`
}

// OperationCatalog supplies the operations catalog. It is built from
// compile-time vocabularies rather than the active config, so it cannot fail.
// A nil OperationCatalog disables the endpoint (503).
type OperationCatalog interface {
	Operations() OperationsView
}

func (c *Console) handleOperations(w http.ResponseWriter, r *http.Request) {
	if c.cfg.Operations == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "operations catalog not available"})
		return
	}
	writeJSON(w, http.StatusOK, c.cfg.Operations.Operations())
}
