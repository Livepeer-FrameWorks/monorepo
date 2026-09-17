package placement

import (
	"fmt"
	"math"
	"math/bits"
	"slices"
	"strings"
)

const (
	maxGroups         = 16
	maxLayers         = 8
	maxSelectors      = 32
	maxSelectorValues = 128
	maxCandidates     = 4096
)

func Validate(p *Policy) error {
	if p == nil {
		return nil
	}
	if p.SchemaVersion != SchemaVersion {
		return fmt.Errorf("unsupported placement schema %d", p.SchemaVersion)
	}
	if len(p.Groups) > maxGroups || len(p.Layers) > maxLayers {
		return fmt.Errorf("placement policy exceeds group or constraint limit")
	}
	seen := make(map[string]bool)
	for _, g := range p.Groups {
		if g.ID == "" || strings.TrimSpace(g.ID) != g.ID || len(g.ID) > 128 || seen[g.ID] {
			return fmt.Errorf("invalid or duplicate group ID %q", g.ID)
		}
		seen[g.ID] = true
		if err := validateSelector(g.Match); err != nil {
			return fmt.Errorf("group %s: %w", g.ID, err)
		}
		if g.Spillover != "" && g.Spillover != Never && g.Spillover != CapacityOnly && g.Spillover != GeoHole && g.Spillover != CapacityOrGeoHole {
			return fmt.Errorf("group %s: invalid spillover", g.ID)
		}
		if g.Order != "" && g.Order != DistanceFirst && g.Order != PriceFirst {
			return fmt.Errorf("group %s: invalid ordering", g.ID)
		}
		for _, v := range []float64{g.MaxDistanceKM, g.GeoHoleDistanceKM, g.MinImprovementKM} {
			if !finite(v) || v < 0 || v > 21000 {
				return fmt.Errorf("group %s: invalid distance bound", g.ID)
			}
		}
		geoSpill := g.Spillover == GeoHole || g.Spillover == CapacityOrGeoHole
		if geoSpill && (g.GeoHoleDistanceKM <= 0 || g.MinImprovementKM <= 0) {
			return fmt.Errorf("group %s: geo spill requires distance and positive improvement thresholds", g.ID)
		}
		if !geoSpill && (g.GeoHoleDistanceKM != 0 || g.MinImprovementKM != 0) {
			return fmt.Errorf("group %s: geo thresholds require geo spill", g.ID)
		}
		if g.Order == PriceFirst {
			if !validPriceBasis(g.PriceCurrency, g.PriceUnit) {
				return fmt.Errorf("group %s: price ordering requires currency and unit", g.ID)
			}
		} else if g.PriceCurrency != "" || g.PriceUnit != "" {
			return fmt.Errorf("group %s: price basis requires price ordering", g.ID)
		}
	}
	for _, layer := range p.Layers {
		if len(layer.Deny) > maxSelectors || (layer.Allow != nil && len(layer.Allow.Any) > maxSelectors) {
			return fmt.Errorf("too many placement selectors")
		}
		for _, s := range layer.Deny {
			if err := validateSelector(s); err != nil {
				return err
			}
		}
		if layer.Allow != nil {
			for _, s := range layer.Allow.Any {
				if err := validateSelector(s); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func validateSelector(s Selector) error {
	for _, values := range [][]string{s.ClusterIDs, s.OwnerIDs, s.Regions, s.NodeIDs} {
		if len(values) > maxSelectorValues {
			return fmt.Errorf("too many selector values")
		}
		for _, value := range values {
			if value == "" || strings.TrimSpace(value) != value || len(value) > 255 {
				return fmt.Errorf("invalid selector value")
			}
		}
	}
	if len(s.Classes) > 3 || len(s.Charging) > 2 {
		return fmt.Errorf("too many class selectors")
	}
	for _, c := range s.Classes {
		if c != Official && c != Private && c != Marketplace {
			return fmt.Errorf("invalid cluster class %q", c)
		}
	}
	for _, c := range s.Charging {
		if c != Rated && c != PermanentlyFree {
			return fmt.Errorf("invalid charging class %q", c)
		}
	}
	return nil
}

type match uint8

const (
	noMatch match = iota
	matched
	unknownMatch
)

func candidateClass(c Candidate, tenantID string) Class {
	if c.Official {
		return Official
	}
	if c.OwnerTenantID == "" {
		return ""
	}
	if c.OwnerTenantID == tenantID {
		return Private
	}
	return Marketplace
}

func matches(s Selector, c Candidate, r Request) match {
	unknown := false
	if len(s.ClusterIDs) > 0 && !slices.Contains(s.ClusterIDs, c.ClusterID) {
		return noMatch
	}
	// A cluster-level candidate (no node identity) can neither satisfy an allow nor
	// escape a deny that names nodes.
	if len(s.NodeIDs) > 0 {
		if c.NodeID == "" {
			unknown = true
		} else if !slices.Contains(s.NodeIDs, c.NodeID) {
			return noMatch
		}
	}
	if len(s.OwnerIDs) > 0 {
		if c.OwnerTenantID == "" {
			unknown = true
		} else if !slices.Contains(s.OwnerIDs, c.OwnerTenantID) {
			return noMatch
		}
	}
	if len(s.Regions) > 0 {
		if c.Region == "" {
			unknown = true
		} else if !slices.Contains(s.Regions, c.Region) {
			return noMatch
		}
	}
	if len(s.Classes) > 0 && !slices.Contains(s.Classes, candidateClass(c, r.TenantID)) {
		return noMatch
	}
	if len(s.Charging) > 0 {
		if (c.Charging != Rated && c.Charging != PermanentlyFree) || !c.ChargingUntil.After(r.Now) {
			unknown = true
		} else if !slices.Contains(s.Charging, c.Charging) {
			return noMatch
		}
	}
	if unknown {
		return unknownMatch
	}
	return matched
}

func policyReason(p *Policy, c Candidate, r Request) Reason {
	unknownFacts := false
	for _, layer := range p.Layers {
		for _, deny := range layer.Deny {
			switch matches(deny, c, r) {
			case matched:
				return PolicyDenied
			case unknownMatch:
				unknownFacts = true
			}
		}
		if layer.Allow != nil {
			allowed, unknown := false, false
			for _, allow := range layer.Allow.Any {
				switch matches(allow, c, r) {
				case matched:
					allowed = true
				case unknownMatch:
					unknown = true
				}
			}
			if !allowed {
				if unknown {
					unknownFacts = true
				} else {
					return PolicyDenied
				}
			}
		}
	}
	if unknownFacts {
		return PolicyFactsUnavailable
	}
	return Eligible
}

func feasibility(c Candidate, r Request, requireSource bool) Reason {
	if c.ObservedAt.IsZero() || c.ObservedAt.After(r.Now) || !c.ExpiresAt.After(r.Now) || !c.ExpiresAt.After(c.ObservedAt) {
		return StaleTelemetry
	}
	if c.Capacity == CapacityUnknown || c.Capacity == "" {
		return UnknownCapacity
	}
	if c.Capacity == CapacityUnavailable {
		return NodeUnavailable
	}
	if c.Capacity != CapacityAvailable && c.Capacity != CapacityExhausted {
		return UnknownCapacity
	}
	if c.RAMMax == 0 || c.RAMUsed > c.RAMMax || c.BWLimit == 0 || c.BWAvailable > c.BWLimit || !finite(c.CPUPercent) || c.CPUPercent < 0 || c.CPUPercent > 100 {
		return InvalidMetrics
	}
	if c.Capacity == CapacityExhausted || c.BWAvailable == 0 {
		return CapacityFull
	}
	if requireSource && r.Verb == Serve {
		if c.Presence != Present && c.Presence != Absent && c.Presence != Materializing {
			return NoSourcePath
		}
		if c.Presence != Present && !c.SourceFeasible {
			return NoSourcePath
		}
	}
	return Eligible
}

type ranked struct {
	candidate Candidate
	distance  *float64
	price     *Price
}

type pool struct {
	ready []ranked
	// Only known capacity exhaustion (or complete empty entitlement) permits capacity spill.
	uncertain bool
}

// Evaluate returns an ordered set within the winning priority group. A failed preparation must
// supply updated facts for a new evaluation; it is not permission to use a lower-priority group.
func Evaluate(r Request) (Decision, error) {
	return evaluatePlacement(r, true)
}

func evaluatePlacement(r Request, requireSource bool) (Decision, error) {
	d := Decision{Complete: r.Complete}
	if strings.TrimSpace(r.TenantID) == "" || (r.Verb != Ingest && r.Verb != Serve) || r.Now.IsZero() {
		return d, fmt.Errorf("tenant, media verb and evaluation time are required")
	}
	if r.Location != nil && !validCoordinates(r.Location) {
		return d, fmt.Errorf("invalid request coordinates")
	}
	if r.Verb != Ingest && r.ActiveIngestClusterID != "" {
		return d, fmt.Errorf("ingest fence is only valid for ingest")
	}
	if len(r.Candidates) > maxCandidates {
		return d, fmt.Errorf("too many placement candidates")
	}
	if err := Validate(r.Policy); err != nil {
		return d, err
	}
	p := r.Policy
	if p == nil {
		p = &Policy{SchemaVersion: SchemaVersion, Groups: []Group{{ID: "entitled", Spillover: Never, Order: DistanceFirst}}}
	}
	seen := make(map[[2]string]bool)
	pools := make([]pool, len(p.Groups))
	for _, c := range r.Candidates {
		key := [2]string{c.ClusterID, c.NodeID}
		if c.NodeID == "" || c.ClusterID == "" || seen[key] {
			return d, fmt.Errorf("missing or duplicate candidate identity")
		}
		seen[key] = true
		a := Assessment{ClusterID: c.ClusterID, NodeID: c.NodeID, Reason: Eligible}
		switch {
		case c.TenantID != r.TenantID || !slices.Contains(c.AllowedVerbs, r.Verb):
			a.Reason = NotEntitled
		case candidateClass(c, r.TenantID) == "":
			a.Reason = UnknownOwnership
		case r.ActiveIngestClusterID != "" && c.ClusterID != r.ActiveIngestClusterID:
			a.Reason = ActiveIngestFence
		default:
			a.Reason = policyReason(p, c, r)
		}
		if a.Reason != Eligible {
			if a.Reason == PolicyFactsUnavailable || a.Reason == UnknownOwnership {
				// Missing hard-rule facts cannot prove that a preferred pool is empty.
				for i := range pools {
					pools[i].uncertain = true
				}
			}
			d.Assessments = append(d.Assessments, a)
			continue
		}
		groupIndex := -1
		for i, g := range p.Groups {
			m := matches(g.Match, c, r)
			if m == noMatch {
				continue
			}
			groupIndex = i
			a.GroupID = g.ID
			if m == unknownMatch {
				a.Reason = PolicyFactsUnavailable
				pools[i].uncertain = true
			}
			break
		}
		if groupIndex < 0 {
			a.Reason = PolicyDenied
			d.Assessments = append(d.Assessments, a)
			continue
		}
		g, bucket := p.Groups[groupIndex], &pools[groupIndex]
		if validCoordinates(c.Location) && r.Location != nil {
			distance := distanceKM(*r.Location, *c.Location)
			a.DistanceKM = &distance
		}
		if a.Reason == Eligible {
			a.Reason = feasibility(c, r, requireSource)
		}
		if a.Reason == Eligible && g.MaxDistanceKM > 0 && (a.DistanceKM == nil || *a.DistanceKM > g.MaxDistanceKM) {
			a.Reason = OutsideGeoBound
		}
		var price *Price
		if a.Reason == Eligible && g.Order == PriceFirst {
			price = comparablePrice(c, g, r.Now)
			if price == nil {
				a.Reason = PriceUnavailable
			}
		}
		if a.Reason == Eligible {
			bucket.ready = append(bucket.ready, ranked{candidate: c, distance: a.DistanceKM, price: price})
		} else if a.Reason != CapacityFull {
			bucket.uncertain = true
		}
		d.Assessments = append(d.Assessments, a)
	}
	for i := range pools {
		slices.SortFunc(pools[i].ready, func(a, b ranked) int { return compare(a, b, p.Groups[i].Order) })
	}
	choices, transitions := choose(p.Groups, pools, 0, nil, r.Complete)
	d.Transitions = transitions
	for _, selected := range choices.nodes {
		c := selected.candidate
		d.Choices = append(d.Choices, Choice{ClusterID: c.ClusterID, NodeID: c.NodeID, GroupID: choices.group, DistanceKM: selected.distance, RequiresPull: r.Verb == Serve && c.Presence != Present})
	}
	d.Reason = NoPermittedDestination
	if len(d.Choices) > 0 {
		d.Reason = Selected
		for i := range d.Assessments {
			a := &d.Assessments[i]
			if a.ClusterID == d.Choices[0].ClusterID && a.NodeID == d.Choices[0].NodeID {
				a.Reason = Selected
			}
		}
	} else if !r.Complete {
		d.Reason = ObservationIncomplete
	}
	slices.SortFunc(d.Assessments, func(a, b Assessment) int {
		if n := strings.Compare(a.ClusterID, b.ClusterID); n != 0 {
			return n
		}
		return strings.Compare(a.NodeID, b.NodeID)
	})
	return d, nil
}

type selection struct {
	group string
	nodes []ranked
}

func choose(groups []Group, pools []pool, i int, distanceLimit *float64, complete bool) (selection, []Transition) {
	if i >= len(groups) {
		return selection{}, nil
	}
	g, bucket := groups[i], pools[i]
	var eligible []ranked
	for _, node := range bucket.ready {
		if distanceLimit == nil || (node.distance != nil && *node.distance <= *distanceLimit) {
			eligible = append(eligible, node)
		}
	}
	base := selection{group: g.ID, nodes: eligible}
	if !complete || bucket.uncertain {
		return base, nil
	}
	if len(bucket.ready) == 0 && (g.Spillover == CapacityOnly || g.Spillover == CapacityOrGeoHole) {
		next, transitions := choose(groups, pools, i+1, distanceLimit, complete)
		return next, append([]Transition{{FromGroup: g.ID, Reason: PreferredCapacityExhausted}}, transitions...)
	}
	if len(bucket.ready) > 0 && (g.Spillover == GeoHole || g.Spillover == CapacityOrGeoHole) {
		nearest := math.Inf(1)
		for _, node := range bucket.ready {
			if node.distance == nil {
				return base, nil
			}
			nearest = math.Min(nearest, *node.distance)
		}
		if nearest > g.GeoHoleDistanceKM {
			bound := nearest - g.MinImprovementKM
			if distanceLimit != nil {
				bound = math.Min(bound, *distanceLimit)
			}
			next, transitions := choose(groups, pools, i+1, &bound, complete)
			if len(next.nodes) > 0 {
				return next, append([]Transition{{FromGroup: g.ID, Reason: GeographicHole}}, transitions...)
			}
		}
	}
	return base, nil
}

func compare(a, b ranked, order Order) int {
	if order == PriceFirst {
		if n := compareUint(a.price.AmountMicros, b.price.AmountMicros); n != 0 {
			return n
		}
	}
	if a.distance == nil && b.distance != nil {
		return 1
	}
	if a.distance != nil && b.distance == nil {
		return -1
	}
	if a.distance != nil && b.distance != nil {
		if *a.distance < *b.distance {
			return -1
		}
		if *a.distance > *b.distance {
			return 1
		}
	}
	if n := compareFraction(a.candidate.BWAvailable, a.candidate.BWLimit, b.candidate.BWAvailable, b.candidate.BWLimit); n != 0 {
		return -n
	}
	if a.candidate.CPUPercent < b.candidate.CPUPercent {
		return -1
	}
	if a.candidate.CPUPercent > b.candidate.CPUPercent {
		return 1
	}
	if n := compareFraction(a.candidate.RAMMax-a.candidate.RAMUsed, a.candidate.RAMMax, b.candidate.RAMMax-b.candidate.RAMUsed, b.candidate.RAMMax); n != 0 {
		return -n
	}
	if a.candidate.Presence == Present && b.candidate.Presence != Present {
		return -1
	}
	if a.candidate.Presence != Present && b.candidate.Presence == Present {
		return 1
	}
	if n := strings.Compare(a.candidate.ClusterID, b.candidate.ClusterID); n != 0 {
		return n
	}
	return strings.Compare(a.candidate.NodeID, b.candidate.NodeID)
}

func compareUint(a, b uint64) int {
	if a < b {
		return -1
	}
	if a > b {
		return 1
	}
	return 0
}

// Cross-products retain all 128 bits; large byte counters must not reverse headroom ordering.
func compareFraction(an, ad, bn, bd uint64) int {
	ah, al := bits.Mul64(an, bd)
	bh, bl := bits.Mul64(bn, ad)
	if n := compareUint(ah, bh); n != 0 {
		return n
	}
	return compareUint(al, bl)
}

func finite(v float64) bool { return !math.IsNaN(v) && !math.IsInf(v, 0) }
func validCoordinates(c *Coordinates) bool {
	return c != nil && finite(c.Latitude) && finite(c.Longitude) && math.Abs(c.Latitude) <= 90 && math.Abs(c.Longitude) <= 180
}
func distanceKM(a, b Coordinates) float64 {
	const rad = math.Pi / 180
	lat := math.Sin((b.Latitude - a.Latitude) * rad / 2)
	lon := math.Sin((b.Longitude - a.Longitude) * rad / 2)
	h := lat*lat + math.Cos(a.Latitude*rad)*math.Cos(b.Latitude*rad)*lon*lon
	return 2 * 6371.0088 * math.Asin(math.Sqrt(math.Max(0, math.Min(1, h))))
}
