package console

import (
	"net"
	"net/http"
	"sync"
	"time"
)

// adminAuditCap bounds the in-memory admin-action audit ring.
const adminAuditCap = 200

// UsageRecord is a console-local, secret-free view of one Interior-only usage
// audit record. It mirrors the Interior audit record's safe fields: it carries
// NO prompt/response content, NO secret, NO Reveal flag, and NO Exterior
// topology (endpoint/region/deployment/ARN/keyring_ref/provider). The concrete
// AuditSource (in internal/consoleaudit) maps the Interior record to this type,
// so the console package never imports the Interior audit package.
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
// A nil AuditSource disables the usage-audit endpoint (503) — e.g. when no file
// audit sink is configured.
type AuditSource interface {
	// Recent returns up to limit most-recent usage records, newest first.
	Recent(limit int) ([]UsageRecord, error)
}

// handleAuditUsage serves recent Interior-only usage records. It is
// read-only and secret-free; when no audit source is wired it returns 503.
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

// AdminAuditEntry is a basic, local record of a console admin action: who (the
// request's remote), what (the action + outcome), and when. It is secret-free:
// it names the config kind or action, never any value, token, or content.
// Tamper-evident export / SIEM / retention are out of scope (Enterprise).
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

// recordAdmin logs a console admin action, secret-free.
func (c *Console) recordAdmin(r *http.Request, action, result string) {
	c.admin.record(AdminAuditEntry{
		Time:   time.Now().UTC(),
		Action: action,
		Result: result,
		Remote: remoteLabel(r.RemoteAddr),
	})
}

// applyOutcome maps an ApplyResult to a stable, secret-free outcome label.
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
// loopback client, otherwise the bare host (not a secret).
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
