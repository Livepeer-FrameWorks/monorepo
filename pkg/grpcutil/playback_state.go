package grpcutil

import (
	"time"

	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/durationpb"
)

// PlaybackErrorDomain scopes the ErrorInfo reasons a viewer resolve carries
// through Commodore to the Gateway, which presents the reason as the public
// extensions.code.
const PlaybackErrorDomain = "playback.frameworks.network"

const (
	// StreamStartingReason: the publisher's source is registered but not yet
	// playable. The status carries RetryInfo with the advised resolve cadence.
	StreamStartingReason = "STREAM_STARTING"
	// StreamOfflineReason: no current publisher exists for the stream.
	StreamOfflineReason = "STREAM_OFFLINE"
)

// StreamStartingError is Unavailable with ErrorInfo STREAM_STARTING and
// RetryInfo, so callers retry at the advised delay rather than their own backoff.
func StreamStartingError(retryAfter time.Duration) error {
	st := status.New(codes.Unavailable, "stream is starting")
	withDetails, err := st.WithDetails(
		&errdetails.ErrorInfo{Domain: PlaybackErrorDomain, Reason: StreamStartingReason},
		&errdetails.RetryInfo{RetryDelay: durationpb.New(retryAfter)},
	)
	if err != nil {
		return st.Err()
	}
	return withDetails.Err()
}

// StreamOfflineError is FailedPrecondition, not Unavailable: an offline stream
// is a state to wait on, not a transient fault to retry immediately.
func StreamOfflineError() error {
	st := status.New(codes.FailedPrecondition, "stream is offline")
	withDetails, err := st.WithDetails(&errdetails.ErrorInfo{Domain: PlaybackErrorDomain, Reason: StreamOfflineReason})
	if err != nil {
		return st.Err()
	}
	return withDetails.Err()
}

// PlaybackStreamState returns the playback ErrorInfo reason of err and the
// RetryInfo delay it carries (zero when absent).
func PlaybackStreamState(err error) (reason string, retryAfter time.Duration, ok bool) {
	st, isStatus := statusFromError(err)
	if !isStatus || err == nil {
		return "", 0, false
	}
	for _, detail := range st.Details() {
		switch typed := detail.(type) {
		case *errdetails.ErrorInfo:
			if typed.GetDomain() == PlaybackErrorDomain && (typed.GetReason() == StreamStartingReason || typed.GetReason() == StreamOfflineReason) {
				reason = typed.GetReason()
			}
		case *errdetails.RetryInfo:
			if delay := typed.GetRetryDelay(); delay != nil && delay.IsValid() {
				retryAfter = delay.AsDuration()
			}
		}
	}
	return reason, retryAfter, reason != ""
}
