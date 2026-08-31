package grpc

import (
	"context"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	commodorepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"
	"github.com/google/uuid"
)

type blockingPurserRefreshClient struct {
	arrived chan<- struct{}
	release <-chan struct{}
}

func (c blockingPurserRefreshClient) RequestMediaAuthorityRefresh(context.Context, string, string, string, string) (*commodorepb.RequestMediaAuthorityRefreshResponse, error) {
	c.arrived <- struct{}{}
	<-c.release
	return &commodorepb.RequestMediaAuthorityRefreshResponse{Accepted: true}, nil
}

func TestPurserMediaAuthorityRefreshBatchDeliversConcurrently(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	mock.MatchExpectationsInOrder(false)
	id1 := "10000000-0000-0000-0000-000000000001"
	id2 := "10000000-0000-0000-0000-000000000002"
	mock.ExpectQuery(`(?s)WITH candidates AS.*UPDATE purser\.media_authority_refresh_outbox`).
		WithArgs(mediaAuthorityRefreshLease.Milliseconds(), mediaAuthorityRefreshBatchSize).
		WillReturnRows(sqlmock.NewRows([]string{"id", "source_event_id", "tenant_id", "reason", "attempts", "revision"}).
			AddRow(id1, "event-1", "tenant-1", "billing_changed", 1, 1).
			AddRow(id2, "event-2", "tenant-2", "billing_changed", 1, 1))
	mock.ExpectExec(`UPDATE purser\.media_authority_refresh_outbox`).WithArgs(uuid.MustParse(id1), int64(1)).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`UPDATE purser\.media_authority_refresh_outbox`).WithArgs(uuid.MustParse(id2), int64(1)).WillReturnResult(sqlmock.NewResult(0, 1))

	arrived := make(chan struct{}, 2)
	release := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- (&PurserServer{db: db}).deliverMediaAuthorityRefreshBatch(context.Background(), blockingPurserRefreshClient{arrived: arrived, release: release})
	}()
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
