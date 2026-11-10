//go:build schema_verify

package handlers

import (
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"testing"
	"time"

	"frameworks/api_billing/internal/database/purserdb"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/billing"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/database"
)

func TestAdvanceBaseFeeTaxReviewIsVisibleAndStable_RealPG(t *testing.T) {
	f := newTierChangeFixture(t, "20.00", firstBaseFeePaid)
	start := time.Now().UTC().Add(-time.Hour).Truncate(time.Microsecond)
	if _, err := f.db.Exec(`UPDATE purser.tenant_subscriptions SET billing_name=NULL,billing_period_start=$2,billing_period_end=$3 WHERE tenant_id=$1::uuid`, f.tenantID, start, billing.NextMonthlyAnniversary(start)); err != nil {
		t.Fatal(err)
	}
	for attempt := range 2 {
		if err := f.jobs.chargeAdvanceBaseFee(t.Context(), f.tenantID, time.Now()); err == nil {
			t.Fatal("missing invoice profile did not refuse collection")
		}
		var reviews int
		if err := f.db.QueryRow(`SELECT count(*) FROM purser.crypto_accounting_anomalies WHERE tenant_id=$1::uuid AND kind='invoice_tax_review' AND reference_type='billing_phase' AND reference_id=$2`, f.tenantID, billingPhaseTaxReviewReference(f.tenantID, start)).Scan(&reviews); err != nil || reviews != 1 {
			t.Fatalf("advance fee has no stable visible review: reviews=%d err=%v", reviews, err)
		}
		if attempt == 0 {
			if _, err := f.db.Exec(`UPDATE purser.crypto_accounting_anomalies SET status='resolved',resolved_at=NOW(),resolution_note='Acknowledged' WHERE tenant_id=$1::uuid AND kind='invoice_tax_review'`, f.tenantID); err != nil {
				t.Fatal(err)
			}
		}
	}
	var open, invoices int
	if err := f.db.QueryRow(`SELECT (SELECT count(*) FROM purser.crypto_accounting_anomalies WHERE tenant_id=$1::uuid AND kind='invoice_tax_review' AND status='open'),(SELECT count(*) FROM purser.billing_invoices WHERE tenant_id=$1::uuid AND base_fee_period_start=$2)`, f.tenantID, start).Scan(&open, &invoices); err != nil || open != 0 || invoices != 0 || len(f.stripe.matching(http.MethodPost, "/v1/payment_intents")) != 1 {
		t.Fatalf("tax refusal changed money or reopened acknowledgment: open=%d invoices=%d err=%v", open, invoices, err)
	}
	if _, err := f.db.Exec(`UPDATE purser.tenant_subscriptions SET billing_name='Corrected Buyer' WHERE tenant_id=$1::uuid`, f.tenantID); err != nil {
		t.Fatal(err)
	}
	if err := f.jobs.chargeAdvanceBaseFee(t.Context(), f.tenantID, time.Now()); err != nil {
		t.Fatalf("corrected fee did not collect: %v", err)
	}
	if err := f.db.QueryRow(`SELECT count(*) FROM purser.billing_invoices WHERE tenant_id=$1::uuid AND base_fee_period_start=$2`, f.tenantID, start).Scan(&invoices); err != nil || invoices != 1 || len(f.stripe.matching(http.MethodPost, "/v1/payment_intents")) != 2 {
		t.Fatalf("corrected fee was not issued/collected once: invoices=%d err=%v", invoices, err)
	}
}

func TestAdvanceUpgradePermanentTaxRefusalCancelsUnacceptedPurchase_RealPG(t *testing.T) {
	f := newTierChangeFixture(t, "20.00", firstBaseFeePaid)
	target := insertTierChangeTier(t, f.db, "50.00")
	if _, err := f.db.Exec(`UPDATE purser.tenant_subscriptions SET billing_name=NULL WHERE tenant_id=$1::uuid`, f.tenantID); err != nil {
		t.Fatal(err)
	}
	var id string
	if err := database.WithRetryablePostgresTx(t.Context(), f.db, nil, func(tx *sql.Tx) error {
		var err error
		id, err = f.service.StageTierUpgradeTx(t.Context(), tx, f.tenantID, target, f.start, f.end)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	applied, err := f.service.ProcessTierUpgrade(t.Context(), f.tenantID, id)
	if applied || !errors.Is(err, billing.ErrTierUpgradeRejected) {
		t.Fatalf("permanent tax refusal remained an automatic retry: applied=%v err=%v", applied, err)
	}
	row, err := purserdb.New(f.db).GetTierUpgrade(t.Context(), purserdb.GetTierUpgradeParams{TenantID: f.tenantID, ID: id})
	if err != nil || row.State != "cancelled" {
		t.Fatalf("unaccepted purchase remains pending: state=%s err=%v", row.State, err)
	}
	var reviews, invoices int
	var tier string
	if err := f.db.QueryRow(`SELECT tier_id::text,(SELECT count(*) FROM purser.crypto_accounting_anomalies WHERE tenant_id=$1::uuid AND kind='invoice_tax_review' AND reference_type='tier_upgrade' AND reference_id=$2),(SELECT count(*) FROM purser.billing_invoices WHERE tenant_id=$1::uuid AND base_fee_period_start IS NOT NULL) FROM purser.tenant_subscriptions WHERE tenant_id=$1::uuid`, f.tenantID, id).Scan(&tier, &reviews, &invoices); err != nil || tier == target || reviews != 1 || invoices != 1 || len(f.stripe.matching(http.MethodPost, "/v1/payment_intents")) != 1 {
		t.Fatalf("rejected purchase lost review or granted unpaid pricing: tier=%s reviews=%d invoices=%d err=%v", tier, reviews, invoices, err)
	}
	var pending sql.NullString
	if err := f.db.QueryRow(`SELECT pending_tier_id::text FROM purser.tenant_subscriptions WHERE tenant_id=$1::uuid`, f.tenantID).Scan(&pending); err != nil || pending.Valid {
		t.Fatalf("rejected purchase retained pending pricing: pending=%v err=%v", pending, err)
	}
	if _, err := f.db.Exec(`UPDATE purser.tenant_subscriptions SET pending_tier_id=$2::uuid,pending_effective_at=NOW()+INTERVAL '1 minute',pending_reason='tier_upgrade' WHERE tenant_id=$1::uuid`, f.tenantID, target); err != nil {
		t.Fatal(err)
	}
	if err := purserdb.New(f.db).ClearRejectedTierUpgrade(t.Context(), purserdb.ClearRejectedTierUpgradeParams{TenantID: f.tenantID, ID: id}); err != nil {
		t.Fatal(err)
	}
	if err := f.db.QueryRow(`SELECT pending_tier_id::text FROM purser.tenant_subscriptions WHERE tenant_id=$1::uuid`, f.tenantID).Scan(&pending); err != nil || !pending.Valid || pending.String != target {
		t.Fatalf("old rejection cleared a newer request: pending=%v err=%v", pending, err)
	}
	t.Run("accepted_provider_payment_is_not_rejected", func(t *testing.T) {
		f := newTierChangeFixture(t, "20.00", firstBaseFeePaid)
		target := insertTierChangeTier(t, f.db, "50.00")
		var id string
		if err := database.WithRetryablePostgresTx(t.Context(), f.db, nil, func(tx *sql.Tx) error {
			var err error
			id, err = f.service.StageTierUpgradeTx(t.Context(), tx, f.tenantID, target, f.start, f.end)
			return err
		}); err != nil {
			t.Fatal(err)
		}
		at := time.Now().UTC().Truncate(time.Second)
		receipt := billing.TierUpgradeReceipt{Applied: true, PeriodStart: at, PeriodEnd: billing.NextMonthlyAnniversary(at), PaidReceiptID: "paid-provider-upgrade"}
		raw, err := json.Marshal(receipt)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.db.Exec(`UPDATE purser.subscription_tier_upgrades SET provider='stripe',state='provider_applied',provider_receipt=$3 WHERE tenant_id=$1::uuid AND id=$2::uuid`, f.tenantID, id, raw); err != nil {
			t.Fatal(err)
		}
		if _, err := f.db.Exec(`UPDATE purser.tenant_subscriptions SET billing_name=NULL WHERE tenant_id=$1::uuid`, f.tenantID); err != nil {
			t.Fatal(err)
		}
		applied, err := f.service.ProcessTierUpgrade(t.Context(), f.tenantID, id)
		row, readErr := purserdb.New(f.db).GetTierUpgrade(t.Context(), purserdb.GetTierUpgradeParams{TenantID: f.tenantID, ID: id})
		if err != nil || !applied || readErr != nil || row.State != "completed" || len(f.stripe.matching(http.MethodPost, "/v1/payment_intents")) != 1 {
			t.Fatalf("mutable profile cancelled or recharged paid entitlement: applied=%v state=%s err=%v read=%v", applied, row.State, err, readErr)
		}
	})
}
