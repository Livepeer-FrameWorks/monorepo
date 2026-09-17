package resolvers

import (
	"context"
	"errors"
	"strings"

	"frameworks/api_gateway/graph/model"
	"frameworks/api_gateway/internal/middleware"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/placement"
	commodorepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"
)

var errSourceLocationUnavailable = errors.New("stream source location is unavailable")

// SourceLocationInputToProto translates the GraphQL source location. Shape
// errors that do not need tenant facts are rejected here; entitlement, node
// ownership and private-source consent are checked by Commodore.
func SourceLocationInputToProto(input *model.SourceLocationInput) (*commodorepb.StreamSourceLocation, *model.ValidationError) {
	out := &commodorepb.StreamSourceLocation{AvoidNodeIds: trimmedIDs(input.AvoidNodeIds)}
	switch input.Mode {
	case model.SourceLocationModeAny:
		if len(input.Clusters) != 0 || len(out.AvoidNodeIds) != 0 {
			return nil, &model.ValidationError{Message: "an ANY source location takes no clusters or avoided nodes", Field: strPtr("sourceLocation")}
		}
		out.Mode = commodorepb.SourceLocationMode_SOURCE_LOCATION_MODE_ANY
	case model.SourceLocationModeRestricted:
		if len(input.Clusters) == 0 {
			return nil, &model.ValidationError{Message: "a RESTRICTED source location needs at least one cluster", Field: strPtr("sourceLocation.clusters")}
		}
		out.Mode = commodorepb.SourceLocationMode_SOURCE_LOCATION_MODE_RESTRICTED
		for _, cluster := range input.Clusters {
			if cluster == nil || strings.TrimSpace(cluster.ClusterID) == "" {
				return nil, &model.ValidationError{Message: "every source location cluster needs a cluster ID", Field: strPtr("sourceLocation.clusters")}
			}
			out.Clusters = append(out.Clusters, &commodorepb.SourceLocationCluster{
				ClusterId: strings.TrimSpace(cluster.ClusterID),
				NodeIds:   trimmedIDs(cluster.NodeIds),
			})
		}
	default:
		return nil, &model.ValidationError{Message: "source location mode must be ANY or RESTRICTED; custom rules are edited in media placement", Field: strPtr("sourceLocation.mode")}
	}
	return out, nil
}

func trimmedIDs(ids []string) []string {
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		if id = strings.TrimSpace(id); id != "" {
			out = append(out, id)
		}
	}
	return out
}

// streamPlacementValidationError surfaces Commodore's placement, entitlement
// and private-source refusals as the reason the caller can act on.
func streamPlacementValidationError(err error) *model.ValidationError {
	if placement.IsNodePlacementNotReady(err) {
		return &model.ValidationError{Message: nodePlacementNotReadyMessage, Field: strPtr("sourceLocation")}
	}
	if vErr := mapInvalidArgument(err); vErr != nil {
		return vErr
	}
	return mapFailedPrecondition(err)
}

// DoStreamSourceLocation reports where a stream's source may be ingested.
// Commodore attaches the location to every stream read; a stream without one
// comes from a Commodore that predates source locations, and reporting ANY for
// it could hide a restriction, so the field fails instead.
func (r *Resolver) DoStreamSourceLocation(ctx context.Context, stream *commodorepb.Stream) (*model.SourceLocation, error) {
	if stream == nil {
		return nil, errSourceLocationUnavailable
	}
	location := stream.GetSourceLocation()
	if location == nil {
		if middleware.IsDemoMode(ctx) {
			return &model.SourceLocation{Mode: model.SourceLocationModeAny, Clusters: []*model.SourceLocationCluster{}, AvoidNodeIds: []string{}}, nil
		}
		return nil, errSourceLocationUnavailable
	}
	out := &model.SourceLocation{Clusters: []*model.SourceLocationCluster{}, AvoidNodeIds: []string{}}
	switch location.GetMode() {
	case commodorepb.SourceLocationMode_SOURCE_LOCATION_MODE_ANY:
		out.Mode = model.SourceLocationModeAny
	case commodorepb.SourceLocationMode_SOURCE_LOCATION_MODE_RESTRICTED:
		out.Mode = model.SourceLocationModeRestricted
		for _, cluster := range location.GetClusters() {
			nodeIDs := append([]string{}, cluster.GetNodeIds()...)
			out.Clusters = append(out.Clusters, &model.SourceLocationCluster{ClusterID: cluster.GetClusterId(), NodeIds: nodeIDs})
		}
		out.AvoidNodeIds = append(out.AvoidNodeIds, location.GetAvoidNodeIds()...)
	case commodorepb.SourceLocationMode_SOURCE_LOCATION_MODE_CUSTOM:
		out.Mode = model.SourceLocationModeCustom
	default:
		return nil, errSourceLocationUnavailable
	}
	return out, nil
}
