//go:build schema_verify

package control

import (
	"context"
	"sync"
	"testing"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	commodorepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"
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

	resp, err := ClaimIngestPlacement(ctx, ledger, "sk", domainTenant, stream, "cell-a", "claim-new", lg)
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

	resp, err := ClaimIngestPlacement(ctx, ledger, "sk", domainTenant, stream, "cell-a", "claim-second", lg)
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
		resp, err := ClaimIngestPlacement(ctx, ledger, "sk", domainTenant, stream, "cell-b", "claim-next", lg)
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
