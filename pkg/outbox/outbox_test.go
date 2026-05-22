package outbox

import (
	"context"
	"errors"
	"io"
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
