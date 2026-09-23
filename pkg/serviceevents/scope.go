// Package serviceevents holds rules for the service_events stream that every
// producer and consumer must agree on.
package serviceevents

import (
	"github.com/Livepeer-FrameWorks/monorepo/pkg/events"
	eventspb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/events"
)

// PlatformIncidentUpdated is Lookout's incident change for the platform
// operator audience. It never carries an envelope tenant: it covers
// platform-scope incidents and every tenant's incidents, and Signalman
// delivers it only on the operator channel.
const PlatformIncidentUpdated = "platform_incident_updated"

const (
	MarketingContactDelivered  = "marketing_contact_delivered"
	MarketingSubscriberCreated = "marketing_subscriber_created"
)

// NodeFingerprintUnbound is Quartermaster's audit of a platform operator
// deleting a node fingerprint binding. It is operator activity on
// infrastructure, so it never carries an envelope tenant; the binding's tenant,
// when it had one, rides in the payload.
const NodeFingerprintUnbound = "node.fingerprint_unbound"

// domainCounterparts maps service_events types that Quartermaster still writes
// alongside their domain event to that domain event type, whose registry spec
// decides the scope.
var domainCounterparts = map[string]string{
	"cluster_created": "cluster.created",
	"cluster_updated": "cluster.updated",
}

// PlatformScoped reports whether eventType may be produced on service_events
// without a tenant. Operator-audience events (incidents, marketing activity)
// are platform-scoped by definition and stay on service_events. A service
// event that duplicates a domain event takes the scope the domain event
// registry declares for its counterpart. Every other service event belongs to
// a tenant.
func PlatformScoped(eventType string) bool {
	switch eventType {
	case PlatformIncidentUpdated, MarketingContactDelivered, MarketingSubscriberCreated, NodeFingerprintUnbound:
		return true
	}
	domainType, ok := domainCounterparts[eventType]
	if !ok {
		return false
	}
	spec, ok := events.Lookup(domainType)
	return ok && spec.Scope == eventspb.Scope_SCOPE_PLATFORM
}
