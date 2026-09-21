package grpc

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"frameworks/api_balancing/internal/control"
	"frameworks/api_balancing/internal/database/foghorndb"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/database"
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
		// Chapters inherit both playback policy and retention from their
		// parent DVR. Their bytes use the VOD storage machinery, but chapter
		// is not an independently mutable VOD business object.
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
	errNotFinalized := errors.New("retention override target not finalized")
	stage := "begin"
	retentionUntil := sql.NullTime{}
	if untilArg != nil {
		retentionUntil = sql.NullTime{Time: untilTime, Valid: true}
	}
	txErr := database.WithRetryablePostgresTxWithHook(ctx, s.db, nil, func(error, int) { stage = "begin" }, func(tx *sql.Tx) error {
		stage = "body"
		affected, err := foghorndb.New(tx).OverrideFinalizedArtifactRetention(ctx, foghorndb.OverrideFinalizedArtifactRetentionParams{
			RetentionUntil: retentionUntil, ArtifactHash: artifactHash,
			TenantID: tenantID, ArtifactType: artifactType,
		})
		if err != nil {
			return fmt.Errorf("retention override failed: %w", err)
		}
		if affected == 0 {
			return errNotFinalized
		}

		if artifactType == "dvr" {
			if _, propErr := control.PropagateChapterRetentionTx(ctx, tx, tenantID, artifactHash, untilArg); propErr != nil {
				return fmt.Errorf("retention override: propagate to child chapters failed: %w", propErr)
			}
		}
		stage = "commit"
		return nil
	})
	if txErr != nil {
		switch {
		case errors.Is(txErr, errNotFinalized):
			return nil, status.Errorf(codes.FailedPrecondition,
				"%s artifact is active or not found; retention overrides apply only to finalized assets", artifactType)
		case stage == "body":
			return nil, status.Error(codes.Internal, txErr.Error())
		case stage == "commit":
			return nil, status.Errorf(codes.Internal, "retention override commit: %v", txErr)
		default:
			return nil, status.Errorf(codes.Internal, "retention override begin: %v", txErr)
		}
	}

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
	endedAt, err := foghorndb.New(s.db).GetFinalizedArtifactEndedAt(ctx, foghorndb.GetFinalizedArtifactEndedAtParams{
		ArtifactHash: artifactHash, TenantID: tenantID, ArtifactType: artifactType,
	})
	if err != nil {
		return time.Time{}, status.Errorf(codes.FailedPrecondition,
			"%s artifact is active, missing ended_at, or not found; retention resets require a finalized asset", artifactType)
	}
	return endedAt.Time, nil
}
