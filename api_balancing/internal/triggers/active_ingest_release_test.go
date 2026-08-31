package triggers

import (
	"context"
	"database/sql"
	"errors"
	"net"
	"sync"
	"sync/atomic"
	"testing"

	"frameworks/api_balancing/internal/control"
	"frameworks/api_balancing/internal/state"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/clients/commodore"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	commodorepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"

	"github.com/sirupsen/logrus"
	"google.golang.org/grpc"
)

// placementCapturingCommodore records the placement syncs a real client makes.
type placementCapturingCommodore struct {
	commodorepb.UnimplementedInternalServiceServer

	mu   sync.Mutex
	reqs []*commodorepb.SyncActiveIngestPlacementRequest
}

// claimAcquired is what Commodore reports back about THIS call's effect on the
// placement row. False models a contended claim (a fresher one is held
// elsewhere) or an admission the caller never got from Commodore at all.
var claimAcquired atomic.Bool

// ValidateStreamKey answers with a suspended tenant: valid credentials, so the
// claim is attempted, but the publish is then rejected by a later gate.
func (c *placementCapturingCommodore) ValidateStreamKey(_ context.Context, _ *commodorepb.ValidateStreamKeyRequest) (*commodorepb.ValidateStreamKeyResponse, error) {
	return &commodorepb.ValidateStreamKeyResponse{
		Valid:         true,
		TenantId:      "tenant-1",
		UserId:        "user-1",
		StreamId:      "stream-1",
		InternalName:  "internal-1",
		IsSuspended:   true,
		ClaimAcquired: claimAcquired.Load(),
	}, nil
}

func (c *placementCapturingCommodore) SyncActiveIngestPlacement(_ context.Context, req *commodorepb.SyncActiveIngestPlacementRequest) (*commodorepb.SyncActiveIngestPlacementResponse, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.reqs = append(c.reqs, req)
	return &commodorepb.SyncActiveIngestPlacementResponse{Released: int32(len(req.GetRelease()))}, nil
}

func (c *placementCapturingCommodore) requests() []*commodorepb.SyncActiveIngestPlacementRequest {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]*commodorepb.SyncActiveIngestPlacementRequest(nil), c.reqs...)
}

func processorWithPlacementCommodore(t *testing.T) (*Processor, *placementCapturingCommodore) {
	t.Helper()

	listener, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	fake := &placementCapturingCommodore{}
	server := grpc.NewServer()
	commodorepb.RegisterInternalServiceServer(server, fake)
	go func() { _ = server.Serve(listener) }()

	client, err := commodore.NewGRPCClient(commodore.GRPCConfig{
		GRPCAddr:      listener.Addr().String(),
		Logger:        logging.Logger(logrus.New()),
		AllowInsecure: true,
	})
	if err != nil {
		server.Stop()
		_ = listener.Close()
		t.Fatalf("commodore client: %v", err)
	}
	t.Cleanup(func() {
		_ = client.Close()
		server.Stop()
		_ = listener.Close()
	})

	p := NewProcessor(logging.NewLogger(), client, nil, nil, nil)
	p.SetClusterID("media-eu")
	return p, fake
}

// installControlDBForTest points the control package's package-level database
// at a mock and restores it afterwards.
func installControlDBForTest(t *testing.T) sqlmock.Sqlmock {
	t.Helper()
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	control.SetDB(db)
	t.Cleanup(func() {
		control.SetDB(nil)
		_ = db.Close()
	})
	return mock
}

// closingPID is the connector PID shared by the seeded publisher session and its close.
const closingPID int64 = 4242

func pushInputCloseTrigger(internalName string, pid int64) *ipcpb.MistTrigger {
	tenantID := "tenant-1"
	// Quartermaster is unwired in this test, so the trigger's own declaration
	// is the node→cluster answer — the same fallback PUSH_REWRITE takes.
	clusterID := "demo-media"
	const closeMillis = int64(1_700_000_000_000)
	return &ipcpb.MistTrigger{
		NodeId:    "node-A",
		TenantId:  &tenantID,
		ClusterId: &clusterID,
		TriggerPayload: &ipcpb.MistTrigger_PushInputClose{
			PushInputClose: &ipcpb.PushInputCloseTrigger{
				StreamName:        "live+" + internalName,
				Pid:               pid,
				TriggerUnixMillis: closeMillis,
			},
		},
	}
}

// endedGeneration is the durable ingest-session id the close finalizes. The
// source flip is a compare-and-set on it, so the release only fires when this
// close really ended the session the registry believes is current.
const endedGeneration = "11111111-2222-3333-4444-555555555555"

// expectSessionFinalized mocks FinalizeIngestSessionClose ending a real
// session: the UPDATE returns its generation and the DVR-stop claim finds
// nothing to stop.
// closingClaimToken owns the placement claim the seeded session took; the
// release matches on it.
const closingClaimToken = "trigger-uuid-1"

func expectSessionFinalized(mock sqlmock.Sqlmock, generation string) {
	mock.ExpectBegin()
	mock.ExpectExec(`pg_advisory_xact_lock`).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery(`UPDATE foghorn\.ingest_sessions[\s\S]*RETURNING id`).
		WillReturnRows(sqlmock.NewRows([]string{"id", "start_trigger_uuid", "ingest_cluster_id"}).
			AddRow(generation, closingClaimToken, "demo-media"))
	mock.ExpectQuery(`foghorn\.artifacts`).
		WillReturnRows(sqlmock.NewRows([]string{"artifact_hash", "storage_node_id"}))
	mock.ExpectQuery(`INSERT INTO foghorn.source_projection_revision_counter`).
		WithArgs(sqlmock.AnyArg(), sqlmock.AnyArg()).
		WillReturnRows(sqlmock.NewRows([]string{"revision"}).AddRow(int64(2)))
	expectOfflineEffectInsert(mock)
	mock.ExpectCommit()
}

// expectSessionFenced mocks a close that finalizes nothing — a delayed close
// whose event time precedes the active session's start, the PID-reuse case. It
// records a close-before-insert tombstone on the no-row path.
func expectSessionFenced(mock sqlmock.Sqlmock) {
	mock.ExpectBegin()
	mock.ExpectExec(`pg_advisory_xact_lock`).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery(`UPDATE foghorn\.ingest_sessions`).WillReturnError(sql.ErrNoRows)
	mock.ExpectExec(`INSERT INTO foghorn\.ingest_close_tombstones`).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
}

// seedLivePublisher puts the registry in the state an accepted PUSH_REWRITE
// leaves behind: an admitted source with its durable generation stamped, which
// is what the close's compare-and-set matches against.
func seedLivePublisher(t *testing.T, internalName, generation string) {
	t.Helper()
	// The close fence matches the generation carried by the projected DB winner.
	projectSourceForTest(t, control.StreamRegistryInstance, internalName, "node-A", closingPID, "", generation, 1)
}

// When the publisher disconnects, the cluster must stop claiming to be the
// stream's ingest cluster. Letting the claim lapse instead keeps routing
// publishers back to a cluster they already left for the rest of the window.
//
// The claim names the PUBLISHING NODE's media cluster, which is what
// PUSH_REWRITE wrote. Foghorn's own CLUSTER_ID names the process and one
// process serves several media clusters, so releasing under it matches no row.
func TestPushInputClose_ReleasesPlacementUnderPublishingNodeCluster(t *testing.T) {
	sm := state.ResetDefaultManagerForTests()
	t.Cleanup(func() { state.ResetDefaultManagerForTests() })
	installRegistryForTest(t)
	mock := installControlDBForTest(t)
	p, fake := processorWithPlacementCommodore(t) // process cluster: media-eu

	const internal = "release-me"
	sm.SetNodeConnectionInfo(context.Background(), "node-A", "node-A:18090", "", "demo-media", nil)
	seedLivePublisher(t, internal, endedGeneration)
	expectSessionFinalized(mock, endedGeneration)

	if _, _, err := p.handlePushInputClose(pushInputCloseTrigger(internal, closingPID)); err != nil {
		t.Fatalf("handlePushInputClose: %v", err)
	}

	reqs := fake.requests()
	if len(reqs) != 1 {
		t.Fatalf("expected one placement sync, got %d", len(reqs))
	}
	if got := reqs[0].GetClusterId(); got != "demo-media" {
		t.Fatalf("released under %q, want the publishing node's demo-media", got)
	}
	if len(reqs[0].GetRenew()) != 0 {
		t.Fatalf("a close must not renew: %+v", reqs[0].GetRenew())
	}
	release := reqs[0].GetRelease()
	if len(release) != 1 || release[0].GetTenantId() != "tenant-1" || release[0].GetInternalName() != internal {
		t.Fatalf("unexpected release payload: %+v", release)
	}
}

// A delayed close from a superseded session finalizes nothing: its event time
// precedes the replacement's session start, so the UPDATE is fenced. Nothing
// was ended, so nothing may be released — otherwise a live publisher loses its
// placement to an earlier connection's close.
func TestPushInputClose_FencedCloseDoesNotReleaseReplacementPlacement(t *testing.T) {
	sm := state.ResetDefaultManagerForTests()
	t.Cleanup(func() { state.ResetDefaultManagerForTests() })
	installRegistryForTest(t)
	mock := installControlDBForTest(t)
	p, fake := processorWithPlacementCommodore(t)

	const internal = "still-live"
	sm.SetNodeConnectionInfo(context.Background(), "node-A", "node-A:18090", "", "demo-media", nil)
	seedLivePublisher(t, internal, endedGeneration) // replacement holds the current generation
	expectSessionFenced(mock)

	if _, _, err := p.handlePushInputClose(pushInputCloseTrigger(internal, closingPID)); err != nil {
		t.Fatalf("handlePushInputClose: %v", err)
	}

	if reqs := fake.requests(); len(reqs) != 0 {
		t.Fatalf("a stale close released the replacement's placement: %+v", reqs)
	}
}

// A close that durably ENDS a session releases THAT session's placement claim even when the registry's
// projection has moved to a newer generation — the release is token-fenced at Commodore (it names the
// ended session's claim), so it gives back only that claim and never a replacement's. A close that
// finalizes NOTHING releases nothing — see
// TestPushInputClose_FencedCloseDoesNotReleaseReplacementPlacement.
func TestPushInputClose_EndedSessionReleasesRegardlessOfProjection(t *testing.T) {
	sm := state.ResetDefaultManagerForTests()
	t.Cleanup(func() { state.ResetDefaultManagerForTests() })
	installRegistryForTest(t)
	mock := installControlDBForTest(t)
	p, fake := processorWithPlacementCommodore(t)

	const internal = "superseded"
	sm.SetNodeConnectionInfo(context.Background(), "node-A", "node-A:18090", "", "demo-media", nil)
	seedLivePublisher(t, internal, endedGeneration)
	// The close finalizes a session whose generation differs from the one the registry currently holds.
	expectSessionFinalized(mock, "99999999-8888-7777-6666-555555555555")

	if _, _, err := p.handlePushInputClose(pushInputCloseTrigger(internal, closingPID)); err != nil {
		t.Fatalf("handlePushInputClose: %v", err)
	}

	// The ended session's (token-fenced) claim is released, independent of the registry projection.
	reqs := fake.requests()
	if len(reqs) != 1 {
		t.Fatalf("a durably-ended session must release its token-fenced claim, got %+v", reqs)
	}
	if got := reqs[0].GetRelease(); len(got) != 1 || got[0].GetClaimToken() != closingClaimToken {
		t.Fatalf("release must carry the ended session's claim token %q, got %+v", closingClaimToken, got)
	}
}

// An unattributable publishing node is refused rather than claimed under this
// Foghorn's own cluster. One process serves many virtual media clusters, so its
// identity is not a media-cluster answer: substituting it would claim placement
// where the publisher is not, and could deny a publisher whose tenant has no
// grant to the process cluster. Retryable — attribution returns with
// Quartermaster or the node's next heartbeat.
func TestPushRewrite_RefusesUnattributableNodeRatherThanUsingProcessCluster(t *testing.T) {
	state.ResetDefaultManagerForTests()
	t.Cleanup(func() { state.ResetDefaultManagerForTests() })
	p, _ := processorWithPlacementCommodore(t) // process cluster: media-eu

	if got := p.IngestClusterIDForNode("unknown-node"); got != "" {
		t.Fatalf("unattributed node resolved to %q; want no cluster", got)
	}
}

// With node state carrying the cluster, attribution succeeds and names the
// node's cluster — not the process's.
func TestIngestClusterIDForNode_UsesNodeStateWhenQuartermasterIsUnreachable(t *testing.T) {
	sm := state.ResetDefaultManagerForTests()
	t.Cleanup(func() { state.ResetDefaultManagerForTests() })
	p, _ := processorWithPlacementCommodore(t) // process cluster: media-eu
	sm.SetNodeConnectionInfo(context.Background(), "edge-node-1", "edge-node-1:18090", "", "demo-media", nil)

	if got := p.IngestClusterIDForNode("edge-node-1"); got != "demo-media" {
		t.Fatalf("cluster = %q, want the node's demo-media", got)
	}
}

// PUSH_REWRITE is where ingest is decided, and ValidateStreamKey claims the
// placement lease as part of that decision. When a gate after it rejects the
// publisher, the claim must not linger: it would block every other entitled
// cluster from accepting them for the rest of the lease window.
func TestPushRewrite_RejectedPublishReleasesItsClaim(t *testing.T) {
	sm := state.ResetDefaultManagerForTests()
	t.Cleanup(func() { state.ResetDefaultManagerForTests() })
	installRegistryForTest(t)
	mock := installControlDBForTest(t)
	p, fake := processorWithPlacementCommodore(t)

	sm.SetNodeConnectionInfo(context.Background(), "node-A", "node-A:18090", "", "demo-media", nil)
	// No existing session for this connection: a new publisher.
	mock.ExpectQuery(`FROM foghorn\.ingest_sessions`).WillReturnError(sql.ErrNoRows)
	// Commodore reports the claim as taken by this attempt.
	claimAcquired.Store(true)
	t.Cleanup(func() { claimAcquired.Store(false) })

	// A suspended tenant validates, taking the claim, then is rejected.
	_, _, err := p.handlePushRewrite(&ipcpb.MistTrigger{
		NodeId: "node-A",
		TriggerPayload: &ipcpb.MistTrigger_PushRewrite{
			PushRewrite: &ipcpb.PushRewriteTrigger{
				Pid: 4242, TriggerUuid: "uuid-1", TriggerUnixMillis: 1,
				StreamName: "sk-abc", Hostname: "127.0.0.1",
			},
		},
	})
	if err == nil {
		t.Fatal("suspended tenant was admitted")
	}

	reqs := fake.requests()
	if len(reqs) != 1 {
		t.Fatalf("expected the rejected publish to release its claim, got %d syncs", len(reqs))
	}
	if got := reqs[0].GetClusterId(); got != "demo-media" {
		t.Fatalf("released under %q, want the publishing node's demo-media", got)
	}
	if len(reqs[0].GetRelease()) != 1 {
		t.Fatalf("expected exactly one release: %+v", reqs[0].GetRelease())
	}
}

// A rejected attempt that never took the claim releases nothing. That covers a
// second publisher losing the contended claim to the one already publishing,
// and an admission answered from the validation cache while Commodore was
// unreachable — in both cases the claim is somebody else's, or nobody's.
func TestPushRewrite_RejectionWithoutClaimReleasesNothing(t *testing.T) {
	sm := state.ResetDefaultManagerForTests()
	t.Cleanup(func() { state.ResetDefaultManagerForTests() })
	installRegistryForTest(t)
	mock := installControlDBForTest(t)
	p, fake := processorWithPlacementCommodore(t)

	sm.SetNodeConnectionInfo(context.Background(), "node-A", "node-A:18090", "", "demo-media", nil)
	mock.ExpectQuery(`FROM foghorn\.ingest_sessions`).WillReturnError(sql.ErrNoRows)
	claimAcquired.Store(false)

	if _, _, err := p.handlePushRewrite(&ipcpb.MistTrigger{
		NodeId: "node-A",
		TriggerPayload: &ipcpb.MistTrigger_PushRewrite{
			PushRewrite: &ipcpb.PushRewriteTrigger{
				Pid: 4243, TriggerUuid: "uuid-2", TriggerUnixMillis: 1,
				StreamName: "sk-abc", Hostname: "127.0.0.1",
			},
		},
	}); err == nil {
		t.Fatal("suspended tenant was admitted")
	}

	if reqs := fake.requests(); len(reqs) != 0 {
		t.Fatalf("an attempt that took no claim still released one: %+v", reqs)
	}
}

// A failed session lookup is not evidence that no session exists. Falling back
// to the node's current cluster would validate and claim it for a connection
// whose session may be bound to another one — the exact drift the lookup
// prevents. Retryable denial instead.
func TestPushRewrite_SessionLookupFailureDeniesRatherThanGuessing(t *testing.T) {
	sm := state.ResetDefaultManagerForTests()
	t.Cleanup(func() { state.ResetDefaultManagerForTests() })
	installRegistryForTest(t)
	mock := installControlDBForTest(t)
	p, fake := processorWithPlacementCommodore(t)

	sm.SetNodeConnectionInfo(context.Background(), "node-A", "node-A:18090", "", "demo-media", nil)
	mock.ExpectQuery(`FROM foghorn\.ingest_sessions`).WillReturnError(errors.New("db down"))

	_, retry, err := p.handlePushRewrite(&ipcpb.MistTrigger{
		NodeId: "node-A",
		TriggerPayload: &ipcpb.MistTrigger_PushRewrite{
			PushRewrite: &ipcpb.PushRewriteTrigger{
				Pid: 4242, TriggerUuid: "uuid-1", TriggerUnixMillis: 1,
				StreamName: "sk-abc", Hostname: "127.0.0.1",
			},
		},
	})
	if err == nil {
		t.Fatal("a failed session lookup was treated as no session")
	}
	if !retry {
		t.Fatal("denial should be retryable")
	}
	if reqs := fake.requests(); len(reqs) != 0 {
		t.Fatalf("placement was touched despite an unresolved session: %+v", reqs)
	}
}

// The open-session lookup is tenant-scoped, so another tenant's session is
// simply not visible: this connection's trigger UUID resolves to nothing, the
// publish falls back to node attribution, and no placement is ever taken under
// a foreign session's cluster.
//
// claimAcquired is left TRUE here deliberately. The earlier shape read across
// tenants, claimed, and only then compared — leaking the claim on the mismatch
// path — and a test that forced claimAcquired=false could not see it. This one
// would.
func TestPushRewrite_ForeignTenantSessionIsNotVisibleAndLeaksNoClaim(t *testing.T) {
	sm := state.ResetDefaultManagerForTests()
	t.Cleanup(func() { state.ResetDefaultManagerForTests() })
	installRegistryForTest(t)
	mock := installControlDBForTest(t)
	p, fake := processorWithPlacementCommodore(t)

	sm.SetNodeConnectionInfo(context.Background(), "node-A", "node-A:18090", "", "demo-media", nil)
	claimAcquired.Store(true)
	t.Cleanup(func() { claimAcquired.Store(false) })

	// Scoped to the validated tenant, so the other tenant's row does not match.
	mock.ExpectQuery(`FROM foghorn\.ingest_sessions`).
		WithArgs("tenant-1", "node-A", "uuid-1").
		WillReturnError(sql.ErrNoRows)

	_, _, err := p.handlePushRewrite(&ipcpb.MistTrigger{
		NodeId: "node-A",
		TriggerPayload: &ipcpb.MistTrigger_PushRewrite{
			PushRewrite: &ipcpb.PushRewriteTrigger{
				Pid: 4242, TriggerUuid: "uuid-1", TriggerUnixMillis: 1,
				StreamName: "sk-abc", Hostname: "127.0.0.1",
			},
		},
	})
	// The suspended-tenant fake rejects after the claim, which is what exercises
	// the release path.
	if err == nil {
		t.Fatal("suspended tenant was admitted")
	}

	reqs := fake.requests()
	if len(reqs) != 1 {
		t.Fatalf("expected the rejected publish to release its claim, got %d syncs", len(reqs))
	}
	if got := reqs[0].GetClusterId(); got != "demo-media" {
		t.Fatalf("released under %q, want the node-attributed demo-media", got)
	}
}
