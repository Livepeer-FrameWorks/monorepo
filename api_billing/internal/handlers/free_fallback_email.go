package handlers

import (
	"fmt"
	"time"

	"frameworks/api_billing/internal/subscriptions"
)

// FreeFallbackNotice is what a tenant is told about its move to Free.
type FreeFallbackNotice struct {
	Recipient  string
	TenantName string
	Reason     string
	MoveAt     time.Time
	Moved      bool
}

// freeFallbackTemplate picks the notice's template and subject.
func freeFallbackTemplate(notice FreeFallbackNotice) (templateName, subject string) {
	switch {
	case !notice.Moved:
		return "payment_grace_started", "Action needed - We could not collect your payment"
	case notice.Reason == subscriptions.FallbackCancelled:
		return "cancellation_moved_to_free", "Your subscription has ended"
	default:
		return "moved_to_free", "Your account moved to the Free plan"
	}
}

// UpdatePaymentAnchor is the billing page section that offers the payment
// action during a grace period.
const UpdatePaymentAnchor = "update-payment"

// freeFallbackActionURL sends a grace notice straight to the payment action.
func freeFallbackActionURL(notice FreeFallbackNotice) string {
	if notice.Moved {
		return billingPageURL()
	}
	return billingPageURL() + "#" + UpdatePaymentAnchor
}

// SendFreeFallbackNotice sends the grace-period or move notice. An unconfigured
// mail service reports an error so the notice stays queued until mail works.
func (s *Service) SendFreeFallbackNotice(notice FreeFallbackNotice) error {
	if s == nil || s.emailService == nil || !s.emailService.IsConfigured() {
		return fmt.Errorf("email service not configured")
	}
	templateName, subject := freeFallbackTemplate(notice)
	body, err := s.emailService.renderTemplate(templateName, EmailData{
		TenantName: notice.TenantName, DueDate: notice.MoveAt, LoginURL: freeFallbackActionURL(notice),
	})
	if err != nil {
		return fmt.Errorf("render %s: %w", templateName, err)
	}
	return s.emailService.sendEmail(notice.Recipient, subject, body)
}
