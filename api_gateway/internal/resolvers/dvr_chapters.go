package resolvers

import (
	"context"
	"fmt"

	"frameworks/api_gateway/graph/model"
	"frameworks/api_gateway/internal/middleware"
	pb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto"
)

// External path: api_gateway (GraphQL) → Commodore (validates tenant) →
// Foghorn (owns chapter materialization). UTC-only; civil-time chapters
// resolve at the edge.

// NormalizeDvrID validates the caller-provided DVR identifier before the
// request crosses into Commodore. Commodore accepts either DVRRequest.id
// (UUID) or DVRRequest.dvrHash and normalizes both to the Foghorn artifact
// hash after tenant validation.
func NormalizeDvrID(input string) (string, error) {
	if input == "" {
		return "", fmt.Errorf("dvr id is required")
	}
	return input, nil
}

// DoRetrieveDVRChapter returns a chapter manifest URL via Commodore.
func (r *Resolver) DoRetrieveDVRChapter(
	ctx context.Context,
	dvrHash string,
	mode model.DVRChapterMode,
	intervalSeconds int32,
	startMs, endMs int64,
) (*model.DVRChapter, error) {
	if err := middleware.RequirePermission(ctx, "streams:read"); err != nil {
		return nil, err
	}
	if middleware.IsDemoMode(ctx) {
		r.Logger.Debug("Demo: retrieve DVR chapter")
		return &model.DVRChapter{
			ChapterID:     "demo_chapter",
			ManifestS3Key: "dvr/demo/chapters/demo_chapter.m3u8",
			ManifestURL:   "https://demo.local/dvr/demo/chapters/demo_chapter.m3u8",
			IsCurrent:     false,
			HasGaps:       false,
			SegmentCount:  0,
		}, nil
	}
	req := &pb.RetrieveDVRChapterRequest{
		DvrArtifactId:   dvrHash,
		Mode:            chapterModeToString(mode),
		IntervalSeconds: intervalSeconds,
		StartMs:         startMs,
		EndMs:           endMs,
	}
	resp, err := r.Clients.Commodore.RetrieveDVRChapter(ctx, req)
	if err != nil {
		r.Logger.WithError(err).Error("RetrieveDVRChapter failed")
		return nil, fmt.Errorf("retrieve chapter: %w", err)
	}
	return &model.DVRChapter{
		ChapterID:     resp.GetChapterId(),
		ManifestS3Key: resp.GetManifestS3Key(),
		ManifestURL:   resp.GetManifestUrl(),
		IsCurrent:     resp.GetIsCurrent(),
		HasGaps:       resp.GetHasGaps(),
		SegmentCount:  int(resp.GetSegmentCount()),
	}, nil
}

// DoListDVRChapters paginates chapter rows for player navigation.
func (r *Resolver) DoListDVRChapters(
	ctx context.Context,
	dvrHash string,
	mode model.DVRChapterMode,
	intervalSeconds int32,
	rangeStartMs, rangeEndMs int64,
	pageSize int32,
	pageToken string,
) (*model.DVRChaptersPage, error) {
	if err := middleware.RequirePermission(ctx, "streams:read"); err != nil {
		return nil, err
	}
	if middleware.IsDemoMode(ctx) {
		r.Logger.Debug("Demo: list DVR chapters")
		return &model.DVRChaptersPage{Chapters: nil}, nil
	}
	req := &pb.ListDVRChaptersRequest{
		DvrArtifactId:   dvrHash,
		Mode:            chapterModeToString(mode),
		IntervalSeconds: intervalSeconds,
		RangeStartMs:    rangeStartMs,
		RangeEndMs:      rangeEndMs,
		PageSize:        pageSize,
		PageToken:       pageToken,
	}
	resp, err := r.Clients.Commodore.ListDVRChapters(ctx, req)
	if err != nil {
		r.Logger.WithError(err).Error("ListDVRChapters failed")
		return nil, fmt.Errorf("list chapters: %w", err)
	}
	chapters := make([]*model.DVRChapterRef, 0, len(resp.GetChapters()))
	for _, c := range resp.GetChapters() {
		var iv *int
		if c.GetIntervalSeconds() > 0 {
			v := int(c.GetIntervalSeconds())
			iv = &v
		}
		var manifest *string
		if c.GetManifestS3Key() != "" {
			s := c.GetManifestS3Key()
			manifest = &s
		}
		chapters = append(chapters, &model.DVRChapterRef{
			ChapterID:       c.GetChapterId(),
			Mode:            chapterModeFromString(c.GetMode()),
			IntervalSeconds: iv,
			StartMs:         float64(c.GetStartMs()),
			EndMs:           float64(c.GetEndMs()),
			IsCurrent:       c.GetIsCurrent(),
			ManifestS3Key:   manifest,
			HasGaps:         c.GetHasGaps(),
			SegmentCount:    int(c.GetSegmentCount()),
		})
	}
	out := &model.DVRChaptersPage{Chapters: chapters}
	if next := resp.GetNextPageToken(); next != "" {
		out.NextPageToken = &next
	}
	return out, nil
}

// DoSetDVRChapterPolicy updates the artifact's default chapter mode.
func (r *Resolver) DoSetDVRChapterPolicy(
	ctx context.Context,
	dvrHash string,
	mode model.DVRChapterMode,
	intervalSeconds int32,
) (*model.SetDVRChapterPolicyResult, error) {
	if err := middleware.RequirePermission(ctx, "streams:write"); err != nil {
		return nil, err
	}
	if middleware.IsDemoMode(ctx) {
		r.Logger.Debug("Demo: set DVR chapter policy")
		msg := "demo"
		return &model.SetDVRChapterPolicyResult{Success: true, Message: &msg}, nil
	}
	req := &pb.SetDVRChapterPolicyRequest{
		DvrArtifactId:   dvrHash,
		Mode:            chapterModeToString(mode),
		IntervalSeconds: intervalSeconds,
	}
	resp, err := r.Clients.Commodore.SetDVRChapterPolicy(ctx, req)
	if err != nil {
		r.Logger.WithError(err).Error("SetDVRChapterPolicy failed")
		return nil, fmt.Errorf("set chapter policy: %w", err)
	}
	out := &model.SetDVRChapterPolicyResult{Success: resp.GetSuccess()}
	if msg := resp.GetMessage(); msg != "" {
		out.Message = &msg
	}
	return out, nil
}

func chapterModeToString(m model.DVRChapterMode) string {
	switch m {
	case model.DVRChapterModeWindowSized:
		return "window_sized_chapters"
	case model.DVRChapterModeFixedInterval:
		return "fixed_interval"
	case model.DVRChapterModeExplicitRange:
		return "explicit_range"
	case model.DVRChapterModeNone:
		return ""
	default:
		return ""
	}
}

func chapterModeFromString(s string) model.DVRChapterMode {
	switch s {
	case "window_sized_chapters":
		return model.DVRChapterModeWindowSized
	case "fixed_interval":
		return model.DVRChapterModeFixedInterval
	case "explicit_range":
		return model.DVRChapterModeExplicitRange
	default:
		return model.DVRChapterModeNone
	}
}
