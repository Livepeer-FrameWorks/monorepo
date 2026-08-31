//go:build schema_verify

package grpc

import (
	"context"
	"database/sql"
	"testing"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	commodorepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"
	"github.com/sirupsen/logrus"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestPlaybackPolicyRepository_RealPG(t *testing.T) {
	db := startCommodoreRealPG(t)
	const (
		tenantID    = "10000000-0000-4000-8000-000000000001"
		userID      = "20000000-0000-4000-8000-000000000001"
		streamID    = "30000000-0000-4000-8000-000000000001"
		otherStream = "30000000-0000-4000-8000-000000000002"
		vodHash     = "vodhash00000000000000000000001"
		clipHash    = "cliphash000000000000000000001"
		chapterHash = "chapterhash00000000000000000001"
		chapterID   = "chapter0000000000000000000000001"
	)
	ctx := context.WithValue(context.Background(), ctxkeys.KeyUserID, userID)
	ctx = context.WithValue(ctx, ctxkeys.KeyTenantID, tenantID)

	if _, err := db.ExecContext(ctx, `
		INSERT INTO commodore.streams
		    (id, tenant_id, user_id, stream_key, playback_id, internal_name, title)
		VALUES ($1::uuid, $2::uuid, $3::uuid, 'stream-key', 'StreamPlayback', 'stream-internal', 'title')
	`, streamID, tenantID, userID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `
		INSERT INTO commodore.streams
		    (id, tenant_id, user_id, stream_key, playback_id, internal_name, title)
		VALUES ($1::uuid, $2::uuid, $3::uuid, 'other-stream-key', 'OtherPlayback', 'other-stream', 'other')
	`, otherStream, tenantID, userID); err != nil {
		t.Fatal(err)
	}
	var vodID, clipID string
	if err := db.QueryRowContext(ctx, `
		INSERT INTO commodore.vod_assets
		    (tenant_id, user_id, vod_hash, internal_name, playback_id, filename)
		VALUES ($1::uuid, $2::uuid, $3, 'vod-internal', 'VodPlayback', 'video.mp4')
		RETURNING id::text
	`, tenantID, userID, vodHash).Scan(&vodID); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(ctx, `
		INSERT INTO commodore.clips
		    (tenant_id, user_id, stream_id, clip_hash, internal_name, playback_id, start_time, duration)
		VALUES ($1::uuid, $2::uuid, $3::uuid, $4, 'clip-internal', 'ClipPlayback', 0, 1000)
		RETURNING id::text
	`, tenantID, userID, streamID, clipHash).Scan(&clipID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `
		INSERT INTO commodore.dvr_recordings
		    (tenant_id, user_id, stream_id, dvr_hash, internal_name, playback_id, stream_internal_name)
		VALUES ($1::uuid, $2::uuid, $3::uuid, 'dvrhash00000000000000000000001',
		        'dvr-internal', 'DVRPlayback', 'stream-internal')
	`, tenantID, userID, streamID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `
		INSERT INTO commodore.dvr_chapter_playback
		    (chapter_id, tenant_id, playback_id, artifact_hash, dvr_hash)
		VALUES ($1, $2::uuid, 'ChapterPlayback', $3, 'dvrhash00000000000000000000001')
	`, chapterID, tenantID, chapterHash); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `
		INSERT INTO commodore.vod_assets
		    (tenant_id, user_id, stream_id, vod_hash, internal_name, playback_id, filename,
		     origin_type, origin_id, library_visible, requires_auth, playback_policy)
		VALUES ($1::uuid, $2::uuid, $3::uuid, $4, 'chapter-internal', 'ChapterPlayback', 'chapter.mkv',
		        'dvr_chapter', $5, false, false, '{"type":"public"}'::jsonb)
	`, tenantID, userID, streamID, chapterHash, chapterID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `
		INSERT INTO commodore.signing_keys (tenant_id, kid, name, public_key_pem, status)
		VALUES ($1::uuid, 'kid-active', 'active', 'public-pem', 'active')
	`, tenantID); err != nil {
		t.Fatal(err)
	}

	server := &CommodoreServer{db: db, logger: logrus.New()}
	if _, err := server.SetPlaybackPolicy(ctx, &commodorepb.SetPlaybackPolicyRequest{
		StreamId: streamID,
		Type:     "jwt",
		Jwt:      &commodorepb.PlaybackJwtPolicy{AllowedKids: []string{"kid-active"}},
	}); err != nil {
		t.Fatalf("set stream JWT policy: %v", err)
	}
	if _, err := server.SetPlaybackPolicy(ctx, &commodorepb.SetPlaybackPolicyRequest{VodAssetId: vodHash, Type: "public"}); err != nil {
		t.Fatalf("set VOD policy by hash: %v", err)
	}
	if _, err := server.SetPlaybackPolicy(ctx, &commodorepb.SetPlaybackPolicyRequest{ClipId: clipID, Type: "public"}); err != nil {
		t.Fatalf("set clip policy by UUID: %v", err)
	}
	if _, err := server.SetPlaybackPolicy(ctx, &commodorepb.SetPlaybackPolicyRequest{VodAssetId: chapterHash, Type: "public"}); status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("direct chapter VOD policy update code = %v, err = %v", status.Code(err), err)
	}

	streamPolicy, err := server.ResolvePlaybackPolicy(ctx, &commodorepb.ResolvePlaybackPolicyRequest{PlaybackId: "streamplayback"})
	if err != nil || streamPolicy.GetType() != "jwt" || streamPolicy.GetTenantId() != tenantID || len(streamPolicy.GetJwtPolicy().GetActiveKeys()) != 1 {
		t.Fatalf("stream playback policy = %#v, err = %v", streamPolicy, err)
	}
	internalPolicy, err := server.ResolvePlaybackPolicy(ctx, &commodorepb.ResolvePlaybackPolicyRequest{InternalName: "stream-internal"})
	if err != nil || internalPolicy.GetType() != "jwt" {
		t.Fatalf("stream internal policy = %#v, err = %v", internalPolicy, err)
	}
	// Prove the DVR resolver consumes the recording snapshot, not a fresh join
	// to the mutable stream row. The public API cascades normal changes; this
	// direct mutation models legacy/drifted data that the read path must not
	// reinterpret as the DVR's policy.
	if _, err := db.ExecContext(ctx, `UPDATE commodore.streams SET requires_auth=false, playback_policy='{"type":"public"}'::jsonb WHERE id=$1::uuid`, streamID); err != nil {
		t.Fatal(err)
	}
	dvrPolicy, err := server.ResolvePlaybackPolicy(ctx, &commodorepb.ResolvePlaybackPolicyRequest{PlaybackId: "dvrplayback"})
	if err != nil || dvrPolicy.GetType() != "jwt" {
		t.Fatalf("inherited DVR policy = %#v, err = %v", dvrPolicy, err)
	}
	chapterPolicy, err := server.ResolvePlaybackPolicy(ctx, &commodorepb.ResolvePlaybackPolicyRequest{PlaybackId: "chapterplayback"})
	if err != nil || chapterPolicy.GetType() != "jwt" {
		t.Fatalf("snapshotted chapter policy = %#v, err = %v", chapterPolicy, err)
	}
	var chapterRequiresAuth bool
	var chapterPolicyType string
	if err := db.QueryRowContext(ctx, `
		SELECT requires_auth, playback_policy->>'type'
		FROM commodore.vod_assets
		WHERE tenant_id = $1::uuid AND vod_hash = $2
	`, tenantID, chapterHash).Scan(&chapterRequiresAuth, &chapterPolicyType); err != nil {
		t.Fatal(err)
	}
	if !chapterRequiresAuth || chapterPolicyType != "jwt" {
		t.Fatalf("chapter snapshot auth=%v policy=%q, want true/jwt", chapterRequiresAuth, chapterPolicyType)
	}
	vodPolicy, err := server.ResolvePlaybackPolicy(ctx, &commodorepb.ResolvePlaybackPolicyRequest{InternalName: "vod-internal"})
	if err != nil || vodPolicy.GetType() != "public" {
		t.Fatalf("VOD internal policy = %#v, err = %v", vodPolicy, err)
	}
	clipPolicy, err := server.ResolvePlaybackPolicy(ctx, &commodorepb.ResolvePlaybackPolicyRequest{PlaybackId: "clipplayback"})
	if err != nil || clipPolicy.GetType() != "public" {
		t.Fatalf("clip playback policy = %#v, err = %v", clipPolicy, err)
	}

	if _, err := db.ExecContext(ctx, `
		INSERT INTO commodore.artifact_catalog_tombstones
		    (tenant_id, kind, artifact_hash, origin_cluster_id, deletion_revision)
		VALUES ($1::uuid, 'vod', $2, 'media-1', 1)
	`, tenantID, vodHash); err != nil {
		t.Fatal(err)
	}
	if _, err := server.SetPlaybackPolicy(ctx, &commodorepb.SetPlaybackPolicyRequest{VodAssetId: vodID, Type: "jwt"}); status.Code(err) != codes.NotFound {
		t.Fatalf("tombstoned VOD update code = %v, err = %v", status.Code(err), err)
	}

	var invalidations int
	if err := db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM commodore.playback_policy_invalidation_outbox
		WHERE tenant_id = $1::uuid AND reason = 'policy_change'
	`, tenantID).Scan(&invalidations); err != nil {
		t.Fatal(err)
	}
	if invalidations != 3 {
		t.Fatalf("policy invalidations = %d, want 3", invalidations)
	}

	// A legacy chapter row can carry a stream_id that disagrees with its durable
	// dvr_chapter_playback identity. The DVR identity is canonical: changing the
	// unrelated stream must not rewrite the chapter.
	if _, err := db.ExecContext(ctx, `UPDATE commodore.vod_assets SET stream_id=$1::uuid WHERE vod_hash=$2`, otherStream, chapterHash); err != nil {
		t.Fatal(err)
	}
	if _, err := server.SetPlaybackPolicy(ctx, &commodorepb.SetPlaybackPolicyRequest{StreamId: otherStream, Type: "public"}); err != nil {
		t.Fatalf("set unrelated stream policy: %v", err)
	}
	if err := db.QueryRowContext(ctx, `SELECT requires_auth, playback_policy->>'type' FROM commodore.vod_assets WHERE vod_hash=$1`, chapterHash).Scan(&chapterRequiresAuth, &chapterPolicyType); err != nil {
		t.Fatal(err)
	}
	if !chapterRequiresAuth || chapterPolicyType != "jwt" {
		t.Fatalf("unrelated stream rewrote DVR-identified chapter: auth=%v policy=%q", chapterRequiresAuth, chapterPolicyType)
	}

	// A durable chapter identity whose DVR no longer exists is different from
	// an existing DVR that simply was not part of this stream update. The former
	// must fail closed instead of borrowing the chapter row's stale stream_id.
	if _, err := db.ExecContext(ctx, `
		UPDATE commodore.dvr_chapter_playback
		SET dvr_hash = 'missingdvr000000000000000000005'
		WHERE tenant_id = $1::uuid AND artifact_hash = $2
	`, tenantID, chapterHash); err != nil {
		t.Fatal(err)
	}
	if _, err := server.SetPlaybackPolicy(ctx, &commodorepb.SetPlaybackPolicyRequest{StreamId: otherStream, Type: "jwt"}); err != nil {
		t.Fatalf("set dangling chapter's stale stream policy: %v", err)
	}
	var danglingPolicyType sql.NullString
	if err := db.QueryRowContext(ctx, `SELECT requires_auth, playback_policy->>'type' FROM commodore.vod_assets WHERE vod_hash=$1`, chapterHash).Scan(&chapterRequiresAuth, &danglingPolicyType); err != nil {
		t.Fatal(err)
	}
	if !chapterRequiresAuth || danglingPolicyType.Valid {
		t.Fatalf("dangling DVR chapter auth=%v policy=%v, want fail-closed true/NULL", chapterRequiresAuth, danglingPolicyType)
	}

	// Restore the durable identity and prove its parent remains the authority
	// even though the legacy stream_id still points elsewhere.
	if _, err := db.ExecContext(ctx, `
		UPDATE commodore.dvr_chapter_playback
		SET dvr_hash = 'dvrhash00000000000000000000001'
		WHERE tenant_id = $1::uuid AND artifact_hash = $2
	`, tenantID, chapterHash); err != nil {
		t.Fatal(err)
	}
	if _, err := server.SetPlaybackPolicy(ctx, &commodorepb.SetPlaybackPolicyRequest{StreamId: streamID, Type: "jwt"}); err != nil {
		t.Fatalf("restore DVR-backed chapter policy: %v", err)
	}

	// Tombstones are a monotonic fence. A later parent policy edit cannot write
	// new authority onto the retained chapter business row.
	if _, err := db.ExecContext(ctx, `
		INSERT INTO commodore.artifact_catalog_tombstones
		    (tenant_id, kind, artifact_hash, origin_cluster_id, deletion_revision)
		VALUES ($1::uuid, 'vod', $2, 'media-1', 2)
	`, tenantID, chapterHash); err != nil {
		t.Fatal(err)
	}
	if _, err := server.SetPlaybackPolicy(ctx, &commodorepb.SetPlaybackPolicyRequest{StreamId: streamID, Type: "public"}); err != nil {
		t.Fatalf("set parent policy after chapter tombstone: %v", err)
	}
	if err := db.QueryRowContext(ctx, `SELECT requires_auth, playback_policy->>'type' FROM commodore.vod_assets WHERE vod_hash=$1`, chapterHash).Scan(&chapterRequiresAuth, &chapterPolicyType); err != nil {
		t.Fatal(err)
	}
	if !chapterRequiresAuth || chapterPolicyType != "jwt" {
		t.Fatalf("parent policy rewrote tombstoned chapter: auth=%v policy=%q", chapterRequiresAuth, chapterPolicyType)
	}

	if _, err := server.GetSignedPolicyBundle(context.Background(), &commodorepb.GetSignedPolicyBundleRequest{
		TenantId: tenantID,
		StreamId: streamID,
	}); status.Code(err) != codes.Unimplemented {
		t.Fatalf("retired policy-bundle RPC code = %v, err = %v", status.Code(err), err)
	}
}
