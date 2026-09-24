package grpc

import (
	"slices"

	mediapb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/media_authority"
	pb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/media_placement"
	"google.golang.org/protobuf/proto"
)

// A JWT key addition or relaxed claim requirement cannot revoke a token the
// previous policy accepted. Key removal, replacement, and tighter claims can.
func playbackAccessRevoked(previous, desired *mediapb.PlaybackPolicy) bool {
	if proto.Equal(previous, desired) || desired == nil || previous == nil ||
		previous.GetKind() == mediapb.PlaybackPolicyKind_PLAYBACK_POLICY_KIND_DENY ||
		desired.GetKind() == mediapb.PlaybackPolicyKind_PLAYBACK_POLICY_KIND_PUBLIC {
		return false
	}
	if previous.GetKind() != desired.GetKind() {
		return true
	}
	if originsNarrowed(previous.GetAllowedOrigins(), desired.GetAllowedOrigins()) {
		return true
	}
	if desired.GetKind() != mediapb.PlaybackPolicyKind_PLAYBACK_POLICY_KIND_JWT {
		return false
	}
	oldJWT, newJWT := previous.GetJwt(), desired.GetJwt()
	if alternativesNarrowed(oldJWT.GetRequiredAudiences(), newJWT.GetRequiredAudiences()) {
		return true
	}
	for claim, value := range newJWT.GetRequiredClaimsJson() {
		if oldValue, ok := oldJWT.GetRequiredClaimsJson()[claim]; !ok || oldValue != value {
			return true
		}
	}
	for _, key := range oldJWT.GetActiveKeys() {
		if len(oldJWT.GetAllowedKeyIds()) != 0 && !slices.Contains(oldJWT.GetAllowedKeyIds(), key.GetKeyId()) {
			continue
		}
		if len(newJWT.GetAllowedKeyIds()) != 0 && !slices.Contains(newJWT.GetAllowedKeyIds(), key.GetKeyId()) {
			return true
		}
		if !slices.ContainsFunc(newJWT.GetActiveKeys(), func(candidate *mediapb.PlaybackSigningKey) bool { return proto.Equal(key, candidate) }) {
			return true
		}
	}
	return false
}

// originsNarrowed reports whether desired admits fewer viewer origins than
// previous. An empty list and one containing "*" both admit every origin.
func originsNarrowed(previous, desired []string) bool {
	unrestricted := func(origins []string) bool { return len(origins) == 0 || slices.Contains(origins, "*") }
	if unrestricted(desired) {
		return false
	}
	return unrestricted(previous) || slices.ContainsFunc(previous, func(origin string) bool { return !slices.Contains(desired, origin) })
}

// Empty alternatives mean unrestricted, not deny-all.
func alternativesNarrowed[T comparable](previous, desired []T) bool {
	return len(desired) != 0 && (len(previous) == 0 || slices.ContainsFunc(previous, func(value T) bool { return !slices.Contains(desired, value) }))
}

func placementAccessChanged(previous, desired *pb.PolicySet) bool {
	return placementRulesChanged(previous.GetIngest(), desired.GetIngest()) || placementRulesChanged(previous.GetServe(), desired.GetServe())
}

func placementRulesChanged(previous, desired *pb.Rules) bool {
	// Clearing an override leaves tenant restrictions intact. Revisions alone
	// say nothing about access, and must not cause a dependency-outage denial.
	if desired == nil || proto.Equal(previous, desired) {
		return false
	}
	if placementPreferencesNarrowed(previous.GetPreferences(), desired.GetPreferences()) {
		return true
	}
	oldConstraints, newConstraints := previous.GetConstraints(), desired.GetConstraints()
	if newConstraints.GetAllow() != nil {
		if oldConstraints.GetAllow() == nil {
			return true
		}
		for _, old := range oldConstraints.GetAllow().GetAny() {
			if !slices.ContainsFunc(newConstraints.GetAllow().GetAny(), func(next *pb.Selector) bool { return placementSelectorCovers(next, old) }) {
				return true
			}
		}
	}
	for _, next := range newConstraints.GetDeny() {
		if !slices.ContainsFunc(oldConstraints.GetDeny(), func(old *pb.Selector) bool { return placementSelectorCovers(old, next) }) {
			return true
		}
	}
	return false
}

func placementSelectorCovers(wider, narrower *pb.Selector) bool {
	return !alternativesNarrowed(narrower.GetClusterIds(), wider.GetClusterIds()) &&
		!alternativesNarrowed(narrower.GetNodeIds(), wider.GetNodeIds()) &&
		!alternativesNarrowed(narrower.GetOwnerIds(), wider.GetOwnerIds()) &&
		!alternativesNarrowed(narrower.GetRegions(), wider.GetRegions()) &&
		!alternativesNarrowed(narrower.GetClasses(), wider.GetClasses()) &&
		!alternativesNarrowed(narrower.GetCharging(), wider.GetCharging())
}

// Keeping every existing group in order and appending fallbacks cannot remove
// an accepted route. Reordering or changing spill conditions is not such a proof.
func placementPreferencesNarrowed(previous, desired *pb.Preferences) bool {
	if desired == nil || proto.Equal(previous, desired) {
		return false
	}
	if previous == nil || len(desired.Groups) < len(previous.Groups) {
		return true
	}
	for i, old := range previous.Groups {
		next := desired.Groups[i]
		if !placementSelectorCovers(next.GetMatch(), old.GetMatch()) ||
			(next.GetMaxDistanceKm() > 0 && (old.GetMaxDistanceKm() == 0 || next.GetMaxDistanceKm() < old.GetMaxDistanceKm())) {
			return true
		}
		oldRules, newRules := proto.CloneOf(old), proto.CloneOf(next)
		if oldRules == nil {
			oldRules = &pb.Group{}
		}
		if newRules == nil {
			newRules = &pb.Group{}
		}
		oldRules.Id, newRules.Id = "", ""
		oldRules.Match, newRules.Match = nil, nil
		oldRules.MaxDistanceKm, newRules.MaxDistanceKm = 0, 0
		if !proto.Equal(oldRules, newRules) {
			return true
		}
	}
	return false
}
