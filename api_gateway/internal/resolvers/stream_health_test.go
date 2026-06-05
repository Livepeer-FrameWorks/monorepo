package resolvers

import (
	periscopepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/periscope"
	"testing"
)

func TestNormalizeStreamHealthMetricsDefaultsHasIssues(t *testing.T) {
	explicit := true
	metrics := []*periscopepb.StreamHealthMetric{
		{StreamId: "stream-1"},
		{StreamId: "stream-2", HasIssues: &explicit},
		nil,
	}

	normalizeStreamHealthMetrics(metrics)

	if metrics[0].HasIssues == nil {
		t.Fatal("expected nil has_issues to be defaulted")
	}
	if metrics[0].GetHasIssues() {
		t.Fatal("expected default has_issues to be false")
	}
	if metrics[1].HasIssues == nil || !metrics[1].GetHasIssues() {
		t.Fatal("expected explicit has_issues to be preserved")
	}
}
