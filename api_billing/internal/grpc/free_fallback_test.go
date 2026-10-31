package grpc

import (
	"database/sql/driver"
	"errors"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

func TestFreeFallbackObservationKeepsLastGoodValue(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	gauge := prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: "fallback_items"}, []string{"stage"})
	counter := prometheus.NewCounterVec(prometheus.CounterOpts{Name: "fallback_read_errors"}, nil)
	server := &PurserServer{db: db, logger: logging.NewLogger(), metrics: &ServerMetrics{FreeFallbackFailures: gauge, FreeFallbackReadFailures: counter}}
	gauge.WithLabelValues("move").Set(7)
	gauge.WithLabelValues("notice").Set(11)
	mock.ExpectQuery("CountRepeatedFreeFallbackFailures").WillReturnError(errors.New("database unavailable"))
	server.observeFreeFallbacks(t.Context())
	if got := testutil.ToFloat64(gauge.WithLabelValues("move")); got != 7 {
		t.Fatalf("move gauge after a failed read = %v, want 7", got)
	}
	if got := testutil.ToFloat64(gauge.WithLabelValues("notice")); got != 11 {
		t.Fatalf("notice gauge after a failed read = %v, want 11", got)
	}
	if got := testutil.ToFloat64(counter.WithLabelValues()); got != 1 {
		t.Fatalf("read failures = %v, want 1", got)
	}
	mock.ExpectQuery("CountRepeatedFreeFallbackFailures").WillReturnRows(sqlmock.NewRows([]string{"moves", "notices"}).AddRow(driver.Value(int64(0)), driver.Value(int64(0))))
	server.observeFreeFallbacks(t.Context())
	if got := testutil.ToFloat64(gauge.WithLabelValues("move")); got != 0 {
		t.Fatalf("resolved move gauge = %v, want 0", got)
	}
	if got := testutil.ToFloat64(gauge.WithLabelValues("notice")); got != 0 {
		t.Fatalf("resolved notice gauge = %v, want 0", got)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
