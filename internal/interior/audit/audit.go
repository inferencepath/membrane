// Package audit holds Interior-only usage records. It shares no types or
// references with the Exterior, so audit data has no path across the Membrane:
// a Record carries only Interior facts (who called, which Pathway, when, how
// long, the coarse outcome, and token counts) and never prompt/response content,
// a secret, the Reveal flag, or any Exterior topology (endpoint, region,
// deployment, ARN, keyring_ref, or Provider family).
package audit

import (
	"encoding/json"
	"io"
	"sync"
	"time"

	"github.com/inferencepath/membrane/internal/types"
)

// SchemaVersion is the current audit record schema version.
const SchemaVersion = 1

// Record is an Interior-only usage record, emitted once per served request. Its
// JSON encoding conforms to audit-record-v1.json. It never contains payload
// content or secrets, and never crosses the Membrane.
type Record struct {
	SchemaVersion    int             `json:"schema_version"`
	Timestamp        time.Time       `json:"timestamp"`
	CallerID         string          `json:"caller_id"`
	PathwayID        types.PathwayID `json:"pathway"`
	Outcome          string          `json:"outcome"`
	DurationMillis   int64           `json:"duration_ms"`
	PromptTokens     int             `json:"prompt_tokens"`
	CompletionTokens int             `json:"completion_tokens"`
	TotalTokens      int             `json:"total_tokens"`
	Stream           bool            `json:"stream"`
	// Tool-usage counts: content-free tallies only — never tool
	// names-as-content, arguments, or results. Omitted when zero so non-tool
	// records serialize exactly as before.
	ToolsOffered      int `json:"tools_offered,omitempty"`
	ToolCallsReturned int `json:"tool_calls_returned,omitempty"`
	ToolTurnsSent     int `json:"tool_turns_sent,omitempty"`
}

// Sink receives audit Records. Implementations are Interior-only and best-effort:
// Write MUST NOT block or fail the request that produced the record.
type Sink interface {
	Write(Record)
}

// Discard is a no-op Sink (the default when auditing is disabled).
type Discard struct{}

// Write implements Sink and discards the record.
func (Discard) Write(Record) {}

// JSONLines is a Sink that appends one JSON object per line to an io.Writer
// (a file or stdout). Writes are serialized and best-effort: a marshal or write
// error is reported to onErr (if set) and never propagated to the request. The
// error is a local I/O/encoding error and carries no secret, so logging it is
// safe.
type JSONLines struct {
	mu    sync.Mutex
	w     io.Writer
	onErr func(error)
}

// NewJSONLines returns a JSONLines Sink writing to w. onErr may be nil.
func NewJSONLines(w io.Writer, onErr func(error)) *JSONLines {
	return &JSONLines{w: w, onErr: onErr}
}

// Write implements Sink.
func (j *JSONLines) Write(r Record) {
	b, err := json.Marshal(r)
	if err != nil {
		j.report(err)
		return
	}
	b = append(b, '\n')
	j.mu.Lock()
	_, werr := j.w.Write(b)
	j.mu.Unlock()
	if werr != nil {
		j.report(werr)
	}
}

func (j *JSONLines) report(err error) {
	if j.onErr != nil {
		j.onErr(err)
	}
}
