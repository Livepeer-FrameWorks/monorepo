package chat

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

var (
	searchQueriesTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: "skipper",
			Name:      "search_queries_total",
			Help:      "Total knowledge search queries",
		},
		[]string{"type"}, // "tool_call", "pre_retrieval"
	)

	searchDuration = promauto.NewHistogram(
		prometheus.HistogramOpts{
			Namespace: "skipper",
			Name:      "search_duration_seconds",
			Help:      "Duration of knowledge search operations in seconds",
			Buckets:   prometheus.DefBuckets,
		},
	)

	searchResultsCount = promauto.NewHistogram(
		prometheus.HistogramOpts{
			Namespace: "skipper",
			Name:      "search_results_count",
			Help:      "Number of results returned per knowledge search",
			Buckets:   []float64{0, 1, 2, 3, 5, 8, 10, 15, 20},
		},
	)

	llmCallsTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: "skipper",
			Name:      "llm_calls_total",
			Help:      "Total LLM API calls",
		},
		[]string{"provider", "model", "status"},
	)

	llmDuration = promauto.NewHistogramVec(
		prometheus.HistogramOpts{
			Namespace: "skipper",
			Name:      "llm_duration_seconds",
			Help:      "Duration of LLM API calls in seconds",
			Buckets:   prometheus.ExponentialBuckets(0.1, 2, 10), // 100ms to ~50s
		},
		[]string{"provider", "model"},
	)

	llmTokensTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: "skipper",
			Name:      "llm_tokens_total",
			Help:      "Total LLM tokens consumed",
		},
		[]string{"provider", "model", "direction"},
	)

	conversationsActive = promauto.NewGauge(
		prometheus.GaugeOpts{
			Namespace: "skipper",
			Name:      "conversations_active",
			Help:      "Number of currently active chat conversations",
		},
	)
)
