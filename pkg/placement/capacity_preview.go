package placement

import (
	"slices"
	"time"
)

// PreviewComparisonPrice returns the same fresh comparison used by ranking,
// detached from observation state. Distance-ordered groups have no chosen basis.
func PreviewComparisonPrice(candidate Candidate, group Group, now time.Time) *Price {
	if group.Order != PriceFirst {
		return nil
	}
	price := comparablePrice(candidate, group, now)
	if price == nil {
		return nil
	}
	copy := *price
	return &copy
}

// CapacityChoice describes policy-ranked capacity without any source or preparation claim.
// It is deliberately not a Choice accepted by destination preparation.
type CapacityChoice struct {
	ClusterID  string
	NodeID     string
	GroupID    string
	DistanceKM *float64
}

type CapacityDecision struct {
	Reason      Reason
	Choices     []CapacityChoice
	Assessments []Assessment
	Transitions []Transition
	Complete    bool
}

// EvaluateCapacity previews observed capacity when no content source is evaluated.
// All entitlement, ownership, policy, price, freshness, geo, health and spillover
// checks are shared with Evaluate. Source facts neither affect ranking nor become
// invented presence. Use Evaluate for source-aware serving and live routing.
func EvaluateCapacity(r Request) (CapacityDecision, error) {
	r.Candidates = slices.Clone(r.Candidates)
	for index := range r.Candidates {
		r.Candidates[index].Presence = ""
		r.Candidates[index].SourceFeasible = false
	}
	decision, err := evaluatePlacement(r, false)
	out := CapacityDecision{Reason: decision.Reason, Assessments: decision.Assessments, Transitions: decision.Transitions, Complete: decision.Complete}
	if err != nil {
		return out, err
	}
	for _, choice := range decision.Choices {
		out.Choices = append(out.Choices, CapacityChoice{ClusterID: choice.ClusterID, NodeID: choice.NodeID, GroupID: choice.GroupID, DistanceKM: choice.DistanceKM})
	}
	return out, nil
}
