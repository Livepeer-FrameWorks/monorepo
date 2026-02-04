package resolvers

import (
	"context"
	"strings"

	"frameworks/api_gateway/internal/middleware"
	"frameworks/pkg/ctxkeys"
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
	apiEventClipCreated                  = "api_clip_created"
	apiEventClipDeleted                  = "api_clip_deleted"
	apiEventDVRDeleted                   = "api_dvr_deleted"
	apiEventVodUploadCreated             = "api_vod_upload_created"
	apiEventVodUploadCompleted           = "api_vod_upload_completed"
	apiEventVodUploadAborted             = "api_vod_upload_aborted"
	apiEventVodAssetDeleted              = "api_vod_asset_deleted"
	apiEventTokenCreated                 = "api_token_created"
	apiEventTokenRevoked                 = "api_token_revoked"
	apiEventPaymentCreated               = "api_payment_created"
	apiEventSubscriptionCreated          = "api_subscription_created"
	apiEventSubscriptionUpdated          = "api_subscription_updated"
	apiEventBillingDetailsUpdated        = "api_billing_details_updated"
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
	return ctxkeys.GetTenantID(ctx)
}

func userIDFromContext(ctx context.Context) string {
	if user := middleware.GetUserFromContext(ctx); user != nil && user.UserID != "" {
		return user.UserID
	}
	return ctxkeys.GetUserID(ctx)
}

const maxEventReasonLength = 128

func truncateReason(reason string) string {
	reason = strings.TrimSpace(reason)
	if len(reason) > maxEventReasonLength {
		return reason[:maxEventReasonLength] + "..."
	}
	return reason
}

func parseRejectReasonCode(reason string) pb.ClusterRejectReason {
	lower := strings.ToLower(reason)
	switch {
	case strings.Contains(lower, "capacity") || strings.Contains(lower, "full"):
		return pb.ClusterRejectReason_CLUSTER_REJECT_REASON_CAPACITY
	case strings.Contains(lower, "policy") || strings.Contains(lower, "terms") || strings.Contains(lower, "violation"):
		return pb.ClusterRejectReason_CLUSTER_REJECT_REASON_POLICY
	case strings.Contains(lower, "eligib") || strings.Contains(lower, "qualify"):
		return pb.ClusterRejectReason_CLUSTER_REJECT_REASON_ELIGIBILITY
	case strings.Contains(lower, "duplicate") || strings.Contains(lower, "already"):
		return pb.ClusterRejectReason_CLUSTER_REJECT_REASON_DUPLICATE
	case strings.Contains(lower, "withdraw") || strings.Contains(lower, "cancel"):
		return pb.ClusterRejectReason_CLUSTER_REJECT_REASON_WITHDRAWN
	default:
		return pb.ClusterRejectReason_CLUSTER_REJECT_REASON_OTHER
	}
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
