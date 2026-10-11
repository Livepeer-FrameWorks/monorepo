package grpc

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	quartermasterpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/quartermaster"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func membershipRows() *sqlmock.Rows {
	return sqlmock.NewRows([]string{"cluster_id", "is_active", "node_id", "admission_enabled", "observed_at"})
}

func TestPlacementMembershipAuthAndInput(t *testing.T) {
	server := &QuartermasterServer{}
	request := &quartermasterpb.GetPlacementMembershipRequest{ControlCellId: "cell"}
	if _, err := server.GetPlacementMembership(context.Background(), request); status.Code(err) != codes.PermissionDenied {
		t.Fatalf("public membership access = %v", err)
	}
	for _, cell := range []string{"", " cell", "cell\n"} {
		if _, err := server.GetPlacementMembership(serviceCtx(), &quartermasterpb.GetPlacementMembershipRequest{ControlCellId: cell}); status.Code(err) != codes.InvalidArgument {
			t.Fatalf("invalid cell %q = %v", cell, err)
		}
	}
}

func TestPlacementMembershipRevisionIgnoresReadInstant(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	now := time.Now().UTC()
	rows := func(at time.Time, admission bool) *sqlmock.Rows {
		return membershipRows().
			AddRow("empty", true, "", false, at).
			AddRow("own", true, "a", true, at).
			AddRow("own", true, "b", admission, at).
			AddRow("retired", false, "", false, at)
	}
	mock.ExpectQuery(`(?s)FROM quartermaster.infrastructure_clusters ic.*LEFT JOIN quartermaster.infrastructure_nodes n.*node_type = 'edge'.*control_cell_id.*LIMIT 8193`).
		WithArgs("cell").WillReturnRows(rows(now, true))
	mock.ExpectQuery("GetPlacementMembership").WithArgs("cell").WillReturnRows(rows(now.Add(time.Minute), true))
	mock.ExpectQuery("GetPlacementMembership").WithArgs("cell").WillReturnRows(rows(now, false))
	server := &QuartermasterServer{db: db}
	request := &quartermasterpb.GetPlacementMembershipRequest{ControlCellId: "cell"}
	first, err := server.GetPlacementMembership(serviceCtx(), request)
	if err != nil || !first.GetComplete() || len(first.GetClusters()) != 3 || len(first.GetNodes()) != 2 || first.GetClusters()[2].GetIsActive() || !first.GetObservedAt().AsTime().Equal(now) {
		t.Fatalf("membership = %+v, %v", first, err)
	}
	later, err := server.GetPlacementMembership(serviceCtx(), request)
	if err != nil || later.GetRevision() != first.GetRevision() {
		t.Fatalf("a later read of the same membership changed the revision: %v %v", later, err)
	}
	drained, err := server.GetPlacementMembership(serviceCtx(), request)
	if err != nil || drained.GetRevision() == first.GetRevision() {
		t.Fatalf("a drained node did not change the revision: %v %v", drained, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestPlacementMembershipCannotCertifyAmbiguousResults(t *testing.T) {
	now := time.Now().UTC()
	overflow := membershipRows()
	for index := range placementMembershipMaxNodes + 1 {
		overflow.AddRow("own", true, fmt.Sprintf("node-%d", index), true, now)
	}
	for _, tc := range []struct {
		name     string
		rows     *sqlmock.Rows
		queryErr error
		code     codes.Code
	}{
		{"duplicate_node", membershipRows().AddRow("a", true, "node", true, now).AddRow("b", true, "node", true, now), nil, codes.Internal},
		{"snapshot_skew", membershipRows().AddRow("a", true, "", false, now).AddRow("b", true, "node", true, now.Add(time.Second)), nil, codes.Internal},
		{"node_bound", overflow, nil, codes.ResourceExhausted},
		{"owner_unavailable", nil, errors.New("private database details"), codes.Unavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db, mock, err := sqlmock.New()
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = db.Close() })
			expected := mock.ExpectQuery("GetPlacementMembership")
			if tc.queryErr != nil {
				expected.WillReturnError(tc.queryErr)
			} else {
				expected.WillReturnRows(tc.rows)
			}
			result, err := (&QuartermasterServer{db: db}).GetPlacementMembership(serviceCtx(), &quartermasterpb.GetPlacementMembershipRequest{ControlCellId: "cell"})
			if result != nil || status.Code(err) != tc.code {
				t.Fatalf("ambiguous membership = %+v, %v", result, err)
			}
		})
	}
}

func TestPlacementMembershipEmptyCellIsComplete(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	mock.ExpectQuery("GetPlacementMembership").WillReturnRows(membershipRows())
	result, err := (&QuartermasterServer{db: db}).GetPlacementMembership(serviceCtx(), &quartermasterpb.GetPlacementMembershipRequest{ControlCellId: "cell"})
	if err != nil || !result.GetComplete() || result.GetRevision() == "" || len(result.GetClusters()) != 0 || result.GetObservedAt() == nil {
		t.Fatalf("empty membership = %+v, %v", result, err)
	}
}

func TestPlacementMembershipFingerprintIsTheDigest(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	mock.ExpectQuery(`(?s)SELECT md5\(format.*is_active.*infrastructure_clusters.*status = 'active'.*infrastructure_nodes.*node_type = 'edge'`).
		WillReturnRows(sqlmock.NewRows([]string{"fingerprint"}).AddRow("digest"))
	hub := newPeerWatchHub()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	hub.ctx = ctx
	_, wake, err := hub.subscribe()
	if err != nil {
		t.Fatal(err)
	}
	server := &QuartermasterServer{db: db, membershipWatch: hub}
	go server.RunPlacementMembershipWatcher(ctx, nil)
	deadline := time.Now().Add(2*peerChangeInterval + time.Second)
	for mock.ExpectationsWereMet() != nil {
		if time.Now().After(deadline) {
			t.Fatal("membership watcher never read the fingerprint")
		}
		time.Sleep(20 * time.Millisecond)
	}
	// The baseline read wakes subscribers once, so a change between their
	// initial view and the baseline still reaches them.
	select {
	case <-wake:
	case <-time.After(time.Second):
		t.Fatal("the baseline fingerprint read did not wake subscribers")
	}
}
