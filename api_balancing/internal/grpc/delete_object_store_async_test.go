package grpc

import (
	"context"
	"database/sql"
	"sync"
	"testing"
	"time"

	"frameworks/api_balancing/internal/artifacts"
	"frameworks/api_balancing/internal/control"

	"github.com/DATA-DOG/go-sqlmock"
	sharedpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/shared"
)

// hangingObjectStore never answers a delete until release is closed or the
// delete's context ends.
type hangingObjectStore struct {
	once    sync.Once
	called  chan struct{}
	release chan struct{}
}

func newHangingObjectStore(t *testing.T) *hangingObjectStore {
	t.Helper()
	s := &hangingObjectStore{called: make(chan struct{}), release: make(chan struct{})}
	t.Cleanup(func() { close(s.release) })
	return s
}

func (s *hangingObjectStore) hang(ctx context.Context) error {
	s.once.Do(func() { close(s.called) })
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-s.release:
		return nil
	}
}

func (s *hangingObjectStore) Delete(ctx context.Context, _ string) error { return s.hang(ctx) }

func (s *hangingObjectStore) DeletePrefix(ctx context.Context, _ string) (int, error) {
	return 0, s.hang(ctx)
}

func (s *hangingObjectStore) ParseS3URL(string) (string, error) { return "", nil }

// A DVR delete answers once its durable soft-delete commits. Freeing the
// recording's objects runs after the answer, so an object store that does not
// answer neither delays nor fails a delete that already happened; the purge
// job frees whatever that pass leaves behind.
func TestDeleteDVR_HangingObjectStoreDoesNotHoldTheDelete(t *testing.T) {
	srv, mock := newLifecycleServer(t)
	captureCatalogDirty(t)
	prevControlDB := control.GetDB()
	control.SetDB(srv.db)
	t.Cleanup(func() { control.SetDB(prevControlDB) })
	store := newHangingObjectStore(t)
	srv.SetArtifactCleaner(&artifacts.Cleaner{LocalCluster: "cluster-a", S3: store, LocalBackendID: "backend-1"})

	mock.ExpectQuery(`FROM foghorn.artifacts\s+WHERE artifact_hash = \$1\s+AND artifact_type = 'dvr'`).
		WithArgs("dvr-h", "tenant-a").
		WillReturnRows(sqlmock.NewRows([]string{
			"status", "stream_internal_name", "size_bytes", "retention_until", "started_at", "ended_at",
			"tenant_id", "user_id", "storage_cluster_id", "origin_cluster_id", "active_object_key",
			"active_dtsh_key", "sync_object_key", "durable_backend_local", "backend_id",
		}).AddRow("completed", "live+stream-1", nil, nil, nil, nil, "tenant-a", "user-1", nil, nil, nil, nil, nil, true, "backend-1"))
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

	type result struct {
		resp *sharedpb.DeleteDVRResponse
		err  error
	}
	done := make(chan result, 1)
	go func() {
		resp, err := srv.DeleteDVR(context.Background(), &sharedpb.DeleteDVRRequest{DvrHash: "dvr-h", TenantId: "tenant-a"})
		done <- result{resp, err}
	}()
	select {
	case r := <-done:
		if r.err != nil || !r.resp.GetSuccess() {
			t.Fatalf("DeleteDVR: resp=%+v err=%v", r.resp, r.err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("DeleteDVR waited on an object store that does not answer after its delete committed")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
	select {
	case <-store.called:
	case <-time.After(5 * time.Second):
		t.Fatal("the deleted recording's objects were never deleted")
	}
}
