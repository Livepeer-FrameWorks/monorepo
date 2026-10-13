package grpc

import (
	"testing"
	"time"
)

func TestCapabilityPricesFromArrays(t *testing.T) {
	// These four parallel arrays come straight from ClickHouse groupArray
	// columns. Intent: zip them positionally. A length mismatch means the
	// query returned ragged arrays (a data bug), so the function clamps to the
	// shortest to avoid an index panic rather than trusting any one length.
	t.Run("equal lengths zip fully", func(t *testing.T) {
		out := capabilityPricesFromArrays(
			[]string{"h264", "av1"},
			[]uint32{0, 1},
			[]int64{100, 200},
			[]int64{1000, 2000},
		)
		if len(out) != 2 {
			t.Fatalf("len = %d, want 2", len(out))
		}
		if out[1].Capability != "av1" || out[1].Position != 1 ||
			out[1].PricePerUnit != 200 || out[1].PixelsPerUnit != 2000 {
			t.Errorf("second entry mismatched: %+v", out[1])
		}
	})

	t.Run("clamps to shortest array", func(t *testing.T) {
		// capabilities is the shortest (1) — only one row should emerge even
		// though the other arrays carry two values.
		out := capabilityPricesFromArrays(
			[]string{"h264"},
			[]uint32{0, 1},
			[]int64{100, 200},
			[]int64{1000, 2000},
		)
		if len(out) != 1 {
			t.Fatalf("len = %d, want 1 (clamp to shortest)", len(out))
		}

		// pricePerUnits shortest (0) → empty result, no panic.
		out = capabilityPricesFromArrays(
			[]string{"h264", "av1"},
			[]uint32{0, 1},
			[]int64{},
			[]int64{1000, 2000},
		)
		if len(out) != 0 {
			t.Fatalf("len = %d, want 0 when a column is empty", len(out))
		}

		// positions shortest (1) → one row.
		out = capabilityPricesFromArrays(
			[]string{"h264", "av1"},
			[]uint32{0},
			[]int64{100, 200},
			[]int64{1000, 2000},
		)
		if len(out) != 1 {
			t.Fatalf("len = %d, want 1 (positions shortest)", len(out))
		}

		// pixelsPerUnits shortest (1) → one row.
		out = capabilityPricesFromArrays(
			[]string{"h264", "av1"},
			[]uint32{0, 1},
			[]int64{100, 200},
			[]int64{1000},
		)
		if len(out) != 1 {
			t.Fatalf("len = %d, want 1 (pixelsPerUnits shortest)", len(out))
		}
	})

	t.Run("all empty", func(t *testing.T) {
		out := capabilityPricesFromArrays(nil, nil, nil, nil)
		if len(out) != 0 {
			t.Fatalf("len = %d, want 0", len(out))
		}
	})
}

// A bucket fed by both the legacy tables and network_observations sums their
// counts and keeps the mean weighted by each source's sample count.
func TestPerformanceSeriesSumsSourcesIntoOnePoint(t *testing.T) {
	ts := time.Unix(1_700_000_000, 0).UTC()
	key := orchestratorPerformanceKey{ts: ts, gatewayID: "gw-1", gatewayRegion: "eu-west", resolvedIP: "10.0.0.1"}
	series := performanceSeries{}
	series.bucket(key).discovery.add(performanceTotals{attempts: 3, successes: 3, latencySum: 300, latencyCount: 3, maxLatency: 120})
	series.bucket(key).discovery.add(performanceTotals{attempts: 2, successes: 1, failures: 1, latencySum: 600, latencyCount: 1, maxLatency: 600})
	series.bucket(key).transcode.add(performanceTotals{attempts: 4, successes: 4, latencySum: 800, latencyCount: 4, maxLatency: 250, pixels: 1000})
	series.bucket(orchestratorPerformanceKey{ts: ts, gatewayID: "gw-2"}).ai.add(performanceTotals{attempts: 1})

	points := series.points()
	if len(points) != 2 {
		t.Fatalf("points = %d, want one per key", len(points))
	}
	p := points[0]
	if p.GetGatewayId() != "gw-1" || p.GetGatewayRegion() != "eu-west" || p.GetResolvedIp() != "10.0.0.1" || !p.GetTimestamp().AsTime().Equal(ts) {
		t.Fatalf("point not seeded from its key: %+v", p)
	}
	if p.GetAttempts() != 5 || p.GetSuccesses() != 4 || p.GetFailures() != 1 || p.GetMaxLatencyMs() != 600 {
		t.Fatalf("discovery totals = %+v", p)
	}
	if p.GetMeanLatencyMs() != 225 { // (300+600)/(3+1)
		t.Fatalf("mean latency = %v, want the sample-weighted 225", p.GetMeanLatencyMs())
	}
	if p.GetTranscodeAttempts() != 4 || p.GetTranscodeMeanOverallMs() != 200 || p.GetTranscodePixels() != 1000 || p.GetAiAttempts() != 0 {
		t.Fatalf("transcode/AI totals leaked between buckets: %+v", p)
	}
	if points[1].GetGatewayId() != "gw-2" || points[1].GetAiAttempts() != 1 {
		t.Fatalf("second key = %+v", points[1])
	}
}
