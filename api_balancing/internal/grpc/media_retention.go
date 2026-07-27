package grpc

import (
	"context"
	"time"

	"frameworks/api_balancing/internal/control"
	foghornpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/foghorn"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// OverrideArtifactRetention pushes a per-asset retention horizon onto an
// existing foghorn.artifacts row so the next RetentionJob tick uses it.
//
// Tenant ownership is verified upstream by Commodore (assertDVRTenant /
// assertClipTenant / assertVodTenant); this handler trusts the
// (tenant_id, artifact_hash, artifact_type) tuple and only touches a row
// that matches all three. Active artifacts are rejected — retention applies
// post-finalize, and changing retention_until on an active recording would
// make the cleanup loop race with the recorder.
func (s *FoghornGRPCServer) OverrideArtifactRetention(ctx context.Context, req *foghornpb.OverrideArtifactRetentionRequest) (*foghornpb.OverrideArtifactRetentionResponse, error) {
	tenantID := req.GetTenantId()
	artifactHash := req.GetDvrHash()
	if tenantID == "" {
		return nil, status.Error(codes.InvalidArgument, "tenant_id is required")
	}
	if artifactHash == "" {
		return nil, status.Error(codes.InvalidArgument, "artifact hash is required")
	}
	artifactType := req.GetArtifactType()
	if artifactType == "" {
		artifactType = "dvr"
	}
	switch artifactType {
	case "dvr", "clip", "vod":
	default:
		return nil, status.Errorf(codes.InvalidArgument, "unsupported artifact_type %q", artifactType)
	}
	// "Keep forever" is retention_until = NULL. Commodore signals it as anchored-to-ended-at with
	// retention_days = 0; treat that (and an explicit request for it) as clearing the horizon
	// rather than rejecting it in resolveRetentionUntil's "days >= 1" guard.
	keepForever := req.GetRetentionUntil() == nil && req.GetAnchorToEndedAt() && req.GetRetentionDays() == 0
	var untilArg interface{}
	var untilTime time.Time
	if !keepForever {
		until, err := s.resolveRetentionUntil(ctx, tenantID, artifactHash, artifactType, req)
		if err != nil {
			return nil, err
		}
		untilArg = until
		untilTime = until
	}

	// The parent-artifact horizon and the child-chapter propagation commit as ONE transaction:
	// a DVR owns its chapters' retention, so they must move together (keep-forever ⇒ NULL for
	// both). A propagation failure rolls the parent update back rather than returning success
	// with diverged children.
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "retention override begin: %v", err)
	}
	committed := false
	defer func() {
		if !committed {
			tx.Rollback() //nolint:errcheck // best-effort rollback of an uncommitted tx
		}
	}()

	res, err := tx.ExecContext(ctx, `
		UPDATE foghorn.artifacts
		   SET retention_until = $1
		 WHERE artifact_hash = $2
		   AND tenant_id::text = $3
		   AND artifact_type = $4
		   AND status IN ('completed', 'completed_partial', 'ready', 'failed')
	`, untilArg, artifactHash, tenantID, artifactType)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "retention override failed: %v", err)
	}
	affected, raErr := res.RowsAffected()
	if raErr != nil {
		return nil, status.Errorf(codes.Internal, "retention override row count failed: %v", raErr)
	}
	if affected == 0 {
		return nil, status.Errorf(codes.FailedPrecondition,
			"%s artifact is active or not found; retention overrides apply only to finalized assets", artifactType)
	}

	if artifactType == "dvr" {
		if _, propErr := control.PropagateChapterRetentionTx(ctx, tx, artifactHash, untilArg); propErr != nil {
			return nil, status.Errorf(codes.Internal, "retention override: propagate to child chapters failed: %v", propErr)
		}
	}

	if commitErr := tx.Commit(); commitErr != nil {
		return nil, status.Errorf(codes.Internal, "retention override commit: %v", commitErr)
	}
	committed = true

	resp := &foghornpb.OverrideArtifactRetentionResponse{Applied: true}
	if !keepForever {
		resp.RetentionUntil = timestamppb.New(untilTime)
	}
	return resp, nil
}

func (s *FoghornGRPCServer) resolveRetentionUntil(ctx context.Context, tenantID, artifactHash, artifactType string, req *foghornpb.OverrideArtifactRetentionRequest) (time.Time, error) {
	var until time.Time
	var endedAt time.Time
	var endedAtSet bool
	if req.GetRetentionUntil() != nil {
		until = req.GetRetentionUntil().AsTime()
	} else if req.GetAnchorToEndedAt() {
		if req.GetRetentionDays() < 1 {
			return time.Time{}, status.Error(codes.InvalidArgument, "retention_days must be >= 1")
		}
		var err error
		endedAt, err = s.artifactEndedAt(ctx, tenantID, artifactHash, artifactType)
		if err != nil {
			return time.Time{}, err
		}
		endedAtSet = true
		until = endedAt.Add(time.Duration(req.GetRetentionDays()) * 24 * time.Hour)
	} else {
		return time.Time{}, status.Error(codes.InvalidArgument, "retention_until is required")
	}

	if maxDays := req.GetMaxRetentionDays(); maxDays > 0 {
		if !endedAtSet {
			var err error
			endedAt, err = s.artifactEndedAt(ctx, tenantID, artifactHash, artifactType)
			if err != nil {
				return time.Time{}, err
			}
		}
		maxUntil := endedAt.Add(time.Duration(maxDays) * 24 * time.Hour)
		if until.After(maxUntil) {
			return time.Time{}, status.Errorf(codes.InvalidArgument,
				"retention horizon exceeds tier bound of %d days after artifact end", maxDays)
		}
	}
	return until, nil
}

func (s *FoghornGRPCServer) artifactEndedAt(ctx context.Context, tenantID, artifactHash, artifactType string) (time.Time, error) {
	var endedAt time.Time
	if err := s.db.QueryRowContext(ctx, `
		SELECT ended_at
		  FROM foghorn.artifacts
		 WHERE artifact_hash = $1
		   AND tenant_id::text = $2
		   AND artifact_type = $3
		   AND status IN ('completed', 'completed_partial', 'ready', 'failed')
		   AND ended_at IS NOT NULL
	`, artifactHash, tenantID, artifactType).Scan(&endedAt); err != nil {
		return time.Time{}, status.Errorf(codes.FailedPrecondition,
			"%s artifact is active, missing ended_at, or not found; retention resets require a finalized asset", artifactType)
	}
	return endedAt, nil
}
