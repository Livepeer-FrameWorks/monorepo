package handlers

import (
	"reflect"
	"testing"
)

func TestServiceHealthSummarySnapshot(t *testing.T) {
	summary := newServiceHealthSummary()
	summary.recordResult("bridge", "healthy")
	summary.recordResult("bridge", "unhealthy")
	summary.recordResult("steward", "healthy")
	summary.recordSkipped("skipper")

	byService, healthyServices, unhealthyServices, skippedServices := summary.snapshot()

	if got := byService["bridge"]; got != (serviceHealthCounts{Checked: 2, Healthy: 1, Unhealthy: 1}) {
		t.Fatalf("bridge counts = %+v", got)
	}
	if got := byService["steward"]; got != (serviceHealthCounts{Checked: 1, Healthy: 1}) {
		t.Fatalf("steward counts = %+v", got)
	}
	if got := byService["skipper"]; got != (serviceHealthCounts{Skipped: 1}) {
		t.Fatalf("skipper counts = %+v", got)
	}
	if !reflect.DeepEqual(healthyServices, []string{"bridge", "steward"}) {
		t.Fatalf("healthy services = %v", healthyServices)
	}
	if !reflect.DeepEqual(unhealthyServices, []string{"bridge"}) {
		t.Fatalf("unhealthy services = %v", unhealthyServices)
	}
	if !reflect.DeepEqual(skippedServices, []string{"skipper"}) {
		t.Fatalf("skipped services = %v", skippedServices)
	}
}
