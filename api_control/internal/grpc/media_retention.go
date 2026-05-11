package grpc

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	foghornclient "github.com/Livepeer-FrameWorks/monorepo/pkg/clients/foghorn"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	pb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// Customer-tunable media retention. Tenant-default policy lives in
// commodore.tenant_media_retention_policies; per-asset overrides live on
// commodore.{dvr_recordings,clips,vod_assets}.{retention_override_days,
// retention_override_until,retention_source}. Cascade order at StartDVR /
// override is per-asset → tenant → Purser entitlement (the existing
// DVRPolicy.recording_retention_days).
//
// Tier entitlement is the upper bound: tenant policy and per-asset
// overrides must be ≤ Purser's recording_retention_days for the tier
// (0 = no entitlement set, treated as system default 30).

const (
	defaultRetentionDays  = 30
	retentionSourceTenant = "tenant_default"
	retentionSourceAsset  = "per_asset_override"
	retentionSourceTier   = "tier_entitlement"

	eventRetentionPolicyChanged   = "media.retention_policy.changed"
	eventRetentionOverrideApplied = "media.retention.override_applied"
	eventRetentionOverrideReset   = "media.retention.override_reset"
)

// fetchEntitlementBound asks Purser for the tier's recording_retention_days.
// Returns the bound (capped to the system default if Purser doesn't know).
func (s *CommodoreServer) fetchEntitlementBound(ctx context.Context, tenantID string) (int32, error) {
	if s.purserClient == nil {
		return defaultRetentionDays, nil
	}
	bs, err := s.purserClient.GetTenantBillingStatus(ctx, tenantID)
	if err != nil {
		return 0, err
	}
	if bs == nil {
		return defaultRetentionDays, nil
	}
	if d := bs.GetRecordingRetentionDays(); d > 0 {
		return d, nil
	}
	return defaultRetentionDays, nil
}

// readTenantPolicy returns the tenant override row if one exists.
func (s *CommodoreServer) readTenantPolicy(ctx context.Context, tenantID string) (set bool, days int32, updatedBy string, updatedAt time.Time, err error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT recording_retention_days, COALESCE(updated_by::text, ''), updated_at
		  FROM commodore.tenant_media_retention_policies
		 WHERE tenant_id = $1::uuid
	`, tenantID)
	var d sql.NullInt32
	if scanErr := row.Scan(&d, &updatedBy, &updatedAt); scanErr != nil {
		if errors.Is(scanErr, sql.ErrNoRows) {
			return false, 0, "", time.Time{}, nil
		}
		return false, 0, "", time.Time{}, scanErr
	}
	if !d.Valid {
		return false, 0, updatedBy, updatedAt, nil
	}
	return true, d.Int32, updatedBy, updatedAt, nil
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
	if _, _, err := client.OverrideArtifactRetention(ctx, &pb.OverrideArtifactRetentionRequest{
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

// GetMediaRetentionPolicy returns the tenant default + entitlement bounds
// + the value the cascade would resolve to today.
func (s *CommodoreServer) GetMediaRetentionPolicy(ctx context.Context, req *pb.GetMediaRetentionPolicyRequest) (*pb.GetMediaRetentionPolicyResponse, error) {
	_, tenantID, err := extractUserContext(ctx)
	if err != nil {
		return nil, err
	}
	if t := req.GetTenantId(); t != "" && t != tenantID {
		return nil, status.Error(codes.PermissionDenied, "tenant mismatch")
	}

	bound, bErr := s.fetchEntitlementBound(ctx, tenantID)
	if bErr != nil {
		s.logger.WithError(bErr).Warn("Purser bound lookup failed; serving with default cap")
		bound = defaultRetentionDays
	}

	set, days, updatedBy, updatedAt, dbErr := s.readTenantPolicy(ctx, tenantID)
	if dbErr != nil {
		return nil, status.Errorf(codes.Internal, "policy lookup failed: %v", dbErr)
	}

	effective := bound
	if set && days > 0 && days < bound {
		effective = days
	}

	resp := &pb.GetMediaRetentionPolicyResponse{
		RecordingRetentionDaysSet:       set,
		RecordingRetentionDays:          days,
		EffectiveRecordingRetentionDays: effective,
		Bounds: &pb.MediaRetentionBounds{
			MaxRecordingRetentionDays: bound,
		},
		UpdatedBy: updatedBy,
	}
	if !updatedAt.IsZero() {
		resp.UpdatedAt = timestamppb.New(updatedAt)
	}
	return resp, nil
}

// SetMediaRetentionPolicy writes the tenant default. A value equal to the
// current tier bound clears the override row so future tier changes flow
// through. Lower bound is 1 day (use deletion to expire immediately).
func (s *CommodoreServer) SetMediaRetentionPolicy(ctx context.Context, req *pb.SetMediaRetentionPolicyRequest) (*pb.SetMediaRetentionPolicyResponse, error) {
	userID, tenantID, err := extractUserContext(ctx)
	if err != nil {
		return nil, err
	}
	if t := req.GetTenantId(); t != "" && t != tenantID {
		return nil, status.Error(codes.PermissionDenied, "tenant mismatch")
	}
	days := req.GetRecordingRetentionDays()
	if days < 1 {
		return nil, status.Error(codes.InvalidArgument, "recording_retention_days must be >= 1")
	}

	bound, bErr := s.fetchEntitlementBound(ctx, tenantID)
	if bErr != nil {
		return nil, status.Errorf(codes.Internal, "entitlement lookup failed: %v", bErr)
	}
	if days > bound {
		return nil, status.Errorf(codes.InvalidArgument,
			"recording_retention_days=%d exceeds tier bound %d; upgrade your plan to extend retention",
			days, bound)
	}

	updatedBy := req.GetUpdatedBy()
	if updatedBy == "" {
		updatedBy = userID
	}

	if days == bound {
		if _, execErr := s.db.ExecContext(ctx, `
			DELETE FROM commodore.tenant_media_retention_policies
			      WHERE tenant_id = $1::uuid
		`, tenantID); execErr != nil {
			return nil, status.Errorf(codes.Internal, "policy reset failed: %v", execErr)
		}
	} else {
		if _, execErr := s.db.ExecContext(ctx, `
			INSERT INTO commodore.tenant_media_retention_policies
			            (tenant_id, recording_retention_days, updated_by, created_at, updated_at)
			VALUES      ($1::uuid, $2, NULLIF($3, '')::uuid, NOW(), NOW())
			ON CONFLICT (tenant_id) DO UPDATE
			   SET recording_retention_days = EXCLUDED.recording_retention_days,
			       updated_by = EXCLUDED.updated_by,
			       updated_at = NOW()
		`, tenantID, days, updatedBy); execErr != nil {
			return nil, status.Errorf(codes.Internal, "policy upsert failed: %v", execErr)
		}
	}

	s.logger.WithFields(logging.Fields{
		"tenant_id":                tenantID,
		"recording_retention_days": days,
		"updated_by":               updatedBy,
	}).Info("Media retention policy updated")
	s.emitRetentionPolicyEvent(ctx, tenantID, userID)

	policy, err := s.GetMediaRetentionPolicy(ctx, &pb.GetMediaRetentionPolicyRequest{TenantId: tenantID})
	if err != nil {
		return nil, err
	}
	return &pb.SetMediaRetentionPolicyResponse{Policy: policy}, nil
}

// assetRetentionTarget bundles the resolution result for a per-asset
// retention operation: which Commodore table to update, which artifact_type
// Foghorn should match against, the canonical hash to key on, and the
// originating cluster (for routing the Foghorn override RPC).
type assetRetentionTarget struct {
	table           string // commodore.dvr_recordings | commodore.clips | commodore.vod_assets
	artifactType    string // dvr | clip | vod (matches foghorn.artifacts.artifact_type)
	hash            string
	originClusterID string
}

// resolveAssetTarget validates tenant ownership of the asset and returns the
// routing context Foghorn needs. It dispatches to the existing per-type
// asserters (assertDVRTenant for DVR; new assertClipTenant / assertVodTenant
// for clips and VOD).
func (s *CommodoreServer) resolveAssetTarget(ctx context.Context, targetType pb.MediaRetentionTarget, targetID, tenantID string) (*assetRetentionTarget, error) {
	switch targetType {
	case pb.MediaRetentionTarget_MEDIA_RETENTION_TARGET_DVR:
		originClusterID, dvrHash, err := s.assertDVRTenant(ctx, targetID, tenantID)
		if err != nil {
			return nil, err
		}
		return &assetRetentionTarget{
			table:           "commodore.dvr_recordings",
			artifactType:    "dvr",
			hash:            dvrHash,
			originClusterID: originClusterID,
		}, nil
	case pb.MediaRetentionTarget_MEDIA_RETENTION_TARGET_CLIP:
		originClusterID, clipHash, err := s.assertClipTenant(ctx, targetID, tenantID)
		if err != nil {
			return nil, err
		}
		return &assetRetentionTarget{
			table:           "commodore.clips",
			artifactType:    "clip",
			hash:            clipHash,
			originClusterID: originClusterID,
		}, nil
	case pb.MediaRetentionTarget_MEDIA_RETENTION_TARGET_VOD:
		originClusterID, vodHash, err := s.assertVodTenant(ctx, targetID, tenantID)
		if err != nil {
			return nil, err
		}
		return &assetRetentionTarget{
			table:           "commodore.vod_assets",
			artifactType:    "vod",
			hash:            vodHash,
			originClusterID: originClusterID,
		}, nil
	default:
		return nil, status.Error(codes.InvalidArgument, "target_type must be DVR, CLIP, or VOD")
	}
}

// assertClipTenant mirrors assertDVRTenant for the clips table. Accepts
// either commodore.clips.id (UUID) or clip_hash; returns (origin_cluster_id,
// clip_hash) — clip_hash is what Foghorn keys on.
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

// assertVodTenant mirrors assertDVRTenant for the vod_assets table. Accepts
// either commodore.vod_assets.id (UUID) or vod_hash; returns
// (origin_cluster_id, vod_hash).
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
// canonical hash. Used by the per-asset override SQL. Trusted internal
// switch; targets that fall through here came from resolveAssetTarget.
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

// UpdateAssetRetention writes a per-asset retention override and pushes the
// new horizon to Foghorn so the existing RetentionJob picks it up.
func (s *CommodoreServer) UpdateAssetRetention(ctx context.Context, req *pb.UpdateAssetRetentionRequest) (*pb.UpdateAssetRetentionResponse, error) {
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
		retentionUntil time.Time
		retentionDays  int32
	)
	if req.GetRetentionUntil() != nil && req.GetRetentionDays() > 0 {
		return nil, status.Error(codes.InvalidArgument, "retention_days and retention_until are mutually exclusive")
	}
	switch {
	case req.GetRetentionUntil() != nil:
		retentionUntil = req.GetRetentionUntil().AsTime()
		if retentionUntil.Before(time.Now()) {
			return nil, status.Error(codes.InvalidArgument, "retention_until must be in the future")
		}
		retentionDays = daysUntil(retentionUntil)
	case req.GetRetentionDays() > 0:
		retentionDays = req.GetRetentionDays()
		retentionUntil = time.Now().Add(time.Duration(retentionDays) * 24 * time.Hour)
	default:
		return nil, status.Error(codes.InvalidArgument, "either retention_days or retention_until must be set")
	}
	if retentionDays > bound {
		return nil, status.Errorf(codes.InvalidArgument,
			"retention_days=%d exceeds tier bound %d", retentionDays, bound)
	}
	if retentionDays < 1 {
		return nil, status.Error(codes.InvalidArgument, "retention_days must be >= 1")
	}

	previousUntil, prevErr := target.readRetentionUntil(ctx, s.db, tenantID)
	if prevErr != nil {
		return nil, status.Errorf(codes.Internal, "retention state lookup failed: %v", prevErr)
	}
	foghornClient, err := s.resolveFoghornForArtifact(ctx, tenantID, target.originClusterID)
	if err != nil {
		return nil, err
	}
	resp, _, err := foghornClient.OverrideArtifactRetention(ctx, &pb.OverrideArtifactRetentionRequest{
		TenantId:         tenantID,
		DvrHash:          target.hash,
		ArtifactType:     target.artifactType,
		RetentionUntil:   timestamppb.New(retentionUntil),
		MaxRetentionDays: bound,
	})
	if err != nil {
		s.logger.WithError(err).WithFields(logging.Fields{
			"tenant_id":     tenantID,
			"artifact_hash": target.hash,
			"artifact_type": target.artifactType,
		}).Error("Foghorn.OverrideArtifactRetention failed")
		return nil, foghornRetentionError(err, "foghorn override")
	}
	if resp.GetRetentionUntil() != nil {
		retentionUntil = resp.GetRetentionUntil().AsTime()
		retentionDays = daysUntil(retentionUntil)
	}

	// Table name + hash column come from the resolved target — both are
	// internal trusted strings (not user input), so direct interpolation
	// into the SQL is safe and keeps the parameter binding clean.
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
	if _, execErr := s.db.ExecContext(ctx, updateSQL,
		retentionDays, retentionUntil, retentionSourceAsset, target.hash, tenantID,
	); execErr != nil {
		s.restoreFoghornRetention(ctx, foghornClient, tenantID, target, previousUntil)
		return nil, status.Errorf(codes.Internal, "override write failed: %v", execErr)
	}

	s.logger.WithFields(logging.Fields{
		"tenant_id":      tenantID,
		"artifact_hash":  target.hash,
		"artifact_type":  target.artifactType,
		"retention_days": retentionDays,
	}).Info("Per-asset retention override applied")
	s.emitRetentionArtifactEvent(ctx, eventRetentionOverrideApplied, tenantID, userID, target, retentionUntil)

	return &pb.UpdateAssetRetentionResponse{
		TargetId:       target.hash,
		RetentionDays:  retentionDays,
		RetentionUntil: timestamppb.New(retentionUntil),
		Source:         retentionSourceAsset,
	}, nil
}

// ResetAssetRetention clears any per-asset override and recomputes the
// horizon from the cascade (tenant default → tier entitlement). The new
// horizon is pushed to Foghorn so the artifact lines up with the rest.
func (s *CommodoreServer) ResetAssetRetention(ctx context.Context, req *pb.ResetAssetRetentionRequest) (*pb.UpdateAssetRetentionResponse, error) {
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
	set, tenantDays, _, _, dbErr := s.readTenantPolicy(ctx, tenantID)
	if dbErr != nil {
		return nil, status.Errorf(codes.Internal, "tenant policy lookup failed: %v", dbErr)
	}

	effectiveDays := bound
	source := retentionSourceTier
	if set && tenantDays > 0 && tenantDays < bound {
		effectiveDays = tenantDays
		source = retentionSourceTenant
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
	// Foghorn's resolveRetentionUntil handles the ended_at lookup against
	// foghorn.artifacts and supports dvr | clip | vod uniformly.
	overrideReq := &pb.OverrideArtifactRetentionRequest{
		TenantId:         tenantID,
		DvrHash:          target.hash,
		ArtifactType:     target.artifactType,
		RetentionDays:    effectiveDays,
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
	if resp.GetRetentionUntil() == nil {
		s.restoreFoghornRetention(ctx, foghornClient, tenantID, target, previousUntil)
		return nil, status.Error(codes.Internal, "foghorn reset returned no retention_until")
	}
	newUntil := resp.GetRetentionUntil().AsTime()
	retentionDays := daysUntil(newUntil)

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
	if _, execErr := s.db.ExecContext(ctx, clearSQL,
		source, newUntil, target.hash, tenantID,
	); execErr != nil {
		s.restoreFoghornRetention(ctx, foghornClient, tenantID, target, previousUntil)
		return nil, status.Errorf(codes.Internal, "override clear failed: %v", execErr)
	}
	s.emitRetentionArtifactEvent(ctx, eventRetentionOverrideReset, tenantID, userID, target, newUntil)

	return &pb.UpdateAssetRetentionResponse{
		TargetId:       target.hash,
		RetentionDays:  retentionDays,
		RetentionUntil: timestamppb.New(newUntil),
		Source:         source,
	}, nil
}

func (s *CommodoreServer) emitRetentionPolicyEvent(ctx context.Context, tenantID, userID string) {
	event := &pb.ServiceEvent{
		EventType:    eventRetentionPolicyChanged,
		Timestamp:    timestamppb.Now(),
		Source:       "commodore",
		TenantId:     tenantID,
		UserId:       userID,
		ResourceType: "tenant",
		ResourceId:   tenantID,
		Payload: &pb.ServiceEvent_TenantEvent{TenantEvent: &pb.TenantEvent{
			TenantId:      tenantID,
			ChangedFields: []string{"recording_retention_days"},
		}},
	}
	s.emitServiceEvent(ctx, event)
}

func (s *CommodoreServer) emitRetentionArtifactEvent(ctx context.Context, eventType, tenantID, userID string, target *assetRetentionTarget, retentionUntil time.Time) {
	if target == nil {
		return
	}
	expiresAt := retentionUntil.Unix()
	event := &pb.ServiceEvent{
		EventType:    eventType,
		Timestamp:    timestamppb.Now(),
		Source:       "commodore",
		TenantId:     tenantID,
		UserId:       userID,
		ResourceType: "artifact",
		ResourceId:   target.hash,
		Payload: &pb.ServiceEvent_ArtifactEvent{ArtifactEvent: &pb.ArtifactEvent{
			ArtifactType: retentionArtifactType(target.artifactType),
			ArtifactId:   target.hash,
			Status:       eventType,
			ExpiresAt:    &expiresAt,
		}},
	}
	s.emitServiceEvent(ctx, event)
}

func retentionArtifactType(t string) pb.ArtifactEvent_ArtifactType {
	switch t {
	case "clip":
		return pb.ArtifactEvent_ARTIFACT_TYPE_CLIP
	case "dvr":
		return pb.ArtifactEvent_ARTIFACT_TYPE_DVR
	case "vod":
		return pb.ArtifactEvent_ARTIFACT_TYPE_VOD
	default:
		return pb.ArtifactEvent_ARTIFACT_TYPE_UNSPECIFIED
	}
}
