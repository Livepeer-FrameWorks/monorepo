package grpc

import (
	"context"
	"database/sql"
	"errors"
	"slices"
	"strings"
	"time"

	"frameworks/api_tenants/internal/database/quartermasterdb"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/authz"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/database"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/middleware"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/placement"
	placementpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/media_placement"
	quartermasterpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/quartermaster"
	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// tenantDomainEligibility is the tenant state the Navigator alias and custom
// domain outboxes decide on. Callers check EntitlementsObserved separately
// because an unobserved tenant enqueues nothing rather than a removal.
type tenantDomainEligibility struct {
	EntitlementsObserved   bool
	Active                 bool
	CustomSubdomainEnabled bool
	CustomDomainEnabled    bool
	HasActiveCluster       bool
}

// customSubdomain reports whether the tenant holds a published subdomain alias.
func (e tenantDomainEligibility) customSubdomain() bool {
	return e.EntitlementsObserved && e.Active && e.CustomSubdomainEnabled && e.HasActiveCluster
}

// customDomain reports whether the tenant's custom domain is published. A
// custom domain rides the subdomain alias, so it needs both entitlements.
func (e tenantDomainEligibility) customDomain() bool {
	return e.customSubdomain() && e.CustomDomainEnabled
}

func customDomainEligibility(row quartermasterdb.GetTenantCustomDomainEligibilityRow) tenantDomainEligibility {
	return tenantDomainEligibility{
		EntitlementsObserved:   billingEntitlementsObserved(row.BillingEntitlementsObservedAt),
		Active:                 row.IsActive.Valid && row.IsActive.Bool,
		CustomSubdomainEnabled: row.CustomSubdomainEnabled,
		CustomDomainEnabled:    row.CustomDomainEnabled,
		HasActiveCluster:       row.HasCluster,
	}
}

func aliasEligibility(row quartermasterdb.LockTenantAliasEligibilityRow) tenantDomainEligibility {
	return tenantDomainEligibility{
		EntitlementsObserved:   billingEntitlementsObserved(row.BillingEntitlementsObservedAt),
		Active:                 row.IsActive.Valid && row.IsActive.Bool,
		CustomSubdomainEnabled: row.CustomSubdomainEnabled,
		HasActiveCluster:       row.HasCluster,
	}
}

// loadTenantRoutingPeers returns every cluster the tenant is entitled to route
// to. GetClusterRouting's cluster_peers and GetTenantClusterCapabilities both
// read this set, so capabilities never describe a cluster routing would refuse.
func loadTenantRoutingPeers(ctx context.Context, queries *quartermasterdb.Queries, tenantID string) ([]quartermasterdb.ListTenantClusterRoutingPeersRow, error) {
	var rows []quartermasterdb.ListTenantClusterRoutingPeersRow
	err := database.RetryPostgres(ctx, database.DefaultRetryAttempts, 25*time.Millisecond, func() error {
		var queryErr error
		rows, queryErr = queries.ListTenantClusterRoutingPeers(ctx, tenantID)
		return queryErr
	})
	return rows, err
}

// routingPeerRole names a peer's relation to the tenant's routing selection.
func routingPeerRole(clusterID, preferredClusterID, officialClusterID string) string {
	switch clusterID {
	case preferredClusterID:
		return "preferred"
	case officialClusterID:
		return "official"
	default:
		return "subscribed"
	}
}

// Edge service types ReportAliveNodes maintains per advertised capability
// (see edgeServiceTypeDerivations).
const (
	edgeIngestServiceType     = "edge-ingest"
	edgeEgressServiceType     = "edge-egress"
	edgeStorageServiceType    = "edge-storage"
	edgeProcessingServiceType = "edge-processing"
)

// clusterMediaCapabilities combines capacity-owner consent with fresh edge
// reports. Ingest and playback need the matching consent verb, storage and
// processing need any consent verb, and every capability needs a fresh node
// that advertises it.
func clusterMediaCapabilities(consent *placementpb.CapacityConsent, freshServiceTypes map[string]bool) *quartermasterpb.ClusterMediaCapabilities {
	verbs := placement.ConsentVerbs(consent)
	placeable := len(verbs) > 0
	return &quartermasterpb.ClusterMediaCapabilities{
		Ingest:     slices.Contains(verbs, placement.Ingest) && freshServiceTypes[edgeIngestServiceType],
		Playback:   slices.Contains(verbs, placement.Serve) && freshServiceTypes[edgeEgressServiceType],
		Storage:    placeable && freshServiceTypes[edgeStorageServiceType],
		Processing: placeable && freshServiceTypes[edgeProcessingServiceType],
	}
}

// authorizeTenantCapabilityReader admits service and platform-operator
// callers, and tenant users (or API tokens with placement:read) who may read
// media placement for their own tenant.
func authorizeTenantCapabilityReader(ctx context.Context, tenantID string) error {
	authType := ctxkeys.GetAuthType(ctx)
	if authType == "service" || ctxkeys.IsPlatformOperator(ctx) {
		return nil
	}
	if authType != "jwt" && authType != "api_token" {
		return status.Error(codes.Unauthenticated, "tenant identity required")
	}
	if authType == "api_token" && !hasAPITokenPermission(ctxkeys.GetPermissions(ctx), "placement:read") {
		return status.Error(codes.PermissionDenied, "API token lacks placement permission")
	}
	authenticatedTenantID := strings.TrimSpace(middleware.GetTenantID(ctx))
	if authenticatedTenantID == "" {
		return status.Error(codes.Unauthenticated, "tenant identity required")
	}
	if authenticatedTenantID != tenantID {
		return status.Error(codes.PermissionDenied, "tenant access denied")
	}
	decision := authz.Default.Can(ctx, authz.Identity{
		UserID: middleware.GetUserID(ctx), TenantID: authenticatedTenantID, Role: ctxkeys.GetRole(ctx),
	}, authz.ActionReadMediaPlacement, authz.Resource{OwnerTenantID: authenticatedTenantID})
	if !decision.Allow {
		return status.Error(codes.PermissionDenied, "tenant membership required")
	}
	return nil
}

func (s *QuartermasterServer) GetTenantClusterCapabilities(ctx context.Context, req *quartermasterpb.GetTenantClusterCapabilitiesRequest) (*quartermasterpb.GetTenantClusterCapabilitiesResponse, error) {
	tenantID := req.GetTenantId()
	if parsed, err := uuid.Parse(tenantID); err != nil || parsed.String() != tenantID {
		return nil, status.Error(codes.InvalidArgument, "tenant_id must be a UUID")
	}
	if err := authorizeTenantCapabilityReader(ctx, tenantID); err != nil {
		return nil, err
	}

	queries := quartermasterdb.New(s.db)
	observedAt := time.Now().UTC()
	var eligibility quartermasterdb.GetTenantCustomDomainEligibilityRow
	var selection quartermasterdb.GetTenantRoutingSelectionRow
	err := database.RetryPostgres(ctx, database.DefaultRetryAttempts, 25*time.Millisecond, func() error {
		var queryErr error
		if eligibility, queryErr = queries.GetTenantCustomDomainEligibility(ctx, tenantID); queryErr != nil {
			return queryErr
		}
		selection, queryErr = queries.GetTenantRoutingSelection(ctx, tenantID)
		return queryErr
	})
	if errors.Is(err, sql.ErrNoRows) {
		return nil, status.Error(codes.NotFound, "Tenant not found")
	}
	if err != nil {
		return nil, status.Errorf(codes.Internal, "database error: %v", err)
	}

	peers, err := loadTenantRoutingPeers(ctx, queries, tenantID)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "database error resolving tenant routing peers: %v", err)
	}
	clusterIDs := make([]string, 0, len(peers))
	for _, peer := range peers {
		clusterIDs = append(clusterIDs, peer.ClusterID)
	}
	fresh := map[string]map[string]bool{}
	if len(clusterIDs) > 0 {
		var rows []quartermasterdb.ListFreshEdgeCapabilityServicesRow
		err = database.RetryPostgres(ctx, database.DefaultRetryAttempts, 25*time.Millisecond, func() error {
			var queryErr error
			rows, queryErr = queries.ListFreshEdgeCapabilityServices(ctx, quartermasterdb.ListFreshEdgeCapabilityServicesParams{
				ClusterIds: clusterIDs, StaleThresholdSeconds: int32(s.physicalEndpointStaleSeconds),
			})
			return queryErr
		})
		if err != nil {
			return nil, status.Errorf(codes.Internal, "database error resolving edge capabilities: %v", err)
		}
		for _, row := range rows {
			if fresh[row.ClusterID] == nil {
				fresh[row.ClusterID] = map[string]bool{}
			}
			fresh[row.ClusterID][row.ServiceType] = true
		}
	}

	// The preferred cluster is the tenant's primary cluster while it is
	// entitled; otherwise routing falls back to the official cluster.
	entitled := func(clusterID string) bool {
		return clusterID != "" && slices.Contains(clusterIDs, clusterID)
	}
	officialClusterID := selection.OfficialClusterID
	preferredClusterID := selection.PrimaryClusterID.String
	if !entitled(preferredClusterID) && entitled(officialClusterID) {
		preferredClusterID = officialClusterID
	}

	domains := customDomainEligibility(eligibility)
	out := &quartermasterpb.GetTenantClusterCapabilitiesResponse{
		CustomSubdomain: domains.customSubdomain(),
		CustomDomain:    domains.customDomain(),
		ObservedAt:      timestamppb.New(observedAt),
	}
	for _, peer := range peers {
		out.Clusters = append(out.Clusters, &quartermasterpb.TenantClusterCapability{
			ClusterId:   peer.ClusterID,
			ClusterName: peer.ClusterName,
			Role:        routingPeerRole(peer.ClusterID, preferredClusterID, officialClusterID),
			AccessLevel: peer.AccessLevel,
			Media: clusterMediaCapabilities(&placementpb.CapacityConsent{
				AllowIngest: peer.MediaAllowIngest, AllowServe: peer.MediaAllowServe,
			}, fresh[peer.ClusterID]),
		})
	}
	return out, nil
}
