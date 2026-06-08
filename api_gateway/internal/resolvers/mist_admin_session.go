package resolvers

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"frameworks/api_gateway/graph/model"
	"frameworks/api_gateway/internal/middleware"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/auth"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/globalid"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	commodorepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"
)

// DoOpenMistAdminSession is the Gateway's first wall on Mist-admin-UI
// access. Two layers enforce node ownership end-to-end because Mist admin
// can read local files and run processes on the edge host.
//
//  1. Here: load the node + cluster via Quartermaster and verify that the
//     caller's platform identity actually owns the node, before paying
//     for the mint round-trip.
//  2. Commodore.MintMistAdminSession: same check on its trusted gRPC
//     context. This keeps token minting fail-closed even when a caller
//     enters Commodore through a different path.
//
// Commodore is the authority; this resolver fails closed when it can't
// reach the same conclusion.
func (r *Resolver) DoOpenMistAdminSession(ctx context.Context, input model.OpenMistAdminSessionInput) (model.OpenMistAdminSessionResult, error) {
	if middleware.IsDemoMode(ctx) {
		return nil, errDemoUnavailable("Mist admin sessions")
	}
	nodeID := strings.TrimSpace(input.NodeID)
	if nodeID == "" {
		return &model.ValidationError{Message: "nodeId is required", Field: strPtr("nodeId")}, nil
	}

	// Resolver-side baseline: must be authenticated. Commodore re-checks.
	userID := strings.TrimSpace(ctxkeys.GetUserID(ctx))
	tenantID := strings.TrimSpace(ctxkeys.GetTenantID(ctx))
	role := strings.TrimSpace(ctxkeys.GetRole(ctx))
	if userID == "" {
		return &model.AuthError{Message: "authentication required"}, nil
	}

	// Resolver-side ownership wall (first defense). Quartermaster is the
	// authority for node→cluster and cluster ownership/kind.
	if r.Clients == nil || r.Clients.Quartermaster == nil {
		r.Logger.Error("openMistAdminSession: quartermaster client unavailable")
		return nil, fmt.Errorf("quartermaster unavailable")
	}
	if typ, rawID, ok := globalid.Decode(nodeID); ok {
		if typ != globalid.TypeInfrastructureNode {
			return &model.ValidationError{Message: "nodeId must reference an infrastructure node", Field: strPtr("nodeId")}, nil
		}
		nodeResp, err := r.Clients.Quartermaster.GetNode(ctx, rawID)
		if err != nil || nodeResp == nil || nodeResp.GetNode() == nil {
			if status.Code(err) == codes.NotFound {
				return &model.AuthError{Message: "node admin access denied"}, nil
			}
			r.Logger.WithError(err).WithField("node_id", rawID).
				Error("openMistAdminSession: GetNode failed")
			if err != nil {
				return nil, fmt.Errorf("resolve node: %w", err)
			}
			return nil, errors.New("resolve node: empty node response")
		}
		nodeID = strings.TrimSpace(nodeResp.GetNode().GetNodeId())
		if nodeID == "" {
			return &model.AuthError{Message: "node admin access denied"}, nil
		}
	}
	ownerResp, err := r.Clients.Quartermaster.GetNodeOwner(ctx, nodeID)
	if err != nil {
		if status.Code(err) == codes.NotFound {
			// Do NOT leak whether the node exists to unauthorized callers
			// — return AuthError uniformly. Real not-found surfaces only
			// for callers who pass the ownership check below.
			return &model.AuthError{Message: "node admin access denied"}, nil
		}
		r.Logger.WithError(err).WithField("node_id", nodeID).
			Error("openMistAdminSession: GetNodeOwner failed")
		return nil, fmt.Errorf("resolve node owner: %w", err)
	}
	clusterID := strings.TrimSpace(ownerResp.GetClusterId())
	if clusterID == "" {
		return &model.AuthError{Message: "node admin access denied"}, nil
	}
	clusterResp, err := r.Clients.Quartermaster.GetCluster(ctx, clusterID)
	if err != nil || clusterResp == nil || clusterResp.GetCluster() == nil {
		r.Logger.WithError(err).WithField("cluster_id", clusterID).
			Error("openMistAdminSession: GetCluster failed")
		if err != nil {
			return nil, fmt.Errorf("resolve cluster: %w", err)
		}
		return nil, errors.New("resolve cluster: empty cluster response")
	}
	if !auth.CanAdminMistNode(strings.TrimSpace(ownerResp.GetOwnerTenantId()), tenantID, role) {
		r.Logger.WithFields(logging.Fields{
			"node_id":              nodeID,
			"cluster_id":           clusterID,
			"caller_user_id":       userID,
			"caller_tenant_id":     tenantID,
			"caller_role":          role,
			"is_platform_official": clusterResp.GetCluster().GetIsPlatformOfficial(),
		}).Warn("openMistAdminSession denied: caller does not own node")
		return &model.AuthError{Message: "node admin access denied"}, nil
	}

	// Commodore mints with its own trusted-context ownership check.
	mintResp, err := r.Clients.Commodore.MintMistAdminSession(ctx, &commodorepb.MintMistAdminSessionRequest{
		NodeId: nodeID,
	})
	if err != nil {
		// Commodore disagreeing with the resolver after the resolver
		// said "ok" is itself a signal — log it loudly. Map upstream
		// PermissionDenied / Unauthenticated → AuthError.
		code := status.Code(err)
		if code == codes.PermissionDenied || code == codes.Unauthenticated {
			r.Logger.WithFields(logging.Fields{
				"node_id":          nodeID,
				"caller_user_id":   userID,
				"caller_tenant_id": tenantID,
				"upstream_code":    code.String(),
			}).Warn("openMistAdminSession: Commodore denied after resolver allowed (defense-in-depth divergence)")
			return &model.AuthError{Message: "node admin access denied"}, nil
		}
		if code == codes.NotFound {
			return &model.NotFoundError{Message: "node not found"}, nil
		}
		r.Logger.WithError(err).Error("openMistAdminSession: Commodore RPC failed")
		return nil, fmt.Errorf("mint mist admin session: %w", err)
	}

	edgeDomain := strings.TrimSpace(mintResp.GetEdgeDomain())
	if edgeDomain == "" {
		return nil, errors.New("commodore returned empty edge_domain")
	}

	return &model.MistAdminSession{
		PostURL:      "https://" + edgeDomain + "/_mist-session",
		SessionToken: mintResp.GetToken(),
		ExpiresAt:    int(mintResp.GetExpiresAt()),
	}, nil
}

func mistAdminCanAdminNode(ownerTenantID, callerTenantID, callerRole string) bool {
	return auth.CanAdminMistNode(ownerTenantID, callerTenantID, callerRole)
}
