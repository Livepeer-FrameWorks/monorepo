package control

import (
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
	"github.com/sirupsen/logrus"
)

// A sidecar declaring a control-protocol version below the staged-thumbnail minimum is refused outright: no
// attempt is minted, no presigned URLs are handed back (strict gate, no legacy fixed-key path).
func TestProcessThumbnailUploadRequest_ProtocolGateRefusesOldSidecar(t *testing.T) {
	stream := &captureStream{}
	processThumbnailUploadRequest(
		"req-old",
		&ipcpb.ThumbnailUploadRequest{InternalName: "live+x", FilePaths: []string{"/tmp/poster.jpg"}},
		"node-1",
		ThumbnailStagedProtocolMin-1,
		stream,
		logging.Logger(logrus.New()),
	)
	if msg := stream.lastSent(); msg != nil {
		t.Fatalf("a sub-min protocol version must be refused with no response, got %T", msg.GetPayload())
	}
}

// expectLoadThumbnailAttempt sets up the two LoadThumbnailAttempt queries (assignment + objects) for a mock DB.
func expectLoadThumbnailAttempt(mock sqlmock.Sqlmock, attempt, tenant, asset, node, status string) {
	mock.ExpectQuery(`SELECT attempt_id, tenant_id, asset_key, node_id, destination_cluster, status, version, expiry\s+FROM foghorn\.thumbnail_task_assignment`).
		WithArgs(attempt).
		WillReturnRows(sqlmock.NewRows([]string{"attempt_id", "tenant_id", "asset_key", "node_id", "destination_cluster", "status", "version", "expiry"}).
			AddRow(attempt, tenant, asset, node, "cluster-a", status, attempt, time.Now().Add(time.Hour)))
	mock.ExpectQuery(`SELECT file_name, staging_key, version_key, etag, size_bytes, verified\s+FROM foghorn\.thumbnail_task_object`).
		WithArgs(attempt).
		WillReturnRows(sqlmock.NewRows([]string{"file_name", "staging_key", "version_key", "etag", "size_bytes", "verified"}).
			AddRow("poster.jpg", ThumbnailStagingKey(asset, attempt, "poster.jpg"), "", "", int64(0), false))
}

// A confirmation with no attempt_id is dropped before any DB access — there is no legacy fixed-key completion.
func TestProcessThumbnailUploaded_NoAttemptIdDropped(t *testing.T) {
	mockDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	prevDB := db
	db = mockDB
	t.Cleanup(func() { db = prevDB; mockDB.Close() })

	processThumbnailUploaded(&ipcpb.ThumbnailUploaded{
		ThumbnailKey: "asset-1",
		S3Keys:       []string{"thumbnails/asset-1/.staging/att/poster.jpg"},
	}, "node-1", logging.NewLoggerWithService("test"))

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err) // no query should have run
	}
}

// A confirmation from a node that is NOT the attempt's assigned node is dropped after the bind check — no verify,
// no promote, no publish, no cache side effect.
func TestProcessThumbnailUploaded_ForeignNodeDropped(t *testing.T) {
	mockDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	prevDB := db
	db = mockDB
	t.Cleanup(func() { db = prevDB; mockDB.Close() })

	attempt, asset := "att-foreign", "asset-1"
	expectLoadThumbnailAttempt(mock, attempt, "tenant-1", asset, "assigned-node", "assigned")
	// No further queries: the reporting node != assigned node → dropped.

	processThumbnailUploaded(&ipcpb.ThumbnailUploaded{
		ThumbnailKey: asset,
		AttemptId:    attempt,
	}, "attacker-node", logging.NewLoggerWithService("test"))

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// A confirmation for an already-published attempt is an idempotent no-op: it loads the assignment, sees the
// terminal state, and returns without re-verifying, re-publishing, or busting the cache.
func TestProcessThumbnailUploaded_AlreadyPublishedIdempotent(t *testing.T) {
	mockDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	prevDB := db
	db = mockDB
	t.Cleanup(func() { db = prevDB; mockDB.Close() })

	attempt, asset := "att-done", "asset-1"
	expectLoadThumbnailAttempt(mock, attempt, "tenant-1", asset, "assigned-node", "published")
	// No further queries: status 'published' → idempotent return.

	processThumbnailUploaded(&ipcpb.ThumbnailUploaded{
		ThumbnailKey: asset,
		AttemptId:    attempt,
	}, "assigned-node", logging.NewLoggerWithService("test"))

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
