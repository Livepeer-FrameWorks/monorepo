package control

import (
	"context"
	"io"
	"testing"
	"time"

	"frameworks/api_balancing/internal/state"

	"github.com/DATA-DOG/go-sqlmock"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
	quartermasterpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/quartermaster"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
)

// stubFingerprintResolves makes the Connect handler resolve a node's canonical identity + tenant from its
// fingerprint, so registration reaches the POST-resolution ownership/marker acquisition with an authenticated
// identity (ownership is no longer acquired on the raw, self-asserted id).
func stubFingerprintResolves(t *testing.T, canonical, tenant string) {
	t.Helper()
	prev := resolveNodeFingerprintFn
	resolveNodeFingerprintFn = func(_ context.Context, _ *quartermasterpb.ResolveNodeFingerprintRequest) (*quartermasterpb.ResolveNodeFingerprintResponse, error) {
		return &quartermasterpb.ResolveNodeFingerprintResponse{CanonicalNodeId: canonical, TenantId: tenant}, nil
	}
	t.Cleanup(func() { resolveNodeFingerprintFn = prev })
}

// registerOnceStream is a fake HelmsmanControl_ConnectServer that yields a single Register message then
// EOF, enough to drive the Connect handler's registration prologue.
type registerOnceStream struct {
	ipcpb.HelmsmanControl_ConnectServer
	msgs []*ipcpb.ControlMessage
	idx  int
}

func (s *registerOnceStream) Recv() (*ipcpb.ControlMessage, error) {
	if s.idx >= len(s.msgs) {
		return nil, io.EOF
	}
	m := s.msgs[s.idx]
	s.idx++
	return m, nil
}

func (s *registerOnceStream) Send(*ipcpb.ControlMessage) error { return nil }
func (s *registerOnceStream) Context() context.Context         { return context.Background() }

// A registration that LOSES the fenced conn-owner CAS (a strictly-higher fence already owns the node on
// a peer) must be rejected AND must never appear in the dispatchable registry: ownership is won before
// the connection is published, so concurrent command dispatch can never select a superseded connection.
func TestConnectRejectsAndHidesConnectionWhenOwnershipLost(t *testing.T) {
	ensureRegistry(t)
	stubFingerprintResolves(t, "node-x", "tenant-x")

	store, _ := newTestStore(t)
	setCommandRelay(t, buildRelay(t, store, "inst-self", "10.0.0.1:9090", &mockRelayPool{}))

	// A peer with a HIGHER fence already owns the node in Redis.
	if acquired, err := store.AcquireConnOwnerFenced(context.Background(), "node-x", "inst-peer", "10.0.0.2:9090", 5); err != nil || !acquired {
		t.Fatalf("seed higher-fence owner: acquired=%v err=%v", acquired, err)
	}

	// Fence allocation issues this connection a LOWER fence (2) than the incumbent (5).
	mockDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	prevDB := db
	db = mockDB
	t.Cleanup(func() { db = prevDB; mockDB.Close() })
	mock.ExpectQuery(`SELECT nextval`).
		WillReturnRows(sqlmock.NewRows([]string{"nextval"}).AddRow(int64(2)))

	stream := &registerOnceStream{msgs: []*ipcpb.ControlMessage{
		{Payload: &ipcpb.ControlMessage_Register{Register: &ipcpb.Register{NodeId: "node-x"}}},
	}}

	srv := &Server{}
	if cerr := srv.Connect(stream); status.Code(cerr) != codes.Aborted {
		t.Fatalf("expected Aborted on lost ownership, got %v", cerr)
	}

	registry.mu.RLock()
	got := registry.conns["node-x"]
	registry.mu.RUnlock()
	if got != nil {
		t.Fatal("connection that lost the fenced ownership race must NOT be dispatch-visible in registry.conns")
	}
}

// A registration that WINS conn-owner ownership but then LOSES the fenced takeover-marker CAS (a
// strictly-higher fence won the node's artifact inventory on a peer in the window after acquisition)
// must be rejected with Aborted, must release the ownership it just acquired, and must never appear in
// the dispatchable registry — otherwise a superseded connection would cordon nothing yet still be
// routable.
func TestConnectRejectsWhenTakeoverMarkerSuperseded(t *testing.T) {
	ensureRegistry(t)
	stubFingerprintResolves(t, "node-y", "tenant-y")

	store, mr := newTestStore(t)
	setCommandRelay(t, buildRelay(t, store, "inst-self", "10.0.0.1:9090", &mockRelayPool{}))

	// The Connect handler's RecordNodeArtifactFence publishes through the state DefaultManager's Redis
	// tier: wire it to the SAME miniredis so the marker CAS sees the seeded higher watermark.
	sm := state.ResetDefaultManagerForTests()
	if err := sm.EnableRedisSync(context.Background(), store, "inst-self", logging.NewLogger()); err != nil {
		t.Fatalf("EnableRedisSync: %v", err)
	}
	t.Cleanup(func() { sm.Shutdown() })

	// A peer already advanced the shared artifact (fence, seq) watermark to a HIGHER fence (5) for this
	// node. conn_owner is left unseeded, so the LOWER-fence (2) acquisition below still WINS ownership —
	// isolating the marker-CAS loss.
	if err := mr.Set("{test-cluster}:artifacts_wm:node-y", "5-1"); err != nil {
		t.Fatalf("seed higher-fence artifact watermark: %v", err)
	}

	// Fence allocation issues this connection fence 2 (below the incumbent artifact fence 5).
	mockDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	prevDB := db
	db = mockDB
	t.Cleanup(func() { db = prevDB; mockDB.Close() })
	mock.ExpectQuery(`SELECT nextval`).
		WillReturnRows(sqlmock.NewRows([]string{"nextval"}).AddRow(int64(2)))

	stream := &registerOnceStream{msgs: []*ipcpb.ControlMessage{
		{Payload: &ipcpb.ControlMessage_Register{Register: &ipcpb.Register{NodeId: "node-y"}}},
	}}

	srv := &Server{}
	if cerr := srv.Connect(stream); status.Code(cerr) != codes.Aborted {
		t.Fatalf("expected Aborted on lost takeover marker, got %v", cerr)
	}

	// Not dispatch-visible.
	registry.mu.RLock()
	got := registry.conns["node-y"]
	registry.mu.RUnlock()
	if got != nil {
		t.Fatal("connection that lost the takeover-marker CAS must NOT be in registry.conns")
	}

	// Ownership released: the conn_owner acquired before the marker write must not linger.
	owner, err := store.GetConnOwner(context.Background(), "node-y")
	if err != nil {
		t.Fatalf("GetConnOwner: %v", err)
	}
	if owner.InstanceID != "" {
		t.Fatalf("ownership must be released after a superseded registration, still held by %q", owner.InstanceID)
	}

	// Inventory never became locally ready.
	snap := sm.GetAllNodesSnapshot()
	for _, n := range snap.Nodes {
		if n.NodeID == "node-y" && n.ArtifactInventoryReady {
			t.Fatal("a superseded registration must not lift the artifact readiness cordon")
		}
	}
}

// SECURITY (F1): a connection that asserts an EXISTING node's id but fails to authenticate (no fingerprint
// match, no enrollment token) must be rejected WITHOUT touching that node's ownership or artifact readiness —
// otherwise it could cordon/steal a live node by naming it. Ownership is acquired only post-resolution.
func TestConnectRejectedRegistrationDoesNotStealAssertedNodeOwnership(t *testing.T) {
	ensureRegistry(t)
	sm := state.ResetDefaultManagerForTests()
	t.Cleanup(func() { state.ResetDefaultManagerForTests() })
	// Fingerprint does NOT resolve (no match) and no enrollment token is provided → rejected before ownership.
	prevFP := resolveNodeFingerprintFn
	resolveNodeFingerprintFn = func(_ context.Context, _ *quartermasterpb.ResolveNodeFingerprintRequest) (*quartermasterpb.ResolveNodeFingerprintResponse, error) {
		return nil, nil
	}
	t.Cleanup(func() { resolveNodeFingerprintFn = prevFP })

	store, _ := newTestStore(t)
	setCommandRelay(t, buildRelay(t, store, "inst-self", "10.0.0.1:9090", &mockRelayPool{}))

	// The legitimate node "victim" is owned by its real connection (fence 3).
	if acquired, err := store.AcquireConnOwnerFenced(context.Background(), "victim", "inst-victim", "10.0.0.9:9090", 3); err != nil || !acquired {
		t.Fatalf("seed victim owner: %v", err)
	}

	// The attacker's connection would get a HIGHER fence (9) — under the old pre-resolution acquisition it
	// would steal ownership before enrollment even ran.
	mockDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	prevDB := db
	db = mockDB
	t.Cleanup(func() { db = prevDB; mockDB.Close() })
	mock.ExpectQuery(`SELECT nextval`).
		WillReturnRows(sqlmock.NewRows([]string{"nextval"}).AddRow(int64(9)))

	stream := &registerOnceStream{msgs: []*ipcpb.ControlMessage{
		{Payload: &ipcpb.ControlMessage_Register{Register: &ipcpb.Register{NodeId: "victim"}}},
	}}
	srv := &Server{}
	_ = srv.Connect(stream) // rejected with an ENROLLMENT_REQUIRED control error (returns nil), before ownership

	// The victim's ownership must be INTACT — the rejected registration never acquired it.
	owner, err := store.GetConnOwner(context.Background(), "victim")
	if err != nil {
		t.Fatalf("GetConnOwner: %v", err)
	}
	if owner.InstanceID != "inst-victim" {
		t.Fatalf("a rejected registration must NOT steal the asserted node's ownership; owner is now %q", owner.InstanceID)
	}

	// Nor may it create/mutate the asserted node's connection-info state (BinHost/liveness/routing/DNS): that
	// write is deferred to AFTER authentication, on the canonical id. A pre-resolution write would let an
	// unauthenticated connection perturb a victim node by naming it.
	snap := sm.GetAllNodesSnapshot()
	for _, n := range snap.Nodes {
		if n.NodeID == "victim" {
			t.Fatal("a rejected registration must not write node connection-info state for the asserted id")
		}
	}
}

// SECURITY (F1): a connection that AUTHENTICATES as canonical id A (via fingerprint) but ASSERTS a raw id B it
// does not own must be published ONLY under A — it must NOT retire/replace node B's live registry connection or
// mutate B's state. Only the canonical id is fenced; the raw asserted id is unverified.
func TestConnectAssertingForeignRawIDDoesNotHijackVictim(t *testing.T) {
	ensureRegistry(t)
	// The attacker's fingerprint resolves to its OWN canonical id, while it asserts the victim's raw id.
	stubFingerprintResolves(t, "attacker-A", "tenant-a")

	store, _ := newTestStore(t)
	setCommandRelay(t, buildRelay(t, store, "inst-self", "10.0.0.1:9090", &mockRelayPool{}))
	sm := state.ResetDefaultManagerForTests()
	if err := sm.EnableRedisSync(context.Background(), store, "inst-self", logging.NewLogger()); err != nil {
		t.Fatalf("EnableRedisSync: %v", err)
	}
	t.Cleanup(func() { sm.Shutdown() })

	// The VICTIM node "victim" has a live registered connection.
	victimStream := &captureStream{}
	cleanup := SetupTestRegistry("victim", victimStream)
	t.Cleanup(cleanup)

	// The attacker's connection is issued a fresh fence; its canonical id "attacker-A" is unowned, so its
	// registration SUCCEEDS — the test is that success under A does not touch B.
	mockDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	prevDB := db
	db = mockDB
	t.Cleanup(func() { db = prevDB; mockDB.Close() })
	mock.ExpectQuery(`SELECT nextval`).
		WillReturnRows(sqlmock.NewRows([]string{"nextval"}).AddRow(int64(7)))

	stream := &registerOnceStream{msgs: []*ipcpb.ControlMessage{
		{Payload: &ipcpb.ControlMessage_Register{Register: &ipcpb.Register{NodeId: "victim"}}},
	}}
	srv := &Server{}
	_ = srv.Connect(stream) // succeeds under canonical "attacker-A", then EOFs

	// The victim's connection MUST be intact — the attacker asserting "victim" never retired/replaced it.
	registry.mu.RLock()
	got := registry.conns["victim"]
	registry.mu.RUnlock()
	if got == nil || got.stream != victimStream {
		t.Fatal("a connection asserting a foreign raw id must NOT retire/replace the victim's registered connection")
	}
}

// SECURITY (F2): the ArtifactDeleted DB callback (which removes a node's artifact_nodes placement) must be
// bound to the AUTHENTICATED canonical session id, not the node-asserted payload node_id — otherwise a
// connection could evict a peer's placement by naming it.
func TestArtifactDeletedCallbackUsesAuthenticatedNodeID(t *testing.T) {
	ensureRegistry(t)
	stubFingerprintResolves(t, "node-real", "tenant-a")

	store, _ := newTestStore(t)
	setCommandRelay(t, buildRelay(t, store, "inst-self", "10.0.0.1:9090", &mockRelayPool{}))
	sm := state.ResetDefaultManagerForTests()
	if err := sm.EnableRedisSync(context.Background(), store, "inst-self", logging.NewLogger()); err != nil {
		t.Fatalf("EnableRedisSync: %v", err)
	}
	t.Cleanup(func() { sm.Shutdown() })

	got := make(chan string, 1)
	prevH := artifactDeletedHandler
	artifactDeletedHandler = func(_ context.Context, del *ipcpb.ArtifactDeleted) {
		select {
		case got <- del.GetNodeId():
		default:
		}
	}
	t.Cleanup(func() { artifactDeletedHandler = prevH })

	mockDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	prevDB := db
	db = mockDB
	t.Cleanup(func() { db = prevDB; mockDB.Close() })
	mock.ExpectQuery(`SELECT nextval`).WillReturnRows(sqlmock.NewRows([]string{"nextval"}).AddRow(int64(3)))

	stream := &registerOnceStream{msgs: []*ipcpb.ControlMessage{
		{Payload: &ipcpb.ControlMessage_Register{Register: &ipcpb.Register{NodeId: "node-real"}}},
		// A FORGED payload node_id — the callback must ignore it in favor of the authenticated session id.
		{Payload: &ipcpb.ControlMessage_ArtifactDeleted{ArtifactDeleted: &ipcpb.ArtifactDeleted{ArtifactHash: "h", NodeId: "victim-node", Reason: "evict"}}},
	}}
	srv := &Server{}
	_ = srv.Connect(stream)

	select {
	case id := <-got:
		if id != "node-real" {
			t.Fatalf("callback must receive the authenticated node id, got %q (a forged payload id must not reach the repository)", id)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("artifact-deleted callback was not invoked")
	}
}

func TestCleanupControlDisconnectDoesNotRemoveNewerLocalStream(t *testing.T) {
	currentStream := &captureStream{}
	staleStream := &captureStream{}
	cleanup := SetupTestRegistry("node-1", currentStream)
	t.Cleanup(cleanup)

	cleanupControlDisconnect("node-1", "", staleStream, logging.NewLogger())

	registry.mu.RLock()
	got := registry.conns["node-1"]
	registry.mu.RUnlock()
	if got == nil {
		t.Fatal("current stream was removed by stale cleanup")
	}
	if got.stream != currentStream {
		t.Fatal("stale cleanup replaced the current stream")
	}
}

func TestCleanupControlDisconnectRemovesOnlyCurrentStream(t *testing.T) {
	currentStream := &captureStream{}
	cleanup := SetupTestRegistry("node-1", currentStream)
	t.Cleanup(cleanup)

	cleanupControlDisconnect("node-1", "", currentStream, logging.NewLogger())

	registry.mu.RLock()
	got := registry.conns["node-1"]
	registry.mu.RUnlock()
	if got != nil {
		t.Fatal("current stream was not removed")
	}
}
