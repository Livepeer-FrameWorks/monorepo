package jobs

import (
	"context"
	"testing"

	"frameworks/api_balancing/internal/control"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
)

func TestPublisherRecoveryFollowsCellLeadership(t *testing.T) {
	leader := false
	calls := 0
	job := NewPublisherAdmissionRecoveryJob(nil, logging.NewLogger(), func() bool { return leader })
	job.recover = func(context.Context, control.PublisherLeaseReleaser) error { calls++; return nil }
	for _, held := range []bool{false, true, false, true} {
		leader = held
		before := calls
		if err := job.reconcile(); err != nil {
			t.Fatal(err)
		}
		expected := before
		if held {
			expected++
		}
		if calls != expected {
			t.Fatalf("leader=%v recovery calls=%d, want %d; follower must not scan or forward markers", held, calls, expected)
		}
	}
}
