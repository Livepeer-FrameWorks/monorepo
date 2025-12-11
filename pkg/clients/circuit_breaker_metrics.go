package clients

import (
	"github.com/prometheus/client_golang/prometheus"
)

var (
	// circuitBreakerState tracks the current state of each circuit breaker.
	// Values: 0=closed, 1=half-open, 2=open
	circuitBreakerState = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "circuit_breaker_state",
			Help: "Current state of circuit breaker (0=closed, 1=half-open, 2=open)",
		},
		[]string{"name"},
	)

	// circuitBreakerStateTransitions counts state transitions
	circuitBreakerStateTransitions = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "circuit_breaker_state_transitions_total",
			Help: "Total number of circuit breaker state transitions",
		},
		[]string{"name", "from", "to"},
	)
)

func init() {
	prometheus.MustRegister(circuitBreakerState)
	prometheus.MustRegister(circuitBreakerStateTransitions)
}

// RecordCircuitBreakerState updates the Prometheus metric for circuit breaker state.
// Call this from the OnStateChange callback.
func RecordCircuitBreakerState(name string, state CircuitBreakerState) {
	circuitBreakerState.WithLabelValues(name).Set(float64(state))
}

// RecordCircuitBreakerTransition records a state transition in Prometheus.
// Call this from the OnStateChange callback.
func RecordCircuitBreakerTransition(name string, from, to CircuitBreakerState) {
	circuitBreakerStateTransitions.WithLabelValues(name, from.String(), to.String()).Inc()
	RecordCircuitBreakerState(name, to)
}

// CircuitBreakerMetricsCallback returns a callback function suitable for use
// with CircuitBreakerConfig.OnStateChange that records metrics.
func CircuitBreakerMetricsCallback(name string) func(string, CircuitBreakerState, CircuitBreakerState) {
	return func(_ string, from, to CircuitBreakerState) {
		RecordCircuitBreakerTransition(name, from, to)
	}
}
