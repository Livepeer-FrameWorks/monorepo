// Package placement evaluates media placement from explicit policy and immutable facts.
// It performs no discovery, authorization RPCs, reservations, or media preparation.
package placement

import "time"

const SchemaVersion uint32 = 1

type Verb string

const (
	Ingest Verb = "ingest"
	Serve  Verb = "serve"
)

type Class string

const (
	Official    Class = "platform_official"
	Private     Class = "tenant_private"
	Marketplace Class = "third_party_marketplace"
)

type Charging string

const (
	ChargingUnknown Charging = "unknown"
	Rated           Charging = "rated"
	PermanentlyFree Charging = "permanently_free"
)

type Capacity string

const (
	CapacityAvailable   Capacity = "available"
	CapacityExhausted   Capacity = "exhausted"
	CapacityUnavailable Capacity = "unavailable"
	CapacityUnknown     Capacity = "unknown"
)

type Presence string

const (
	Present       Presence = "present"
	Materializing Presence = "materializing"
	Absent        Presence = "absent"
)

type Spillover string

const (
	Never             Spillover = "never"
	CapacityOnly      Spillover = "capacity_only"
	GeoHole           Spillover = "geo_hole"
	CapacityOrGeoHole Spillover = "capacity_or_geo_hole"
)

type Order string

const (
	DistanceFirst Order = "distance"
	PriceFirst    Order = "price"
)

type Coordinates struct {
	Latitude  float64
	Longitude float64
}

// Selector combines fields with AND, values within a field with OR. An empty selector matches all.
// Class is relative to the consuming tenant: an owner's marketplace cluster is private to its owner.
// NodeIDs is omitted from the canonical encoding when empty so policies without node selectors keep
// the digests they had before the field existed.
type Selector struct {
	ClusterIDs []string
	OwnerIDs   []string
	Regions    []string
	Classes    []Class
	Charging   []Charging
	NodeIDs    []string `json:",omitempty"`
}

type SelectorSet struct {
	Any []Selector
}

// Constraints from different authorities are intersected. Nil Allow imposes no additional bound;
// a non-nil, empty Allow permits nothing. Denies cannot be erased by a preference or overlay.
type Constraints struct {
	Allow *SelectorSet
	Deny  []Selector
}

type Group struct {
	ID        string
	Match     Selector
	Spillover Spillover
	Order     Order
	// MaxDistanceKM is a hard bound; zero is unbounded. Unknown geography cannot meet a bound.
	MaxDistanceKM float64
	// GeoHoleDistanceKM is a soft spill threshold, not permission to drop hard constraints.
	GeoHoleDistanceKM float64
	MinImprovementKM  float64
	// Price ordering requires one explicit comparable basis; it never performs currency conversion.
	PriceCurrency string
	PriceUnit     string
}

// Policy is the compiled policy for one verb. Layers retain independent hard constraints;
// Groups is the effective preference list, not an index-based merge of tenant and stream lists.
// A nil policy selects entitled feasible capacity; an explicitly empty Groups list denies all.
type Policy struct {
	SchemaVersion uint32
	Layers        []Constraints
	Groups        []Group
}

type Price struct {
	AmountMicros uint64
	Currency     string
	Unit         string
	Revision     string
	ExpiresAt    time.Time
}

// Candidate contains facts already authorized for TenantID and the listed verbs. A coordinator's
// identity is deliberately absent. These facts must never come from an untrusted preview input.
type Candidate struct {
	TenantID         string
	ClusterID        string
	NodeID           string
	OwnerTenantID    string
	Official         bool
	Region           string
	AllowedVerbs     []Verb
	Charging         Charging
	ChargingRevision string
	ChargingUntil    time.Time
	Location         *Coordinates
	ObservedAt       time.Time
	ExpiresAt        time.Time
	Capacity         Capacity
	BWAvailable      uint64 // bytes/second after admission headroom
	BWLimit          uint64 // bytes/second
	CPUPercent       float64
	RAMUsed          uint64
	RAMMax           uint64
	Presence         Presence
	SourceFeasible   bool
	Price            *Price
	Prices           []Price
}

type Request struct {
	TenantID   string
	Verb       Verb
	Now        time.Time
	Location   *Coordinates
	Policy     *Policy
	Candidates []Candidate
	// Complete certifies that every policy-relevant pool was observed. A timeout is not an empty pool.
	Complete bool
	// ActiveIngestClusterID is an ownership fence, not a score or a grant.
	ActiveIngestClusterID string
}

type Reason string

const (
	Selected                   Reason = "selected"
	Eligible                   Reason = "eligible"
	PolicyDenied               Reason = "policy_denied"
	NotEntitled                Reason = "not_entitled"
	UnknownOwnership           Reason = "unknown_ownership"
	PolicyFactsUnavailable     Reason = "policy_facts_unavailable"
	StaleTelemetry             Reason = "stale_telemetry"
	CapacityFull               Reason = "capacity_exhausted"
	NodeUnavailable            Reason = "node_unavailable"
	UnknownCapacity            Reason = "capacity_unknown"
	InvalidMetrics             Reason = "invalid_metrics"
	NoSourcePath               Reason = "no_source_path"
	PriceUnavailable           Reason = "price_unavailable"
	OutsideGeoBound            Reason = "outside_geo_bound"
	InsufficientGeoImprovement Reason = "insufficient_geo_improvement"
	ActiveIngestFence          Reason = "active_ingest_fence"
	PreferredCapacityExhausted Reason = "preferred_capacity_exhausted"
	GeographicHole             Reason = "geographic_hole"
	ObservationIncomplete      Reason = "observation_incomplete"
	NoPermittedDestination     Reason = "no_permitted_destination"
)

type Assessment struct {
	ClusterID  string
	NodeID     string
	GroupID    string
	Reason     Reason
	DistanceKM *float64
}

type Choice struct {
	ClusterID    string
	NodeID       string
	GroupID      string
	DistanceKM   *float64
	RequiresPull bool
}

type Transition struct {
	FromGroup string
	Reason    Reason
}

type Decision struct {
	Reason      Reason
	Choices     []Choice
	Assessments []Assessment
	Transitions []Transition
	Complete    bool
}
