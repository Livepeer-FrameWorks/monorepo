//go:build schema_verify

package handlers

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"frameworks/api_billing/internal/database/purserdb"
	"frameworks/api_billing/internal/subscriptions"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	"github.com/google/uuid"
)

func TestPreparedCheckoutCanRecoverWithoutProvider_RealPG(t *testing.T) {
	db := startProviderReservationDatabase(t)
	now := time.Now().UTC()
	tenant := newPhaseTenant(t, db, "prepaid", now, now.AddDate(0, 1, 0), 0)
	ctx := context.WithValue(t.Context(), ctxkeys.KeyUserID, uuid.NewString())
	id, err := subscriptions.ReserveCheckout(ctx, db, tenant.id, func(tx *sql.Tx) (string, error) {
		return purserdb.New(tx).UpsertStripeTenantCheckoutIntent(ctx, purserdb.UpsertStripeTenantCheckoutIntentParams{TenantID: tenant.id, TierID: tenant.supporterID, Currency: "EUR", IdempotencyKey: "before-provider"})
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := purserdb.New(db).SetProviderIntentCustomer(ctx, purserdb.SetProviderIntentCustomerParams{TenantID: tenant.id, IntentID: id, CustomerID: sql.NullString{String: "customer", Valid: true}}); err != nil {
		t.Fatal(err)
	}
	result, err := tenant.svc.RecoverCheckoutResult(ctx, tenant.id, id, "failed before provider submission")
	if err != nil || result.GetOutcome() != "expired" {
		t.Fatalf("pre-call failure cannot be cleared while provider unavailable: result=%v err=%v", result, err)
	}
	if pending, err := pendingProviderCheckoutForTest(ctx, db, tenant.id); err != nil || pending {
		t.Fatalf("pre-call attempt still fences tenant: %v %v", pending, err)
	}
}
