package jobs

import (
	"testing"
	"time"
)

func TestCatalogLockRetryDelayDoublesCapsAndJitters(t *testing.T) {
	for lost, want := range map[int]time.Duration{1: 5 * time.Second, 2: 10 * time.Second, 3: 20 * time.Second, 4: 30 * time.Second, 40: 30 * time.Second} {
		if got := catalogLockRetryDelay(catalogLockRetryInterval, lost, 0.5); got != want {
			t.Errorf("loss %d: delay %s, want %s", lost, got, want)
		}
		low := time.Duration(float64(want) * (1 - catalogLockRetryJitter))
		high := time.Duration(float64(want) * (1 + catalogLockRetryJitter))
		if got := catalogLockRetryDelay(catalogLockRetryInterval, lost, 0); got != low {
			t.Errorf("loss %d at the bottom of the band: %s, want %s", lost, got, low)
		}
		if got := catalogLockRetryDelay(catalogLockRetryInterval, lost, 0.999999); got >= high || got < want {
			t.Errorf("loss %d at the top of the band: %s, want just below %s", lost, got, high)
		}
	}
	if catalogLockRetryInterval <= 2*time.Second {
		t.Fatalf("first retry %s; losers must not retry every two seconds", catalogLockRetryInterval)
	}
}
