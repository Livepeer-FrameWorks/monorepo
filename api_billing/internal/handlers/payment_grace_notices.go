package handlers

import (
	"context"
	"database/sql"
	"fmt"
	"frameworks/api_billing/internal/appconfig"
	"frameworks/api_billing/internal/database/purserdb"
	"strings"
	"time"
)

func (jm *JobManager) runPrepaidPaymentNotices(ctx context.Context) {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		jm.prepaidPaymentNoticeTick(ctx)
		select {
		case <-ctx.Done():
			return
		case <-jm.stopCh:
			return
		case <-ticker.C:
		}
	}
}
func (jm *JobManager) prepaidPaymentNoticeTick(ctx context.Context) {
	if jm.billing == nil {
		return
	}
	q := purserdb.New(jm.db)
	notices, err := q.ListPrepaidPaymentNotices(ctx)
	if err != nil {
		jm.logger.WithError(err).Warn("Payment notice discovery unavailable")
		return
	}
	for _, n := range notices {
		if ctx.Err() != nil {
			return
		}
		claimed, claimErr := q.ClaimPrepaidPaymentNotice(ctx, purserdb.ClaimPrepaidPaymentNoticeParams{TenantID: n.TenantID, Kind: n.Kind, GraceEndsAt: n.GraceEndsAt, FreeRecoveryUntil: n.FreeRecoveryUntil})
		if claimErr != nil || claimed == 0 {
			continue
		}
		if strings.TrimSpace(n.Recipient) == "" && jm.commodoreClient != nil {
			identityCtx, identityCancel := context.WithTimeout(ctx, 4*time.Second)
			identity, identityErr := jm.commodoreClient.GetTenantPrimaryUser(identityCtx, n.TenantID)
			identityCancel()
			if identityErr == nil && identity.GetFreeTierEligible() {
				n.Recipient = identity.GetEmail()
			}
		}
		sendErr := jm.billing.sendPrepaidPaymentNotice(n)
		lastError := sql.NullString{}
		if sendErr != nil {
			lastError = sql.NullString{String: sendErr.Error(), Valid: true}
			jm.logger.WithError(sendErr).WithField("tenant_id", n.TenantID).Warn("Payment notice awaits retry")
		}
		if _, finishErr := q.FinishPrepaidPaymentNotice(ctx, purserdb.FinishPrepaidPaymentNoticeParams{TenantID: n.TenantID, Kind: n.Kind, GraceEndsAt: n.GraceEndsAt, FreeRecoveryUntil: n.FreeRecoveryUntil, Sent: sendErr == nil, LastError: lastError}); finishErr != nil {
			jm.logger.WithError(finishErr).Warn("Payment notice delivery receipt unavailable")
		}
	}
}
func (s *Service) sendPrepaidPaymentNotice(n purserdb.ListPrepaidPaymentNoticesRow) error {
	if s.emailService == nil || !s.emailService.IsConfigured() {
		return fmt.Errorf("email service not configured")
	}
	if strings.TrimSpace(n.Recipient) == "" {
		return fmt.Errorf("billing email missing")
	}
	if strings.TrimSpace(appconfig.Runtime().WebAppURL) == "" {
		return fmt.Errorf("web app URL not configured")
	}
	templateName := "prepaid_payment_grace"
	deadline := n.GraceEndsAt.Time
	subject := "Your prepaid payment grace"
	if n.Kind == "low" {
		templateName = "prepaid_low_balance"
		subject = "Your prepaid balance is running low"
	}
	if n.Kind == "recovery" || n.Kind == "recovery_reminder" {
		templateName = "prepaid_media_recovery"
		subject = "Your media recovery window"
		deadline = n.FreeRecoveryUntil.Time
	}
	body, err := s.emailService.renderBillingTemplate(templateName, EmailData{TenantName: n.TenantName, DueDate: deadline, Amount: float64(n.GraceCreditCents) / 100, Balance: float64(n.BalanceCents) / 100, Currency: "EUR", LoginURL: strings.TrimRight(appconfig.Runtime().WebAppURL, "/") + "/account/balance"})
	if err != nil {
		return err
	}
	return s.emailService.sendEmail(n.Recipient, subject, body)
}
