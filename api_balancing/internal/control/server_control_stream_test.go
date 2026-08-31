package control

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"frameworks/api_balancing/internal/state"

	"github.com/DATA-DOG/go-sqlmock"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
	quartermasterpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/quartermaster"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/nodeidentity"
)

var controlStreamTestPrivateKey = ed25519.NewKeyFromSeed(bytes.Repeat([]byte{0x51}, ed25519.SeedSize))

// stubFingerprintResolves makes the Connect handler resolve a node's canonical identity + tenant from its
// fingerprint, so registration reaches the POST-resolution ownership/marker acquisition with an authenticated
// identity (ownership is no longer acquired on the raw, self-asserted id).
func stubFingerprintResolves(t *testing.T, canonical, tenant string) {
	t.Helper()
	prev := resolveNodeFingerprintFn
	resolveNodeFingerprintFn = func(_ context.Context, req *quartermasterpb.ResolveNodeFingerprintRequest) (*quartermasterpb.ResolveNodeFingerprintResponse, error) {
		wantKey := controlStreamTestPrivateKey.Public().(ed25519.PublicKey)
		if !bytes.Equal(req.GetNodeIdentityPublicKeyEd25519(), wantKey) {
			t.Fatalf("fingerprint resolution omitted the locally verified node identity key")
		}
		return &quartermasterpb.ResolveNodeFingerprintResponse{
			CanonicalNodeId: canonical, TenantId: tenant,
			NodeIdentityPublicKeyEd25519: wantKey,
		}, nil
	}
	previousConsume := consumeNodeIdentityProofFn
	consumeNodeIdentityProofFn = func(context.Context, *ipcpb.Register) error { return nil }
	t.Cleanup(func() {
		resolveNodeFingerprintFn = prev
		consumeNodeIdentityProofFn = previousConsume
	})
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
	if register := m.GetRegister(); register != nil && register.GetControlProtocolVersion() >= MinControlProtocolVersion && len(register.GetNodeIdentityProofEd25519()) == 0 {
		if register.GetFingerprint() == nil {
			machineID := strings.Repeat("a", 64)
			register.Fingerprint = &ipcpb.NodeFingerprint{MachineIdSha256: &machineID}
		}
		if err := nodeidentity.SignRegistration(register, controlStreamTestPrivateKey, time.Now()); err != nil {
			return nil, err
		}
	}
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
	mock.ExpectQuery(`INSERT INTO foghorn.node_control_fence_counter`).WithArgs("node-x").
		WillReturnRows(sqlmock.NewRows([]string{"value"}).AddRow(int64(2)))

	stream := &registerOnceStream{msgs: []*ipcpb.ControlMessage{
		{Payload: &ipcpb.ControlMessage_Register{Register: &ipcpb.Register{NodeId: "node-x", ControlProtocolVersion: MinControlProtocolVersion}}},
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

// A sidecar declaring a control-protocol version BELOW the minimum is rejected at registration with
// FailedPrecondition at the EARLIEST point — before fence allocation, fingerprint authentication, ownership
// acquisition, or registry publication. It mutates no state: the fingerprint resolver fatals if called, and the DB
// mock expects no query, so a fence allocation (SELECT nextval) would surface as Unavailable, not FailedPrecondition.
func TestConnectRejectsSubMinimumProtocolBeforeAnyMutation(t *testing.T) {
	ensureRegistry(t)

	// Authentication (fingerprint resolution) must NOT be reached.
	prevResolve := resolveNodeFingerprintFn
	resolveNodeFingerprintFn = func(context.Context, *quartermasterpb.ResolveNodeFingerprintRequest) (*quartermasterpb.ResolveNodeFingerprintResponse, error) {
		t.Fatal("fingerprint resolution must not run for a sub-minimum registration")
		return nil, nil
	}
	t.Cleanup(func() { resolveNodeFingerprintFn = prevResolve })

	// No DB mutation (e.g. the fence-allocation nextval) must run: the mock expects no query.
	mockDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	prevDB := db
	db = mockDB
	t.Cleanup(func() { db = prevDB; mockDB.Close() })

	stream := &registerOnceStream{msgs: []*ipcpb.ControlMessage{
		{Payload: &ipcpb.ControlMessage_Register{Register: &ipcpb.Register{NodeId: "node-old", ControlProtocolVersion: MinControlProtocolVersion - 1}}},
	}}

	srv := &Server{}
	if cerr := srv.Connect(stream); status.Code(cerr) != codes.FailedPrecondition {
		t.Fatalf("expected FailedPrecondition for a sub-minimum protocol, got %v", cerr)
	}

	registry.mu.RLock()
	got := registry.conns["node-old"]
	registry.mu.RUnlock()
	if got != nil {
		t.Fatal("a rejected sub-minimum registration must never be published to registry.conns")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("no DB mutation expected for a sub-minimum registration: %v", err)
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
	mock.ExpectQuery(`INSERT INTO foghorn.node_control_fence_counter`).WithArgs("node-y").
		WillReturnRows(sqlmock.NewRows([]string{"value"}).AddRow(int64(2)))

	stream := &registerOnceStream{msgs: []*ipcpb.ControlMessage{
		{Payload: &ipcpb.ControlMessage_Register{Register: &ipcpb.Register{NodeId: "node-y", ControlProtocolVersion: MinControlProtocolVersion}}},
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

// SECURITY: a connection that asserts an EXISTING node's id but fails to authenticate (no fingerprint
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

	// The attacker's connection carries a HIGHER fence (9); ownership is acquired only AFTER identity resolution and
	// enrollment, so a higher fence cannot steal ownership ahead of authentication.
	mockDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	prevDB := db
	db = mockDB
	t.Cleanup(func() { db = prevDB; mockDB.Close() })
	mock.ExpectQuery(`INSERT INTO foghorn.node_control_fence_counter`).WithArgs("victim").
		WillReturnRows(sqlmock.NewRows([]string{"value"}).AddRow(int64(9)))

	stream := &registerOnceStream{msgs: []*ipcpb.ControlMessage{
		{Payload: &ipcpb.ControlMessage_Register{Register: &ipcpb.Register{NodeId: "victim", ControlProtocolVersion: MinControlProtocolVersion}}},
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

// SECURITY: a connection that AUTHENTICATES as canonical id A (via fingerprint) but ASSERTS a raw id B it
// does not own must be published ONLY under A — it must NOT retire/replace node B's live registry connection or
// mutate B's state. Only the canonical id is fenced; the raw asserted id is unverified.
func TestConnectAssertingForeignRawIDDoesNotHijackVictim(t *testing.T) {
	ensureRegistry(t)
	// The attacker's fingerprint resolves to its OWN canonical id, while it asserts the victim's raw id.
	stubFingerprintResolves(t, "attacker-A", "tenant-a")
	// This security regression test must exercise the real proof verifier and
	// replay fence. The shared helper stubs persistence for tests whose subject
	// is later in registration, but doing so here would bypass the identity
	// property this test claims to prove.
	consumeNodeIdentityProofFn = consumeNodeIdentityProof

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
	mock.ExpectExec(`INSERT INTO foghorn.node_admission_proof_nonces`).
		WithArgs(sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`DELETE FROM foghorn.node_admission_proof_nonces`).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery(`INSERT INTO foghorn.node_control_fence_counter`).WithArgs("attacker-A").
		WillReturnRows(sqlmock.NewRows([]string{"value"}).AddRow(int64(7)))
	mock.ExpectBegin()
	mock.ExpectQuery(`INSERT INTO foghorn.node_config_seeds`).
		WithArgs("attacker-A").
		WillReturnRows(sqlmock.NewRows([]string{"version_counter"}).AddRow(int64(1)))
	mock.ExpectQuery(`SELECT COALESCE\(seed_version, 0\)::bigint AS seed_version, seed_payload`).
		WithArgs("attacker-A").
		WillReturnRows(sqlmock.NewRows([]string{"seed_version", "seed_payload"}))
	mock.ExpectExec(`UPDATE foghorn.node_config_seeds`).
		WithArgs(int64(1), sqlmock.AnyArg(), "attacker-A").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	stream := &registerOnceStream{msgs: []*ipcpb.ControlMessage{
		{Payload: &ipcpb.ControlMessage_Register{Register: &ipcpb.Register{NodeId: "victim", ControlProtocolVersion: MinControlProtocolVersion}}},
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
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("registration did not exercise the authenticated canonical path: %v", err)
	}
}

// SECURITY: the ArtifactDeleted DB callback (which removes a node's artifact_nodes placement) must be
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

	got := make(chan *ipcpb.ArtifactDeleted, 1)
	prevH := artifactDeletedHandler
	artifactDeletedHandler = func(_ context.Context, del *ipcpb.ArtifactDeleted) {
		select {
		case got <- del:
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
	mock.ExpectQuery(`INSERT INTO foghorn.node_control_fence_counter`).
		WithArgs("node-real").
		WillReturnRows(sqlmock.NewRows([]string{"value"}).AddRow(int64(3)))
	mock.ExpectBegin()
	mock.ExpectQuery(`INSERT INTO foghorn.node_config_seeds`).
		WithArgs("node-real").
		WillReturnRows(sqlmock.NewRows([]string{"version_counter"}).AddRow(int64(1)))
	mock.ExpectQuery(`SELECT COALESCE\(seed_version, 0\)::bigint AS seed_version, seed_payload`).
		WithArgs("node-real").
		WillReturnRows(sqlmock.NewRows([]string{"seed_version", "seed_payload"}))
	mock.ExpectExec(`UPDATE foghorn.node_config_seeds`).
		WithArgs(int64(1), sqlmock.AnyArg(), "node-real").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	stream := &registerOnceStream{msgs: []*ipcpb.ControlMessage{
		{Payload: &ipcpb.ControlMessage_Register{Register: &ipcpb.Register{NodeId: "node-real", ControlProtocolVersion: MinControlProtocolVersion}}},
		// A FORGED payload node_id — the callback must ignore it in favor of the authenticated session id.
		{SentAt: timestamppb.New(time.UnixMilli(1234)), Payload: &ipcpb.ControlMessage_ArtifactDeleted{ArtifactDeleted: &ipcpb.ArtifactDeleted{ArtifactHash: "h", NodeId: "victim-node", Reason: "evict"}}},
	}}
	srv := &Server{}
	_ = srv.Connect(stream)

	select {
	case deleted := <-got:
		if deleted.GetNodeId() != "node-real" {
			t.Fatalf("callback must receive the authenticated node id, got %q (a forged payload id must not reach the repository)", deleted.GetNodeId())
		}
		if deleted.GetDeletedAtMs() != 1234 {
			t.Fatalf("legacy deletion fence = %d, want envelope time 1234", deleted.GetDeletedAtMs())
		}
	case <-time.After(2 * time.Second):
		t.Fatal("artifact-deleted callback was not invoked")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestConnectTerminatesWhenConfigSeedApplyResultCannotBePersisted(t *testing.T) {
	ensureRegistry(t)
	const nodeID = "node-configseed-persist-fail"
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
		return &quartermasterpb.NodeOwnerResponse{ClusterId: "cluster-1"}, nil
	}
	t.Cleanup(func() { getNodeOwnerFn = previousOwner })

	persistErr := errors.New("database unavailable")
	writer := &recordingConfigSeedApplyAckWriter{err: persistErr}
	previousWriter := configSeedApplyAckWriter
	configSeedApplyAckWriter = writer
	t.Cleanup(func() { configSeedApplyAckWriter = previousWriter })

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

	ack := &ipcpb.ConfigSeedApplyResult{SeedVersion: 1, Success: true}
	stream := &registerOnceStream{msgs: []*ipcpb.ControlMessage{
		{Payload: &ipcpb.ControlMessage_Register{Register: &ipcpb.Register{NodeId: nodeID, ControlProtocolVersion: MinControlProtocolVersion}}},
		{Payload: &ipcpb.ControlMessage_ConfigSeedApplyResult{ConfigSeedApplyResult: ack}},
	}}

	connectErr := (&Server{}).Connect(stream)
	if status.Code(connectErr) != codes.Unavailable {
		t.Fatalf("Connect error=%v, want Unavailable", connectErr)
	}
	if writer.nodeID != nodeID || writer.clusterID != "cluster-1" || writer.ack != ack {
		t.Fatalf("writer saw node=%q cluster=%q ack=%p", writer.nodeID, writer.clusterID, writer.ack)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
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

func TestDrainAcknowledgementFromRetiredConnectionCannotSettleObligation(t *testing.T) {
	prevRegistry := registry
	prevDB := db
	mockDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	registry = &Registry{conns: map[string]*conn{
		"node-1": {rawNodeID: "node-1", fence: 12},
	}, log: logging.NewLogger()}
	db = mockDB
	t.Cleanup(func() {
		registry = prevRegistry
		db = prevDB
		mockDB.Close()
	})

	accepted := processDrainStreamResponse(&ipcpb.DrainStreamResponse{
		RuntimeName: "live+stream", SourceGeneration: "generation-new",
	}, NodeSession{RawNodeID: "node-1", Fence: 11}, logging.NewLogger())
	if accepted {
		t.Fatal("retired connection was accepted as the current drain acknowledger")
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("retired connection reached the durable drain marker: %v", err)
	}
}
