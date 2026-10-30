package chat_test

import (
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/inferencepath/membrane/internal/interior/audit"
	"github.com/inferencepath/membrane/internal/server/chat"
	fakeoa "github.com/inferencepath/membrane/test/fakes/openai"
	"github.com/inferencepath/membrane/test/harness"
)

// recordingSink captures audit records for assertions.
type recordingSink struct {
	mu      sync.Mutex
	records []audit.Record
}

func (s *recordingSink) Write(r audit.Record) {
	s.mu.Lock()
	s.records = append(s.records, r)
	s.mu.Unlock()
}

func (s *recordingSink) all() []audit.Record {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]audit.Record(nil), s.records...)
}

// TestAuditOneRecordPerServedRequest checks that a successful request writes
// one record with the right Caller, Pathway, outcome and token counts.
func TestAuditOneRecordPerServedRequest(t *testing.T) {
	t.Setenv(harness.EgressEnv, harness.EgressToken)
	fake := fakeoa.New(fakeoa.Config{Content: "hi", Model: "m", FinishReason: "stop", TotalTokens: 9})
	defer fake.Close()

	sink := &recordingSink{}
	h, err := harness.BuildChatHandler(fake.URL(), nil, []chat.HandlerOption{chat.WithAudit(sink)})
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(h)
	defer ts.Close()

	resp := post(t, ts.URL+chat.Route, harness.CallerKey, `{"pathway":"chat","messages":[{"role":"user","content":"hello"}]}`)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}

	recs := sink.all()
	if len(recs) != 1 {
		t.Fatalf("want exactly 1 audit record, got %d", len(recs))
	}
	r := recs[0]
	if r.SchemaVersion != audit.SchemaVersion {
		t.Fatalf("schema version = %d", r.SchemaVersion)
	}
	if r.CallerID != "acme-app" || r.PathwayID != "chat" || r.Outcome != "success" {
		t.Fatalf("record = %+v", r)
	}
	if r.TotalTokens != 9 {
		t.Fatalf("tokens = %d, want 9", r.TotalTokens)
	}
	if r.Timestamp.IsZero() {
		t.Fatalf("timestamp must be set")
	}
}

// TestAuditRecordsUpstreamFailureOutcome checks that an upstream error writes
// one record with a failure outcome and no token counts.
func TestAuditRecordsUpstreamFailureOutcome(t *testing.T) {
	t.Setenv(harness.EgressEnv, harness.EgressToken)
	fake := fakeoa.New(fakeoa.Config{Status: http.StatusInternalServerError})
	defer fake.Close()

	sink := &recordingSink{}
	h, err := harness.BuildChatHandler(fake.URL(), nil, []chat.HandlerOption{chat.WithAudit(sink)})
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(h)
	defer ts.Close()

	resp := post(t, ts.URL+chat.Route, harness.CallerKey, `{"pathway":"chat","messages":[{"role":"user","content":"hi"}]}`)
	resp.Body.Close()

	recs := sink.all()
	if len(recs) != 1 {
		t.Fatalf("want 1 record, got %d", len(recs))
	}
	if recs[0].Outcome == "success" {
		t.Fatalf("upstream failure must not be recorded as success")
	}
	if recs[0].TotalTokens != 0 {
		t.Fatalf("failed request must have 0 tokens, got %d", recs[0].TotalTokens)
	}
}
