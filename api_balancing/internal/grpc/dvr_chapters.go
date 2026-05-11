package grpc

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"frameworks/api_balancing/internal/control"
	pb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Caller surface for chapter-aware playback:
//   - RetrieveDVRChapter: materialize-on-request, returns chapter metadata.
//   - ListDVRChapters:    paginated chapter index for player UI.
//   - SetDVRChapterPolicy: change the artifact's default chapter mode.
//
// All operate on UTC epoch ranges. Civil-time chapters (e.g. "yesterday in
// Europe/Amsterdam") submit mode=explicit_range with caller-resolved
// (start_ms, end_ms) — no timezone state lives in the backend.

const minAutomaticChapterIntervalSeconds int32 = 3600

// RetrieveDVRChapter materializes cache-on-request chapter metadata. Playback
// routes through MistServer using dvr+{chapter_id}; the response does not
// expose object-store URLs.
func (s *FoghornGRPCServer) RetrieveDVRChapter(ctx context.Context, req *pb.RetrieveDVRChapterRequest) (*pb.RetrieveDVRChapterResponse, error) {
	if req.GetDvrArtifactId() == "" {
		return nil, status.Error(codes.InvalidArgument, "dvr_artifact_id is required")
	}
	if req.GetEndMs() <= req.GetStartMs() {
		return nil, status.Error(codes.InvalidArgument, "end_ms must be greater than start_ms")
	}
	mode := req.GetMode()
	if mode == "" {
		mode = control.ChapterModeExplicitRange
	}
	if mode == control.ChapterModeFixedInterval && req.GetIntervalSeconds() <= 0 {
		return nil, status.Error(codes.InvalidArgument, "interval_seconds is required for fixed_interval mode")
	}
	if mode == control.ChapterModeFixedInterval && req.GetIntervalSeconds() < minAutomaticChapterIntervalSeconds {
		return nil, status.Errorf(codes.InvalidArgument, "interval_seconds must be at least %d for fixed_interval mode", minAutomaticChapterIntervalSeconds)
	}

	if err := s.assertChapterTenant(ctx, req.GetDvrArtifactId(), req.GetTenantId()); err != nil {
		return nil, err
	}
	maxRangeMs, err := control.DVRChapterMaxRangeMs(ctx, s.db, req.GetDvrArtifactId(), "")
	if err != nil {
		return nil, status.Error(codes.NotFound, "DVR artifact not found")
	}
	if req.GetEndMs()-req.GetStartMs() > maxRangeMs {
		return nil, status.Errorf(codes.InvalidArgument, "chapter range exceeds maximum %dms", maxRangeMs)
	}
	if mode == control.ChapterModeFixedInterval && int64(req.GetIntervalSeconds())*1000 > maxRangeMs {
		return nil, status.Errorf(codes.InvalidArgument, "interval_seconds exceeds maximum %d", maxRangeMs/1000)
	}
	intervalSeconds := req.GetIntervalSeconds()
	policy, hasPolicy, policyErr := control.ReadDVRChapterPolicy(ctx, req.GetDvrArtifactId())
	if policyErr != nil {
		s.logger.WithError(policyErr).WithField("artifact_hash", req.GetDvrArtifactId()).Warn("Failed to read DVR chapter policy")
		return nil, status.Error(codes.Internal, "failed to read chapter policy")
	}
	if mode == control.ChapterModeWindowSized && hasPolicy {
		intervalSeconds = policy.EffectiveIntervalSeconds()
	}

	activeForPolicy := func(p control.DVRChapterPolicy, ok bool) bool {
		if !ok || !control.DVRArtifactStillRecording(ctx, req.GetDvrArtifactId()) || mode != p.Mode || intervalSeconds != p.EffectiveIntervalSeconds() {
			return false
		}
		currentStart, currentEnd, boundsOK := control.CurrentChapterBounds(p.Mode, intervalSeconds, p.StartedAtMs, time.Now().UnixMilli())
		return boundsOK && req.GetStartMs() == currentStart && req.GetEndMs() == currentEnd
	}

	chapterID := control.BuildChapterID(req.GetDvrArtifactId(), mode, intervalSeconds, req.GetStartMs(), req.GetEndMs())
	existing, err := control.GetChapter(ctx, chapterID)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		s.logger.WithError(err).WithField("chapter_id", chapterID).Error("Failed to read chapter row")
		return nil, status.Error(codes.Internal, "failed to read chapter")
	}

	isActive := activeForPolicy(policy, hasPolicy)

	// (Re)materialize when missing, when gap invalidation marked the row dirty, or
	// when this is the active current chapter (the sweeper will keep it
	// fresh on the next tick anyway, but cache-on-request gets the caller
	// a non-stale manifest immediately).
	needsBuild := existing == nil || !existing.MaterializedAt.Valid || !existing.LastRebuiltAt.Valid || isActive
	if needsBuild {
		build := func(active bool) error {
			_, _, buildErr := control.GenerateChapter(ctx, control.GenerateChapterOptions{
				ArtifactHash:    req.GetDvrArtifactId(),
				Mode:            mode,
				IntervalSeconds: intervalSeconds,
				StartMs:         req.GetStartMs(),
				EndMs:           req.GetEndMs(),
				IsActive:        active,
			}, s.logger)
			return buildErr
		}
		var buildErr error
		if isActive {
			buildErr = control.WithDVRChapterMutationLock(ctx, req.GetDvrArtifactId(), func() error {
				lockedPolicy, lockedHasPolicy, lockedErr := control.ReadDVRChapterPolicy(ctx, req.GetDvrArtifactId())
				if lockedErr != nil {
					return lockedErr
				}
				isActive = activeForPolicy(lockedPolicy, lockedHasPolicy)
				return build(isActive)
			})
		} else {
			buildErr = build(false)
		}
		if buildErr != nil {
			s.logger.WithError(buildErr).WithField("chapter_id", chapterID).Error("Failed to materialize chapter")
			return nil, status.Error(codes.Internal, "failed to materialize chapter")
		}
	}

	// Re-read for fresh has_gaps / segment_count after the build.
	row, err := control.GetChapter(ctx, chapterID)
	if err != nil || row == nil {
		s.logger.WithError(err).WithField("chapter_id", chapterID).Warn("Chapter row missing after materialization")
		return nil, status.Error(codes.Internal, "chapter row missing after materialization")
	}
	resp := &pb.RetrieveDVRChapterResponse{
		ChapterId:     row.ChapterID,
		ManifestS3Key: row.ManifestS3Key.String,
		ManifestUrl:   control.DVRChapterPlaybackID(row.ChapterID),
		IsCurrent:     row.IsCurrent,
		HasGaps:       row.HasGaps,
		SegmentCount:  row.SegmentCount,
	}
	return resp, nil
}

// ListDVRChapters returns a paginated chapter index for player UI.
func (s *FoghornGRPCServer) ListDVRChapters(ctx context.Context, req *pb.ListDVRChaptersRequest) (*pb.ListDVRChaptersResponse, error) {
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
	listFn := control.ListChaptersForArtifact
	if mode != control.ChapterModeExplicitRange {
		listFn = control.ListVirtualChaptersForArtifact
	}
	rows, nextToken, err := listFn(
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
	out := make([]*pb.ChapterRef, 0, len(rows))
	for _, r := range rows {
		out = append(out, &pb.ChapterRef{
			ChapterId:       r.ChapterID,
			Mode:            r.Mode,
			IntervalSeconds: r.IntervalSeconds.Int32,
			StartMs:         r.StartMs,
			EndMs:           r.EndMs,
			IsCurrent:       r.IsCurrent,
			ManifestS3Key:   r.ManifestS3Key.String,
			HasGaps:         r.HasGaps,
			SegmentCount:    r.SegmentCount,
		})
	}
	return &pb.ListDVRChaptersResponse{
		Chapters:      out,
		NextPageToken: nextToken,
	}, nil
}

// SetDVRChapterPolicy updates the artifact's default chapter mode.
func (s *FoghornGRPCServer) SetDVRChapterPolicy(ctx context.Context, req *pb.SetDVRChapterPolicyRequest) (*pb.SetDVRChapterPolicyResponse, error) {
	if req.GetDvrArtifactId() == "" {
		return nil, status.Error(codes.InvalidArgument, "dvr_artifact_id is required")
	}
	mode := req.GetMode()
	switch mode {
	case "", control.ChapterModeWindowSized, control.ChapterModeFixedInterval:
		// valid
	default:
		return nil, status.Errorf(codes.InvalidArgument, "invalid mode: %q", mode)
	}
	if mode == control.ChapterModeFixedInterval && req.GetIntervalSeconds() <= 0 {
		return nil, status.Error(codes.InvalidArgument, "interval_seconds is required for fixed_interval mode")
	}
	if mode == control.ChapterModeFixedInterval && req.GetIntervalSeconds() < minAutomaticChapterIntervalSeconds {
		return nil, status.Errorf(codes.InvalidArgument, "interval_seconds must be at least %d for fixed_interval mode", minAutomaticChapterIntervalSeconds)
	}
	if err := s.assertChapterTenant(ctx, req.GetDvrArtifactId(), req.GetTenantId()); err != nil {
		return nil, err
	}
	maxRangeMs, err := control.DVRChapterMaxRangeMs(ctx, s.db, req.GetDvrArtifactId(), "")
	if err != nil {
		return nil, status.Error(codes.NotFound, "DVR artifact not found")
	}
	if mode == control.ChapterModeFixedInterval && int64(req.GetIntervalSeconds())*1000 > maxRangeMs {
		return nil, status.Errorf(codes.InvalidArgument, "interval_seconds exceeds maximum %d", maxRangeMs/1000)
	}

	err = control.WithDVRChapterMutationLock(ctx, req.GetDvrArtifactId(), func() error {
		if cErr := control.FinalizeCurrentChapter(ctx, req.GetDvrArtifactId(), s.logger); cErr != nil {
			s.logger.WithError(cErr).WithField("artifact_hash", req.GetDvrArtifactId()).Error("SetDVRChapterPolicy: close current chapter failed")
			return status.Error(codes.Internal, "failed to close current chapter")
		}

		var intervalArg interface{}
		if req.GetIntervalSeconds() > 0 {
			intervalArg = req.GetIntervalSeconds()
		}
		var modeArg interface{}
		if mode != "" {
			modeArg = mode
		}
		if _, updateErr := s.db.ExecContext(ctx, `
			UPDATE foghorn.artifacts
			   SET dvr_chapter_mode     = $2::text,
			       dvr_chapter_interval = $3::int,
			       updated_at           = NOW()
			 WHERE artifact_hash = $1 AND artifact_type = 'dvr'
		`, req.GetDvrArtifactId(), modeArg, intervalArg); updateErr != nil {
			s.logger.WithError(updateErr).Error("SetDVRChapterPolicy: failed to update artifact policy")
			return status.Error(codes.Internal, "failed to update chapter policy")
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &pb.SetDVRChapterPolicyResponse{Success: true, Message: "chapter policy updated"}, nil
}

// assertChapterTenant verifies the tenant_id on the request matches the
// artifact's owner. Empty tenant_id on the request is allowed for internal
// callers (federation / sweeper); external HTTP exposure should always
// pass a tenant_id (api_gateway enforces).
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
