package tools

import (
	"context"

	"frameworks/api_gateway/graph/model"
	"frameworks/api_gateway/internal/clients"
	"frameworks/api_gateway/internal/mcp/mcperrors"
	"frameworks/api_gateway/internal/mcp/preflight"
	"frameworks/api_gateway/internal/resolvers"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// RegisterClusterSubscriptionTools registers cluster invite + subscription
// flows. Two distinct flows:
//
//   - Invite: cluster owner invites a tenant. Owner-side: create_cluster_invite,
//     revoke_cluster_invite. Invitee-side: accept_cluster_invite.
//   - Subscription request: tenant requests subscription to a cluster.
//     Tenant-side: request_cluster_subscription. Owner-side:
//     approve_subscription_request, reject_subscription_request.
//
// Marketplace browsing, direct subscription, and preferred-cluster selection
// are registered in infrastructure.go; this file owns invite and approval flows.
func RegisterClusterSubscriptionTools(server *mcp.Server, _ *clients.ServiceClients, resolver *resolvers.Resolver, _ *preflight.Checker, logger logging.Logger) {
	mcp.AddTool(server, &mcp.Tool{
		Name:        "create_cluster_invite",
		Description: "Cluster owner: invite another tenant to subscribe. Returns an invite token the recipient passes to accept_cluster_invite.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, args CreateClusterInviteInput) (*mcp.CallToolResult, any, error) {
		return handleCreateClusterInvite(ctx, args, resolver, logger)
	})
	mcp.AddTool(server, &mcp.Tool{
		Name:        "revoke_cluster_invite",
		Description: "Cluster owner: revoke an outstanding invite by ID. Already-accepted invites are unaffected (use unsubscribe to remove the resulting subscription).",
	}, func(ctx context.Context, req *mcp.CallToolRequest, args RevokeClusterInviteInput) (*mcp.CallToolResult, any, error) {
		return handleRevokeClusterInvite(ctx, args, resolver, logger)
	})
	mcp.AddTool(server, &mcp.Tool{
		Name:        "accept_cluster_invite",
		Description: "Invitee: redeem a cluster invite token to create the subscription.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, args AcceptClusterInviteInput) (*mcp.CallToolResult, any, error) {
		return handleAcceptClusterInvite(ctx, args, resolver, logger)
	})
	mcp.AddTool(server, &mcp.Tool{
		Name:        "request_cluster_subscription",
		Description: "Tenant: request subscription to a cluster. The owner approves or rejects via approve_subscription_request / reject_subscription_request. Optional invite_token short-circuits the approval workflow.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, args RequestClusterSubscriptionInput) (*mcp.CallToolResult, any, error) {
		return handleRequestClusterSubscription(ctx, args, resolver, logger)
	})
	mcp.AddTool(server, &mcp.Tool{
		Name:        "approve_subscription_request",
		Description: "Cluster owner: approve a pending subscription request by subscription ID.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, args ApproveSubscriptionInput) (*mcp.CallToolResult, any, error) {
		return handleApproveSubscription(ctx, args, resolver, logger)
	})
	mcp.AddTool(server, &mcp.Tool{
		Name:        "reject_subscription_request",
		Description: "Cluster owner: reject a pending subscription request. Optional reason is stored on the subscription row.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, args RejectSubscriptionInput) (*mcp.CallToolResult, any, error) {
		return handleRejectSubscription(ctx, args, resolver, logger)
	})
}

type CreateClusterInviteInput struct {
	ClusterID       string `json:"cluster_id" jsonschema:"required"`
	InvitedTenantID string `json:"invited_tenant_id" jsonschema:"required"`
	AccessLevel     string `json:"access_level,omitempty" jsonschema_description:"'viewer' | 'subscriber' | 'admin'. Defaults to 'subscriber'."`
	ExpiresInDays   int32  `json:"expires_in_days,omitempty" jsonschema_description:"Invite TTL. Defaults to 7."`
}

type RevokeClusterInviteInput struct {
	InviteID string `json:"invite_id" jsonschema:"required"`
}

type AcceptClusterInviteInput struct {
	InviteToken string `json:"invite_token" jsonschema:"required"`
}

type RequestClusterSubscriptionInput struct {
	ClusterID   string `json:"cluster_id" jsonschema:"required"`
	InviteToken string `json:"invite_token,omitempty" jsonschema_description:"Optional — present an invite token to skip the approval workflow."`
}

type ApproveSubscriptionInput struct {
	SubscriptionID string `json:"subscription_id" jsonschema:"required"`
}

type RejectSubscriptionInput struct {
	SubscriptionID string `json:"subscription_id" jsonschema:"required"`
	Reason         string `json:"reason,omitempty"`
}

func handleCreateClusterInvite(ctx context.Context, args CreateClusterInviteInput, resolver *resolvers.Resolver, logger logging.Logger) (*mcp.CallToolResult, any, error) {
	tenantID := ctxkeys.GetTenantID(ctx)
	if tenantID == "" {
		return nil, nil, mcperrors.AuthRequired()
	}
	if args.ClusterID == "" || args.InvitedTenantID == "" {
		return toolError("cluster_id and invited_tenant_id are required")
	}
	accessLevel := args.AccessLevel
	if accessLevel == "" {
		accessLevel = "subscriber"
	}
	expiresIn := args.ExpiresInDays
	if expiresIn <= 0 {
		expiresIn = 7
	}
	expiresInDays := int(expiresIn)
	result, err := resolver.DoCreateClusterInvite(ctx, model.CreateClusterInviteInput{
		ClusterID:       args.ClusterID,
		InvitedTenantID: args.InvitedTenantID,
		AccessLevel:     &accessLevel,
		ExpiresInDays:   &expiresInDays,
	})
	if err != nil {
		logger.WithError(err).Warn("create_cluster_invite failed")
		return nil, nil, err
	}
	return toolSuccess(result)
}

func handleRevokeClusterInvite(ctx context.Context, args RevokeClusterInviteInput, resolver *resolvers.Resolver, logger logging.Logger) (*mcp.CallToolResult, any, error) {
	tenantID := ctxkeys.GetTenantID(ctx)
	if tenantID == "" {
		return nil, nil, mcperrors.AuthRequired()
	}
	if args.InviteID == "" {
		return toolError("invite_id is required")
	}
	result, err := resolver.DoRevokeClusterInvite(ctx, args.InviteID)
	if err != nil {
		logger.WithError(err).Warn("revoke_cluster_invite failed")
		return nil, nil, err
	}
	return toolSuccess(result)
}

func handleAcceptClusterInvite(ctx context.Context, args AcceptClusterInviteInput, resolver *resolvers.Resolver, logger logging.Logger) (*mcp.CallToolResult, any, error) {
	tenantID := ctxkeys.GetTenantID(ctx)
	if tenantID == "" {
		return nil, nil, mcperrors.AuthRequired()
	}
	if args.InviteToken == "" {
		return toolError("invite_token is required")
	}
	result, err := resolver.DoAcceptClusterInvite(ctx, args.InviteToken)
	if err != nil {
		logger.WithError(err).Warn("accept_cluster_invite failed")
		return nil, nil, err
	}
	return toolSuccess(result)
}

func handleRequestClusterSubscription(ctx context.Context, args RequestClusterSubscriptionInput, resolver *resolvers.Resolver, logger logging.Logger) (*mcp.CallToolResult, any, error) {
	tenantID := ctxkeys.GetTenantID(ctx)
	if tenantID == "" {
		return nil, nil, mcperrors.AuthRequired()
	}
	if args.ClusterID == "" {
		return toolError("cluster_id is required")
	}
	var inviteToken *string
	if args.InviteToken != "" {
		token := args.InviteToken
		inviteToken = &token
	}
	result, err := resolver.DoRequestClusterSubscription(ctx, args.ClusterID, inviteToken)
	if err != nil {
		logger.WithError(err).Warn("request_cluster_subscription failed")
		return nil, nil, err
	}
	return toolSuccess(result)
}

func handleApproveSubscription(ctx context.Context, args ApproveSubscriptionInput, resolver *resolvers.Resolver, logger logging.Logger) (*mcp.CallToolResult, any, error) {
	tenantID := ctxkeys.GetTenantID(ctx)
	if tenantID == "" {
		return nil, nil, mcperrors.AuthRequired()
	}
	if args.SubscriptionID == "" {
		return toolError("subscription_id is required")
	}
	result, err := resolver.DoApproveClusterSubscription(ctx, args.SubscriptionID)
	if err != nil {
		logger.WithError(err).Warn("approve_subscription_request failed")
		return nil, nil, err
	}
	return toolSuccess(result)
}

func handleRejectSubscription(ctx context.Context, args RejectSubscriptionInput, resolver *resolvers.Resolver, logger logging.Logger) (*mcp.CallToolResult, any, error) {
	tenantID := ctxkeys.GetTenantID(ctx)
	if tenantID == "" {
		return nil, nil, mcperrors.AuthRequired()
	}
	if args.SubscriptionID == "" {
		return toolError("subscription_id is required")
	}
	var reason *string
	if args.Reason != "" {
		value := args.Reason
		reason = &value
	}
	result, err := resolver.DoRejectClusterSubscription(ctx, args.SubscriptionID, reason)
	if err != nil {
		logger.WithError(err).Warn("reject_subscription_request failed")
		return nil, nil, err
	}
	return toolSuccess(result)
}
