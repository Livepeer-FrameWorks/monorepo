//go:build schema_verify

package commodoredb

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	commodoremigrations "frameworks/api_control/internal/datamigrations"
	dbsql "github.com/Livepeer-FrameWorks/monorepo/pkg/database/sql"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/testutil/dockerpg"
	_ "github.com/lib/pq"
)

func TestGeneratedQueryCatalogPrepares_RealPG(t *testing.T) {
	prepareCommodoreQueryCatalog(t, startCommodoreQueryCatalogRealPG(t))
}

func TestDVRRegistrationSnapshotsReadyWebhookAuthority_RealPG(t *testing.T) {
	db := startCommodoreQueryCatalogRealPG(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	const tenantID = "10000000-0000-0000-0000-000000000001"
	const userID = "20000000-0000-0000-0000-000000000001"
	const streamID = "25000000-0000-0000-0000-000000000001"
	if _, err := db.ExecContext(ctx, `
INSERT INTO commodore.streams (
    id, tenant_id, user_id, stream_key, playback_id, internal_name, title,
    requires_auth, playback_policy, playback_webhook_secret_enc
) VALUES ($1, $2, $3, 'dvr-webhook-key', 'dvr-parent-playback', 'dvr-parent',
          'DVR parent', true, '{"type":"webhook","webhook":{"url":"https://auth.invalid"}}'::jsonb,
          'encrypted-webhook-secret')`, streamID, tenantID, userID); err != nil {
		t.Fatal(err)
	}
	queries := New(db)
	if err := queries.InsertDVRRegistration(ctx, InsertDVRRegistrationParams{
		ID: "30000000-0000-0000-0000-000000000001", TenantID: tenantID, UserID: userID,
		StreamID: streamID, DvrHash: "dvr-webhook-hash", InternalName: "dvr-webhook-internal",
		PlaybackID: "dvr-webhook-playback", StreamInternalName: "dvr-parent",
	}); err != nil {
		t.Fatal(err)
	}
	row, err := queries.LookupDVRPolicyByInternalName(ctx, "dvr-webhook-internal")
	if err != nil {
		t.Fatal(err)
	}
	var ready bool
	if err := db.QueryRowContext(ctx, `SELECT playback_authority_ready FROM commodore.dvr_recordings WHERE dvr_hash='dvr-webhook-hash'`).Scan(&ready); err != nil {
		t.Fatal(err)
	}
	if !ready || !strings.Contains(row.PlaybackPolicy, `"type": "webhook"`) || row.PlaybackWebhookSecretEnc != "encrypted-webhook-secret" {
		t.Fatalf("DVR snapshot ready=%v policy=%q secret=%q", ready, row.PlaybackPolicy, row.PlaybackWebhookSecretEnc)
	}
}

func TestResolveVODByHashIdentifiesChapterParent_RealPG(t *testing.T) {
	db := startCommodoreQueryCatalogRealPG(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	const tenantID = "10000000-0000-0000-0000-000000000001"
	const userID = "20000000-0000-0000-0000-000000000001"
	const streamID = "25000000-0000-0000-0000-000000000001"
	if _, err := db.ExecContext(ctx, `
INSERT INTO commodore.streams (id, tenant_id, user_id, stream_key, playback_id, internal_name, title)
VALUES ($1, $2, $3, 'chapter-parent-key', 'chapter-parent-playback', 'chapter-live-parent', 'Parent')`, streamID, tenantID, userID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `
INSERT INTO commodore.dvr_recordings (
    tenant_id, user_id, stream_id, dvr_hash, internal_name, playback_id, stream_internal_name
) VALUES ($2, $3, $1, 'chapter-parent-dvr', 'chapter-parent-dvr-internal',
          'chapter-parent-dvr-playback', 'chapter-live-parent')`, streamID, tenantID, userID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `
INSERT INTO commodore.dvr_chapter_playback (chapter_id, tenant_id, playback_id, artifact_hash, dvr_hash)
VALUES ('chapter-id', $1, 'chapter-playback', 'chapter-artifact-hash', 'chapter-parent-dvr')`, tenantID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `
INSERT INTO commodore.vod_assets (
    id, tenant_id, user_id, stream_id, vod_hash, internal_name, playback_id, filename,
    origin_type, origin_id, library_visible
) VALUES ('40000000-0000-0000-0000-000000000001', $2, $3, $1,
          'chapter-artifact-hash', 'chapter-internal', 'chapter-playback', 'chapter.mkv',
          'dvr_chapter', 'chapter-id', false)`, streamID, tenantID, userID); err != nil {
		t.Fatal(err)
	}
	row, err := New(db).ResolveVODByHash(ctx, "chapter-artifact-hash")
	if err != nil {
		t.Fatal(err)
	}
	if row.ContentType != "chapter" || row.ParentStreamInternalName != "chapter-live-parent" {
		t.Fatalf("chapter hash resolution = kind:%q parent:%q", row.ContentType, row.ParentStreamInternalName)
	}
}

func TestChapterPlaybackAuthorityDataMigration_RealPG(t *testing.T) {
	db := startCommodoreQueryCatalogRealPG(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	const tenantID = "10000000-0000-0000-0000-000000000001"
	const userID = "20000000-0000-0000-0000-000000000001"
	const streamID = "25000000-0000-0000-0000-000000000001"
	const chapterVODID = "40000000-0000-0000-0000-000000000001"
	const chapterID = "chapter0000000000000000000000001"
	const chapterHash = "chapterhash00000000000000000001"
	const dvrBackedChapterVODID = "40000000-0000-0000-0000-000000000002"
	const orphanChapterVODID = "40000000-0000-0000-0000-000000000003"
	const conflictingParentsChapterVODID = "40000000-0000-0000-0000-000000000004"
	const danglingDVRChapterVODID = "40000000-0000-0000-0000-000000000005"
	const unreadyDVRChapterVODID = "40000000-0000-0000-0000-000000000006"
	if _, err := db.ExecContext(ctx, `
INSERT INTO commodore.streams (
    id, tenant_id, user_id, stream_key, playback_id, internal_name, title,
    requires_auth, playback_policy
) VALUES ($1, $2, $3, 'legacy-chapter-key', 'legacy-parent-playback', 'live-parent',
          'Legacy parent', true, '{"type":"jwt"}'::jsonb)`, streamID, tenantID, userID); err != nil {
		t.Fatal(err)
	}
	// Deliberately omit DVR and chapter-playback registry rows. Legacy chapter
	// VODs must inherit directly through stream_id; requiring a dvr_hash bridge
	// would make this row invisible to both the migration and its verifier.
	if _, err := db.ExecContext(ctx, `
INSERT INTO commodore.vod_assets (
	    id, tenant_id, user_id, stream_id, vod_hash, internal_name, playback_id, filename,
	    origin_type, origin_id, library_visible, requires_auth
) VALUES ($1, $2, $3, $4, $5, 'chapter-vod', 'chapter-playback', 'chapter.mp4',
	      'dvr_chapter', $6, false, false)`, chapterVODID, tenantID, userID, streamID, chapterHash, chapterID); err != nil {
		t.Fatal(err)
	}
	// The stream row may already be gone while the DVR snapshot and chapter
	// identity remain. In that state the DVR is the nearest surviving policy
	// authority and must win over the legacy stream fallback.
	if _, err := db.ExecContext(ctx, `
INSERT INTO commodore.dvr_recordings (
    tenant_id, user_id, stream_id, dvr_hash, internal_name, playback_id,
    stream_internal_name, requires_auth, playback_policy, playback_authority_ready
) VALUES ($1, $2, '25000000-0000-0000-0000-000000000099',
          'dvrhash00000000000000000000002', 'orphan-dvr', 'orphan-dvr-playback',
          'deleted-parent', true, '{"type":"webhook"}'::jsonb, true)`, tenantID, userID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `
INSERT INTO commodore.dvr_chapter_playback
    (chapter_id, tenant_id, playback_id, artifact_hash, dvr_hash)
VALUES ('chapter0000000000000000000000002', $1,
        'chapter-dvr-playback', 'chapterhash00000000000000000002',
        'dvrhash00000000000000000000002')`, tenantID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `
INSERT INTO commodore.vod_assets (
    id, tenant_id, user_id, stream_id, vod_hash, internal_name, playback_id, filename,
    origin_type, origin_id, library_visible, requires_auth, playback_policy
) VALUES ($1, $2, $3, '25000000-0000-0000-0000-000000000099',
          'chapterhash00000000000000000002', 'dvr-backed-chapter',
          'chapter-dvr-playback', 'chapter-dvr.mkv', 'dvr_chapter',
          'chapter0000000000000000000000002', false, false, '{"type":"public"}'::jsonb)`,
		dvrBackedChapterVODID, tenantID, userID); err != nil {
		t.Fatal(err)
	}
	// If neither parent survives, the only safe repair is an explicit
	// fail-closed policy. The row must not remain permanently unverifiable.
	if _, err := db.ExecContext(ctx, `
INSERT INTO commodore.vod_assets (
    id, tenant_id, user_id, stream_id, vod_hash, internal_name, playback_id, filename,
    origin_type, origin_id, library_visible, requires_auth, playback_policy
) VALUES ($1, $2, $3, '25000000-0000-0000-0000-000000000098',
          'chapterhash00000000000000000003', 'fully-orphan-chapter',
          'chapter-orphan-playback', 'chapter-orphan.mkv', 'dvr_chapter',
          'chapter0000000000000000000000003', false, false, '{"type":"public"}'::jsonb)`,
		orphanChapterVODID, tenantID, userID); err != nil {
		t.Fatal(err)
	}
	// When both parents exist, every policy field must come from the same
	// nearest authority (the DVR snapshot), never a stream/DVR mixture.
	if _, err := db.ExecContext(ctx, `
INSERT INTO commodore.dvr_recordings (
    tenant_id, user_id, stream_id, dvr_hash, internal_name, playback_id,
    stream_internal_name, requires_auth, playback_policy, playback_authority_ready
) VALUES ($1, $2, $3, 'dvrhash00000000000000000000004', 'conflict-dvr',
          'conflict-dvr-playback', 'live-parent', false, '{"type":"public"}'::jsonb, true)`, tenantID, userID, streamID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `
INSERT INTO commodore.dvr_chapter_playback
    (chapter_id, tenant_id, playback_id, artifact_hash, dvr_hash)
VALUES ('chapter0000000000000000000000004', $1,
        'chapter-conflict-playback', 'chapterhash00000000000000000004',
        'dvrhash00000000000000000000004')`, tenantID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `
INSERT INTO commodore.vod_assets (
    id, tenant_id, user_id, stream_id, vod_hash, internal_name, playback_id, filename,
    origin_type, origin_id, library_visible, requires_auth, playback_policy
) VALUES ($1, $2, $3, $4, 'chapterhash00000000000000000004', 'conflict-chapter',
          'chapter-conflict-playback', 'chapter-conflict.mkv', 'dvr_chapter',
          'chapter0000000000000000000000004', false, true, '{"type":"jwt"}'::jsonb)`,
		conflictingParentsChapterVODID, tenantID, userID, streamID); err != nil {
		t.Fatal(err)
	}
	// A chapter identity that names a missing DVR must not silently inherit
	// from the stream. Runtime policy cascades treat the DVR linkage as
	// authoritative, so migration converges the dangling row to fail-closed.
	if _, err := db.ExecContext(ctx, `
INSERT INTO commodore.dvr_chapter_playback
    (chapter_id, tenant_id, playback_id, artifact_hash, dvr_hash)
VALUES ('chapter0000000000000000000000005', $1,
        'chapter-dangling-playback', 'chapterhash00000000000000000005',
        'missingdvr000000000000000000005')`, tenantID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `
INSERT INTO commodore.vod_assets (
    id, tenant_id, user_id, stream_id, vod_hash, internal_name, playback_id, filename,
    origin_type, origin_id, library_visible, requires_auth, playback_policy
) VALUES ($1, $2, $3, $4, 'chapterhash00000000000000000005', 'dangling-dvr-chapter',
          'chapter-dangling-playback', 'chapter-dangling.mkv', 'dvr_chapter',
          'chapter0000000000000000000000005', false, false, '{"type":"public"}'::jsonb)`,
		danglingDVRChapterVODID, tenantID, userID, streamID); err != nil {
		t.Fatal(err)
	}
	// A rolling old replica can insert a DVR after the DVR migration completed,
	// before its playback-authority snapshot is ready. The chapter migration
	// must use the parent stream until that readiness bit flips.
	if _, err := db.ExecContext(ctx, `
INSERT INTO commodore.dvr_recordings (
    tenant_id, user_id, stream_id, dvr_hash, internal_name, playback_id,
    stream_internal_name, requires_auth, playback_policy, playback_authority_ready
) VALUES ($1, $2, $3, 'dvrhash00000000000000000000006', 'unready-dvr',
          'unready-dvr-playback', 'live-parent', false, '{"type":"public"}'::jsonb, false)`, tenantID, userID, streamID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `
INSERT INTO commodore.dvr_chapter_playback
    (chapter_id, tenant_id, playback_id, artifact_hash, dvr_hash)
VALUES ('chapter0000000000000000000000006', $1,
        'chapter-unready-playback', 'chapterhash00000000000000000006',
        'dvrhash00000000000000000000006')`, tenantID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `
INSERT INTO commodore.vod_assets (
    id, tenant_id, user_id, stream_id, vod_hash, internal_name, playback_id, filename,
    origin_type, origin_id, library_visible, requires_auth, playback_policy
) VALUES ($1, $2, $3, $4, 'chapterhash00000000000000000006', 'unready-dvr-chapter',
          'chapter-unready-playback', 'chapter-unready.mkv', 'dvr_chapter',
          'chapter0000000000000000000000006', false, false, '{"type":"public"}'::jsonb)`,
		unreadyDVRChapterVODID, tenantID, userID, streamID); err != nil {
		t.Fatal(err)
	}
	var scanned, changed, skipped int64
	if err := db.QueryRowContext(ctx, commodoremigrations.ChapterPlaybackAuthorityBatchSQL, 100).Scan(&scanned, &changed, &skipped); err != nil {
		t.Fatal(err)
	}
	var requiresAuth bool
	var policy string
	if err := db.QueryRowContext(ctx, `SELECT requires_auth, playback_policy::text FROM commodore.vod_assets WHERE id=$1`, chapterVODID).Scan(&requiresAuth, &policy); err != nil {
		t.Fatal(err)
	}
	if scanned != 6 || changed != 6 || skipped != 0 || !requiresAuth || policy != `{"type": "jwt"}` {
		t.Fatalf("chapter migration = scanned:%d changed:%d skipped:%d auth:%v policy:%q", scanned, changed, skipped, requiresAuth, policy)
	}
	var dvrRequiresAuth, orphanRequiresAuth bool
	var dvrPolicy string
	var orphanPolicy sql.NullString
	if err := db.QueryRowContext(ctx, `SELECT requires_auth, playback_policy::text FROM commodore.vod_assets WHERE id=$1`, dvrBackedChapterVODID).
		Scan(&dvrRequiresAuth, &dvrPolicy); err != nil {
		t.Fatal(err)
	}
	if !dvrRequiresAuth || dvrPolicy != `{"type": "webhook"}` {
		t.Fatalf("DVR-backed orphan chapter auth=%v policy=%q, want true/webhook", dvrRequiresAuth, dvrPolicy)
	}
	var conflictRequiresAuth bool
	var conflictPolicy string
	if err := db.QueryRowContext(ctx, `SELECT requires_auth, playback_policy::text FROM commodore.vod_assets WHERE id=$1`, conflictingParentsChapterVODID).
		Scan(&conflictRequiresAuth, &conflictPolicy); err != nil {
		t.Fatal(err)
	}
	if conflictRequiresAuth || conflictPolicy != `{"type": "public"}` {
		t.Fatalf("conflicting-parent chapter auth=%v policy=%q, want DVR snapshot false/public", conflictRequiresAuth, conflictPolicy)
	}
	var danglingRequiresAuth bool
	var danglingPolicy sql.NullString
	if err := db.QueryRowContext(ctx, `SELECT requires_auth, playback_policy::text FROM commodore.vod_assets WHERE id=$1`, danglingDVRChapterVODID).
		Scan(&danglingRequiresAuth, &danglingPolicy); err != nil {
		t.Fatal(err)
	}
	if !danglingRequiresAuth || !danglingPolicy.Valid || danglingPolicy.String != `{"type": "jwt"}` {
		t.Fatalf("dangling-DVR chapter auth=%v policy=%v, want stream fallback true/JWT", danglingRequiresAuth, danglingPolicy)
	}
	var unreadyRequiresAuth bool
	var unreadyPolicy string
	if err := db.QueryRowContext(ctx, `SELECT requires_auth, playback_policy::text FROM commodore.vod_assets WHERE id=$1`, unreadyDVRChapterVODID).
		Scan(&unreadyRequiresAuth, &unreadyPolicy); err != nil {
		t.Fatal(err)
	}
	if !unreadyRequiresAuth || unreadyPolicy != `{"type": "jwt"}` {
		t.Fatalf("unready-DVR chapter auth=%v policy=%q, want stream fallback true/JWT", unreadyRequiresAuth, unreadyPolicy)
	}
	if err := db.QueryRowContext(ctx, `SELECT requires_auth, playback_policy::text FROM commodore.vod_assets WHERE id=$1`, orphanChapterVODID).
		Scan(&orphanRequiresAuth, &orphanPolicy); err != nil {
		t.Fatal(err)
	}
	if !orphanRequiresAuth || orphanPolicy.Valid {
		t.Fatalf("fully orphaned chapter auth=%v policy=%v, want fail-closed true/NULL", orphanRequiresAuth, orphanPolicy)
	}
}

func TestChapterPlaybackAuthorityBackfillDoesNotOverwriteConcurrentPolicyCascade_RealPG(t *testing.T) {
	db := startCommodoreQueryCatalogRealPG(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	const tenantID = "10000000-0000-0000-0000-000000000001"
	const userID = "20000000-0000-0000-0000-000000000001"
	const streamID = "25000000-0000-0000-0000-000000000001"
	const chapterVODID = "40000000-0000-0000-0000-000000000001"
	if _, err := db.ExecContext(ctx, `
INSERT INTO commodore.streams (
    id, tenant_id, user_id, stream_key, playback_id, internal_name, title,
    requires_auth, playback_policy
) VALUES ($1, $2, $3, 'concurrent-policy-key', 'concurrent-policy-playback',
          'concurrent-policy-stream', 'Concurrent policy stream', false,
          '{"type":"public"}'::jsonb)`, streamID, tenantID, userID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `
INSERT INTO commodore.vod_assets (
    id, tenant_id, user_id, stream_id, vod_hash, internal_name, playback_id, filename,
    origin_type, origin_id, library_visible, requires_auth, playback_policy
) VALUES ($1, $2, $3, $4, 'concurrentchapterhash00000000001',
          'concurrent-chapter', 'concurrent-chapter-playback', 'chapter.mkv',
          'dvr_chapter', 'concurrent-chapter-id', false, true, '{"type":"jwt"}'::jsonb)`,
		chapterVODID, tenantID, userID, streamID); err != nil {
		t.Fatal(err)
	}

	policyTx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := New(policyTx).SetStreamPlaybackPolicy(ctx, SetStreamPlaybackPolicyParams{
		RequiresAuth: true, PlaybackPolicy: `{"type":"webhook"}`,
		WebhookSecret: sql.NullString{String: "new-secret", Valid: true},
		TargetID:      streamID, TenantID: tenantID,
	}); err != nil {
		_ = policyTx.Rollback()
		t.Fatal(err)
	}

	// The policy transaction owns the chapter row. The backfill sees the old
	// committed snapshot, but SKIP LOCKED must prevent that snapshot from
	// becoming a stale write while the runtime cascade is in flight.
	var scanned, changed, skipped int64
	if err := db.QueryRowContext(ctx, commodoremigrations.ChapterPlaybackAuthorityBatchSQL, 100).
		Scan(&scanned, &changed, &skipped); err != nil {
		_ = policyTx.Rollback()
		t.Fatal(err)
	}
	if scanned != 0 || changed != 0 || skipped != 0 {
		_ = policyTx.Rollback()
		t.Fatalf("concurrent backfill scanned=%d changed=%d skipped=%d, want locked row skipped", scanned, changed, skipped)
	}
	if err := policyTx.Commit(); err != nil {
		t.Fatal(err)
	}

	if err := db.QueryRowContext(ctx, commodoremigrations.ChapterPlaybackAuthorityBatchSQL, 100).
		Scan(&scanned, &changed, &skipped); err != nil {
		t.Fatal(err)
	}
	if scanned != 0 || changed != 0 || skipped != 0 {
		t.Fatalf("post-cascade backfill scanned=%d changed=%d skipped=%d, want no stale repair", scanned, changed, skipped)
	}
	var requiresAuth bool
	var policy string
	var secret sql.NullString
	if err := db.QueryRowContext(ctx, `
SELECT requires_auth, playback_policy::text, playback_webhook_secret_enc
FROM commodore.vod_assets WHERE id=$1`, chapterVODID).Scan(&requiresAuth, &policy, &secret); err != nil {
		t.Fatal(err)
	}
	if !requiresAuth || policy != `{"type": "webhook"}` || !secret.Valid || secret.String != "new-secret" {
		t.Fatalf("chapter authority after concurrent cascade: auth=%v policy=%q secret=%v", requiresAuth, policy, secret)
	}
}

func TestUpsertChapterPlaybackIDRejectsCrossTenantCollision_RealPG(t *testing.T) {
	db := startCommodoreQueryCatalogRealPG(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	queries := New(db)
	first := UpsertChapterPlaybackIDParams{
		ChapterID: "shared-chapter", TenantID: "10000000-0000-0000-0000-000000000001",
		PlaybackID: "tenant-a-playback", ArtifactHash: "tenant-a-hash", DvrHash: "tenant-a-dvr",
	}
	if _, err := queries.UpsertChapterPlaybackID(ctx, first); err != nil {
		t.Fatal(err)
	}
	second := UpsertChapterPlaybackIDParams{
		ChapterID: first.ChapterID, TenantID: "10000000-0000-0000-0000-000000000002",
		PlaybackID: "tenant-b-playback", ArtifactHash: "tenant-b-hash", DvrHash: "tenant-b-dvr",
	}
	if _, err := queries.UpsertChapterPlaybackID(ctx, second); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("cross-tenant upsert error = %v, want sql.ErrNoRows", err)
	}
	var tenantID, playbackID, artifactHash, dvrHash string
	if err := db.QueryRowContext(ctx, `
		SELECT tenant_id::text, playback_id, artifact_hash, dvr_hash
		FROM commodore.dvr_chapter_playback WHERE chapter_id = $1
	`, first.ChapterID).Scan(&tenantID, &playbackID, &artifactHash, &dvrHash); err != nil {
		t.Fatal(err)
	}
	if tenantID != first.TenantID || playbackID != first.PlaybackID || artifactHash != first.ArtifactHash || dvrHash != first.DvrHash {
		t.Fatalf("cross-tenant collision mutated owner row: tenant=%q playback=%q artifact=%q dvr=%q", tenantID, playbackID, artifactHash, dvrHash)
	}
}

func TestGeneratedQueryCatalogPrepares_RealYugabyte(t *testing.T) {
	prepareCommodoreQueryCatalog(t, startCommodoreQueryCatalogRealYugabyte(t))
}

func prepareCommodoreQueryCatalog(t *testing.T, db *sql.DB) {
	t.Helper()
	queries := commodoreGeneratedQueries(t)
	if len(queries) != 305 {
		t.Fatalf("found %d generated Commodore queries, want 305", len(queries))
	}
	ctx := context.Background()
	conn, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	for index, query := range queries {
		name := fmt.Sprintf("commodore_contract_%d", index)
		if _, err := conn.ExecContext(ctx, "PREPARE "+name+" AS "+query.sql); err != nil {
			t.Fatalf("prepare %s from %s: %v\n%s", query.name, query.file, err, query.sql)
		}
		if _, err := conn.ExecContext(ctx, "DEALLOCATE "+name); err != nil {
			t.Fatalf("deallocate %s: %v", query.name, err)
		}
	}
}

func TestArtifactCreationCommandAckLease_RealPG(t *testing.T) {
	verifyArtifactCreationCommandAckLease(t, startCommodoreQueryCatalogRealPG(t))
}

func TestArtifactCreationCommandAckLease_RealYugabyte(t *testing.T) {
	verifyArtifactCreationCommandAckLease(t, startCommodoreQueryCatalogRealYugabyte(t))
}

func verifyArtifactCreationCommandAckLease(t *testing.T, db *sql.DB) {
	t.Helper()
	ctx := context.Background()
	const tenantID = "11111111-1111-1111-1111-111111111111"
	for i := 0; i < 6; i++ {
		requestID := fmt.Sprintf("00000000-0000-0000-0000-%012d", i+1)
		if _, err := db.ExecContext(ctx, `INSERT INTO commodore.artifact_creation_intents
			(tenant_id, kind, artifact_hash, request_id, origin_cluster_id, status, command_ack_pending)
			VALUES ($1::uuid, 'clip', $2, $3::uuid, 'cluster-a', 'committed', TRUE)`, tenantID, fmt.Sprintf("lease-hash-%d", i), requestID); err != nil {
			t.Fatal(err)
		}
	}
	queries := New(db)
	claim := func(token string, limit int32) []ClaimArtifactCreationCommandAcksRow {
		rows, err := queries.ClaimArtifactCreationCommandAcks(ctx, ClaimArtifactCreationCommandAcksParams{
			LeaseInterval: "2 minutes", LeaseToken: token, BatchSize: limit,
		})
		if err != nil {
			t.Fatalf("claim %s: %v", token, err)
		}
		return rows
	}
	tokenA := "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"
	tokenB := "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb"
	first := claim(tokenA, 3)
	second := claim(tokenB, 3)
	if len(first) != 3 || len(second) != 3 {
		t.Fatalf("claim sizes = %d, %d, want 3, 3", len(first), len(second))
	}
	claimed := make(map[string]bool, len(first))
	for _, row := range first {
		claimed[row.ArtifactHash] = true
	}
	for _, row := range second {
		if claimed[row.ArtifactHash] {
			t.Fatalf("second replica reclaimed live lease for %s", row.ArtifactHash)
		}
	}
	if rows := claim("cccccccc-cccc-cccc-cccc-cccccccccccc", 6); len(rows) != 0 {
		t.Fatalf("fully leased catalog returned %d rows", len(rows))
	}
	if _, err := db.ExecContext(ctx, `UPDATE commodore.artifact_creation_intents
		SET command_ack_leased_until = NOW() - INTERVAL '1 second'
		WHERE command_ack_lease_token = $1::uuid`, tokenA); err != nil {
		t.Fatal(err)
	}
	tokenC := "cccccccc-cccc-cccc-cccc-cccccccccccc"
	reclaimed := claim(tokenC, 6)
	if len(reclaimed) != 3 {
		t.Fatalf("reclaimed rows = %d, want 3", len(reclaimed))
	}
	target := reclaimed[0]
	stale := BackoffArtifactCreationCommandAckParams{
		TenantID: target.TenantID, Kind: target.Kind, ArtifactHash: target.ArtifactHash, LeaseToken: tokenA,
	}
	if err := queries.BackoffArtifactCreationCommandAck(ctx, stale); err != nil {
		t.Fatal(err)
	}
	var currentToken sql.NullString
	if err := db.QueryRowContext(ctx, `SELECT command_ack_lease_token::text FROM commodore.artifact_creation_intents
		WHERE tenant_id=$1::uuid AND kind=$2 AND artifact_hash=$3`, target.TenantID, target.Kind, target.ArtifactHash).Scan(&currentToken); err != nil {
		t.Fatal(err)
	}
	if currentToken.String != tokenC {
		t.Fatalf("stale worker changed current lease token to %q", currentToken.String)
	}
	stale.LeaseToken = tokenC
	if err := queries.BackoffArtifactCreationCommandAck(ctx, stale); err != nil {
		t.Fatal(err)
	}
	var attempts int
	var leaseCleared bool
	if err := db.QueryRowContext(ctx, `SELECT command_ack_attempts, command_ack_lease_token IS NULL
		FROM commodore.artifact_creation_intents WHERE tenant_id=$1::uuid AND kind=$2 AND artifact_hash=$3`, target.TenantID, target.Kind, target.ArtifactHash).Scan(&attempts, &leaseCleared); err != nil {
		t.Fatal(err)
	}
	if attempts != 1 || !leaseCleared {
		t.Fatalf("backoff attempts=%d leaseCleared=%t", attempts, leaseCleared)
	}
	clear := reclaimed[1]
	if err := queries.ClearArtifactCreationCommandAck(ctx, ClearArtifactCreationCommandAckParams{
		TenantID: clear.TenantID, Kind: clear.Kind, ArtifactHash: clear.ArtifactHash, LeaseToken: tokenC,
	}); err != nil {
		t.Fatal(err)
	}
	var pending bool
	if err := db.QueryRowContext(ctx, `SELECT command_ack_pending FROM commodore.artifact_creation_intents
		WHERE tenant_id=$1::uuid AND kind=$2 AND artifact_hash=$3`, clear.TenantID, clear.Kind, clear.ArtifactHash).Scan(&pending); err != nil {
		t.Fatal(err)
	}
	if pending {
		t.Fatal("current lease owner did not clear acknowledgement obligation")
	}
}

func TestManualQueryAdapters_RealPG(t *testing.T) {
	db := startCommodoreQueryCatalogRealPG(t)
	ctx := context.Background()
	const (
		tenantID = "11111111-1111-1111-1111-111111111111"
		userID   = "22222222-2222-2222-2222-222222222222"
		streamID = "33333333-3333-3333-3333-333333333333"
		vodID    = "44444444-4444-4444-4444-444444444444"
	)
	if _, err := db.ExecContext(ctx, `
		INSERT INTO commodore.users (id, tenant_id, email, password_hash, is_active)
		VALUES ($1::uuid, $2::uuid, 'adapter@example.com', 'x', TRUE)
	`, userID, tenantID); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	if _, err := db.ExecContext(ctx, `
		INSERT INTO commodore.streams
		    (id, tenant_id, user_id, internal_name, stream_key, playback_id, ingest_mode, title)
		VALUES ($1::uuid, $2::uuid, $3::uuid, 'adapter-stream', 'adapter-key', 'adapter-playback', 'push', 'Adapter stream')
	`, streamID, tenantID, userID); err != nil {
		t.Fatalf("seed stream: %v", err)
	}

	queries := New(db)
	affected, refused, err := queries.ApplyActiveIngestPlacementBatch(ctx, ActiveIngestPlacementBatchParams{
		TenantIDs: []string{tenantID}, InternalNames: []string{"adapter-stream"},
		ClaimTokens: []string{"adapter-claim"}, ClusterIDs: []string{"media-eu"},
		LeaseSeconds: 60, Renew: true,
	})
	if err != nil || affected != 1 || len(refused) != 0 {
		t.Fatalf("renew placement: affected=%d refused=%v err=%v", affected, refused, err)
	}
	affected, refused, err = queries.ApplyActiveIngestPlacementBatch(ctx, ActiveIngestPlacementBatchParams{
		TenantIDs: []string{tenantID}, InternalNames: []string{"adapter-stream"},
		ClaimTokens: []string{"adapter-claim"}, ClusterIDs: []string{"media-eu"}, Renew: false,
	})
	if err != nil || affected != 1 || len(refused) != 0 {
		t.Fatalf("release placement: affected=%d refused=%v err=%v", affected, refused, err)
	}

	if err := queries.InsertVODUploadRegistration(ctx, InsertVODUploadRegistrationParams{
		ID: vodID, TenantID: tenantID, UserID: userID, VodHash: "adapter-vod",
		InternalName: "vod+adapter", PlaybackID: "adapter-vod-playback",
		Title:       sql.NullString{String: "Adapter VOD", Valid: true},
		Description: sql.NullString{String: "contract", Valid: true},
		Filename:    "adapter.mp4", ContentType: sql.NullString{String: "video/mp4", Valid: true},
		SizeBytes:       sql.NullInt64{Int64: 123, Valid: true},
		OriginClusterID: sql.NullString{String: "media-eu", Valid: true},
	}); err != nil {
		t.Fatalf("insert VOD registration: %v", err)
	}
	resolvedStream, err := queries.ResolveIdentifierCatalog(ctx, ResolveIdentifierCatalogParams{
		IncludeIds: true,
		Identifier: streamID,
	})
	if err != nil {
		t.Fatalf("resolve stream identifier: %v", err)
	}
	if resolvedStream.IdentifierType != "stream_id" || resolvedStream.StreamID != streamID || resolvedStream.TenantID != tenantID {
		t.Fatalf("unexpected resolved stream: %+v", resolvedStream)
	}
	resolvedVOD, err := queries.ResolveIdentifierCatalog(ctx, ResolveIdentifierCatalogParams{
		Identifier: "adapter-vod",
	})
	if err != nil {
		t.Fatalf("resolve VOD identifier: %v", err)
	}
	if resolvedVOD.IdentifierType != "vod" || resolvedVOD.TenantID != tenantID || resolvedVOD.UserID != userID {
		t.Fatalf("unexpected resolved VOD: %+v", resolvedVOD)
	}
	catalog, err := queries.ListStorageArtifactCatalog(ctx, StorageArtifactFilter{
		TenantID: tenantID, SortField: "created_at", SortDirection: "DESC", Limit: 25,
	})
	if err != nil {
		t.Fatalf("list storage catalog: %v", err)
	}
	if catalog.Total != 1 || len(catalog.Rows) != 1 || catalog.Rows[0].ArtifactHash != "adapter-vod" || catalog.KindCounts["vod"] != 1 {
		t.Fatalf("unexpected storage catalog: total=%d rows=%+v facets=%v", catalog.Total, catalog.Rows, catalog.KindCounts)
	}
}

type commodoreGeneratedQuery struct {
	file string
	name string
	sql  string
}

func commodoreGeneratedQueries(t *testing.T) []commodoreGeneratedQuery {
	t.Helper()
	paths, err := filepath.Glob("*.sql.go")
	if err != nil {
		t.Fatal(err)
	}
	var queries []commodoreGeneratedQuery
	for _, path := range paths {
		file, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", path, err)
		}
		for _, declaration := range file.Decls {
			general, ok := declaration.(*ast.GenDecl)
			if !ok || general.Tok != token.CONST {
				continue
			}
			for _, specification := range general.Specs {
				value, ok := specification.(*ast.ValueSpec)
				if !ok {
					continue
				}
				for index, expression := range value.Values {
					literal, ok := expression.(*ast.BasicLit)
					if !ok || literal.Kind != token.STRING {
						continue
					}
					querySQL, err := strconv.Unquote(literal.Value)
					if err != nil {
						t.Fatal(err)
					}
					if !strings.HasPrefix(querySQL, "-- name:") {
						continue
					}
					queryName := "unknown"
					if index < len(value.Names) {
						queryName = value.Names[index].Name
					}
					queries = append(queries, commodoreGeneratedQuery{file: path, name: queryName, sql: querySQL})
				}
			}
		}
	}
	return queries
}

func startCommodoreQueryCatalogRealPG(t *testing.T) *sql.DB {
	t.Helper()
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker not available")
	}
	name := fmt.Sprintf("fw-commodore-query-catalog-realpg-%d", time.Now().UnixNano())
	t.Cleanup(func() { _, _ = dockerpg.CLI("rm", "-fv", name) })
	image, err := dockerpg.PostgresImage()
	if err != nil {
		t.Fatal(err)
	}
	if output, err := dockerpg.Run("run", "-d", "--name", name, "-P", "-e", "POSTGRES_PASSWORD=harness", image); err != nil {
		t.Fatalf("docker run: %v\n%s", err, output)
	}
	port, err := dockerpg.DiscoverPublishedHostPort(name, "5432/tcp")
	if err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("postgres", fmt.Sprintf("postgres://postgres:harness@127.0.0.1:%s/postgres?sslmode=disable", port))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := dockerpg.WaitReady(db, name); err != nil {
		t.Fatal(err)
	}
	schema, err := dbsql.Content.ReadFile("schema/commodore.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(string(schema)); err != nil {
		t.Fatal(err)
	}
	return db
}

func startCommodoreQueryCatalogRealYugabyte(t *testing.T) *sql.DB {
	t.Helper()
	if db, ok := dockerpg.OpenSharedYugabyteDatabase(t, "commodore"); ok {
		schema, err := dbsql.Content.ReadFile("schema/commodore.sql")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(string(schema)); err != nil {
			t.Fatal(err)
		}
		return db
	}
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker not available")
	}
	name := fmt.Sprintf("fw-commodore-query-catalog-yb-%d", time.Now().UnixNano())
	t.Cleanup(func() { _, _ = dockerpg.CLI("rm", "-fv", name) })
	image, err := dockerpg.YugabyteImage()
	if err != nil {
		t.Fatal(err)
	}
	if output, err := dockerpg.Run("run", "-d", "--name", name, "-P", "--hostname", name, image, "bash", "-c", `exec bin/yugabyted start --background=false --advertise_address="$(hostname -i)"`); err != nil {
		t.Fatalf("docker run: %v\n%s", err, output)
	}
	port, err := dockerpg.DiscoverPublishedHostPort(name, "5433/tcp")
	if err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("postgres", fmt.Sprintf("postgres://yugabyte@127.0.0.1:%s/yugabyte?sslmode=disable", port))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := dockerpg.WaitReadyFor(db, name, 3*time.Minute); err != nil {
		t.Fatal(err)
	}
	schema, err := dbsql.Content.ReadFile("schema/commodore.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(string(schema)); err != nil {
		t.Fatal(err)
	}
	return db
}
