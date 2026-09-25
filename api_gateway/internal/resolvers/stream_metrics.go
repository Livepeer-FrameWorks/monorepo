package resolvers

import periscopepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/periscope"

// ObservedStreamMetrics returns resp only when Periscope has observed the
// stream. Periscope answers a stream it has no state row for with a default
// "offline" status and no update time; StreamMetrics.updatedAt is non-null, so
// that default must surface as metrics: null rather than a field error that
// fails the whole stream (createStream, streamsConnection) for every client
// selecting it.
func ObservedStreamMetrics(resp *periscopepb.StreamStatusResponse) *periscopepb.StreamStatusResponse {
	if resp == nil || resp.GetUpdatedAt() == nil {
		return nil
	}
	return resp
}
