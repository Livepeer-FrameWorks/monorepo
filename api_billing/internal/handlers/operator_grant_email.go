package handlers

import (
	"fmt"
	"time"
)

// Operator grant notice kinds, as stored in purser.operator_grant_notices.
const (
	OperatorGrantNoticeEnding = "ending"
	OperatorGrantNoticeEnded  = "ended"
)

// OperatorGrantNoticeLead is how long before a grant's end the ending notice
// goes out.
const OperatorGrantNoticeLead = 7 * 24 * time.Hour

// OperatorGrantNotice is what a tenant is told about the end of its
// FrameWorks arrangement (an operator grant such as a trial).
type OperatorGrantNotice struct {
	Recipient  string
	TenantName string
	Kind       string
	EndsAt     time.Time
	PlanName   string
}

// operatorGrantTemplate picks the notice's template and subject.
func operatorGrantTemplate(notice OperatorGrantNotice) (templateName, subject string, err error) {
	switch notice.Kind {
	case OperatorGrantNoticeEnding:
		return "operator_grant_ending", "Your FrameWorks arrangement ends on " + notice.EndsAt.UTC().Format("January 2, 2006"), nil
	case OperatorGrantNoticeEnded:
		return "operator_grant_ended", "Your FrameWorks arrangement has ended", nil
	default:
		return "", "", fmt.Errorf("unknown operator grant notice kind %q", notice.Kind)
	}
}

// SendOperatorGrantNotice sends the ending or ended notice. An unconfigured
// mail service reports an error so the notice stays queued until mail works.
func (s *Service) SendOperatorGrantNotice(notice OperatorGrantNotice) error {
	if s == nil || s.emailService == nil || !s.emailService.IsConfigured() {
		return fmt.Errorf("email service not configured")
	}
	templateName, subject, err := operatorGrantTemplate(notice)
	if err != nil {
		return err
	}
	body, err := s.emailService.renderTemplate(templateName, EmailData{
		TenantName: notice.TenantName, DueDate: notice.EndsAt, PlanName: notice.PlanName, LoginURL: billingPageURL(),
	})
	if err != nil {
		return fmt.Errorf("render %s: %w", templateName, err)
	}
	return s.emailService.sendEmail(notice.Recipient, subject, body)
}
