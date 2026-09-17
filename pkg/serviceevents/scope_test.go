package serviceevents

import "testing"

func TestPlatformScopedAllowsOnlyClusterLifecycleAndOperatorEvents(t *testing.T) {
	for eventType, want := range map[string]bool{
		"cluster_created":           true,
		"cluster_updated":           true,
		"platform_incident_updated": true,
		"incident_updated":          false,
		"cluster_invite_created":    false,
		"tenant_created":            false,
		"stream_created":            false,
		"":                          false,
	} {
		if got := PlatformScoped(eventType); got != want {
			t.Fatalf("PlatformScoped(%q) = %v, want %v", eventType, got, want)
		}
	}
}
