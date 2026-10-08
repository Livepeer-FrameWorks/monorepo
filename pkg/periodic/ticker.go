// Package periodic schedules background loops so that replicas and loops started together do not fire together.
package periodic

import (
	"math/rand/v2"
	"sync"
	"time"
)

// DefaultJitter is the fraction by which NewTicker varies each interval: every tick lands between 85% and 115% of
// the interval after the previous one.
const DefaultJitter = 0.15

// Ticker delivers ticks on C like time.Ticker, but the first tick lands at a random offset in [0, interval) and
// every later one a jittered interval after the previous tick. Loops built on time.NewTicker at process start all
// fire in the same second on every replica, and that second's database writes then contend with each other.
//
// Like time.Ticker, C holds one tick; a receiver slower than the interval sees ticks dropped, not queued.
type Ticker struct {
	C <-chan time.Time

	stop     chan struct{}
	stopOnce sync.Once
}

// NewTicker returns a running Ticker with DefaultJitter. interval must be positive.
func NewTicker(interval time.Duration) *Ticker {
	return newTicker(interval, DefaultJitter, rand.Float64)
}

func newTicker(interval time.Duration, jitter float64, random func() float64) *Ticker {
	if interval <= 0 {
		panic("periodic: non-positive interval for NewTicker")
	}
	c := make(chan time.Time, 1)
	t := &Ticker{C: c, stop: make(chan struct{})}
	go t.run(c, StartOffset(interval, random()), func() time.Duration {
		return Jittered(interval, jitter, random())
	})
	return t
}

func (t *Ticker) run(c chan<- time.Time, first time.Duration, next func() time.Duration) {
	timer := time.NewTimer(first)
	defer timer.Stop()
	for {
		select {
		case <-t.stop:
			return
		case now := <-timer.C:
			select {
			case c <- now:
			default:
			}
			timer.Reset(next())
		}
	}
}

// Stop turns the ticker off and releases its goroutine, which unlike time.Ticker's timer is not collected while
// unstopped. A tick already buffered in C, or being sent as Stop runs, may still be read.
func (t *Ticker) Stop() {
	t.stopOnce.Do(func() { close(t.stop) })
}

// StartOffset maps r in [0, 1) to the delay before a loop's first tick, in [0, interval).
func StartOffset(interval time.Duration, r float64) time.Duration {
	return time.Duration(clampUnit(r) * float64(interval))
}

// Jittered maps r in [0, 1) to an interval scaled by a factor in [1-jitter, 1+jitter).
func Jittered(interval time.Duration, jitter, r float64) time.Duration {
	jitter = min(max(jitter, 0), 1)
	return time.Duration(float64(interval) * (1 - jitter + 2*jitter*clampUnit(r)))
}

func clampUnit(r float64) float64 {
	return min(max(r, 0), 1)
}
