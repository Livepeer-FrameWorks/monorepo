package grpcutil

import (
	controlpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/foghorn_control"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// PendingStreamStop preserves acknowledged progress while the caller retries failed nodes.
func PendingStreamStop(progress *controlpb.TerminateTenantStreamsResponse) error {
	st := status.New(codes.Unavailable, "stream termination remains pending")
	detailed, err := st.WithDetails(progress)
	if err != nil {
		return st.Err()
	}
	return detailed.Err()
}

// StreamStopProgress decodes acknowledged delivery counts from a partial failure.
func StreamStopProgress(err error) *controlpb.TerminateTenantStreamsResponse {
	for _, detail := range status.Convert(err).Details() {
		if progress, ok := detail.(*controlpb.TerminateTenantStreamsResponse); ok {
			return progress
		}
	}
	return nil
}
