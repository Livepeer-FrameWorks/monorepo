package grpc

import (
	"context"
	"errors"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	pb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/foghorn_control"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestClaimedStreamStopRefusesUnfencedScope(t *testing.T) {
	server, _ := newLifecycleServer(t)
	_, err := server.TerminateClaimedStreamSessions(context.WithValue(context.Background(), ctxkeys.KeyAuthType, "service"), &pb.TerminateStreamSessionsRequest{TenantId: "10000000-0000-0000-0000-000000000001", StreamId: "20000000-0000-0000-0000-000000000001", StreamNames: []string{"stream"}})
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("claim-fenced stop accepted missing claim: %v", err)
	}
}

func TestClaimedStreamStopSettlesMissingOldClaim(t *testing.T) {
	server, mock := newLifecycleServer(t)
	mock.ExpectQuery("ListClaimedIngestStopTargets").WillReturnRows(sqlmock.NewRows([]string{"id"}))
	response, err := server.TerminateClaimedStreamSessions(context.WithValue(context.Background(), ctxkeys.KeyAuthType, "service"), &pb.TerminateStreamSessionsRequest{TenantId: "10000000-0000-0000-0000-000000000001", StreamId: "20000000-0000-0000-0000-000000000001", StreamNames: []string{"stream"}, ClaimFences: map[string]string{"stream": "old-claim"}})
	if err != nil || response.GetSessionsTerminated() != 0 {
		t.Fatalf("ended/replaced claim did not settle without touching its replacement: %v %v", response, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestClaimedStreamStopPersistsRequestBeforeEdgeCommand(t *testing.T) {
	server, mock := newLifecycleServer(t)
	mock.ExpectQuery("ListClaimedIngestStopTargets").WillReturnRows(sqlmock.NewRows([]string{"session_id", "node_id", "connector_pid", "runtime_name", "ended_at", "ended_reason", "ingest_disconnect_requested_at", "ingest_disconnected_at"}).AddRow("30000000-0000-0000-0000-000000000001", "node", int64(42), "live+stream", nil, "", nil, nil))
	mock.ExpectExec("MarkIngestDisconnectRequested").WillReturnError(errors.New("request fence unavailable"))
	_, err := server.TerminateClaimedStreamSessions(context.WithValue(t.Context(), ctxkeys.KeyAuthType, "service"), &pb.TerminateStreamSessionsRequest{TenantId: "10000000-0000-0000-0000-000000000001", StreamId: "20000000-0000-0000-0000-000000000001", StreamNames: []string{"stream"}, ClaimFences: map[string]string{"stream": "old-claim"}})
	if status.Code(err) != codes.Unavailable {
		t.Fatalf("failed durable fence must remain pending: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("edge command preceded durable execution fence: %v", err)
	}
}
