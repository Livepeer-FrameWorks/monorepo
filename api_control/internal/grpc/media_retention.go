package grpc

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	foghornclient "github.com/Livepeer-FrameWorks/monorepo/pkg/clients/foghorn"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	commodorepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"
	foghornpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/foghorn"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// Customer-tunable media retention. Persistence:
//   - commodore.tenant_media_retention_policies — one row per tenant, one
//     column per asset class (default_vod/dvr/clip_retention_days).
//   - commodore.streams.{dvr,clip}_retention_days_override — per-stream.
//   - commodore.{dvr_recordings,clips,vod_assets}.{retention_override_days,
//     retention_override_until,retention_source} — per-asset.
//
// Resolution cascade at artifact create / DVR start:
//   per-asset override → per-stream override (DVR/clip only) →
//     per-class tenant default → system default (VOD: keep forever,
//     DVR/clip: 30d) → tier cap (0 = no cap, paid; Free has a finite cap).
//
// The resolved value is snapshotted onto the artifact at create time so the
// horizon is stable even if the tenant's plan changes later. UpdateAsset /
// ResetAsset rewrite an existing artifact's snapshot.

const (
	// safeFallbackRetentionDays is the Free-equivalent horizon used only
	// when the Purser tier-cap lookup fails — never the system default for
	// new artifacts (those come from systemRetentionDefault).
	safeFallbackRetentionDays = 30

	retentionSourceTenant = "tenant_default"
	retentionSourceStream = "per_stream_override"
	retentionSourceAsset  = "per_asset_override"
	retentionSourceTier   = "tier_entitlement"

	eventRetentionPolicyChanged   = "media.retention_policy.changed"
	eventRetentionOverrideApplied = "media.retention.override_applied"
	eventRetentionOverrideReset   = "media.retention.override_reset"
)

// fetchEntitlementBound returns the tenant's tier cap from Purser. 0 means
// "no cap" (paid-tier baseline). Falls back to safeFallbackRetentionDays when
// Purser is unreachable so we don't accidentally retain forever on an
// unknown plan.
func (s *CommodoreServer) fetchEntitlementBound(ctx context.Context, tenantID string) (int32, error) {
	if s.purserClient == nil {
		return safeFallbackRetentionDays, nil
	}
	bs, err := s.purserClient.GetTenantBillingStatus(ctx, tenantID)
	if err != nil {
		return 0, err
	}
	if bs == nil {
		return safeFallbackRetentionDays, nil
	}
	return bs.GetRecordingRetentionDays(), nil
}

// systemRetentionDefault returns the per-class system default. 0 = keep
// forever (paid-tier VOD baseline); 30 = DVR/clip (live recordings are
// ephemeral by nature). Tier cap clamps this at resolve time on Free.
func systemRetentionDefault(target commodorepb.MediaRetentionTarget) int32 {
	switch target {
	case commodorepb.MediaRetentionTarget_MEDIA_RETENTION_TARGET_VOD:
		return 0
	case commodorepb.MediaRetentionTarget_MEDIA_RETENTION_TARGET_DVR,
		commodorepb.MediaRetentionTarget_MEDIA_RETENTION_TARGET_CLIP:
		return 30
	}
	return 30
}

// perClassColumn maps a retention target to the tenant-policy column. ""
// for UNSPECIFIED.
func perClassColumn(target commodorepb.MediaRetentionTarget) string {
	switch target {
	case commodorepb.MediaRetentionTarget_MEDIA_RETENTION_TARGET_VOD:
		return "default_vod_retention_days"
	case commodorepb.MediaRetentionTarget_MEDIA_RETENTION_TARGET_DVR:
		return "default_dvr_retention_days"
	case commodorepb.MediaRetentionTarget_MEDIA_RETENTION_TARGET_CLIP:
		return "default_clip_retention_days"
	}
	return ""
}

// streamOverrideColumn maps a retention target to the streams column for
// per-stream overrides. "" for VOD (uploads aren't stream-bound).
func streamOverrideColumn(target commodorepb.MediaRetentionTarget) string {
	switch target {
	case commodorepb.MediaRetentionTarget_MEDIA_RETENTION_TARGET_DVR:
		return "dvr_retention_days_override"
	case commodorepb.MediaRetentionTarget_MEDIA_RETENTION_TARGET_CLIP:
		return "clip_retention_days_override"
	}
	return ""
}

// readTenantPerClassDefault returns the tenant's default for a specific
// asset class. (0, false, nil) when the column is NULL or no policy row.
func (s *CommodoreServer) readTenantPerClassDefault(ctx context.Context, tenantID string, target commodorepb.MediaRetentionTarget) (days int32, set bool, err error) {
	classCol := perClassColumn(target)
	if classCol == "" {
		return 0, false, nil
	}
	// classCol is one of three trusted constants — safe to interpolate.
	query := fmt.Sprintf(`
		SELECT %s
		  FROM commodore.tenant_media_retention_policies
		 WHERE tenant_id = $1::uuid
	`, classCol)
	var v sql.NullInt32
	scanErr := s.db.QueryRowContext(ctx, query, tenantID).Scan(&v)
	if errors.Is(scanErr, sql.ErrNoRows) {
		return 0, false, nil
	}
	if scanErr != nil {
		return 0, false, scanErr
	}
	if !v.Valid {
		return 0, false, nil
	}
	return v.Int32, true, nil
}

// readStreamRetentionOverride returns the per-stream override for DVR or
// clip retention. (0, false, nil) for streams without an override, for VOD,
// when streamID is empty, or when tenantID is empty.
//
// tenantID is required — the lookup is tenant-scoped (repo invariant:
// every DB query filters by tenant_id).
func (s *CommodoreServer) readStreamRetentionOverride(ctx context.Context, tenantID, streamID string, target commodorepb.MediaRetentionTarget) (days int32, set bool, err error) {
	if streamID == "" || tenantID == "" {
		return 0, false, nil
	}
	col := streamOverrideColumn(target)
	if col == "" {
		return 0, false, nil
	}
	// col is one of two trusted constants — safe to interpolate.
	query := fmt.Sprintf(`SELECT %s FROM commodore.streams WHERE id = $1::uuid AND tenant_id = $2::uuid`, col)
	var v sql.NullInt32
	scanErr := s.db.QueryRowContext(ctx, query, streamID, tenantID).Scan(&v)
	if errors.Is(scanErr, sql.ErrNoRows) {
		return 0, false, nil
	}
	if scanErr != nil {
		return 0, false, scanErr
	}
	if !v.Valid {
		return 0, false, nil
	}
	return v.Int32, true, nil
}

// resolveInitialRetention computes the retention days for a brand-new
// artifact of the given class. Cascade:
//
//  1. per-stream override (DVR/clip only).
//  2. per-class tenant default.
//  3. per-class system default (VOD: 0/forever, DVR/clip: 30).
//
// The tier cap (Purser.recording_retention_days; 0 = no cap) clamps the
// resolved value. Returns int32 days; 0 means "keep forever" (artifact
// retention_until written as NULL).
func (s *CommodoreServer) resolveInitialRetention(ctx context.Context, target commodorepb.MediaRetentionTarget, tenantID, streamID string) (int32, error) {
	days, set, err := s.readStreamRetentionOverride(ctx, tenantID, streamID, target)
	if err != nil {
		return 0, fmt.Errorf("stream override: %w", err)
	}
	if !set {
		days, set, err = s.readTenantPerClassDefault(ctx, tenantID, target)
		if err != nil {
			return 0, fmt.Errorf("tenant per-class default: %w", err)
		}
	}
	if !set {
		days = systemRetentionDefault(target)
	}

	cap, capErr := s.fetchEntitlementBound(ctx, tenantID)
	if capErr != nil {
		// Tier lookup failed: fall back to a 30-day clamp so we don't
		// unintentionally retain forever on an unknown plan.
		cap = safeFallbackRetentionDays
	}
	if cap > 0 {
		// 0 = forever must be clamped to the cap; anything above the cap too.
		if days <= 0 || days > cap {
			days = cap
		}
	}
	return days, nil
}

// resolveTenantPerClassEffective returns the effective retention the
// cascade would yield for a hypothetical new artifact of `target`, given
// only tenant-level inputs (no per-stream, no per-asset). Used by
// GetMediaRetentionPolicy.
func resolveTenantPerClassEffective(target commodorepb.MediaRetentionTarget, days int32, set bool, cap int32) int32 {
	if !set {
		days = systemRetentionDefault(target)
	}
	if cap > 0 {
		if days <= 0 || days > cap {
			days = cap
		}
	}
	return days
}

func daysUntil(t time.Time) int32 {
	days := int32((time.Until(t) + 24*time.Hour - 1) / (24 * time.Hour))
	if days < 0 {
		return 0
	}
	return days
}

func (s *CommodoreServer) restoreFoghornRetention(ctx context.Context, client *foghornclient.GRPCClient, tenantID string, target *assetRetentionTarget, previous sql.NullTime) {
	if !previous.Valid {
		s.logger.WithFields(logging.Fields{
			"tenant_id":     tenantID,
			"artifact_hash": target.hash,
			"artifact_type": target.artifactType,
		}).Error("Cannot roll back Foghorn retention after Commodore write failure: previous retention_until was NULL")
		return
	}
	if _, _, err := client.OverrideArtifactRetention(ctx, &foghornpb.OverrideArtifactRetentionRequest{
		TenantId:       tenantID,
		DvrHash:        target.hash,
		ArtifactType:   target.artifactType,
		RetentionUntil: timestamppb.New(previous.Time),
	}); err != nil {
		s.logger.WithError(err).WithFields(logging.Fields{
			"tenant_id":     tenantID,
			"artifact_hash": target.hash,
			"artifact_type": target.artifactType,
		}).Error("Failed to roll back Foghorn retention after Commodore write failure")
	}
}

func foghornRetentionError(err error, operation string) error {
	if st, ok := status.FromError(err); ok {
		switch st.Code() {
		case codes.InvalidArgument, codes.NotFound, codes.PermissionDenied:
			return err
		case codes.FailedPrecondition:
			return status.Error(codes.InvalidArgument, st.Message())
		}
	}
	return status.Errorf(codes.Internal, "%s failed: %v", operation, err)
}

// GetMediaRetentionPolicy returns the tenant's per-class defaults and the
// effective horizons the cascade resolves to today for a hypothetical new
// artifact of each class (without per-stream context).
func (s *CommodoreServer) GetMediaRetentionPolicy(ctx context.Context, req *commodorepb.GetMediaRetentionPolicyRequest) (*commodorepb.GetMediaRetentionPolicyResponse, error) {
	_, tenantID, err := extractUserContext(ctx)
	if err != nil {
		return nil, err
	}
	if t := req.GetTenantId(); t != "" && t != tenantID {
		return nil, status.Error(codes.PermissionDenied, "tenant mismatch")
	}

	bound, bErr := s.fetchEntitlementBound(ctx, tenantID)
	if bErr != nil {
		s.logger.WithError(bErr).Warn("Purser bound lookup failed; serving with fallback cap")
		bound = safeFallbackRetentionDays
	}

	var (
		vodDays   sql.NullInt32
		dvrDays   sql.NullInt32
		clipDays  sql.NullInt32
		updatedBy string
		updatedAt time.Time
	)
	scanErr := s.db.QueryRowContext(ctx, `
		SELECT default_vod_retention_days,
		       default_dvr_retention_days,
		       default_clip_retention_days,
		       COALESCE(updated_by::text, ''),
		       updated_at
		  FROM commodore.tenant_media_retention_policies
		 WHERE tenant_id = $1::uuid
	`, tenantID).Scan(&vodDays, &dvrDays, &clipDays, &updatedBy, &updatedAt)
	if scanErr != nil && !errors.Is(scanErr, sql.ErrNoRows) {
		return nil, status.Errorf(codes.Internal, "policy lookup failed: %v", scanErr)
	}

	resp := &commodorepb.GetMediaRetentionPolicyResponse{
		Bounds: &commodorepb.MediaRetentionBounds{MaxRecordingRetentionDays: bound},
		EffectiveVodRetentionDays: resolveTenantPerClassEffective(
			commodorepb.MediaRetentionTarget_MEDIA_RETENTION_TARGET_VOD, vodDays.Int32, vodDays.Valid, bound),
		EffectiveDvrRetentionDays: resolveTenantPerClassEffective(
			commodorepb.MediaRetentionTarget_MEDIA_RETENTION_TARGET_DVR, dvrDays.Int32, dvrDays.Valid, bound),
		EffectiveClipRetentionDays: resolveTenantPerClassEffective(
			commodorepb.MediaRetentionTarget_MEDIA_RETENTION_TARGET_CLIP, clipDays.Int32, clipDays.Valid, bound),
		UpdatedBy: updatedBy,
	}
	if vodDays.Valid {
		v := vodDays.Int32
		resp.DefaultVodRetentionDays = &v
	}
	if dvrDays.Valid {
		v := dvrDays.Int32
		resp.DefaultDvrRetentionDays = &v
	}
	if clipDays.Valid {
		v := clipDays.Int32
		resp.DefaultClipRetentionDays = &v
	}
	if !updatedAt.IsZero() {
		resp.UpdatedAt = timestamppb.New(updatedAt)
	}
	return resp, nil
}

// SetMediaRetentionPolicy writes a tenant per-class default. target_type
// must be VOD, DVR, or CLIP. clear=true sets the column to NULL (tenant
// falls back to the per-class system default). Free-tier writes of 0
// ("keep forever") are clamped up to the tier cap; paid-tier writes accept
// any non-negative value.
func (s *CommodoreServer) SetMediaRetentionPolicy(ctx context.Context, req *commodorepb.SetMediaRetentionPolicyRequest) (*commodorepb.SetMediaRetentionPolicyResponse, error) {
	userID, tenantID, err := extractUserContext(ctx)
	if err != nil {
		return nil, err
	}
	if t := req.GetTenantId(); t != "" && t != tenantID {
		return nil, status.Error(codes.PermissionDenied, "tenant mismatch")
	}

	target := req.GetTargetType()
	column := perClassColumn(target)
	if column == "" {
		return nil, status.Error(codes.InvalidArgument, "target_type must be VOD, DVR, or CLIP")
	}

	updatedBy := req.GetUpdatedBy()
	if updatedBy == "" {
		updatedBy = userID
	}

	clear := req.GetClear()
	days := req.GetDays()

	if !clear {
		if days < 0 {
			return nil, status.Error(codes.InvalidArgument, "days must be >= 0 (0 = no auto-expire)")
		}
		bound, bErr := s.fetchEntitlementBound(ctx, tenantID)
		if bErr != nil {
			return nil, status.Errorf(codes.Internal, "entitlement lookup failed: %v", bErr)
		}
		if bound > 0 && days > bound {
			return nil, status.Errorf(codes.InvalidArgument,
				"days=%d exceeds tier cap %d; upgrade your plan to extend retention", days, bound)
		}
		// Free-tier writes of 0 (forever) get clamped to the cap; paid
		// tiers without a cap accept 0 as-is.
		if bound > 0 && days == 0 {
			days = bound
		}
	}

	// column is a trusted internal constant — safe to interpolate.
	var execErr error
	if clear {
		_, execErr = s.db.ExecContext(ctx, fmt.Sprintf(`
			INSERT INTO commodore.tenant_media_retention_policies
			            (tenant_id, %s, updated_by, created_at, updated_at)
			VALUES      ($1::uuid, NULL, NULLIF($2, '')::uuid, NOW(), NOW())
			ON CONFLICT (tenant_id) DO UPDATE
			   SET %s         = NULL,
			       updated_by = EXCLUDED.updated_by,
			       updated_at = NOW()
		`, column, column), tenantID, updatedBy)
	} else {
		// Upsert touches only the chosen column so other classes' overrides survive.
		_, execErr = s.db.ExecContext(ctx, fmt.Sprintf(`
			INSERT INTO commodore.tenant_media_retention_policies
			            (tenant_id, %s, updated_by, created_at, updated_at)
			VALUES      ($1::uuid, $2, NULLIF($3, '')::uuid, NOW(), NOW())
			ON CONFLICT (tenant_id) DO UPDATE
			   SET %s = EXCLUDED.%s,
			       updated_by = EXCLUDED.updated_by,
			       updated_at = NOW()
		`, column, column, column), tenantID, days, updatedBy)
	}
	if execErr != nil {
		return nil, status.Errorf(codes.Internal, "policy write failed: %v", execErr)
	}

	s.logger.WithFields(logging.Fields{
		"tenant_id":  tenantID,
		"target":     target.String(),
		"column":     column,
		"days":       days,
		"clear":      clear,
		"updated_by": updatedBy,
	}).Info("Media retention policy updated")
	s.emitRetentionPolicyEvent(ctx, tenantID, userID, column)

	policy, err := s.GetMediaRetentionPolicy(ctx, &commodorepb.GetMediaRetentionPolicyRequest{TenantId: tenantID})
	if err != nil {
		return nil, err
	}
	return &commodorepb.SetMediaRetentionPolicyResponse{Policy: policy}, nil
}

// SetStreamRetentionOverrides writes per-stream DVR/clip retention
// overrides. Each pair (override, clear_*_override) governs one column:
// clear takes precedence; otherwise the optional override value writes;
// otherwise the column is left alone.
func (s *CommodoreServer) SetStreamRetentionOverrides(ctx context.Context, req *commodorepb.SetStreamRetentionOverridesRequest) (*commodorepb.SetStreamRetentionOverridesResponse, error) {
	_, tenantID, err := extractUserContext(ctx)
	if err != nil {
		return nil, err
	}
	if t := req.GetTenantId(); t != "" && t != tenantID {
		return nil, status.Error(codes.PermissionDenied, "tenant mismatch")
	}
	streamID := req.GetStreamId()
	if streamID == "" {
		return nil, status.Error(codes.InvalidArgument, "stream_id required")
	}

	// Verify tenant ownership before any update lands.
	var ownedStream string
	if scanErr := s.db.QueryRowContext(ctx,
		`SELECT id::text FROM commodore.streams WHERE id = $1::uuid AND tenant_id = $2::uuid`,
		streamID, tenantID,
	).Scan(&ownedStream); scanErr != nil {
		if errors.Is(scanErr, sql.ErrNoRows) {
			return nil, status.Error(codes.NotFound, "stream not found")
		}
		return nil, status.Errorf(codes.Internal, "stream lookup failed: %v", scanErr)
	}

	bound, bErr := s.fetchEntitlementBound(ctx, tenantID)
	if bErr != nil {
		return nil, status.Errorf(codes.Internal, "entitlement lookup failed: %v", bErr)
	}

	type assignment struct {
		column string
		value  sql.NullInt32 // Valid=false → set NULL
	}
	var assignments []assignment
	add := func(column string, override *int32, clear bool) error {
		if clear {
			assignments = append(assignments, assignment{column: column})
			return nil
		}
		if override == nil {
			return nil
		}
		v := *override
		if v < 0 {
			return status.Errorf(codes.InvalidArgument, "%s must be >= 0 (0 = no auto-expire)", column)
		}
		if bound > 0 {
			if v == 0 || v > bound {
				v = bound
			}
		}
		assignments = append(assignments, assignment{column: column, value: sql.NullInt32{Valid: true, Int32: v}})
		return nil
	}
	if addErr := add("dvr_retention_days_override", req.DvrRetentionDaysOverride, req.GetClearDvrRetentionOverride()); addErr != nil {
		return nil, addErr
	}
	if addErr := add("clip_retention_days_override", req.ClipRetentionDaysOverride, req.GetClearClipRetentionOverride()); addErr != nil {
		return nil, addErr
	}
	if len(assignments) == 0 {
		return nil, status.Error(codes.InvalidArgument, "no override fields set")
	}

	args := []any{streamID, tenantID}
	sets := "updated_at = NOW()"
	for _, a := range assignments {
		if !a.value.Valid {
			sets += ", " + a.column + " = NULL"
			continue
		}
		args = append(args, a.value.Int32)
		sets += fmt.Sprintf(", %s = $%d", a.column, len(args))
	}
	if _, execErr := s.db.ExecContext(ctx,
		"UPDATE commodore.streams SET "+sets+" WHERE id = $1::uuid AND tenant_id = $2::uuid",
		args...,
	); execErr != nil {
		return nil, status.Errorf(codes.Internal, "stream override write failed: %v", execErr)
	}

	// Read back the resolved state so the caller sees persisted post-clamp values.
	var dvrCol, clipCol sql.NullInt32
	if scanErr := s.db.QueryRowContext(ctx,
		`SELECT dvr_retention_days_override, clip_retention_days_override
		   FROM commodore.streams
		  WHERE id = $1::uuid`,
		streamID,
	).Scan(&dvrCol, &clipCol); scanErr != nil {
		return nil, status.Errorf(codes.Internal, "stream override readback failed: %v", scanErr)
	}
	resp := &commodorepb.SetStreamRetentionOverridesResponse{StreamId: streamID}
	if dvrCol.Valid {
		v := dvrCol.Int32
		resp.DvrRetentionDaysOverride = &v
	}
	if clipCol.Valid {
		v := clipCol.Int32
		resp.ClipRetentionDaysOverride = &v
	}
	return resp, nil
}

// assetRetentionTarget bundles the resolution result for a per-asset
// retention operation: which Commodore table to update, which artifact_type
// Foghorn should match against, the canonical hash to key on, and the
// originating cluster (for routing the Foghorn override RPC).
type assetRetentionTarget struct {
	table           string
	artifactType    string
	hash            string
	originClusterID string
	mediaTarget     commodorepb.MediaRetentionTarget
}

// resolveAssetTarget validates tenant ownership of the asset and returns
// the routing context Foghorn needs.
func (s *CommodoreServer) resolveAssetTarget(ctx context.Context, targetType commodorepb.MediaRetentionTarget, targetID, tenantID string) (*assetRetentionTarget, error) {
	switch targetType {
	case commodorepb.MediaRetentionTarget_MEDIA_RETENTION_TARGET_DVR:
		originClusterID, dvrHash, err := s.assertDVRTenant(ctx, targetID, tenantID)
		if err != nil {
			return nil, err
		}
		return &assetRetentionTarget{
			table:           "commodore.dvr_recordings",
			artifactType:    "dvr",
			hash:            dvrHash,
			originClusterID: originClusterID,
			mediaTarget:     targetType,
		}, nil
	case commodorepb.MediaRetentionTarget_MEDIA_RETENTION_TARGET_CLIP:
		originClusterID, clipHash, err := s.assertClipTenant(ctx, targetID, tenantID)
		if err != nil {
			return nil, err
		}
		return &assetRetentionTarget{
			table:           "commodore.clips",
			artifactType:    "clip",
			hash:            clipHash,
			originClusterID: originClusterID,
			mediaTarget:     targetType,
		}, nil
	case commodorepb.MediaRetentionTarget_MEDIA_RETENTION_TARGET_VOD:
		originClusterID, vodHash, err := s.assertVodTenant(ctx, targetID, tenantID)
		if err != nil {
			return nil, err
		}
		return &assetRetentionTarget{
			table:           "commodore.vod_assets",
			artifactType:    "vod",
			hash:            vodHash,
			originClusterID: originClusterID,
			mediaTarget:     targetType,
		}, nil
	default:
		return nil, status.Error(codes.InvalidArgument, "target_type must be DVR, CLIP, or VOD")
	}
}

// streamIDForAsset returns the source stream_id for an asset, or "" for
// VOD (uploads aren't stream-bound) and orphan rows. Used by reset to feed
// the per-stream override into the cascade.
func (s *CommodoreServer) streamIDForAsset(ctx context.Context, target *assetRetentionTarget, tenantID string) (string, error) {
	switch target.artifactType {
	case "dvr":
		var streamID sql.NullString
		err := s.db.QueryRowContext(ctx, `
			SELECT stream_id::text
			  FROM commodore.dvr_recordings
			 WHERE dvr_hash = $1
			   AND tenant_id = $2::uuid
		`, target.hash, tenantID).Scan(&streamID)
		if errors.Is(err, sql.ErrNoRows) || err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return "", nil
			}
			return "", err
		}
		if !streamID.Valid {
			return "", nil
		}
		return streamID.String, nil
	case "clip":
		var streamID sql.NullString
		err := s.db.QueryRowContext(ctx, `
			SELECT stream_id::text
			  FROM commodore.clips
			 WHERE clip_hash = $1
			   AND tenant_id = $2::uuid
		`, target.hash, tenantID).Scan(&streamID)
		if errors.Is(err, sql.ErrNoRows) || err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return "", nil
			}
			return "", err
		}
		if !streamID.Valid {
			return "", nil
		}
		return streamID.String, nil
	}
	return "", nil
}

func (s *CommodoreServer) assertClipTenant(ctx context.Context, identifier, tenantID string) (originClusterID, clipHash string, err error) {
	if tenantID == "" {
		return "", "", status.Error(codes.PermissionDenied, "tenant_id required")
	}
	if identifier == "" {
		return "", "", status.Error(codes.InvalidArgument, "clip id is required")
	}
	if scanErr := s.db.QueryRowContext(ctx,
		`SELECT COALESCE(origin_cluster_id, ''), clip_hash
		   FROM commodore.clips
		  WHERE tenant_id = $2::uuid
		    AND (clip_hash = $1 OR id::text = $1)`,
		identifier, tenantID,
	).Scan(&originClusterID, &clipHash); scanErr != nil {
		if errors.Is(scanErr, sql.ErrNoRows) {
			return "", "", status.Error(codes.NotFound, "clip not found")
		}
		return "", "", status.Errorf(codes.Internal, "tenant lookup failed: %v", scanErr)
	}
	if originClusterID == "" {
		return "", "", status.Error(codes.FailedPrecondition, "clip origin cluster is missing")
	}
	return originClusterID, clipHash, nil
}

func (s *CommodoreServer) assertVodTenant(ctx context.Context, identifier, tenantID string) (originClusterID, vodHash string, err error) {
	if tenantID == "" {
		return "", "", status.Error(codes.PermissionDenied, "tenant_id required")
	}
	if identifier == "" {
		return "", "", status.Error(codes.InvalidArgument, "vod asset id is required")
	}
	if scanErr := s.db.QueryRowContext(ctx,
		`SELECT COALESCE(origin_cluster_id, ''), vod_hash
		   FROM commodore.vod_assets
		  WHERE tenant_id = $2::uuid
		    AND (vod_hash = $1 OR id::text = $1)`,
		identifier, tenantID,
	).Scan(&originClusterID, &vodHash); scanErr != nil {
		if errors.Is(scanErr, sql.ErrNoRows) {
			return "", "", status.Error(codes.NotFound, "vod asset not found")
		}
		return "", "", status.Errorf(codes.Internal, "tenant lookup failed: %v", scanErr)
	}
	if originClusterID == "" {
		return "", "", status.Error(codes.FailedPrecondition, "vod asset origin cluster is missing")
	}
	return originClusterID, vodHash, nil
}

// hashColumn returns the SQL column name in the asset table that holds the
// canonical hash. Trusted internal switch.
func (t *assetRetentionTarget) hashColumn() string {
	switch t.artifactType {
	case "dvr":
		return "dvr_hash"
	case "clip":
		return "clip_hash"
	case "vod":
		return "vod_hash"
	}
	return ""
}

func (t *assetRetentionTarget) readRetentionUntil(ctx context.Context, db *sql.DB, tenantID string) (sql.NullTime, error) {
	var retentionUntil sql.NullTime
	query := fmt.Sprintf(`
		SELECT retention_until
		  FROM %s
		 WHERE %s = $1
		   AND tenant_id = $2::uuid
	`, t.table, t.hashColumn())
	err := db.QueryRowContext(ctx, query, t.hash, tenantID).Scan(&retentionUntil)
	return retentionUntil, err
}

// UpdateAssetRetention writes a per-asset retention override and pushes
// the new horizon to Foghorn. retention_days = 0 (set explicitly via the
// optional field) means "keep forever" — Commodore writes NULL retention_until
// and Foghorn's RetentionJob skips the artifact.
func (s *CommodoreServer) UpdateAssetRetention(ctx context.Context, req *commodorepb.UpdateAssetRetentionRequest) (*commodorepb.UpdateAssetRetentionResponse, error) {
	userID, tenantID, err := extractUserContext(ctx)
	if err != nil {
		return nil, err
	}
	if t := req.GetTenantId(); t != "" && t != tenantID {
		return nil, status.Error(codes.PermissionDenied, "tenant mismatch")
	}

	target, err := s.resolveAssetTarget(ctx, req.GetTargetType(), req.GetTargetId(), tenantID)
	if err != nil {
		return nil, err
	}

	bound, bErr := s.fetchEntitlementBound(ctx, tenantID)
	if bErr != nil {
		return nil, status.Errorf(codes.Internal, "entitlement lookup failed: %v", bErr)
	}

	var (
		retentionUntil  time.Time
		retentionDays   int32
		keepForever     bool
		hasUntil        = req.RetentionUntil != nil
		hasDaysExplicit = req.RetentionDays != nil
	)
	if hasUntil && hasDaysExplicit {
		return nil, status.Error(codes.InvalidArgument, "retention_days and retention_until are mutually exclusive")
	}
	switch {
	case hasUntil:
		retentionUntil = req.GetRetentionUntil().AsTime()
		if retentionUntil.Before(time.Now()) {
			return nil, status.Error(codes.InvalidArgument, "retention_until must be in the future")
		}
		retentionDays = daysUntil(retentionUntil)
	case hasDaysExplicit:
		retentionDays = req.GetRetentionDays()
		if retentionDays < 0 {
			return nil, status.Error(codes.InvalidArgument, "retention_days must be >= 0 (0 = keep forever)")
		}
		if retentionDays == 0 {
			keepForever = true
		} else {
			retentionUntil = time.Now().Add(time.Duration(retentionDays) * 24 * time.Hour)
		}
	default:
		return nil, status.Error(codes.InvalidArgument, "either retention_days or retention_until must be set")
	}

	// Free tier cap clamps both finite and "keep forever" overrides.
	if bound > 0 {
		if keepForever || retentionDays > bound {
			retentionDays = bound
			retentionUntil = time.Now().Add(time.Duration(bound) * 24 * time.Hour)
			keepForever = false
		}
	}

	previousUntil, prevErr := target.readRetentionUntil(ctx, s.db, tenantID)
	if prevErr != nil {
		return nil, status.Errorf(codes.Internal, "retention state lookup failed: %v", prevErr)
	}
	foghornClient, err := s.resolveFoghornForArtifact(ctx, tenantID, target.originClusterID)
	if err != nil {
		return nil, err
	}

	overrideReq := &foghornpb.OverrideArtifactRetentionRequest{
		TenantId:         tenantID,
		DvrHash:          target.hash,
		ArtifactType:     target.artifactType,
		MaxRetentionDays: bound,
	}
	if keepForever {
		// retention_days = 0 anchored to ended_at signals Foghorn to clear
		// retention_until (keep forever, RetentionJob skips the artifact).
		overrideReq.RetentionDays = 0
		overrideReq.AnchorToEndedAt = true
	} else {
		overrideReq.RetentionUntil = timestamppb.New(retentionUntil)
	}
	resp, _, err := foghornClient.OverrideArtifactRetention(ctx, overrideReq)
	if err != nil {
		s.logger.WithError(err).WithFields(logging.Fields{
			"tenant_id":     tenantID,
			"artifact_hash": target.hash,
			"artifact_type": target.artifactType,
		}).Error("Foghorn.OverrideArtifactRetention failed")
		return nil, foghornRetentionError(err, "foghorn override")
	}
	if !keepForever && resp.GetRetentionUntil() != nil {
		retentionUntil = resp.GetRetentionUntil().AsTime()
		retentionDays = daysUntil(retentionUntil)
	}

	// Table name + hash column come from the resolved target — both are
	// internal trusted strings (not user input), so direct interpolation
	// into the SQL is safe and keeps the parameter binding clean.
	var execErr error
	if keepForever {
		updateSQL := fmt.Sprintf(`
			UPDATE %s
			   SET retention_override_days  = NULL,
			       retention_override_until = NULL,
			       retention_source         = $1,
			       retention_until          = NULL,
			       updated_at               = NOW()
			 WHERE %s     = $2
			   AND tenant_id = $3::uuid
		`, target.table, target.hashColumn())
		_, execErr = s.db.ExecContext(ctx, updateSQL, retentionSourceAsset, target.hash, tenantID)
	} else {
		updateSQL := fmt.Sprintf(`
			UPDATE %s
			   SET retention_override_days  = $1,
			       retention_override_until = $2,
			       retention_source         = $3,
			       retention_until          = $2,
			       updated_at               = NOW()
			 WHERE %s     = $4
			   AND tenant_id = $5::uuid
		`, target.table, target.hashColumn())
		_, execErr = s.db.ExecContext(ctx, updateSQL,
			retentionDays, retentionUntil, retentionSourceAsset, target.hash, tenantID,
		)
	}
	if execErr != nil {
		s.restoreFoghornRetention(ctx, foghornClient, tenantID, target, previousUntil)
		return nil, status.Errorf(codes.Internal, "override write failed: %v", execErr)
	}

	s.logger.WithFields(logging.Fields{
		"tenant_id":      tenantID,
		"artifact_hash":  target.hash,
		"artifact_type":  target.artifactType,
		"retention_days": retentionDays,
		"keep_forever":   keepForever,
	}).Info("Per-asset retention override applied")
	s.emitRetentionArtifactEvent(ctx, eventRetentionOverrideApplied, tenantID, userID, target, retentionUntil, keepForever)

	out := &commodorepb.UpdateAssetRetentionResponse{
		TargetId:      target.hash,
		RetentionDays: retentionDays,
		Source:        retentionSourceAsset,
	}
	if !keepForever {
		out.RetentionUntil = timestamppb.New(retentionUntil)
	}
	return out, nil
}

// ResetAssetRetention clears any per-asset override and recomputes the
// horizon from the per-class cascade for the artifact's source stream.
// VOD uses the tenant per-class default (no stream context).
func (s *CommodoreServer) ResetAssetRetention(ctx context.Context, req *commodorepb.ResetAssetRetentionRequest) (*commodorepb.UpdateAssetRetentionResponse, error) {
	userID, tenantID, err := extractUserContext(ctx)
	if err != nil {
		return nil, err
	}
	if t := req.GetTenantId(); t != "" && t != tenantID {
		return nil, status.Error(codes.PermissionDenied, "tenant mismatch")
	}

	target, err := s.resolveAssetTarget(ctx, req.GetTargetType(), req.GetTargetId(), tenantID)
	if err != nil {
		return nil, err
	}

	streamID, sidErr := s.streamIDForAsset(ctx, target, tenantID)
	if sidErr != nil {
		return nil, status.Errorf(codes.Internal, "asset stream lookup failed: %v", sidErr)
	}

	// Determine which cascade layer wins so the retention_source column on
	// the asset row records where the new horizon came from. The cascade
	// itself is computed by resolveInitialRetention below; this mirror is
	// just bookkeeping. Order matches the cascade: per-stream override
	// > per-class tenant default > tier entitlement (clamp).
	source := retentionSourceTier
	if _, ok, streamErr := s.readStreamRetentionOverride(ctx, tenantID, streamID, target.mediaTarget); streamErr == nil && ok {
		source = retentionSourceStream
	} else if _, ok, classErr := s.readTenantPerClassDefault(ctx, tenantID, target.mediaTarget); classErr == nil && ok {
		source = retentionSourceTenant
	}

	resolvedDays, resolveErr := s.resolveInitialRetention(ctx, target.mediaTarget, tenantID, streamID)
	if resolveErr != nil {
		return nil, status.Errorf(codes.Internal, "retention resolve failed: %v", resolveErr)
	}

	bound, bErr := s.fetchEntitlementBound(ctx, tenantID)
	if bErr != nil {
		return nil, status.Errorf(codes.Internal, "entitlement lookup failed: %v", bErr)
	}

	previousUntil, prevErr := target.readRetentionUntil(ctx, s.db, tenantID)
	if prevErr != nil {
		return nil, status.Errorf(codes.Internal, "retention state lookup failed: %v", prevErr)
	}
	foghornClient, err := s.resolveFoghornForArtifact(ctx, tenantID, target.originClusterID)
	if err != nil {
		return nil, err
	}

	// Anchor reset to ended_at for ALL asset types so a 60-day-old clip
	// reset to a 30-day default doesn't grant another 30 days from now.
	overrideReq := &foghornpb.OverrideArtifactRetentionRequest{
		TenantId:         tenantID,
		DvrHash:          target.hash,
		ArtifactType:     target.artifactType,
		RetentionDays:    resolvedDays,
		AnchorToEndedAt:  true,
		MaxRetentionDays: bound,
	}
	resp, _, err := foghornClient.OverrideArtifactRetention(ctx, overrideReq)
	if err != nil {
		s.logger.WithError(err).WithFields(logging.Fields{
			"tenant_id":     tenantID,
			"artifact_hash": target.hash,
			"artifact_type": target.artifactType,
		}).Error("Foghorn.OverrideArtifactRetention failed during reset")
		return nil, foghornRetentionError(err, "foghorn reset")
	}

	keepForever := resolvedDays == 0
	var newUntil time.Time
	if !keepForever {
		if resp.GetRetentionUntil() == nil {
			s.restoreFoghornRetention(ctx, foghornClient, tenantID, target, previousUntil)
			return nil, status.Error(codes.Internal, "foghorn reset returned no retention_until")
		}
		newUntil = resp.GetRetentionUntil().AsTime()
	}
	retentionDays := resolvedDays
	if !keepForever {
		retentionDays = daysUntil(newUntil)
	}

	var execErr error
	if keepForever {
		clearSQL := fmt.Sprintf(`
			UPDATE %s
			   SET retention_override_days  = NULL,
			       retention_override_until = NULL,
			       retention_source         = $1,
			       retention_until          = NULL,
			       updated_at               = NOW()
			 WHERE %s     = $2
			   AND tenant_id = $3::uuid
		`, target.table, target.hashColumn())
		_, execErr = s.db.ExecContext(ctx, clearSQL, source, target.hash, tenantID)
	} else {
		clearSQL := fmt.Sprintf(`
			UPDATE %s
			   SET retention_override_days  = NULL,
			       retention_override_until = NULL,
			       retention_source         = $1,
			       retention_until          = $2,
			       updated_at               = NOW()
			 WHERE %s     = $3
			   AND tenant_id = $4::uuid
		`, target.table, target.hashColumn())
		_, execErr = s.db.ExecContext(ctx, clearSQL, source, newUntil, target.hash, tenantID)
	}
	if execErr != nil {
		s.restoreFoghornRetention(ctx, foghornClient, tenantID, target, previousUntil)
		return nil, status.Errorf(codes.Internal, "override clear failed: %v", execErr)
	}
	s.emitRetentionArtifactEvent(ctx, eventRetentionOverrideReset, tenantID, userID, target, newUntil, keepForever)

	out := &commodorepb.UpdateAssetRetentionResponse{
		TargetId:      target.hash,
		RetentionDays: retentionDays,
		Source:        source,
	}
	if !keepForever {
		out.RetentionUntil = timestamppb.New(newUntil)
	}
	return out, nil
}

func (s *CommodoreServer) emitRetentionPolicyEvent(ctx context.Context, tenantID, userID, column string) {
	event := &ipcpb.ServiceEvent{
		EventType:    eventRetentionPolicyChanged,
		Timestamp:    timestamppb.Now(),
		Source:       "commodore",
		TenantId:     tenantID,
		UserId:       userID,
		ResourceType: "tenant",
		ResourceId:   tenantID,
		Payload: &ipcpb.ServiceEvent_TenantEvent{TenantEvent: &ipcpb.TenantEvent{
			TenantId:      tenantID,
			ChangedFields: []string{column},
		}},
	}
	s.emitServiceEvent(ctx, event)
}

func (s *CommodoreServer) emitRetentionArtifactEvent(ctx context.Context, eventType, tenantID, userID string, target *assetRetentionTarget, retentionUntil time.Time, keepForever bool) {
	if target == nil {
		return
	}
	event := &ipcpb.ServiceEvent{
		EventType:    eventType,
		Timestamp:    timestamppb.Now(),
		Source:       "commodore",
		TenantId:     tenantID,
		UserId:       userID,
		ResourceType: "artifact",
		ResourceId:   target.hash,
		Payload: &ipcpb.ServiceEvent_ArtifactEvent{ArtifactEvent: &ipcpb.ArtifactEvent{
			ArtifactType: retentionArtifactType(target.artifactType),
			ArtifactId:   target.hash,
			Status:       eventType,
		}},
	}
	if !keepForever {
		expiresAt := retentionUntil.Unix()
		event.GetArtifactEvent().ExpiresAt = &expiresAt
	}
	s.emitServiceEvent(ctx, event)
}

func retentionArtifactType(t string) ipcpb.ArtifactEvent_ArtifactType {
	switch t {
	case "clip":
		return ipcpb.ArtifactEvent_ARTIFACT_TYPE_CLIP
	case "dvr":
		return ipcpb.ArtifactEvent_ARTIFACT_TYPE_DVR
	case "vod":
		return ipcpb.ArtifactEvent_ARTIFACT_TYPE_VOD
	default:
		return ipcpb.ArtifactEvent_ARTIFACT_TYPE_UNSPECIFIED
	}
}
