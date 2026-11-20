package grpc

import (
	"context"
	"database/sql"
	"errors"

	"frameworks/api_balancing/internal/control"
	"frameworks/api_balancing/internal/database/foghorndb"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	foghorncontrolpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/foghorn_control"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
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
func (s *FoghornGRPCServer) RetrieveDVRChapter(ctx context.Context, req *foghorncontrolpb.RetrieveDVRChapterRequest) (*foghorncontrolpb.RetrieveDVRChapterResponse, error) {
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
	if mode == control.ChapterModeFixedInterval && req.GetIntervalSeconds() < minAutomaticChapterIntervalSeconds {
		return nil, status.Errorf(codes.InvalidArgument, "interval_seconds must be at least %d for fixed_interval mode", minAutomaticChapterIntervalSeconds)
	}
	if err := s.assertChapterTenant(ctx, req.GetDvrArtifactId(), req.GetTenantId()); err != nil {
		return nil, err
	}
	intervalSeconds := req.GetIntervalSeconds()
	if mode == "" || mode == control.ChapterModeWindowSized {
		policy, hasPolicy, policyErr := control.ReadDVRChapterPolicy(ctx, req.GetDvrArtifactId())
		if policyErr != nil {
			return nil, status.Error(codes.Internal, "failed to read chapter policy")
		}
		if !hasPolicy && mode == "" {
			return nil, status.Error(codes.FailedPrecondition, "DVR has no chapter policy")
		}
		if hasPolicy {
			if mode == "" {
				mode = policy.Mode
			}
			intervalSeconds = policy.EffectiveIntervalSeconds()
		}
	}
	row, err := control.GetChapterAtStart(ctx, req.GetDvrArtifactId(), mode, intervalSeconds, req.GetStartMs())
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, status.Error(codes.NotFound, "chapter not found")
		}
		s.logger.WithError(err).WithField("dvr_artifact_id", req.GetDvrArtifactId()).Error("Failed to read chapter row")
		return nil, status.Error(codes.Internal, "failed to read chapter")
	}
	// The request names an exact range; a chapter whose end differs is a
	// different range.
	if row.EndMs != req.GetEndMs() {
		return nil, status.Error(codes.NotFound, "chapter not found")
	}
	resp := &foghorncontrolpb.RetrieveDVRChapterResponse{
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
func (s *FoghornGRPCServer) ListDVRChapters(ctx context.Context, req *foghorncontrolpb.ListDVRChaptersRequest) (*foghorncontrolpb.ListDVRChaptersResponse, error) {
	if req.GetDvrArtifactId() == "" {
		return nil, status.Error(codes.InvalidArgument, "dvr_artifact_id is required")
	}
	if req.GetMode() == foghorncontrolpb.ArchivedClipCoverageMode {
		if ctxkeys.GetAuthType(ctx) != "service" || req.GetTenantId() == "" {
			return nil, status.Error(codes.PermissionDenied, "archived clip lookup requires service and tenant identity")
		}
		return s.listArchivedClipCoverage(ctx, req)
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
	out := make([]*foghorncontrolpb.ChapterRef, 0, len(rows))
	for _, r := range rows {
		ref := &foghorncontrolpb.ChapterRef{
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
	return &foghorncontrolpb.ListDVRChaptersResponse{
		Chapters:      out,
		NextPageToken: nextToken,
	}, nil
}

// Query materialized media directly. Viewer listings can clamp their range to
// the start of the final segment or skip chapters under a former policy.
const findArchivedClipCoverage = `-- name: FindArchivedClipCoverage
SELECT c.chapter_id, c.state, c.playback_artifact_hash, c.actual_media_start_ms, c.actual_media_end_ms
FROM foghorn.dvr_chapters c
JOIN foghorn.artifacts a ON a.artifact_hash = c.artifact_hash
WHERE a.artifact_type = 'dvr' AND a.tenant_id = $1 AND a.artifact_hash = $2
  AND c.state IN ('finalized', 'frozen', 'reclaimed') AND NOT c.is_current
  AND c.playback_artifact_hash IS NOT NULL
  AND c.actual_media_start_ms > 0 AND c.actual_media_start_ms <= $3 AND c.actual_media_end_ms >= $4
ORDER BY c.chapter_id
LIMIT 201`

func (s *FoghornGRPCServer) listArchivedClipCoverage(ctx context.Context, req *foghorncontrolpb.ListDVRChaptersRequest) (*foghorncontrolpb.ListDVRChaptersResponse, error) {
	startMs, endMs := req.GetRangeStartMs(), req.GetRangeEndMs()
	if startMs <= 0 || endMs <= startMs {
		return nil, status.Error(codes.InvalidArgument, "archived clip lookup requires a positive wall-clock range")
	}
	rows, err := s.db.QueryContext(ctx, findArchivedClipCoverage, req.GetTenantId(), req.GetDvrArtifactId(), startMs, endMs)
	if err != nil {
		s.logger.WithError(err).Warn("Archived clip coverage query failed")
		return nil, status.Error(codes.Internal, "failed to read archived clip coverage")
	}
	defer rows.Close()
	response := &foghorncontrolpb.ListDVRChaptersResponse{}
	for rows.Next() {
		chapter := &foghorncontrolpb.ChapterRef{}
		if err := rows.Scan(&chapter.ChapterId, &chapter.State, &chapter.PlaybackArtifactHash,
			&chapter.ActualMediaStartMs, &chapter.ActualMediaEndMs); err != nil {
			return nil, status.Error(codes.Internal, "failed to read archived clip chapter")
		}
		response.Chapters = append(response.Chapters, chapter)
		if len(response.Chapters) > 200 {
			return nil, status.Error(codes.FailedPrecondition, "too many archived chapters cover the clip range")
		}
	}
	if err := rows.Err(); err != nil {
		return nil, status.Error(codes.Internal, "failed to read archived clip coverage")
	}
	grpc.SetTrailer(ctx, metadata.Pairs(foghorncontrolpb.ArchivedClipCoverageTrailer, "1"))
	return response, nil
}

// assertChapterTenant verifies the tenant_id on the request matches the
// artifact's owner. Empty tenant_id on the request is allowed for
// internal callers (federation / sweeper); external HTTP exposure
// should always pass a tenant_id (api_gateway enforces).
func (s *FoghornGRPCServer) assertChapterTenant(ctx context.Context, artifactHash, claimedTenantID string) error {
	if claimedTenantID == "" {
		return nil
	}
	tenantID, err := foghorndb.New(s.db).GetDVRArtifactTenantID(ctx, artifactHash)
	if err != nil {
		return status.Error(codes.NotFound, "DVR artifact not found")
	}
	if tenantID != claimedTenantID {
		return status.Error(codes.PermissionDenied, "tenant mismatch")
	}
	return nil
}
