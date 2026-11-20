package grpc

import (
	"context"
	"fmt"

	"frameworks/api_control/internal/database/commodoredb"
	media "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/media_authority"
)

func recordingConfigurationMatches(saved commodoredb.LockLiveStreamRecordingConfigurationRow, live *media.LiveStreamAuthority) bool {
	if live == nil {
		return false
	}
	if saved.IsRecordingEnabled != live.GetRecordingEnabled() {
		return false
	}
	// A chapter-only change must not hold up ordinary, non-recording ingest.
	if !saved.IsRecordingEnabled {
		return true
	}
	mode, interval := streamDVRChapterPolicy(saved.DvrChapterMode, saved.DvrChapterIntervalSeconds)
	return live.GetPublisherConfigurationVersion() >= 1 &&
		mode == live.GetDvrChapterMode() && interval == live.GetDvrChapterIntervalSeconds()
}

// lockLiveRecordingPublication serializes publication with the same owner row
// that setting writes and publisher acquisition lock. No owner or publication
// lock is held while external policy sources are queried.
func lockLiveRecordingPublication(ctx context.Context, q *commodoredb.Queries, payload *media.MediaObjectAuthority) error {
	live := payload.GetLiveStream()
	if live == nil || payload.GetLifecycle() != media.AuthorityLifecycle_AUTHORITY_LIFECYCLE_ACTIVE || live.GetPublisherConfigurationVersion() < 1 {
		return nil
	}
	saved, err := q.LockLiveStreamRecordingConfiguration(ctx, commodoredb.LockLiveStreamRecordingConfigurationParams{
		TenantID: payload.GetTenantId(), StreamID: live.GetStreamId(),
	})
	if err != nil {
		return fmt.Errorf("recording source unavailable before publication: %w", err)
	}
	// Publication also fences chapter changes while auto-record is disabled:
	// a later manual start must not receive an older chapter configuration.
	mode, interval := streamDVRChapterPolicy(saved.DvrChapterMode, saved.DvrChapterIntervalSeconds)
	if saved.IsRecordingEnabled != live.GetRecordingEnabled() || mode != live.GetDvrChapterMode() || interval != live.GetDvrChapterIntervalSeconds() {
		return fmt.Errorf("recording settings changed during compilation: %w", errMediaAuthorityCompileSuperseded)
	}
	return nil
}
