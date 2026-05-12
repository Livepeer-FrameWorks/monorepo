package heartbeat

import (
	"context"
	"strings"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	pb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto"
)

type TenantContactClient interface {
	GetTenantPrimaryUser(ctx context.Context, tenantID string) (*pb.GetTenantPrimaryUserResponse, error)
}

func resolveTenantNotificationContact(ctx context.Context, tenantID string, billing BillingClient, contacts TenantContactClient, defaultRecipient string, logger logging.Logger) (string, string) {
	tenantID = strings.TrimSpace(tenantID)
	if tenantID == "" {
		return strings.TrimSpace(defaultRecipient), ""
	}

	if billing != nil {
		status, err := billing.GetBillingStatus(ctx, tenantID)
		if err != nil {
			if logger != nil {
				logger.WithError(err).WithField("tenant_id", tenantID).Warn("Notification contact: billing status lookup failed")
			}
		} else if status != nil {
			if sub := status.GetSubscription(); sub != nil {
				if email := strings.TrimSpace(sub.GetBillingEmail()); email != "" {
					return email, strings.TrimSpace(sub.GetBillingCompany())
				}
			}
		}
	}

	if contacts != nil {
		user, err := contacts.GetTenantPrimaryUser(ctx, tenantID)
		if err != nil {
			if logger != nil {
				logger.WithError(err).WithField("tenant_id", tenantID).Warn("Notification contact: primary user lookup failed")
			}
		} else if user != nil {
			if email := strings.TrimSpace(user.GetEmail()); email != "" {
				return email, strings.TrimSpace(user.GetName())
			}
		}
	}

	return strings.TrimSpace(defaultRecipient), ""
}
