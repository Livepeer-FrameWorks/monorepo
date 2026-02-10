package diagnostics

import (
	"testing"
)

func TestCorrelateEmpty(t *testing.T) {
	results := Correlate(nil)
	if len(results) != 0 {
		t.Errorf("expected empty, got %d", len(results))
	}
	results = Correlate([]Deviation{})
	if len(results) != 0 {
		t.Errorf("expected empty, got %d", len(results))
	}
}

func TestCorrelateNetworkDegradation(t *testing.T) {
	devs := []Deviation{
		{Metric: "avg_packet_loss", Current: 0.05, Baseline: 0.01, StdDev: 0.005, Sigma: 8.0, Direction: "above"},
		{Metric: "avg_buffer_health", Current: 1.3, Baseline: 2.8, StdDev: 0.3, Sigma: 5.0, Direction: "below"},
	}
	results := Correlate(devs)
	if len(results) == 0 {
		t.Fatal("expected at least 1 correlation")
	}
	found := false
	for _, r := range results {
		if r.Pattern == PatternNetworkDegradation {
			found = true
			if r.Confidence < 0.5 {
				t.Errorf("expected confidence >= 0.5, got %v", r.Confidence)
			}
			if len(r.Signals) < 2 {
				t.Errorf("expected >= 2 signals, got %d", len(r.Signals))
			}
			if r.Hypothesis == "" {
				t.Error("expected non-empty hypothesis")
			}
		}
	}
	if !found {
		t.Error("network_degradation pattern not found")
	}
}

func TestCorrelateEncoderOverload(t *testing.T) {
	devs := []Deviation{
		{Metric: "avg_fps", Current: 15, Baseline: 30, StdDev: 1.5, Sigma: 10, Direction: "below"},
		{Metric: "avg_bitrate", Current: 500000, Baseline: 3000000, StdDev: 200000, Sigma: 12.5, Direction: "below"},
	}
	results := Correlate(devs)
	found := false
	for _, r := range results {
		if r.Pattern == PatternEncoderOverload {
			found = true
			// packet_loss NOT deviated → absence boost
			if r.Confidence < 0.9 {
				t.Errorf("expected high confidence with absence boost, got %v", r.Confidence)
			}
		}
	}
	if !found {
		t.Error("encoder_overload pattern not found")
	}
}

func TestCorrelateEncoderOverloadNoBoostWhenPacketLossPresent(t *testing.T) {
	devs := []Deviation{
		{Metric: "avg_fps", Current: 15, Baseline: 30, StdDev: 1.5, Sigma: 10, Direction: "below"},
		{Metric: "avg_bitrate", Current: 500000, Baseline: 3000000, StdDev: 200000, Sigma: 12.5, Direction: "below"},
		{Metric: "avg_packet_loss", Current: 0.1, Baseline: 0.01, StdDev: 0.005, Sigma: 18, Direction: "above"},
	}
	results := Correlate(devs)
	for _, r := range results {
		if r.Pattern == PatternEncoderOverload {
			// With packet_loss deviated, no absence boost: 2/2 = 1.0 base, no +0.1
			if r.Confidence > 1.0 {
				t.Errorf("expected confidence <= 1.0 without boost, got %v", r.Confidence)
			}
		}
	}
}

func TestCorrelateViewerSideIssues(t *testing.T) {
	devs := []Deviation{
		{Metric: "avg_buffer_health", Current: 1.0, Baseline: 3.0, StdDev: 0.4, Sigma: 5, Direction: "below"},
		{Metric: "total_rebuffer_count", Current: 50, Baseline: 5, StdDev: 3, Sigma: 15, Direction: "above"},
	}
	results := Correlate(devs)
	found := false
	for _, r := range results {
		if r.Pattern == PatternViewerSideIssues {
			found = true
		}
	}
	if !found {
		t.Error("viewer_side_issues pattern not found")
	}
}

func TestCorrelateIngestInstability(t *testing.T) {
	devs := []Deviation{
		{Metric: "avg_bitrate", Current: 400000, Baseline: 3000000, StdDev: 200000, Sigma: 13, Direction: "below"},
		{Metric: "avg_fps", Current: 12, Baseline: 30, StdDev: 1.5, Sigma: 12, Direction: "below"},
		{Metric: "total_issue_count", Current: 5, Baseline: 0.2, StdDev: 0.1, Sigma: 48, Direction: "above"},
	}
	results := Correlate(devs)
	found := false
	for _, r := range results {
		if r.Pattern == PatternIngestInstability {
			found = true
			if r.Confidence < 0.9 {
				t.Errorf("expected high confidence with 3/3 signals, got %v", r.Confidence)
			}
		}
	}
	if !found {
		t.Error("ingest_instability pattern not found")
	}
}

func TestCorrelateCDNPressure(t *testing.T) {
	devs := []Deviation{
		{Metric: "avg_bandwidth_out", Current: 100e6, Baseline: 50e6, StdDev: 10e6, Sigma: 5, Direction: "above"},
		{Metric: "active_sessions", Current: 500, Baseline: 100, StdDev: 20, Sigma: 20, Direction: "above"},
		{Metric: "total_rebuffer_count", Current: 30, Baseline: 5, StdDev: 3, Sigma: 8.3, Direction: "above"},
	}
	results := Correlate(devs)
	found := false
	for _, r := range results {
		if r.Pattern == PatternCDNPressure {
			found = true
			if len(r.Signals) < 3 {
				t.Errorf("expected 3 signals with rebuffer, got %d", len(r.Signals))
			}
		}
	}
	if !found {
		t.Error("cdn_pressure pattern not found")
	}
}

func TestCorrelateSingleDeviationNoMatch(t *testing.T) {
	devs := []Deviation{
		{Metric: "avg_fps", Current: 20, Baseline: 30, StdDev: 2, Sigma: 5, Direction: "below"},
	}
	results := Correlate(devs)
	if len(results) != 0 {
		t.Errorf("expected no correlations for single deviation, got %d", len(results))
	}
}

func TestCorrelateOverlappingPatterns(t *testing.T) {
	// fps↓ + bitrate↓ + buffer_health↓ + rebuffer↑ → both encoder_overload AND viewer_side_issues
	devs := []Deviation{
		{Metric: "avg_fps", Current: 15, Baseline: 30, StdDev: 1.5, Sigma: 10, Direction: "below"},
		{Metric: "avg_bitrate", Current: 500000, Baseline: 3000000, StdDev: 200000, Sigma: 12.5, Direction: "below"},
		{Metric: "avg_buffer_health", Current: 1.0, Baseline: 3.0, StdDev: 0.4, Sigma: 5, Direction: "below"},
		{Metric: "total_rebuffer_count", Current: 50, Baseline: 5, StdDev: 3, Sigma: 15, Direction: "above"},
	}
	results := Correlate(devs)
	patterns := make(map[CorrelationPattern]bool)
	for _, r := range results {
		patterns[r.Pattern] = true
	}
	if !patterns[PatternEncoderOverload] {
		t.Error("expected encoder_overload in overlapping scenario")
	}
	if !patterns[PatternViewerSideIssues] {
		t.Error("expected viewer_side_issues in overlapping scenario")
	}
}
