package grpc

import (
	"context"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	foghornclient "github.com/Livepeer-FrameWorks/monorepo/pkg/clients/foghorn"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	pb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/foghorn_control"
	quartermasterpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/quartermaster"
	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// A Foghorn answers IDLE for every requested stream it holds no session for, so for a pull,
// whose serving cell Commodore does not know, a bystander cell's IDLE is not an answer.
func pullIngestStates(t *testing.T, cellA, cellB *ingestStateCell) (*pb.GetStreamIngestStatesResponse, string) {
	t.Helper()
	return pullIngestStatesWithInventory(t, true, cellA, cellB)
}

func pullIngestStatesWithInventory(t *testing.T, complete bool, cellA, cellB *ingestStateCell) (*pb.GetStreamIngestStatesResponse, string) {
	t.Helper()
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	logger := logging.NewLogger()
	pool := foghornclient.NewPool(foghornclient.PoolConfig{Logger: logger})
	t.Cleanup(func() { _ = pool.Close() })
	server := &CommodoreServer{db: db, logger: logger, foghornPool: pool}
	server.controlRecipientLookup = func(context.Context, *quartermasterpb.GetTenantControlRecipientsRequest) (*quartermasterpb.GetTenantControlRecipientsResponse, error) {
		return &quartermasterpb.GetTenantControlRecipientsResponse{Complete: complete, Recipients: []*quartermasterpb.TenantControlRecipient{{CellId: "cell-a"}, {CellId: "cell-b"}}}, nil
	}
	server.foghornDiscovery.Store("cell-a", &foghornDiscoveryEntry{at: time.Now(), addrs: []string{startTenantControlTestServer(t, cellA)}})
	server.foghornDiscovery.Store("cell-b", &foghornDiscoveryEntry{at: time.Now(), addrs: []string{startTenantControlTestServer(t, cellB)}})
	tenant, user, pull := uuid.NewString(), uuid.NewString(), uuid.NewString()
	mock.ExpectQuery("GetStreamIngestRoutes").WithArgs(tenant, user, sqlmock.AnyArg()).WillReturnRows(sqlmock.NewRows([]string{"id", "ingest_mode", "cluster_id"}).AddRow(pull, "pull", "cell-b"))
	mock.ExpectQuery("ListAccountIngestCells").WithArgs(tenant, "").WillReturnRows(sqlmock.NewRows([]string{"cell_id"}).AddRow("cell-b"))
	ctx := context.WithValue(t.Context(), ctxkeys.KeyTenantID, tenant)
	ctx = context.WithValue(ctx, ctxkeys.KeyUserID, user)
	ctx = context.WithValue(ctx, ctxkeys.KeyAuthType, "api_token")
	ctx = context.WithValue(ctx, ctxkeys.KeyPermissions, []string{"streams:read"})
	response, err := server.GetStreamIngestStates(ctx, &pb.GetStreamIngestStatesRequest{StreamIds: []string{pull}})
	if err != nil {
		t.Fatal(err)
	}
	return response, pull
}

func idleDefault() *ingestStateCell {
	return &ingestStateCell{idleDefault: true}
}

func TestPullIngestStateIsUnavailableWhileAnyCellIsUnanswered(t *testing.T) {
	response, pull := pullIngestStates(t, idleDefault(), &ingestStateCell{err: status.Error(codes.Unavailable, "serving cell down")})
	if state := response.GetStates()[pull]; state != nil {
		t.Fatalf("a bystander cell's IDLE stood in for the unanswered serving cell: %v", state)
	}
	if got := response.GetUnavailableStreamIds(); len(got) != 1 || got[0] != pull {
		t.Fatalf("unanswered pull not reported unavailable: %v", got)
	}
}

func TestPullIngestStateTakesTheServingCellSession(t *testing.T) {
	live := &pb.StreamIngest{State: pb.StreamIngestState_STREAM_INGEST_STATE_LIVE, IsLive: true, SessionId: "cell-session"}
	response, pull := pullIngestStates(t, idleDefault(), &ingestStateCell{session: live})
	if state := response.GetStates()[pull]; state.GetSessionId() != "cell-session" || !state.GetIsLive() || len(response.GetUnavailableStreamIds()) != 0 {
		t.Fatalf("serving cell's live session not exposed: %v unavailable=%v", state, response.GetUnavailableStreamIds())
	}
}

func TestPullIngestSessionFoundStandsWhileAnotherCellIsUnanswered(t *testing.T) {
	ended := &pb.StreamIngest{State: pb.StreamIngestState_STREAM_INGEST_STATE_IDLE, SessionId: "cell-session"}
	response, pull := pullIngestStates(t, &ingestStateCell{err: status.Error(codes.Unavailable, "bystander down")}, &ingestStateCell{session: ended})
	if state := response.GetStates()[pull]; state.GetSessionId() != "cell-session" || len(response.GetUnavailableStreamIds()) != 0 {
		t.Fatalf("the cell's ended session was not exposed: %v unavailable=%v", state, response.GetUnavailableStreamIds())
	}
}

func TestPullIngestStateIsUnavailableWhenTheCellInventoryIsIncomplete(t *testing.T) {
	response, pull := pullIngestStatesWithInventory(t, false, idleDefault(), idleDefault())
	if state := response.GetStates()[pull]; state != nil || len(response.GetUnavailableStreamIds()) != 1 {
		t.Fatalf("an incomplete cell inventory answered IDLE for a pull: %v unavailable=%v", state, response.GetUnavailableStreamIds())
	}
}

func TestPullIngestStateIsIdleWhenEveryCellAnswersWithoutASession(t *testing.T) {
	response, pull := pullIngestStates(t, idleDefault(), idleDefault())
	if state := response.GetStates()[pull]; state.GetState() != pb.StreamIngestState_STREAM_INGEST_STATE_IDLE || state.GetSessionId() != "" || len(response.GetUnavailableStreamIds()) != 0 {
		t.Fatalf("a never-started pull is not IDLE: %v unavailable=%v", state, response.GetUnavailableStreamIds())
	}
}
