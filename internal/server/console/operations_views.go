package console

import "net/http"

// This file defines the console's read-only OPERATIONS catalog: the closed set
// of egress operations a Pathway may serve, each with its canonical route and
// (for the non-chat operations) the per-operation Permeability field palette the
// wizard uses to filter which allowlist fields are selectable. As with every
// console port the view types live here (secret-free, identity-free) and the
// concrete OperationCatalog is implemented in an app-layer adapter and injected,
// so the console imports no Interior/Membrane/Exterior package.

// OperationView describes one egress operation for the Pathways wizard.
//
// Restricted reports whether the operation has a closed Permeability vocabulary:
// true for the non-chat operations (the wizard should offer only OutboundAllow /
// InboundFields), false for `chat`, which is deliberately unrestricted for
// backward compatibility (any non-denylisted field name is permitted outbound).
type OperationView struct {
	Operation     string   `json:"operation"`
	Route         string   `json:"route"`
	Restricted    bool     `json:"restricted"`
	OutboundAllow []string `json:"outboundAllow,omitempty"`
	InboundFields []string `json:"inboundFields,omitempty"`
}

// OperationsView is the full operations catalog in a stable, tier order.
type OperationsView struct {
	Operations []OperationView `json:"operations"`
}

// OperationCatalog supplies the operations catalog (the closed operation enum
// plus each operation's route and Permeability field palette). It is static —
// derived from compile-time vocabularies, not the active config — so it never
// errors. A nil OperationCatalog disables the endpoint (503).
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
