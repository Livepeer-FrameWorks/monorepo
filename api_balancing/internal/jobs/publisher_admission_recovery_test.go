package jobs

import (
	"context"
	"errors"
	"testing"
	"time"

	"frameworks/api_balancing/internal/control"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
)

func TestPublisherRecoveryBacksOffRepeatedFailure(t *testing.T) {
	calls := 0
	job := NewPublisherAdmissionRecoveryJob(nil, logging.NewLogger(), func() bool { return true })
	job.recover = func(context.Context, control.PublisherLeaseReleaser) error {
		calls++
		return errors.New("central authority unavailable")
	}
	_ = job.reconcile()
	_ = job.reconcile()
	if calls != 1 {
		t.Fatalf("failed recovery retried immediately: calls=%d, want 1 until backoff expires", calls)
	}
}

func TestPublisherRecoveryBackoffIsBoundedAndLeadershipResetsIt(t *testing.T) {
	now := time.Unix(1000, 0)
	leader, calls := true, 0
	job := NewPublisherAdmissionRecoveryJob(nil, logging.NewLogger(), func() bool { return leader })
	job.now = func() time.Time { return now }
	job.recover = func(ctx context.Context, _ control.PublisherLeaseReleaser) error {
		calls++
		deadline, ok := ctx.Deadline()
		if !ok || time.Until(deadline) > 5*time.Second {
			t.Error("recovery lost its bounded request lifetime")
		}
		return errors.New("temporary outage")
	}
	for _, want := range []time.Duration{time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second, 15 * time.Second, 15 * time.Second} {
		before := calls
		if err := job.reconcile(); err == nil || calls != before+1 {
			t.Fatalf("due recovery did not run: calls=%d err=%v", calls, err)
		}
		if job.nextAttempt.Sub(now) != want {
			t.Fatalf("retry delay=%s, want %s", job.nextAttempt.Sub(now), want)
		}
		_ = job.reconcile()
		if calls != before+1 {
			t.Fatal("backoff dispatched another recovery")
		}
		now = now.Add(want)
	}
	leader = false
	_ = job.reconcile()
	leader = true
	if err := job.reconcile(); err == nil || job.backoff != time.Second {
		t.Fatal("leadership acquisition did not reset outage backoff")
	}
	job.recover = func(context.Context, control.PublisherLeaseReleaser) error { return nil }
	now = job.nextAttempt
	if err := job.reconcile(); err != nil || !job.nextAttempt.IsZero() || job.backoff != 0 {
		t.Fatal("successful recovery retained outage scheduling")
	}
}

func TestPublisherRecoveryWarningsAreRateLimited(t *testing.T) {
	now := time.Unix(1000, 0)
	job := NewPublisherAdmissionRecoveryJob(nil, logging.NewLogger(), nil)
	job.now = func() time.Time { return now }
	if !job.shouldWarn() || job.shouldWarn() {
		t.Fatal("duplicate failures were not rate limited")
	}
	now = now.Add(time.Minute)
	if !job.shouldWarn() {
		t.Fatal("continuing outage became permanently silent")
	}
}

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
