package diagnostics

import (
	"context"
	"testing"

	pb "frameworks/pkg/proto"
)

func TestPerStreamAnalyzerNil(t *testing.T) {
	var a *PerStreamAnalyzer
	result, err := a.Analyze(context.Background(), "t1", nil)
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
	if result != nil {
		t.Fatalf("expected nil result, got %v", result)
	}
}

func TestPerStreamAnalyzerEmpty(t *testing.T) {
	store := newMemStore()
	eval := NewBaselineEvaluator(store, 2.0, 5)
	a := NewPerStreamAnalyzer(eval)
	result, err := a.Analyze(context.Background(), "t1", nil)
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
	if result != nil {
		t.Fatalf("expected nil for empty metrics, got %v", result)
	}
}

func TestPerStreamAnalyzerFindsOutlier(t *testing.T) {
	store := newMemStore()
	eval := NewBaselineEvaluator(store, 2.0, 5)
	ctx := context.Background()

	// Build tenant-wide baseline with normal values.
	for _, v := range []float64{5000000, 5100000, 4900000, 5050000, 4950000, 5000000, 5100000, 4900000, 5050000, 4950000} {
		_ = eval.Update(ctx, "t1", "", map[string]float64{
			"avg_bitrate":       v,
			"avg_fps":           30,
			"avg_buffer_health": 3.0,
		})
	}

	// One healthy stream, one outlier.
	metrics := []*pb.StreamHealthMetric{
		{StreamId: "stream-good", Bitrate: 5000000, Fps: 30, BufferHealth: 3.0},
		{StreamId: "stream-good", Bitrate: 5000000, Fps: 30, BufferHealth: 3.0},
		{StreamId: "stream-bad", Bitrate: 100000, Fps: 10, BufferHealth: 0.5},
		{StreamId: "stream-bad", Bitrate: 100000, Fps: 10, BufferHealth: 0.5},
	}

	a := NewPerStreamAnalyzer(eval)
	anomalies, err := a.Analyze(ctx, "t1", metrics)
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	if len(anomalies) == 0 {
		t.Fatal("expected at least 1 anomaly")
	}
	// stream-bad should be first (highest sigma).
	if anomalies[0].StreamID != "stream-bad" {
		t.Errorf("expected stream-bad as top anomaly, got %s", anomalies[0].StreamID)
	}
	if anomalies[0].MaxSigma < 2.0 {
		t.Errorf("expected max sigma > 2.0, got %v", anomalies[0].MaxSigma)
	}
}

func TestPerStreamAnalyzerCapsAt20(t *testing.T) {
	store := newMemStore()
	eval := NewBaselineEvaluator(store, 2.0, 5)
	ctx := context.Background()

	// Build baseline.
	for i := 0; i < 10; i++ {
		_ = eval.Update(ctx, "t1", "", map[string]float64{
			"avg_bitrate":       5000000,
			"avg_fps":           30,
			"avg_buffer_health": 3.0,
		})
	}

	// Create 25 outlier streams.
	var metrics []*pb.StreamHealthMetric
	for i := 0; i < 25; i++ {
		sid := "stream-" + string(rune('a'+i))
		metrics = append(metrics, &pb.StreamHealthMetric{
			StreamId:     sid,
			Bitrate:      100000,
			Fps:          10,
			BufferHealth: 0.5,
		})
	}

	a := NewPerStreamAnalyzer(eval)
	anomalies, err := a.Analyze(ctx, "t1", metrics)
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	if len(anomalies) > maxAnomalousStreams {
		t.Errorf("expected at most %d anomalies, got %d", maxAnomalousStreams, len(anomalies))
	}
}

func TestPerStreamAnalyzerHealthyFilteredOut(t *testing.T) {
	store := newMemStore()
	eval := NewBaselineEvaluator(store, 2.0, 5)
	ctx := context.Background()

	// Build baseline matching the streams exactly.
	for i := 0; i < 10; i++ {
		_ = eval.Update(ctx, "t1", "", map[string]float64{
			"avg_bitrate":       5000000,
			"avg_fps":           30,
			"avg_buffer_health": 3.0,
		})
	}

	metrics := []*pb.StreamHealthMetric{
		{StreamId: "healthy-1", Bitrate: 5000000, Fps: 30, BufferHealth: 3.0},
		{StreamId: "healthy-2", Bitrate: 5000000, Fps: 30, BufferHealth: 3.0},
	}

	a := NewPerStreamAnalyzer(eval)
	anomalies, err := a.Analyze(ctx, "t1", metrics)
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	if len(anomalies) != 0 {
		t.Errorf("expected 0 anomalies for healthy streams, got %d", len(anomalies))
	}
}

func TestGroupByStream(t *testing.T) {
	metrics := []*pb.StreamHealthMetric{
		{StreamId: "s1", Bitrate: 1000, Fps: 20, BufferHealth: 2.0},
		{StreamId: "s1", Bitrate: 3000, Fps: 30, BufferHealth: 4.0},
		{StreamId: "s2", Bitrate: 5000, Fps: 25, BufferHealth: 3.0},
		nil,                                   // should be skipped
		{StreamId: "", Bitrate: 999, Fps: 99}, // should be skipped
	}

	result := groupByStream(metrics)
	if len(result) != 2 {
		t.Fatalf("expected 2 streams, got %d", len(result))
	}
	s1 := result["s1"]
	if s1["avg_bitrate"] != 2000 {
		t.Errorf("s1 avg_bitrate = %v, want 2000", s1["avg_bitrate"])
	}
	if s1["avg_fps"] != 25 {
		t.Errorf("s1 avg_fps = %v, want 25", s1["avg_fps"])
	}
}
