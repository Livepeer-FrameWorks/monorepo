package grpc

import (
	"context"
	"database/sql"
	"errors"

	"frameworks/api_balancing/internal/control"
	foghornpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/foghorn"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Caller surface for chapter metadata. Chapters are produced by the
// finalization queue as canonical .mkv VOD artifacts
// (origin_type='dvr_chapter', library_visible=false). This RPC surface
// reads the chapter row + its playback artifact; it never builds
// manifests.
//
// All operate on UTC epoch ranges.

const minAutomaticChapterIntervalSeconds int32 = 3600

// RetrieveDVRChapter returns the chapter row metadata for a single
// chapter. The chapter must already exist (the chapter sweeper opens
// rows at boundary rotation); cache-on-request synthesis is not
// supported in the new finalization model.
func (s *FoghornGRPCServer) RetrieveDVRChapter(ctx context.Context, req *foghornpb.RetrieveDVRChapterRequest) (*foghornpb.RetrieveDVRChapterResponse, error) {
	if req.GetDvrArtifactId() == "" {
		return nil, status.Error(codes.InvalidArgument, "dvr_artifact_id is required")
	}
	if req.GetEndMs() <= req.GetStartMs() {
		return nil, status.Error(codes.InvalidArgument, "end_ms must be greater than start_ms")
	}
	mode := req.GetMode()
	switch mode {
	case "", control.ChapterModeWindowSized, control.ChapterModeFixedInterval:
	default:
		return nil, status.Errorf(codes.InvalidArgument, "invalid mode: %q", mode)
	}
	if mode == "" {
		policy, hasPolicy, err := control.ReadDVRChapterPolicy(ctx, req.GetDvrArtifactId())
		if err != nil {
			return nil, status.Error(codes.Internal, "failed to read chapter policy")
		}
		if !hasPolicy {
			return nil, status.Error(codes.FailedPrecondition, "DVR has no chapter policy")
		}
		mode = policy.Mode
	}
	if mode == control.ChapterModeFixedInterval && req.GetIntervalSeconds() < minAutomaticChapterIntervalSeconds {
		return nil, status.Errorf(codes.InvalidArgument, "interval_seconds must be at least %d for fixed_interval mode", minAutomaticChapterIntervalSeconds)
	}
	if err := s.assertChapterTenant(ctx, req.GetDvrArtifactId(), req.GetTenantId()); err != nil {
		return nil, err
	}
	intervalSeconds := req.GetIntervalSeconds()
	if mode == control.ChapterModeWindowSized {
		policy, hasPolicy, policyErr := control.ReadDVRChapterPolicy(ctx, req.GetDvrArtifactId())
		if policyErr != nil {
			return nil, status.Error(codes.Internal, "failed to read chapter policy")
		}
		if hasPolicy {
			intervalSeconds = policy.EffectiveIntervalSeconds()
		}
	}
	chapterID := control.BuildChapterID(req.GetDvrArtifactId(), mode, intervalSeconds, req.GetStartMs(), req.GetEndMs())
	row, err := control.GetChapter(ctx, chapterID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, status.Error(codes.NotFound, "chapter not found")
		}
		s.logger.WithError(err).WithField("chapter_id", chapterID).Error("Failed to read chapter row")
		return nil, status.Error(codes.Internal, "failed to read chapter")
	}
	resp := &foghornpb.RetrieveDVRChapterResponse{
		ChapterId:    row.ChapterID,
		State:        row.State,
		IsCurrent:    row.IsCurrent,
		HasGaps:      row.HasGaps,
		SegmentCount: row.SegmentCount,
		StartMs:      row.StartMs,
		EndMs:        row.EndMs,
	}
	if row.PlaybackArtifactHash.Valid {
		resp.PlaybackArtifactHash = row.PlaybackArtifactHash.String
	}
	if row.PlaybackID.Valid {
		resp.PlaybackId = row.PlaybackID.String
	}
	if row.LastFailureReason.Valid {
		resp.LastFailureReason = row.LastFailureReason.String
	}
	if row.ActualMediaStartMs.Valid {
		resp.ActualMediaStartMs = row.ActualMediaStartMs.Int64
	}
	if row.ActualMediaEndMs.Valid {
		resp.ActualMediaEndMs = row.ActualMediaEndMs.Int64
	}
	return resp, nil
}

// ListDVRChapters returns a paginated chapter index for player UI.
// Virtual chapters (computed from the artifact's policy) overlay
// materialized rows so the player can address future boundaries
// before the sweeper opens them.
func (s *FoghornGRPCServer) ListDVRChapters(ctx context.Context, req *foghornpb.ListDVRChaptersRequest) (*foghornpb.ListDVRChaptersResponse, error) {
	if req.GetDvrArtifactId() == "" {
		return nil, status.Error(codes.InvalidArgument, "dvr_artifact_id is required")
	}
	if err := s.assertChapterTenant(ctx, req.GetDvrArtifactId(), req.GetTenantId()); err != nil {
		return nil, err
	}
	pageSize := int(req.GetPageSize())
	if pageSize <= 0 {
		pageSize = 200
	}
	if pageSize > 1000 {
		pageSize = 1000
	}
	mode := req.GetMode()
	if mode == control.ChapterModeFixedInterval {
		if req.GetIntervalSeconds() <= 0 {
			return nil, status.Error(codes.InvalidArgument, "interval_seconds is required for fixed_interval mode")
		}
		if req.GetIntervalSeconds() < minAutomaticChapterIntervalSeconds {
			return nil, status.Errorf(codes.InvalidArgument, "interval_seconds must be at least %d for fixed_interval mode", minAutomaticChapterIntervalSeconds)
		}
		maxRangeMs, err := control.DVRChapterMaxRangeMs(ctx, s.db, req.GetDvrArtifactId(), "")
		if err != nil {
			return nil, status.Error(codes.NotFound, "DVR artifact not found")
		}
		if int64(req.GetIntervalSeconds())*1000 > maxRangeMs {
			return nil, status.Errorf(codes.InvalidArgument, "interval_seconds exceeds maximum %d", maxRangeMs/1000)
		}
	}
	rows, nextToken, err := control.ListVirtualChaptersForArtifact(
		ctx,
		req.GetDvrArtifactId(),
		mode,
		req.GetIntervalSeconds(),
		req.GetRangeStartMs(),
		req.GetRangeEndMs(),
		pageSize,
		req.GetPageToken(),
	)
	if err != nil {
		s.logger.WithError(err).Error("Failed to list chapters")
		return nil, status.Error(codes.Internal, "failed to list chapters")
	}
	out := make([]*foghornpb.ChapterRef, 0, len(rows))
	for _, r := range rows {
		ref := &foghornpb.ChapterRef{
			ChapterId:       r.ChapterID,
			Mode:            r.Mode,
			IntervalSeconds: r.IntervalSeconds.Int32,
			StartMs:         r.StartMs,
			EndMs:           r.EndMs,
			IsCurrent:       r.IsCurrent,
			State:           r.State,
			HasGaps:         r.HasGaps,
			SegmentCount:    r.SegmentCount,
		}
		if r.PlaybackArtifactHash.Valid {
			ref.PlaybackArtifactHash = r.PlaybackArtifactHash.String
		}
		if r.PlaybackID.Valid {
			ref.PlaybackId = r.PlaybackID.String
		}
		if r.LastFailureReason.Valid {
			ref.LastFailureReason = r.LastFailureReason.String
		}
		if r.ActualMediaStartMs.Valid {
			ref.ActualMediaStartMs = r.ActualMediaStartMs.Int64
		}
		if r.ActualMediaEndMs.Valid {
			ref.ActualMediaEndMs = r.ActualMediaEndMs.Int64
		}
		out = append(out, ref)
	}
	return &foghornpb.ListDVRChaptersResponse{
		Chapters:      out,
		NextPageToken: nextToken,
	}, nil
}

// assertChapterTenant verifies the tenant_id on the request matches the
// artifact's owner. Empty tenant_id on the request is allowed for
// internal callers (federation / sweeper); external HTTP exposure
// should always pass a tenant_id (api_gateway enforces).
func (s *FoghornGRPCServer) assertChapterTenant(ctx context.Context, artifactHash, claimedTenantID string) error {
	if claimedTenantID == "" {
		return nil
	}
	var tenantID string
	if err := s.db.QueryRowContext(ctx,
		`SELECT tenant_id::text FROM foghorn.artifacts WHERE artifact_hash = $1 AND artifact_type = 'dvr'`,
		artifactHash,
	).Scan(&tenantID); err != nil {
		return status.Error(codes.NotFound, "DVR artifact not found")
	}
	if tenantID != claimedTenantID {
		return status.Error(codes.PermissionDenied, "tenant mismatch")
	}
	return nil
}
