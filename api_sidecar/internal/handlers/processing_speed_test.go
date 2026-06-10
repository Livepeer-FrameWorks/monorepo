package handlers

import (
	"testing"

	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
)

func TestProcessingSpeedSampler(t *testing.T) {
	s := &processingSpeedSampler{}

	// First observation only primes the baseline.
	s.observe(1000, 0)
	if s.samples != 0 {
		t.Fatalf("samples after prime = %d, want 0", s.samples)
	}

	// 5s wall, 40s media -> 8x; then 5s wall, 5s media -> 1x.
	s.observe(6000, 40000)
	s.observe(11000, 45000)
	if s.samples != 2 {
		t.Fatalf("samples = %d, want 2", s.samples)
	}
	if s.minX != 1 || s.maxX != 8 {
		t.Fatalf("min/max = %v/%v, want 1/8", s.minX, s.maxX)
	}
	if avg := s.sumX / float64(s.samples); avg != 4.5 {
		t.Fatalf("avg = %v, want 4.5", avg)
	}

	// Media not advancing (stall) must not produce a sample.
	s.observe(16000, 45000)
	if s.samples != 2 {
		t.Fatalf("stalled tick added a sample: %d", s.samples)
	}
}

func TestProcessingSpeedTelemetry_PrefersMistStats(t *testing.T) {
	drainMs := int64(30000)
	evt := &ProcessingRecordingEndEvent{
		MediaDurationMs: 40792,
		ProcessingSpeed: &ipcpb.ProcessingSpeedStats{
			Ticks: 40, SpeedMin: 1, SpeedAvg: 6.5, SpeedMax: 24,
			HardSlowTicks: 3, StaleHoldTicks: 12, DrainMs: &drainMs,
		},
	}
	sampler := &processingSpeedSampler{minX: 2, maxX: 4, sumX: 6, samples: 2}

	outputs, _ := processingSpeedTelemetry(nil, evt, sampler, 0)
	if outputs["speed_source"] != "mist" {
		t.Fatalf("speed_source = %q, want mist", outputs["speed_source"])
	}
	if outputs["speed_avg_x"] != "6.50" || outputs["speed_min_x"] != "1.00" || outputs["speed_max_x"] != "24.00" {
		t.Fatalf("speed outputs mismatch: %v", outputs)
	}
	if outputs["hard_slow_ticks"] != "3" || outputs["stale_hold_ticks"] != "12" || outputs["drain_ms"] != "30000" {
		t.Fatalf("verdict outputs mismatch: %v", outputs)
	}
	if outputs["processing_wall_ms"] == "" {
		t.Fatal("processing_wall_ms missing")
	}
}

func TestProcessingSpeedTelemetry_FallsBackToSampler(t *testing.T) {
	evt := &ProcessingRecordingEndEvent{MediaDurationMs: 1000}
	sampler := &processingSpeedSampler{minX: 2, maxX: 8, sumX: 10, samples: 2}

	outputs, _ := processingSpeedTelemetry(map[string]string{"existing": "kept"}, evt, sampler, 0)
	if outputs["speed_source"] != "sampled" {
		t.Fatalf("speed_source = %q, want sampled", outputs["speed_source"])
	}
	if outputs["speed_min_x"] != "2.00" || outputs["speed_avg_x"] != "5.00" || outputs["speed_max_x"] != "8.00" {
		t.Fatalf("sampled outputs mismatch: %v", outputs)
	}
	if outputs["existing"] != "kept" {
		t.Fatal("existing outputs must be preserved")
	}

	// Nil event and empty sampler: wall time only, no speed keys.
	outputs, _ = processingSpeedTelemetry(nil, nil, &processingSpeedSampler{}, 0)
	if _, ok := outputs["speed_source"]; ok {
		t.Fatal("no speed data should yield no speed_source")
	}
	if outputs["processing_wall_ms"] == "" {
		t.Fatal("processing_wall_ms missing on nil event")
	}
}
