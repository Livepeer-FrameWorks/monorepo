package resolvers

import (
	"fmt"
	"strconv"
	"strings"

	"frameworks/api_gateway/graph/model"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/placement"
	placementpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/media_placement"
)

func placementScopeInput(in *model.MediaPlacementScopeInput) (*placementpb.Scope, error) {
	if in == nil || !in.Kind.IsValid() {
		return nil, fmt.Errorf("tenant or stream scope is required")
	}
	out := &placementpb.Scope{StreamId: placementString(in.StreamID)}
	if in.Kind == model.MediaPlacementScopeKindTenant {
		out.Kind = placementpb.ScopeKind_SCOPE_KIND_TENANT
		if in.StreamID != nil {
			return nil, fmt.Errorf("tenant scope cannot include streamId")
		}
	} else {
		out.Kind = placementpb.ScopeKind_SCOPE_KIND_STREAM
		if out.StreamId == "" || len(out.StreamId) > 255 || strings.TrimSpace(out.StreamId) != out.StreamId {
			return nil, fmt.Errorf("stream scope requires streamId")
		}
	}
	return out, nil
}

func placementRevisionInput(value string) (uint64, error) {
	parsed, err := strconv.ParseUint(value, 10, 63)
	if err != nil || strconv.FormatUint(parsed, 10) != value {
		return 0, fmt.Errorf("revision must be a canonical nonnegative signed-64-bit decimal string")
	}
	return parsed, nil
}

func placementReviewInput(in model.ReviewMediaPlacementChangeInput) (*placementpb.ReviewChangeRequest, error) {
	scope, err := placementScopeInput(in.Scope)
	if err != nil {
		return nil, err
	}
	revision, err := placementRevisionInput(in.ExpectedRevision)
	if err != nil {
		return nil, err
	}
	parent, err := placementRevisionInput(in.ExpectedParentRevision)
	if err != nil {
		return nil, err
	}
	out := &placementpb.ReviewChangeRequest{Scope: scope, ExpectedRevision: revision, ExpectedParentRevision: parent}
	for _, update := range in.Updates {
		if update == nil || !update.Verb.IsValid() || !update.Kind.IsValid() {
			return nil, fmt.Errorf("valid verb and update kind are required")
		}
		wire := &placementpb.VerbUpdate{Verb: placementpb.Verb_VERB_INGEST, Kind: placementpb.UpdateKind_UPDATE_KIND_SET}
		if update.Verb == model.MediaPlacementVerbServe {
			wire.Verb = placementpb.Verb_VERB_SERVE
		}
		if update.Kind == model.MediaPlacementUpdateKindClear {
			wire.Kind = placementpb.UpdateKind_UPDATE_KIND_CLEAR
		}
		wire.Rules, err = placementRulesInput(update.Rules)
		if err != nil {
			return nil, err
		}
		out.Updates = append(out.Updates, wire)
	}
	if err := placement.ValidateReviewChangeRequest(out); err != nil {
		return nil, err
	}
	return out, nil
}

func placementRulesInput(in *model.MediaPlacementRulesInput) (*placementpb.Rules, error) {
	if in == nil {
		return nil, nil
	}
	if in.SchemaVersion != int(placement.SchemaVersion) || in.Constraints == nil {
		return nil, fmt.Errorf("supported schemaVersion and constraints are required")
	}
	out := &placement.Rules{SchemaVersion: placement.SchemaVersion}
	for _, selector := range in.Constraints.Deny {
		converted, err := placementSelectorInput(selector)
		if err != nil {
			return nil, err
		}
		out.Constraints.Deny = append(out.Constraints.Deny, converted)
	}
	if in.Constraints.Allow != nil {
		out.Constraints.Allow = &placement.SelectorSet{}
		for _, selector := range in.Constraints.Allow.Any {
			converted, err := placementSelectorInput(selector)
			if err != nil {
				return nil, err
			}
			out.Constraints.Allow.Any = append(out.Constraints.Allow.Any, converted)
		}
	}
	if in.Preferences != nil {
		out.Preferences = &placement.Preferences{}
		for _, group := range in.Preferences.Groups {
			if group == nil || !group.Order.IsValid() || !group.Spillover.IsValid() {
				return nil, fmt.Errorf("valid placement group order and spillover are required")
			}
			selector, err := placementSelectorInput(group.Match)
			if err != nil {
				return nil, err
			}
			out.Preferences.Groups = append(out.Preferences.Groups, placement.Group{
				ID: group.ID, Match: selector, Order: placement.Order(strings.ToLower(string(group.Order))), Spillover: placement.Spillover(strings.ToLower(string(group.Spillover))),
				MaxDistanceKM: group.MaxDistanceKm, GeoHoleDistanceKM: group.GeoHoleDistanceKm, MinImprovementKM: group.MinImprovementKm,
				PriceCurrency: placementString(group.PriceCurrency), PriceUnit: placementString(group.PriceUnit),
			})
		}
	}
	return placement.RulesToProto(out)
}

func placementSelectorInput(in *model.MediaPlacementSelectorInput) (placement.Selector, error) {
	if in == nil {
		return placement.Selector{}, fmt.Errorf("placement selector cannot be null")
	}
	out := placement.Selector{ClusterIDs: append([]string(nil), in.ClusterIds...), OwnerIDs: append([]string(nil), in.OwnerIds...), Regions: append([]string(nil), in.Regions...)}
	for _, class := range in.Classes {
		if !class.IsValid() {
			return out, fmt.Errorf("invalid placement class")
		}
		out.Classes = append(out.Classes, placement.Class(strings.ToLower(string(class))))
	}
	for _, charging := range in.Charging {
		if !charging.IsValid() {
			return out, fmt.Errorf("invalid placement charging classification")
		}
		out.Charging = append(out.Charging, placement.Charging(strings.ToLower(string(charging))))
	}
	return out, nil
}

func placementString(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}
