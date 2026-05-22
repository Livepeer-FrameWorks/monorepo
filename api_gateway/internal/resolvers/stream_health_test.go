package resolvers

import (
	"testing"

	pb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto"
)

func TestNormalizeStreamHealthMetricsDefaultsHasIssues(t *testing.T) {
	explicit := true
	metrics := []*pb.StreamHealthMetric{
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
