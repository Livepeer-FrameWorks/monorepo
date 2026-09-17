package grpc

import (
	"context"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/sirupsen/logrus"
)

func TestAdvanceControlCellReassignmentsPublishesStateCounts(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	server := NewQuartermasterServer(db, logrus.New(), nil, nil, nil, nil, nil)
	gauge := prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: "test_control_cell_reassignments"}, []string{"state"})
	server.metrics = &ServerMetrics{ControlCellReassignments: gauge}
	gauge.WithLabelValues("switching").Set(3)

	mock.ExpectQuery("(?s)SELECT.*FROM quartermaster\\.infrastructure_clusters.*reassignment_state = 'switching'").
		WillReturnRows(sqlmock.NewRows([]string{"cluster_id", "owner_tenant_id", "control_cell_id", "previous_control_cell_id", "reassignment_state", "reassignment_started_at", "reassignment_deadline_at", "reassignment_error"}))
	mock.ExpectQuery("(?s)SELECT reassignment_state, COUNT\\(\\*\\).*GROUP BY reassignment_state").
		WillReturnRows(sqlmock.NewRows([]string{"reassignment_state", "count"}).AddRow("failed", int64(2)))

	server.advanceControlCellReassignmentsOnce(context.Background())

	if got := testutil.ToFloat64(gauge.WithLabelValues("failed")); got != 2 {
		t.Fatalf("failed reassignments gauge = %v, want 2", got)
	}
	if got := testutil.ToFloat64(gauge.WithLabelValues("switching")); got != 0 {
		t.Fatalf("switching reassignments gauge = %v, want 0 after the state emptied", got)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
