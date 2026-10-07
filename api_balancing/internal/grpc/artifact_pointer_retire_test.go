package grpc

import (
	"context"
	"database/sql"
	"testing"

	"frameworks/api_balancing/internal/federation"

	"github.com/DATA-DOG/go-sqlmock"
	sharedpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/shared"
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
