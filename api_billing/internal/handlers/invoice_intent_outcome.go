package handlers

import (
	"context"
	"fmt"

	"frameworks/api_billing/internal/database/purserdb"
	"github.com/google/uuid"
)

// reconcileConfirmedInvoiceIntents repairs collection metadata from confirmed
// receipts. It neither creates a provider charge nor alters the invoice or wallet.
func (s *Service) reconcileConfirmedInvoiceIntents(ctx context.Context) error {
	q := purserdb.New(s.db)
	after := uuid.Nil.String()
	for ctx.Err() == nil {
		tenants, err := q.ListUnreconciledInvoiceIntentTenants(ctx, after)
		if err != nil {
			return fmt.Errorf("list confirmed invoice intents: %w", err)
		}
		for _, tenantID := range tenants {
			rows, updateErr := q.ReconcileConfirmedInvoiceIntents(ctx, tenantID)
			if updateErr != nil {
				return fmt.Errorf("reconcile invoice intents for tenant %s: %w", tenantID, updateErr)
			}
			if rows > 0 {
				s.logger.WithField("tenant_id", tenantID).WithField("intents", rows).Info("Reconciled invoice intent status from confirmed receipts")
			}
			after = tenantID
		}
		if len(tenants) < 100 {
			return nil
		}
	}
	return ctx.Err()
}
