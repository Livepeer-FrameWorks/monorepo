package resolvers

import (
	"fmt"
	"math"
	"strconv"
	"strings"

	"frameworks/api_gateway/graph/model"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/placement"
	placementpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/media_placement"
	"google.golang.org/protobuf/proto"
)

func placementPolicyOutput(in *placementpb.PolicyState) (*model.MediaPlacementPolicyState, error) {
	if in == nil || in.GetActions() == nil || in.GetFeatures() == nil || in.GetFeatures().GetSchemaVersion() != placement.SchemaVersion || in.GetActiveParentRevision() > math.MaxInt64 {
		return nil, fmt.Errorf("invalid placement policy response")
	}
	scope, err := placementScopeOutput(in.GetScope())
	if err != nil {
		return nil, err
	}
	for _, set := range []*placementpb.PolicySet{in.GetOwn(), in.GetInherited(), in.GetActive()} {
		if validationErr := placement.ValidatePolicySet(set); validationErr != nil {
			return nil, validationErr
		}
	}
	rollout, err := placementRolloutOutput(in.GetRollout())
	if err != nil {
		return nil, err
	}
	if in.GetActive().GetRevision() > in.GetOwn().GetRevision() || in.GetActiveParentRevision() > in.GetInherited().GetRevision() ||
		(in.GetActive() == nil && in.GetActiveParentRevision() != 0) ||
		(scope.Kind == model.MediaPlacementScopeKindTenant && in.GetInherited().GetRevision() != 0) {
		return nil, fmt.Errorf("inconsistent placement activation revisions")
	}
	hasActive := in.GetActive() != nil && (in.GetActive().GetRevision() != 0 || in.GetActiveParentRevision() != 0)
	if rollout.Status == model.MediaPlacementRolloutStatusEffective &&
		(!hasActive || !proto.Equal(in.GetActive(), in.GetOwn()) || in.GetActiveParentRevision() != in.GetInherited().GetRevision()) {
		return nil, fmt.Errorf("effective placement lacks matching active policy")
	}
	actions, features := in.GetActions(), in.GetFeatures()
	out := &model.MediaPlacementPolicyState{
		Scope: scope, Revision: strconv.FormatUint(in.GetOwn().GetRevision(), 10), ParentRevision: strconv.FormatUint(in.GetInherited().GetRevision(), 10), Rollout: rollout,
		Verbs:    []*model.MediaPlacementVerbPolicy{},
		Actions:  &model.MediaPlacementActions{CanRead: actions.GetCanRead(), CanPreview: actions.GetCanPreview(), CanManage: actions.GetCanManage(), CanInspectPrivateCandidates: actions.GetCanInspectPrivateCandidates()},
		Features: &model.MediaPlacementFeatures{SchemaVersion: int(features.GetSchemaVersion()), GeographicSpillover: features.GetGeographicSpillover(), PriceOrdering: features.GetPriceOrdering(), SupportedPresets: append([]string{}, features.GetSupportedPresets()...)},
	}
	if hasActive {
		out.ActiveRevision = strPtr(strconv.FormatUint(in.GetActive().GetRevision(), 10))
		out.ActiveParentRevision = strPtr(strconv.FormatUint(in.GetActiveParentRevision(), 10))
	}
	for _, verb := range []placement.Verb{placement.Ingest, placement.Serve} {
		own, inherited := in.GetOwn().GetIngest(), in.GetInherited().GetIngest()
		if verb == placement.Serve {
			own, inherited = in.GetOwn().GetServe(), in.GetInherited().GetServe()
		}
		ownRules, ownErr := placement.RulesFromProto(own)
		inheritedRules, inheritedErr := placement.RulesFromProto(inherited)
		if ownErr != nil || inheritedErr != nil {
			return nil, fmt.Errorf("invalid placement rules response")
		}
		compiled, compileErr := placement.Compile(inheritedRules, ownRules)
		if compileErr != nil {
			return nil, compileErr
		}
		digest, digestErr := placement.Digest(compiled)
		if digestErr != nil {
			return nil, digestErr
		}
		effective := &model.MediaPlacementEffectivePolicy{SchemaVersion: int(placement.SchemaVersion), Digest: digest, Layers: []*model.MediaPlacementConstraints{}, Groups: []*model.MediaPlacementGroup{}}
		if compiled == nil {
			// Absence uses entitled nearest capacity; an explicit empty group list denies all.
			effective.Groups = append(effective.Groups, placementGroupOutput(placement.Group{ID: "entitled", Order: placement.DistanceFirst, Spillover: placement.Never}))
		} else {
			for _, layer := range compiled.Layers {
				effective.Layers = append(effective.Layers, placementConstraintsOutput(layer))
			}
			for _, group := range compiled.Groups {
				effective.Groups = append(effective.Groups, placementGroupOutput(group))
			}
		}
		out.Verbs = append(out.Verbs, &model.MediaPlacementVerbPolicy{Verb: model.MediaPlacementVerb(strings.ToUpper(string(verb))), OwnRules: placementRulesOutput(ownRules), InheritedRules: placementRulesOutput(inheritedRules), RequestedEffective: effective})
	}
	return out, nil
}

func placementScopeOutput(in *placementpb.Scope) (*model.MediaPlacementScope, error) {
	if in == nil {
		return nil, fmt.Errorf("missing placement scope")
	}
	out := &model.MediaPlacementScope{Kind: model.MediaPlacementScopeKindTenant}
	switch in.GetKind() {
	case placementpb.ScopeKind_SCOPE_KIND_TENANT:
		if in.GetStreamId() != "" {
			return nil, fmt.Errorf("invalid tenant placement scope")
		}
	case placementpb.ScopeKind_SCOPE_KIND_STREAM:
		if in.GetStreamId() == "" {
			return nil, fmt.Errorf("invalid stream placement scope")
		}
		out.Kind, out.StreamID = model.MediaPlacementScopeKindStream, strPtr(in.GetStreamId())
	default:
		return nil, fmt.Errorf("unsupported placement scope")
	}
	return out, nil
}

func placementRulesOutput(in *placement.Rules) *model.MediaPlacementRules {
	if in == nil {
		return nil
	}
	out := &model.MediaPlacementRules{SchemaVersion: int(in.SchemaVersion), Constraints: placementConstraintsOutput(in.Constraints)}
	if in.Preferences != nil {
		out.Preferences = &model.MediaPlacementPreferences{Groups: []*model.MediaPlacementGroup{}}
		for _, group := range in.Preferences.Groups {
			out.Preferences.Groups = append(out.Preferences.Groups, placementGroupOutput(group))
		}
	}
	return out
}

func placementConstraintsOutput(in placement.Constraints) *model.MediaPlacementConstraints {
	out := &model.MediaPlacementConstraints{Deny: []*model.MediaPlacementSelector{}}
	if in.Allow != nil {
		out.Allow = &model.MediaPlacementAllow{Any: []*model.MediaPlacementSelector{}}
		for _, selector := range in.Allow.Any {
			out.Allow.Any = append(out.Allow.Any, placementSelectorOutput(selector))
		}
	}
	for _, selector := range in.Deny {
		out.Deny = append(out.Deny, placementSelectorOutput(selector))
	}
	return out
}

func placementSelectorOutput(in placement.Selector) *model.MediaPlacementSelector {
	out := &model.MediaPlacementSelector{ClusterIds: append([]string{}, in.ClusterIDs...), OwnerIds: append([]string{}, in.OwnerIDs...), Regions: append([]string{}, in.Regions...), Classes: []model.MediaPlacementClass{}, Charging: []model.MediaPlacementCharging{}}
	for _, class := range in.Classes {
		out.Classes = append(out.Classes, model.MediaPlacementClass(strings.ToUpper(string(class))))
	}
	for _, charging := range in.Charging {
		out.Charging = append(out.Charging, model.MediaPlacementCharging(strings.ToUpper(string(charging))))
	}
	return out
}

func placementGroupOutput(in placement.Group) *model.MediaPlacementGroup {
	order, spillover := in.Order, in.Spillover
	if order == "" {
		order = placement.DistanceFirst
	}
	if spillover == "" {
		spillover = placement.Never
	}
	out := &model.MediaPlacementGroup{ID: in.ID, Match: placementSelectorOutput(in.Match), Order: model.MediaPlacementOrder(strings.ToUpper(string(order))), Spillover: model.MediaPlacementSpillover(strings.ToUpper(string(spillover))),
		MaxDistanceKm: in.MaxDistanceKM, GeoHoleDistanceKm: in.GeoHoleDistanceKM, MinImprovementKm: in.MinImprovementKM}
	if in.PriceCurrency != "" {
		out.PriceCurrency = strPtr(in.PriceCurrency)
	}
	if in.PriceUnit != "" {
		out.PriceUnit = strPtr(in.PriceUnit)
	}
	return out
}

func placementRolloutOutput(in *placementpb.Rollout) (*model.MediaPlacementRollout, error) {
	if in == nil || in.GetRequiredRecipients() > math.MaxInt32 || in.GetAppliedRecipients() > in.GetRequiredRecipients() {
		return nil, fmt.Errorf("invalid placement rollout")
	}
	state := model.MediaPlacementRolloutStatus(strings.TrimPrefix(in.GetStatus().String(), "ROLLOUT_STATUS_"))
	if !state.IsValid() {
		return nil, fmt.Errorf("unknown placement rollout")
	}
	if state == model.MediaPlacementRolloutStatusEffective && (in.GetAppliedRecipients() != in.GetRequiredRecipients() || len(in.GetPendingRecipients()) != 0) {
		return nil, fmt.Errorf("effective placement has pending recipients")
	}
	out := &model.MediaPlacementRollout{Status: state, RequiredRecipients: int(in.GetRequiredRecipients()), AppliedRecipients: int(in.GetAppliedRecipients()), ExistingSessionsRetained: in.GetExistingSessionsRetained(), PendingRecipients: []*model.MediaPlacementRecipient{}}
	if in.GetUpdatedAt() != nil {
		if !in.GetUpdatedAt().IsValid() {
			return nil, fmt.Errorf("invalid placement rollout timestamp")
		}
		value := in.GetUpdatedAt().AsTime()
		out.UpdatedAt = &value
	}
	for _, recipient := range in.GetPendingRecipients() {
		status := model.MediaPlacementRolloutStatus(strings.ToUpper(recipient.GetStatus()))
		if recipient == nil || !status.IsValid() {
			return nil, fmt.Errorf("invalid placement recipient")
		}
		converted := &model.MediaPlacementRecipient{ID: recipient.GetId(), Name: recipient.GetName(), Status: status}
		if recipient.GetReason() != "" {
			converted.Reason = strPtr(recipient.GetReason())
		}
		if recipient.GetAuthorityExpiresAt() != nil {
			if !recipient.GetAuthorityExpiresAt().IsValid() {
				return nil, fmt.Errorf("invalid recipient expiry")
			}
			value := recipient.GetAuthorityExpiresAt().AsTime()
			converted.AuthorityExpiresAt = &value
		}
		out.PendingRecipients = append(out.PendingRecipients, converted)
	}
	return out, nil
}

func placementChangeOutput(in *placementpb.Change) (*model.MediaPlacementChange, error) {
	if in == nil || in.GetCreatedAt() == nil || !in.GetCreatedAt().IsValid() || in.GetRevision() > math.MaxInt64 || in.GetParentRevision() > math.MaxInt64 {
		return nil, fmt.Errorf("invalid placement change response")
	}
	scope, err := placementScopeOutput(in.GetScope())
	if err != nil {
		return nil, err
	}
	rollout, err := placementRolloutOutput(in.GetRollout())
	if err != nil {
		return nil, err
	}
	return &model.MediaPlacementChange{Scope: scope, IdempotencyKey: in.GetIdempotencyKey(), Revision: strconv.FormatUint(in.GetRevision(), 10), ParentRevision: strconv.FormatUint(in.GetParentRevision(), 10), Digest: in.GetDigest(), Rollout: rollout, CreatedAt: in.GetCreatedAt().AsTime()}, nil
}

func placementReviewOutput(in *placementpb.Review) (*model.MediaPlacementReview, error) {
	if in == nil || in.GetExpiresAt() == nil || !in.GetExpiresAt().IsValid() || in.GetImpact() == nil || in.GetImpact().GetAffectedStreams() > math.MaxInt32 || in.GetImpact().GetActivePublishers() > math.MaxInt32 {
		return nil, fmt.Errorf("invalid placement review response")
	}
	out := &model.MediaPlacementReview{ReviewToken: in.GetReviewToken(), Digest: in.GetDigest(), ExpiresAt: in.GetExpiresAt().AsTime(), Differences: []*model.MediaPlacementDifference{}, Warnings: []*model.MediaPlacementWarning{},
		Impact: &model.MediaPlacementImpact{AffectedStreams: int(in.GetImpact().GetAffectedStreams()), ActivePublishers: int(in.GetImpact().GetActivePublishers()), Complete: in.GetImpact().GetComplete(), ExistingSessionsRetained: in.GetImpact().GetExistingSessionsRetained()}}
	for _, difference := range in.GetDifferences() {
		if difference == nil {
			return nil, fmt.Errorf("invalid placement difference")
		}
		out.Differences = append(out.Differences, &model.MediaPlacementDifference{Path: difference.GetPath(), Label: difference.GetLabel(), Before: difference.GetBefore(), After: difference.GetAfter()})
	}
	for _, warning := range in.GetWarnings() {
		if warning == nil {
			return nil, fmt.Errorf("invalid placement warning")
		}
		out.Warnings = append(out.Warnings, &model.MediaPlacementWarning{ID: warning.GetId(), Message: warning.GetMessage(), Severity: model.MediaPlacementWarningSeverityWarning, AcknowledgementRequired: warning.GetAcknowledgementRequired()})
	}
	return out, nil
}
