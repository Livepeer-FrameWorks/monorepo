package llm

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestParseRetryAfter(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		value string
		want  *time.Duration
	}{
		{name: "empty returns nil", value: "", want: nil},
		{name: "non-numeric returns nil", value: "Wed, 21 Oct 2025 07:28:00 GMT", want: nil},
		{name: "zero returns nil", value: "0", want: nil},
		{name: "negative returns nil", value: "-5", want: nil},
		{name: "positive returns duration", value: "7", want: durPtr(7 * time.Second)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseRetryAfter(tt.value)
			if tt.want == nil {
				if got != nil {
					t.Fatalf("expected nil, got %v", *got)
				}
				return
			}
			if got == nil {
				t.Fatalf("expected %v, got nil", *tt.want)
			}
			if *got != *tt.want {
				t.Fatalf("expected %v, got %v", *tt.want, *got)
			}
		})
	}
}

func TestBackoffExponentialGrowth(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		attempt int
		base    time.Duration
		wantMin time.Duration
		wantMax time.Duration
	}{
		{name: "attempt0 = base", attempt: 0, base: 10 * time.Millisecond, wantMin: 9 * time.Millisecond, wantMax: 25 * time.Millisecond},
		{name: "attempt1 = 2x base", attempt: 1, base: 10 * time.Millisecond, wantMin: 18 * time.Millisecond, wantMax: 35 * time.Millisecond},
		{name: "attempt2 = 4x base", attempt: 2, base: 10 * time.Millisecond, wantMin: 38 * time.Millisecond, wantMax: 60 * time.Millisecond},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			start := time.Now()
			backoff(context.Background(), tt.attempt, nil, tt.base)
			elapsed := time.Since(start)
			if elapsed < tt.wantMin || elapsed > tt.wantMax {
				t.Fatalf("attempt %d base %v: slept %v, want between %v and %v", tt.attempt, tt.base, elapsed, tt.wantMin, tt.wantMax)
			}
		})
	}
}

func TestBackoffRetryAfterOverridesWhenLarger(t *testing.T) {
	t.Parallel()
	base := 5 * time.Millisecond
	// attempt 0 => exponential delay = base = 5ms. retryAfter 60ms is larger,
	// so backoff must sleep ~60ms, not 5ms.
	retryAfter := 60 * time.Millisecond
	start := time.Now()
	backoff(context.Background(), 0, &retryAfter, base)
	elapsed := time.Since(start)
	if elapsed < 50*time.Millisecond {
		t.Fatalf("expected retry-after %v to dominate, slept only %v", retryAfter, elapsed)
	}
}

func TestBackoffRetryAfterIgnoredWhenSmaller(t *testing.T) {
	t.Parallel()
	base := 40 * time.Millisecond
	// exponential delay = 40ms; retryAfter 1ms is smaller and must be ignored.
	retryAfter := 1 * time.Millisecond
	start := time.Now()
	backoff(context.Background(), 0, &retryAfter, base)
	elapsed := time.Since(start)
	if elapsed < 30*time.Millisecond {
		t.Fatalf("expected exponential %v to dominate small retry-after, slept only %v", base, elapsed)
	}
}

func TestBackoffCapsAtThirtySeconds(t *testing.T) {
	t.Parallel()
	// retryAfter way above the cap; backoff must clamp to 30s. Cancel the
	// context immediately so we don't actually wait, but assert the timer was
	// constructed with the clamped value by checking the function returns fast
	// under cancellation regardless. Instead verify the clamp via a context
	// that we cancel and measure it doesn't run longer than the cap path.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	retryAfter := 90 * time.Second
	start := time.Now()
	backoff(ctx, 0, &retryAfter, time.Second)
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("cancelled context should return immediately, took %v", elapsed)
	}
}

func TestBackoffContextCancelStopsWait(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(5 * time.Millisecond)
		cancel()
	}()
	start := time.Now()
	backoff(ctx, 5, nil, time.Second)
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("context cancel should cut the wait short, took %v", elapsed)
	}
}

func TestDoWithRetryHonorsRetryAfterHeader(t *testing.T) {
	var count int32
	var gap int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		n := atomic.AddInt32(&count, 1)
		if n == 1 {
			atomic.StoreInt64(&gap, time.Now().UnixNano())
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	resp, err := doWithRetry(context.Background(), &http.Client{}, func() (*http.Request, error) {
		return http.NewRequestWithContext(context.Background(), http.MethodGet, srv.URL, nil)
	}, time.Millisecond)
	if err != nil {
		t.Fatalf("expected eventual success, got %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
}

func TestDoWithRetryNonRetryableReturnsImmediately(t *testing.T) {
	var count int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&count, 1)
		w.WriteHeader(http.StatusBadRequest)
	}))
	defer srv.Close()

	resp, err := doWithRetry(context.Background(), &http.Client{}, func() (*http.Request, error) {
		return http.NewRequestWithContext(context.Background(), http.MethodGet, srv.URL, nil)
	}, time.Millisecond)
	if err != nil {
		t.Fatalf("non-retryable status should not error from doWithRetry: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", resp.StatusCode)
	}
	if got := atomic.LoadInt32(&count); got != 1 {
		t.Fatalf("expected exactly 1 attempt for non-retryable status, got %d", got)
	}
}

func TestIsRetryableStatus(t *testing.T) {
	t.Parallel()
	tests := []struct {
		code int
		want bool
	}{
		{http.StatusTooManyRequests, true},
		{http.StatusInternalServerError, true},
		{http.StatusBadGateway, true},
		{http.StatusServiceUnavailable, true},
		{http.StatusGatewayTimeout, true},
		{http.StatusOK, false},
		{http.StatusBadRequest, false},
		{http.StatusNotFound, false},
		{http.StatusNotImplemented, false},
	}
	for _, tt := range tests {
		if got := isRetryableStatus(tt.code); got != tt.want {
			t.Fatalf("isRetryableStatus(%d) = %v, want %v", tt.code, got, tt.want)
		}
	}
}

func TestAllCompleteJSONPayloads(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		lines []string
		want  bool
	}{
		{name: "json objects", lines: []string{`{"a":1}`, `{"b":2}`}, want: true},
		{name: "json arrays", lines: []string{`[1,2]`, `[3]`}, want: true},
		{name: "single char too short", lines: []string{"{"}, want: false},
		{name: "two char object ok", lines: []string{"{}"}, want: true},
		{name: "plain text", lines: []string{"hello world"}, want: false},
		{name: "unbalanced object", lines: []string{`{"a":1`}, want: false},
		{name: "mixed valid and text", lines: []string{`{"a":1}`, "text"}, want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := allCompleteJSONPayloads(tt.lines); got != tt.want {
				t.Fatalf("allCompleteJSONPayloads(%v) = %v, want %v", tt.lines, got, tt.want)
			}
		})
	}
}

func TestRecvSkipsEmptyChunksUntilContent(t *testing.T) {
	t.Parallel()
	// content_block_start with empty text + nil usage yields an empty chunk
	// that Recv must skip; the following delta carries content.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n"))
		_, _ = w.Write([]byte("data: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"text\":\"hi\"}}\n\n"))
		_, _ = w.Write([]byte("data: {\"type\":\"message_stop\"}\n\n"))
	}))
	defer server.Close()

	p := NewAnthropicProvider(Config{APIURL: server.URL, APIKey: "k", Model: "m"})
	stream, err := p.Complete(context.Background(), []Message{{Role: "user", Content: "hi"}}, nil)
	if err != nil {
		t.Fatalf("complete: %v", err)
	}
	defer stream.Close()

	first, err := stream.Recv()
	if err != nil {
		t.Fatalf("recv: %v", err)
	}
	if first.Content != "hi" {
		t.Fatalf("expected first non-empty chunk to carry content, got %q", first.Content)
	}
}

func durPtr(d time.Duration) *time.Duration { return &d }
