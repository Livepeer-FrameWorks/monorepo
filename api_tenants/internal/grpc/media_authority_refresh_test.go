package grpc

import (
	"context"
	"testing"
	"time"

	"frameworks/api_tenants/internal/database/quartermasterdb"
	"github.com/DATA-DOG/go-sqlmock"
	commodorepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"
)

type blockingQuartermasterRefreshClient struct {
	arrived chan<- struct{}
	release <-chan struct{}
}

func (c blockingQuartermasterRefreshClient) RequestMediaAuthorityRefresh(context.Context, string, string, string, string) (*commodorepb.RequestMediaAuthorityRefreshResponse, error) {
	c.arrived <- struct{}{}
	<-c.release
	return &commodorepb.RequestMediaAuthorityRefreshResponse{Accepted: true}, nil
}

type acceptingQuartermasterRefreshClient struct{}

func (acceptingQuartermasterRefreshClient) RequestMediaAuthorityRefresh(context.Context, string, string, string, string) (*commodorepb.RequestMediaAuthorityRefreshResponse, error) {
	return &commodorepb.RequestMediaAuthorityRefreshResponse{Accepted: true}, nil
}

// A change that folds into a row while it is being delivered bumps the row's
// revision. The delivery of the older revision must leave the newer one
// pending, not complete it away.
func TestQuartermasterMediaAuthorityRefreshDoesNotCompleteANewerRevision(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	const id = "10000000-0000-0000-0000-000000000001"
	mock.ExpectExec(`(?s)SET status = 'completed'.*AND revision = \$2`).WithArgs(id, int64(2)).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(`(?s)SET status = 'pending', next_attempt_at = NOW\(\).*AND revision > \$2`).WithArgs(id, int64(2)).WillReturnResult(sqlmock.NewResult(0, 1))

	server := &QuartermasterServer{db: db, mediaAuthorityRefreshClient: acceptingQuartermasterRefreshClient{}}
	row := quartermasterdb.ClaimMediaAuthorityRefreshBatchRow{ID: id, SourceEventID: "event-1", TenantID: "tenant-1", Reason: "tenant_authority_changed", Attempts: 1, Revision: 2}
	if err := server.deliverMediaAuthorityRefreshRow(context.Background(), row); err != nil {
		t.Fatalf("superseded delivery must settle cleanly: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestQuartermasterMediaAuthorityRefreshBatchDeliversConcurrently(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	mock.MatchExpectationsInOrder(false)
	id1 := "10000000-0000-0000-0000-000000000001"
	id2 := "10000000-0000-0000-0000-000000000002"
	mock.ExpectQuery(`(?s)WITH candidates AS.*UPDATE quartermaster\.media_authority_refresh_outbox`).
		WithArgs(mediaAuthorityRefreshLease.Milliseconds(), mediaAuthorityRefreshBatchSize).
		WillReturnRows(sqlmock.NewRows([]string{"id", "source_event_id", "tenant_id", "reason", "attempts", "revision"}).
			AddRow(id1, "event-1", "tenant-1", "tenant_changed", 1, int64(1)).
			AddRow(id2, "event-2", "tenant-2", "tenant_changed", 1, int64(4)))
	mock.ExpectExec(`UPDATE quartermaster\.media_authority_refresh_outbox`).WithArgs(id1, int64(1)).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`UPDATE quartermaster\.media_authority_refresh_outbox`).WithArgs(id2, int64(4)).WillReturnResult(sqlmock.NewResult(0, 1))

	arrived := make(chan struct{}, 2)
	release := make(chan struct{})
	done := make(chan error, 1)
	server := &QuartermasterServer{db: db, mediaAuthorityRefreshClient: blockingQuartermasterRefreshClient{arrived: arrived, release: release}}
	go func() { done <- server.deliverMediaAuthorityRefreshBatch(context.Background()) }()
	for range 2 {
		select {
		case <-arrived:
		case <-time.After(time.Second):
			t.Fatal("refresh rows were not delivered concurrently")
		}
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
