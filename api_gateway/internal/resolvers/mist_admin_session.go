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
	"github.com/Livepeer-FrameWorks/monorepo/pkg/authz"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/globalid"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	commodorepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"
)

// DoOpenMistAdminSession is the Gateway's role-and-scope wall for Mist admin
// access. Commodore is the authoritative ownership boundary: it resolves the
// node through its service-authenticated Quartermaster client and independently
// verifies that the represented caller owns the node before minting.
func (r *Resolver) DoOpenMistAdminSession(ctx context.Context, input model.OpenMistAdminSessionInput) (model.OpenMistAdminSessionResult, error) {
	// Preserve the mutation's typed unauthenticated result before applying the
	// privileged action gate. Demo mode must not bypass either check.
	userID := strings.TrimSpace(ctxkeys.GetUserID(ctx))
	tenantID := strings.TrimSpace(ctxkeys.GetTenantID(ctx))
	if userID == "" {
		return &model.AuthError{Message: "authentication required"}, nil
	}
	if err := middleware.RequireTenantAction(ctx, "infrastructure:write", authz.ActionAdminMistNode, ctxkeys.GetTenantID(ctx)); err != nil {
		return nil, err
	}
	if middleware.IsDemoMode(ctx) {
		return nil, errDemoUnavailable("Mist admin sessions")
	}
	nodeID := strings.TrimSpace(input.NodeID)
	if nodeID == "" {
		return &model.ValidationError{Message: "nodeId is required", Field: strPtr("nodeId")}, nil
	}

	if typ, rawID, ok := globalid.Decode(nodeID); ok {
		if typ != globalid.TypeInfrastructureNode {
			return &model.ValidationError{Message: "nodeId must reference an infrastructure node", Field: strPtr("nodeId")}, nil
		}
		nodeID = rawID
	}

	if r.Clients == nil || r.Clients.Commodore == nil {
		r.Logger.Error("openMistAdminSession: commodore client unavailable")
		return nil, fmt.Errorf("commodore unavailable")
	}
	// Commodore mints only after its trusted-context ownership check.
	mintResp, err := r.Clients.Commodore.MintMistAdminSession(ctx, &commodorepb.MintMistAdminSessionRequest{
		NodeId: nodeID,
	})
	if err != nil {
		// Map the authoritative ownership boundary's authentication denials to the
		// public union without leaking node existence.
		code := status.Code(err)
		if code == codes.PermissionDenied || code == codes.Unauthenticated {
			r.Logger.WithFields(logging.Fields{
				"node_id":          nodeID,
				"caller_user_id":   userID,
				"caller_tenant_id": tenantID,
				"upstream_code":    code.String(),
			}).Warn("openMistAdminSession: Commodore denied node administration")
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
