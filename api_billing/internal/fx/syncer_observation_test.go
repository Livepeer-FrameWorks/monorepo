package fx

import (
	"errors"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

func TestReferenceAgeDoesNotClaimFreshDataAfterReadFailure(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	gauge := prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: "test_fx_reference_age"}, []string{"currency"})
	gauge.WithLabelValues(USD).Set(3600)
	mock.ExpectQuery("SELECT").WillReturnError(errors.New("database unavailable"))
	if err := NewSyncer(db, logging.NewLogger(), nil, gauge).RefreshAge(t.Context()); err == nil {
		t.Fatal("database read failure hidden")
	}
	if got := testutil.CollectAndCount(gauge); got != 0 {
		t.Fatalf("frozen fresh series suppresses missing-rate alert: %d", got)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
