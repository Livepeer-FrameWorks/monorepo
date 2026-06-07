package diagnostics

import (
	"strings"
	"testing"
)

func TestFormatReportHeaderOnly(t *testing.T) {
	r := TriageResult{Action: "notify", Trigger: "threshold", Reason: "cpu high"}
	out := r.FormatReport()

	for _, want := range []string{"Action: notify", "Trigger: threshold", "Reason: cpu high"} {
		if !strings.Contains(out, want) {
			t.Errorf("report missing %q\n%s", want, out)
		}
	}
	// With no detail slices, the optional sections must be absent.
	for _, absent := range []string{"Threshold Violations:", "Baseline Deviations:", "Correlations:"} {
		if strings.Contains(out, absent) {
			t.Errorf("report unexpectedly contains %q for empty result\n%s", absent, out)
		}
	}
}

func TestFormatReportAllSections(t *testing.T) {
	r := TriageResult{
		Action:       "escalate",
		Trigger:      "correlation",
		Reason:       "multi-signal",
		Violations:   []ThresholdViolation{{Message: "cpu > 90%"}},
		Deviations:   []Deviation{{}},
		Correlations: []MetricCorrelation{{Hypothesis: "disk saturation", Confidence: 0.87}},
	}
	out := r.FormatReport()

	wants := []string{
		"Threshold Violations:",
		"- cpu > 90%",
		"Baseline Deviations:",
		"Correlations:",
		"disk saturation",
		"confidence 0.87",
	}
	for _, want := range wants {
		if !strings.Contains(out, want) {
			t.Errorf("report missing %q\n%s", want, out)
		}
	}
}
