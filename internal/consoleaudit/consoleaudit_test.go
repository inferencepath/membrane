package consoleaudit_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/inferencepath/membrane/internal/consoleaudit"
	"github.com/inferencepath/membrane/internal/server/console"
)

func write(t *testing.T, dir, name, body string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestRecentReturnsNewestFirst(t *testing.T) {
	dir := t.TempDir()
	// Three records; a blank line and a malformed line are tolerated.
	body := `{"schema_version":1,"timestamp":"2026-10-08T10:00:00Z","caller_id":"a","pathway":"chat","outcome":"success","total_tokens":3}
{"schema_version":1,"timestamp":"2026-10-08T10:01:00Z","caller_id":"b","pathway":"chat","outcome":"success","total_tokens":5}

{not valid json}
{"schema_version":1,"timestamp":"2026-10-08T10:02:00Z","caller_id":"c","pathway":"chat","outcome":"error","total_tokens":0}
`
	src := consoleaudit.New(write(t, dir, "audit.jsonl", body))
	recs, err := src.Recent(10)
	if err != nil {
		t.Fatalf("Recent: %v", err)
	}
	if len(recs) != 3 {
		t.Fatalf("got %d records, want 3 (malformed/blank skipped)", len(recs))
	}
	if recs[0].CallerID != "c" || recs[2].CallerID != "a" {
		t.Fatalf("not newest-first: %+v", recs)
	}
}

func TestRecentLimitKeepsLatest(t *testing.T) {
	dir := t.TempDir()
	body := `{"schema_version":1,"caller_id":"a","pathway":"chat","outcome":"success"}
{"schema_version":1,"caller_id":"b","pathway":"chat","outcome":"success"}
{"schema_version":1,"caller_id":"c","pathway":"chat","outcome":"success"}
`
	src := consoleaudit.New(write(t, dir, "audit.jsonl", body))
	recs, err := src.Recent(2)
	if err != nil {
		t.Fatalf("Recent: %v", err)
	}
	if len(recs) != 2 || recs[0].CallerID != "c" || recs[1].CallerID != "b" {
		t.Fatalf("limit did not keep latest newest-first: %+v", recs)
	}
}

func TestRecentMissingFileIsEmpty(t *testing.T) {
	src := consoleaudit.New(filepath.Join(t.TempDir(), "nope.jsonl"))
	recs, err := src.Recent(10)
	if err != nil || len(recs) != 0 {
		t.Fatalf("missing file: recs=%v err=%v", recs, err)
	}
}

func TestRecentZeroLimit(t *testing.T) {
	src := consoleaudit.New(filepath.Join(t.TempDir(), "x.jsonl"))
	if recs, err := src.Recent(0); err != nil || recs != nil {
		t.Fatalf("zero limit: recs=%v err=%v", recs, err)
	}
}

func TestImplementsAuditSource(t *testing.T) {
	var _ console.AuditSource = (*consoleaudit.Source)(nil)
}
