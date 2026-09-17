package resolvers

import (
	"context"
	"fmt"
	"strings"
	"time"

	"frameworks/api_gateway/graph/model"
	"frameworks/api_gateway/internal/demo"
	"frameworks/api_gateway/internal/middleware"
	placementpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/media_placement"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

func (r *Resolver) DoMediaPlacementOptions(ctx context.Context, scope model.MediaPlacementScopeInput, filter *model.MediaPlacementOptionsFilter, after *string, first *int) (model.MediaPlacementOptionsResult, error) {
	if denied := r.placementAccess(ctx, false); denied != nil {
		return denied, nil
	}
	wireScope, err := placementScopeInput(&scope)
	if err != nil {
		return placementInvalidInput(err), nil
	}
	request := &placementpb.GetOptionsRequest{Scope: wireScope, After: placementString(after), First: 50}
	if first != nil {
		if *first < 1 || *first > 100 {
			return placementInvalidInput(fmt.Errorf("first must be between 1 and 100")), nil
		}
		request.First = int32(*first)
	}
	if len(request.After) > 2048 {
		return placementInvalidInput(fmt.Errorf("options cursor exceeds bounds")), nil
	}
	if filter != nil {
		request.Filter = &placementpb.OptionsFilter{Query: placementString(filter.Query), ClusterId: strings.TrimSpace(placementString(filter.ClusterID))}
		if len(request.Filter.Query) > 128 || len(request.Filter.ClusterId) > 255 || len(filter.Classes) > 3 {
			return placementInvalidInput(fmt.Errorf("options filter exceeds bounds")), nil
		}
		if filter.Kind != nil {
			if !filter.Kind.IsValid() {
				return placementInvalidInput(fmt.Errorf("unsupported options kind")), nil
			}
			request.Filter.Kind = placementpb.OptionKind(placementpb.OptionKind_value["OPTION_KIND_"+string(*filter.Kind)])
		}
		for _, class := range filter.Classes {
			if !class.IsValid() {
				return placementInvalidInput(fmt.Errorf("unsupported options class")), nil
			}
			request.Filter.Classes = append(request.Filter.Classes, placementpb.ClusterClass(placementpb.ClusterClass_value["CLUSTER_CLASS_"+string(class)]))
		}
	}
	ctx, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()
	var response *placementpb.Options
	if middleware.IsDemoMode(ctx) {
		response, err = demo.GenerateMediaPlacementOptions(request)
	} else {
		response, err = r.Clients.Commodore.GetMediaPlacementOptions(ctx, request)
	}
	if err != nil {
		failure := placementFailure(err)
		if detail, ok := failure.(*model.MediaPlacementError); ok {
			switch detail.Code {
			case model.MediaPlacementErrorCodeRevisionConflict:
				detail.Message = "Available options changed. Restart the search."
			case model.MediaPlacementErrorCodeInvalidInput:
				detail.Message = "The options filter or cursor is invalid. Restart the search."
			case model.MediaPlacementErrorCodeUnavailable:
				detail.Message = "Authorized options are temporarily unavailable. Try searching again."
			}
		}
		return failure, nil
	}
	if response == nil || !proto.Equal(response.GetScope(), wireScope) || len(response.GetNodes()) > int(request.First) || len(response.GetStartCursor()) > 2048 || len(response.GetEndCursor()) > 2048 {
		return placementFailure(status.Error(codes.Internal, "inconsistent placement options response")), nil
	}
	out := &model.MediaPlacementOptionsConnection{Nodes: []*model.MediaPlacementOption{}, PageInfo: &model.PageInfo{HasNextPage: response.GetHasNextPage(), HasPreviousPage: response.GetHasPreviousPage()}}
	if response.GetStartCursor() != "" {
		out.PageInfo.StartCursor = strPtr(response.GetStartCursor())
	}
	if response.GetEndCursor() != "" {
		out.PageInfo.EndCursor = strPtr(response.GetEndCursor())
	}
	for _, option := range response.GetNodes() {
		kind := model.MediaPlacementOptionKind(strings.TrimPrefix(option.GetKind().String(), "OPTION_KIND_"))
		if option == nil || !kind.IsValid() || option.GetId() == "" || option.GetName() == "" {
			return placementFailure(status.Error(codes.Internal, "invalid placement option")), nil
		}
		node := &model.MediaPlacementOption{ID: option.GetId(), Name: option.GetName(), Kind: kind, Eligible: option.GetEligible()}
		if option.GetClusterClass() != placementpb.ClusterClass_CLUSTER_CLASS_UNSPECIFIED {
			class := model.MediaPlacementClass(strings.TrimPrefix(option.GetClusterClass().String(), "CLUSTER_CLASS_"))
			if !class.IsValid() {
				return placementFailure(status.Error(codes.Internal, "invalid placement option class")), nil
			}
			node.ClusterClass = &class
		}
		if option.GetRegion() != "" {
			node.Region = strPtr(option.GetRegion())
		}
		if option.GetOwnerId() != "" {
			node.OwnerID = strPtr(option.GetOwnerId())
		}
		if option.GetReason() != "" {
			node.Reason = strPtr(option.GetReason())
		}
		if option.GetClusterId() != "" {
			node.ClusterID = strPtr(option.GetClusterId())
		}
		out.Nodes = append(out.Nodes, node)
	}
	return out, nil
}
