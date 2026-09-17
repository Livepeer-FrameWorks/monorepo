// Package serviceevents holds rules for the service_events stream that every
// producer and consumer must agree on.
package serviceevents

// PlatformIncidentUpdated is Lookout's incident change for the platform
// operator audience. It never carries an envelope tenant: it covers
// platform-scope incidents and every tenant's incidents, and Signalman
// delivers it only on the operator channel.
const PlatformIncidentUpdated = "platform_incident_updated"

const (
	MarketingContactDelivered  = "marketing_contact_delivered"
	MarketingSubscriberCreated = "marketing_subscriber_created"
)

// PlatformScoped reports whether eventType may be produced without a tenant.
// These events describe infrastructure no tenant owns, such as a cluster
// created without an owner, or are addressed to platform operators. Every
// other service event belongs to a tenant.
func PlatformScoped(eventType string) bool {
	switch eventType {
	case "cluster_created", "cluster_updated", PlatformIncidentUpdated,
		MarketingContactDelivered, MarketingSubscriberCreated:
		return true
	default:
		return false
	}
}
