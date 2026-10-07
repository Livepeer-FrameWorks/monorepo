package grpc

import (
	"context"
	"database/sql"
	"sync"
	"testing"
	"time"

	"frameworks/api_balancing/internal/federation"

	"github.com/DATA-DOG/go-sqlmock"
	foghornfederationpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/foghorn_federation"
	sharedpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/shared"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// A clip delete committed on its origin cell has every peer cell retire its
// adopted pointer before the delete returns, so a peer stops redirecting
// viewers to the deleted clip without waiting for the catalog projection and
// the signed tombstone that follows it.
func TestDeleteClip_PeerCellsRetireTheirPointerBeforeTheDeleteReturns(t *testing.T) {
	srv, mock := newLifecycleServer(t)
	_ = captureCatalogDirty(t)
	srv.clusterID = "cell-eu"
	fed := &mockFedRPC{handlers: map[string]bool{"cell-us": true}}
	srv.federationClient = fed
	srv.peerManager = &mockPeerResolver{peers: map[string]string{"cell-us": "foghorn.us:18019", "cell-eu": "foghorn.eu:18019"}}
	srv.recordThumbnailCleanup = func(context.Context, *sql.Tx, string, string) error { return nil }

	mock.ExpectQuery(`SELECT status, size_bytes`).
		WithArgs("clip-h", "tenant-a").
		WillReturnRows(sqlmock.NewRows([]string{
			"status", "size_bytes", "retention_until", "stream_internal_name",
			"tenant_id", "user_id", "format", "storage_cluster_id", "origin_cluster_id", "active_object_key",
			"active_dtsh_key", "sync_object_key", "durable_backend_local", "backend_id",
		}).AddRow("ready", nil, nil, "live+stream-1", "tenant-a", "user-1", "mkv", nil, nil, nil, nil, nil, false, nil))
	mock.ExpectQuery(`SELECT node_id FROM foghorn.artifact_nodes`).
		WithArgs("clip-h").
		WillReturnRows(sqlmock.NewRows([]string{"node_id"}))
	mock.ExpectBegin()
	mock.ExpectExec(`UPDATE foghorn.artifacts SET status = 'deleted'`).
		WithArgs("clip-h", "tenant-a").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`INSERT INTO foghorn\.artifact_event_outbox`).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`INSERT INTO foghorn\.artifact_event_outbox`).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	mock.ExpectExec(`UPDATE foghorn.processing_jobs`).
		WithArgs("clip-h").
		WillReturnResult(sqlmock.NewResult(0, 0))

	resp, err := srv.DeleteClip(context.Background(), &sharedpb.DeleteClipRequest{ClipHash: "clip-h", TenantId: "tenant-a"})
	if err != nil || !resp.GetSuccess() {
		t.Fatalf("delete = %+v, %v", resp, err)
	}
	if len(fed.calls) != 1 {
		t.Fatalf("peer commands sent = %+v, want one retire to cell-us", fed.calls)
	}
	call := fed.calls[0]
	if call.clusterID != "cell-us" || call.command != federation.RetireArtifactPointerCommand ||
		call.artifactHash != "clip-h" || call.tenantID != "tenant-a" {
		t.Fatalf("peer command = %+v, want %s of clip-h for tenant-a to cell-us", call, federation.RetireArtifactPointerCommand)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}

// peerCells answers ForwardArtifactCommand per peer: a partitioned peer never
// answers, so the call returns only when its context ends; a down peer refuses
// at once.
type peerCells struct {
	mockFedRPC
	mu          sync.Mutex
	partitioned map[string]bool
	deadlines   map[string]time.Time
}

func (p *peerCells) ForwardArtifactCommand(ctx context.Context, clusterID, _ string, _ *foghornfederationpb.ForwardArtifactCommandRequest) (*foghornfederationpb.ForwardArtifactCommandResponse, error) {
	deadline, _ := ctx.Deadline()
	p.mu.Lock()
	p.deadlines[clusterID] = deadline
	p.mu.Unlock()
	if p.partitioned[clusterID] {
		<-ctx.Done()
		return nil, status.FromContextError(ctx.Err()).Err()
	}
	return nil, status.Error(codes.Unavailable, "connection refused")
}

// A delete whose peer cells are down or partitioned still succeeds, and
// returns once the federation forward deadline ends the call to the
// partitioned peer: the deletion is already committed, and a peer that missed
// the retire converges when the signed tombstone reaches it.
func TestDeleteClip_UnreachablePeersDoNotHoldOrFailTheDelete(t *testing.T) {
	srv, mock := newLifecycleServer(t)
	_ = captureCatalogDirty(t)
	srv.clusterID = "cell-eu"
	peers := &peerCells{partitioned: map[string]bool{"cell-us": true}, deadlines: map[string]time.Time{}}
	srv.federationClient = peers
	srv.peerManager = &mockPeerResolver{peers: map[string]string{"cell-us": "foghorn.us:18019", "cell-ap": "foghorn.ap:18019"}}

	mock.ExpectQuery(`SELECT status, size_bytes`).
		WithArgs("clip-h", "tenant-a").
		WillReturnRows(sqlmock.NewRows([]string{
			"status", "size_bytes", "retention_until", "stream_internal_name",
			"tenant_id", "user_id", "format", "storage_cluster_id", "origin_cluster_id", "active_object_key",
			"active_dtsh_key", "sync_object_key", "durable_backend_local", "backend_id",
		}).AddRow("ready", nil, nil, "live+stream-1", "tenant-a", "user-1", "mkv", nil, nil, nil, nil, nil, false, nil))
	mock.ExpectQuery(`SELECT node_id FROM foghorn.artifact_nodes`).
		WithArgs("clip-h").
		WillReturnRows(sqlmock.NewRows([]string{"node_id"}))
	mock.ExpectBegin()
	mock.ExpectExec(`UPDATE foghorn.artifacts SET status = 'deleted'`).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`INSERT INTO foghorn\.artifact_event_outbox`).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`INSERT INTO foghorn\.artifact_event_outbox`).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	mock.ExpectExec(`UPDATE foghorn.processing_jobs`).WillReturnResult(sqlmock.NewResult(0, 0))

	// The caller's own budget is Commodore's Foghorn call timeout.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	started := time.Now()
	resp, err := srv.DeleteClip(ctx, &sharedpb.DeleteClipRequest{ClipHash: "clip-h", TenantId: "tenant-a"})
	elapsed := time.Since(started)
	if err != nil || !resp.GetSuccess() {
		t.Fatalf("delete with unreachable peers = %+v, %v; want success", resp, err)
	}
	if elapsed > artifactForwardPeerTimeout+2*time.Second {
		t.Fatalf("delete took %s with a partitioned peer, want at most the %s federation forward deadline", elapsed, artifactForwardPeerTimeout)
	}
	peers.mu.Lock()
	defer peers.mu.Unlock()
	for _, cell := range []string{"cell-us", "cell-ap"} {
		deadline, asked := peers.deadlines[cell]
		if !asked {
			t.Fatalf("%s was not asked to retire its pointer", cell)
		}
		if deadline.IsZero() || deadline.Sub(started) > artifactForwardPeerTimeout+time.Second || deadline.After(started.Add(elapsed)) {
			t.Fatalf("%s call deadline %s after start (delete returned after %s), want the %s forward deadline, ended before the delete returned",
				cell, deadline.Sub(started), elapsed, artifactForwardPeerTimeout)
		}
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}
