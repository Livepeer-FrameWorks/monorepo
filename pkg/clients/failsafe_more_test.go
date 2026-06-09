package clients

import (
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/failsafe-go/failsafe-go"
	"github.com/sirupsen/logrus"
)

func TestNormalizeHTTPExecutorConfig_AppliesDefaults(t *testing.T) {
	got := normalizeHTTPExecutorConfig(HTTPExecutorConfig{MaxRetries: -5, BaseDelay: 0, MaxDelay: 0})

	if got.MaxRetries != 0 {
		t.Errorf("negative MaxRetries should clamp to 0, got %d", got.MaxRetries)
	}
	if got.BaseDelay != 100*time.Millisecond {
		t.Errorf("zero BaseDelay should default to 100ms, got %v", got.BaseDelay)
	}
	if got.MaxDelay != 5*time.Second {
		t.Errorf("zero MaxDelay should default to 5s, got %v", got.MaxDelay)
	}
	if got.ShouldRetry == nil {
		t.Error("nil ShouldRetry should default to a non-nil predicate")
	}
}

func TestNormalizeHTTPExecutorConfig_PreservesValidValues(t *testing.T) {
	custom := func(*http.Response, error) bool { return true }
	got := normalizeHTTPExecutorConfig(HTTPExecutorConfig{
		MaxRetries:  4,
		BaseDelay:   50 * time.Millisecond,
		MaxDelay:    2 * time.Second,
		ShouldRetry: custom,
	})

	if got.MaxRetries != 4 {
		t.Errorf("valid MaxRetries mutated: got %d, want 4", got.MaxRetries)
	}
	if got.BaseDelay != 50*time.Millisecond {
		t.Errorf("valid BaseDelay mutated: got %v, want 50ms", got.BaseDelay)
	}
	if got.MaxDelay != 2*time.Second {
		t.Errorf("valid MaxDelay mutated: got %v, want 2s", got.MaxDelay)
	}
	if got.ShouldRetry == nil {
		t.Error("provided ShouldRetry was dropped")
	}
}

func TestNormalizeHTTPExecutorConfig_ClampsMaxDelayBelowBase(t *testing.T) {
	got := normalizeHTTPExecutorConfig(HTTPExecutorConfig{
		BaseDelay: 200 * time.Millisecond,
		MaxDelay:  100 * time.Millisecond,
	})
	if got.MaxDelay != 200*time.Millisecond {
		t.Errorf("MaxDelay below BaseDelay should clamp up to BaseDelay (200ms), got %v", got.MaxDelay)
	}
}

//nolint:bodyclose // test responses have no body
func TestNewHTTPRetryPolicy_HonorsShouldRetryFalse(t *testing.T) {
	// ShouldRetry returning false must suppress retries; without the HandleIf
	// wiring the policy falls back to default (retry-everything) behaviour.
	cfg := HTTPExecutorConfig{
		MaxRetries:  3,
		BaseDelay:   time.Millisecond,
		MaxDelay:    time.Millisecond,
		ShouldRetry: func(*http.Response, error) bool { return false },
	}
	policy := NewHTTPRetryPolicy(cfg)

	var attempts int32
	_, _ = failsafe.With(policy).Get(func() (*http.Response, error) {
		atomic.AddInt32(&attempts, 1)
		return nil, errors.New("boom")
	})
	if got := atomic.LoadInt32(&attempts); got != 1 {
		t.Fatalf("ShouldRetry=false must prevent retries, got %d attempts", got)
	}
}

func TestNewCircuitBreaker_FiresOnStateChangeWhenOpening(t *testing.T) {
	var transitions int32
	cb := NewCircuitBreaker(CircuitBreakerConfig{
		Name:         "test-cb",
		MinRequests:  2,
		FailureRatio: 1.0,
		MaxRequests:  1,
		Timeout:      time.Minute,
		OnStateChange: func(_ string, _, _ CircuitBreakerState) {
			atomic.AddInt32(&transitions, 1)
		},
	})

	for i := 0; i < 5; i++ {
		_ = cb.Call(func() error { return errors.New("fail") })
	}

	if atomic.LoadInt32(&transitions) == 0 {
		t.Fatal("expected OnStateChange callback to fire as the breaker opened")
	}
	if cb.State() != StateOpen {
		t.Errorf("expected breaker to be open after sustained failures, got %v", cb.State())
	}
}

// warnCaptureHook records Warn-level log messages for assertions.
type warnCaptureHook struct {
	mu  sync.Mutex
	msg []string
}

func (h *warnCaptureHook) Levels() []logrus.Level { return logrus.AllLevels }
func (h *warnCaptureHook) Fire(e *logrus.Entry) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if e.Level == logrus.WarnLevel {
		h.msg = append(h.msg, e.Message)
	}
	return nil
}
func (h *warnCaptureHook) has(substr string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, m := range h.msg {
		if strings.Contains(m, substr) {
			return true
		}
	}
	return false
}

func TestNewCircuitBreaker_LogsStateChangeViaLogger(t *testing.T) {
	// With a Logger but no OnStateChange callback, the state-change handler must
	// still be wired so transitions are logged.
	logger := logrus.New()
	logger.Out = io.Discard
	hook := &warnCaptureHook{}
	logger.AddHook(hook)

	cb := NewCircuitBreaker(CircuitBreakerConfig{
		Name:         "logged-cb",
		MinRequests:  2,
		FailureRatio: 1.0,
		MaxRequests:  1,
		Timeout:      time.Minute,
		Logger:       logger,
	})

	for i := 0; i < 5; i++ {
		_ = cb.Call(func() error { return errors.New("fail") })
	}

	if !hook.has("circuit breaker state change") {
		t.Fatal("expected a state-change warning to be logged when the breaker opened")
	}
}
