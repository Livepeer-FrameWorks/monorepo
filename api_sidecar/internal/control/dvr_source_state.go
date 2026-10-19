package control

import (
	"context"
	"time"

	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
)

const dvrSourceStateTimeout = 3 * time.Second

// Mist's active_streams "status" values (controller_statistics.cpp) that decide a source state.
const (
	mistStreamStatusOnline       = "Online"
	mistStreamStatusShuttingDown = "Shutting down"
	mistStreamStatusOffline      = "Offline"
)

// dvrSourceActiveStreamsReader is the Mist API read the source state is taken from.
type dvrSourceActiveStreamsReader interface {
	GetActiveStreamsFilteredContext(ctx context.Context, streams []string) (map[string]interface{}, error)
}

// mistDVRSourceState reads a recording's source stream from Mist's active_streams when its
// stop is reported. Mist's buffer ends its recordings when it shuts down, so by the time a
// writer that ended with its source is reported, the buffer already reads shutting down or
// the stream is gone from the list.
func mistDVRSourceState(client dvrSourceActiveStreamsReader) func(streamName string) ipcpb.DVRSourceState {
	return func(streamName string) ipcpb.DVRSourceState {
		if streamName == "" {
			return ipcpb.DVRSourceState_DVR_SOURCE_STATE_UNSPECIFIED
		}
		ctx, cancel := context.WithTimeout(context.Background(), dvrSourceStateTimeout)
		defer cancel()
		response, err := client.GetActiveStreamsFilteredContext(ctx, []string{streamName})
		if err != nil {
			return ipcpb.DVRSourceState_DVR_SOURCE_STATE_UNSPECIFIED
		}
		return classifyDVRSourceState(response, streamName)
	}
}

// classifyDVRSourceState maps Mist's active_streams answer for one stream to the source state.
// Mist answers null when no active stream matched, so a present but empty answer means the
// stream is not active. Only Mist's definitive states decide: an online buffer is LIVE; a
// buffer that is shutting down or offline, or no active stream, is ENDED. A buffer that is
// still initializing, booting or waiting for data, and an answer without the stream's status,
// are UNSPECIFIED.
func classifyDVRSourceState(response map[string]interface{}, streamName string) ipcpb.DVRSourceState {
	raw, ok := response["active_streams"]
	if !ok {
		return ipcpb.DVRSourceState_DVR_SOURCE_STATE_UNSPECIFIED
	}
	if raw == nil {
		return ipcpb.DVRSourceState_DVR_SOURCE_STATE_ENDED
	}
	streams, ok := raw.(map[string]interface{})
	if !ok {
		return ipcpb.DVRSourceState_DVR_SOURCE_STATE_UNSPECIFIED
	}
	entry, listed := streams[streamName]
	if !listed {
		return ipcpb.DVRSourceState_DVR_SOURCE_STATE_ENDED
	}
	info, ok := entry.(map[string]interface{})
	if !ok {
		return ipcpb.DVRSourceState_DVR_SOURCE_STATE_UNSPECIFIED
	}
	status, ok := info["status"].(string)
	if !ok {
		return ipcpb.DVRSourceState_DVR_SOURCE_STATE_UNSPECIFIED
	}
	switch status {
	case mistStreamStatusOnline:
		return ipcpb.DVRSourceState_DVR_SOURCE_STATE_LIVE
	case mistStreamStatusShuttingDown, mistStreamStatusOffline:
		return ipcpb.DVRSourceState_DVR_SOURCE_STATE_ENDED
	default:
		return ipcpb.DVRSourceState_DVR_SOURCE_STATE_UNSPECIFIED
	}
}
