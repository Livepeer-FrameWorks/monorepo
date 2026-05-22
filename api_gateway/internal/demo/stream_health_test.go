package demo

import "testing"

func TestGenerateStreamHealthMetricsSatisfiesGraphQLNonNulls(t *testing.T) {
	metrics := GenerateStreamHealthMetrics()
	if len(metrics) == 0 {
		t.Fatal("expected demo stream health metrics")
	}

	for i, metric := range metrics {
		if metric.GetStreamId() != DemoStreamID {
			t.Fatalf("metric %d stream id = %q, want %q", i, metric.GetStreamId(), DemoStreamID)
		}
		if metric.HasIssues == nil {
			t.Fatalf("metric %d has nil has_issues", i)
		}
	}
}
