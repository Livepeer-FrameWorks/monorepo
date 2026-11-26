//go:build schema_verify

package handlers

import (
	"testing"

	"frameworks/api_billing/internal/database/purserdb"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	"github.com/google/uuid"
)

func TestConfirmedInvoiceIntentCannotReopen_RealPG(t *testing.T) {
	db := startPurserUsageRealPG(t)
	tenant, intent := uuid.NewString(), uuid.NewString()
	if _, err := db.Exec(`INSERT INTO purser.payment_provider_intents (id,tenant_id,provider,purpose,status,currency,amount_cents,idempotency_key,succeeded_at) VALUES ($1,$2,'stripe','stripe_overage_charge','succeeded','EUR',7900,$3,NOW())`, intent, tenant, "intent-"+intent); err != nil {
		t.Fatal(err)
	}
	if err := purserdb.New(db).SetProviderPaymentIntentStatus(t.Context(), purserdb.SetProviderPaymentIntentStatusParams{TenantID: tenant, IntentID: intent, Status: "provider_open"}); err != nil {
		t.Fatal(err)
	}
	var current string
	if err := db.QueryRow(`SELECT status FROM purser.payment_provider_intents WHERE tenant_id=$1 AND id=$2`, tenant, intent).Scan(&current); err != nil {
		t.Fatal(err)
	}
	if current != "succeeded" {
		t.Fatalf("late API response reopened confirmed invoice intent: %s", current)
	}
}

func TestInvoiceIntentReconciliationRequiresExactConfirmedEvidence_RealPG(t *testing.T) {
	db := startPurserUsageRealPG(t)
	svc := &Service{db: db, logger: logging.NewLogger()}
	for _, tc := range []struct {
		name                                                                          string
		pending, foreignOwner, wrongAmount, wrongCurrency, wrongPayment, wrongPurpose bool
		want                                                                          bool
	}{
		{name: "confirmed capture", want: true},
		{name: "unconfirmed", pending: true},
		{name: "foreign owner", foreignOwner: true},
		{name: "wrong amount", wrongAmount: true},
		{name: "wrong currency", wrongCurrency: true},
		{name: "different provider payment", wrongPayment: true},
		{name: "unrelated checkout", wrongPurpose: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			invoice, txIDs := seedHalfPaidInvoice(t, db)
			var tenant string
			if err := db.QueryRow(`SELECT tenant_id::text FROM purser.billing_invoices WHERE id=$1`, invoice).Scan(&tenant); err != nil {
				t.Fatal(err)
			}
			owner, purpose, currency, paymentID := tenant, "stripe_overage_charge", "EUR", txIDs[0]
			amount := int64(5000)
			if tc.foreignOwner {
				owner = uuid.NewString()
			}
			if tc.wrongAmount {
				amount++
			}
			if tc.wrongCurrency {
				currency = "USD"
			}
			if tc.wrongPayment {
				paymentID = "pi_other"
			}
			if tc.wrongPurpose {
				purpose = "tenant_subscription_checkout"
			}
			intent := uuid.NewString()
			if _, err := db.Exec(`INSERT INTO purser.payment_provider_intents (id,tenant_id,provider,purpose,local_reference_type,local_reference_id,status,currency,amount_cents,idempotency_key,provider_payment_id) VALUES($1,$2,'stripe',$3,'invoice',$4,'provider_open',$5,$6,$7,$8)`, intent, owner, purpose, invoice, currency, amount, "intent-"+intent, paymentID); err != nil {
				t.Fatal(err)
			}
			paymentStatus := "confirmed"
			if tc.pending {
				paymentStatus = "pending"
			}
			if _, err := db.Exec(`UPDATE purser.billing_payments SET intent_id=$1,status=$2,confirmed_at=NOW()-INTERVAL '30 minutes' WHERE invoice_id=$3 AND tx_id=$4`, intent, paymentStatus, invoice, txIDs[0]); err != nil {
				t.Fatal(err)
			}
			for range 2 {
				if err := svc.reconcileConfirmedInvoiceIntents(t.Context()); err != nil {
					t.Fatal(err)
				}
			}
			var outcome string
			if err := db.QueryRow(`SELECT status FROM purser.payment_provider_intents WHERE tenant_id=$1 AND id=$2`, owner, intent).Scan(&outcome); err != nil {
				t.Fatal(err)
			}
			if (outcome == "succeeded") != tc.want {
				t.Fatalf("intent outcome=%s, want confirmed=%v", outcome, tc.want)
			}
			if status := invoiceStatus(t, db, invoice); status != "pending" {
				t.Fatalf("metadata reconciliation changed invoice state: %s", status)
			}
			var credits int
			if err := db.QueryRow(`SELECT count(*) FROM purser.balance_transactions WHERE tenant_id=$1`, tenant).Scan(&credits); err != nil || credits != 0 {
				t.Fatalf("metadata reconciliation moved wallet funds: count=%d err=%v", credits, err)
			}
		})
	}
}
