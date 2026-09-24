package triggers

import sharedpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/shared"

// applyStreamDVRChapterPolicy copies the stream's chapter policy from
// Commodore's stream context onto an auto-started recording. An empty mode is
// left unset; StartDVR then records window-sized chapters.
func applyStreamDVRChapterPolicy(req *sharedpb.StartDVRRequest, mode string, intervalSeconds int32) {
	if req == nil || mode == "" {
		return
	}
	req.DvrChapterMode = &mode
	if intervalSeconds > 0 {
		req.DvrChapterIntervalSeconds = &intervalSeconds
	}
}
