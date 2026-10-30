package console

import (
	"net"
	"net/http"
	"sync"
	"time"
)

// adminAuditCap bounds the in-memory admin-action audit ring.
const adminAuditCap = 200

// UsageRecord is the console's view of one Interior-only usage audit record.
// It has no prompt or response content, no secret, no Reveal flag, and no
// Exterior topology (endpoint, region, deployment, ARN, keyring_ref,
// provider). internal/consoleaudit maps Interior records to this type so the
// console does not import the Interior audit package.
type UsageRecord struct {
	Time             time.Time `json:"time"`
	CallerID         string    `json:"callerId"`
	Pathway          string    `json:"pathway"`
	Outcome          string    `json:"outcome"`
	DurationMillis   int64     `json:"durationMs"`
	PromptTokens     int       `json:"promptTokens"`
	CompletionTokens int       `json:"completionTokens"`
	TotalTokens      int       `json:"totalTokens"`
	Stream           bool      `json:"stream"`
}

// AuditSource provides read-only access to recent Interior-only usage records.
// A nil AuditSource, as when no file audit sink is configured, makes the
// usage-audit endpoint return 503.
type AuditSource interface {
	// Recent returns up to limit most-recent usage records, newest first.
	Recent(limit int) ([]UsageRecord, error)
}

// handleAuditUsage serves recent Interior-only usage records.
func (c *Console) handleAuditUsage(w http.ResponseWriter, _ *http.Request) {
	if c.cfg.Audit == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{
			"error": "usage audit is not available (enable a file audit sink)",
		})
		return
	}
	records, err := c.cfg.Audit.Recent(200)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	if records == nil {
		records = []UsageRecord{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"records": records})
}

// AdminAuditEntry is a local record of a console admin action: the remote
// host, the action and its result, and the time. It names the config kind or
// action, never a value, token, or content.
type AdminAuditEntry struct {
	Time   time.Time `json:"time"`
	Action string    `json:"action"`
	Result string    `json:"result"`
	Remote string    `json:"remote"`
}

// adminAudit is a fixed-capacity, in-memory ring of admin-action entries.
type adminAudit struct {
	mu      sync.Mutex
	entries []AdminAuditEntry
	max     int
}

func newAdminAudit(max int) *adminAudit {
	return &adminAudit{max: max}
}

func (a *adminAudit) record(e AdminAuditEntry) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.entries = append(a.entries, e)
	if len(a.entries) > a.max {
		a.entries = a.entries[len(a.entries)-a.max:]
	}
}

// list returns a copy of the entries, newest first.
func (a *adminAudit) list() []AdminAuditEntry {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := make([]AdminAuditEntry, len(a.entries))
	for i, e := range a.entries {
		out[len(a.entries)-1-i] = e
	}
	return out
}

// recordAdmin adds a console admin action to the admin audit.
func (c *Console) recordAdmin(r *http.Request, action, result string) {
	c.admin.record(AdminAuditEntry{
		Time:   time.Now().UTC(),
		Action: action,
		Result: result,
		Remote: remoteLabel(r.RemoteAddr),
	})
}

// applyOutcome maps an ApplyResult to a stable outcome label.
func applyOutcome(res ApplyResult) string {
	switch {
	case res.Applied:
		return "applied"
	case res.Unchanged:
		return "unchanged"
	default:
		return "rejected"
	}
}

// handleAuditAdmin serves the in-memory admin-action audit (newest first).
func (c *Console) handleAuditAdmin(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"entries": c.admin.list()})
}

// remoteLabel renders a request's origin for the admin audit: "localhost" for a
// loopback client, otherwise the bare host.
func remoteLabel(remoteAddr string) string {
	if isLoopbackRemote(remoteAddr) {
		return "localhost"
	}
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		return remoteAddr
	}
	return host
}
