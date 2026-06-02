package handlers

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// Storage freeze/eviction metrics. Bounded labels only — per-tenant /
// per-asset attribution lives in ClickHouse storage_events, not here.

var (
	// freezeUploads counts S3 freeze uploads by asset type and outcome.
	// asset_type: "clip"|"vod". status: "success"|"failed".
	freezeUploads = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: "helmsman",
			Name:      "freeze_uploads_total",
			Help:      "S3 freeze (sync) uploads by asset type and outcome",
		},
		[]string{"asset_type", "status"},
	)

	// freezeUploadSeconds is the S3 upload duration for a freeze.
	freezeUploadSeconds = promauto.NewHistogramVec(
		prometheus.HistogramOpts{
			Namespace: "helmsman",
			Name:      "freeze_upload_seconds",
			Help:      "S3 freeze upload duration by asset type",
			Buckets:   prometheus.ExponentialBuckets(0.05, 2, 14),
		},
		[]string{"asset_type"},
	)

	// freezeUploadBytes counts bytes uploaded on successful freezes.
	freezeUploadBytes = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: "helmsman",
			Name:      "freeze_upload_bytes_total",
			Help:      "Bytes uploaded to S3 on successful freezes by asset type",
		},
		[]string{"asset_type"},
	)

	// localEvictionPasses counts block-cache eviction passes under disk
	// pressure; localEvictionBytes counts the bytes those passes reclaimed.
	localEvictionPasses = promauto.NewCounter(
		prometheus.CounterOpts{
			Namespace: "helmsman",
			Name:      "local_eviction_passes_total",
			Help:      "Relay block-cache eviction passes run under disk pressure",
		},
	)
	localEvictionBytes = promauto.NewCounter(
		prometheus.CounterOpts{
			Namespace: "helmsman",
			Name:      "local_eviction_bytes_total",
			Help:      "Bytes reclaimed by relay block-cache eviction",
		},
	)
)
