package grpc

import (
	"context"
	"database/sql"
	"strings"
	"testing"
	"time"

	"frameworks/api_tenants/internal/database/quartermasterdb"
	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	quartermasterpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/quartermaster"
	"github.com/sirupsen/logrus"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func controlCellServiceCtx() context.Context {
	return context.WithValue(context.Background(), ctxkeys.KeyAuthType, "service")
}

func TestControlCellReassignmentTimeoutBounds(t *testing.T) {
	cases := []struct {
		seconds int64
		want    time.Duration
		code    codes.Code
	}{
		{seconds: 0, want: defaultControlCellReassignmentTimeout},
		{seconds: 60, want: time.Minute},
		{seconds: 86400, want: 24 * time.Hour},
		{seconds: 59, code: codes.InvalidArgument},
		{seconds: 86401, code: codes.InvalidArgument},
		{seconds: -1, code: codes.InvalidArgument},
	}
	for _, tc := range cases {
		got, err := controlCellReassignmentTimeout(tc.seconds)
		if status.Code(err) != tc.code || got != tc.want {
			t.Fatalf("timeout(%d) = %v, %v; want %v, %v", tc.seconds, got, err, tc.want, tc.code)
		}
	}
}

func TestReassignClusterControlCellRequiresClusterAndTarget(t *testing.T) {
	server := NewQuartermasterServer(nil, logrus.New(), nil, nil, nil, nil, nil)
	for _, req := range []*quartermasterpb.ReassignClusterControlCellRequest{
		{TargetControlCellId: "cell-us"},
		{ClusterId: "private-eu"},
	} {
		if _, err := server.ReassignClusterControlCell(controlCellServiceCtx(), req); status.Code(err) != codes.InvalidArgument {
			t.Fatalf("ReassignClusterControlCell(%+v) = %v, want InvalidArgument", req, err)
		}
	}
}

func reassignmentRows(state, controlCell, previousCell string, started, deadline sql.NullTime) *sqlmock.Rows {
	return sqlmock.NewRows([]string{"cluster_id", "owner_tenant_id", "control_cell_id", "previous_control_cell_id", "reassignment_state", "reassignment_started_at", "reassignment_deadline_at", "reassignment_error"}).
		AddRow("private-eu", "tenant-1", controlCell, previousCell, state, started, deadline, "")
}

func TestReassignClusterControlCellRefusesOpenReassignment(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	server := NewQuartermasterServer(db, logrus.New(), nil, nil, nil, nil, nil)
	started := sql.NullTime{Time: time.Now(), Valid: true}
	mock.ExpectQuery("(?s)SELECT.*FROM quartermaster\\.infrastructure_clusters.*cluster_class = 'tenant_private'").
		WithArgs("private-eu").
		WillReturnRows(reassignmentRows("switching", "cell-us", "cell-eu", started, started))

	_, err = server.ReassignClusterControlCell(controlCellServiceCtx(), &quartermasterpb.ReassignClusterControlCellRequest{ClusterId: "private-eu", TargetControlCellId: "cell-ap"})
	if status.Code(err) != codes.FailedPrecondition || !strings.Contains(err.Error(), "already moving") {
		t.Fatalf("err = %v, want FailedPrecondition for an open reassignment", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestReassignClusterControlCellRefusesCellWithoutFoghorn(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	server := NewQuartermasterServer(db, logrus.New(), nil, nil, nil, nil, nil)
	mock.ExpectQuery("(?s)SELECT.*FROM quartermaster\\.infrastructure_clusters.*cluster_class = 'tenant_private'").
		WithArgs("private-eu").
		WillReturnRows(reassignmentRows("", "cell-eu", "", sql.NullTime{}, sql.NullTime{}))
	mock.ExpectQuery("(?s)SELECT EXISTS.*cell\\.cluster_class = 'platform_official'").
		WithArgs("cell-us").
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(false))

	_, err = server.ReassignClusterControlCell(controlCellServiceCtx(), &quartermasterpb.ReassignClusterControlCellRequest{ClusterId: "private-eu", TargetControlCellId: "cell-us"})
	if status.Code(err) != codes.FailedPrecondition || !strings.Contains(err.Error(), "running Foghorn") {
		t.Fatalf("err = %v, want FailedPrecondition for a cell without Foghorn", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func switchingReassignment(deadline time.Time) quartermasterdb.ClusterControlCellReassignment {
	return quartermasterdb.ClusterControlCellReassignment{
		ClusterID: "private-eu", OwnerTenantID: "tenant-1", ControlCellID: "cell-us", PreviousControlCellID: "cell-eu",
		State:      "switching",
		StartedAt:  sql.NullTime{Time: deadline.Add(-30 * time.Minute), Valid: true},
		DeadlineAt: sql.NullTime{Time: deadline, Valid: true},
	}
}

func TestAdvanceControlCellReassignmentWaitsForEdgesBeforeDeadline(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	server := NewQuartermasterServer(db, logrus.New(), nil, nil, nil, nil, nil)
	now := time.Now()
	mock.ExpectQuery("(?s)SELECT node_id.*FROM quartermaster\\.infrastructure_nodes").
		WithArgs("private-eu", "cell-us", controlCellObservationWindow.Seconds()).
		WillReturnRows(sqlmock.NewRows([]string{"node_id"}).AddRow("edge-2"))

	if err := server.advanceControlCellReassignment(context.Background(), switchingReassignment(now.Add(time.Minute)), now); err != nil {
		t.Fatal(err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestAdvanceControlCellReassignmentFailsPastDeadline(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	server := NewQuartermasterServer(db, logrus.New(), nil, nil, nil, nil, nil)
	now := time.Now()
	reassignment := switchingReassignment(now.Add(-time.Second))
	mock.ExpectQuery("(?s)SELECT node_id.*FROM quartermaster\\.infrastructure_nodes").
		WithArgs("private-eu", "cell-us", controlCellObservationWindow.Seconds()).
		WillReturnRows(sqlmock.NewRows([]string{"node_id"}).AddRow("edge-2"))
	mock.ExpectExec("(?s)UPDATE quartermaster\\.infrastructure_clusters.*reassignment_state = 'failed'").
		WithArgs("private-eu", reassignment.StartedAt.Time, "1 edge node(s) still observed by another control cell at the deadline: edge-2").
		WillReturnResult(sqlmock.NewResult(0, 1))

	if err := server.advanceControlCellReassignment(context.Background(), reassignment, now); err != nil {
		t.Fatal(err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestPendingControlCellNodesReasonCapsListedNodes(t *testing.T) {
	pending := make([]string, maxReportedPendingNodes+5)
	for i := range pending {
		pending[i] = "edge"
	}
	reason := pendingControlCellNodesReason(pending)
	if !strings.HasPrefix(reason, "25 edge node(s)") || strings.Count(reason, "edge,")+1 != maxReportedPendingNodes {
		t.Fatalf("reason = %q", reason)
	}
}
