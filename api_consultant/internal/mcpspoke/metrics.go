package mcpspoke

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

var (
	spokeSearchQueriesTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: "skipper",
			Subsystem: "spoke",
			Name:      "search_queries_total",
			Help:      "Total MCP spoke search queries",
		},
		[]string{"tool"},
	)

	spokeSearchDuration = promauto.NewHistogram(
		prometheus.HistogramOpts{
			Namespace: "skipper",
			Subsystem: "spoke",
			Name:      "search_duration_seconds",
			Help:      "Duration of MCP spoke search operations in seconds",
			Buckets:   prometheus.DefBuckets,
		},
	)

	spokeSearchResultsCount = promauto.NewHistogram(
		prometheus.HistogramOpts{
			Namespace: "skipper",
			Subsystem: "spoke",
			Name:      "search_results_count",
			Help:      "Number of results returned per MCP spoke search",
			Buckets:   []float64{0, 1, 2, 3, 5, 8, 10, 15, 20},
		},
	)
)
