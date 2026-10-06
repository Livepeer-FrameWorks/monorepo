package grpc

import (
	"context"
	"database/sql"
	"sync/atomic"
	"testing"

	"frameworks/api_balancing/internal/control"

	"github.com/DATA-DOG/go-sqlmock"
	sharedpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/shared"
)

// captureCatalogDirty counts NotifyCatalogDirty kicks of the catalog projection.
func captureCatalogDirty(t *testing.T) *int32 {
	t.Helper()
	var kicks int32
	control.SetOnCatalogDirty(func() { atomic.AddInt32(&kicks, 1) })
	t.Cleanup(func() { control.SetOnCatalogDirty(nil) })
	return &kicks
}

// The Commodore catalog drops a deleted artifact's playback IDs only when the
// artifact reconciler projects the deletion. A committed delete kicks that
// projection at once; without the kick the catalog kept listing a deleted DVR
// and its chapters as ready until the next unrelated reconciler pass.
func TestDeleteDVR_CommittedDeleteKicksCatalogProjection(t *testing.T) {
	srv, mock := newLifecycleServer(t)
	kicks := captureCatalogDirty(t)
	prevControlDB := control.GetDB()
	control.SetDB(srv.db)
	t.Cleanup(func() { control.SetDB(prevControlDB) })

	mock.ExpectQuery(`FROM foghorn.artifacts\s+WHERE artifact_hash = \$1\s+AND artifact_type = 'dvr'`).
		WithArgs("dvr-h", "tenant-a").
		WillReturnRows(sqlmock.NewRows([]string{
			"status", "stream_internal_name", "size_bytes", "retention_until", "started_at", "ended_at",
			"tenant_id", "user_id", "storage_cluster_id", "origin_cluster_id", "active_object_key",
			"active_dtsh_key", "sync_object_key", "durable_backend_local", "backend_id",
		}).AddRow("completed", "live+stream-1", nil, nil, nil, nil, "tenant-a", "user-1", nil, nil, nil, nil, nil, false, nil))
	mock.ExpectQuery(`SELECT node_id\s+FROM foghorn.artifact_nodes`).
		WithArgs("dvr-h").
		WillReturnError(sql.ErrNoRows)
	mock.ExpectBegin()
	mock.ExpectExec(`UPDATE foghorn.artifacts SET status = 'deleted'`).
		WithArgs("dvr-h", "tenant-a").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(`UPDATE foghorn.artifacts a SET status = 'deleted'`).
		WithArgs("dvr-h", "tenant-a").
		WillReturnRows(sqlmock.NewRows([]string{"artifact_hash"}))
	mock.ExpectExec(`DELETE FROM foghorn.dvr_chapters`).
		WithArgs("dvr-h", "tenant-a").
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery(`FROM foghorn.artifacts\s+WHERE artifact_hash = \$1 AND artifact_type = 'dvr'`).
		WithArgs("dvr-h").
		WillReturnRows(sqlmock.NewRows([]string{"tenant_id", "user_id", "stream_id", "stream_internal_name", "retention_until", "started_at"}).
			AddRow("tenant-a", "user-1", "stream-1", "live+stream-1", nil, nil))
	mock.ExpectExec(`INSERT INTO foghorn\.artifact_event_outbox`).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`INSERT INTO foghorn\.artifact_event_outbox`).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	resp, err := srv.DeleteDVR(context.Background(), &sharedpb.DeleteDVRRequest{DvrHash: "dvr-h", TenantId: "tenant-a"})
	if err != nil || !resp.GetSuccess() {
		t.Fatalf("DeleteDVR: resp=%+v err=%v", resp, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
	if got := atomic.LoadInt32(kicks); got != 1 {
		t.Fatalf("catalog projection kicks after DVR delete = %d, want 1", got)
	}
}
