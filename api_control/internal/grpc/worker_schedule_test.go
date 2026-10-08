package grpc

import (
	"testing"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/periodic"
)

func TestMediaAuthorityQueueObservationIsSpreadAndRunsOncePerInterval(t *testing.T) {
	start := time.Now()
	firstRuns := map[time.Duration]bool{}
	for range 50 {
		server := &CommodoreServer{}
		if server.mediaAuthorityQueueObservationDue(start) {
			t.Fatal("the first call must only schedule the observation")
		}
		var first time.Time
		for at := start; at.Before(start.Add(mediaAuthorityQueueObserveInterval + time.Second)); at = at.Add(time.Second) {
			if server.mediaAuthorityQueueObservationDue(at) {
				first = at
				break
			}
		}
		if first.IsZero() {
			t.Fatal("observation never became due within its first interval")
		}
		firstRuns[first.Sub(start)] = true
		if server.mediaAuthorityQueueObservationDue(first) {
			t.Fatal("observation ran twice at the same moment")
		}
		earliest := first.Add(time.Duration(float64(mediaAuthorityQueueObserveInterval) * (1 - periodic.DefaultJitter)))
		if server.mediaAuthorityQueueObservationDue(earliest.Add(-time.Second)) {
			t.Fatal("observation ran again before its jittered interval")
		}
		latest := first.Add(time.Duration(float64(mediaAuthorityQueueObserveInterval)*(1+periodic.DefaultJitter)) + time.Second)
		if !server.mediaAuthorityQueueObservationDue(latest) {
			t.Fatal("observation did not run again after its jittered interval")
		}
	}
	if len(firstRuns) < 10 {
		t.Fatalf("replicas started together observe in only %d distinct seconds", len(firstRuns))
	}
}
