package grpc

import (
	"sync/atomic"
)

// mediaAuthoritySchedule is the replica's scheduling state for the media
// authority workers. The workers start at a random point of their first
// interval and jitter every later one through pkg/periodic, so replicas started
// by the same rollout do not query the database in the same second.
type mediaAuthoritySchedule struct {
	// deadlineWake starts a short-lease delivery claim as soon as this replica
	// commits one or one of its retries falls due.
	deadlineWake   mediaAuthorityWake
	deadlineCursor mediaAuthorityDeadlineCursor
	// queueObserveWait is the jittered wait, in nanoseconds, from the last
	// queue observation to the next; zero means one interval.
	queueObserveWait atomic.Int64
}
