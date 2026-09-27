//go:build schema_verify

package control

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	commodorepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"
	publicv1 "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/events/public/v1"
)

// claimLedger models Commodore's single placement claim per stream: acquire succeeds when nothing
// holds it or its owner re-fires; anyone else is refused with the owner's token when the claim is
// in the requesting cluster; release clears only for the owning cluster and token.
type claimLedger struct {
	mu       sync.Mutex
	cluster  string
	token    string
	releases []*commodorepb.ActiveIngestStream
}

func (l *claimLedger) ValidateStreamKeyForClaim(_ context.Context, _, clusterID, claimToken string) (*commodorepb.ValidateStreamKeyResponse, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.token == "" || l.token == claimToken {
		acquired := l.token == ""
		l.cluster, l.token = clusterID, claimToken
		return &commodorepb.ValidateStreamKeyResponse{Valid: true, TenantId: domainTenant, ClaimAcquired: acquired}, nil
	}
	resp := &commodorepb.ValidateStreamKeyResponse{
		Valid: false, RejectionReason: commodorepb.StreamKeyRejectionReason_STREAM_KEY_REJECTION_DUPLICATE_INGEST,
	}
	if l.cluster == clusterID {
		resp.HeldClaimToken = l.token
	}
	return resp, nil
}

func (l *claimLedger) SyncActiveIngestPlacement(_ context.Context, clusterID string, _, release []*commodorepb.ActiveIngestStream) (*commodorepb.SyncActiveIngestPlacementResponse, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	resp := &commodorepb.SyncActiveIngestPlacementResponse{}
	for _, entry := range release {
		l.releases = append(l.releases, entry)
		if l.cluster == clusterID && l.token == entry.GetClaimToken() {
			l.cluster, l.token = "", ""
			resp.Released++
		}
	}
	return resp, nil
}

func (l *claimLedger) holder() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.token
}

func (l *claimLedger) releaseCount() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.releases)
}

// A session this cell ended (here: the re-registered node no longer holds its publisher) still owns
// Commodore's claim until the lease lapses. The next publisher's admission releases exactly that
// token and is admitted in the same call, with the claim moved to its own token.
func TestClaimIngestPlacement_EndedOwnerIsReleasedAndReclaimed_RealPG(t *testing.T) {
	useRealPG(t)
	ctx := context.Background()
	lg := logging.NewLogger()

	const stream = "live+claim-after-end"
	gen := mintProjected(t, "node-reregistered", stream, lostStreamID, 300, "claim-old", 1000)
	ledger := &claimLedger{cluster: "cell-a", token: "claim-old"}
	if ok, err := RetireIngestSession(ctx, gen, domainTenant, stream, IngestEndedAbsentOnReregister, lg); err != nil || !ok {
		t.Fatalf("end session: ok=%v err=%v", ok, err)
	}

	resp, err := ClaimIngestPlacement(ctx, ledger, "sk", domainTenant, stream, "node-reregistered", "cell-a", "claim-new", lg)
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	if !resp.GetValid() || !resp.GetClaimAcquired() {
		t.Fatalf("new publisher refused after the owner ended: valid=%v acquired=%v reason=%v", resp.GetValid(), resp.GetClaimAcquired(), resp.GetRejectionReason())
	}
	if got := ledger.holder(); got != "claim-new" {
		t.Fatalf("claim owner = %q, want claim-new", got)
	}
	if n := ledger.releaseCount(); n != 1 {
		t.Fatalf("releases = %d, want exactly the ended owner's", n)
	}
}

// A live incumbent keeps its claim: the second publisher stays a duplicate and nothing is released.
func TestClaimIngestPlacement_LiveOwnerStaysDuplicate_RealPG(t *testing.T) {
	useRealPG(t)
	ctx := context.Background()
	lg := logging.NewLogger()

	const stream = "live+claim-live-owner"
	mintProjected(t, "node-live", stream, lostStreamID, 301, "claim-live", 1000)
	ledger := &claimLedger{cluster: "cell-a", token: "claim-live"}

	resp, err := ClaimIngestPlacement(ctx, ledger, "sk", domainTenant, stream, "node-live", "cell-a", "claim-second", lg)
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	if resp.GetValid() || resp.GetRejectionReason() != commodorepb.StreamKeyRejectionReason_STREAM_KEY_REJECTION_DUPLICATE_INGEST {
		t.Fatalf("second publisher admitted against a live owner: valid=%v reason=%v", resp.GetValid(), resp.GetRejectionReason())
	}
	if got := ledger.holder(); got != "claim-live" {
		t.Fatalf("claim owner = %q, want the live claim-live", got)
	}
	if n := ledger.releaseCount(); n != 0 {
		t.Fatalf("a live owner's claim was released (%d releases)", n)
	}
}

// An ended session admitted into another cluster does not prove this cluster's claim stale, and a
// token this cell has no session for proves nothing: both stay duplicates.
func TestClaimIngestPlacement_UnprovenOwnerStaysDuplicate_RealPG(t *testing.T) {
	useRealPG(t)
	ctx := context.Background()
	lg := logging.NewLogger()

	const stream = "live+claim-unproven"
	gen := mintProjected(t, "node-other-cluster", stream, lostStreamID, 302, "claim-cell-a", 1000)
	if ok, err := RetireIngestSession(ctx, gen, domainTenant, stream, IngestEndedAbsentOnReregister, lg); err != nil || !ok {
		t.Fatalf("end session: ok=%v err=%v", ok, err)
	}
	for _, ledger := range []*claimLedger{
		{cluster: "cell-b", token: "claim-cell-a"},
		{cluster: "cell-b", token: "claim-unknown"},
	} {
		resp, err := ClaimIngestPlacement(ctx, ledger, "sk", domainTenant, stream, "node-other-cluster", "cell-b", "claim-next", lg)
		if err != nil {
			t.Fatalf("claim: %v", err)
		}
		if resp.GetValid() {
			t.Fatalf("admitted against an owner %q this cell did not end in cell-b", ledger.token)
		}
		if n := ledger.releaseCount(); n != 0 {
			t.Fatalf("released an unproven owner's claim (%d releases)", n)
		}
	}
}

// mintPending admits a publisher through MintIngestSession and leaves it pending (its projection
// never confirmed), with its row minted age ago.
func mintPending(t *testing.T, node, stream string, pid int64, trigger string, age time.Duration) string {
	t.Helper()
	id, outcome, err := MintIngestSession(context.Background(), IngestSessionRequest{
		TenantID: domainTenant, NodeID: node, InternalName: stream, StreamID: lostStreamID, PlaybackID: lostPlaybackID,
		Protocol: publicv1.IngestProtocol_INGEST_PROTOCOL_RTMP, ConnectorPID: pid, TriggerUUID: trigger,
		StartedAtMillis: time.Now().UnixMilli(), IngestClusterID: "cell-a",
	}, logging.NewLogger())
	if err != nil || outcome != IngestSessionActive {
		t.Fatalf("mint %s on %s: outcome=%v err=%v", trigger, node, outcome, err)
	}
	if _, err := db.ExecContext(context.Background(), `
		UPDATE foghorn.ingest_sessions SET started_at = NOW() - ($2::bigint * INTERVAL '1 millisecond')
		 WHERE id = $1::uuid AND projection_state = 'pending'
	`, id, age.Milliseconds()); err != nil {
		t.Fatalf("age pending session: %v", err)
	}
	return id
}

// The staging defect: an admission's INSERT committed after its caller's deadline, so Mist never got
// the answer and refused the push, and the pending session kept the stream's claim. The encoder's
// next connector on the same node is admitted: the lapsed session ends admission_lapsed, its claim is
// released by token, the claim moves to the new connector, and the new session mints.
func TestClaimIngestPlacement_LapsedPendingOwnerIsSupersededAndReclaimed_RealPG(t *testing.T) {
	useRealPG(t)
	ctx := context.Background()
	lg := logging.NewLogger()

	const node, stream = "node-lapsed", "live+claim-lapsed-pending"
	lapsed := mintPending(t, node, stream, 500, "claim-timed-out", IngestAdmissionWindow+time.Second)
	ledger := &claimLedger{cluster: "cell-a", token: "claim-timed-out"}

	resp, err := ClaimIngestPlacement(ctx, ledger, "sk", domainTenant, stream, node, "cell-a", "claim-reconnect", lg)
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	if !resp.GetValid() || !resp.GetClaimAcquired() {
		t.Fatalf("reconnect refused behind a lapsed admission: valid=%v acquired=%v reason=%v", resp.GetValid(), resp.GetClaimAcquired(), resp.GetRejectionReason())
	}
	if got := ledger.holder(); got != "claim-reconnect" {
		t.Fatalf("claim owner = %q, want claim-reconnect", got)
	}
	if n := ledger.releaseCount(); n != 1 {
		t.Fatalf("releases = %d, want exactly the lapsed owner's", n)
	}
	if ended, reason := sessionEnded(t, lapsed); !ended || reason != IngestEndedAdmissionLapsed {
		t.Fatalf("lapsed session: ended=%v reason=%q, want %s", ended, reason, IngestEndedAdmissionLapsed)
	}
	_, outcome, err := MintIngestSession(ctx, IngestSessionRequest{
		TenantID: domainTenant, NodeID: node, InternalName: stream, StreamID: lostStreamID, PlaybackID: lostPlaybackID,
		Protocol: publicv1.IngestProtocol_INGEST_PROTOCOL_RTMP, ConnectorPID: 501, TriggerUUID: "claim-reconnect",
		StartedAtMillis: time.Now().UnixMilli(), IngestClusterID: "cell-a",
	}, lg)
	if err != nil || outcome != IngestSessionActive {
		t.Fatalf("reconnect mint: outcome=%v err=%v, want active", outcome, err)
	}
}

// A pending admission still inside its window may yet be answered (Mist retries the same trigger),
// so a second connector stays a duplicate at both the claim and the mint, and nothing is released.
func TestClaimIngestPlacement_PendingOwnerInsideWindowStaysDuplicate_RealPG(t *testing.T) {
	useRealPG(t)
	ctx := context.Background()
	lg := logging.NewLogger()

	const node, stream = "node-pending", "live+claim-pending-window"
	pending := mintPending(t, node, stream, 510, "claim-in-flight", 0)
	ledger := &claimLedger{cluster: "cell-a", token: "claim-in-flight"}

	resp, err := ClaimIngestPlacement(ctx, ledger, "sk", domainTenant, stream, node, "cell-a", "claim-second", lg)
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	if resp.GetValid() || resp.GetRejectionReason() != commodorepb.StreamKeyRejectionReason_STREAM_KEY_REJECTION_DUPLICATE_INGEST {
		t.Fatalf("second connector admitted against an in-flight admission: valid=%v reason=%v", resp.GetValid(), resp.GetRejectionReason())
	}
	if got, n := ledger.holder(), ledger.releaseCount(); got != "claim-in-flight" || n != 0 {
		t.Fatalf("claim owner=%q releases=%d, want claim-in-flight kept", got, n)
	}
	_, outcome, err := MintIngestSession(ctx, IngestSessionRequest{
		TenantID: domainTenant, NodeID: node, InternalName: stream, StreamID: lostStreamID, PlaybackID: lostPlaybackID,
		Protocol: publicv1.IngestProtocol_INGEST_PROTOCOL_RTMP, ConnectorPID: 511, TriggerUUID: "claim-second",
		StartedAtMillis: time.Now().UnixMilli(), IngestClusterID: "cell-a",
	}, lg)
	if err != nil || outcome != IngestSessionRejectedDuplicate {
		t.Fatalf("second connector mint: outcome=%v err=%v, want duplicate", outcome, err)
	}
	if ended, _ := sessionEnded(t, pending); ended {
		t.Fatal("an admission still inside its window was ended")
	}
}

// Once the lapsed claim has expired on its own, the new connector's claim succeeds and the mint
// itself supersedes the lapsed pending incumbent on the same node. A lapsed pending session on
// ANOTHER node is left to the takeover rules and stays a duplicate while that node's presence is
// unknown.
func TestMintIngestSession_LapsedPendingIncumbent_RealPG(t *testing.T) {
	useRealPG(t)
	ctx := context.Background()
	lg := logging.NewLogger()
	prevGuard := ingestTakeoverNodeGuard
	ingestTakeoverNodeGuard = nil
	t.Cleanup(func() { ingestTakeoverNodeGuard = prevGuard })

	const node, stream = "node-mint-lapsed", "live+mint-lapsed-pending"
	lapsed := mintPending(t, node, stream, 520, "mint-timed-out", IngestAdmissionWindow+time.Second)

	_, outcome, err := MintIngestSession(ctx, IngestSessionRequest{
		TenantID: domainTenant, NodeID: "node-elsewhere", InternalName: stream, StreamID: lostStreamID, PlaybackID: lostPlaybackID,
		Protocol: publicv1.IngestProtocol_INGEST_PROTOCOL_RTMP, ConnectorPID: 530, TriggerUUID: "mint-other-node",
		StartedAtMillis: time.Now().UnixMilli(), IngestClusterID: "cell-a",
	}, lg)
	if err != nil || outcome != IngestSessionRejectedDuplicate {
		t.Fatalf("other-node mint: outcome=%v err=%v, want duplicate", outcome, err)
	}

	id, outcome, err := MintIngestSession(ctx, IngestSessionRequest{
		TenantID: domainTenant, NodeID: node, InternalName: stream, StreamID: lostStreamID, PlaybackID: lostPlaybackID,
		Protocol: publicv1.IngestProtocol_INGEST_PROTOCOL_RTMP, ConnectorPID: 521, TriggerUUID: "mint-reconnect",
		StartedAtMillis: time.Now().UnixMilli(), IngestClusterID: "cell-a",
	}, lg)
	if err != nil || outcome != IngestSessionActive || id == "" || id == lapsed {
		t.Fatalf("same-node reconnect mint: id=%q outcome=%v err=%v, want a new active session", id, outcome, err)
	}
	if ended, reason := sessionEnded(t, lapsed); !ended || reason != IngestEndedAdmissionLapsed {
		t.Fatalf("lapsed incumbent: ended=%v reason=%q, want %s", ended, reason, IngestEndedAdmissionLapsed)
	}
}

// Claim renewal re-asserts a pending session's claim only inside its admission window; a projected
// session is renewed however old it is.
func TestActiveIngestSessionClaimsSkipLapsedPending_RealPG(t *testing.T) {
	useRealPG(t)
	ctx := context.Background()

	mintPending(t, "node-renew", "live+renew-fresh", 540, "renew-fresh", 0)
	lapsed := mintPending(t, "node-renew", "live+renew-lapsed", 541, "renew-lapsed", IngestAdmissionWindow+time.Second)
	projected := mintPending(t, "node-renew", "live+renew-projected", 542, "renew-projected", 0)
	projectAged(t, projected, time.Hour)

	claims, err := activeIngestSessionClaims(ctx)
	if err != nil {
		t.Fatalf("list claims: %v", err)
	}
	renewed := map[string]bool{}
	for _, claim := range claims {
		renewed[claim.ClaimToken] = true
	}
	if !renewed["renew-fresh"] || !renewed["renew-projected"] {
		t.Fatalf("renewal dropped a live claim: %v", renewed)
	}
	if renewed["renew-lapsed"] {
		t.Fatalf("renewal re-asserted the claim of lapsed pending session %s", lapsed)
	}
}
