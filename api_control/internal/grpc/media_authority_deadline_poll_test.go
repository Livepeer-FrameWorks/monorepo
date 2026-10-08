package grpc

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/periodic"
)

func TestMediaAuthorityDeadlinePollBacksOffOnEmptyAndFailedClaims(t *testing.T) {
	var poll mediaAuthorityDeadlinePoll
	now := time.Now()
	failed := mediaAuthorityDeadlineDrain{err: context.DeadlineExceeded}
	for i, step := range []struct {
		drain mediaAuthorityDeadlineDrain
		want  time.Duration
	}{
		{mediaAuthorityDeadlineDrain{}, time.Second},
		{mediaAuthorityDeadlineDrain{}, 2 * time.Second},
		{mediaAuthorityDeadlineDrain{}, 4 * time.Second},
		{mediaAuthorityDeadlineDrain{}, 4 * time.Second},
		{mediaAuthorityDeadlineDrain{claimed: 3}, time.Second},
		{failed, 2 * time.Second},
		{failed, 4 * time.Second},
		{failed, 4 * time.Second},
		{mediaAuthorityDeadlineDrain{claimed: 1}, time.Second},
		// A drain that claimed rows and then failed still backs off.
		{mediaAuthorityDeadlineDrain{claimed: 2, err: context.DeadlineExceeded}, 2 * time.Second},
	} {
		got, _ := poll.next(step.drain, now)
		if got != step.want {
			t.Fatalf("step %d (%+v): next claim in %s, want %s", i, step.drain, got, step.want)
		}
	}
	longestWait := time.Duration(float64(mediaAuthorityDeadlinePollMax) * (1 + periodic.DefaultJitter))
	if longestWait+mediaAuthorityDeadlineClaimBudget+mediaAuthorityDeadlineDeliveryTimeout >= 20*time.Second {
		t.Fatal("a delivery found by the poll could not reach its cell within the twenty seconds a short-lease renewal has")
	}
}

func TestMediaAuthorityDeadlinePollReportsFailuresOncePerInterval(t *testing.T) {
	var poll mediaAuthorityDeadlinePoll
	start := time.Now()
	failed := mediaAuthorityDeadlineDrain{err: context.DeadlineExceeded}
	for i, step := range []struct {
		drain    mediaAuthorityDeadlineDrain
		at       time.Duration
		level    mediaAuthorityDeadlineReportLevel
		failures int
	}{
		{mediaAuthorityDeadlineDrain{}, 0, mediaAuthorityDeadlineReportNone, 0},
		{failed, time.Second, mediaAuthorityDeadlineReportFailure, 1},
		{failed, 3 * time.Second, mediaAuthorityDeadlineReportRepeat, 2},
		{failed, 7 * time.Second, mediaAuthorityDeadlineReportRepeat, 3},
		{failed, 61 * time.Second, mediaAuthorityDeadlineReportFailure, 4},
		{failed, 65 * time.Second, mediaAuthorityDeadlineReportRepeat, 5},
		{mediaAuthorityDeadlineDrain{}, 69 * time.Second, mediaAuthorityDeadlineReportRecovered, 5},
		{mediaAuthorityDeadlineDrain{}, 70 * time.Second, mediaAuthorityDeadlineReportNone, 0},
		{failed, 71 * time.Second, mediaAuthorityDeadlineReportFailure, 1},
	} {
		_, report := poll.next(step.drain, start.Add(step.at))
		if report.level != step.level || report.failures != step.failures {
			t.Fatalf("step %d: report %+v, want level %d after %d failures", i, report, step.level, step.failures)
		}
		if step.level == mediaAuthorityDeadlineReportRecovered && report.failingFor != 68*time.Second {
			t.Fatalf("recovery reported failing for %s, want 68s", report.failingFor)
		}
	}
}

func TestMediaAuthorityDeadlineClaimStopsAtItsBudget(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	mock.ExpectQuery("ClaimMediaAuthorityDeadlineDelivery").WillDelayFor(time.Minute).WillReturnRows(sqlmock.NewRows(nil))
	server := &CommodoreServer{db: db}
	started := time.Now()
	_, err = server.claimMediaAuthorityDeadlineDeliveries(context.Background(), mediaAuthorityDeliveryWorkers)
	if elapsed := time.Since(started); elapsed > mediaAuthorityDeadlineClaimBudget+time.Second {
		t.Fatalf("claim ran %s, past its %s budget", elapsed, mediaAuthorityDeadlineClaimBudget)
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("claim stopped by its budget returned %v, want it to report the exceeded budget", err)
	}

	// A claim ended by shutdown is not a budget failure.
	mock.ExpectQuery("ClaimMediaAuthorityDeadlineDelivery").WillDelayFor(time.Minute).WillReturnRows(sqlmock.NewRows(nil))
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(50*time.Millisecond, cancel)
	if _, err := server.claimMediaAuthorityDeadlineDeliveries(ctx, mediaAuthorityDeliveryWorkers); err == nil || errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("cancelled claim returned %v", err)
	}
}

func TestMediaAuthorityWakeCollapsesAndFiresLater(t *testing.T) {
	var wake mediaAuthorityWake
	wake.notify()
	wake.notify()
	select {
	case <-wake.channel():
	default:
		t.Fatal("notify did not wake the worker")
	}
	select {
	case <-wake.channel():
		t.Fatal("two notifications while busy woke the worker twice")
	default:
	}
	wake.notifyAt(time.Now().Add(20 * time.Millisecond))
	select {
	case <-wake.channel():
	case <-time.After(time.Second):
		t.Fatal("a scheduled wake-up did not arrive")
	}
}
