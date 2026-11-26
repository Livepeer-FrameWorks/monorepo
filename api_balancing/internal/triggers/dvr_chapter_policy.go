package triggers

import (
	sharedpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/shared"
)

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

type managedDVRContext interface {
	GetUserId() string
	GetStreamId() string
	GetTenantId() string
	GetInternalName() string
	GetOriginClusterId() string
	GetDvrPolicy() *sharedpb.DVRPolicy
	GetDvrProcessesJson() string
	GetDvrChapterMode() string
	GetDvrChapterIntervalSeconds() int32
}

func managedStreamDVRRequest(stream managedDVRContext) *sharedpb.StartDVRRequest {
	userID, streamID := stream.GetUserId(), stream.GetStreamId()
	req := &sharedpb.StartDVRRequest{TenantId: stream.GetTenantId(), StreamId: &streamID, InternalName: stream.GetInternalName(), UserId: &userID, ClusterId: stream.GetOriginClusterId(), DvrPolicy: stream.GetDvrPolicy(), ProcessesJson: stream.GetDvrProcessesJson()}
	applyStreamDVRChapterPolicy(req, stream.GetDvrChapterMode(), stream.GetDvrChapterIntervalSeconds())
	return req
}
