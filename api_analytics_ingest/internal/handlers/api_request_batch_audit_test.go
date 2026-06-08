package handlers

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/kafka"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"

	"github.com/google/uuid"
)

// decodeAuditSummary pulls the apiRequestBatchAuditSummary JSON written into the
// api_events.details column (append position 7) of a captured row.
func decodeAuditSummary(t *testing.T, row []any) apiRequestBatchAuditSummary {
	t.Helper()
	detailsJSON, ok := row[7].(string)
	if !ok {
		t.Fatalf("row[7] (details) = %#v, want JSON string", row[7])
	}
	var s apiRequestBatchAuditSummary
	if err := json.Unmarshal([]byte(detailsJSON), &s); err != nil {
		t.Fatalf("decode audit summary %q: %v", detailsJSON, err)
	}
	return s
}

func auditEvent() kafka.ServiceEvent {
	return kafka.ServiceEvent{
		EventID:   uuid.NewString(),
		EventType: "api_request_batch",
		Source:    "gateway",
	}
}

// TestProcessServiceAPIRequestBatchAuditCoalescesPerTenant pins the aggregation
// contract: every aggregate for the same tenant folds into one summary row whose
// counters are summed and whose auth/operation classifiers are tallied by
// occurrence. Hash lists contribute their length, not their contents.
func TestProcessServiceAPIRequestBatchAuditCoalescesPerTenant(t *testing.T) {
	tenant := uuid.New()
	batch := &captureBatch{}
	h := &AnalyticsHandler{clickhouse: &captureClickhouse{batch: batch}, logger: logging.NewLoggerWithService("test")}

	aggregates := []any{
		map[string]any{
			"tenant_id": tenant.String(), "auth_type": "jwt", "operation_type": "query",
			"request_count": uint64(3), "error_count": uint64(1),
			"total_duration_ms": uint64(10), "total_complexity": uint64(2),
			"user_hashes": []any{uint64(1), uint64(2)}, "token_hashes": []any{uint64(9)},
		},
		map[string]any{
			"tenant_id": tenant.String(), "auth_type": "apikey", "operation_type": "mutation",
			"request_count": uint64(2), "error_count": uint64(0),
			"total_duration_ms": uint64(5), "total_complexity": uint64(1),
			"user_hashes": []any{uint64(3)}, "token_hashes": []any{},
		},
	}

	if err := h.processServiceAPIRequestBatchAudit(context.Background(), auditEvent(), aggregates, "node-a", time.Now()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !batch.sendCalled {
		t.Fatal("expected the batch to be sent")
	}
	if len(batch.rows) != 1 {
		t.Fatalf("got %d rows, want 1 coalesced summary", len(batch.rows))
	}
	if got := batch.rows[0][1]; got != tenant {
		t.Fatalf("row tenant = %#v, want %v", got, tenant)
	}

	s := decodeAuditSummary(t, batch.rows[0])
	if s.AggregateCount != 2 {
		t.Errorf("AggregateCount = %d, want 2", s.AggregateCount)
	}
	if s.RequestCount != 5 || s.ErrorCount != 1 || s.TotalDurationMS != 15 || s.TotalComplexity != 3 {
		t.Errorf("counters = req%d err%d dur%d cx%d, want req5 err1 dur15 cx3", s.RequestCount, s.ErrorCount, s.TotalDurationMS, s.TotalComplexity)
	}
	if s.UserHashCount != 3 || s.TokenHashCount != 1 {
		t.Errorf("hash counts = user%d token%d, want user3 token1 (length, not contents)", s.UserHashCount, s.TokenHashCount)
	}
	if s.AuthTypes["jwt"] != 1 || s.AuthTypes["apikey"] != 1 {
		t.Errorf("AuthTypes = %#v, want {jwt:1, apikey:1}", s.AuthTypes)
	}
	if s.OperationTypes["query"] != 1 || s.OperationTypes["mutation"] != 1 {
		t.Errorf("OperationTypes = %#v, want {query:1, mutation:1}", s.OperationTypes)
	}
	if s.SourceNode != "node-a" {
		t.Errorf("SourceNode = %q, want node-a", s.SourceNode)
	}
}

// TestProcessServiceAPIRequestBatchAuditSeparatesTenants confirms distinct
// tenants get distinct summary rows, and that a classifier repeated within one
// tenant accumulates its occurrence count rather than collapsing to 1.
func TestProcessServiceAPIRequestBatchAuditSeparatesTenants(t *testing.T) {
	tenantA, tenantB := uuid.New(), uuid.New()
	batch := &captureBatch{}
	h := &AnalyticsHandler{clickhouse: &captureClickhouse{batch: batch}, logger: logging.NewLoggerWithService("test")}

	aggregates := []any{
		map[string]any{"tenant_id": tenantA.String(), "auth_type": "jwt", "operation_type": "query", "request_count": uint64(1)},
		map[string]any{"tenant_id": tenantA.String(), "auth_type": "jwt", "operation_type": "query", "request_count": uint64(1)},
		map[string]any{"tenant_id": tenantB.String(), "auth_type": "jwt", "operation_type": "query", "request_count": uint64(4)},
	}

	if err := h.processServiceAPIRequestBatchAudit(context.Background(), auditEvent(), aggregates, "node-a", time.Now()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(batch.rows) != 2 {
		t.Fatalf("got %d rows, want one per tenant", len(batch.rows))
	}
	byTenant := map[uuid.UUID]apiRequestBatchAuditSummary{}
	for _, row := range batch.rows {
		byTenant[row[1].(uuid.UUID)] = decodeAuditSummary(t, row)
	}
	if a := byTenant[tenantA]; a.AggregateCount != 2 || a.AuthTypes["jwt"] != 2 || a.RequestCount != 2 {
		t.Errorf("tenantA summary = %#v, want 2 aggregates, jwt:2, request 2", a)
	}
	if b := byTenant[tenantB]; b.AggregateCount != 1 || b.RequestCount != 4 {
		t.Errorf("tenantB summary = %#v, want 1 aggregate, request 4", b)
	}
}

// TestProcessServiceAPIRequestBatchAuditSkipsUnusable proves the guard: a batch
// of only un-attributable aggregates (non-map entries and invalid/missing
// tenant_ids) produces no summary and never opens or sends a ClickHouse batch.
func TestProcessServiceAPIRequestBatchAuditSkipsUnusable(t *testing.T) {
	batch := &captureBatch{}
	h := &AnalyticsHandler{clickhouse: &captureClickhouse{batch: batch}, logger: logging.NewLoggerWithService("test")}

	aggregates := []any{
		"not-a-map",
		map[string]any{"tenant_id": "not-a-uuid", "request_count": uint64(1)},
		map[string]any{"auth_type": "jwt", "request_count": uint64(1)}, // no tenant_id
	}

	if err := h.processServiceAPIRequestBatchAudit(context.Background(), auditEvent(), aggregates, "node-a", time.Now()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if batch.sendCalled {
		t.Fatal("no usable aggregates must not send a batch")
	}
	if len(batch.rows) != 0 {
		t.Fatalf("expected no rows, got %d", len(batch.rows))
	}
}

// TestAPIRequestBatchAuditSummaryAddIgnoresBlankClassifiers pins that empty
// auth_type / operation_type strings are not inserted into the cardinality maps
// (so a "" key can never pollute downstream group-bys), while the numeric
// counters still advance.
func TestAPIRequestBatchAuditSummaryAddIgnoresBlankClassifiers(t *testing.T) {
	s := newAPIRequestBatchAuditSummary("node-a")
	s.add(map[string]any{"request_count": uint64(7)}) // neither auth_type nor operation_type

	if s.AggregateCount != 1 || s.RequestCount != 7 {
		t.Errorf("counters = agg%d req%d, want agg1 req7", s.AggregateCount, s.RequestCount)
	}
	if len(s.AuthTypes) != 0 || len(s.OperationTypes) != 0 {
		t.Errorf("blank classifiers leaked: auth=%#v op=%#v", s.AuthTypes, s.OperationTypes)
	}
}
