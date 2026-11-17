package control

import (
	"context"
	"errors"
	"github.com/DATA-DOG/go-sqlmock"
	pb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"
	"testing"
)

type sourceLeaseProofClient struct {
	*fakeCommodore
	read func(context.Context, *pb.PublisherLeaseRequest) (*pb.PublisherLeaseResponse, error)
}

func (c *sourceLeaseProofClient) GetPublisherLease(ctx context.Context, req *pb.PublisherLeaseRequest) (*pb.PublisherLeaseResponse, error) {
	return c.read(ctx, req)
}

func TestPublisherSourceLeaseRequiresCurrentGlobalOwner(t *testing.T) {
	for _, scenario := range []string{"current owner", "replaced owner", "wrong epoch", "expired lease", "central unavailable", "session ended", "wrong local epoch"} {
		t.Run(scenario, func(t *testing.T) {
			conn, mock, err := sqlmock.New()
			if err != nil {
				t.Fatal(err)
			}
			oldDB, oldCell := db, localControlCellID
			SetDB(conn)
			localControlCellID = "cell"
			t.Cleanup(func() { SetDB(oldDB); localControlCellID = oldCell; _ = conn.Close() })
			rows := sqlmock.NewRows([]string{"stream_id", "start_trigger_uuid", "ingest_cluster_id", "publisher_lease_epoch"})
			if scenario != "session ended" {
				epoch := int64(7)
				if scenario == "wrong local epoch" {
					epoch = 6
				}
				rows.AddRow("stream-id", "execution", "media", epoch)
			}
			mock.ExpectQuery("FROM foghorn.ingest_sessions").WithArgs("tenant", "generation", "internal", "node").WillReturnRows(rows)
			calls := 0
			client := &sourceLeaseProofClient{fakeCommodore: &fakeCommodore{}, read: func(_ context.Context, req *pb.PublisherLeaseRequest) (*pb.PublisherLeaseResponse, error) {
				calls++
				if req.GetTenantId() != "tenant" || req.GetStreamId() != "stream-id" || req.GetNodeId() != "node" || req.GetControlCellId() != "cell" || req.GetClusterId() != "media" || req.GetExecutionToken() != "execution" {
					t.Fatalf("source verification lost exact ownership scope: %v", req)
				}
				response := &pb.PublisherLeaseResponse{Outcome: pb.PublisherLeaseOutcome_PUBLISHER_LEASE_OUTCOME_SAME_EXECUTION, LeaseEpoch: 7}
				switch scenario {
				case "replaced owner":
					response.Outcome = pb.PublisherLeaseOutcome_PUBLISHER_LEASE_OUTCOME_HELD_BY_OTHER
				case "wrong epoch":
					response.LeaseEpoch = 8
				case "expired lease":
					response.Outcome = pb.PublisherLeaseOutcome_PUBLISHER_LEASE_OUTCOME_UNSPECIFIED
				case "central unavailable":
					return nil, errors.New("unavailable")
				}
				return response, nil
			}}
			r := NewStreamRegistry(client, "cell", 0)
			err = r.VerifyPublisherSourceLease(t.Context(), "tenant", "internal", "node", "generation", 7)
			if (err == nil) != (scenario == "current owner") {
				t.Fatalf("source admission result: err=%v scenario=%s", err, scenario)
			}
			wantCalls := 1
			if scenario == "session ended" || scenario == "wrong local epoch" {
				wantCalls = 0
			}
			if calls != wantCalls {
				t.Fatalf("central verification calls=%d want=%d", calls, wantCalls)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}
