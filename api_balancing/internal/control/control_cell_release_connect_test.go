package control

import (
	"context"
	"testing"
	"time"

	"frameworks/api_balancing/internal/state"
	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
	quartermasterpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/quartermaster"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestConnectReleasesAndRefusesEdgesOfClusterMovedToAnotherCell(t *testing.T) {
	ensureRegistry(t)
	const nodeID = "node-control-cell-release"
	stubFingerprintResolves(t, nodeID, "tenant-a")

	store, _ := newTestStore(t)
	setCommandRelay(t, buildRelay(t, store, "inst-self", "10.0.0.1:9090", &mockRelayPool{}))
	sm := state.ResetDefaultManagerForTests()
	if err := sm.EnableRedisSync(context.Background(), store, "inst-self", logging.NewLogger()); err != nil {
		t.Fatalf("EnableRedisSync: %v", err)
	}
	t.Cleanup(func() { sm.Shutdown() })

	previousOwner := getNodeOwnerFn
	getNodeOwnerFn = func(context.Context, string) (*quartermasterpb.NodeOwnerResponse, error) {
		return &quartermasterpb.NodeOwnerResponse{ClusterId: "private-moved"}, nil
	}
	t.Cleanup(func() { getNodeOwnerFn = previousOwner })

	oldLocal := localClusterID
	oldServed := servedClusters.Load()
	oldReleased := releasedControlClusters.Load()
	localClusterID = "cell-eu"
	t.Cleanup(func() {
		localClusterID = oldLocal
		servedClusters.Store(oldServed)
		releasedControlClusters.Store(oldReleased)
	})
	applyServedClustersRefresh([]string{"private-moved"}, nil)

	mockDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	previousDB := db
	db = mockDB
	t.Cleanup(func() { db = previousDB; mockDB.Close() })
	mock.ExpectQuery(`INSERT INTO foghorn.node_control_fence_counter`).WithArgs(nodeID).
		WillReturnRows(sqlmock.NewRows([]string{"value"}).AddRow(int64(3)))
	mock.ExpectBegin()
	mock.ExpectQuery(`INSERT INTO foghorn.node_config_seeds`).
		WithArgs(nodeID).
		WillReturnRows(sqlmock.NewRows([]string{"version_counter"}).AddRow(int64(1)))
	mock.ExpectQuery(`SELECT COALESCE\(seed_version, 0\)::bigint AS seed_version, seed_payload`).
		WithArgs(nodeID).
		WillReturnRows(sqlmock.NewRows([]string{"seed_version", "seed_payload"}))
	mock.ExpectExec(`UPDATE foghorn.node_config_seeds`).
		WithArgs(int64(1), sqlmock.AnyArg(), nodeID).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	register := &ipcpb.ControlMessage{Payload: &ipcpb.ControlMessage_Register{Register: &ipcpb.Register{NodeId: nodeID, ControlProtocolVersion: MinControlProtocolVersion}}}
	idle := make(chan struct{})
	t.Cleanup(func() { close(idle) })
	stream := &registerOnceStream{msgs: []*ipcpb.ControlMessage{register}, afterMessages: idle}
	connectDone := make(chan error, 1)
	go func() { connectDone <- (&Server{}).Connect(stream) }()

	deadline := time.Now().Add(2 * time.Second)
	for {
		if _, ok := currentNodeSession(nodeID); ok {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("node did not register")
		}
		time.Sleep(time.Millisecond)
	}

	applyServedClustersRefresh(nil, []string{"private-moved"})
	select {
	case err := <-connectDone:
		if status.Code(err) != codes.Unavailable {
			t.Fatalf("Connect after release = %v, want Unavailable", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("released control stream stayed open")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}

	reconnect := &registerOnceStream{msgs: []*ipcpb.ControlMessage{register}}
	if err := (&Server{}).Connect(reconnect); status.Code(err) != codes.Unavailable {
		t.Fatalf("reconnect to the previous control cell = %v, want Unavailable", err)
	}
	if _, ok := currentNodeSession(nodeID); ok {
		t.Fatal("refused reconnect was registered")
	}
}
