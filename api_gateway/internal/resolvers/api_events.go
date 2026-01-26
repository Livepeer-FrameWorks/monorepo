package resolvers

import (
	"context"

	"frameworks/api_gateway/internal/middleware"
	pb "frameworks/pkg/proto"

	"google.golang.org/protobuf/types/known/timestamppb"
)

const (
	apiEventStreamCreated                = "api_stream_created"
	apiEventStreamUpdated                = "api_stream_updated"
	apiEventStreamDeleted                = "api_stream_deleted"
	apiEventStreamKeyCreated             = "api_stream_key_created"
	apiEventStreamKeyDeleted             = "api_stream_key_deleted"
	apiEventStreamKeyRotated             = "api_stream_key_rotated"
	apiEventTokenCreated                 = "api_token_created"
	apiEventTokenRevoked                 = "api_token_revoked"
	apiEventPaymentCreated               = "api_payment_created"
	apiEventSubscriptionCreated          = "api_subscription_created"
	apiEventSubscriptionUpdated          = "api_subscription_updated"
	apiEventTopupCreated                 = "api_topup_created"
	apiEventTenantUpdated                = "api_tenant_updated"
	apiEventTenantClusterAssigned        = "api_tenant_cluster_assigned"
	apiEventTenantClusterUnassigned      = "api_tenant_cluster_unassigned"
	apiEventClusterCreated               = "api_cluster_created"
	apiEventClusterUpdated               = "api_cluster_updated"
	apiEventClusterInviteCreated         = "api_cluster_invite_created"
	apiEventClusterInviteRevoked         = "api_cluster_invite_revoked"
	apiEventClusterSubscriptionRequested = "api_cluster_subscription_requested"
	apiEventClusterSubscriptionApproved  = "api_cluster_subscription_approved"
	apiEventClusterSubscriptionRejected  = "api_cluster_subscription_rejected"
)

func tenantIDFromContext(ctx context.Context) string {
	if user := middleware.GetUserFromContext(ctx); user != nil && user.TenantID != "" {
		return user.TenantID
	}
	if v, ok := ctx.Value("tenant_id").(string); ok {
		return v
	}
	return ""
}

func userIDFromContext(ctx context.Context) string {
	if user := middleware.GetUserFromContext(ctx); user != nil && user.UserID != "" {
		return user.UserID
	}
	if v, ok := ctx.Value("user_id").(string); ok {
		return v
	}
	return ""
}

func (r *Resolver) sendServiceEvent(ctx context.Context, event *pb.ServiceEvent) {
	if r == nil || event == nil {
		return
	}
	if middleware.IsDemoMode(ctx) {
		return
	}
	if r.Clients == nil || r.Clients.Decklog == nil {
		return
	}
	if event.Source == "" {
		event.Source = "bridge"
	}
	if event.Timestamp == nil {
		event.Timestamp = timestamppb.Now()
	}
	if event.TenantId == "" {
		event.TenantId = tenantIDFromContext(ctx)
	}
	if event.UserId == "" {
		event.UserId = userIDFromContext(ctx)
	}
	if event.TenantId == "" {
		r.Logger.WithField("event_type", event.EventType).Warn("Skipping API service event without tenant_id")
		return
	}

	if err := r.Clients.Decklog.SendServiceEvent(event); err != nil {
		r.Logger.WithError(err).WithField("event_type", event.EventType).Warn("Failed to emit API service event")
	}
}
