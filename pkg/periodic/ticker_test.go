package periodic

import (
	"math/rand/v2"
	"testing"
	"time"
)

func TestStartOffsetStaysWithinOneInterval(t *testing.T) {
	interval := 30 * time.Second
	if got := StartOffset(interval, 0); got != 0 {
		t.Fatalf("offset at r=0 = %s, want 0", got)
	}
	for range 1000 {
		r := rand.Float64()
		got := StartOffset(interval, r)
		if got < 0 || got >= interval {
			t.Fatalf("offset at r=%v = %s, want within [0, %s)", r, got, interval)
		}
	}
	if got := StartOffset(interval, 0.5); got != 15*time.Second {
		t.Fatalf("offset at r=0.5 = %s, want 15s", got)
	}
}

func TestJitteredStaysWithinBounds(t *testing.T) {
	interval := 30 * time.Second
	low := time.Duration(float64(interval) * (1 - DefaultJitter))
	high := time.Duration(float64(interval) * (1 + DefaultJitter))
	if got := Jittered(interval, DefaultJitter, 0); got != low {
		t.Fatalf("jittered at r=0 = %s, want %s", got, low)
	}
	for range 1000 {
		r := rand.Float64()
		got := Jittered(interval, DefaultJitter, r)
		if got < low || got >= high {
			t.Fatalf("jittered at r=%v = %s, want within [%s, %s)", r, got, low, high)
		}
	}
	if DefaultJitter < 0.10 || DefaultJitter > 0.20 {
		t.Fatalf("DefaultJitter = %v, want between 10%% and 20%%", DefaultJitter)
	}
	if got := Jittered(interval, 0, 0.9); got != interval {
		t.Fatalf("zero jitter = %s, want the interval", got)
	}
}

// TestTickerFirstTickFollowsTheOffset drives a ticker with a fixed random source: the first tick arrives after the
// start offset, not after a full interval, and later ticks a jittered interval apart.
func TestTickerFirstTickFollowsTheOffset(t *testing.T) {
	interval := 400 * time.Millisecond
	// r=0.25: first tick at 100ms, then every 400ms*(1-0.15+0.3*0.25) = 370ms.
	start := time.Now()
	tk := newTicker(interval, DefaultJitter, func() float64 { return 0.25 })
	defer tk.Stop()

	first := <-tk.C
	if elapsed := first.Sub(start); elapsed < 80*time.Millisecond || elapsed > 300*time.Millisecond {
		t.Fatalf("first tick after %s, want about 100ms", elapsed)
	}
	second := <-tk.C
	if gap := second.Sub(first); gap < 330*time.Millisecond || gap > 600*time.Millisecond {
		t.Fatalf("second tick %s after the first, want about 370ms", gap)
	}
}

func TestTickerStopEndsTicks(t *testing.T) {
	tk := newTicker(20*time.Millisecond, DefaultJitter, func() float64 { return 0 })
	<-tk.C
	tk.Stop()
	tk.Stop()
	// Drain a tick that was in flight as Stop ran, then expect silence.
	select {
	case <-tk.C:
	case <-time.After(60 * time.Millisecond):
	}
	select {
	case <-tk.C:
		t.Fatal("tick delivered after Stop")
	case <-time.After(100 * time.Millisecond):
	}
}
