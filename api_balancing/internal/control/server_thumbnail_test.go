package control

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
	"github.com/sirupsen/logrus"
)

// The register-before-mint fence gates whether a LIVE thumbnail upload is minted: only a positive registration
// (registered=true) allows it. A refusal (registered=false — the stream was deleted, deletion won the serialize race)
// or a registration error must DROP the upload, so no object is created that a later deletion cannot reach. The
// registration dependency is faked through the registerThumbnailServingCellFn seam.
func TestLiveThumbnailMintMayProceed(t *testing.T) {
	orig := registerThumbnailServingCellFn
	defer func() { registerThumbnailServingCellFn = orig }()
	logger := logging.Logger(logrus.New())

	// Distinct stream ids per case: a SUCCESS is cached, so reusing an id would short-circuit later cases.
	registerThumbnailServingCellFn = func(_ context.Context, _, _, _ string) (bool, error) { return true, nil }
	if !liveThumbnailMintMayProceed("stream-ok", "tenant-1", "cluster-1", logger) {
		t.Fatal("a successful registration must allow the mint")
	}
	registerThumbnailServingCellFn = func(_ context.Context, _, _, _ string) (bool, error) { return false, nil }
	if liveThumbnailMintMayProceed("stream-refused", "tenant-1", "cluster-1", logger) {
		t.Fatal("a refused registration (deleted stream) must block the mint")
	}
	registerThumbnailServingCellFn = func(_ context.Context, _, _, _ string) (bool, error) { return false, errors.New("transport") }
	if liveThumbnailMintMayProceed("stream-err", "tenant-1", "cluster-1", logger) {
		t.Fatal("a registration error must block the mint (fail closed)")
	}

	// A cached success is NOT re-registered: after one success for a stream, a subsequent call returns true even
	// though the seam would now refuse (cache hit skips the RPC).
	registerThumbnailServingCellFn = func(_ context.Context, _, _, _ string) (bool, error) { return true, nil }
	if !liveThumbnailMintMayProceed("stream-cached", "tenant-1", "cluster-1", logger) {
		t.Fatal("first registration must succeed")
	}
	registerThumbnailServingCellFn = func(_ context.Context, _, _, _ string) (bool, error) {
		t.Error("a cached stream must NOT re-register on the next mint")
		return false, nil
	}
	if !liveThumbnailMintMayProceed("stream-cached", "tenant-1", "cluster-1", logger) {
		t.Fatal("a cached registration must allow the mint without re-registering")
	}
}

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

// The publish-path projection COMPUTES each source version key from the winning publishToken (the just-loaded attempt
// carries no in-memory version_key), then the pure copy core writes it to the deterministic served key — reporting
// success only when every file copied. This exercises the token-source computation + copy together, so a regression to
// an empty source key is caught.
func TestProjectThumbnail_ComputesSourceFromTokenAndCopies(t *testing.T) {
	m := &mockS3Client{}
	objs := thumbnailObjectsFromToken("stream-1", "tok-9", []string{"poster.jpg", "sprite.jpg"})
	ok := copyThumbnailObjectsToDeterministic(context.Background(), m, "stream-1", objs, logging.NewLoggerWithService("test"))
	if !ok {
		t.Fatal("expected all files copied")
	}

	m.mu.Lock()
	got := append([]string(nil), m.promoteCalls...)
	m.mu.Unlock()
	want := map[string]bool{
		ThumbnailVersionKey("stream-1", "tok-9", "poster.jpg") + "->" + ThumbnailDeterministicKey("stream-1", "poster.jpg"): false,
		ThumbnailVersionKey("stream-1", "tok-9", "sprite.jpg") + "->" + ThumbnailDeterministicKey("stream-1", "sprite.jpg"): false,
	}
	for _, c := range got {
		if _, ok := want[c]; !ok {
			t.Fatalf("unexpected/empty-source projection %q (want computed version keys)", c)
		}
		want[c] = true
	}
	for c, seen := range want {
		if !seen {
			t.Fatalf("missing deterministic projection %q (got %v)", c, got)
		}
	}
}

// A source version object that isn't readable (missing/zero-etag) is skipped, and the copy core reports NOT-complete so
// the fenced caller commits nothing and leaves the attempt for recovery rather than exposing has_thumbnails.
func TestProjectThumbnail_SkipsUnreadableSourceAndReportsIncomplete(t *testing.T) {
	m := &mockS3Client{
		headObjectInfoFn: func(context.Context, string) (bool, int64, string, error) {
			return false, 0, "", nil // source not present
		},
	}
	objs := thumbnailObjectsFromToken("stream-2", "tok", []string{"poster.jpg"})
	ok := copyThumbnailObjectsToDeterministic(context.Background(), m, "stream-2", objs, logging.NewLoggerWithService("test"))

	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.promoteCalls) != 0 {
		t.Fatalf("must not copy when the source is unreadable, got %v", m.promoteCalls)
	}
	if ok {
		t.Fatal("an unreadable source must report the copy INCOMPLETE (leave for recovery)")
	}
}
