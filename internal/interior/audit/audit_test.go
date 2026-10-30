package audit_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/inferencepath/membrane/internal/interior/audit"
)

var _ audit.Sink = audit.Discard{}
var _ audit.Sink = (*audit.JSONLines)(nil)

func sampleRecord() audit.Record {
	return audit.Record{
		SchemaVersion:    audit.SchemaVersion,
		Timestamp:        time.Date(2026, 10, 3, 4, 5, 6, 0, time.UTC),
		CallerID:         "acme-app",
		PathwayID:        "chat",
		Outcome:          "success",
		DurationMillis:   42,
		PromptTokens:     3,
		CompletionTokens: 7,
		TotalTokens:      10,
		Stream:           false,
	}
}

func TestDiscardWrite(t *testing.T) {
	var sink audit.Sink = audit.Discard{}
	sink.Write(sampleRecord()) // must not panic
}

func TestJSONLinesWritesOneLinePerRecord(t *testing.T) {
	var buf bytes.Buffer
	sink := audit.NewJSONLines(&buf, nil)
	sink.Write(sampleRecord())
	sink.Write(sampleRecord())

	lines := strings.Split(strings.TrimRight(buf.String(), "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("want 2 lines, got %d: %q", len(lines), buf.String())
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &got); err != nil {
		t.Fatalf("line is not valid JSON: %v", err)
	}
	// Required fields are present with the expected values.
	for _, k := range []string{"schema_version", "timestamp", "caller_id", "pathway", "outcome", "duration_ms", "prompt_tokens", "completion_tokens", "total_tokens", "stream"} {
		if _, ok := got[k]; !ok {
			t.Fatalf("record missing field %q: %v", k, got)
		}
	}
	if got["caller_id"] != "acme-app" || got["pathway"] != "chat" || got["outcome"] != "success" {
		t.Fatalf("unexpected values: %v", got)
	}
}

// TestJSONLinesContentSafety checks that only the known Interior fields are
// serialized.
func TestJSONLinesContentSafety(t *testing.T) {
	var buf bytes.Buffer
	audit.NewJSONLines(&buf, nil).Write(sampleRecord())

	allowed := map[string]struct{}{
		"schema_version": {}, "timestamp": {}, "caller_id": {}, "pathway": {},
		"outcome": {}, "duration_ms": {}, "prompt_tokens": {}, "completion_tokens": {},
		"total_tokens": {}, "stream": {},
		"tools_offered": {}, "tool_calls_returned": {}, "tool_turns_sent": {},
	}
	var got map[string]any
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	for k := range got {
		if _, ok := allowed[k]; !ok {
			t.Fatalf("unexpected field %q in audit record (possible leak)", k)
		}
	}
	// Check the forbidden keys by name as well.
	for _, forbidden := range []string{"content", "messages", "secret", "reveal", "endpoint", "region", "deployment", "provider", "keyring_ref", "arn"} {
		if _, ok := got[forbidden]; ok {
			t.Fatalf("audit record contains forbidden field %q", forbidden)
		}
	}
}

// failingWriter fails every write.
type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) { return 0, errors.New("disk full") }

func TestJSONLinesBestEffortOnWriteError(t *testing.T) {
	var gotErr error
	sink := audit.NewJSONLines(failingWriter{}, func(err error) { gotErr = err })
	// The error goes to onErr; Write itself must not panic.
	sink.Write(sampleRecord())
	if gotErr == nil {
		t.Fatalf("expected onErr to be called on write failure")
	}
}

func TestJSONLinesNilOnErrSafe(t *testing.T) {
	sink := audit.NewJSONLines(failingWriter{}, nil)
	sink.Write(sampleRecord()) // nil onErr must be safe
}
