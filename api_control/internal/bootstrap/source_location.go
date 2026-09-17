package bootstrap

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"frameworks/api_control/internal/database/commodoredb"
	"frameworks/api_control/internal/placementpolicy"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/placement"
	placementpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/media_placement"
)

// SourceLocation mirrors cli/pkg/bootstrap.SourceLocationRendered. The renderer
// has already mapped every node to its cluster; absent means any cluster.
type SourceLocation struct {
	Clusters     []SourceLocationCluster `yaml:"clusters"`
	AvoidNodeIDs []string                `yaml:"avoid_node_ids,omitempty"`
}

type SourceLocationCluster struct {
	ClusterID string   `yaml:"cluster_id"`
	NodeIDs   []string `yaml:"node_ids,omitempty"`
}

var errLegacyAllowedClusterIDs = errors.New("allowed_cluster_ids is no longer supported; declare source_location: {clusters: [...]} in the manifest and re-render")

func (l *SourceLocation) placement() (placementpolicy.SourceLocation, error) {
	if l == nil {
		return placementpolicy.SourceLocation{Mode: placementpolicy.SourceLocationAny}, nil
	}
	location := placementpolicy.SourceLocation{Mode: placementpolicy.SourceLocationRestricted, AvoidNodeIDs: l.AvoidNodeIDs}
	for _, cluster := range l.Clusters {
		location.Clusters = append(location.Clusters, placementpolicy.SourceLocationCluster{ClusterID: cluster.ClusterID, NodeIDs: cluster.NodeIDs})
	}
	canonical, err := placementpolicy.CanonicalSourceLocation(location)
	if err != nil {
		return placementpolicy.SourceLocation{}, fmt.Errorf("source_location: %w", err)
	}
	return canonical, nil
}

// NodePlacementChecker is Commodore's node selector gate for placement writes:
// every media cell serving the tenant must attest node placement, and a tenant
// other than the system tenant may only name nodes of clusters it owns, each
// inside the cluster it is listed under.
type NodePlacementChecker interface {
	CheckTenantPlacementNodes(ctx context.Context, tenantID string, nodes []placementpolicy.PlacementNode) error
}

// sourceLocationNodes is one owner's declared source locations that name nodes.
type sourceLocationNodes struct {
	ownerRef string
	streams  []string
	nodes    []placementpolicy.PlacementNode
}

// declaredSourceLocationNodes groups the nodes of every declared source location
// (node_ids paired with their cluster, and avoid_node_ids) by owner tenant ref,
// in declaration order.
func declaredSourceLocationNodes(section CommodoreSection) ([]sourceLocationNodes, error) {
	var out []sourceLocationNodes
	add := func(kind, playbackID, ownerRef string, location placementpolicy.SourceLocation) {
		nodes := location.PlacementNodes()
		if len(nodes) == 0 {
			return
		}
		index := slices.IndexFunc(out, func(group sourceLocationNodes) bool { return group.ownerRef == ownerRef })
		if index < 0 {
			out = append(out, sourceLocationNodes{ownerRef: ownerRef})
			index = len(out) - 1
		}
		out[index].streams = append(out[index].streams, fmt.Sprintf("%s %q", kind, playbackID))
		out[index].nodes = append(out[index].nodes, nodes...)
	}
	for _, ps := range section.PullStreams {
		_, location, err := validatePullStreamShape(ps)
		if err != nil {
			return nil, err
		}
		add("pull_stream", ps.PlaybackID, ps.OwnerTenant.Ref, location)
	}
	for _, ms := range section.MistNativeStreams {
		location, err := validateMistNativeShape(ms)
		if err != nil {
			return nil, err
		}
		add("mist_native_stream", ms.PlaybackID, ms.OwnerTenant.Ref, location)
	}
	for i := range out {
		out[i].nodes = placementpolicy.SortedUniquePlacementNodes(out[i].nodes)
	}
	return out, nil
}

// NodeSourceLocationRequirements describes, for the offline --check pass, each
// declared stream whose source location names nodes. Apply refuses those
// streams until every media cell serving the owner attests node placement.
func NodeSourceLocationRequirements(section CommodoreSection) ([]string, error) {
	groups, err := declaredSourceLocationNodes(section)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, group := range groups {
		for _, stream := range group.streams {
			out = append(out, fmt.Sprintf("%s (owner %s) names nodes; apply requires every media cell serving that tenant to support node placement", stream, group.ownerRef))
		}
	}
	return out, nil
}

// RequireSourceLocationNodePlacement refuses the bootstrap before any write when
// a declared source location names nodes the placement gate would not accept.
// Tenant authorities carry node-bearing stream rules only once every serving
// cell attests node placement, so writing them earlier would leave the stream
// without an authority instead of failing.
func RequireSourceLocationNodePlacement(ctx context.Context, section CommodoreSection, resolver TenantResolver, checker NodePlacementChecker) error {
	groups, err := declaredSourceLocationNodes(section)
	if err != nil || len(groups) == 0 {
		return err
	}
	if resolver == nil || checker == nil {
		return errors.New("RequireSourceLocationNodePlacement: tenant resolver and node placement checker are required")
	}
	for _, group := range groups {
		streams := strings.Join(group.streams, ", ")
		alias, err := AliasFromRef(group.ownerRef)
		if err != nil {
			return fmt.Errorf("%s: %w", streams, err)
		}
		tenantID, err := resolver.Resolve(ctx, alias)
		if err != nil {
			return fmt.Errorf("%s: %w", streams, err)
		}
		if err := checker.CheckTenantPlacementNodes(ctx, tenantID, group.nodes); err != nil {
			if placement.IsNodePlacementNotReady(err) {
				return fmt.Errorf("%s: source_location names nodes, which requires every media cell serving tenant %s to support node placement; upgrade Foghorn in every cell, then rerun bootstrap", streams, group.ownerRef)
			}
			return fmt.Errorf("%s: source_location nodes: %w", streams, err)
		}
	}
	return nil
}

// ReconcileStreamSourceLocations writes every declared pull and managed stream's
// source location as that stream's own ingest placement rules through the
// placement store's system path (actor system:bootstrap). It runs after
// ReconcilePullStreams and ReconcileMistNativeStreams in the same transaction,
// so every declared stream exists; the comparison happens under the placement
// locks and an unchanged location writes nothing.
func ReconcileStreamSourceLocations(ctx context.Context, exec DBTX, section CommodoreSection, resolver TenantResolver) (Result, error) {
	if exec == nil || resolver == nil {
		return Result{}, errors.New("ReconcileStreamSourceLocations: executor and tenant resolver are required")
	}
	queries := commodoredb.New(exec)
	res := Result{}
	apply := func(kind, playbackID, ownerRef string, location placementpolicy.SourceLocation, streamID func(tenantID string) (string, error)) error {
		alias, err := AliasFromRef(ownerRef)
		if err != nil {
			return fmt.Errorf("%s %q: %w", kind, playbackID, err)
		}
		tenantID, err := resolver.Resolve(ctx, alias)
		if err != nil {
			return fmt.Errorf("%s %q: %w", kind, playbackID, err)
		}
		id, err := streamID(tenantID)
		if err != nil {
			return fmt.Errorf("%s %q: find stream: %w", kind, playbackID, err)
		}
		changed, err := applyBootstrapSourceLocation(ctx, exec, tenantID, id, location)
		if err != nil {
			return fmt.Errorf("%s %q: %w", kind, playbackID, err)
		}
		if changed {
			res.Updated = append(res.Updated, playbackID)
		} else {
			res.Noop = append(res.Noop, playbackID)
		}
		return nil
	}
	for _, ps := range section.PullStreams {
		_, location, err := validatePullStreamShape(ps)
		if err != nil {
			return Result{}, err
		}
		if err := apply("pull_stream", ps.PlaybackID, ps.OwnerTenant.Ref, location, func(tenantID string) (string, error) {
			row, lookupErr := queries.GetBootstrapPullStream(ctx, commodoredb.GetBootstrapPullStreamParams{TenantID: tenantID, PlaybackID: ps.PlaybackID})
			return row.StreamID, lookupErr
		}); err != nil {
			return Result{}, err
		}
	}
	for _, ms := range section.MistNativeStreams {
		location, err := validateMistNativeShape(ms)
		if err != nil {
			return Result{}, err
		}
		if err := apply("mist_native_stream", ms.PlaybackID, ms.OwnerTenant.Ref, location, func(tenantID string) (string, error) {
			row, lookupErr := queries.GetBootstrapMistNativeStream(ctx, commodoredb.GetBootstrapMistNativeStreamParams{TenantID: tenantID, PlaybackID: ms.PlaybackID})
			return row.StreamID, lookupErr
		}); err != nil {
			return Result{}, err
		}
	}
	return res, nil
}

// applyBootstrapSourceLocation writes the declared source location as the
// stream's own ingest constraints inside the bootstrap transaction. Bootstrap
// owns the placement of the streams it declares, so it replaces custom own
// ingest constraints; ingest preferences and serve rules are kept.
func applyBootstrapSourceLocation(ctx context.Context, exec DBTX, tenantID, streamID string, location placementpolicy.SourceLocation) (bool, error) {
	result, err := placementpolicy.ApplySystem(ctx, exec, placementpolicy.SystemApplyInput{
		Scope:   placementpolicy.Scope{TenantID: tenantID, Kind: "stream", ID: streamID},
		ActorID: placementpolicy.SystemActorBootstrap,
		Update: func(own *placementpb.PolicySet) ([]*placementpb.VerbUpdate, error) {
			return placementpolicy.SourceLocationUpdate(own, location, true)
		},
	})
	if err != nil {
		return false, fmt.Errorf("apply source_location: %w", err)
	}
	return result.Changed, nil
}
