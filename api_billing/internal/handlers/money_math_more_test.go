package handlers

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
)

func newCodecJM(t *testing.T) (*JobManager, sqlmock.Sqlmock) {
	t.Helper()
	mockDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	t.Cleanup(func() { _ = mockDB.Close() })
	return &JobManager{db: mockDB, logger: logging.NewLogger(), billing: &Service{}}, mock
}

// collectInvoiceUsage unions usage_records + usage_adjustments and groups per
// (cluster, meter). Assert the per-cluster nested map and the query-error path.
func TestCollectInvoiceUsageMapsRowsAndError(t *testing.T) {
	t.Run("maps rows", func(t *testing.T) {
		jm, mock := newCodecJM(t)
		ps := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
		pe := ps.AddDate(0, 1, 0)
		mock.ExpectQuery(`FROM purser\.usage_records`).
			WithArgs("tenant-1", ps, pe).
			WillReturnRows(sqlmock.NewRows([]string{"cluster_id", "usage_type", "aggregated_value"}).
				AddRow("cluster-a", "egress_bytes", float64(1024)).
				AddRow("", "media_seconds", float64(33)))

		got, err := jm.collectInvoiceUsage(context.Background(), "tenant-1", ps, pe)
		if err != nil {
			t.Fatalf("collectInvoiceUsage: %v", err)
		}
		if got["cluster-a"]["egress_bytes"] != 1024 || got[""]["media_seconds"] != 33 {
			t.Fatalf("usage map wrong: %+v", got)
		}
	})

	t.Run("query error", func(t *testing.T) {
		jm, mock := newCodecJM(t)
		ps := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
		pe := ps.AddDate(0, 1, 0)
		mock.ExpectQuery(`FROM purser\.usage_records`).
			WillReturnError(errors.New("db down"))
		if _, err := jm.collectInvoiceUsage(context.Background(), "tenant-1", ps, pe); err == nil {
			t.Fatal("expected query error, got nil")
		}
	})
}
