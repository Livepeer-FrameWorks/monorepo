package handlers

import (
	"sync"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// mistAPIUnreachableThreshold is the number of consecutive failed
// authenticated api2 polls after which the controller counts as unreachable.
// The active_streams poll runs every 10 seconds, so three failures is about
// 30 seconds: long enough to ride out a controller restart, short enough that
// placement stops sending viewers and jobs to a node that cannot be driven.
const mistAPIUnreachableThreshold = 3

var (
	mistAPIReachableGauge = promauto.NewGauge(prometheus.GaugeOpts{
		Namespace: "helmsman",
		Name:      "mist_api_reachable",
		Help:      "Whether the authenticated MistServer controller API (api2) answers Helmsman (1=reachable, 0=unreachable)",
	})
	mistAPIConsecutiveFailuresGauge = promauto.NewGauge(prometheus.GaugeOpts{
		Namespace: "helmsman",
		Name:      "mist_api_consecutive_failures",
		Help:      "Consecutive failed authenticated MistServer api2 polls",
	})
)

func init() {
	mistAPIReachableGauge.Set(1)
}

// mistAPIHealth tracks whether Mist's controller API answers authenticated
// requests. A transport or authentication failure counts; a response Mist
// actually produced (even a malformed one) proves the controller is serving.
// The zero value is reachable, so a freshly started Helmsman does not report
// its node unhealthy before the first poll has run.
type mistAPIHealth struct {
	mu                  sync.Mutex
	consecutiveFailures int
}

// recordSuccess returns true when the API was unreachable before this call.
func (h *mistAPIHealth) recordSuccess() bool {
	h.mu.Lock()
	wasUnreachable := h.consecutiveFailures >= mistAPIUnreachableThreshold
	h.consecutiveFailures = 0
	h.mu.Unlock()
	mistAPIConsecutiveFailuresGauge.Set(0)
	mistAPIReachableGauge.Set(1)
	return wasUnreachable
}

// recordFailure returns true when this failure crossed the unreachable
// threshold, so the caller logs the transition once.
func (h *mistAPIHealth) recordFailure() bool {
	h.mu.Lock()
	h.consecutiveFailures++
	failures := h.consecutiveFailures
	h.mu.Unlock()
	mistAPIConsecutiveFailuresGauge.Set(float64(failures))
	if failures >= mistAPIUnreachableThreshold {
		mistAPIReachableGauge.Set(0)
	}
	return failures == mistAPIUnreachableThreshold
}

func (h *mistAPIHealth) reachable() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.consecutiveFailures < mistAPIUnreachableThreshold
}
