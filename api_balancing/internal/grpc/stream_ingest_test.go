package grpc

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	pb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/foghorn_control"
	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestStreamIngestStatesUseSessionCatalog(t *testing.T) {
	server, mock := newLifecycleServer(t)
	tenant, live, connected, idle, never := uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()
	start := time.Now().Add(-time.Minute).UTC()
	end := start.Add(30 * time.Second)
	session := uuid.NewString()
	mock.ExpectQuery("GetStreamIngestStates").WithArgs(tenant, sqlmock.AnyArg()).WillReturnRows(sqlmock.NewRows([]string{"stream_id", "id", "started_at", "playable_at", "ended_at", "is_pull"}).AddRow(live, session, start, start, nil, false).AddRow(connected, uuid.NewString(), start, nil, nil, false).AddRow(idle, uuid.NewString(), start, start, end, false))
	ctx := context.WithValue(t.Context(), ctxkeys.KeyAuthType, "service")
	response, err := server.GetStreamIngestStates(ctx, &pb.GetStreamIngestStatesRequest{TenantId: tenant, StreamIds: []string{live, connected, idle, never}})
	if err != nil {
		t.Fatalf("catalog live-state RPC failed: %v", err)
	}
	if got := response.GetStates()[live]; got.GetState() != pb.StreamIngestState_STREAM_INGEST_STATE_LIVE || !got.GetIsLive() || got.GetSessionId() != session || !got.GetConnectedAt().AsTime().Equal(start) {
		t.Fatalf("live state = %v", got)
	}
	if got := response.GetStates()[connected]; got.GetState() != pb.StreamIngestState_STREAM_INGEST_STATE_CONNECTED || got.GetIsLive() {
		t.Fatalf("connected state = %v", got)
	}
	if got := response.GetStates()[idle]; got.GetState() != pb.StreamIngestState_STREAM_INGEST_STATE_IDLE || !got.GetResumableUntil().AsTime().Equal(end.Add(12*time.Second)) {
		t.Fatalf("ended state = %v", got)
	}
	if got := response.GetStates()[never]; got == nil || got.GetState() != pb.StreamIngestState_STREAM_INGEST_STATE_IDLE || got.GetSessionId() != "" {
		t.Fatalf("never-connected state = %v", got)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestStreamIngestStateDatabaseFailureIsUnavailable(t *testing.T) {
	server, mock := newLifecycleServer(t)
	mock.ExpectQuery("GetStreamIngestStates").WillReturnError(errors.New("database unavailable"))
	ctx := context.WithValue(t.Context(), ctxkeys.KeyAuthType, "service")
	_, err := server.GetStreamIngestStates(ctx, &pb.GetStreamIngestStatesRequest{TenantId: uuid.NewString(), StreamIds: []string{uuid.NewString()}})
	if status.Code(err) != codes.Unavailable {
		t.Fatalf("database outage = %v, want Unavailable", err)
	}
}
