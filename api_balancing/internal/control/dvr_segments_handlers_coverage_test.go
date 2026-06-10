package control

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
)

// These tests lock in the DVR per-segment ledger control-stream handlers and
// the peer-relay authorization path. The invariants under test:
//   - tenant/stream resolution is the guard every segment handler relies on
//     (a missing artifact row must NOT let a segment be recorded);
//   - segment record/mark/drop mutate the correct row and reply over the
//     stream with the right verdict;
//   - eviction-window math defaults safely and clamps to the stamped window;
//   - relay grants are honored only for the exact (node, artifact, path) they
//     were minted for — a mismatched grant must be denied.
// Every assertion inspects the captured Send() payload, not just that a
// function ran.

// extractRecordResp pulls the typed RecordDVRSegmentResponse out of the last
// message captured on the stream.
func extractRecordResp(t *testing.T, cs *captureStream) *ipcpb.RecordDVRSegmentResponse {
	t.Helper()
	msg := cs.lastSent()
	if msg == nil {
		t.Fatal("no message sent over stream")
	}
	resp := msg.GetRecordDvrSegmentResponse()
	if resp == nil {
		t.Fatalf("last message is not a RecordDVRSegmentResponse: %v", msg)
	}
	return resp
}

// --- resolveDVRTenantAndStream: the guard every caller relies on ---

// Invariant: a DVR hash with no matching artifacts row resolves ok=false so
// callers refuse the segment instead of building an S3 prefix from empty
// tenant/stream. With CommodoreClient nil there is no fallback authority.
func TestResolveDVRTenantAndStream_NotFound(t *testing.T) {
	mock, _, _ := setupArtifactTestDeps(t)

	mock.ExpectQuery("SELECT tenant_id::text, stream_internal_name").
		WithArgs("dvr-missing").
		WillReturnError(sql.ErrNoRows)

	tenant, stream, ok := resolveDVRTenantAndStream(context.Background(), "dvr-missing", logging.NewLogger())
	if ok {
		t.Fatalf("expected ok=false for missing DVR, got tenant=%q stream=%q", tenant, stream)
	}
	if tenant != "" || stream != "" {
		t.Fatalf("expected empty tenant/stream on miss, got %q/%q", tenant, stream)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// Invariant: a populated artifacts row yields the tenant + stream used to build
// the S3 prefix, and reports ok=true.
func TestResolveDVRTenantAndStream_Found(t *testing.T) {
	mock, _, _ := setupArtifactTestDeps(t)

	rows := sqlmock.NewRows([]string{"tenant_id", "stream_internal_name"}).
		AddRow("tenant-7", "live+show")
	mock.ExpectQuery("SELECT tenant_id::text, stream_internal_name").
		WithArgs("dvr-abc").
		WillReturnRows(rows)

	tenant, stream, ok := resolveDVRTenantAndStream(context.Background(), "dvr-abc", logging.NewLogger())
	if !ok || tenant != "tenant-7" || stream != "live+show" {
		t.Fatalf("expected tenant-7/live+show ok=true, got %q/%q ok=%v", tenant, stream, ok)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// Invariant: a row that exists but has a blank tenant or stream is NOT a valid
// resolution — ok must be false rather than returning a half-built prefix.
func TestResolveDVRTenantAndStream_BlankFieldsNotOK(t *testing.T) {
	mock, _, _ := setupArtifactTestDeps(t)

	rows := sqlmock.NewRows([]string{"tenant_id", "stream_internal_name"}).
		AddRow("tenant-9", "   ")
	mock.ExpectQuery("SELECT tenant_id::text, stream_internal_name").
		WithArgs("dvr-blank").
		WillReturnRows(rows)

	_, _, ok := resolveDVRTenantAndStream(context.Background(), "dvr-blank", logging.NewLogger())
	if ok {
		t.Fatal("expected ok=false when stream_internal_name is blank")
	}
}

// --- dvrEffectiveWindowSeconds: window math / default fallback ---

// Invariant: a positive stamped dvr_window_seconds is returned verbatim so
// eviction uses the live seek window decided at DVR start.
func TestDVREffectiveWindowSeconds_StampedValue(t *testing.T) {
	mock, _, _ := setupArtifactTestDeps(t)

	mock.ExpectQuery("SELECT dvr_window_seconds").
		WithArgs("dvr-1").
		WillReturnRows(sqlmock.NewRows([]string{"dvr_window_seconds"}).AddRow(7200))

	if got := dvrEffectiveWindowSeconds(context.Background(), "dvr-1"); got != 7200 {
		t.Fatalf("expected 7200, got %d", got)
	}
}

// Invariant: a NULL / non-positive window resolves to 0 so the caller falls
// back to its safe 4h default rather than evicting on a zero window.
func TestDVREffectiveWindowSeconds_NullFallsBackToZero(t *testing.T) {
	mock, _, _ := setupArtifactTestDeps(t)

	mock.ExpectQuery("SELECT dvr_window_seconds").
		WithArgs("dvr-2").
		WillReturnRows(sqlmock.NewRows([]string{"dvr_window_seconds"}).AddRow(nil))

	if got := dvrEffectiveWindowSeconds(context.Background(), "dvr-2"); got != 0 {
		t.Fatalf("expected 0 for NULL window, got %d", got)
	}
}

// Invariant: a lookup error is swallowed to 0 (safe default), never propagated
// as a window the caller would trust.
func TestDVREffectiveWindowSeconds_ErrorIsZero(t *testing.T) {
	mock, _, _ := setupArtifactTestDeps(t)

	mock.ExpectQuery("SELECT dvr_window_seconds").
		WithArgs("dvr-3").
		WillReturnError(errors.New("boom"))

	if got := dvrEffectiveWindowSeconds(context.Background(), "dvr-3"); got != 0 {
		t.Fatalf("expected 0 on error, got %d", got)
	}
}

// --- processRecordDVRSegment ---

// Invariant: a record with empty dvr_hash/segment_name is rejected before any
// DB work, replying accepted=false with the validation reason.
func TestProcessRecordDVRSegment_MissingFields(t *testing.T) {
	setupArtifactTestDeps(t)
	cs := &captureStream{}

	processRecordDVRSegment(&ipcpb.RecordDVRSegmentRequest{
		RequestId: "r1",
		DvrHash:   "",
	}, "node-1", cs, logging.NewLogger())

	resp := extractRecordResp(t, cs)
	if resp.GetAccepted() {
		t.Fatal("expected accepted=false for missing fields")
	}
	if resp.GetReason() != "missing_dvr_hash_or_segment_name" {
		t.Fatalf("unexpected reason %q", resp.GetReason())
	}
}

// Invariant: when the DVR artifact can't be resolved, the segment is refused
// with dvr_artifact_not_found — the resolution guard short-circuits the insert.
func TestProcessRecordDVRSegment_DVRNotFound(t *testing.T) {
	mock, _, _ := setupArtifactTestDeps(t)
	cs := &captureStream{}

	mock.ExpectQuery("SELECT tenant_id::text, stream_internal_name").
		WithArgs("dvr-x").
		WillReturnError(sql.ErrNoRows)

	processRecordDVRSegment(&ipcpb.RecordDVRSegmentRequest{
		RequestId:   "r2",
		DvrHash:     "dvr-x",
		SegmentName: "seg-0.ts",
	}, "node-1", cs, logging.NewLogger())

	resp := extractRecordResp(t, cs)
	if resp.GetAccepted() {
		t.Fatal("expected accepted=false when DVR not found")
	}
	if resp.GetReason() != "dvr_artifact_not_found" {
		t.Fatalf("unexpected reason %q", resp.GetReason())
	}
}

// Invariant: the happy path resolves tenant/stream, refreshes the recording
// node, inserts a 'pending' ledger row with a Foghorn-assigned sequence, mints
// a presigned PUT for the derived <prefix>/segments/<name> key, and replies
// accepted=true with that sequence + s3_key + URL.
func TestProcessRecordDVRSegment_HappyPathInsertsAndReplies(t *testing.T) {
	mock, s3, _ := setupArtifactTestDeps(t)
	cs := &captureStream{}

	// resolveDVRTenantAndStream
	mock.ExpectQuery("SELECT tenant_id::text, stream_internal_name").
		WithArgs("dvr-hot").
		WillReturnRows(sqlmock.NewRows([]string{"tenant_id", "stream_internal_name"}).
			AddRow("tenant-1", "live+demo"))
	// artifact_nodes refresh (node_id non-empty)
	mock.ExpectExec("INSERT INTO foghorn.artifact_nodes").
		WithArgs("dvr-hot", "node-9").
		WillReturnResult(sqlmock.NewResult(0, 1))
	// InsertDVRSegment tx
	mock.ExpectBegin()
	mock.ExpectQuery("SELECT status FROM foghorn.artifacts").
		WithArgs("dvr-hot").
		WillReturnRows(sqlmock.NewRows([]string{"status"}).AddRow("recording"))
	mock.ExpectQuery("SELECT sequence, status, media_start_ms, media_end_ms, duration_ms").
		WithArgs("dvr-hot", "seg-3.ts").
		WillReturnError(sql.ErrNoRows)
	mock.ExpectQuery("SELECT COALESCE\\(MAX\\(sequence\\), -1\\) \\+ 1").
		WithArgs("dvr-hot").
		WillReturnRows(sqlmock.NewRows([]string{"seq"}).AddRow(3))
	mock.ExpectExec("INSERT INTO foghorn.dvr_segments").
		WithArgs("dvr-hot", "seg-3.ts", int64(3), int64(1000), int64(2000), int64(1000), "tenant-1/live+demo/dvr/dvr-hot/segments/seg-3.ts").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	processRecordDVRSegment(&ipcpb.RecordDVRSegmentRequest{
		RequestId:    "r3",
		DvrHash:      "dvr-hot",
		SegmentName:  "seg-3.ts",
		MediaStartMs: 1000,
		MediaEndMs:   2000,
		DurationMs:   1000,
	}, "node-9", cs, logging.NewLogger())

	resp := extractRecordResp(t, cs)
	if !resp.GetAccepted() {
		t.Fatalf("expected accepted=true, got reason=%q", resp.GetReason())
	}
	if resp.GetSequence() != 3 {
		t.Fatalf("expected sequence 3, got %d", resp.GetSequence())
	}
	wantKey := "tenant-1/live+demo/dvr/dvr-hot/segments/seg-3.ts"
	if resp.GetS3Key() != wantKey {
		t.Fatalf("expected s3_key %q, got %q", wantKey, resp.GetS3Key())
	}
	if resp.GetPresignedPutUrl() != "https://s3.test/put/"+wantKey {
		t.Fatalf("unexpected presigned PUT %q", resp.GetPresignedPutUrl())
	}
	if len(s3.presignPUTCalls) != 1 || s3.presignPUTCalls[0] != wantKey {
		t.Fatalf("expected one presign PUT for %q, got %v", wantKey, s3.presignPUTCalls)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// Invariant: an insert failure on a terminal DVR maps to the dvr_terminal
// reason and never mints a presigned URL.
func TestProcessRecordDVRSegment_TerminalInsertRejected(t *testing.T) {
	mock, s3, _ := setupArtifactTestDeps(t)
	cs := &captureStream{}

	mock.ExpectQuery("SELECT tenant_id::text, stream_internal_name").
		WithArgs("dvr-done").
		WillReturnRows(sqlmock.NewRows([]string{"tenant_id", "stream_internal_name"}).
			AddRow("tenant-1", "live+demo"))
	mock.ExpectExec("INSERT INTO foghorn.artifact_nodes").
		WithArgs("dvr-done", "node-9").
		WillReturnResult(sqlmock.NewResult(0, 1))
	// InsertDVRSegment tx: artifact is terminal ('completed'), no existing row,
	// recovery insert not allowed → ErrDVRSegmentTerminal.
	mock.ExpectBegin()
	mock.ExpectQuery("SELECT status FROM foghorn.artifacts").
		WithArgs("dvr-done").
		WillReturnRows(sqlmock.NewRows([]string{"status"}).AddRow("completed"))
	mock.ExpectQuery("SELECT sequence, status, media_start_ms, media_end_ms, duration_ms").
		WithArgs("dvr-done", "seg-9.ts").
		WillReturnError(sql.ErrNoRows)
	mock.ExpectRollback()

	processRecordDVRSegment(&ipcpb.RecordDVRSegmentRequest{
		RequestId:    "r4",
		DvrHash:      "dvr-done",
		SegmentName:  "seg-9.ts",
		MediaStartMs: 1,
		MediaEndMs:   2,
		DurationMs:   1,
	}, "node-9", cs, logging.NewLogger())

	resp := extractRecordResp(t, cs)
	if resp.GetAccepted() {
		t.Fatal("expected accepted=false for terminal DVR")
	}
	if resp.GetReason() != "dvr_terminal" {
		t.Fatalf("expected reason dvr_terminal, got %q", resp.GetReason())
	}
	if len(s3.presignPUTCalls) != 0 {
		t.Fatalf("expected no presign on rejected insert, got %v", s3.presignPUTCalls)
	}
}

// Invariant: with no S3 client wired the record is refused with
// s3_client_unavailable AFTER tenant resolution — the prefix can't be minted.
func TestProcessRecordDVRSegment_NoS3Client(t *testing.T) {
	mock, _, _ := setupArtifactTestDeps(t)
	prevS3 := s3Client
	s3Client = nil
	t.Cleanup(func() { s3Client = prevS3 })
	cs := &captureStream{}

	mock.ExpectQuery("SELECT tenant_id::text, stream_internal_name").
		WithArgs("dvr-s3").
		WillReturnRows(sqlmock.NewRows([]string{"tenant_id", "stream_internal_name"}).
			AddRow("tenant-1", "live+demo"))
	// node_id empty here so artifact_nodes refresh is skipped.

	processRecordDVRSegment(&ipcpb.RecordDVRSegmentRequest{
		RequestId:   "r5",
		DvrHash:     "dvr-s3",
		SegmentName: "seg-0.ts",
	}, "", cs, logging.NewLogger())

	resp := extractRecordResp(t, cs)
	if resp.GetReason() != "s3_client_unavailable" {
		t.Fatalf("expected reason s3_client_unavailable, got %q", resp.GetReason())
	}
}

// --- processMarkDVRSegmentUploaded ---

// Invariant: a valid mark transitions the named row to 'uploaded' (UPDATE
// scoped to artifact_hash + segment_name) and then aggregates progress.
func TestProcessMarkDVRSegmentUploaded_MutatesRow(t *testing.T) {
	mock, _, _ := setupArtifactTestDeps(t)

	mock.ExpectExec("UPDATE foghorn.dvr_segments").
		WithArgs("dvr-u", "seg-1.ts", int64(4096)).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery("SELECT COUNT\\(\\*\\), COALESCE\\(SUM\\(size_bytes\\), 0\\)").
		WithArgs("dvr-u").
		WillReturnRows(sqlmock.NewRows([]string{"count", "sum"}).AddRow(2, 8192))

	processMarkDVRSegmentUploaded(&ipcpb.MarkDVRSegmentUploaded{
		DvrHash:     "dvr-u",
		SegmentName: "seg-1.ts",
		SizeBytes:   4096,
	}, "node-1", logging.NewLogger())

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// Invariant: empty identifiers are a no-op — no UPDATE is issued (an empty
// mark must never touch the ledger).
func TestProcessMarkDVRSegmentUploaded_EmptyNoop(t *testing.T) {
	mock, _, _ := setupArtifactTestDeps(t)
	// No expectations registered: any DB call would fail ExpectationsWereMet.
	processMarkDVRSegmentUploaded(&ipcpb.MarkDVRSegmentUploaded{
		DvrHash: "", SegmentName: "seg",
	}, "node-1", logging.NewLogger())
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// --- processDVRSegmentDropped ---

// Invariant: a was_uploaded=true drop transitions the row to deleted_local via
// an UPDATE scoped to the named segment (no lost-row insert, no warn path).
func TestProcessDVRSegmentDropped_UploadedDeletesLocal(t *testing.T) {
	mock, _, _ := setupArtifactTestDeps(t)

	mock.ExpectExec("UPDATE foghorn.dvr_segments").
		WithArgs("dvr-d", "seg-2.ts", "deleted_local", "storage_pressure", true).
		WillReturnResult(sqlmock.NewResult(0, 1))

	processDVRSegmentDropped(&ipcpb.DVRSegmentDropped{
		DvrHash:     "dvr-d",
		SegmentName: "seg-2.ts",
		Reason:      "storage_pressure",
		WasUploaded: true,
	}, "node-1", logging.NewLogger())

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// Invariant: a was_uploaded=false drop that matches an existing row marks it
// lost_local (target='lost_local'); rows-affected>0 means no placeholder insert.
func TestProcessDVRSegmentDropped_LostLocalUpdatesExisting(t *testing.T) {
	mock, _, _ := setupArtifactTestDeps(t)

	mock.ExpectExec("UPDATE foghorn.dvr_segments").
		WithArgs("dvr-d2", "seg-5.ts", "lost_local", "evicted_before_upload", false).
		WillReturnResult(sqlmock.NewResult(0, 1))

	processDVRSegmentDropped(&ipcpb.DVRSegmentDropped{
		DvrHash:     "dvr-d2",
		SegmentName: "seg-5.ts",
		Reason:      "evicted_before_upload",
		WasUploaded: false,
	}, "node-1", logging.NewLogger())

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// --- processEvictableSegmentsRequest ---

// Invariant: a request with empty dvr_hash replies with an empty segment list
// and never queries the ledger.
func TestProcessEvictableSegmentsRequest_EmptyHash(t *testing.T) {
	mock, _, _ := setupArtifactTestDeps(t)
	cs := &captureStream{}

	processEvictableSegmentsRequest(&ipcpb.EvictableSegmentsRequest{
		RequestId: "e0",
		DvrHash:   "",
	}, "node-1", cs, logging.NewLogger())

	msg := cs.lastSent()
	if msg == nil || msg.GetEvictableSegmentsResponse() == nil {
		t.Fatal("expected EvictableSegmentsResponse")
	}
	if len(msg.GetEvictableSegmentsResponse().GetSegmentNames()) != 0 {
		t.Fatal("expected empty segment names for empty hash")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// Invariant: the effective window stamped on the artifact is the one fed to the
// evictable query, and the returned names are sent back on the stream. The
// window-resolution query precedes the eviction query.
func TestProcessEvictableSegmentsRequest_UsesStampedWindowAndReplies(t *testing.T) {
	mock, _, _ := setupArtifactTestDeps(t)
	cs := &captureStream{}

	// dvrEffectiveWindowSeconds returns a stamped window.
	mock.ExpectQuery("SELECT dvr_window_seconds").
		WithArgs("dvr-e").
		WillReturnRows(sqlmock.NewRows([]string{"dvr_window_seconds"}).AddRow(3600))
	// ListEvictableDVRSegments returns two names.
	mock.ExpectQuery("SELECT s.segment_name").
		WillReturnRows(sqlmock.NewRows([]string{"segment_name"}).
			AddRow("seg-0.ts").AddRow("seg-1.ts"))

	processEvictableSegmentsRequest(&ipcpb.EvictableSegmentsRequest{
		RequestId: "e1",
		DvrHash:   "dvr-e",
		MaxCount:  10,
	}, "node-1", cs, logging.NewLogger())

	msg := cs.lastSent()
	if msg == nil || msg.GetEvictableSegmentsResponse() == nil {
		t.Fatal("expected EvictableSegmentsResponse")
	}
	names := msg.GetEvictableSegmentsResponse().GetSegmentNames()
	if len(names) != 2 || names[0] != "seg-0.ts" || names[1] != "seg-1.ts" {
		t.Fatalf("unexpected evictable names %v", names)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// --- relay grant authorization (relay_grant.go) ---

// relayGrantAllows is the pure authorization decision. These cases lock in the
// grant security model: a grant authorizes exactly one (node, artifact, path)
// triple — any mismatch is a deny.
func TestRelayGrantAllows_Decisions(t *testing.T) {
	base := relayGrant{
		ArtifactHash: "art-1",
		OriginNodeID: "node-A",
		AllowedPaths: []string{"/internal/artifact/vod/art-1.mp4"},
	}
	cases := []struct {
		name      string
		grant     relayGrant
		found     bool
		node      string
		hash      string
		path      string
		wantAllow bool
		wantWhy   string
	}{
		{"valid", base, true, "node-A", "art-1", "/internal/artifact/vod/art-1.mp4", true, ""},
		{"unknown grant", base, false, "node-A", "art-1", "/internal/artifact/vod/art-1.mp4", false, "unknown or expired grant"},
		{"node mismatch", base, true, "node-B", "art-1", "/internal/artifact/vod/art-1.mp4", false, "node mismatch"},
		{"artifact mismatch", base, true, "node-A", "art-2", "/internal/artifact/vod/art-1.mp4", false, "artifact mismatch"},
		{"path not authorized", base, true, "node-A", "art-1", "/internal/artifact/vod/art-1.mp4.dtsh", false, "path not authorized"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			allow, why := relayGrantAllows(tc.grant, tc.found, tc.node, tc.hash, tc.path)
			if allow != tc.wantAllow {
				t.Fatalf("allow=%v want %v (why=%q)", allow, tc.wantAllow, why)
			}
			if why != tc.wantWhy {
				t.Fatalf("why=%q want %q", why, tc.wantWhy)
			}
		})
	}
}

// Invariant: an end-to-end mint→authorize with a matching node/artifact/path
// yields allowed=true on the captured response. The serving node id is taken
// from the control connection, never the request.
func TestProcessAuthorizeRelayPull_ValidGrant(t *testing.T) {
	// In-memory grant store (no Redis wired) — deterministic.
	path := "/internal/artifact/vod/art-9.mp4"
	grantID, err := MintRelayGrant("art-9", "node-serv", []string{path})
	if err != nil {
		t.Fatalf("MintRelayGrant: %v", err)
	}
	t.Cleanup(func() {
		relayGrants.mu.Lock()
		delete(relayGrants.mem, grantID)
		relayGrants.mu.Unlock()
	})

	cs := &captureStream{}
	processAuthorizeRelayPullRequest(&ipcpb.AuthorizeRelayPullRequest{
		RequestId:    "a1",
		GrantId:      grantID,
		ArtifactHash: "art-9",
		RequestPath:  path,
	}, "node-serv", cs, logging.NewLogger())

	msg := cs.lastSent()
	if msg == nil || msg.GetAuthorizeRelayPullResponse() == nil {
		t.Fatal("expected AuthorizeRelayPullResponse")
	}
	resp := msg.GetAuthorizeRelayPullResponse()
	if !resp.GetAllowed() {
		t.Fatalf("expected allowed=true, got reason=%q", resp.GetReason())
	}
}

// Invariant: a grant minted for node-A must NOT authorize a pull arriving on
// node-B — the captured response is denied with a node-mismatch reason. This
// is the cross-node grant-confusion guard.
func TestProcessAuthorizeRelayPull_NodeMismatchDenied(t *testing.T) {
	path := "/internal/artifact/vod/art-x.mp4"
	grantID, err := MintRelayGrant("art-x", "node-A", []string{path})
	if err != nil {
		t.Fatalf("MintRelayGrant: %v", err)
	}
	t.Cleanup(func() {
		relayGrants.mu.Lock()
		delete(relayGrants.mem, grantID)
		relayGrants.mu.Unlock()
	})

	cs := &captureStream{}
	processAuthorizeRelayPullRequest(&ipcpb.AuthorizeRelayPullRequest{
		RequestId:    "a2",
		GrantId:      grantID,
		ArtifactHash: "art-x",
		RequestPath:  path,
	}, "node-B", cs, logging.NewLogger())

	resp := cs.lastSent().GetAuthorizeRelayPullResponse()
	if resp == nil || resp.GetAllowed() {
		t.Fatal("expected allowed=false for node mismatch")
	}
	if resp.GetReason() != "node mismatch" {
		t.Fatalf("expected reason 'node mismatch', got %q", resp.GetReason())
	}
}

// Invariant: an unknown grant id is denied (unknown-or-expired) — no grant
// state, no authorization.
func TestProcessAuthorizeRelayPull_UnknownGrantDenied(t *testing.T) {
	cs := &captureStream{}
	processAuthorizeRelayPullRequest(&ipcpb.AuthorizeRelayPullRequest{
		RequestId:    "a3",
		GrantId:      "deadbeefdeadbeef",
		ArtifactHash: "art-z",
		RequestPath:  "/internal/artifact/vod/art-z.mp4",
	}, "node-any", cs, logging.NewLogger())

	resp := cs.lastSent().GetAuthorizeRelayPullResponse()
	if resp == nil || resp.GetAllowed() {
		t.Fatal("expected allowed=false for unknown grant")
	}
	if resp.GetReason() != "unknown or expired grant" {
		t.Fatalf("unexpected reason %q", resp.GetReason())
	}
}

// --- relay resolve (relay_resolve.go) ---

// Invariant: an unknown asset_kind never touches the DB and replies with the
// error string + SOURCE_MISSING state (the relay 404s).
func TestProcessRelayResolveRequest_UnknownKind(t *testing.T) {
	mock, _, _ := setupArtifactTestDeps(t)
	cs := &captureStream{}

	processRelayResolveRequest(&ipcpb.RelayResolveRequest{
		RequestId: "rr1",
		AssetHash: "h",
		AssetKind: "bogus",
	}, "node-1", cs, logging.NewLogger())

	msg := cs.lastSent()
	if msg == nil || msg.GetRelayResolveResponse() == nil {
		t.Fatal("expected RelayResolveResponse")
	}
	resp := msg.GetRelayResolveResponse()
	if resp.GetState() != ipcpb.AssetState_ASSET_STATE_SOURCE_MISSING {
		t.Fatalf("expected SOURCE_MISSING, got %v", resp.GetState())
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// Invariant: a vod resolve with no local artifacts row stays SOURCE_MISSING and
// is NOT federated by hash from here (RelayResolve has no requesting-tenant
// context). The captured response carries no media URL.
func TestProcessRelayResolveRequest_VODNoRowSourceMissing(t *testing.T) {
	mock, _, _ := setupArtifactTestDeps(t)
	cs := &captureStream{}

	mock.ExpectQuery("FROM foghorn.artifacts").
		WithArgs("h-missing").
		WillReturnError(sql.ErrNoRows)

	processRelayResolveRequest(&ipcpb.RelayResolveRequest{
		RequestId: "rr2",
		AssetHash: "h-missing",
		AssetKind: "vod",
	}, "node-1", cs, logging.NewLogger())

	resp := cs.lastSent().GetRelayResolveResponse()
	if resp.GetState() != ipcpb.AssetState_ASSET_STATE_SOURCE_MISSING {
		t.Fatalf("expected SOURCE_MISSING, got %v", resp.GetState())
	}
	if resp.GetMediaPresignedUrl() != "" {
		t.Fatalf("expected no media URL on miss, got %q", resp.GetMediaPresignedUrl())
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// Invariant: a synced vod row with an s3_url mints a presigned GET and flips
// the response to PLAYABLE with the media URL + expected size. This is the
// happy resolve that feeds the relay's byte serve.
func TestProcessRelayResolveRequest_VODSyncedPlayable(t *testing.T) {
	mock, s3, _ := setupArtifactTestDeps(t)
	cs := &captureStream{}

	// fillFileArtifactResolve main lookup: s3_url set, sync_status='synced'.
	cols := []string{"s3_url", "size_bytes", "format", "dtsh_synced", "stream_internal_name",
		"sync_status", "origin_cluster_id", "storage_cluster_id", "tenant_id", "artifact_type"}
	mock.ExpectQuery("FROM foghorn.artifacts").
		WithArgs("h-ok").
		WillReturnRows(sqlmock.NewRows(cols).AddRow(
			"s3://bucket/vods/h-ok/file.mp4", 1234, "mp4", false, sql.NullString{},
			"synced", "", "", "tenant-1", "vod"))

	processRelayResolveRequest(&ipcpb.RelayResolveRequest{
		RequestId: "rr3",
		AssetHash: "h-ok",
		AssetKind: "vod",
	}, "node-1", cs, logging.NewLogger())

	resp := cs.lastSent().GetRelayResolveResponse()
	if resp.GetState() != ipcpb.AssetState_ASSET_STATE_PLAYABLE {
		t.Fatalf("expected PLAYABLE, got %v", resp.GetState())
	}
	if resp.GetMediaPresignedUrl() == "" {
		t.Fatal("expected a media presigned URL on synced row")
	}
	if resp.GetExpectedSizeBytes() != 1234 {
		t.Fatalf("expected size 1234, got %d", resp.GetExpectedSizeBytes())
	}
	if len(s3.presignGETCalls) == 0 {
		t.Fatal("expected a presigned GET to be minted")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// --- fillCrossClusterArtifact (relay_resolve.go) ---

// Invariant: when cross-cluster deps are unwired, fillCrossClusterArtifact is
// silent — it must NOT flip the response to PLAYABLE (the relay falls through
// to 404 rather than serving an unresolved artifact).
func TestFillCrossClusterArtifact_DepsUnwiredSilent(t *testing.T) {
	setupArtifactTestDeps(t)

	resp := &ipcpb.RelayResolveResponse{
		AssetHash: "h-peer",
		State:     ipcpb.AssetState_ASSET_STATE_SOURCE_MISSING,
	}
	fillCrossClusterArtifact(context.Background(),
		&ipcpb.RelayResolveRequest{AssetHash: "h-peer"},
		resp, logging.NewLogger(), "peer-cluster", "tenant-1", "vod", nil)

	if resp.GetState() != ipcpb.AssetState_ASSET_STATE_SOURCE_MISSING {
		t.Fatalf("expected state unchanged (SOURCE_MISSING) when deps unwired, got %v", resp.GetState())
	}
	if resp.GetMediaPresignedUrl() != "" || resp.GetPeerRelayUrl() != "" {
		t.Fatal("expected no URLs populated when cross-cluster resolve is unavailable")
	}
}
