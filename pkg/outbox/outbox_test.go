package outbox

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/lib/pq"
	"github.com/sirupsen/logrus"
)

func TestComputeBackoffDoubles(t *testing.T) {
	cfg := Config{BaseBackoff: 2 * time.Second, MaxBackoff: 1 * time.Hour}
	cases := []struct {
		attempts int
		want     time.Duration
	}{
		{0, 2 * time.Second},
		{1, 4 * time.Second},
		{2, 8 * time.Second},
		{3, 16 * time.Second},
	}
	for _, tc := range cases {
		if got := ComputeBackoff(cfg, tc.attempts); got != tc.want {
			t.Errorf("attempts=%d: got %v, want %v", tc.attempts, got, tc.want)
		}
	}
}

func TestComputeBackoffCapsAtMax(t *testing.T) {
	cfg := Config{BaseBackoff: 2 * time.Second, MaxBackoff: 1 * time.Hour}
	if got := ComputeBackoff(cfg, 20); got != cfg.MaxBackoff {
		t.Errorf("got %v, want %v", got, cfg.MaxBackoff)
	}
}

func TestComputeBackoffOverflowClampsToMax(t *testing.T) {
	cfg := Config{BaseBackoff: 2 * time.Second, MaxBackoff: 1 * time.Hour}
	// Large attempts make the shift overflow to a non-positive duration; the
	// helper must clamp to MaxBackoff rather than scheduling retries at 0.
	if got := ComputeBackoff(cfg, 100); got != cfg.MaxBackoff {
		t.Errorf("got %v, want %v", got, cfg.MaxBackoff)
	}
}

func TestComputeBackoffNegativeAttemptsTreatedAsZero(t *testing.T) {
	cfg := Config{BaseBackoff: 2 * time.Second, MaxBackoff: 1 * time.Hour}
	if got := ComputeBackoff(cfg, -5); got != cfg.BaseBackoff {
		t.Errorf("got %v, want %v", got, cfg.BaseBackoff)
	}
}

func TestComputeBackoffNonPositiveBaseReturnsMax(t *testing.T) {
	// BaseBackoff <= 0 short-circuits to MaxBackoff regardless of attempts.
	for _, base := range []time.Duration{0, -1 * time.Second} {
		cfg := Config{BaseBackoff: base, MaxBackoff: 1 * time.Hour}
		if got := ComputeBackoff(cfg, 3); got != cfg.MaxBackoff {
			t.Errorf("base=%v: got %v, want %v", base, got, cfg.MaxBackoff)
		}
	}
}

// captureHook records emitted log entries so tests can assert on the worker's
// alert/warn behaviour (output is discarded; hooks fire regardless).
type captureHook struct{ entries []*logrus.Entry }

func (h *captureHook) Levels() []logrus.Level { return logrus.AllLevels }
func (h *captureHook) Fire(e *logrus.Entry) error {
	h.entries = append(h.entries, e)
	return nil
}
func (h *captureHook) count(level logrus.Level, substr string) int {
	n := 0
	for _, e := range h.entries {
		if e.Level == level && strings.Contains(e.Message, substr) {
			n++
		}
	}
	return n
}
func (h *captureHook) firstError(substr string) *logrus.Entry {
	for _, e := range h.entries {
		if e.Level == logrus.ErrorLevel && strings.Contains(e.Message, substr) {
			return e
		}
	}
	return nil
}

func newCapturingWorker(store Store[string], disp Dispatcher[string], alertAfter int) (*Worker[string], *captureHook) {
	logger := logrus.New()
	logger.Out = io.Discard
	hook := &captureHook{}
	logger.AddHook(hook)
	return &Worker[string]{
		Config: Config{
			BaseBackoff:        2 * time.Second,
			MaxBackoff:         1 * time.Hour,
			BatchSize:          16,
			PollPeriod:         30 * time.Second,
			Lease:              60 * time.Second,
			AlertAfterAttempts: alertAfter,
		},
		Store:      store,
		Dispatcher: disp,
		Logger:     logger,
		AlertLabel: "test outbox",
	}, hook
}

const alertMsg = "failing for many attempts"

func TestRecordFailureAlertsAtThreshold(t *testing.T) {
	// AlertAfterAttempts=3, nextAttempts = currentAttempts+1.
	// currentAttempts=2 → nextAttempts=3 → alert fires with attempts=3.
	w, hook := newCapturingWorker(&fakeStore{}, &fakeDispatcher{err: errors.New("boom")}, 3)
	w.TryDispatch(context.Background(), "o1", 2, "p1")

	if n := hook.count(logrus.ErrorLevel, alertMsg); n != 1 {
		t.Fatalf("expected exactly 1 alert at threshold, got %d", n)
	}
	e := hook.firstError(alertMsg)
	if e == nil {
		t.Fatal("alert entry not captured")
	}
	if got := e.Data["attempts"]; got != 3 {
		t.Errorf("alert attempts field: got %v, want 3", got)
	}
	// cause is carried into the alert so on-call sees why it is failing.
	if got := e.Data["cause"]; got != "boom" {
		t.Errorf("alert cause field: got %v, want %q", got, "boom")
	}
	// The configured AlertLabel prefixes the message for alert routing.
	if !strings.Contains(e.Message, "test outbox") {
		t.Errorf("alert message should carry AlertLabel, got %q", e.Message)
	}
}

func TestRecordFailureAlertDefaultsLabelWhenEmpty(t *testing.T) {
	// Empty AlertLabel falls back to the "outbox" prefix.
	w, hook := newCapturingWorker(&fakeStore{}, &fakeDispatcher{err: errors.New("boom")}, 1)
	w.AlertLabel = ""
	w.TryDispatch(context.Background(), "o1", 0, "p1")

	e := hook.firstError(alertMsg)
	if e == nil {
		t.Fatal("alert entry not captured")
	}
	if !strings.HasPrefix(e.Message, "outbox ") {
		t.Errorf("empty label should default to %q prefix, got %q", "outbox", e.Message)
	}
}

func TestRecordFailureNoAlertBelowThreshold(t *testing.T) {
	// currentAttempts=1 → nextAttempts=2 < AlertAfterAttempts=3 → no alert.
	w, hook := newCapturingWorker(&fakeStore{}, &fakeDispatcher{err: errors.New("boom")}, 3)
	w.TryDispatch(context.Background(), "o1", 1, "p1")

	if n := hook.count(logrus.ErrorLevel, alertMsg); n != 0 {
		t.Fatalf("expected no alert below threshold, got %d", n)
	}
}

func TestRecordFailureNoAlertWhenDisabled(t *testing.T) {
	// AlertAfterAttempts=0 disables alerting even at high attempt counts.
	w, hook := newCapturingWorker(&fakeStore{}, &fakeDispatcher{err: errors.New("boom")}, 0)
	w.TryDispatch(context.Background(), "o1", 100, "p1")

	if n := hook.count(logrus.ErrorLevel, alertMsg); n != 0 {
		t.Fatalf("alert must not fire when AlertAfterAttempts=0, got %d", n)
	}
}

func TestProcessBatchLogsWarnOnClaimError(t *testing.T) {
	w, hook := newCapturingWorker(&fakeStore{claimErr: errors.New("db down")}, &fakeDispatcher{}, 12)
	w.ProcessBatch(context.Background())

	if n := hook.count(logrus.WarnLevel, "claim outbox batch failed"); n != 1 {
		t.Fatalf("expected claim-failure warn, got %d", n)
	}
}

func TestProcessBatchLogsWarnOnCompletionError(t *testing.T) {
	store := &fakeStore{
		claims:       []Claim[string]{{ID: "o1", Payload: "p1"}},
		completedErr: errors.New("disk full"),
	}
	w, hook := newCapturingWorker(store, &fakeDispatcher{}, 12)
	w.ProcessBatch(context.Background())

	if n := hook.count(logrus.WarnLevel, "mark outbox completed failed"); n != 1 {
		t.Fatalf("expected completion-failure warn, got %d", n)
	}
}

func TestTryDispatchLogsWarnOnCompletionError(t *testing.T) {
	w, hook := newCapturingWorker(&fakeStore{completedErr: errors.New("disk full")}, &fakeDispatcher{}, 12)
	w.TryDispatch(context.Background(), "o1", 0, "p1")

	if n := hook.count(logrus.WarnLevel, "mark outbox completed failed"); n != 1 {
		t.Fatalf("expected completion-failure warn, got %d", n)
	}
}

type fakeStore struct {
	claims        []Claim[string]
	claimErr      error
	claimErrs     []error
	claimCalls    int
	completed     []string
	completedErr  error
	completedErrs []error
	completeCalls int
	failures      []failureCall
	failureErr    error
	failureErrs   []error
	failureCalls  int
}

type failureCall struct {
	id            string
	attempts      int
	failedTargets []string
	cause         error
	backoff       time.Duration
}

func (s *fakeStore) ClaimBatch(_ context.Context, _ int, _ time.Duration) ([]Claim[string], error) {
	s.claimCalls++
	if len(s.claimErrs) > 0 {
		err := s.claimErrs[0]
		s.claimErrs = s.claimErrs[1:]
		if err != nil {
			return nil, err
		}
	}
	return s.claims, s.claimErr
}

func (s *fakeStore) MarkCompleted(_ context.Context, id string) error {
	s.completeCalls++
	if len(s.completedErrs) > 0 {
		err := s.completedErrs[0]
		s.completedErrs = s.completedErrs[1:]
		if err != nil {
			return err
		}
	}
	if s.completedErr != nil {
		return s.completedErr
	}
	s.completed = append(s.completed, id)
	return nil
}

func (s *fakeStore) RecordFailure(_ context.Context, id string, attempts int, failedTargets []string, cause error, backoff time.Duration) error {
	s.failureCalls++
	if len(s.failureErrs) > 0 {
		err := s.failureErrs[0]
		s.failureErrs = s.failureErrs[1:]
		if err != nil {
			return err
		}
	}
	if s.failureErr != nil {
		return s.failureErr
	}
	s.failures = append(s.failures, failureCall{id, attempts, failedTargets, cause, backoff})
	return nil
}

type fakeDispatcher struct {
	dispatched []string
	failed     []string
	err        error
}

func (d *fakeDispatcher) Dispatch(_ context.Context, payload string) ([]string, error) {
	d.dispatched = append(d.dispatched, payload)
	return d.failed, d.err
}

func newTestWorker(store Store[string], disp Dispatcher[string]) *Worker[string] {
	logger := logrus.New()
	logger.Out = io.Discard
	return &Worker[string]{
		Config: Config{
			BaseBackoff:        2 * time.Second,
			MaxBackoff:         1 * time.Hour,
			BatchSize:          16,
			PollPeriod:         30 * time.Second,
			Lease:              60 * time.Second,
			AlertAfterAttempts: 12,
		},
		Store:      store,
		Dispatcher: disp,
		Logger:     logger,
		AlertLabel: "test outbox",
	}
}

func TestProcessBatchMarksCompletedOnSuccess(t *testing.T) {
	store := &fakeStore{claims: []Claim[string]{{ID: "o1", Attempts: 0, Payload: "p1"}}}
	disp := &fakeDispatcher{}
	w := newTestWorker(store, disp)

	w.ProcessBatch(context.Background())

	if len(disp.dispatched) != 1 || disp.dispatched[0] != "p1" {
		t.Fatalf("dispatcher not invoked once: %+v", disp.dispatched)
	}
	if len(store.completed) != 1 || store.completed[0] != "o1" {
		t.Fatalf("completed not recorded: %+v", store.completed)
	}
	if len(store.failures) != 0 {
		t.Fatalf("unexpected failures: %+v", store.failures)
	}
}

func TestProcessBatchRecordsFailureOnDispatchError(t *testing.T) {
	store := &fakeStore{claims: []Claim[string]{{ID: "o1", Attempts: 3, Payload: "p1"}}}
	disp := &fakeDispatcher{err: errors.New("boom")}
	w := newTestWorker(store, disp)

	w.ProcessBatch(context.Background())

	if len(store.failures) != 1 {
		t.Fatalf("expected one failure, got %+v", store.failures)
	}
	f := store.failures[0]
	if f.id != "o1" || f.attempts != 3 || f.cause == nil {
		t.Fatalf("unexpected failure call: %+v", f)
	}
	// attempts=3 → backoff = 2s << 3 = 16s.
	if f.backoff != 16*time.Second {
		t.Errorf("backoff: got %v, want %v", f.backoff, 16*time.Second)
	}
}

func TestProcessBatchRecordsFailureOnPartialFanout(t *testing.T) {
	store := &fakeStore{claims: []Claim[string]{{ID: "o1", Attempts: 0, Payload: "p1"}}}
	disp := &fakeDispatcher{failed: []string{"cluster-a"}}
	w := newTestWorker(store, disp)

	w.ProcessBatch(context.Background())

	if len(store.failures) != 1 {
		t.Fatalf("expected one failure, got %+v", store.failures)
	}
	if store.failures[0].failedTargets[0] != "cluster-a" {
		t.Errorf("failed targets: %+v", store.failures[0].failedTargets)
	}
	if len(store.completed) != 0 {
		t.Errorf("should not mark completed on partial fanout: %+v", store.completed)
	}
}

func TestTryDispatchSuccess(t *testing.T) {
	store := &fakeStore{}
	disp := &fakeDispatcher{}
	w := newTestWorker(store, disp)

	w.TryDispatch(context.Background(), "o1", 0, "p1")

	if len(store.completed) != 1 {
		t.Fatalf("expected completed, got %+v", store.completed)
	}
}

func TestTryDispatchFailure(t *testing.T) {
	store := &fakeStore{}
	disp := &fakeDispatcher{err: errors.New("boom")}
	w := newTestWorker(store, disp)

	w.TryDispatch(context.Background(), "o1", 0, "p1")

	if len(store.failures) != 1 {
		t.Fatalf("expected failure, got %+v", store.failures)
	}
	if store.failures[0].attempts != 0 {
		t.Errorf("attempts: got %d, want 0", store.failures[0].attempts)
	}
}

func TestTryDispatchEmptyIDNoOp(t *testing.T) {
	store := &fakeStore{}
	disp := &fakeDispatcher{}
	w := newTestWorker(store, disp)

	w.TryDispatch(context.Background(), "", 0, "p1")

	if len(disp.dispatched) != 0 {
		t.Errorf("dispatcher should not be called with empty id")
	}
	if len(store.completed) != 0 || len(store.failures) != 0 {
		t.Errorf("store should not be touched with empty id")
	}
}

func TestProcessBatchSwallowsClaimError(t *testing.T) {
	store := &fakeStore{claimErr: errors.New("db down")}
	disp := &fakeDispatcher{}
	w := newTestWorker(store, disp)

	// Should not panic; should log and return.
	w.ProcessBatch(context.Background())

	if len(disp.dispatched) != 0 {
		t.Errorf("dispatcher should not be invoked on claim error")
	}
}

func TestProcessBatchRetriesRetryableClaimError(t *testing.T) {
	store := &fakeStore{
		claims:    []Claim[string]{{ID: "o1", Attempts: 0, Payload: "p1"}},
		claimErrs: []error{&pq.Error{Code: "40001", Message: "schema version mismatch for table x: expected 89, got 88"}, nil},
	}
	disp := &fakeDispatcher{}
	w := newTestWorker(store, disp)

	w.ProcessBatch(context.Background())

	if store.claimCalls != 2 {
		t.Fatalf("claim calls = %d, want 2", store.claimCalls)
	}
	if len(disp.dispatched) != 1 || disp.dispatched[0] != "p1" {
		t.Fatalf("dispatcher not invoked after retry: %+v", disp.dispatched)
	}
}

func TestTryDispatchRetriesRetryableCompletionError(t *testing.T) {
	store := &fakeStore{
		completedErrs: []error{&pq.Error{Code: "40001", Message: "schema version mismatch for table x: expected 89, got 88"}, nil},
	}
	disp := &fakeDispatcher{}
	w := newTestWorker(store, disp)

	w.TryDispatch(context.Background(), "o1", 0, "p1")

	if store.completeCalls != 2 {
		t.Fatalf("complete calls = %d, want 2", store.completeCalls)
	}
	if len(store.completed) != 1 || store.completed[0] != "o1" {
		t.Fatalf("completed not recorded after retry: %+v", store.completed)
	}
}
