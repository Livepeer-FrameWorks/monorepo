package knowledge

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

var (
	crawlPagesTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: "skipper",
			Name:      "crawl_pages_total",
			Help:      "Total pages processed during crawl cycles",
		},
		[]string{"status"},
	)

	crawlDuration = promauto.NewHistogramVec(
		prometheus.HistogramOpts{
			Namespace: "skipper",
			Name:      "crawl_duration_seconds",
			Help:      "Duration of crawl cycles in seconds",
			Buckets:   prometheus.ExponentialBuckets(1, 2, 12), // 1s to ~1h
		},
		[]string{"source"},
	)

	embedCallsTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: "skipper",
			Name:      "embed_calls_total",
			Help:      "Total embedding API calls",
		},
		[]string{"provider", "model", "status"},
	)

	embedDuration = promauto.NewHistogramVec(
		prometheus.HistogramOpts{
			Namespace: "skipper",
			Name:      "embed_duration_seconds",
			Help:      "Duration of embedding API calls in seconds",
			Buckets:   prometheus.DefBuckets,
		},
		[]string{"provider", "model"},
	)

	embedInputsTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: "skipper",
			Name:      "embed_inputs_total",
			Help:      "Total individual texts submitted for embedding",
		},
		[]string{"provider", "model"},
	)

	renderPagesTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: "skipper",
			Name:      "render_pages_total",
			Help:      "Total pages rendered via headless browser",
		},
		[]string{"status"},
	)

	renderDuration = promauto.NewHistogram(
		prometheus.HistogramOpts{
			Namespace: "skipper",
			Name:      "render_duration_seconds",
			Help:      "Duration of headless browser page renders in seconds",
			Buckets:   prometheus.ExponentialBuckets(0.5, 2, 8), // 0.5s to ~64s
		},
	)

	contextualCallsTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: "skipper",
			Name:      "contextual_calls_total",
			Help:      "Total contextual retrieval LLM calls",
		},
		[]string{"provider", "model", "status"},
	)

	contextualDuration = promauto.NewHistogramVec(
		prometheus.HistogramOpts{
			Namespace: "skipper",
			Name:      "contextual_duration_seconds",
			Help:      "Duration of contextual retrieval LLM calls in seconds",
			Buckets:   prometheus.ExponentialBuckets(0.5, 2, 8),
		},
		[]string{"provider", "model"},
	)

	rerankCallsTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: "skipper",
			Name:      "rerank_calls_total",
			Help:      "Total reranker API calls",
		},
		[]string{"provider", "model", "status"},
	)

	rerankDuration = promauto.NewHistogramVec(
		prometheus.HistogramOpts{
			Namespace: "skipper",
			Name:      "rerank_duration_seconds",
			Help:      "Duration of reranker API calls in seconds",
			Buckets:   prometheus.DefBuckets,
		},
		[]string{"provider", "model"},
	)

	rerankDocumentsTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: "skipper",
			Name:      "rerank_documents_total",
			Help:      "Total documents submitted for reranking",
		},
		[]string{"provider", "model"},
	)

	linkDiscoveryTotal = promauto.NewCounter(
		prometheus.CounterOpts{
			Namespace: "skipper",
			Name:      "link_discovery_total",
			Help:      "Total links discovered from crawled pages",
		},
	)

	headCheckSkipsTotal = promauto.NewCounter(
		prometheus.CounterOpts{
			Namespace: "skipper",
			Name:      "head_check_skips_total",
			Help:      "Total render skips from HEAD size-match optimization",
		},
	)

	chunksFilteredTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: "skipper",
			Name:      "chunks_filtered_total",
			Help:      "Total chunks filtered during embedding",
		},
		[]string{"reason"},
	)

	crawlQueueSize = promauto.NewGauge(
		prometheus.GaugeOpts{
			Namespace: "skipper",
			Name:      "crawl_queue_size",
			Help:      "Total items in the current crawl queue",
		},
	)

	crawlQueueRemaining = promauto.NewGauge(
		prometheus.GaugeOpts{
			Namespace: "skipper",
			Name:      "crawl_queue_remaining",
			Help:      "Remaining items in the current crawl queue",
		},
	)

	crawlTickInterval = promauto.NewGauge(
		prometheus.GaugeOpts{
			Namespace: "skipper",
			Name:      "crawl_tick_interval_seconds",
			Help:      "Current interval between crawl item dispatches",
		},
	)
)
