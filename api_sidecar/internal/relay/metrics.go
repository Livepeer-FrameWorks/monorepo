package relay

import (
	"strings"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// Relay artifact-serving metrics. Labels are bounded only — per-tenant /
// per-asset attribution lives in ClickHouse storage_events (see the
// ACTION_CACHED read-through events emitted by the defrost aggregator),
// never on a Prometheus label.

// transferDurationBuckets spans 5ms → ~40s, covering both warm-disk serves
// and cold S3 range fetches under load.
var transferDurationBuckets = prometheus.ExponentialBuckets(0.005, 2, 14)

var (
	// relayRequests counts artifact serve requests by container format and
	// the source the bytes came from. source: "local" (warm disk),
	// "s3" (cold read-through), "peer" (cross-cluster relay grant).
	// status: "served", "not_playable", "error".
	relayRequests = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: "helmsman",
			Name:      "relay_requests_total",
			Help:      "Artifact relay serve requests by format and source",
		},
		[]string{"format", "source", "status"},
	)

	// relayServeSeconds is end-to-end serve latency for a relay request.
	relayServeSeconds = promauto.NewHistogramVec(
		prometheus.HistogramOpts{
			Namespace: "helmsman",
			Name:      "relay_serve_seconds",
			Help:      "Artifact relay serve duration by format and source",
			Buckets:   transferDurationBuckets,
		},
		[]string{"format", "source"},
	)

	// defrostBlocks counts cold block fetches (read-through) by upstream
	// source and outcome. source: "s3" or "peer". status: "success"/"error".
	defrostBlocks = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: "helmsman",
			Name:      "defrost_blocks_total",
			Help:      "Cold block read-through fetches by source and outcome",
		},
		[]string{"source", "status"},
	)

	// defrostBytes counts bytes pulled on cold block fetches by source.
	defrostBytes = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: "helmsman",
			Name:      "defrost_bytes_total",
			Help:      "Bytes fetched on cold block read-through by source",
		},
		[]string{"source"},
	)

	// defrostTTFB is time to response headers on a cold block fetch — the
	// viewer-visible latency cost of reading a frozen artifact.
	defrostTTFB = promauto.NewHistogramVec(
		prometheus.HistogramOpts{
			Namespace: "helmsman",
			Name:      "defrost_ttfb_seconds",
			Help:      "Time to first byte on cold block read-through by source",
			Buckets:   transferDurationBuckets,
		},
		[]string{"source"},
	)

	// coldfetchCoalesced counts same-block cold-fan-out coalescing outcomes.
	// result: "leader" (ran the fetch) or "follower" (waited on the leader).
	coldfetchCoalesced = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: "helmsman",
			Name:      "coldfetch_coalesced_total",
			Help:      "Same-block cold-fetch coalescing outcomes",
		},
		[]string{"result"},
	)

	// dtshGeneration counts .dtsh sidecar activity at the relay. source:
	// "lazy_404" (relay returned 404 so Mist generates one) or "putback"
	// (Mist wrote a freshly generated sidecar back). status: "ok"/"error".
	dtshGeneration = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: "helmsman",
			Name:      "dtsh_generation_total",
			Help:      "DTSH sidecar generation activity at the relay by source",
		},
		[]string{"source", "status"},
	)

	// dtshUpload counts direct .dtsh uploads from the relay to S3.
	dtshUpload = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: "helmsman",
			Name:      "dtsh_upload_total",
			Help:      "Direct .dtsh sidecar uploads from the relay to S3 by status",
		},
		[]string{"status"},
	)
)

// relayFormatLabel reduces a filename to a bounded container-format label.
func relayFormatLabel(file string) string {
	file = strings.TrimSuffix(file, ".dtsh")
	dot := strings.LastIndexByte(file, '.')
	if dot < 0 || dot == len(file)-1 {
		return "none"
	}
	ext := strings.ToLower(file[dot+1:])
	switch ext {
	case "mp4", "mov", "mkv", "webm", "ts", "m2ts", "m3u8", "m3u":
		return ext
	default:
		return "other"
	}
}

// upstreamSourceLabel classifies a fetch by whether it carries a peer-relay
// grant ("peer") or is an S3 presigned URL ("s3").
func upstreamSourceLabel(grantID string) string {
	if grantID != "" {
		return "peer"
	}
	return "s3"
}
