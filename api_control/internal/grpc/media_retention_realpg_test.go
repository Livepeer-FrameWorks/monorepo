//go:build schema_verify

package grpc

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"frameworks/api_control/internal/database/commodoredb"
)

func TestMediaRetentionRepository_RealPG(t *testing.T) {
	db := startCommodoreRealPG(t)
	ctx := context.Background()
	queries := commodoredb.New(db)
	const (
		tenantID = "10000000-0000-4000-8000-000000000011"
		otherID  = "10000000-0000-4000-8000-000000000012"
		userID   = "20000000-0000-4000-8000-000000000011"
		streamID = "30000000-0000-4000-8000-000000000011"
		clipID   = "40000000-0000-4000-8000-000000000011"
		dvrID    = "50000000-0000-4000-8000-000000000011"
		vodID    = "60000000-0000-4000-8000-000000000011"
		clipHash = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaa11"
		dvrHash  = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbb11"
		vodHash  = "cccccccccccccccccccccccccccccc11"
	)

	if _, err := db.ExecContext(ctx, `
		INSERT INTO commodore.streams
		    (id, tenant_id, user_id, stream_key, playback_id, internal_name, title)
		VALUES ($1::uuid, $2::uuid, $3::uuid, 'retention-key', 'retention-playback', 'live+retention', 'Retention')
	`, streamID, tenantID, userID); err != nil {
		t.Fatalf("seed stream: %v", err)
	}

	if err := queries.UpsertTenantMediaRetentionPolicy(ctx, commodoredb.UpsertTenantMediaRetentionPolicyParams{
		TenantID: tenantID, ApplyDvr: true, DvrDays: sql.NullInt32{Int32: 7, Valid: true}, UpdatedBy: userID,
	}); err != nil {
		t.Fatalf("set DVR policy: %v", err)
	}
	if err := queries.UpsertTenantMediaRetentionPolicy(ctx, commodoredb.UpsertTenantMediaRetentionPolicyParams{
		TenantID: tenantID, ApplyClip: true, ClipDays: sql.NullInt32{Int32: 14, Valid: true}, UpdatedBy: userID,
	}); err != nil {
		t.Fatalf("set clip policy: %v", err)
	}
	policy, err := queries.GetTenantMediaRetentionPolicy(ctx, tenantID)
	if err != nil || !policy.DefaultDvrRetentionDays.Valid || policy.DefaultDvrRetentionDays.Int32 != 7 ||
		!policy.DefaultClipRetentionDays.Valid || policy.DefaultClipRetentionDays.Int32 != 14 || policy.DefaultVodRetentionDays.Valid {
		t.Fatalf("selective policy upserts lost sibling state: row=%#v err=%v", policy, err)
	}
	if err := queries.UpsertTenantMediaRetentionPolicy(ctx, commodoredb.UpsertTenantMediaRetentionPolicyParams{
		TenantID: tenantID, ApplyDvr: true, UpdatedBy: userID,
	}); err != nil {
		t.Fatalf("clear DVR policy: %v", err)
	}
	policy, err = queries.GetTenantMediaRetentionPolicy(ctx, tenantID)
	if err != nil || policy.DefaultDvrRetentionDays.Valid || !policy.DefaultClipRetentionDays.Valid {
		t.Fatalf("DVR clear changed the wrong policy column: row=%#v err=%v", policy, err)
	}

	stream, err := queries.UpdateStreamRetentionOverrides(ctx, commodoredb.UpdateStreamRetentionOverridesParams{
		ApplyDvr: true, DvrDays: sql.NullInt32{Int32: 12, Valid: true}, StreamID: streamID, TenantID: tenantID,
	})
	if err != nil || !stream.DvrRetentionDaysOverride.Valid || stream.DvrRetentionDaysOverride.Int32 != 12 || stream.ClipRetentionDaysOverride.Valid {
		t.Fatalf("set stream override: row=%#v err=%v", stream, err)
	}
	if _, err := queries.GetStreamRetentionOverrides(ctx, commodoredb.GetStreamRetentionOverridesParams{StreamID: streamID, TenantID: otherID}); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("cross-tenant stream read returned %v, want sql.ErrNoRows", err)
	}
	stream, err = queries.UpdateStreamRetentionOverrides(ctx, commodoredb.UpdateStreamRetentionOverridesParams{
		ApplyDvr: true, StreamID: streamID, TenantID: tenantID,
	})
	if err != nil || stream.DvrRetentionDaysOverride.Valid {
		t.Fatalf("clear stream override: row=%#v err=%v", stream, err)
	}

	if _, err := db.ExecContext(ctx, `
		INSERT INTO commodore.clips
		    (id, tenant_id, user_id, stream_id, clip_hash, internal_name, playback_id, start_time, duration, origin_cluster_id)
		VALUES ($1::uuid, $2::uuid, $3::uuid, $4::uuid, $5, 'clip+retention', 'clip-retention', 1, 2, 'media-a');
	`, clipID, tenantID, userID, streamID, clipHash); err != nil {
		t.Fatalf("seed clip: %v", err)
	}
	if _, err := db.ExecContext(ctx, `
		INSERT INTO commodore.dvr_recordings
		    (id, tenant_id, user_id, stream_id, dvr_hash, internal_name, playback_id, stream_internal_name, origin_cluster_id)
		VALUES ($1::uuid, $2::uuid, $3::uuid, $4::uuid, $5, 'dvr+retention', 'dvr-retention', 'live+retention', 'media-a');
	`, dvrID, tenantID, userID, streamID, dvrHash); err != nil {
		t.Fatalf("seed DVR: %v", err)
	}
	if _, err := db.ExecContext(ctx, `
		INSERT INTO commodore.vod_assets
		    (id, tenant_id, user_id, vod_hash, internal_name, playback_id, filename, origin_cluster_id)
		VALUES ($1::uuid, $2::uuid, $3::uuid, $4, 'vod+retention', 'vod-retention', 'retention.mp4', 'media-a')
	`, vodID, tenantID, userID, vodHash); err != nil {
		t.Fatalf("seed VOD: %v", err)
	}

	clip, err := queries.ResolveClipTenantArtifact(ctx, commodoredb.ResolveClipTenantArtifactParams{Identifier: clipID, TenantID: tenantID})
	if err != nil || clip.ClipHash != clipHash || clip.OriginClusterID != "media-a" {
		t.Fatalf("resolve clip: row=%#v err=%v", clip, err)
	}
	dvr, err := queries.ResolveDVRTenantArtifact(ctx, commodoredb.ResolveDVRTenantArtifactParams{Identifier: "DVR-RETENTION", TenantID: tenantID})
	if err != nil || dvr.DvrHash != dvrHash || dvr.OriginClusterID != "media-a" {
		t.Fatalf("resolve DVR by case-insensitive playback ID: row=%#v err=%v", dvr, err)
	}
	vod, err := queries.ResolveVODTenantArtifact(ctx, commodoredb.ResolveVODTenantArtifactParams{Identifier: vodHash, TenantID: tenantID})
	if err != nil || vod.VodHash != vodHash || vod.OriginType != "" {
		t.Fatalf("resolve VOD: row=%#v err=%v", vod, err)
	}

	until := time.Now().UTC().Add(48 * time.Hour).Truncate(time.Microsecond)
	state := sql.NullTime{Time: until, Valid: true}
	if err := queries.ApplyClipRetentionState(ctx, commodoredb.ApplyClipRetentionStateParams{
		OverrideDays: sql.NullInt32{Int32: 2, Valid: true}, OverrideUntil: state,
		RetentionSource: sql.NullString{String: retentionSourceAsset, Valid: true}, RetentionUntil: state,
		ArtifactHash: clipHash, TenantID: tenantID,
	}); err != nil {
		t.Fatalf("apply clip retention: %v", err)
	}
	if err := queries.ApplyDVRRetentionState(ctx, commodoredb.ApplyDVRRetentionStateParams{
		RetentionSource: sql.NullString{String: retentionSourceTier, Valid: true}, RetentionUntil: state,
		ArtifactHash: dvrHash, TenantID: tenantID,
	}); err != nil {
		t.Fatalf("apply DVR reset state: %v", err)
	}
	if err := queries.ApplyVODRetentionState(ctx, commodoredb.ApplyVODRetentionStateParams{
		RetentionSource: sql.NullString{String: retentionSourceAsset, Valid: true},
		ArtifactHash:    vodHash, TenantID: tenantID,
	}); err != nil {
		t.Fatalf("apply VOD keep-forever state: %v", err)
	}

	clipUntil, err := queries.GetClipRetentionUntil(ctx, commodoredb.GetClipRetentionUntilParams{ArtifactHash: clipHash, TenantID: tenantID})
	if err != nil || !clipUntil.Valid || !clipUntil.Time.Equal(until) {
		t.Fatalf("clip retention readback=%v err=%v, want %v", clipUntil, err, until)
	}
	dvrUntil, err := queries.GetDVRRetentionUntil(ctx, commodoredb.GetDVRRetentionUntilParams{ArtifactHash: dvrHash, TenantID: tenantID})
	if err != nil || !dvrUntil.Valid || !dvrUntil.Time.Equal(until) {
		t.Fatalf("DVR retention readback=%v err=%v, want %v", dvrUntil, err, until)
	}
	vodUntil, err := queries.GetVODRetentionUntil(ctx, commodoredb.GetVODRetentionUntilParams{ArtifactHash: vodHash, TenantID: tenantID})
	if err != nil || vodUntil.Valid {
		t.Fatalf("VOD keep-forever readback=%v err=%v, want NULL", vodUntil, err)
	}

	var overrideDays sql.NullInt32
	var overrideUntil sql.NullTime
	if err := db.QueryRowContext(ctx, `
		SELECT retention_override_days, retention_override_until
		FROM commodore.dvr_recordings
		WHERE tenant_id = $1::uuid AND dvr_hash = $2
	`, tenantID, dvrHash).Scan(&overrideDays, &overrideUntil); err != nil {
		t.Fatal(err)
	}
	if overrideDays.Valid || overrideUntil.Valid {
		t.Fatalf("reset state must clear both override columns: days=%v until=%v", overrideDays, overrideUntil)
	}
}
