package control

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"

	"github.com/DATA-DOG/go-sqlmock"
)

func TestProcessSyncComplete_NilRepo_EarlyReturn(t *testing.T) {
	mock, _, _ := setupArtifactTestDeps(t)
	artifactRepo = nil // override
	logger := logging.NewLogger()

	processSyncComplete(&ipcpb.SyncComplete{
		AssetHash: "hash-1",
		Status:    "success",
	}, "node-1", logger)

	// No DB expectations set — should not query
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

const metadataSelectRe = "SELECT COALESCE\\(artifact_type,''\\), COALESCE\\(stream_internal_name,''\\), COALESCE\\(format,''\\), COALESCE\\(tenant_id::text,''\\), COALESCE\\(stream_id::text,''\\), COALESCE\\(sync_object_key,''\\), COALESCE\\(sync_status,''\\), COALESCE\\(active_object_key,''\\)"

// expectMetadata queues the attempt-SCOPED pre-transaction identity read. The read matches the row only
// when the echoed request id ($2) + authenticated node ($3) equal the persisted main/dtsh attempt, so the
// helper takes them explicitly. syncStatus drives the publication branch (in_progress → main upload);
// activeKey is the row's CURRENT active_object_key (empty on a first sync; the previous version on re-sync).
// The three trailing (optional) values are the row's CURRENT active_dtsh_key, s3_url, and dtsh_sync_request_id
// — empty by default (first sync). Tests exercising the incremental-.dtsh supersede, the legacy s3_url
// recovery, or the overlapping-incremental-.dtsh clear pass them.
func expectMetadata(mock sqlmock.Sqlmock, hash, requestID, node, artifactType, internalName, format, tenant, streamID, activeKey, syncStatus, objectKey string, extra ...string) {
	activeDtshKey, s3URL, dtshReq := "", "", ""
	if len(extra) > 0 {
		activeDtshKey = extra[0]
	}
	if len(extra) > 1 {
		s3URL = extra[1]
	}
	if len(extra) > 2 {
		dtshReq = extra[2]
	}
	// The pre-read is tenant-scoped ($2 = the identity-resolved owner tenant, "tenant-1" in these tests),
	// then attempt+node scoped ($3/$4).
	mock.ExpectQuery(metadataSelectRe).WithArgs(hash, "tenant-1", requestID, node).
		WillReturnRows(sqlmock.NewRows([]string{"artifact_type", "stream_internal_name", "format", "tenant_id", "stream_id", "sync_object_key", "sync_status", "active_object_key", "active_dtsh_key", "s3_url", "dtsh_sync_request_id"}).
			AddRow(artifactType, internalName, format, tenant, streamID, objectKey, syncStatus, activeKey, activeDtshKey, s3URL, dtshReq))
}

// expectLedgerRecord queues the standalone (out-of-tx) publication-ledger INSERT that precedes each promote —
// one statement per attempt-object pair (staging + candidate). The staging key pins it; the rest is loose.
func expectLedgerRecord(mock sqlmock.Sqlmock, stagingKey string) {
	mock.ExpectExec("INSERT INTO foghorn.freeze_publication_ledger").
		WithArgs(stagingKey, sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 1))
}

// expectLedgerClear queues the in-tx publication-ledger DELETE a committing completion issues for its own keys.
func expectLedgerClear(mock sqlmock.Sqlmock) {
	mock.ExpectExec("DELETE FROM foghorn.freeze_publication_ledger").WillReturnResult(sqlmock.NewResult(0, 1))
}

// stagingHead returns a headObjectInfoFn that reports the MAIN staging object (…mp4.staging.<attempt>) as
// present at mainSize, and the .dtsh staging object (…mp4.dtsh.staging.<attempt>) per dtshPresent/dtshSize.
func stagingHead(mainSize int64, dtshPresent bool, dtshSize int64) func(context.Context, string) (bool, int64, string, error) {
	return func(_ context.Context, key string) (bool, int64, string, error) {
		if strings.Contains(key, ".dtsh.staging.") {
			return dtshPresent, dtshSize, "etag-dtsh", nil
		}
		return true, mainSize, "etag-main", nil
	}
}

// expectNodeCopyGained queues the AddCachedNodeCopyTx sequence for a freshly-present cache copy,
// including the durable node-copy outbox write.
func expectNodeCopyGained(mock sqlmock.Sqlmock, hash, node string, size ...int64) {
	sz := int64(0)
	if len(size) > 0 {
		sz = size[0]
	}
	mock.ExpectQuery("SELECT role, is_orphaned, is_complete FROM foghorn.artifact_nodes").
		WithArgs(hash, node).
		WillReturnRows(sqlmock.NewRows([]string{"role", "is_orphaned", "is_complete"}))
	mock.ExpectQuery("INSERT INTO foghorn.artifact_nodes").
		WithArgs(hash, node, "", sz).
		WillReturnRows(sqlmock.NewRows([]string{"?column?", "size_bytes"}).AddRow(true, int64(0)))
	mock.ExpectQuery("SELECT tenant_id::text FROM foghorn.artifacts").
		WithArgs(hash).
		WillReturnRows(sqlmock.NewRows([]string{"tenant_id"}).AddRow("tenant-1"))
	mock.ExpectQuery("SELECT nextval\\('foghorn.artifact_node_copy_version_seq'\\)").
		WillReturnRows(sqlmock.NewRows([]string{"nextval"}).AddRow(int64(1)))
	mock.ExpectExec("UPDATE foghorn.artifact_nodes SET last_emitted_version").
		WithArgs(int64(1), hash, node).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("INSERT INTO foghorn.artifact_event_outbox").
		WillReturnResult(sqlmock.NewResult(0, 1))
}

// A successful main-upload completion verifies + PROMOTES the staging object, then applies the guarded
// transaction (UPDATE, node copy, vod_metadata) atomically. The default mock reports staging present at
// verifiedMockObjectSize (1024), so the promoted size is 1024.
func TestProcessSyncComplete_Success_AppliesGuardedTransaction(t *testing.T) {
	mock, s3Mock, _ := setupArtifactTestDeps(t)
	logger := logging.NewLogger()
	s3Mock.headObjectInfoFn = stagingHead(1024, true, 1024)

	expectMetadata(mock, "hash-1", "req-1", "node-1", "vod", "vod-int", "mp4", "tenant-1", "", "", "in_progress", "tenant-1/vods/hash-1/hash-1.mp4")
	// Each attempt-object pair (staging + candidate) is durably recorded in the publication ledger BEFORE it is
	// promoted (main, then bundled .dtsh).
	expectLedgerRecord(mock, FreezeStagingKey("tenant-1/vods/hash-1/hash-1.mp4", "req-1"))
	expectLedgerRecord(mock, FreezeStagingKey("tenant-1/vods/hash-1/hash-1.mp4.dtsh", "req-1"))
	mock.ExpectBegin()
	// Publication flips active_object_key ($9) and s3_url to the fresh version key (candidate + ".att-" + req),
	// and active_dtsh_key ($10) to the version-addressed .dtsh candidate when the upload bundled it.
	mock.ExpectExec("UPDATE foghorn.artifacts.*sync_status = 'synced'.*active_object_key = COALESCE.*sync_status = 'in_progress'").
		WithArgs("s3://bucket/tenant-1/vods/hash-1/hash-1.mp4.att-req-1", true, "hash-1", int64(1024), "req-1", "node-1", "tenant-1", "tenant-1/vods/hash-1/hash-1.mp4", "tenant-1/vods/hash-1/hash-1.mp4.att-req-1", FreezePublishDtshKey("tenant-1/vods/hash-1/hash-1.mp4", "req-1")).
		WillReturnResult(sqlmock.NewResult(0, 1))
	expectNodeCopyGained(mock, "hash-1", "node-1", 1024)
	mock.ExpectExec("INSERT INTO foghorn.vod_metadata").
		WithArgs("hash-1", "tenant-1/vods/hash-1/hash-1.mp4.att-req-1", sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("INSERT INTO foghorn.artifact_event_outbox").WillReturnResult(sqlmock.NewResult(0, 1))
	// Both staging objects are durably enqueued for deletion IN the transaction (main then .dtsh).
	mock.ExpectExec("INSERT INTO foghorn.staging_cleanup_queue").
		WithArgs(FreezeStagingKey("tenant-1/vods/hash-1/hash-1.mp4", "req-1")).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("INSERT INTO foghorn.staging_cleanup_queue").
		WithArgs(FreezeStagingKey("tenant-1/vods/hash-1/hash-1.mp4.dtsh", "req-1")).
		WillReturnResult(sqlmock.NewResult(0, 1))
	expectLedgerClear(mock)
	mock.ExpectCommit()

	processSyncComplete(&ipcpb.SyncComplete{
		AssetHash: "hash-1", Status: "success", RequestId: "req-1", DtshIncluded: true,
	}, "node-1", logger)

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
	// The main object AND the .dtsh index were each published (promoted staging → fresh version key).
	s3Mock.mu.Lock()
	defer s3Mock.mu.Unlock()
	if len(s3Mock.promoteCalls) != 2 {
		t.Fatalf("expected 2 promotions (main + dtsh), got %d", len(s3Mock.promoteCalls))
	}
}

// A main upload that BUNDLES a .dtsh while an OVERLAPPING incremental .dtsh attempt (a different request id)
// is in-flight: the guarded UPDATE clears that overlapping identity, so the completion must enqueue the
// overlapping attempt's .dtsh staging AND its versioned candidate — otherwise those objects leak once the
// identity is gone. Its own bundled .dtsh (req-1) keys are enqueued/kept separately.
func TestProcessSyncComplete_BundledDtshEnqueuesOverlappingIncrementalAttempt(t *testing.T) {
	mock, s3Mock, _ := setupArtifactTestDeps(t)
	logger := logging.NewLogger()
	s3Mock.headObjectInfoFn = stagingHead(1024, true, 1024)

	obj := "tenant-1/vods/hash-1/hash-1.mp4"
	// extra: active_dtsh_key="", s3_url="", dtsh_sync_request_id="old-dtsh" (the overlapping in-flight attempt).
	expectMetadata(mock, "hash-1", "req-1", "node-1", "vod", "vod-int", "mp4", "tenant-1", "", "", "in_progress", obj, "", "", "old-dtsh")
	expectLedgerRecord(mock, FreezeStagingKey(obj, "req-1"))
	expectLedgerRecord(mock, FreezeStagingKey(obj+".dtsh", "req-1"))
	mock.ExpectBegin()
	mock.ExpectExec("UPDATE foghorn.artifacts.*sync_status = 'synced'.*active_object_key = COALESCE.*sync_status = 'in_progress'").
		WithArgs("s3://bucket/"+obj+".att-req-1", true, "hash-1", int64(1024), "req-1", "node-1", "tenant-1", obj, obj+".att-req-1", FreezePublishDtshKey(obj, "req-1")).
		WillReturnResult(sqlmock.NewResult(0, 1))
	// The overlapping incremental .dtsh attempt's staging + candidate are enqueued (before the node copy).
	mock.ExpectExec("INSERT INTO foghorn.staging_cleanup_queue").
		WithArgs(FreezeStagingKey(obj+".dtsh", "old-dtsh")).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("INSERT INTO foghorn.staging_cleanup_queue").
		WithArgs(FreezePublishDtshKey(obj, "old-dtsh")).
		WillReturnResult(sqlmock.NewResult(0, 1))
	expectNodeCopyGained(mock, "hash-1", "node-1", 1024)
	mock.ExpectExec("INSERT INTO foghorn.vod_metadata").
		WithArgs("hash-1", obj+".att-req-1", sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("INSERT INTO foghorn.artifact_event_outbox").WillReturnResult(sqlmock.NewResult(0, 1))
	// This attempt's OWN main + bundled .dtsh staging objects (req-1) are enqueued last.
	mock.ExpectExec("INSERT INTO foghorn.staging_cleanup_queue").
		WithArgs(FreezeStagingKey(obj, "req-1")).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("INSERT INTO foghorn.staging_cleanup_queue").
		WithArgs(FreezeStagingKey(obj+".dtsh", "req-1")).
		WillReturnResult(sqlmock.NewResult(0, 1))
	expectLedgerClear(mock)
	mock.ExpectCommit()

	processSyncComplete(&ipcpb.SyncComplete{
		AssetHash: "hash-1", Status: "success", RequestId: "req-1", DtshIncluded: true,
	}, "node-1", logger)

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// A staged object with size 0 is REFUSED for clip/VOD (a durable media object is never empty): no
// transaction opens, no promotion, the attempt stays in_progress.
func TestProcessSyncComplete_ZeroSizeRefused(t *testing.T) {
	mock, s3Mock, _ := setupArtifactTestDeps(t)
	logger := logging.NewLogger()
	s3Mock.headObjectInfoFn = func(context.Context, string) (bool, int64, string, error) { return true, 0, "e", nil }

	expectMetadata(mock, "hash-1", "req-1", "node-1", "clip", "s-int", "mp4", "tenant-1", "", "", "in_progress", "tenant-1/s-int/clips/hash-1.mp4")
	// No ExpectBegin: a 0-byte staged object fails closed.
	processSyncComplete(&ipcpb.SyncComplete{AssetHash: "hash-1", Status: "success", RequestId: "req-1"}, "node-1", logger)

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
	if len(s3Mock.promoteCalls) != 0 {
		t.Fatal("must not promote a 0-byte object")
	}
}

// dtsh_included but the .dtsh index absent → the main object still promotes but dtsh is NOT finalized
// ($2 = dtsh_synced = false).
func TestProcessSyncComplete_DtshAbsentNotFinalized(t *testing.T) {
	mock, s3Mock, _ := setupArtifactTestDeps(t)
	logger := logging.NewLogger()
	s3Mock.headObjectInfoFn = stagingHead(2048, false, 0) // main present @2048; .dtsh absent

	expectMetadata(mock, "hash-1", "req-1", "node-1", "vod", "vod-int", "mp4", "tenant-1", "", "", "in_progress", "tenant-1/vods/hash-1/hash-1.mp4")
	// Only the MAIN pair is recorded — the .dtsh is absent so it is never promoted (nor ledger-recorded).
	expectLedgerRecord(mock, FreezeStagingKey("tenant-1/vods/hash-1/hash-1.mp4", "req-1"))
	mock.ExpectBegin()
	mock.ExpectExec("UPDATE foghorn.artifacts.*sync_status = 'synced'.*sync_status = 'in_progress'").
		WithArgs("s3://bucket/tenant-1/vods/hash-1/hash-1.mp4.att-req-1", false, "hash-1", int64(2048), "req-1", "node-1", "tenant-1", "tenant-1/vods/hash-1/hash-1.mp4", "tenant-1/vods/hash-1/hash-1.mp4.att-req-1", "").
		WillReturnResult(sqlmock.NewResult(0, 1))
	expectNodeCopyGained(mock, "hash-1", "node-1", 2048)
	mock.ExpectExec("INSERT INTO foghorn.vod_metadata").
		WithArgs("hash-1", "tenant-1/vods/hash-1/hash-1.mp4.att-req-1", sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("INSERT INTO foghorn.artifact_event_outbox").WillReturnResult(sqlmock.NewResult(0, 1))
	// Only the MAIN staging object is enqueued (the .dtsh was absent, so it was never promoted).
	mock.ExpectExec("INSERT INTO foghorn.staging_cleanup_queue").
		WithArgs(FreezeStagingKey("tenant-1/vods/hash-1/hash-1.mp4", "req-1")).
		WillReturnResult(sqlmock.NewResult(0, 1))
	expectLedgerClear(mock)
	mock.ExpectCommit()

	processSyncComplete(&ipcpb.SyncComplete{
		AssetHash: "hash-1", Status: "success", RequestId: "req-1", DtshIncluded: true,
	}, "node-1", logger)

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// A bundled .dtsh whose PROMOTE fails (or is invalid/zero-byte, or whose ledger write fails) downgrades
// dtsh_synced but must NOT leak the already-uploaded .dtsh staging object: because the staging was HEAD-verified
// present, it is scheduled for cleanup and the committing main completion enqueues it alongside the main staging.
func TestProcessSyncComplete_DtshPromoteFailsStillCleansDtshStaging(t *testing.T) {
	mock, s3Mock, _ := setupArtifactTestDeps(t)
	logger := logging.NewLogger()
	s3Mock.headObjectInfoFn = stagingHead(1024, true, 1024) // main + .dtsh staging both present
	obj := "tenant-1/vods/hash-1/hash-1.mp4"
	// Main promote succeeds; the .dtsh promote fails.
	s3Mock.promoteObjectFn = func(_ context.Context, _, dst, _ string) error {
		if strings.Contains(dst, ".dtsh.att-") {
			return errors.New("precondition failed")
		}
		return nil
	}

	expectMetadata(mock, "hash-1", "req-1", "node-1", "vod", "vod-int", "mp4", "tenant-1", "", "", "in_progress", obj)
	// Both pairs recorded before their promotes (the .dtsh candidate's promote then fails).
	expectLedgerRecord(mock, FreezeStagingKey(obj, "req-1"))
	expectLedgerRecord(mock, FreezeStagingKey(obj+".dtsh", "req-1"))
	mock.ExpectBegin()
	// dtsh_synced ($2) is FALSE (downgraded); $10 (publishDtshKey) empty.
	mock.ExpectExec("UPDATE foghorn.artifacts.*sync_status = 'synced'.*sync_status = 'in_progress'").
		WithArgs("s3://bucket/"+obj+".att-req-1", false, "hash-1", int64(1024), "req-1", "node-1", "tenant-1", obj, obj+".att-req-1", "").
		WillReturnResult(sqlmock.NewResult(0, 1))
	expectNodeCopyGained(mock, "hash-1", "node-1", 1024)
	mock.ExpectExec("INSERT INTO foghorn.vod_metadata").
		WithArgs("hash-1", obj+".att-req-1", sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("INSERT INTO foghorn.artifact_event_outbox").WillReturnResult(sqlmock.NewResult(0, 1))
	// BOTH staging objects are enqueued — the main and the .dtsh (present-but-not-finalized) — so neither leaks.
	mock.ExpectExec("INSERT INTO foghorn.staging_cleanup_queue").
		WithArgs(FreezeStagingKey(obj, "req-1")).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("INSERT INTO foghorn.staging_cleanup_queue").
		WithArgs(FreezeStagingKey(obj+".dtsh", "req-1")).
		WillReturnResult(sqlmock.NewResult(0, 1))
	expectLedgerClear(mock)
	mock.ExpectCommit()

	processSyncComplete(&ipcpb.SyncComplete{
		AssetHash: "hash-1", Status: "success", RequestId: "req-1", DtshIncluded: true,
	}, "node-1", logger)

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// A staged object that is ABSENT is REFUSED before any durable promotion: the node reported success
// without a verified upload. No transaction, no promotion.
func TestProcessSyncComplete_UnverifiedObjectRefused(t *testing.T) {
	mock, s3Mock, _ := setupArtifactTestDeps(t)
	logger := logging.NewLogger()
	s3Mock.headObjectInfoFn = func(context.Context, string) (bool, int64, string, error) { return false, 0, "", nil }

	expectMetadata(mock, "hash-1", "req-1", "node-1", "clip", "s-int", "mp4", "tenant-1", "", "", "in_progress", "tenant-1/s-int/clips/hash-1.mp4")
	processSyncComplete(&ipcpb.SyncComplete{AssetHash: "hash-1", Status: "success", RequestId: "req-1"}, "node-1", logger)

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
	if len(s3Mock.promoteCalls) != 0 {
		t.Fatal("must not promote an absent object")
	}
}

// A transient HEAD failure is RETRYABLE (no transaction, left in_progress).
func TestProcessSyncComplete_VerificationHeadErrorRetryable(t *testing.T) {
	mock, s3Mock, _ := setupArtifactTestDeps(t)
	logger := logging.NewLogger()
	s3Mock.headObjectInfoFn = func(context.Context, string) (bool, int64, string, error) {
		return false, 0, "", errors.New("head timeout")
	}

	expectMetadata(mock, "hash-1", "req-1", "node-1", "clip", "s-int", "mp4", "tenant-1", "", "", "in_progress", "tenant-1/s-int/clips/hash-1.mp4")
	processSyncComplete(&ipcpb.SyncComplete{AssetHash: "hash-1", Status: "success", RequestId: "req-1"}, "node-1", logger)

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// A failed publication copy (staging overwritten mid-flight / transient) is RETRYABLE — never a false
// success. Publication runs OUTSIDE the transaction to a fresh candidate key; on failure the attempt stays
// in_progress and the (possibly-partial) candidate is durably enqueued for cleanup. No transaction opens.
func TestProcessSyncComplete_PublicationFailureRetryable(t *testing.T) {
	mock, s3Mock, _ := setupArtifactTestDeps(t)
	logger := logging.NewLogger()
	s3Mock.headObjectInfoFn = stagingHead(1024, true, 1024)
	s3Mock.promoteObjectFn = func(context.Context, string, string, string) error { return errors.New("precondition failed") }

	expectMetadata(mock, "hash-1", "req-1", "node-1", "clip", "s-int", "mp4", "tenant-1", "", "", "in_progress", "tenant-1/s-int/clips/hash-1.mp4")
	// The publication ledger records the pair BEFORE the promote; then the promote FAILS. No transaction opens.
	// The ledger row survives — the sweep is req-aware and leaves it while this attempt is still retrying, so a
	// retry re-publishes to the same candidate without a cleanup race.
	expectLedgerRecord(mock, FreezeStagingKey("tenant-1/s-int/clips/hash-1.mp4", "req-1"))

	processSyncComplete(&ipcpb.SyncComplete{AssetHash: "hash-1", Status: "success", RequestId: "req-1"}, "node-1", logger)

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
	if len(s3Mock.promoteCalls) != 1 {
		t.Fatalf("expected exactly one publication attempt, got %d", len(s3Mock.promoteCalls))
	}
}

// A completion whose pre-read matched and whose staging verified, but whose guarded CAS then affects ZERO
// rows (retired / a concurrent completion won mid-flight), publishes to a FRESH candidate key that is never
// referenced (the pointer flip is rejected). The losing attempt enqueues that orphan candidate for cleanup
// and never overwrites the winner's object.
func TestProcessSyncComplete_LostCAS_OrphanCandidateCleanedUp(t *testing.T) {
	mock, s3Mock, _ := setupArtifactTestDeps(t)
	logger := logging.NewLogger()
	s3Mock.headObjectInfoFn = stagingHead(1024, false, 0) // main staging verifies; no dtsh

	expectMetadata(mock, "hash-1", "req-1", "node-1", "clip", "s-int", "mp4", "tenant-1", "", "", "in_progress", "tenant-1/s-int/clips/hash-1.mp4")
	// The pair is recorded in the publication ledger BEFORE the promote.
	expectLedgerRecord(mock, FreezeStagingKey("tenant-1/s-int/clips/hash-1.mp4", "req-1"))
	mock.ExpectBegin()
	// The guarded CAS affects ZERO rows: this attempt lost the race, so the pointer is NOT flipped. Nothing is
	// cleaned inline — the orphaned candidate stays in the ledger for reconcileFreezePublicationLedger to collect
	// durably (req-aware). The transaction simply rolls back.
	mock.ExpectExec("UPDATE foghorn.artifacts.*sync_status = 'synced'.*sync_status = 'in_progress'").
		WithArgs("s3://bucket/tenant-1/s-int/clips/hash-1.mp4.att-req-1", false, "hash-1", int64(1024), "req-1", "node-1", "tenant-1", "tenant-1/s-int/clips/hash-1.mp4", "tenant-1/s-int/clips/hash-1.mp4.att-req-1", "").
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectRollback()

	processSyncComplete(&ipcpb.SyncComplete{AssetHash: "hash-1", Status: "success", RequestId: "req-1"}, "node-1", logger)

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
	if len(s3Mock.promoteCalls) != 1 {
		t.Fatalf("expected exactly one publication (main), got %d", len(s3Mock.promoteCalls))
	}
}

// CRITICAL: a concurrent DUPLICATE of the SAME attempt publishes the SAME candidate; if that duplicate won
// the CAS, this candidate is the LIVE active_object_key. The lost-CAS path must NOT enqueue it for deletion.
func TestProcessSyncComplete_LostCAS_DuplicateSuccess_DoesNotDeleteLiveObject(t *testing.T) {
	mock, s3Mock, _ := setupArtifactTestDeps(t)
	logger := logging.NewLogger()
	s3Mock.headObjectInfoFn = stagingHead(1024, false, 0)

	expectMetadata(mock, "hash-1", "req-1", "node-1", "clip", "s-int", "mp4", "tenant-1", "", "", "in_progress", "tenant-1/s-int/clips/hash-1.mp4")
	expectLedgerRecord(mock, FreezeStagingKey("tenant-1/s-int/clips/hash-1.mp4", "req-1"))
	mock.ExpectBegin()
	mock.ExpectExec("UPDATE foghorn.artifacts.*sync_status = 'synced'.*sync_status = 'in_progress'").
		WithArgs("s3://bucket/tenant-1/s-int/clips/hash-1.mp4.att-req-1", false, "hash-1", int64(1024), "req-1", "node-1", "tenant-1", "tenant-1/s-int/clips/hash-1.mp4", "tenant-1/s-int/clips/hash-1.mp4.att-req-1", "").
		WillReturnResult(sqlmock.NewResult(0, 0))
	// A concurrent duplicate of THIS attempt already committed the SAME candidate as active. The lost-CAS path
	// does nothing inline; the ledger sweep re-reads active pointers and, finding the candidate LIVE, drops the
	// ledger row WITHOUT deleting the object. Roll back.
	mock.ExpectRollback()

	processSyncComplete(&ipcpb.SyncComplete{AssetHash: "hash-1", Status: "success", RequestId: "req-1"}, "node-1", logger)

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// MIXED DUPLICATE: two completions for the same main attempt DISAGREE on dtsh_included. The dtsh_included=false
// completion wins the main CAS (clearing active_dtsh_key), while THIS dtsh_included=true completion has already
// published its .dtsh candidate and then loses the main CAS. The main candidate is LIVE (both published the same
// deterministic key) so it must NOT be deleted — but the .dtsh candidate is orphaned (the winner cleared the
// pointer) and MUST be enqueued. The two candidates are reconciled INDEPENDENTLY.
func TestProcessSyncComplete_LostCAS_MixedDuplicateEnqueuesOrphanedDtshOnly(t *testing.T) {
	mock, s3Mock, _ := setupArtifactTestDeps(t)
	logger := logging.NewLogger()
	s3Mock.headObjectInfoFn = stagingHead(1024, true, 1024) // main + dtsh both staged & promoted

	obj := "tenant-1/s-int/clips/hash-1.mp4"
	expectMetadata(mock, "hash-1", "req-1", "node-1", "clip", "s-int", "mp4", "tenant-1", "", "", "in_progress", obj)
	// BOTH pairs (main + .dtsh) are recorded in the ledger before their promotes.
	expectLedgerRecord(mock, FreezeStagingKey(obj, "req-1"))
	expectLedgerRecord(mock, FreezeStagingKey(obj+".dtsh", "req-1"))
	mock.ExpectBegin()
	// This attempt LOSES the main CAS (0 rows) after publishing BOTH candidates.
	mock.ExpectExec("UPDATE foghorn.artifacts.*sync_status = 'synced'.*sync_status = 'in_progress'").
		WithArgs("s3://bucket/"+obj+".att-req-1", true, "hash-1", int64(1024), "req-1", "node-1", "tenant-1", obj, obj+".att-req-1", FreezePublishDtshKey(obj, "req-1")).
		WillReturnResult(sqlmock.NewResult(0, 0))
	// dtsh_included=true → the dtsh-only branch runs its CAS, which also matches 0 rows. Nothing is cleaned
	// inline; the orphaned .dtsh candidate stays in the ledger, and the req-aware sweep — finding the main
	// candidate LIVE but active_dtsh_key EMPTY (the dtsh_included=false winner cleared it) — collects ONLY the
	// orphaned .dtsh candidate. Roll back.
	mock.ExpectExec("UPDATE foghorn.artifacts.*dtsh_synced = true.*dtsh_sync_request_id = \\$2 AND dtsh_sync_node_id = \\$3.*tenant_id::text = \\$4").
		WithArgs("hash-1", "req-1", "node-1", "tenant-1", FreezePublishDtshKey(obj, "req-1")).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectRollback()

	processSyncComplete(&ipcpb.SyncComplete{AssetHash: "hash-1", Status: "success", RequestId: "req-1", DtshIncluded: true}, "node-1", logger)

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
	if len(s3Mock.promoteCalls) != 2 {
		t.Fatalf("expected 2 publications (main + dtsh), got %d", len(s3Mock.promoteCalls))
	}
}

// Durable state records the PROVIDER-OBSERVED (staging) size, never the node's assertion: $4 = 4242
// (staging size), not the node-reported SizeBytes (100).
func TestProcessSyncComplete_UsesProviderObservedSize(t *testing.T) {
	mock, s3Mock, _ := setupArtifactTestDeps(t)
	logger := logging.NewLogger()
	s3Mock.headObjectInfoFn = stagingHead(4242, true, 10)

	expectMetadata(mock, "hash-1", "req-1", "node-1", "vod", "vod-int", "mp4", "tenant-1", "", "", "in_progress", "tenant-1/vods/hash-1/hash-1.mp4")
	expectLedgerRecord(mock, FreezeStagingKey("tenant-1/vods/hash-1/hash-1.mp4", "req-1"))
	expectLedgerRecord(mock, FreezeStagingKey("tenant-1/vods/hash-1/hash-1.mp4.dtsh", "req-1"))
	mock.ExpectBegin()
	mock.ExpectExec("UPDATE foghorn.artifacts.*sync_status = 'synced'.*sync_status = 'in_progress'").
		WithArgs("s3://bucket/tenant-1/vods/hash-1/hash-1.mp4.att-req-1", true, "hash-1", int64(4242), "req-1", "node-1", "tenant-1", "tenant-1/vods/hash-1/hash-1.mp4", "tenant-1/vods/hash-1/hash-1.mp4.att-req-1", FreezePublishDtshKey("tenant-1/vods/hash-1/hash-1.mp4", "req-1")).
		WillReturnResult(sqlmock.NewResult(0, 1))
	expectNodeCopyGained(mock, "hash-1", "node-1", 4242)
	mock.ExpectExec("INSERT INTO foghorn.vod_metadata").
		WithArgs("hash-1", "tenant-1/vods/hash-1/hash-1.mp4.att-req-1", sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("INSERT INTO foghorn.artifact_event_outbox").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("INSERT INTO foghorn.staging_cleanup_queue").
		WithArgs(FreezeStagingKey("tenant-1/vods/hash-1/hash-1.mp4", "req-1")).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("INSERT INTO foghorn.staging_cleanup_queue").
		WithArgs(FreezeStagingKey("tenant-1/vods/hash-1/hash-1.mp4.dtsh", "req-1")).
		WillReturnResult(sqlmock.NewResult(0, 1))
	expectLedgerClear(mock)
	mock.ExpectCommit()

	processSyncComplete(&ipcpb.SyncComplete{
		AssetHash: "hash-1", Status: "success", SizeBytes: 100, RequestId: "req-1", DtshIncluded: true,
	}, "node-1", logger)

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// Completion promotes the EXACT persisted canonical key (the staging→canonical copy targets it), and
// vod_metadata.s3_key is that key verbatim — never a format-reconstructed key.
func TestProcessSyncComplete_ConsumesPersistedObjectKey(t *testing.T) {
	mock, s3Mock, _ := setupArtifactTestDeps(t)
	logger := logging.NewLogger()
	s3Mock.headObjectInfoFn = stagingHead(1024, true, 1024)

	expectMetadata(mock, "hash-k", "req-k", "node-1", "vod", "vod-int", "mp4", "tenant-1", "", "", "in_progress", "tenant-1/frozen/hash-k.custom")
	expectLedgerRecord(mock, FreezeStagingKey("tenant-1/frozen/hash-k.custom", "req-k"))
	expectLedgerRecord(mock, FreezeStagingKey("tenant-1/frozen/hash-k.custom.dtsh", "req-k"))
	mock.ExpectBegin()
	mock.ExpectExec("UPDATE foghorn.artifacts.*sync_status = 'synced'.*sync_status = 'in_progress'").
		WithArgs("s3://bucket/tenant-1/frozen/hash-k.custom.att-req-k", true, "hash-k", int64(1024), "req-k", "node-1", "tenant-1", "tenant-1/frozen/hash-k.custom", "tenant-1/frozen/hash-k.custom.att-req-k", FreezePublishDtshKey("tenant-1/frozen/hash-k.custom", "req-k")).
		WillReturnResult(sqlmock.NewResult(0, 1))
	expectNodeCopyGained(mock, "hash-k", "node-1", 1024)
	mock.ExpectExec("INSERT INTO foghorn.vod_metadata").
		WithArgs("hash-k", "tenant-1/frozen/hash-k.custom.att-req-k", sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("INSERT INTO foghorn.artifact_event_outbox").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("INSERT INTO foghorn.staging_cleanup_queue").
		WithArgs(FreezeStagingKey("tenant-1/frozen/hash-k.custom", "req-k")).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("INSERT INTO foghorn.staging_cleanup_queue").
		WithArgs(FreezeStagingKey("tenant-1/frozen/hash-k.custom.dtsh", "req-k")).
		WillReturnResult(sqlmock.NewResult(0, 1))
	expectLedgerClear(mock)
	mock.ExpectCommit()

	processSyncComplete(&ipcpb.SyncComplete{
		AssetHash: "hash-k", Status: "success", RequestId: "req-k", DtshIncluded: true,
	}, "node-1", logger)

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// An incremental .dtsh sync runs on an ALREADY-SYNCED artifact (sync_status='synced' at pre-read): no
// staging promotion runs, the main-upload guard matches nothing, and the dtsh transition applies against
// the persisted DTSH attempt.
func TestProcessSyncComplete_DtshOnlyOnSyncedArtifact(t *testing.T) {
	mock, s3Mock, _ := setupArtifactTestDeps(t)
	logger := logging.NewLogger()
	s3Mock.headObjectInfoFn = stagingHead(1024, true, 512) // .dtsh present & non-empty

	// The row is ALREADY synced at active_object_key = <version>; the incremental .dtsh co-locates under it.
	expectMetadata(mock, "hash-d", "req-d", "node-1", "clip", "clip-int", "mp4", "tenant-1", "", "tenant-1/clip-int/clips/hash-d.mp4.att-orig", "synced", "tenant-1/clip-int/clips/hash-d.mp4")
	// No main object promotes on a synced row; only the incremental .dtsh pair is recorded before its promote.
	expectLedgerRecord(mock, FreezeStagingKey("tenant-1/clip-int/clips/hash-d.mp4.dtsh", "req-d"))
	mock.ExpectBegin()
	// Main-upload guard matches nothing; $4 (size)=0 (no main publish on a synced row); $9 (publishMainKey)="";
	// $10 (publishDtshKey) = the version-addressed .dtsh candidate this incremental attempt published.
	mock.ExpectExec("UPDATE foghorn.artifacts.*sync_status = 'in_progress'").
		WithArgs("s3://bucket/tenant-1/clip-int/clips/hash-d.mp4.att-orig", true, "hash-d", int64(0), "req-d", "node-1", "tenant-1", "tenant-1/clip-int/clips/hash-d.mp4", "", FreezePublishDtshKey("tenant-1/clip-int/clips/hash-d.mp4", "req-d")).
		WillReturnResult(sqlmock.NewResult(0, 0))
	// The dtsh-only UPDATE flips active_dtsh_key ($5) to the freshly-published versioned index.
	mock.ExpectExec("UPDATE foghorn.artifacts.*dtsh_synced = true.*dtsh_sync_request_id = \\$2 AND dtsh_sync_node_id = \\$3.*tenant_id::text = \\$4").
		WithArgs("hash-d", "req-d", "node-1", "tenant-1", FreezePublishDtshKey("tenant-1/clip-int/clips/hash-d.mp4", "req-d")).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("INSERT INTO foghorn.artifact_event_outbox").WillReturnResult(sqlmock.NewResult(0, 1))
	// The promoted .dtsh staging object is durably enqueued for deletion IN the dtsh-only transaction.
	mock.ExpectExec("INSERT INTO foghorn.staging_cleanup_queue").
		WithArgs(FreezeStagingKey("tenant-1/clip-int/clips/hash-d.mp4.dtsh", "req-d")).
		WillReturnResult(sqlmock.NewResult(0, 1))
	expectLedgerClear(mock)
	mock.ExpectCommit()

	processSyncComplete(&ipcpb.SyncComplete{
		AssetHash: "hash-d", Status: "success", RequestId: "req-d", DtshIncluded: true,
	}, "node-1", logger)

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// A completion whose echoed attempt id matches NO persisted attempt for this node reads no row: tenant/
// descriptor stay empty and the completion is refused before any transaction (server-derived URL empty).
func TestProcessSyncComplete_UnmatchedAttempt_NoOp(t *testing.T) {
	mock, _, _ := setupArtifactTestDeps(t)
	logger := logging.NewLogger()

	// Attempt-scoped pre-read returns NO row (the echoed id/node match no persisted attempt).
	mock.ExpectQuery(metadataSelectRe).
		WithArgs("hash-1", "tenant-1", "stale-req", "node-1").
		WillReturnError(errors.New("sql: no rows in result set"))
	// No ExpectBegin — refused before any transaction.
	processSyncComplete(&ipcpb.SyncComplete{
		AssetHash: "hash-1", Status: "success", RequestId: "stale-req",
	}, "node-1", logger)

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// A clip/vod completion whose row carries NO persisted sync_object_key must be REFUSED before any
// transaction (the claim binds the descriptor; completion never promotes an unverified key).
func TestProcessSyncComplete_DescriptorlessClipRefused(t *testing.T) {
	mock, _, _ := setupArtifactTestDeps(t)
	logger := logging.NewLogger()

	expectMetadata(mock, "clip-hash", "req-c", "node-1", "clip", "stream-1", "mp4", "tenant-1", "", "", "in_progress", "")
	processSyncComplete(&ipcpb.SyncComplete{
		AssetHash: "clip-hash", Status: "success", RequestId: "req-c",
	}, "node-1", logger)

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// The attempt-scoped identity pre-read fails CLOSED on a genuine query error: rejected BEFORE any
// transaction opens — no promotion, no node copy, no outbox.
func TestProcessSyncComplete_IdentityReadError_FailsClosed(t *testing.T) {
	mock, _, _ := setupArtifactTestDeps(t)
	logger := logging.NewLogger()

	mock.ExpectQuery(metadataSelectRe).
		WithArgs("hash-e", "tenant-1", "req-e", "node-1").
		WillReturnError(errors.New("db down"))
	processSyncComplete(&ipcpb.SyncComplete{
		AssetHash: "hash-e", Status: "success", RequestId: "req-e",
	}, "node-1", logger)

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// Failure path: a DB error while locking the outstanding attempt rejects the failure completion (fail
// closed) — no orphaning, transaction rolled back. The failed path does not use the attempt-scoped read.
func TestProcessSyncComplete_Failed_GuardDBError_FailsClosed(t *testing.T) {
	mock, _, _ := setupArtifactTestDeps(t)
	logger := logging.NewLogger()

	mock.ExpectBegin()
	mock.ExpectQuery("UPDATE foghorn.artifacts.*dtsh_status = 'failed'.*RETURNING").
		WithArgs("hash-fail", "req-x", "node-1", "connection reset", "tenant-1").
		WillReturnRows(sqlmock.NewRows([]string{"sync_object_key"}))
	mock.ExpectRollback()
	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT COALESCE\(tenant_id::text,''\), COALESCE\(sync_object_key,''\) FROM foghorn.artifacts.*sync_status = 'in_progress'`).
		WithArgs("hash-fail", "req-x", "node-1", "tenant-1").
		WillReturnError(errors.New("db down"))
	mock.ExpectRollback()

	processSyncComplete(&ipcpb.SyncComplete{
		AssetHash: "hash-fail", Status: "failed", Error: "connection reset", RequestId: "req-x",
	}, "node-1", logger)

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// Completion attributes the node-copy to the AUTHENTICATED CONNECTION identity ($6 / cache-upsert node arg
// = "fallback-node").
func TestProcessSyncComplete_PayloadNodeIDIgnored_UsesConnectionIdentity(t *testing.T) {
	mock, s3Mock, _ := setupArtifactTestDeps(t)
	logger := logging.NewLogger()
	s3Mock.headObjectInfoFn = stagingHead(1024, false, 0)

	expectMetadata(mock, "hash-1", "", "fallback-node", "clip", "clip-int", "mp4", "tenant-1", "", "", "in_progress", "tenant-1/clip-int/clips/hash-1.mp4")
	expectLedgerRecord(mock, FreezeStagingKey("tenant-1/clip-int/clips/hash-1.mp4", ""))
	mock.ExpectBegin()
	mock.ExpectExec("UPDATE foghorn.artifacts.*sync_status = 'in_progress'").
		WithArgs(sqlmock.AnyArg(), false, "hash-1", int64(1024), "", "fallback-node", "tenant-1", "tenant-1/clip-int/clips/hash-1.mp4", "tenant-1/clip-int/clips/hash-1.mp4.att-", "").
		WillReturnResult(sqlmock.NewResult(0, 1))
	expectNodeCopyGained(mock, "hash-1", "fallback-node", 1024)
	mock.ExpectExec("INSERT INTO foghorn.artifact_event_outbox").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("INSERT INTO foghorn.staging_cleanup_queue").
		WithArgs(FreezeStagingKey("tenant-1/clip-int/clips/hash-1.mp4", "")).
		WillReturnResult(sqlmock.NewResult(0, 1))
	expectLedgerClear(mock)
	mock.ExpectCommit()

	processSyncComplete(&ipcpb.SyncComplete{
		AssetHash: "hash-1", Status: "success",
	}, "fallback-node", logger)

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// Re-publishing a VOD over a PREVIOUS version durably enqueues the superseded object (and its co-located
// .dtsh) for cleanup IN the transaction — no best-effort post-commit DeleteByURL.
func TestProcessSyncComplete_RepublishEnqueuesSupersededObject(t *testing.T) {
	mock, s3Mock, _ := setupArtifactTestDeps(t)
	logger := logging.NewLogger()
	s3Mock.headObjectInfoFn = stagingHead(1024, false, 0)

	// The row is already published at a PREVIOUS version key (active_object_key), which this re-publish
	// supersedes; its .dtsh index is version-addressed at the PREVIOUS active_dtsh_key.
	prev := "tenant-1/vods/vod-hash/vod-hash.mp4.att-prev"
	prevDtsh := "tenant-1/vods/vod-hash/vod-hash.mp4.dtsh.att-prev"
	expectMetadata(mock, "vod-hash", "", "node-1", "vod", "vod-int", "mp4", "tenant-1", "", prev, "in_progress", "tenant-1/vods/vod-hash/vod-hash.mp4", prevDtsh)
	expectLedgerRecord(mock, FreezeStagingKey("tenant-1/vods/vod-hash/vod-hash.mp4", ""))
	mock.ExpectBegin()
	mock.ExpectExec("UPDATE foghorn.artifacts.*sync_status = 'in_progress'").
		WithArgs("s3://bucket/tenant-1/vods/vod-hash/vod-hash.mp4.att-", false, "vod-hash", int64(1024), "", "node-1", "tenant-1", "tenant-1/vods/vod-hash/vod-hash.mp4", "tenant-1/vods/vod-hash/vod-hash.mp4.att-", "").
		WillReturnResult(sqlmock.NewResult(0, 1))
	// Superseded MEDIA object + its version-addressed .dtsh index enqueued (before node-copy).
	mock.ExpectExec("INSERT INTO foghorn.staging_cleanup_queue").WithArgs(prev).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("INSERT INTO foghorn.staging_cleanup_queue").WithArgs(prevDtsh).WillReturnResult(sqlmock.NewResult(0, 1))
	expectNodeCopyGained(mock, "vod-hash", "node-1", 1024)
	mock.ExpectExec("INSERT INTO foghorn.vod_metadata").
		WithArgs("vod-hash", "tenant-1/vods/vod-hash/vod-hash.mp4.att-", sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("INSERT INTO foghorn.artifact_event_outbox").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("INSERT INTO foghorn.staging_cleanup_queue").
		WithArgs(FreezeStagingKey("tenant-1/vods/vod-hash/vod-hash.mp4", "")).
		WillReturnResult(sqlmock.NewResult(0, 1))
	expectLedgerClear(mock)
	mock.ExpectCommit()

	processSyncComplete(&ipcpb.SyncComplete{
		AssetHash: "vod-hash", Status: "success",
	}, "node-1", logger)

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// A LEGACY row (synced before version-addressing, so active_object_key is empty) that is RE-UPLOADED must not
// leak its old durable object: the re-upload resets the row to in_progress, and the completion recovers the
// prior object key prefix-aware from s3_url so the superseded media object AND its co-located .dtsh are durably
// enqueued — even though active_object_key/active_dtsh_key were never populated for this row.
func TestProcessSyncComplete_LegacyRepublishRecoversSupersededObjectFromS3URL(t *testing.T) {
	mock, s3Mock, _ := setupArtifactTestDeps(t)
	logger := logging.NewLogger()
	s3Mock.headObjectInfoFn = stagingHead(1024, false, 0) // main staging verifies; no dtsh bundled

	// Legacy synced object lives at the key encoded in s3_url; active_object_key/active_dtsh_key are empty.
	legacyKey := "tenant-1/vods/legacy-hash/legacy.mp4"
	legacyURL := "s3://bucket/" + legacyKey
	// extra params: active_dtsh_key="" (legacy), s3_url=legacyURL.
	expectMetadata(mock, "legacy-hash", "", "node-1", "vod", "vod-int", "mp4", "tenant-1", "", "", "in_progress", legacyKey, "", legacyURL)
	expectLedgerRecord(mock, FreezeStagingKey(legacyKey, ""))
	mock.ExpectBegin()
	mock.ExpectExec("UPDATE foghorn.artifacts.*sync_status = 'in_progress'").
		WithArgs("s3://bucket/"+legacyKey+".att-", false, "legacy-hash", int64(1024), "", "node-1", "tenant-1", legacyKey, legacyKey+".att-", "").
		WillReturnResult(sqlmock.NewResult(0, 1))
	// The prior object recovered from s3_url (== legacyKey) and its co-located .dtsh are enqueued as superseded.
	mock.ExpectExec("INSERT INTO foghorn.staging_cleanup_queue").WithArgs(legacyKey).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("INSERT INTO foghorn.staging_cleanup_queue").WithArgs(legacyKey + ".dtsh").WillReturnResult(sqlmock.NewResult(0, 1))
	expectNodeCopyGained(mock, "legacy-hash", "node-1", 1024)
	mock.ExpectExec("INSERT INTO foghorn.vod_metadata").
		WithArgs("legacy-hash", legacyKey+".att-", sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("INSERT INTO foghorn.artifact_event_outbox").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("INSERT INTO foghorn.staging_cleanup_queue").
		WithArgs(FreezeStagingKey(legacyKey, "")).
		WillReturnResult(sqlmock.NewResult(0, 1))
	expectLedgerClear(mock)
	mock.ExpectCommit()

	processSyncComplete(&ipcpb.SyncComplete{
		AssetHash: "legacy-hash", Status: "success",
	}, "node-1", logger)

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestProcessSyncComplete_Failed(t *testing.T) {
	mock, _, _ := setupArtifactTestDeps(t)
	logger := logging.NewLogger()

	// applyDtshCompletionFailure runs first (tx); no dtsh attempt matches → rollback.
	mock.ExpectBegin()
	mock.ExpectQuery("UPDATE foghorn.artifacts.*dtsh_status = 'failed'.*RETURNING").
		WithArgs("hash-fail", "req-9", "node-1", "connection reset", "tenant-1").
		WillReturnRows(sqlmock.NewRows([]string{"sync_object_key"}))
	mock.ExpectRollback()
	// applySyncCompletionFailure: the guard now RETURNS the descriptor so the ambiguous staging object is
	// durably enqueued for cleanup alongside the failure transition.
	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT COALESCE\(tenant_id::text,''\), COALESCE\(sync_object_key,''\) FROM foghorn.artifacts.*sync_status = 'in_progress'`).
		WithArgs("hash-fail", "req-9", "node-1", "tenant-1").
		WillReturnRows(sqlmock.NewRows([]string{"tenant_id", "sync_object_key"}).AddRow("tenant-1", "obj/hash-fail"))
	mock.ExpectExec("UPDATE foghorn.artifacts.*sync_status = \\$2::text.*tenant_id::text = \\$4").
		WithArgs("hash-fail", "failed", "connection reset", "tenant-1").
		WillReturnResult(sqlmock.NewResult(0, 1))
	// The abandoned attempt's staging + published-candidate objects (main + .dtsh) are ALL durably enqueued —
	// any of them may have landed before the failure, and once the identity is cleared purge cannot derive them.
	for _, k := range []string{
		FreezeStagingKey("obj/hash-fail", "req-9"),
		FreezeStagingKey("obj/hash-fail.dtsh", "req-9"),
		FreezePublishKey("obj/hash-fail", "req-9"),
		FreezePublishDtshKey("obj/hash-fail", "req-9"),
	} {
		mock.ExpectExec("INSERT INTO foghorn.staging_cleanup_queue").
			WithArgs(k).
			WillReturnResult(sqlmock.NewResult(0, 1))
	}
	mock.ExpectCommit()

	processSyncComplete(&ipcpb.SyncComplete{
		AssetHash: "hash-fail", Status: "failed", Error: "connection reset", RequestId: "req-9",
	}, "node-1", logger)

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestProcessSyncComplete_Failed_MismatchIsNoOp(t *testing.T) {
	mock, _, _ := setupArtifactTestDeps(t)
	logger := logging.NewLogger()

	mock.ExpectBegin()
	mock.ExpectQuery("UPDATE foghorn.artifacts.*dtsh_status = 'failed'.*RETURNING").
		WithArgs("hash-fail", "stale-req", "node-1", "connection reset", "tenant-1").
		WillReturnRows(sqlmock.NewRows([]string{"sync_object_key"}))
	mock.ExpectRollback()
	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT COALESCE\(tenant_id::text,''\), COALESCE\(sync_object_key,''\) FROM foghorn.artifacts.*status NOT IN \('deleted', 'expired', 'aborted'\).*sync_status = 'in_progress'`).
		WithArgs("hash-fail", "stale-req", "node-1", "tenant-1").
		WillReturnRows(sqlmock.NewRows([]string{"tenant_id", "sync_object_key"}))
	mock.ExpectRollback()

	processSyncComplete(&ipcpb.SyncComplete{
		AssetHash: "hash-fail", Status: "failed", Error: "connection reset", RequestId: "stale-req",
	}, "node-1", logger)

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestProcessSyncComplete_LocalMissing_OtherCopySurvives(t *testing.T) {
	mock, _, _ := setupArtifactTestDeps(t)
	logger := logging.NewLogger()

	mock.ExpectBegin()
	mock.ExpectQuery("UPDATE foghorn.artifacts.*dtsh_status = 'failed'.*RETURNING").
		WithArgs("hash-lm", "req-lm", "node-1", "gone here", "tenant-1").
		WillReturnRows(sqlmock.NewRows([]string{"sync_object_key"}))
	mock.ExpectRollback()
	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT COALESCE\(tenant_id::text,''\), COALESCE\(sync_object_key,''\) FROM foghorn.artifacts.*sync_status = 'in_progress'`).
		WithArgs("hash-lm", "req-lm", "node-1", "tenant-1").
		WillReturnRows(sqlmock.NewRows([]string{"tenant_id", "sync_object_key"}).AddRow("tenant-1", "obj/hash-lm"))
	mock.ExpectQuery("SELECT role FROM foghorn.artifact_nodes").
		WithArgs("hash-lm", "node-1").
		WillReturnRows(sqlmock.NewRows([]string{"role"}).AddRow("cache"))
	mock.ExpectExec("DELETE FROM foghorn.artifact_nodes").
		WithArgs("hash-lm", "node-1").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery("SELECT tenant_id::text FROM foghorn.artifacts").
		WithArgs("hash-lm").
		WillReturnRows(sqlmock.NewRows([]string{"tenant_id"}).AddRow("tenant-1"))
	mock.ExpectQuery("SELECT nextval\\('foghorn.artifact_node_copy_version_seq'\\)").
		WillReturnRows(sqlmock.NewRows([]string{"nextval"}).AddRow(int64(7)))
	mock.ExpectExec("UPDATE foghorn.artifact_nodes SET last_emitted_version").
		WithArgs(int64(0), "hash-lm", "node-1").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("INSERT INTO foghorn.artifact_event_outbox").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery("SELECT count\\(\\*\\) FROM foghorn.artifact_nodes").
		WithArgs("hash-lm", "node-1").
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))
	mock.ExpectExec("UPDATE foghorn.artifacts.*sync_status = \\$2::text.*tenant_id::text = \\$4").
		WithArgs("hash-lm", "failed", "gone here", "tenant-1").
		WillReturnResult(sqlmock.NewResult(0, 1))
	for _, k := range []string{
		FreezeStagingKey("obj/hash-lm", "req-lm"),
		FreezeStagingKey("obj/hash-lm.dtsh", "req-lm"),
		FreezePublishKey("obj/hash-lm", "req-lm"),
		FreezePublishDtshKey("obj/hash-lm", "req-lm"),
	} {
		mock.ExpectExec("INSERT INTO foghorn.staging_cleanup_queue").
			WithArgs(k).
			WillReturnResult(sqlmock.NewResult(0, 1))
	}
	mock.ExpectCommit()

	processSyncComplete(&ipcpb.SyncComplete{
		AssetHash: "hash-lm", Status: "failed", Error: "gone here", RequestId: "req-lm", LocalMissing: true,
	}, "node-1", logger)

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
