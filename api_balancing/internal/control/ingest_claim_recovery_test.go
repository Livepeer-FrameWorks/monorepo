package control

import (
	"context"
	"errors"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	pb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"
)

type heldRecoveryClaim struct{ released bool }

func (*heldRecoveryClaim) ValidateStreamKeyForClaim(context.Context, string, string, string) (*pb.ValidateStreamKeyResponse, error) {
	return &pb.ValidateStreamKeyResponse{HeldClaimToken: "held-execution", RejectionReason: pb.StreamKeyRejectionReason_STREAM_KEY_REJECTION_DUPLICATE_INGEST}, nil
}

func (c *heldRecoveryClaim) SyncActiveIngestPlacement(context.Context, string, []*pb.ActiveIngestStream, []*pb.ActiveIngestStream) (*pb.SyncActiveIngestPlacementResponse, error) {
	c.released = true
	return &pb.SyncActiveIngestPlacementResponse{}, nil
}

func TestClaimRecoveryDoesNotHideLocalDatabaseFailure(t *testing.T) {
	conn, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	previous := db
	SetDB(conn)
	t.Cleanup(func() { SetDB(previous) })
	cause := errors.New("local database unavailable")
	mock.ExpectQuery("FROM foghorn.ingest_sessions").WillReturnError(cause)
	client := &heldRecoveryClaim{}
	response, err := ClaimIngestPlacement(t.Context(), client, "credential", "tenant", "stream", "node", "cluster", "new-execution", logging.NewLogger())
	if !errors.Is(err, cause) {
		t.Fatalf("local recovery failure was hidden as an ordinary duplicate: response=%v error=%v", response, err)
	}
	if client.released || response.GetValid() {
		t.Fatal("unproven ownership allowed release or ingest")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestClaimRecoveryActiveOwnerNeedsNoEnrichmentOrRetirement(t *testing.T) {
	conn, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	previous := db
	SetDB(conn)
	t.Cleanup(func() { SetDB(previous) })
	mock.ExpectQuery("FROM foghorn.ingest_sessions").WillReturnRows(sqlmock.NewRows([]string{"ended", "active", "lapsed"}).AddRow(false, true, false))
	client := &heldRecoveryClaim{}
	response, err := ClaimIngestPlacement(t.Context(), client, "credential", "tenant", "stream", "node", "cluster", "new-execution", logging.NewLogger())
	if err != nil || response.GetValid() || client.released {
		t.Fatalf("live owner was disturbed: response=%v error=%v released=%v", response, err, client.released)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
