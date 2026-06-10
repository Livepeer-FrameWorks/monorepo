package handlers

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
)

// convertToEurCents multiplies USD cents by the ECB EUR/USD rate and rounds to
// the nearest integer cent. A fresh cache makes the rate deterministic (no
// network). Rounding is half-away-from-zero (Go math.Round).
func TestConvertToEurCents(t *testing.T) {
	h := &X402Handler{logger: logging.NewLogger()}

	cases := []struct {
		name     string
		rate     float64
		usdCents int64
		want     int64
	}{
		{"plain multiply", 0.92, 1000, 920},
		{"rounds half away from zero", 0.5, 3, 2},    // 1.5 -> 2
		{"rounds down below half", 0.9255, 200, 185}, // 185.1 -> 185
		{"zero amount", 1.0, 0, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			setECBCache(tc.rate, time.Now())
			t.Cleanup(resetECBCache)

			got, err := h.convertToEurCents(tc.usdCents)
			if err != nil {
				t.Fatalf("convertToEurCents: %v", err)
			}
			if got != tc.want {
				t.Fatalf("convertToEurCents(%d) @ %.4f = %d, want %d", tc.usdCents, tc.rate, got, tc.want)
			}
		})
	}
}

// When the rate is uncached and the fetch fails with no stale fallback,
// convertToEurCents must surface the error rather than silently returning 0
// cents — a zero conversion would under-bill the tenant.
func TestConvertToEurCentsPropagatesRateError(t *testing.T) {
	resetECBCache()
	t.Cleanup(resetECBCache)
	withDefaultHTTPClient(t, &http.Client{
		Transport: testRoundTripFunc(func(*http.Request) (*http.Response, error) {
			return nil, errors.New("network down")
		}),
	})

	h := &X402Handler{logger: logging.NewLogger()}
	if _, err := h.convertToEurCents(1000); err == nil {
		t.Fatal("expected error when rate is unavailable, got nil")
	}
}

func newCodecJM(t *testing.T) (*JobManager, sqlmock.Sqlmock) {
	t.Helper()
	mockDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	t.Cleanup(func() { _ = mockDB.Close() })
	return &JobManager{db: mockDB, logger: logging.NewLogger(), billing: &Service{}}, mock
}

// collectInvoiceCodecBreakdowns folds the (cluster, meter, key, seconds) rows
// into a nested map. Duplicate keys are summed (defensive against any
// pre-aggregation gap), and distinct clusters/meters/keys stay separate.
func TestCollectInvoiceCodecBreakdownsBuildsNestedMap(t *testing.T) {
	jm, mock := newCodecJM(t)
	ps := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	pe := ps.AddDate(0, 1, 0)

	rows := sqlmock.NewRows([]string{"cluster_id", "usage_type", "key", "seconds"}).
		AddRow("cluster-a", "media_seconds", "h264", float64(100)).
		AddRow("cluster-a", "media_seconds", "h264", float64(50)). // dup → summed
		AddRow("cluster-a", "media_seconds", "h265", float64(20)).
		AddRow("cluster-b", "media_seconds", "Livepeer:h264", float64(7))

	mock.ExpectQuery(`usage_codec_rows`).
		WithArgs("tenant-1", ps, pe).
		WillReturnRows(rows)

	got, err := jm.collectInvoiceCodecBreakdowns(context.Background(), "tenant-1", ps, pe)
	if err != nil {
		t.Fatalf("collectInvoiceCodecBreakdowns: %v", err)
	}
	if got["cluster-a"]["media_seconds"]["h264"] != 150 {
		t.Fatalf("h264 = %v, want 150 (100+50 summed)", got["cluster-a"]["media_seconds"]["h264"])
	}
	if got["cluster-a"]["media_seconds"]["h265"] != 20 {
		t.Fatalf("h265 = %v, want 20", got["cluster-a"]["media_seconds"]["h265"])
	}
	if got["cluster-b"]["media_seconds"]["Livepeer:h264"] != 7 {
		t.Fatalf("joint key = %v, want 7", got["cluster-b"]["media_seconds"]["Livepeer:h264"])
	}
}

func TestCollectInvoiceCodecBreakdownsQueryError(t *testing.T) {
	jm, mock := newCodecJM(t)
	ps := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	pe := ps.AddDate(0, 1, 0)
	mock.ExpectQuery(`usage_codec_rows`).
		WillReturnError(errors.New("boom"))

	if _, err := jm.collectInvoiceCodecBreakdowns(context.Background(), "tenant-1", ps, pe); err == nil {
		t.Fatal("expected query error, got nil")
	}
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
