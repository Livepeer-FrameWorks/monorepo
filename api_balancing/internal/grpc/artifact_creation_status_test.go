package grpc

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"frameworks/api_balancing/internal/storage"
	sharedpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/shared"

	"github.com/DATA-DOG/go-sqlmock"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// A 'committed' command-ledger row means the create durably committed even if its
// original response was lost — the sweep reads this as committed, never rejected.
func TestGetArtifactCreationStatus_Committed(t *testing.T) {
	srv, mock, cleanup := newStatusServer(t, &fakeVodS3Client{})
	defer cleanup()

	mock.ExpectQuery(`FROM foghorn\.artifact_creation_commands`).
		WithArgs("req-1", "t1").
		WillReturnRows(sqlmock.NewRows([]string{"status", "catalog_revision", "kind", "artifact_hash"}).
			AddRow("committed", int64(7), "vod", "vh1"))

	resp, err := srv.GetArtifactCreationStatus(context.Background(), &sharedpb.GetArtifactCreationStatusRequest{
		TenantId: "t1", Kind: "vod", ArtifactHash: "vh1", RequestId: "req-1",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.GetOutcome() != sharedpb.ArtifactCreationOutcome_ARTIFACT_CREATION_OUTCOME_COMMITTED {
		t.Fatalf("outcome = %v, want COMMITTED", resp.GetOutcome())
	}
	if resp.GetCatalogRevision() != 7 {
		t.Fatalf("catalog_revision = %d, want 7", resp.GetCatalogRevision())
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// A 'rejected' command-ledger row is the ONLY signal the sweep may treat as a
// definitive rejection.
func TestGetArtifactCreationStatus_Rejected(t *testing.T) {
	srv, mock, cleanup := newStatusServer(t, &fakeVodS3Client{})
	defer cleanup()

	mock.ExpectQuery(`FROM foghorn\.artifact_creation_commands`).
		WithArgs("req-rej", "t1").
		WillReturnRows(sqlmock.NewRows([]string{"status", "catalog_revision", "kind", "artifact_hash"}).
			AddRow("rejected", int64(0), "vod", "vh-missing"))

	resp, err := srv.GetArtifactCreationStatus(context.Background(), &sharedpb.GetArtifactCreationStatusRequest{
		TenantId: "t1", Kind: "vod", ArtifactHash: "vh-missing", RequestId: "req-rej",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.GetOutcome() != sharedpb.ArtifactCreationOutcome_ARTIFACT_CREATION_OUTCOME_REJECTED {
		t.Fatalf("outcome = %v, want REJECTED", resp.GetOutcome())
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// No ledger row for the identity is the MISSING outcome — DISTINCT from an in-flight
// ACCEPTED, and never inferred as a rejection. Only a MISSING outcome (past the deadline)
// is bounded-abortable by the sweep.
func TestGetArtifactCreationStatus_MissingWhenNoRow(t *testing.T) {
	srv, mock, cleanup := newStatusServer(t, &fakeVodS3Client{})
	defer cleanup()

	mock.ExpectQuery(`FROM foghorn\.artifact_creation_commands`).
		WithArgs("req-inflight", "t1").
		WillReturnError(sql.ErrNoRows)

	resp, err := srv.GetArtifactCreationStatus(context.Background(), &sharedpb.GetArtifactCreationStatusRequest{
		TenantId: "t1", Kind: "vod", ArtifactHash: "vh2", RequestId: "req-inflight",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.GetOutcome() != sharedpb.ArtifactCreationOutcome_ARTIFACT_CREATION_OUTCOME_MISSING {
		t.Fatalf("outcome = %v, want MISSING", resp.GetOutcome())
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// A committed clip returns its fulfilled range (recovered from the durable
// processing job) so the convergence sweep can complete the catalog row.
func TestGetArtifactCreationStatus_ClipTiming(t *testing.T) {
	srv, mock, cleanup := newStatusServer(t, &fakeVodS3Client{})
	defer cleanup()

	mock.ExpectQuery(`FROM foghorn\.artifact_creation_commands`).
		WithArgs("req-clip", "t1").
		WillReturnRows(sqlmock.NewRows([]string{"status", "catalog_revision", "kind", "artifact_hash"}).
			AddRow("committed", int64(3), "clip", "clip1"))
	mock.ExpectQuery(`FROM foghorn\.processing_jobs j`).
		WithArgs("clip1", "t1").
		WillReturnRows(sqlmock.NewRows([]string{"source_params"}).
			AddRow([]byte(`{"source_start_unix":"100","source_stop_unix":"130"}`)))

	resp, err := srv.GetArtifactCreationStatus(context.Background(), &sharedpb.GetArtifactCreationStatusRequest{
		TenantId: "t1", Kind: "clip", ArtifactHash: "clip1", RequestId: "req-clip",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.GetOutcome() != sharedpb.ArtifactCreationOutcome_ARTIFACT_CREATION_OUTCOME_COMMITTED {
		t.Fatalf("expected committed outcome, got %v", resp.GetOutcome())
	}
	if resp.GetEffectiveStartMs() != 100000 || resp.GetEffectiveDurationMs() != 30000 {
		t.Fatalf("timing = (%d,%d), want (100000,30000)", resp.GetEffectiveStartMs(), resp.GetEffectiveDurationMs())
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// A request_id whose stored row carries a DIFFERENT kind/hash than the request is an
// invariant violation, NOT a missing command: the lookup binds (tenant, request_id) only
// and the stored identity is compared, so a reused request_id resolves to IDENTITY_MISMATCH
// (which the sweep must never abort on) rather than MISSING (which it could bounded-abort).
func TestGetArtifactCreationStatus_MismatchedIdentityIsIdentityMismatch(t *testing.T) {
	srv, mock, cleanup := newStatusServer(t, &fakeVodS3Client{})
	defer cleanup()

	// The stored row is for (vod, vh1); this query carries (vod, wrong-hash). The lookup
	// finds the row by (tenant, request_id) and the stored hash mismatches the request.
	mock.ExpectQuery(`FROM foghorn\.artifact_creation_commands`).
		WithArgs("req-1", "t1").
		WillReturnRows(sqlmock.NewRows([]string{"status", "catalog_revision", "kind", "artifact_hash"}).
			AddRow("committed", int64(7), "vod", "vh1"))

	resp, err := srv.GetArtifactCreationStatus(context.Background(), &sharedpb.GetArtifactCreationStatusRequest{
		TenantId: "t1", Kind: "vod", ArtifactHash: "wrong-hash", RequestId: "req-1",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.GetOutcome() != sharedpb.ArtifactCreationOutcome_ARTIFACT_CREATION_OUTCOME_IDENTITY_MISMATCH {
		t.Fatalf("outcome = %v, want IDENTITY_MISMATCH", resp.GetOutcome())
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// The status read is pure: an 'accepted' row is reported in-flight regardless of age
// and NEVER writes. A stranded 'accepted' past the deadline is terminalized by the
// CreationCommandExpiryJob worker, not from this path — so the read issues exactly one
// statement (the ledger SELECT) and no reject write can race a concurrent commit.
func TestGetArtifactCreationStatus_AcceptedIsReadOnlyInFlight(t *testing.T) {
	srv, mock, cleanup := newStatusServer(t, &fakeVodS3Client{})
	defer cleanup()

	mock.ExpectQuery(`FROM foghorn\.artifact_creation_commands`).
		WithArgs("req-recent", "t1").
		WillReturnRows(sqlmock.NewRows([]string{"status", "catalog_revision", "kind", "artifact_hash"}).
			AddRow("accepted", int64(0), "vod", "vh-r"))

	resp, err := srv.GetArtifactCreationStatus(context.Background(), &sharedpb.GetArtifactCreationStatusRequest{
		TenantId: "t1", Kind: "vod", ArtifactHash: "vh-r", RequestId: "req-recent",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.GetOutcome() != sharedpb.ArtifactCreationOutcome_ARTIFACT_CREATION_OUTCOME_ACCEPTED {
		t.Fatalf("outcome = %v, want ACCEPTED", resp.GetOutcome())
	}
	// ExpectationsWereMet fails if any additional (write) statement had been issued.
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// A committed terminal row discharges the obligation: the ack stamps consumed_at and
// returns COMMITTED. The UPDATE is issued ONLY on the terminal path.
func TestAckArtifactCreationCommand_CommittedStampsConsumed(t *testing.T) {
	srv, mock, cleanup := newStatusServer(t, &fakeVodS3Client{})
	defer cleanup()

	mock.ExpectQuery(`SELECT status, kind, artifact_hash\s+FROM foghorn\.artifact_creation_commands`).
		WithArgs("req-1", "t1").
		WillReturnRows(sqlmock.NewRows([]string{"status", "kind", "artifact_hash"}).
			AddRow("committed", "vod", "vh1"))
	mock.ExpectExec(`UPDATE foghorn\.artifact_creation_commands`).
		WithArgs("req-1", "t1", "vod", "vh1").
		WillReturnResult(sqlmock.NewResult(0, 1))

	resp, err := srv.AckArtifactCreationCommand(context.Background(), &sharedpb.AckArtifactCreationCommandRequest{
		TenantId: "t1", Kind: "vod", ArtifactHash: "vh1", RequestId: "req-1",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.GetOutcome() != sharedpb.ArtifactCreationOutcome_ARTIFACT_CREATION_OUTCOME_COMMITTED {
		t.Fatalf("outcome = %v, want COMMITTED", resp.GetOutcome())
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// A rejected terminal row also discharges: stamp consumed_at and return REJECTED.
func TestAckArtifactCreationCommand_RejectedStampsConsumed(t *testing.T) {
	srv, mock, cleanup := newStatusServer(t, &fakeVodS3Client{})
	defer cleanup()

	mock.ExpectQuery(`SELECT status, kind, artifact_hash\s+FROM foghorn\.artifact_creation_commands`).
		WithArgs("req-rej", "t1").
		WillReturnRows(sqlmock.NewRows([]string{"status", "kind", "artifact_hash"}).
			AddRow("rejected", "vod", "vh1"))
	mock.ExpectExec(`UPDATE foghorn\.artifact_creation_commands`).
		WithArgs("req-rej", "t1", "vod", "vh1").
		WillReturnResult(sqlmock.NewResult(0, 1))

	resp, err := srv.AckArtifactCreationCommand(context.Background(), &sharedpb.AckArtifactCreationCommandRequest{
		TenantId: "t1", Kind: "vod", ArtifactHash: "vh1", RequestId: "req-rej",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.GetOutcome() != sharedpb.ArtifactCreationOutcome_ARTIFACT_CREATION_OUTCOME_REJECTED {
		t.Fatalf("outcome = %v, want REJECTED", resp.GetOutcome())
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// A repeat ack of an already-consumed terminal row REFRESHES consumed_at: the terminal
// UPDATE is unguarded on consumed_at, so every terminal ack re-stamps consumed_at=NOW() and
// pushes the retention window forward. Here the same command is acked twice; both cycles
// issue the SELECT and the refreshing UPDATE — proving the second ack is not a no-op gated
// on consumed_at IS NULL, so a retried ack after a lost response keeps the row alive.
func TestAckArtifactCreationCommand_ReAckRefreshesConsumed(t *testing.T) {
	srv, mock, cleanup := newStatusServer(t, &fakeVodS3Client{})
	defer cleanup()

	for i := 0; i < 2; i++ {
		mock.ExpectQuery(`SELECT status, kind, artifact_hash\s+FROM foghorn\.artifact_creation_commands`).
			WithArgs("req-1", "t1").
			WillReturnRows(sqlmock.NewRows([]string{"status", "kind", "artifact_hash"}).
				AddRow("committed", "vod", "vh1"))
		// The refresh UPDATE fires on EVERY terminal ack (no consumed_at IS NULL guard).
		mock.ExpectExec(`UPDATE foghorn\.artifact_creation_commands\s+SET consumed_at = NOW\(\)`).
			WithArgs("req-1", "t1", "vod", "vh1").
			WillReturnResult(sqlmock.NewResult(0, 1))
	}

	for i := 0; i < 2; i++ {
		resp, err := srv.AckArtifactCreationCommand(context.Background(), &sharedpb.AckArtifactCreationCommandRequest{
			TenantId: "t1", Kind: "vod", ArtifactHash: "vh1", RequestId: "req-1",
		})
		if err != nil {
			t.Fatalf("ack %d: unexpected error: %v", i, err)
		}
		if resp.GetOutcome() != sharedpb.ArtifactCreationOutcome_ARTIFACT_CREATION_OUTCOME_COMMITTED {
			t.Fatalf("ack %d: outcome = %v, want COMMITTED", i, resp.GetOutcome())
		}
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// A still-'accepted' row is NOT consumed: the ack returns ACCEPTED and stamps NOTHING, so
// the obligation survives until the command terminalizes. ExpectationsWereMet fails if any
// UPDATE had been issued.
func TestAckArtifactCreationCommand_AcceptedDoesNotStamp(t *testing.T) {
	srv, mock, cleanup := newStatusServer(t, &fakeVodS3Client{})
	defer cleanup()

	mock.ExpectQuery(`SELECT status, kind, artifact_hash\s+FROM foghorn\.artifact_creation_commands`).
		WithArgs("req-acc", "t1").
		WillReturnRows(sqlmock.NewRows([]string{"status", "kind", "artifact_hash"}).
			AddRow("accepted", "vod", "vh1"))

	resp, err := srv.AckArtifactCreationCommand(context.Background(), &sharedpb.AckArtifactCreationCommandRequest{
		TenantId: "t1", Kind: "vod", ArtifactHash: "vh1", RequestId: "req-acc",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.GetOutcome() != sharedpb.ArtifactCreationOutcome_ARTIFACT_CREATION_OUTCOME_ACCEPTED {
		t.Fatalf("outcome = %v, want ACCEPTED", resp.GetOutcome())
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// No ledger row for the identity is MISSING: no stamp, and the caller keeps the obligation
// with backoff (an anomaly for a terminalized intent).
func TestAckArtifactCreationCommand_MissingWhenNoRow(t *testing.T) {
	srv, mock, cleanup := newStatusServer(t, &fakeVodS3Client{})
	defer cleanup()

	mock.ExpectQuery(`SELECT status, kind, artifact_hash\s+FROM foghorn\.artifact_creation_commands`).
		WithArgs("req-x", "t1").
		WillReturnError(sql.ErrNoRows)

	resp, err := srv.AckArtifactCreationCommand(context.Background(), &sharedpb.AckArtifactCreationCommandRequest{
		TenantId: "t1", Kind: "vod", ArtifactHash: "vh1", RequestId: "req-x",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.GetOutcome() != sharedpb.ArtifactCreationOutcome_ARTIFACT_CREATION_OUTCOME_MISSING {
		t.Fatalf("outcome = %v, want MISSING", resp.GetOutcome())
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// A row bound to a DIFFERENT kind/hash is IDENTITY_MISMATCH: no stamp, and the caller fails
// closed and never clears the obligation.
func TestAckArtifactCreationCommand_IdentityMismatch(t *testing.T) {
	srv, mock, cleanup := newStatusServer(t, &fakeVodS3Client{})
	defer cleanup()

	mock.ExpectQuery(`SELECT status, kind, artifact_hash\s+FROM foghorn\.artifact_creation_commands`).
		WithArgs("req-1", "t1").
		WillReturnRows(sqlmock.NewRows([]string{"status", "kind", "artifact_hash"}).
			AddRow("committed", "vod", "vh1"))

	resp, err := srv.AckArtifactCreationCommand(context.Background(), &sharedpb.AckArtifactCreationCommandRequest{
		TenantId: "t1", Kind: "vod", ArtifactHash: "wrong-hash", RequestId: "req-1",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.GetOutcome() != sharedpb.ArtifactCreationOutcome_ARTIFACT_CREATION_OUTCOME_IDENTITY_MISMATCH {
		t.Fatalf("outcome = %v, want IDENTITY_MISMATCH", resp.GetOutcome())
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestGetArtifactCreationStatus_Validation(t *testing.T) {
	srv, _, cleanup := newStatusServer(t, &fakeVodS3Client{})
	defer cleanup()

	cases := []*sharedpb.GetArtifactCreationStatusRequest{
		{Kind: "vod", ArtifactHash: "h", RequestId: "r"}, // missing tenant
		{TenantId: "t1", Kind: "vod", ArtifactHash: "h"}, // missing request_id
		{TenantId: "t1", Kind: "bogus", RequestId: "r"},  // unknown kind
	}
	for i, req := range cases {
		if _, err := srv.GetArtifactCreationStatus(context.Background(), req); status.Code(err) != codes.InvalidArgument {
			t.Fatalf("case %d: expected InvalidArgument, got %v", i, status.Code(err))
		}
	}
}

// A retry that carries the same Commodore-minted vod_hash must re-sign the
// EXISTING multipart, not create a second artifact/multipart/outbox. The only DB
// expectation is the idempotency lookup: no Begin/insert means no double-create.
func TestCreateVodUpload_IdempotentRetry(t *testing.T) {
	s3 := &fakeVodS3Client{
		createID: "up-new",
		uploadParts: []storage.UploadPart{
			{PartNumber: 1, PresignedURL: "https://s3.example/part/1"},
			{PartNumber: 2, PresignedURL: "https://s3.example/part/2"},
		},
	}
	srv, mock, cleanup := newStatusServer(t, s3)
	defer cleanup()

	mock.ExpectQuery(`FROM foghorn\.artifacts a`).
		WithArgs("vh1", "00000000-0000-0000-0000-000000000001").
		WillReturnRows(sqlmock.NewRows([]string{"s3_upload_id", "s3_key", "total_parts", "upload_expires_at"}).
			AddRow("up-existing", "vod/t1/vh1/video.mp4", 2, time.Now().Add(time.Hour)))

	internalName := "vod-test"
	vodHash := "vh1"
	resp, err := srv.CreateVodUpload(context.Background(), &sharedpb.CreateVodUploadRequest{
		TenantId:     "00000000-0000-0000-0000-000000000001",
		Filename:     "video.mp4",
		SizeBytes:    1024,
		VodHash:      &vodHash,
		InternalName: &internalName,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.GetUploadId() != "up-existing" {
		t.Fatalf("upload_id = %q, want existing up-existing (no second multipart)", resp.GetUploadId())
	}
	if s3.abortCalls != 0 {
		t.Fatalf("existing-upload retry must not abort, abortCalls=%d", s3.abortCalls)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
