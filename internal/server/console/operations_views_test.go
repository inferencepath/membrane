package console_test

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/inferencepath/membrane/internal/observability/metrics"
	"github.com/inferencepath/membrane/internal/server/console"
)

// stubOperations is a controllable OperationCatalog.
type stubOperations struct {
	view console.OperationsView
}

func (s stubOperations) Operations() console.OperationsView { return s.view }

func TestOperationsEndpointDisabledWhenNil(t *testing.T) {
	h := console.New(console.Config{
		Status:  stubStatus{ready: true},
		Metrics: metrics.NewRegistry(),
	}).Handler()
	rec := get(t, h, "/api/console/operations", "127.0.0.1:1", nil)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("code = %d, want 503", rec.Code)
	}
}

func TestOperationsEndpointReturnsCatalog(t *testing.T) {
	catalog := console.OperationsView{Operations: []console.OperationView{
		{Operation: "chat", Route: "/v1/chat", Restricted: false},
		{
			Operation:     "embeddings",
			Route:         "/v1/embeddings",
			Restricted:    true,
			OutboundAllow: []string{"input", "encoding_format", "dimensions"},
			InboundFields: []string{"data", "usage", "provider", "model"},
		},
	}}
	h := console.New(console.Config{
		Status:     stubStatus{ready: true},
		Metrics:    metrics.NewRegistry(),
		Operations: stubOperations{view: catalog},
	}).Handler()

	rec := get(t, h, "/api/console/operations", "127.0.0.1:1", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d, want 200", rec.Code)
	}
	var got console.OperationsView
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got.Operations) != 2 {
		t.Fatalf("operations = %d, want 2", len(got.Operations))
	}
	if got.Operations[0].Operation != "chat" || got.Operations[0].Restricted {
		t.Errorf("chat should be unrestricted: %+v", got.Operations[0])
	}
	emb := got.Operations[1]
	if emb.Operation != "embeddings" || !emb.Restricted || len(emb.OutboundAllow) != 3 || len(emb.InboundFields) != 4 {
		t.Errorf("embeddings palette wrong: %+v", emb)
	}
}
