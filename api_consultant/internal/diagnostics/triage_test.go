package diagnostics

import (
	"testing"
	"time"
)

func TestTriageThresholdViolation(t *testing.T) {
	result := Triage(
		[]ThresholdViolation{{Metric: "buffer", Value: 1.0, Limit: 1.5, Message: "buffer health 1.0 < 1.5"}},
		nil,
		nil,
	)
	if result.Action != TriageInvestigate {
		t.Errorf("expected investigate, got %s", result.Action)
	}
	if result.Trigger != "threshold" {
		t.Errorf("expected trigger=threshold, got %s", result.Trigger)
	}
}

func TestTriageHighConfidenceCorrelation(t *testing.T) {
	result := Triage(
		nil,
		[]Deviation{{Metric: "fps", Sigma: 3}},
		[]MetricCorrelation{{Pattern: PatternEncoderOverload, Confidence: 0.6, Hypothesis: "encoder overload"}},
	)
	if result.Action != TriageInvestigate {
		t.Errorf("expected investigate, got %s", result.Action)
	}
	if result.Trigger != "correlation" {
		t.Errorf("expected trigger=correlation, got %s", result.Trigger)
	}
}

func TestTriageLowConfidenceCorrelationFlags(t *testing.T) {
	result := Triage(
		nil,
		[]Deviation{{Metric: "fps", Sigma: 3}, {Metric: "bitrate", Sigma: 2.5}},
		[]MetricCorrelation{{Pattern: PatternEncoderOverload, Confidence: 0.3}},
	)
	// Low confidence correlation doesn't trigger investigate, but 2 deviations → flag
	if result.Action != TriageFlag {
		t.Errorf("expected flag, got %s", result.Action)
	}
}

func TestTriageMultipleDeviationsFlag(t *testing.T) {
	result := Triage(
		nil,
		[]Deviation{{Metric: "fps", Sigma: 3}, {Metric: "bitrate", Sigma: 2.5}},
		nil,
	)
	if result.Action != TriageFlag {
		t.Errorf("expected flag, got %s", result.Action)
	}
	if result.Trigger != "baseline" {
		t.Errorf("expected trigger=baseline, got %s", result.Trigger)
	}
}

func TestTriageSingleDeviationFlag(t *testing.T) {
	result := Triage(
		nil,
		[]Deviation{{Metric: "fps", Current: 20, Baseline: 30, StdDev: 2, Sigma: 5, Direction: "below"}},
		nil,
	)
	if result.Action != TriageFlag {
		t.Errorf("expected flag, got %s", result.Action)
	}
}

func TestTriageOK(t *testing.T) {
	result := Triage(nil, nil, nil)
	if result.Action != TriageOK {
		t.Errorf("expected ok, got %s", result.Action)
	}
}

func TestTriageThresholdTakesPrecedence(t *testing.T) {
	// Both violations and high-confidence correlations — threshold wins.
	result := Triage(
		[]ThresholdViolation{{Message: "bad buffer"}},
		[]Deviation{{Metric: "fps"}},
		[]MetricCorrelation{{Confidence: 0.9, Hypothesis: "network issue"}},
	)
	if result.Action != TriageInvestigate {
		t.Errorf("expected investigate, got %s", result.Action)
	}
	if result.Trigger != "threshold" {
		t.Errorf("expected trigger=threshold, got %s", result.Trigger)
	}
}

func TestTriageFormatReport(t *testing.T) {
	result := TriageResult{
		Action:  TriageFlag,
		Trigger: "baseline",
		Reason:  "1 metric deviated",
		Deviations: []Deviation{
			{Metric: "fps", Current: 20, Baseline: 30, StdDev: 2, Sigma: 5, Direction: "below"},
		},
	}
	report := result.FormatReport()
	if report == "" {
		t.Error("expected non-empty report")
	}
}

func TestCooldownSuppressesRepeatFlags(t *testing.T) {
	cd := NewTriageCooldown(100 * time.Millisecond)

	if !cd.ShouldFlag("t1") {
		t.Error("first flag should pass")
	}
	if cd.ShouldFlag("t1") {
		t.Error("second flag within cooldown should be suppressed")
	}
	// Different tenant unaffected
	if !cd.ShouldFlag("t2") {
		t.Error("different tenant should not be suppressed")
	}

	time.Sleep(150 * time.Millisecond)
	if !cd.ShouldFlag("t1") {
		t.Error("flag after cooldown should pass")
	}
}

func TestCooldownNil(t *testing.T) {
	var cd *TriageCooldown
	if !cd.ShouldFlag("t1") {
		t.Error("nil cooldown should always allow")
	}
}
