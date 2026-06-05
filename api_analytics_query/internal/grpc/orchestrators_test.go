package grpc

import (
	"testing"
	"time"

	periscopepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/periscope"
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

func TestPerformancePoint(t *testing.T) {
	// Intent: a get-or-create accumulator. The same key must return the SAME
	// pointer so callers can keep summing metrics into one row; a new key
	// must mint a row seeded from the key's dimensions.
	s := &PeriscopeServer{}
	points := map[orchestratorPerformanceKey]*periscopepb.OrchestratorPerformancePoint{}

	ts := time.Unix(1_700_000_000, 0).UTC()
	key := orchestratorPerformanceKey{
		ts:            ts,
		gatewayID:     "gw-1",
		gatewayRegion: "eu-west",
		resolvedIP:    "10.0.0.1",
	}

	first := s.performancePoint(points, key)
	if first == nil {
		t.Fatal("expected a point")
	}
	if first.GatewayId != "gw-1" || first.GatewayRegion != "eu-west" || first.ResolvedIp != "10.0.0.1" {
		t.Errorf("point not seeded from key: %+v", first)
	}
	if first.GetTimestamp().AsTime().Unix() != ts.Unix() {
		t.Errorf("timestamp = %v, want %v", first.GetTimestamp().AsTime(), ts)
	}
	if len(points) != 1 {
		t.Errorf("map size = %d, want 1 after create", len(points))
	}

	// Same key → same pointer, no new row.
	again := s.performancePoint(points, key)
	if again != first {
		t.Error("same key must return the identical pointer")
	}
	if len(points) != 1 {
		t.Errorf("map size = %d, want 1 (no duplicate)", len(points))
	}

	// Different key → distinct row.
	other := s.performancePoint(points, orchestratorPerformanceKey{ts: ts, gatewayID: "gw-2"})
	if other == first {
		t.Error("distinct key must return a distinct pointer")
	}
	if len(points) != 2 {
		t.Errorf("map size = %d, want 2", len(points))
	}
}
