package handlers

import (
	"context"
	"database/sql"
	"time"

	"frameworks/pkg/billing"
	"frameworks/pkg/logging"
)

const suspensionThresholdCents int64 = -1000

// ThresholdEnforcer ensures prepaid balance thresholds trigger enforcement actions.
type ThresholdEnforcer struct {
	db              *sql.DB
	logger          logging.Logger
	commodoreClient CommodoreClient
	emailService    *EmailService
}

func NewThresholdEnforcer(db *sql.DB, logger logging.Logger, commodoreClient CommodoreClient, emailService *EmailService) *ThresholdEnforcer {
	return &ThresholdEnforcer{
		db:              db,
		logger:          logger,
		commodoreClient: commodoreClient,
		emailService:    emailService,
	}
}

// EnforcePrepaidThresholds enforces zero-crossing and suspension thresholds for prepaid tenants.
func (e *ThresholdEnforcer) EnforcePrepaidThresholds(ctx context.Context, tenantID string, previousBalance, newBalance int64) error {
	if tenantID == "" {
		return nil
	}

	isPrepaid, err := e.isPrepaidTenant(ctx, tenantID)
	if err != nil {
		e.logger.WithFields(logging.Fields{
			"tenant_id": tenantID,
			"error":     err,
		}).Warn("Failed to resolve billing model for threshold enforcement")
		return err
	}
	if !isPrepaid {
		return nil
	}

	if previousBalance > 0 && newBalance <= 0 {
		e.invalidateTenantCache(ctx, tenantID, "balance_crossed_zero")
	}

	if newBalance < suspensionThresholdCents {
		if err := e.suspendTenantForBalance(ctx, tenantID, newBalance); err != nil {
			return err
		}
	}

	return nil
}

func (e *ThresholdEnforcer) isPrepaidTenant(ctx context.Context, tenantID string) (bool, error) {
	var billingModel string
	err := e.db.QueryRowContext(ctx, `
		SELECT COALESCE(billing_model, 'postpaid')
		FROM purser.tenant_subscriptions
		WHERE tenant_id = $1 AND status != 'cancelled'
		ORDER BY created_at DESC
		LIMIT 1
	`, tenantID).Scan(&billingModel)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return billingModel == "prepaid", nil
}

func (e *ThresholdEnforcer) invalidateTenantCache(ctx context.Context, tenantID, reason string) {
	if e.commodoreClient == nil {
		return
	}
	invalidateCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	_, err := e.commodoreClient.InvalidateTenantCache(invalidateCtx, tenantID, reason)
	if err != nil {
		e.logger.WithFields(logging.Fields{
			"tenant_id": tenantID,
			"reason":    reason,
			"error":     err,
		}).Warn("Failed to invalidate tenant cache after balance change")
	}
}

func (e *ThresholdEnforcer) suspendTenantForBalance(ctx context.Context, tenantID string, balanceCents int64) error {
	result, err := e.db.ExecContext(ctx, `
		UPDATE purser.tenant_subscriptions
		SET status = 'suspended', updated_at = NOW()
		WHERE tenant_id = $1 AND status = 'active'
	`, tenantID)
	if err != nil {
		return err
	}

	rowsAffected, _ := result.RowsAffected()
	if rowsAffected == 0 {
		return nil
	}

	e.logger.WithFields(logging.Fields{
		"tenant_id":     tenantID,
		"balance_cents": balanceCents,
	}).Warn("Suspended tenant due to negative prepaid balance")

	if e.commodoreClient != nil {
		terminateCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		resp, err := e.commodoreClient.TerminateTenantStreams(terminateCtx, tenantID, "insufficient_balance")
		if err != nil {
			e.logger.WithError(err).WithField("tenant_id", tenantID).Error("Failed to terminate tenant streams on suspension")
		} else {
			e.logger.WithFields(logging.Fields{
				"tenant_id":           tenantID,
				"streams_terminated":  resp.StreamsTerminated,
				"sessions_terminated": resp.SessionsTerminated,
				"stream_names":        resp.StreamNames,
			}).Info("Terminated tenant streams due to insufficient balance")
		}

		e.invalidateTenantCache(ctx, tenantID, "suspended")
	}

	e.notifyTenantSuspended(tenantID, balanceCents)
	return nil
}

func (e *ThresholdEnforcer) notifyTenantSuspended(tenantID string, balanceCents int64) {
	if e.emailService == nil {
		return
	}

	var billingEmail string
	err := e.db.QueryRow(`
		SELECT billing_email FROM purser.tenant_subscriptions
		WHERE tenant_id = $1
	`, tenantID).Scan(&billingEmail)
	if err != nil {
		e.logger.WithFields(logging.Fields{
			"tenant_id": tenantID,
			"error":     err,
		}).Warn("Failed to load billing email for suspension notification")
		return
	}
	if billingEmail == "" {
		e.logger.WithField("tenant_id", tenantID).Warn("No billing email for suspension notification")
		return
	}

	tenantName := ""
	if tenantInfo, err := getTenantInfo(tenantID); err == nil && tenantInfo != nil {
		tenantName = tenantInfo.Name
	}

	balance := float64(balanceCents) / 100
	if err := e.emailService.SendAccountSuspendedEmail(billingEmail, tenantName, balance, billing.DefaultCurrency()); err != nil {
		e.logger.WithFields(logging.Fields{
			"tenant_id": tenantID,
			"error":     err,
		}).Warn("Failed to send suspension notification email")
	}
}
