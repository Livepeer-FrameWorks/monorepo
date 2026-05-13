package policybundle

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

func newEntry(version int64, soft, hard time.Duration, now time.Time) Entry {
	return Entry{
		BundleJWT:     "jwt-v" + intStr(version),
		BundleVersion: version,
		IssuedAt:      now,
		SoftExpiresAt: now.Add(soft),
		ExpiresAt:     now.Add(hard),
	}
}

func intStr(n int64) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	neg := n < 0
	if neg {
		n = -n
	}
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}

func TestGetServesCachedUnderSoftTTL(t *testing.T) {
	c := New()
	now := time.Now()
	c.putEntry(cacheKey("t1", "s1"), newEntry(1, 60*time.Second, 30*time.Minute, now))

	calls := atomic.Int32{}
	fetch := func(_ context.Context, _, _ string) (Entry, error) {
		calls.Add(1)
		return Entry{}, errors.New("should not be called")
	}
	e, err := c.Get(context.Background(), "t1", "s1", fetch, now.Add(30*time.Second))
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if e.BundleVersion != 1 {
		t.Errorf("got version %d, want 1", e.BundleVersion)
	}
	if calls.Load() != 0 {
		t.Errorf("fetch called %d times under soft TTL", calls.Load())
	}
}

func TestGetTriggersBackgroundRefreshPastSoftTTL(t *testing.T) {
	c := New()
	now := time.Now()
	c.putEntry(cacheKey("t1", "s1"), newEntry(1, 60*time.Second, 30*time.Minute, now))

	fetched := make(chan struct{}, 1)
	fetch := func(_ context.Context, _, _ string) (Entry, error) {
		select {
		case fetched <- struct{}{}:
		default:
		}
		return newEntry(2, 60*time.Second, 30*time.Minute, time.Now()), nil
	}

	// Past soft TTL but within hard TTL → return cached, kick background refresh.
	e, err := c.Get(context.Background(), "t1", "s1", fetch, now.Add(2*time.Minute))
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if e.BundleVersion != 1 {
		t.Errorf("got version %d, want 1 (cached)", e.BundleVersion)
	}

	select {
	case <-fetched:
	case <-time.After(2 * time.Second):
		t.Fatal("background refresh did not fire")
	}
	// Wait a touch for the put to land.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if peeked, ok := c.Peek("t1", "s1"); ok && peeked.BundleVersion == 2 {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("background refresh did not update cache")
}

func TestGetFetchesSynchronouslyPastHardTTL(t *testing.T) {
	c := New()
	now := time.Now()
	c.putEntry(cacheKey("t1", "s1"), newEntry(1, 60*time.Second, 30*time.Minute, now))

	fetch := func(_ context.Context, _, _ string) (Entry, error) {
		return newEntry(5, 60*time.Second, 30*time.Minute, time.Now()), nil
	}

	// Past hard TTL → synchronous fetch.
	e, err := c.Get(context.Background(), "t1", "s1", fetch, now.Add(45*time.Minute))
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if e.BundleVersion != 5 {
		t.Errorf("got version %d, want 5 (synchronously fetched)", e.BundleVersion)
	}
}

func TestGetSyncFetchErrorReturnsFailClosed(t *testing.T) {
	c := New()
	fetch := func(_ context.Context, _, _ string) (Entry, error) {
		return Entry{}, errors.New("commodore down")
	}
	_, err := c.Get(context.Background(), "t1", "s1", fetch, time.Now())
	if err == nil {
		t.Fatal("expected fail-closed error, got nil")
	}
	if !errors.Is(err, ErrFetchFailed) {
		t.Errorf("expected ErrFetchFailed, got %v", err)
	}
}

func TestBumpWatermarkInvalidatesCachedEntry(t *testing.T) {
	c := New()
	now := time.Now()
	c.putEntry(cacheKey("t1", "s1"), newEntry(1, 60*time.Second, 30*time.Minute, now))

	c.BumpWatermark("t1", "s1", 5)

	if got := c.Watermark("t1", "s1"); got != 5 {
		t.Errorf("watermark: got %d, want 5", got)
	}

	// Under-watermark cached entry must force a fresh fetch.
	fetch := func(_ context.Context, _, _ string) (Entry, error) {
		return newEntry(5, 60*time.Second, 30*time.Minute, time.Now()), nil
	}
	e, err := c.Get(context.Background(), "t1", "s1", fetch, now.Add(10*time.Second))
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if e.BundleVersion != 5 {
		t.Errorf("got version %d, want 5", e.BundleVersion)
	}
}

func TestBumpWatermarkMonotonic(t *testing.T) {
	c := New()
	c.BumpWatermark("t1", "s1", 5)
	c.BumpWatermark("t1", "s1", 3) // older revoke
	if got := c.Watermark("t1", "s1"); got != 5 {
		t.Errorf("watermark should not decrease: got %d, want 5", got)
	}
}

func TestBumpWatermarkBeforeAnyEntry(t *testing.T) {
	c := New()
	c.BumpWatermark("t1", "s1", 7)
	if got := c.Watermark("t1", "s1"); got != 7 {
		t.Errorf("watermark before any entry: got %d, want 7", got)
	}
}

func TestPeekReturnsFalseWhenAbsent(t *testing.T) {
	c := New()
	if _, ok := c.Peek("nope", "nope"); ok {
		t.Error("expected no entry")
	}
}

func TestBackgroundRefreshSerializesConcurrentTriggers(t *testing.T) {
	c := New()
	now := time.Now()
	c.putEntry(cacheKey("t1", "s1"), newEntry(1, 60*time.Second, 30*time.Minute, now))

	calls := atomic.Int32{}
	block := make(chan struct{})
	fetch := func(_ context.Context, _, _ string) (Entry, error) {
		calls.Add(1)
		<-block
		return newEntry(2, 60*time.Second, 30*time.Minute, time.Now()), nil
	}

	// Fire several soft-expired Gets in quick succession.
	for range 5 {
		_, err := c.Get(context.Background(), "t1", "s1", fetch, now.Add(2*time.Minute))
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
	}
	close(block)
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if calls.Load() >= 1 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if got := calls.Load(); got > 1 {
		t.Errorf("expected serialized refresh (1 call), got %d", got)
	}
}
