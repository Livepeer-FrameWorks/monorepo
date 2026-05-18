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

// DoRetrieveDVRChapter returns chapter row metadata via Commodore.
// Chapter playback addresses the chapter's VOD artifact directly via
// its playback_artifact_hash; this RPC no longer exposes manifest URLs.
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
			ChapterID:            "demo_chapter",
			State:                model.DVRChapterStateOpen,
			IsCurrent:            true,
			HasGaps:              false,
			SegmentCount:         0,
			WallClockStartUnixMs: float64(startMs),
			WallClockEndUnixMs:   float64(endMs),
			PlayableNow:          false,
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
	state := chapterStateFromString(resp.GetState())
	wallClockStartMs := resp.GetStartMs()
	if actual := resp.GetActualMediaStartMs(); actual > 0 {
		wallClockStartMs = actual
	}
	wallClockEndMs := resp.GetEndMs()
	if actual := resp.GetActualMediaEndMs(); actual > wallClockStartMs {
		wallClockEndMs = actual
	}
	out := &model.DVRChapter{
		ChapterID:            resp.GetChapterId(),
		State:                state,
		IsCurrent:            resp.GetIsCurrent(),
		HasGaps:              resp.GetHasGaps(),
		SegmentCount:         int(resp.GetSegmentCount()),
		WallClockStartUnixMs: float64(wallClockStartMs),
		WallClockEndUnixMs:   float64(wallClockEndMs),
		PlayableNow:          chapterPlayableNow(state),
	}
	// playbackId returns the Commodore-minted public playback ID for
	// the chapter artifact. The chapter dispatcher only advances past
	// 'closed' when the mint succeeds, so any chapter in a playable
	// state (>= finalized) has a non-empty playback_id.
	if pid := resp.GetPlaybackId(); pid != "" {
		out.PlaybackID = &pid
	}
	if reason := resp.GetLastFailureReason(); reason != "" {
		out.LastFailureReason = &reason
	}
	return out, nil
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
		ref := &model.DVRChapterRef{
			ChapterID:       c.GetChapterId(),
			Mode:            chapterModeFromString(c.GetMode()),
			IntervalSeconds: iv,
			StartMs:         float64(c.GetStartMs()),
			EndMs:           float64(c.GetEndMs()),
			IsCurrent:       c.GetIsCurrent(),
			State:           chapterStateFromString(c.GetState()),
			HasGaps:         c.GetHasGaps(),
			SegmentCount:    int(c.GetSegmentCount()),
		}
		// Commodore-minted public playback ID; populated for any
		// chapter that has been dispatched for finalization.
		if pid := c.GetPlaybackId(); pid != "" {
			ref.PlaybackID = &pid
		}
		if reason := c.GetLastFailureReason(); reason != "" {
			ref.LastFailureReason = &reason
		}
		chapters = append(chapters, ref)
	}
	out := &model.DVRChaptersPage{Chapters: chapters}
	if next := resp.GetNextPageToken(); next != "" {
		out.NextPageToken = &next
	}
	return out, nil
}

func chapterModeToString(m model.DVRChapterMode) string {
	switch m {
	case model.DVRChapterModeWindowSized:
		return "window_sized_chapters"
	case model.DVRChapterModeFixedInterval:
		return "fixed_interval"
	case model.DVRChapterModeNone:
		return ""
	default:
		return ""
	}
}

func chapterStateFromString(s string) model.DVRChapterState {
	switch s {
	case "open":
		return model.DVRChapterStateOpen
	case "closed":
		return model.DVRChapterStateClosed
	case "finalizing":
		return model.DVRChapterStateFinalizing
	case "finalized":
		return model.DVRChapterStateFinalized
	case "frozen":
		return model.DVRChapterStateFrozen
	case "reclaimed":
		return model.DVRChapterStateReclaimed
	case "failed_source_missing":
		return model.DVRChapterStateFailedSourceMissing
	case "failed_permanent":
		return model.DVRChapterStateFailedPermanent
	default:
		return model.DVRChapterStateOpen
	}
}

func chapterPlayableNow(s model.DVRChapterState) bool {
	switch s {
	case model.DVRChapterStateFinalized, model.DVRChapterStateFrozen, model.DVRChapterStateReclaimed:
		return true
	default:
		return false
	}
}

func chapterModeFromString(s string) model.DVRChapterMode {
	return ChapterModeFromString(s)
}

// ChapterModeFromString is the exported variant used by schema-level
// resolvers (Stream.dvrChapterMode) that live in api_gateway/graph.
func ChapterModeFromString(s string) model.DVRChapterMode {
	switch s {
	case "window_sized_chapters":
		return model.DVRChapterModeWindowSized
	case "fixed_interval":
		return model.DVRChapterModeFixedInterval
	default:
		return model.DVRChapterModeNone
	}
}
