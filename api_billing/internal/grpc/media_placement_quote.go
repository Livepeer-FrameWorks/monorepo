package grpc

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"time"

	"frameworks/api_billing/internal/pricing"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/middleware"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/placement"
	clusterpeerpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/cluster_peer"
	placementpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/media_placement"
	quartermasterpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/quartermaster"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protopath"
	"google.golang.org/protobuf/reflect/protorange"
	"google.golang.org/protobuf/reflect/protoreflect"
)

func (s *PurserServer) GetMediaPlacementQuote(ctx context.Context, request *placementpb.CommercialQuoteRequest) (*placementpb.CommercialQuoteResponse, error) {
	if !middleware.IsServiceCall(ctx) {
		return nil, status.Error(codes.PermissionDenied, "commercial placement quotes require service authentication")
	}
	scope, _, err := placement.CanonicalCommercialQuoteRequest(request)
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "invalid commercial placement quote request")
	}
	if s.db == nil || s.quartermasterClient == nil {
		return nil, status.Error(codes.Unavailable, "commercial placement dependencies unavailable")
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	before, err := s.readPlacementQuoteEntitlement(ctx, scope)
	if err != nil {
		return nil, err
	}
	snapshot, err := pricing.ReadPlacementTariffs(ctx, s.db, scope.GetTenantId(), before.owners)
	if err != nil {
		return nil, placementQuoteRPCError(err)
	}
	// No database transaction is held while current access/consent is rechecked.
	after, err := s.readPlacementQuoteEntitlement(ctx, scope)
	if err != nil {
		return nil, err
	}
	if before.revision != after.revision {
		return nil, status.Error(codes.Aborted, "commercial placement entitlement changed during quote")
	}
	response, err := pricing.ProjectPlacementQuote(snapshot, scope, after.revision, after.accessUntil, time.Now())
	if err != nil {
		return nil, placementQuoteRPCError(err)
	}
	if err := placement.ValidateCommercialQuoteResponse(scope, response, time.Now()); err != nil {
		return nil, placementQuoteRPCError(err)
	}
	if err := ctx.Err(); err != nil {
		return nil, placementQuoteRPCError(err)
	}
	return response, nil
}

type placementQuoteEntitlement struct {
	revision    string
	owners      []*pricing.ClusterOwnershipSnapshot
	accessUntil map[string]time.Time
}

type placementQuoteOwnership map[string]*quartermasterpb.InfrastructureCluster

func (ownership placementQuoteOwnership) GetCluster(_ context.Context, clusterID string) (*quartermasterpb.ClusterResponse, error) {
	cluster := ownership[clusterID]
	if cluster == nil {
		return nil, fmt.Errorf("missing captured placement ownership")
	}
	return &quartermasterpb.ClusterResponse{Cluster: proto.CloneOf(cluster)}, nil
}

func (s *PurserServer) readPlacementQuoteEntitlement(ctx context.Context, request *placementpb.CommercialQuoteRequest) (*placementQuoteEntitlement, error) {
	if err := ctx.Err(); err != nil {
		return nil, placementQuoteRPCError(err)
	}
	readCtx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	response, err := s.quartermasterClient.GetTenantEntitlement(readCtx, request.GetTenantId())
	if err != nil {
		return nil, placementQuoteRPCError(err)
	}
	if response == nil || len(response.GetAllowedClusterIds()) > 4096 || len(response.GetEffectiveAccess()) > 4096 || proto.Size(response) > 16<<20 {
		return nil, status.Error(codes.Unavailable, "invalid commercial entitlement snapshot")
	}
	response = proto.CloneOf(response)
	if wireErr := rejectUnknownQuoteEntitlement(response); wireErr != nil {
		return nil, status.Error(codes.Unavailable, "unsupported commercial entitlement snapshot")
	}
	allowed := make(map[string]bool, len(response.GetAllowedClusterIds()))
	for _, id := range response.GetAllowedClusterIds() {
		if id == "" || allowed[id] {
			return nil, status.Error(codes.Unavailable, "ambiguous commercial entitlement membership")
		}
		allowed[id] = true
	}
	peers := make(map[string]*clusterpeerpb.TenantClusterPeer, len(response.GetEffectiveAccess()))
	for _, peer := range response.GetEffectiveAccess() {
		if peer == nil || peer.GetClusterId() == "" || peers[peer.GetClusterId()] != nil {
			return nil, status.Error(codes.Unavailable, "ambiguous commercial entitlement peers")
		}
		peers[peer.GetClusterId()] = peer
	}
	now := time.Now()
	out := &placementQuoteEntitlement{accessUntil: make(map[string]time.Time, len(request.GetClusterIds()))}
	bound := make([]*clusterpeerpb.TenantClusterPeer, 0, len(request.GetClusterIds()))
	ownership := placementQuoteOwnership{}
	for _, id := range request.GetClusterIds() {
		peer := peers[id]
		if !allowed[id] || peer == nil || !peer.GetAccessActive() || peer.GetSubscriptionStatus() != "active" {
			return nil, status.Error(codes.PermissionDenied, "cluster is not currently entitled for commercial placement quotes")
		}
		if peer.GetAccessSource() < clusterpeerpb.TenantClusterAccessSource_TENANT_CLUSTER_ACCESS_SOURCE_PLATFORM_TIER || peer.GetAccessSource() > clusterpeerpb.TenantClusterAccessSource_TENANT_CLUSTER_ACCESS_SOURCE_OPERATOR_OVERRIDE || peer.GetMediaConsent() == nil || peer.GetMediaConsent().GetRevision() > math.MaxInt64 {
			return nil, status.Error(codes.Unavailable, "commercial access provenance or consent unavailable")
		}
		if peer.GetClusterClass() != "platform_official" && peer.GetClusterClass() != "tenant_private" && peer.GetClusterClass() != "third_party_marketplace" {
			return nil, status.Error(codes.Unavailable, "unknown commercial cluster class")
		}
		if peer.GetAccessSource() == clusterpeerpb.TenantClusterAccessSource_TENANT_CLUSTER_ACCESS_SOURCE_OWNER && peer.GetOwnerTenantId() != request.GetTenantId() {
			return nil, status.Error(codes.Unavailable, "commercial owner access does not match tenant")
		}
		if peer.GetAccessSource() == clusterpeerpb.TenantClusterAccessSource_TENANT_CLUSTER_ACCESS_SOURCE_PLATFORM_TIER && peer.GetClusterClass() != "platform_official" {
			return nil, status.Error(codes.Unavailable, "commercial platform access has invalid class")
		}
		until := now.Add(30 * time.Second)
		if expiry := peer.GetAccessExpiresAt(); expiry != nil {
			if !expiry.IsValid() || !expiry.AsTime().After(now) {
				return nil, status.Error(codes.PermissionDenied, "commercial cluster access expired")
			}
			if expiry.AsTime().Before(until) {
				until = expiry.AsTime()
			}
		}
		out.accessUntil[id] = until
		cluster := &quartermasterpb.InfrastructureCluster{ClusterId: id, IsPlatformOfficial: peer.GetClusterClass() == "platform_official"}
		if peer.GetOwnerTenantId() != "" {
			owner := peer.GetOwnerTenantId()
			cluster.OwnerTenantId = &owner
		}
		ownership[id] = cluster
		captured, captureErr := pricing.CaptureClusterOwnership(ctx, ownership, id)
		if captureErr != nil {
			return nil, status.Error(codes.Unavailable, "invalid commercial cluster ownership")
		}
		out.owners = append(out.owners, captured)
		bound = append(bound, peer)
	}
	revision, err := placement.CommercialEntitlementDigest(request.GetTenantId(), bound)
	if err != nil {
		return nil, status.Error(codes.Unavailable, "commercial entitlement encoding failed")
	}
	out.revision = revision
	if err := readCtx.Err(); err != nil {
		return nil, placementQuoteRPCError(err)
	}
	return out, nil
}

func rejectUnknownQuoteEntitlement(message proto.Message) error {
	return protorange.Range(message.ProtoReflect(), func(values protopath.Values) error {
		if value, ok := values.Index(-1).Value.Interface().(protoreflect.Message); ok && len(value.GetUnknown()) != 0 {
			return fmt.Errorf("unknown commercial entitlement fields")
		}
		return nil
	})
}

func placementQuoteRPCError(err error) error {
	switch {
	case errors.Is(err, context.Canceled), status.Code(err) == codes.Canceled:
		return status.Error(codes.Canceled, "commercial quote canceled")
	case errors.Is(err, context.DeadlineExceeded), status.Code(err) == codes.DeadlineExceeded:
		return status.Error(codes.DeadlineExceeded, "commercial quote deadline exceeded")
	case errors.Is(err, sql.ErrNoRows), errors.Is(err, pricing.ErrPlacementQuoteUnavailable):
		return status.Error(codes.FailedPrecondition, "commercial quote evidence unavailable")
	default:
		return status.Error(codes.Unavailable, "commercial quote evidence read failed")
	}
}
