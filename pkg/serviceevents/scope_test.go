package serviceevents

import (
	"testing"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/events"
	eventspb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/events"
)

func TestPlatformScopedAllowsOnlyClusterLifecycleAndOperatorEvents(t *testing.T) {
	for eventType, want := range map[string]bool{
		"cluster_created":           true,
		"cluster_updated":           true,
		"platform_incident_updated": true,
		MarketingContactDelivered:   true,
		MarketingSubscriberCreated:  true,
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

func TestDomainCounterpartsTakeScopeFromTheRegistry(t *testing.T) {
	for legacy, domainType := range domainCounterparts {
		spec, ok := events.Lookup(domainType)
		if !ok {
			t.Fatalf("%s maps to unregistered domain type %s", legacy, domainType)
		}
		want := spec.Scope == eventspb.Scope_SCOPE_PLATFORM
		if got := PlatformScoped(legacy); got != want {
			t.Fatalf("PlatformScoped(%q) = %v, registry scope of %s is %s", legacy, got, domainType, spec.Scope)
		}
	}
}
