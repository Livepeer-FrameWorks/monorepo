package grpc

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	foghornclient "github.com/Livepeer-FrameWorks/monorepo/pkg/clients/foghorn"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	foghornpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/foghorn"
	pb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/foghorn_control"
	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type ingestStateCell struct {
	foghornpb.UnimplementedTenantControlServiceServer
	response   *pb.GetStreamIngestStatesResponse
	err        error
	delayFirst *atomic.Int32
}

func (c *ingestStateCell) GetStreamIngestStates(ctx context.Context, req *pb.GetStreamIngestStatesRequest) (*pb.GetStreamIngestStatesResponse, error) {
	if c.delayFirst != nil {
		if c.delayFirst.Add(1) <= 8 {
			<-ctx.Done()
			return nil, ctx.Err()
		}
		response := &pb.GetStreamIngestStatesResponse{States: map[string]*pb.StreamIngest{}}
		for _, id := range req.GetStreamIds() {
			response.States[id] = &pb.StreamIngest{State: pb.StreamIngestState_STREAM_INGEST_STATE_LIVE, IsLive: true}
		}
		return response, nil
	}
	return c.response, c.err
}

func TestStreamIngestFanoutIsolatesFailedCell(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	logger := logging.NewLogger()
	pool := foghornclient.NewPool(foghornclient.PoolConfig{Logger: logger})
	t.Cleanup(func() { _ = pool.Close() })
	server := &CommodoreServer{db: db, logger: logger, foghornPool: pool}
	tenant, user, live, down, never := uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()
	mock.ExpectQuery("GetStreamIngestRoutes").WithArgs(tenant, user, sqlmock.AnyArg()).WillReturnRows(sqlmock.NewRows([]string{"id", "ingest_mode", "cluster_id"}).AddRow(live, "push", "healthy").AddRow(down, "push", "down").AddRow(never, "push", ""))
	healthy := startTenantControlTestServer(t, &ingestStateCell{response: &pb.GetStreamIngestStatesResponse{States: map[string]*pb.StreamIngest{live: {State: pb.StreamIngestState_STREAM_INGEST_STATE_LIVE, IsLive: true, SessionId: "session-1"}}}})
	failed := startTenantControlTestServer(t, &ingestStateCell{err: status.Error(codes.Unavailable, "cell down")})
	server.foghornDiscovery.Store("healthy", &foghornDiscoveryEntry{at: time.Now(), addrs: []string{healthy}})
	server.foghornDiscovery.Store("down", &foghornDiscoveryEntry{at: time.Now(), addrs: []string{failed}})
	ctx := context.WithValue(t.Context(), ctxkeys.KeyTenantID, tenant)
	ctx = context.WithValue(ctx, ctxkeys.KeyUserID, user)
	ctx = context.WithValue(ctx, ctxkeys.KeyAuthType, "api_token")
	ctx = context.WithValue(ctx, ctxkeys.KeyPermissions, []string{"streams:read"})
	response, err := server.GetStreamIngestStates(ctx, &pb.GetStreamIngestStatesRequest{StreamIds: []string{live, down, never}})
	if err != nil {
		t.Fatal(err)
	}
	if !response.GetStates()[live].GetIsLive() || response.GetStates()[never].GetState() != pb.StreamIngestState_STREAM_INGEST_STATE_IDLE || len(response.GetUnavailableStreamIds()) != 1 || response.GetUnavailableStreamIds()[0] != down {
		t.Fatalf("partial state result: %v", response)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestStreamIngestFanoutDoesNotStarveLaterCell(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	logger := logging.NewLogger()
	pool := foghornclient.NewPool(foghornclient.PoolConfig{Logger: logger})
	t.Cleanup(func() { _ = pool.Close() })
	server := &CommodoreServer{db: db, logger: logger, foghornPool: pool}
	var calls atomic.Int32
	addr := startTenantControlTestServer(t, &ingestStateCell{delayFirst: &calls})
	tenant, user := uuid.NewString(), uuid.NewString()
	rows := sqlmock.NewRows([]string{"id", "ingest_mode", "cluster_id"})
	ids := []string{}
	for i := range 9 {
		cluster := fmt.Sprintf("cell-%d", i)
		id := uuid.NewString()
		ids = append(ids, id)
		rows.AddRow(id, "push", cluster)
		server.foghornDiscovery.Store(cluster, &foghornDiscoveryEntry{at: time.Now(), addrs: []string{addr}})
	}
	mock.ExpectQuery("GetStreamIngestRoutes").WillReturnRows(rows)
	ctx := context.WithValue(t.Context(), ctxkeys.KeyTenantID, tenant)
	ctx = context.WithValue(ctx, ctxkeys.KeyUserID, user)
	ctx, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
	defer cancel()
	response, err := server.GetStreamIngestStates(ctx, &pb.GetStreamIngestStatesRequest{StreamIds: ids})
	if err != nil || len(response.GetStates()) != 1 || len(response.GetUnavailableStreamIds()) != 8 {
		t.Fatalf("eight hanging cells starved the later healthy cell: states=%d unavailable=%d err=%v", len(response.GetStates()), len(response.GetUnavailableStreamIds()), err)
	}
}
