//go:build schema_verify

package handlers

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"frameworks/api_billing/internal/database/purserdb"
	"frameworks/api_billing/internal/subscriptions"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/database"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/testutil/dockerpg"
	"github.com/google/uuid"
)

func TestMonthlyInvoiceRejectsPhaseChangedByGrant_RealPG(t *testing.T) {
	for _, model := range []string{"postpaid", "prepaid"} {
		t.Run(model, func(t *testing.T) { monthlyRejectsChangedPhase(t, model) })
	}
}

func monthlyRejectsChangedPhase(t *testing.T, model string) {
	t.Helper()
	db := startPurserUsageRealPG(t)
	ctx := context.Background()
	start := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	end := start.AddDate(0, 1, 0)
	tenant := newPhaseTenant(t, db, model, start, end, 0)
	if _, writeErr := db.ExecContext(ctx, `UPDATE purser.billing_tiers SET metering_enabled=false WHERE id IN ($1,$2)`, tenant.supporterID, tenant.paygID); writeErr != nil {
		t.Fatal(writeErr)
	}
	due, err := purserdb.New(db).ListSubscriptionsDueForInvoice(ctx, end.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	// A grant closes the old pricing phase after the monthly job selected it.
	boundary := start.Add(15 * 24 * time.Hour)
	if _, err = db.ExecContext(ctx, `UPDATE purser.tenant_subscriptions SET billing_period_start=$2 WHERE tenant_id=$1`, tenant.id, boundary); err != nil {
		t.Fatal(err)
	}
	if generated := tenant.jobs.finalizeSubscriptionPeriods(ctx, due, end.Add(time.Hour), time.Time{}); generated != 0 {
		t.Fatalf("stale monthly snapshot finalized %d periods after grant boundary, want 0", generated)
	}
	var invoices int
	if err = db.QueryRowContext(ctx, `SELECT COUNT(*) FROM purser.billing_invoices WHERE tenant_id=$1`, tenant.id).Scan(&invoices); err != nil {
		t.Fatal(err)
	}
	if invoices != 0 {
		t.Fatalf("stale monthly snapshot wrote %d invoices", invoices)
	}
}

func TestStripeWebhookPreservesClosedGrantPhase_RealPG(t *testing.T) {
	db := startPurserUsageRealPG(t)
	testRecoverableStripeStateKeepsWaivedUsage(t)
	testProviderActivationRefusesGrant(t)
	testCompletedCheckoutCannotReactivatePricing(t)
	testCheckoutReservationsPreserveSettledReceipts(t)
	testCheckoutCompletionWaitsForBinding(t)
	testEveryProviderPathWithGrant(t)
	testScheduledChangeWithGrant(t)
	testProviderBoundaryRequiresCompleteMetering(t)
	testProviderActivationWaitsForGrant(t)
	testProviderBoundaryRefusalLeavesNoInvoice(t)
	ctx := context.Background()
	start := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	end := start.AddDate(0, 1, 0)
	boundary := start.Add(15 * 24 * time.Hour)
	seedCompleteMetering(t, db, start, end)
	tenant := newPhaseTenant(t, db, "postpaid", start, end, 0)
	if _, writeErr := db.ExecContext(ctx, `UPDATE purser.tenant_subscriptions SET stripe_subscription_id='sub_grant',stripe_customer_id='cus_grant',payment_method='stripe' WHERE tenant_id=$1`, tenant.id); writeErr != nil {
		t.Fatal(writeErr)
	}
	if _, writeErr := db.ExecContext(ctx, `INSERT INTO purser.subscription_operator_grants(subscription_id,waive_usage,collection,reason,granted_at,expires_at,assigned_tier_id) SELECT id,true,'provider','midcycle waiver',$2,$3,tier_id FROM purser.tenant_subscriptions WHERE tenant_id=$1`, tenant.id, start, boundary); writeErr != nil {
		t.Fatal(writeErr)
	}
	recordEgressReceived(t, db, tenant.id, strings.Repeat("a", 64), start.Add(time.Hour), 100, start.Add(time.Hour))
	closing, err := PreparePostpaidGrantBoundary(ctx, db, tenant.jobs.logger, tenant.svc, tenant.id, boundary, false)
	if err != nil {
		t.Fatal(err)
	}
	if actionErr := withTx(ctx, db, func(tx *sql.Tx) error {
		if lockSubscriptionForPeriodCloseErr := purserdb.New(tx).LockSubscriptionForPeriodClose(ctx, tenant.id); lockSubscriptionForPeriodCloseErr != nil {
			return lockSubscriptionForPeriodCloseErr
		}
		if _, commitTxErr := closing.CommitTx(ctx, tx); commitTxErr != nil {
			return commitTxErr
		}
		_, deleteErr := tx.ExecContext(ctx, `DELETE FROM purser.subscription_operator_grants WHERE subscription_id=(SELECT id FROM purser.tenant_subscriptions WHERE tenant_id=$1)`, tenant.id)
		return deleteErr
	}); actionErr != nil {
		t.Fatal(actionErr)
	}
	recordEgressReceived(t, db, tenant.id, strings.Repeat("b", 64), boundary.Add(time.Hour), 40, boundary.Add(time.Hour))
	for _, status := range []string{"past_due", "active"} {
		payload := StripeWebhookPayload{Type: "customer.subscription.updated"}
		payload.Data.Object = json.RawMessage(fmt.Sprintf(`{"id":"sub_grant","customer":"cus_grant","status":%q,"items":{"data":[{"current_period_start":%d,"current_period_end":%d}]}}`, status, start.Unix(), end.Unix()))
		if handleStripeSubscriptionEventErr := tenant.svc.handleStripeSubscriptionEvent(payload); handleStripeSubscriptionEventErr != nil {
			t.Fatal(handleStripeSubscriptionEventErr)
		}
		got, _ := subscriptionPeriod(t, db, tenant.id)
		if !got.Equal(boundary) {
			t.Fatalf("%s webhook rewound phase to %s, want %s", status, got, boundary)
		}
	}
	tenant.monthEnd(t)
	first, last := readInvoice(t, db, tenant.id, start), readInvoice(t, db, tenant.id, boundary)
	if !first.periodEnd.Equal(boundary) || !last.periodEnd.Equal(end) {
		t.Fatalf("invoice boundaries: %+v %+v", first, last)
	}
	if first.metered != "0.00" || last.metered != "0.80" {
		t.Fatalf("metered charges before/after waiver expiry: %s/%s, want 0.00/0.80", first.metered, last.metered)
	}
	due, err := purserdb.New(db).ListSubscriptionsDueForInvoice(ctx, end.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if count := tenant.jobs.finalizeSubscriptionPeriods(ctx, due, end.Add(time.Hour), time.Time{}); count != 0 {
		t.Fatalf("replay generated %d invoices", count)
	}
	var count int
	if readErr := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM purser.billing_invoices WHERE tenant_id=$1 AND document_kind='invoice'`, tenant.id).Scan(&count); readErr != nil {
		t.Fatal(readErr)
	}
	if count != 2 {
		t.Fatalf("invoice count=%d, want 2", count)
	}
	nextEnd := end.AddDate(0, 1, 0)
	for _, cycle := range []struct {
		start, end time.Time
		status     string
	}{{end, nextEnd, "active"}, {start, end, "past_due"}, {start, end, "active"}} {
		payload := StripeWebhookPayload{Type: "customer.subscription.updated"}
		payload.Data.Object = json.RawMessage(fmt.Sprintf(`{"id":"sub_grant","customer":"cus_grant","status":%q,"items":{"data":[{"current_period_start":%d,"current_period_end":%d}]}}`, cycle.status, cycle.start.Unix(), cycle.end.Unix()))
		if handleStripeSubscriptionEventErr2 := tenant.svc.handleStripeSubscriptionEvent(payload); handleStripeSubscriptionEventErr2 != nil {
			t.Fatal(handleStripeSubscriptionEventErr2)
		}
		gotStart, gotEnd := subscriptionPeriod(t, db, tenant.id)
		if !gotStart.Equal(end) || !gotEnd.Equal(nextEnd) {
			t.Fatalf("out-of-order cycle rewound period to %s..%s, want %s..%s", gotStart, gotEnd, end, nextEnd)
		}
	}

	beforeEnd, err := purserdb.New(db).ListSubscriptionsDueForInvoice(ctx, nextEnd.Add(-time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	for _, subscription := range beforeEnd {
		if subscription.TenantID == tenant.id {
			t.Fatal("subscription became due before UTC period end")
		}
	}
}

func testRecoverableStripeStateKeepsWaivedUsage(t *testing.T) {
	t.Helper()
	for _, providerState := range []string{"unpaid", "incomplete_expired"} {
		t.Run(providerState+" preserves waived phase", func(t *testing.T) {
			db := startProviderReservationDatabase(t)
			ctx := t.Context()
			now := time.Now().UTC().Truncate(time.Second)
			start := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
			end := start.AddDate(0, 1, 0)
			seedCompleteMetering(t, db, start, end)
			tenant := newPhaseTenant(t, db, "postpaid", start, end, 0)
			sub, customer := "sub_"+tenant.id, "cus_"+tenant.id
			if _, writeErr := db.ExecContext(ctx, `UPDATE purser.tenant_subscriptions SET stripe_subscription_id=$2,stripe_customer_id=$3,payment_method='stripe' WHERE tenant_id=$1`, tenant.id, sub, customer); writeErr != nil {
				t.Fatal(writeErr)
			}
			if _, writeErr := db.ExecContext(ctx, `INSERT INTO purser.subscription_operator_grants(subscription_id,waive_usage,collection,reason,granted_at,assigned_tier_id) SELECT id,true,'provider','evaluation',$2,tier_id FROM purser.tenant_subscriptions WHERE tenant_id=$1`, tenant.id, start); writeErr != nil {
				t.Fatal(writeErr)
			}

			intent := uuid.NewString()
			if _, err := db.Exec(`INSERT INTO purser.payment_provider_intents(id,tenant_id,provider,purpose,status,currency,idempotency_key,provider_subscription_id,expires_at) VALUES($1::text::uuid,$2::text::uuid,'stripe','tenant_subscription_checkout','succeeded','EUR',$1::text,$3,NOW()-INTERVAL '1 hour')`, intent, tenant.id, sub); err != nil {
				t.Fatal(err)
			}
			for _, state := range []string{providerState, "active"} {
				payload := StripeWebhookPayload{Type: "customer.subscription.updated"}
				payload.Data.Object = json.RawMessage(fmt.Sprintf(`{"id":%q,"customer":%q,"status":%q,"metadata":{"checkout_reservation_id":%q,"tenant_id":%q},"items":{"data":[{"current_period_start":%d,"current_period_end":%d}]}}`, sub, customer, state, intent, tenant.id, start.Unix(), end.Unix()))
				if webhookErr := tenant.svc.handleStripeSubscriptionEvent(payload); webhookErr != nil {
					t.Fatal(webhookErr)
				}
				var actual string
				if err := db.QueryRow(`SELECT status FROM purser.tenant_subscriptions WHERE tenant_id=$1`, tenant.id).Scan(&actual); err != nil {
					t.Fatal(err)
				}
				wantStatus := MapStripeSubscriptionStatus(state, false)
				if state == "unpaid" {
					wantStatus = "past_due"
				}
				if actual != wantStatus {
					t.Fatalf("settled receipt swallowed renewal %s: %s", state, actual)
				}
			}
			recordEgressReceived(t, db, tenant.id, strings.Repeat(map[string]string{"unpaid": "c", "incomplete_expired": "d"}[providerState], 64), now.Add(end.Sub(now)/2), 100, now.Add(end.Sub(now)/2))
			tenant.monthEnd(t)
			invoice := readInvoice(t, db, tenant.id, start)
			if invoice.metered != "0.00" {
				t.Fatalf("%s recovery billed waived usage: %s", providerState, invoice.metered)
			}
			var grants int
			if readErr := db.QueryRowContext(ctx, `SELECT count(*) FROM purser.subscription_operator_grants g JOIN purser.tenant_subscriptions s ON s.id=g.subscription_id WHERE s.tenant_id=$1`, tenant.id).Scan(&grants); readErr != nil {
				t.Fatal(readErr)
			}
			if grants != 1 {
				t.Fatalf("%s recovery lost %d grants", providerState, 1-grants)
			}
		})
	}
}

func testProviderActivationRefusesGrant(t *testing.T) {
	t.Helper()
	for _, provider := range []string{"stripe", "mollie"} {
		t.Run(provider+" advance activation under grant", func(t *testing.T) {
			db := startPurserUsageRealPG(t)
			now := time.Now().UTC()
			tenant := newPhaseTenant(t, db, "prepaid", now, now.AddDate(0, 1, 0), 0)
			if _, seedErr := db.Exec(`INSERT INTO purser.subscription_operator_grants(subscription_id,waive_usage,reason) SELECT id,true,'advance protection' FROM purser.tenant_subscriptions WHERE tenant_id=$1`, tenant.id); seedErr != nil {
				t.Fatal(seedErr)
			}
			charged := 0
			tenant.svc.chargeAdvanceBaseFee = func(context.Context, string) error { charged++; return nil }
			callErr := tenant.svc.activateAdvanceBilledTenant(t.Context(), tenant.id, tenant.supporterID, provider, "customer", now, "")
			var tier, model string
			if readErr := db.QueryRow(`SELECT tier_id::text,billing_model FROM purser.tenant_subscriptions WHERE tenant_id=$1`, tenant.id).Scan(&tier, &model); readErr != nil {
				t.Fatal(readErr)
			}
			if callErr == nil || charged != 0 || tier != tenant.paygID || model != "prepaid" {
				t.Fatalf("grant bypass: err=%v charges=%d tier=%s model=%s", callErr, charged, tier, model)
			}
		})
	}
	t.Run("missing Stripe renewal is a no-op", func(t *testing.T) {
		db := startPurserUsageRealPG(t)
		service := &Service{db: db}
		_, callErr := service.activateTenantSubscriptionFromStripe(t.Context(), db, "10000000-0000-4000-8000-000000000099", "customer", "subscription", "", nil, nil, "")
		if callErr != nil {
			t.Fatalf("missing renewal must remain a no-op: %v", callErr)
		}
	})
}

func testEveryProviderPathWithGrant(t *testing.T) {
	t.Helper()
	for _, path := range []struct {
		name      string
		operation subscriptions.ProviderOperation
		provider  string
	}{
		{"Stripe checkout preparation", subscriptions.ProviderCheckout, "stripe"},
		{"Mollie subscription preparation", subscriptions.ProviderCheckout, "mollie"},
		{"Mollie first payment preparation", subscriptions.ProviderCheckout, "mollie"},
		{"Stripe subscription checkout", subscriptions.StripeCheckout, "stripe"},
		{"Stripe subscription renewal", subscriptions.StripeRenewal, "stripe"},
		{"Mollie subscription activation", subscriptions.MollieActivation, "mollie"},
		{"Stripe setup completion", subscriptions.AdvanceActivation, "stripe"},
		{"Mollie mandate completion", subscriptions.AdvanceActivation, "mollie"},
	} {
		for _, granted := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s grant=%t", path.name, granted), func(t *testing.T) {
				db := startPurserUsageRealPG(t)
				now := time.Now().UTC()
				tenant := newPhaseTenant(t, db, "prepaid", now, now.AddDate(0, 1, 0), 0)
				if path.operation == subscriptions.StripeRenewal {
					if _, seedErr := db.Exec(`UPDATE purser.tenant_subscriptions SET billing_model='postpaid',stripe_subscription_id='existing',stripe_customer_id='customer',status='cancelled' WHERE tenant_id=$1`, tenant.id); seedErr != nil {
						t.Fatal(seedErr)
					}
				}
				if granted {
					if _, seedErr := db.Exec(`INSERT INTO purser.subscription_operator_grants(subscription_id,waive_usage,reason,expires_at) SELECT id,true,'provider table',NOW()+INTERVAL '15 minutes' FROM purser.tenant_subscriptions WHERE tenant_id=$1`, tenant.id); seedErr != nil {
						t.Fatal(seedErr)
					}
				}
				var callErr error
				charges := 0
				switch path.operation {
				case subscriptions.StripeCheckout:
					if _, err := db.Exec(`INSERT INTO purser.payment_provider_intents(tenant_id,provider,purpose,local_reference_type,local_reference_id,status,currency,idempotency_key,provider_session_id) VALUES($1,'stripe','tenant_subscription_checkout','billing_tiers',$2,'provider_open','EUR','matrix-checkout','session')`, tenant.id, tenant.supporterID); err != nil {
						t.Fatal(err)
					}
					callErr = tenant.svc.handleSubscriptionCheckoutCompleted(t.Context(), "session", tenant.id, tenant.supporterID, "customer", "replacement", true, "")
				case subscriptions.StripeRenewal:
					_, callErr = tenant.svc.activateTenantSubscriptionFromStripe(t.Context(), db, tenant.id, "customer", "existing", tenant.supporterID, &now, nil, "")
				case subscriptions.AdvanceActivation:
					tenant.svc.chargeAdvanceBaseFee = func(context.Context, string) error { charges++; return nil }
					callErr = tenant.svc.activateAdvanceBilledTenant(t.Context(), tenant.id, tenant.supporterID, path.provider, "customer", now, "")
				default:
					_, callErr = subscriptions.TransitionProvider(t.Context(), db, subscriptions.ProviderChange{TenantID: tenant.id, Operation: path.operation, Mollie: purserdb.ActivateMollieTenantSubscriptionParams{TierID: tenant.supporterID, SubscriptionID: sql.NullString{String: "replacement", Valid: true}, PeriodStart: sql.NullTime{Time: now, Valid: true}, PeriodEnd: sql.NullTime{Time: now.AddDate(0, 1, 0), Valid: true}, NextPaymentDate: now.AddDate(0, 1, 0)}})
				}
				refuse := granted && path.operation != subscriptions.StripeRenewal
				if refuse && !errors.Is(callErr, subscriptions.ErrOutstandingGrant) {
					t.Fatalf("provider did not refuse grant: %v", callErr)
				}
				if !refuse && callErr != nil {
					t.Fatalf("provider path failed: %v", callErr)
				}
				var tier, status, model string
				var grants, documents int
				if readErr := db.QueryRow(`SELECT tier_id::text,status,billing_model,(SELECT count(*) FROM purser.subscription_operator_grants WHERE subscription_id=s.id),(SELECT count(*) FROM purser.billing_invoices WHERE tenant_id=s.tenant_id) FROM purser.tenant_subscriptions s WHERE tenant_id=$1`, tenant.id).Scan(&tier, &status, &model, &grants, &documents); readErr != nil {
					t.Fatal(readErr)
				}
				wantTier := tenant.supporterID
				if refuse || path.operation == subscriptions.ProviderCheckout || granted {
					wantTier = tenant.paygID
				}
				if tier != wantTier || (granted && grants != 1) || documents != 0 || (refuse && charges != 0) || (path.operation == subscriptions.StripeRenewal && status != "active") {
					t.Fatalf("provider result tier=%s want=%s status=%s model=%s grants=%d documents=%d charges=%d", tier, wantTier, status, model, grants, documents, charges)
				}
			})
		}
	}
}

func testProviderActivationWaitsForGrant(t *testing.T) {
	t.Helper()
	db := startPurserUsageRealPG(t)
	now := time.Now().UTC()
	tenant := newPhaseTenant(t, db, "prepaid", now, now.AddDate(0, 1, 0), 0)
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	tx, beginErr := db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if beginErr != nil {
		t.Fatal(beginErr)
	}
	if _, lockErr := purserdb.New(tx).LockTenantSubscriptionForOperatorAssignment(ctx, tenant.id); lockErr != nil {
		t.Fatal(lockErr)
	}
	done := make(chan error, 1)
	go func() {
		_, transitionErr := subscriptions.TransitionProvider(ctx, db, subscriptions.ProviderChange{TenantID: tenant.id, Operation: subscriptions.StripeCheckout, Stripe: purserdb.ActivateTenantSubscriptionFromStripeParams{TierID: tenant.supporterID, SubscriptionID: "replacement"}})
		done <- transitionErr
	}()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	waiting := false
	for !waiting {
		if readErr := db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE wait_event_type='Lock' AND query LIKE '%LockTenantSubscriptionForProviderChange%')`).Scan(&waiting); readErr != nil {
			t.Fatal(readErr)
		}
		if waiting {
			break
		}
		select {
		case early := <-done:
			t.Fatalf("activation did not wait on grant owner: %v", early)
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-ticker.C:
		}
	}
	if _, seedErr := tx.ExecContext(ctx, `INSERT INTO purser.subscription_operator_grants(subscription_id,waive_usage,reason) SELECT id,true,'concurrent waive-only grant' FROM purser.tenant_subscriptions WHERE tenant_id=$1`, tenant.id); seedErr != nil {
		t.Fatal(seedErr)
	}
	if commitErr := tx.Commit(); commitErr != nil {
		t.Fatal(commitErr)
	}
	if transitionErr := <-done; !errors.Is(transitionErr, subscriptions.ErrOutstandingGrant) {
		t.Fatalf("activation missed a grant committed while its row lock waited: %v", transitionErr)
	}
	var tier string
	if readErr := db.QueryRowContext(ctx, `SELECT tier_id::text FROM purser.tenant_subscriptions WHERE tenant_id=$1`, tenant.id).Scan(&tier); readErr != nil {
		t.Fatal(readErr)
	}
	if tier != tenant.paygID {
		t.Fatalf("raced provider changed tier: %s", tier)
	}
}

func testProviderBoundaryRefusalLeavesNoInvoice(t *testing.T) {
	t.Helper()
	db := startPurserUsageRealPG(t)
	now := time.Now().UTC().Truncate(time.Microsecond)
	start := now.Add(-24 * time.Hour)
	tenant := newPhaseTenant(t, db, "postpaid", start, start.AddDate(0, 1, 0), 0)
	if _, seedErr := db.Exec(`UPDATE purser.billing_tiers SET metering_enabled=false WHERE id=$1`, tenant.supporterID); seedErr != nil {
		t.Fatal(seedErr)
	}
	closing, prepareErr := PrepareProviderBillingBoundary(t.Context(), db, tenant.jobs.logger, tenant.svc, tenant.id, now)
	if prepareErr != nil {
		t.Fatal(prepareErr)
	}
	if closing == nil {
		t.Fatal("fixture did not prepare a billable boundary")
	}
	if _, seedErr := db.Exec(`INSERT INTO purser.subscription_operator_grants(subscription_id,waive_usage,reason) SELECT id,true,'arrived during boundary preparation' FROM purser.tenant_subscriptions WHERE tenant_id=$1`, tenant.id); seedErr != nil {
		t.Fatal(seedErr)
	}
	_, callErr := subscriptions.TransitionProvider(t.Context(), db, subscriptions.ProviderChange{TenantID: tenant.id, Operation: subscriptions.AdvanceActivation, Boundary: closing, Advance: purserdb.ActivateAdvanceBilledSubscriptionParams{TenantID: tenant.id, TierID: tenant.supporterID, PaymentMethod: "stripe", PeriodStart: now, PeriodEnd: now.AddDate(0, 1, 0)}})
	if !errors.Is(callErr, subscriptions.ErrOutstandingGrant) {
		t.Fatalf("prepared activation did not refuse concurrent grant: %v", callErr)
	}
	var invoices int
	var currentStart time.Time
	if readErr := db.QueryRow(`SELECT billing_period_start,(SELECT count(*) FROM purser.billing_invoices WHERE tenant_id=s.tenant_id) FROM purser.tenant_subscriptions s WHERE tenant_id=$1`, tenant.id).Scan(&currentStart, &invoices); readErr != nil {
		t.Fatal(readErr)
	}
	if invoices != 0 || !currentStart.Equal(start.Truncate(time.Microsecond)) {
		t.Fatalf("refusal committed a boundary: invoices=%d start=%s want=%s", invoices, currentStart, start)
	}

	acceptedDB := startPurserUsageRealPG(t)
	accepted := newPhaseTenant(t, acceptedDB, "postpaid", start, start.AddDate(0, 1, 0), 0)
	seedCompleteMetering(t, acceptedDB, start.Truncate(5*time.Minute), now.Add(5*time.Minute))
	prepared, prepareErr := PrepareProviderBillingBoundary(t.Context(), acceptedDB, accepted.jobs.logger, accepted.svc, accepted.id, now)
	if prepareErr != nil {
		t.Fatal(prepareErr)
	}
	result, activateErr := subscriptions.TransitionProvider(t.Context(), acceptedDB, subscriptions.ProviderChange{TenantID: accepted.id, Operation: subscriptions.AdvanceActivation, Boundary: prepared, Advance: purserdb.ActivateAdvanceBilledSubscriptionParams{TenantID: accepted.id, TierID: accepted.paygID, PaymentMethod: "stripe", PeriodStart: now, PeriodEnd: now.AddDate(0, 1, 0)}})
	if activateErr != nil || result.Rows != 1 {
		t.Fatalf("activation without a grant failed: rows=%d err=%v", result.Rows, activateErr)
	}
	var currentTier string
	if readErr := acceptedDB.QueryRow(`SELECT tier_id::text,billing_period_start,(SELECT count(*) FROM purser.billing_invoices WHERE tenant_id=s.tenant_id) FROM purser.tenant_subscriptions s WHERE tenant_id=$1`, accepted.id).Scan(&currentTier, &currentStart, &invoices); readErr != nil {
		t.Fatal(readErr)
	}
	if invoices != 1 || !currentStart.Equal(now) || currentTier != accepted.paygID {
		t.Fatalf("activation did not commit its invoice, period and tier together: invoices=%d start=%s tier=%s", invoices, currentStart, currentTier)
	}
}

func testScheduledChangeWithGrant(t *testing.T) {
	t.Helper()
	for _, granted := range []bool{false, true} {
		t.Run(fmt.Sprintf("scheduled tier change grant=%t", granted), func(t *testing.T) {
			db := startPurserUsageRealPG(t)
			now := time.Now().UTC()
			tenant := newPhaseTenant(t, db, "postpaid", now.Add(-time.Hour), now.Add(time.Hour), 0)
			if _, err := db.Exec(`UPDATE purser.tenant_subscriptions SET pending_tier_id=$2,pending_reason='downgrade',pending_effective_at=NOW()-INTERVAL '1 second' WHERE tenant_id=$1`, tenant.id, tenant.paygID); err != nil {
				t.Fatal(err)
			}
			if granted {
				if _, err := db.Exec(`INSERT INTO purser.subscription_operator_grants(subscription_id,base_price,waive_usage,reason,assigned_tier_id) SELECT id,0,true,'protected',tier_id FROM purser.tenant_subscriptions WHERE tenant_id=$1`, tenant.id); err != nil {
					t.Fatal(err)
				}
			}
			tenant.jobs.tierReconciler = &stubTierReconciler{}
			tenant.jobs.applyPendingDowngrade(t.Context(), tenant.id)
			var got string
			if err := db.QueryRow(`SELECT tier_id::text FROM purser.tenant_subscriptions WHERE tenant_id=$1`, tenant.id).Scan(&got); err != nil {
				t.Fatal(err)
			}
			want := tenant.paygID
			if granted {
				want = tenant.supporterID
			}
			if got != want {
				t.Fatalf("scheduled change with grant=%t tier=%s want=%s", granted, got, want)
			}
		})
	}
}

func testProviderBoundaryRequiresCompleteMetering(t *testing.T) {
	t.Helper()
	db := startPurserUsageRealPG(t)
	now := time.Now().UTC().Truncate(5 * time.Minute)
	start := now.Add(-10 * time.Minute)
	tenant := newPhaseTenant(t, db, "postpaid", start, now.AddDate(0, 1, 0), 0)
	charges := 0
	tenant.svc.chargeAdvanceBaseFee = func(context.Context, string) error { charges++; return nil }
	err := tenant.svc.activateAdvanceBilledTenant(t.Context(), tenant.id, tenant.paygID, "stripe", "customer", now, "")
	if err == nil || !strings.Contains(err.Error(), "metering") {
		t.Fatalf("incomplete advance period was finalized: %v", err)
	}
	var tier string
	var documents int
	if err = db.QueryRow(`SELECT tier_id::text,(SELECT count(*) FROM purser.billing_invoices WHERE tenant_id=s.tenant_id) FROM purser.tenant_subscriptions s WHERE tenant_id=$1`, tenant.id).Scan(&tier, &documents); err != nil {
		t.Fatal(err)
	}
	if tier != tenant.supporterID || documents != 0 || charges != 0 {
		t.Fatalf("incomplete close mutated billing: tier=%s documents=%d charges=%d", tier, documents, charges)
	}
	seedCompleteMetering(t, db, start, now)
	if err = tenant.svc.activateAdvanceBilledTenant(t.Context(), tenant.id, tenant.paygID, "stripe", "customer", now, ""); err != nil {
		t.Fatal(err)
	}
	if charges != 1 {
		t.Fatalf("complete activation charges=%d", charges)
	}
}

func testCompletedCheckoutCannotReactivatePricing(t *testing.T) {
	t.Helper()
	for _, setup := range []bool{false, true} {
		t.Run(fmt.Sprintf("completed checkout replay setup=%t", setup), func(t *testing.T) {
			db := startPurserUsageRealPG(t)
			now := time.Now().UTC()
			tenant := newPhaseTenant(t, db, "prepaid", now, now.AddDate(0, 1, 0), 0)
			if _, err := db.Exec(`INSERT INTO purser.subscription_operator_grants(subscription_id,base_price,waive_usage,reason) SELECT id,0,true,'later grant' FROM purser.tenant_subscriptions WHERE tenant_id=$1`, tenant.id); err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec(`INSERT INTO purser.payment_provider_intents(tenant_id,provider,purpose,local_reference_type,local_reference_id,status,currency,idempotency_key,provider_session_id) VALUES($1,'stripe','tenant_subscription_checkout','billing_tiers',$2,'succeeded','EUR','completed-checkout','completed-session')`, tenant.id, tenant.paygID); err != nil {
				t.Fatal(err)
			}
			var err error
			if setup {
				err = tenant.svc.handleStripeSetupCheckoutCompleted(t.Context(), "completed-session", tenant.id, tenant.supporterID, "customer", "setup", "")
			} else {
				err = tenant.svc.handleSubscriptionCheckoutCompleted(t.Context(), "completed-session", tenant.id, tenant.supporterID, "customer", "subscription", true, "")
			}
			if err != nil {
				t.Fatalf("settled checkout replay remains retryable under a later grant: %v", err)
			}
			var tier string
			if err = db.QueryRow(`SELECT tier_id::text FROM purser.tenant_subscriptions WHERE tenant_id=$1`, tenant.id).Scan(&tier); err != nil {
				t.Fatal(err)
			}
			if tier != tenant.paygID {
				t.Fatal("settled checkout replay changed pricing")
			}
		})
	}
}

func testCheckoutReservationsPreserveSettledReceipts(t *testing.T) {
	t.Helper()
	for _, provider := range []string{"stripe", "mollie-first", "mollie-sub"} {
		t.Run("checkout attempts "+provider, func(t *testing.T) {
			db := startProviderReservationDatabase(t)
			now := time.Now().UTC()
			tenant := newPhaseTenant(t, db, "prepaid", now, now.AddDate(0, 1, 0), 0)
			logicalKey := provider + ":" + tenant.id + ":" + tenant.supporterID + ":" + uuid.NewString()
			reserve := func(tx *sql.Tx) (string, error) {
				q := purserdb.New(tx)
				switch provider {
				case "stripe":
					return q.UpsertStripeTenantCheckoutIntent(t.Context(), purserdb.UpsertStripeTenantCheckoutIntentParams{TenantID: tenant.id, TierID: tenant.supporterID, Currency: "EUR", IdempotencyKey: logicalKey})
				case "mollie-first":
					return q.UpsertMollieFirstPaymentIntent(t.Context(), purserdb.UpsertMollieFirstPaymentIntentParams{TenantID: tenant.id, TierID: tenant.supporterID, Currency: "EUR", AmountCents: 100, IdempotencyKey: logicalKey})
				default:
					return q.UpsertMollieSubscriptionIntent(t.Context(), purserdb.UpsertMollieSubscriptionIntentParams{TenantID: tenant.id, TierID: tenant.supporterID, CustomerID: sql.NullString{String: "customer", Valid: true}, Currency: "EUR", AmountCents: 100, IdempotencyKey: logicalKey})
				}
			}

			legacyID := uuid.NewString()
			providerName, purpose := "mollie", "mollie_subscription_create"
			if provider == "stripe" {
				providerName, purpose = "stripe", "tenant_subscription_checkout"
			}
			if provider == "mollie-first" {
				purpose = "mollie_first_payment"
			}
			if _, err := db.Exec(`INSERT INTO purser.payment_provider_intents(id,tenant_id,provider,purpose,local_reference_id,status,currency,idempotency_key,created_at) VALUES($1,$2,$3,$4,$5,'provider_call_failed','EUR',$6,NOW()-INTERVAL '25 hours')`, legacyID, tenant.id, providerName, purpose, tenant.supporterID, logicalKey); err != nil {
				t.Fatal(err)
			}
			if err := tenant.svc.expireCheckoutReservation(t.Context(), checkoutReservationExpiry{TenantID: tenant.id, IntentID: legacyID}); err != nil {
				t.Fatal(err)
			}
			first, err := subscriptions.ReserveCheckout(t.Context(), db, tenant.id, reserve)
			if err != nil {
				t.Fatal(err)
			}

			if first == legacyID {
				t.Fatal("new request reused an expired attempt key")
			}
			retry, err := subscriptions.ReserveCheckout(t.Context(), db, tenant.id, reserve)
			if err != nil || retry != first {
				t.Fatalf("open checkout retry changed attempt: %s/%s,%v", first, retry, err)
			}
			if _, err = db.Exec(`UPDATE purser.payment_provider_intents SET status='succeeded',provider_session_id=CASE WHEN provider='stripe' THEN 'settled-session' END,provider_payment_id=CASE WHEN purpose='mollie_first_payment' THEN 'settled-payment' END,provider_subscription_id=CASE WHEN purpose='mollie_subscription_create' THEN 'settled-subscription' END WHERE tenant_id=$1 AND id=$2`, tenant.id, first); err != nil {
				t.Fatal(err)
			}
			next, err := subscriptions.ReserveCheckout(t.Context(), db, tenant.id, reserve)
			if err != nil {
				t.Fatal(err)
			}
			if next == first {
				t.Fatal("new checkout reused a settled receipt instead of reserving a new attempt")
			}
			var firstKey, nextKey, firstStatus, nextStatus string
			if err = db.QueryRow(`SELECT a.idempotency_key,b.idempotency_key,a.status,b.status FROM purser.payment_provider_intents a,purser.payment_provider_intents b WHERE a.tenant_id=$1 AND b.tenant_id=$1 AND a.id=$2 AND b.id=$3`, tenant.id, first, next).Scan(&firstKey, &nextKey, &firstStatus, &nextStatus); err != nil {
				t.Fatal(err)
			}
			if len(firstKey) > 128 || len(nextKey) > 128 {
				t.Fatalf("provider key exceeds the schema bound: %d/%d", len(firstKey), len(nextKey))
			}
			if firstKey == nextKey || firstStatus != "succeeded" || nextStatus != "pending" {
				t.Fatalf("receipt was overwritten: keys=%s/%s states=%s/%s", firstKey, nextKey, firstStatus, nextStatus)
			}
			if pending, err := pendingProviderCheckoutForTest(t.Context(), db, tenant.id); err != nil || !pending {
				t.Fatalf("new checkout has no grant fence: %v,%v", pending, err)
			}
		})
	}
}

func startProviderReservationDatabase(t *testing.T) *sql.DB {
	t.Helper()
	if strings.Contains(t.Name(), "_RealYugabyte") {
		db, ok := dockerpg.OpenSharedYugabyteBaseline(t, "provider_reservations", "purser")
		if !ok {
			t.Skip("requires the shared Yugabyte fixture")
		}
		return db
	}
	return startPurserUsageRealPG(t)
}

func TestProviderReservationLifecycle_RealYugabyte(t *testing.T) {
	// Subtest cleanup returns each baseline to the shared pool before the next case.
	for _, tc := range []struct {
		name string
		run  func(*testing.T)
	}{
		{"AdvanceActivationStartsOneCappedCalendarMonth", TestAdvanceActivationStartsOneCappedCalendarMonth_RealPG},
		{"AdvanceSetupRefusesMidPeriodTierChange", TestAdvanceSetupRefusesMidPeriodTierChange_RealPG},
		{"IneligibleCheckoutCompensates", TestIneligibleCheckoutCompensates_RealPG},
		{"SwitchToPrepaidForfeitsUnusedBaseFee", TestSwitchToPrepaidForfeitsTheUnusedAdvanceBaseFee_RealPG},
		{"AdvanceBaseFeeCreditIsCappedAndAudited", TestAdvanceBaseFeeCreditIsCappedAndAudited_RealPG},
		{"ReplacementBaseFeeCannotBeCreditedTwice", TestReplacementBaseFeeCannotBeCreditedTwice_RealPG},
		{"CheckoutRefundSettlementAndOperatorCredit", TestCheckoutRefundSettlementAndOperatorCredit_RealPG},
		{"HistoricalMollieSubscriptionCheckoutSettlesWithoutRefund", TestHistoricalMollieSubscriptionCheckoutSettlesWithoutRefund_RealPG},
		{"CheckoutWakeStartsNewRecoveryAttempt", TestCheckoutWakeStartsNewRecoveryAttempt_RealPG},
		{"DefiniteCheckoutRejectionReleasesOnlyUnboundIntent", TestDefiniteCheckoutRejectionReleasesOnlyUnboundIntent_RealPG},
		{"SuspensionStopRetriesAndRespectsResumption", TestSuspensionStopRetriesAndRespectsResumption_RealPG},
		{"TenantClosureRecoversUnfulfilledClusterReceipts", TestTenantClosureRecoversUnfulfilledClusterReceipts_RealPG},
		{"TenantClosureCancelsPendingTierUpgrade", TestTenantClosureCancelsPendingTierUpgrade_RealPG},
		{"TenantClosureCancelsLateClusterSubscription", TestTenantClosureCancelsLateClusterSubscription_RealPG},
		{"TenantClosureDoesNotInvoicePhaseOpenedAfterCutoff", TestTenantClosureDoesNotInvoicePhaseOpenedAfterCutoff_RealPG},
		{"TenantClosureFencesWaitingPricingWrite", TestTenantClosureFencesWaitingPricingWrite_RealPG},
		{"HistoricalRecoveryDoesNotReplaceEstablishedSubscription", TestHistoricalRecoveryDoesNotReplaceEstablishedSubscription_RealPG},
		{"CheckoutRecoveryAuditTerminalResultIsImmutable", TestCheckoutRecoveryAuditTerminalResultIsImmutable_RealPG},
		{"MollieCompletionSurvivesLateSettlement", TestMollieCompletionSurvivesLateSettlement_RealPG},
		{"ProviderWebhookAlternatingWaitFailureAlerts", TestProviderWebhookAlternatingWaitFailureAlerts_RealPG},
		{"CheckoutRecoveryTerminalOutcomeStable", TestCheckoutRecoveryTerminalOutcomeStable_RealPG},
		{"ForcedRecoveryOnlyLocatesProviderObjects", TestForcedRecoveryOnlyLocatesProviderObjects_RealPG},
		{"AsyncFailureKeepsFenceUntilCancellation", TestAsyncFailureKeepsFenceUntilCancellation_RealPG},
		{"TenantClosureStopsBilling", TestTenantClosureStopsBilling_RealPG},
		{"PendingCheckoutPreservesEstablishedStripeSubscription", testPendingCheckoutPreservesEstablishedStripeSubscription},
		{"RecoverableStripeStateKeepsWaivedUsage", testRecoverableStripeStateKeepsWaivedUsage},
		{"ProviderCheckoutReservationDeadline", testProviderCheckoutReservationDeadline},
		{"StripeDelayedSettlementKeepsReservationUntilTerminal", TestStripeDelayedSettlementKeepsReservationUntilTerminal_RealPG},
		{"HistoricalCompletedStripeCheckoutAndUnboundRecovery", TestHistoricalCompletedStripeCheckoutAndUnboundRecovery_RealPG},
		{"ProviderSettlementWaitBackoffAndAgeAlert", TestProviderSettlementWaitBackoffAndAgeAlert_RealPG},
		{"CheckoutRequestJournalAndLateResponseHandoff", TestCheckoutRequestJournalAndLateResponseHandoff_RealPG},
		{"MollieRequestJournalHasOneHourCredentialScope", TestMollieRequestJournalHasOneHourCredentialScope_RealPG},
		{"PreparedCheckoutCanRecoverWithoutProvider", TestPreparedCheckoutCanRecoverWithoutProvider_RealPG},
		{"ProviderCompletionBeforeDeadlineDeliveredLate", TestProviderCompletionBeforeDeadlineDeliveredLate_RealPG},
		{"ProviderCheckoutLegacyNaturalWindow", TestProviderCheckoutLegacyNaturalWindow_RealPG},
		{"RecoveryPreservesProviderCompletionAndOperatorCanCloseOpenCheckout", TestRecoveryPreservesProviderCompletionAndOperatorCanCloseOpenCheckout_RealPG},
		{"ProviderCheckoutDeadlinesRecoverAbandonedAttempts", testProviderCheckoutDeadlinesRecoverAbandonedAttempts},
		{"ProviderCheckoutLegacyRowsDoNotFence", testProviderCheckoutLegacyRowsDoNotFence},
		{"CheckoutExpiryReleasesStagedReservation", testCheckoutExpiryReleasesStagedReservation},
		{"CheckoutReservationsPreserveSettledReceipts", testCheckoutReservationsPreserveSettledReceipts},
		{"CheckoutCompletionWaitsForBinding", testCheckoutCompletionWaitsForBinding},
	} {
		t.Run(tc.name, tc.run)
	}
}

func testCheckoutCompletionWaitsForBinding(t *testing.T) {
	t.Helper()
	db := startProviderReservationDatabase(t)
	now := time.Now().UTC()
	tenant := newPhaseTenant(t, db, "prepaid", now, now.AddDate(0, 1, 0), 0)
	reserve := func(tx *sql.Tx) (string, error) {
		return purserdb.New(tx).UpsertStripeTenantCheckoutIntent(t.Context(), purserdb.UpsertStripeTenantCheckoutIntentParams{TenantID: tenant.id, TierID: tenant.supporterID, Currency: "EUR", IdempotencyKey: "early-response"})
	}
	intent, err := subscriptions.ReserveCheckout(t.Context(), db, tenant.id, reserve)
	if err != nil {
		t.Fatal(err)
	}
	err = tenant.svc.handleSubscriptionCheckoutCompleted(t.Context(), "early-session", tenant.id, tenant.supporterID, "customer", "subscription", true, intent)
	if err == nil || !strings.Contains(err.Error(), "binding") {
		t.Fatalf("unbound callback activated pricing: %v", err)
	}
	var tier string
	if err = db.QueryRow(`SELECT tier_id::text FROM purser.tenant_subscriptions WHERE tenant_id=$1`, tenant.id).Scan(&tier); err != nil {
		t.Fatal(err)
	}
	if tier != tenant.paygID {
		t.Fatal("unbound callback changed tier")
	}
	if err = purserdb.New(db).SetProviderIntentSessionOpen(t.Context(), purserdb.SetProviderIntentSessionOpenParams{TenantID: tenant.id, IntentID: intent, SessionID: sql.NullString{String: "early-session", Valid: true}}); err != nil {
		t.Fatal(err)
	}
	if err = tenant.svc.handleSubscriptionCheckoutCompleted(t.Context(), "early-session", tenant.id, tenant.supporterID, "customer", "subscription", true, intent); err != nil {
		t.Fatal(err)
	}
	if err = purserdb.New(db).SetProviderIntentSessionOpen(t.Context(), purserdb.SetProviderIntentSessionOpenParams{TenantID: tenant.id, IntentID: intent, SessionID: sql.NullString{String: "early-session", Valid: true}}); err != nil {
		t.Fatal(err)
	}
	if err = db.QueryRow(`SELECT status FROM purser.payment_provider_intents WHERE tenant_id=$1 AND id=$2`, tenant.id, intent).Scan(&tier); err != nil {
		t.Fatal(err)
	}
	if tier != "succeeded" {
		t.Fatalf("late response reopened settled receipt: %s", tier)
	}
}

func TestProviderCheckoutExpiryReleasesReservation_RealPG(t *testing.T) {
	testCheckoutExpiryReleasesStagedReservation(t)
}

func testCheckoutExpiryReleasesStagedReservation(t *testing.T) {
	t.Helper()
	db := startProviderReservationDatabase(t)
	now := time.Now().UTC()
	tenant := newPhaseTenant(t, db, "prepaid", now, now.AddDate(0, 1, 0), 0)
	q := purserdb.New(db)
	intent, err := subscriptions.ReserveCheckout(t.Context(), db, tenant.id, func(tx *sql.Tx) (string, error) {
		return purserdb.New(tx).UpsertStripeTenantCheckoutIntent(t.Context(), purserdb.UpsertStripeTenantCheckoutIntentParams{TenantID: tenant.id, TierID: tenant.supporterID, Currency: "EUR", IdempotencyKey: "expiry-reservation"})
	})
	if err != nil {
		t.Fatal(err)
	}
	if err = q.SetProviderIntentSessionOpen(t.Context(), purserdb.SetProviderIntentSessionOpenParams{TenantID: tenant.id, IntentID: intent, SessionID: sql.NullString{String: "expired-session", Valid: true}}); err != nil {
		t.Fatal(err)
	}
	if rows, err := q.StageStripeCheckoutTier(t.Context(), purserdb.StageStripeCheckoutTierParams{TenantID: tenant.id, TierID: tenant.supporterID, IntentID: intent}); err != nil || rows != 1 {
		t.Fatalf("stage=%d,%v", rows, err)
	}
	if err = q.FinishProviderCheckoutIntent(t.Context(), purserdb.FinishProviderCheckoutIntentParams{TenantID: tenant.id, IntentID: intent, Status: "expired", SessionID: sql.NullString{String: "expired-session", Valid: true}}); err != nil {
		t.Fatal(err)
	}
	if pending, err := pendingProviderCheckoutForTest(t.Context(), db, tenant.id); err != nil || pending {
		t.Fatalf("confirmed expiry still blocks a grant: %v,%v", pending, err)
	}
	// A late expiry of the old attempt must preserve a later staged checkout.
	next, err := subscriptions.ReserveCheckout(t.Context(), db, tenant.id, func(tx *sql.Tx) (string, error) {
		return purserdb.New(tx).UpsertStripeTenantCheckoutIntent(t.Context(), purserdb.UpsertStripeTenantCheckoutIntentParams{TenantID: tenant.id, TierID: tenant.supporterID, Currency: "EUR", IdempotencyKey: "expiry-reservation"})
	})
	if err != nil {
		t.Fatal(err)
	}
	if rows, err := q.StageStripeCheckoutTier(t.Context(), purserdb.StageStripeCheckoutTierParams{TenantID: tenant.id, TierID: tenant.supporterID, IntentID: next}); err != nil || rows != 1 {
		t.Fatalf("next stage=%d,%v", rows, err)
	}

	lateExpiry := StripeWebhookPayload{Type: "checkout.session.expired"}
	lateExpiry.Data.Object = json.RawMessage(fmt.Sprintf(`{"id":"expired-session","metadata":{"purpose":"subscription","tenant_id":%q}}`, tenant.id))
	if err = tenant.svc.handleStripeCheckoutExpired(lateExpiry); err != nil {
		t.Fatal(err)
	}

	var staged string
	if err = db.QueryRow(`SELECT pending_intent_id::text FROM purser.tenant_subscriptions WHERE tenant_id=$1`, tenant.id).Scan(&staged); err != nil || staged != next {
		t.Fatalf("old expiry erased the later reservation: %s,%v", staged, err)
	}
	if err = tenant.svc.handleSubscriptionCheckoutCompleted(t.Context(), "expired-session", tenant.id, tenant.supporterID, "customer", "subscription", true, ""); err != nil {
		t.Fatal(err)
	}
	var tier string
	if err = db.QueryRow(`SELECT tier_id::text FROM purser.tenant_subscriptions WHERE tenant_id=$1`, tenant.id).Scan(&tier); err != nil {
		t.Fatal(err)
	}
	if tier != tenant.paygID {
		t.Fatal("expired checkout replay activated pricing")
	}
}

func TestProviderCheckoutLegacyRowsDoNotFence_RealPG(t *testing.T) {
	testProviderCheckoutLegacyRowsDoNotFence(t)
}

func testProviderCheckoutLegacyRowsDoNotFence(t *testing.T) {
	t.Helper()
	for _, state := range []string{"provider_open", "provider_call_failed"} {
		t.Run(state, func(t *testing.T) {
			db := startProviderReservationDatabase(t)
			now := time.Now().UTC()
			tenant := newPhaseTenant(t, db, "postpaid", now, now.AddDate(0, 1, 0), 0)
			if _, err := db.Exec(`UPDATE purser.tenant_subscriptions SET mollie_subscription_id=$2 WHERE tenant_id=$1`, tenant.id, "sub_existing_"+state); err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec(`INSERT INTO purser.payment_provider_intents(tenant_id,provider,purpose,local_reference_type,local_reference_id,status,currency,idempotency_key,provider_subscription_id,created_at) VALUES($1,'mollie','mollie_subscription_create','billing_tiers',$2,$3,'EUR',$4,$5,NOW()-INTERVAL '25 hours')`, tenant.id, tenant.supporterID, state, uuid.NewString(), "sub_existing_"+state); err != nil {
				t.Fatal(err)
			}
			err := database.WithRetryablePostgresTx(t.Context(), db, nil, func(tx *sql.Tx) error {
				if _, err := subscriptions.LockChange(t.Context(), tx, tenant.id, subscriptions.PreservePricing); err != nil {
					return err
				}
				return subscriptions.CheckGrantCheckout(t.Context(), tx, tenant.id)
			})
			if err != nil {
				t.Fatalf("established Mollie receipt blocked a grant: %v", err)
			}
		})
	}
}

func TestProviderCheckoutReservationDeadline_RealPG(t *testing.T) {
	testProviderCheckoutReservationDeadline(t)
}

func testProviderCheckoutReservationDeadline(t *testing.T) {
	db := startProviderReservationDatabase(t)
	now := time.Now().UTC()
	tenant := newPhaseTenant(t, db, "prepaid", now, now.AddDate(0, 1, 0), 0)
	intent, err := subscriptions.ReserveCheckout(t.Context(), db, tenant.id, func(tx *sql.Tx) (string, error) {
		return purserdb.New(tx).UpsertStripeTenantCheckoutIntent(t.Context(), purserdb.UpsertStripeTenantCheckoutIntentParams{TenantID: tenant.id, TierID: tenant.supporterID, Currency: "EUR", IdempotencyKey: "deadline"})
	})
	if err != nil {
		t.Fatal(err)
	}
	var deadline sql.NullTime
	if err = db.QueryRow(`SELECT expires_at FROM purser.payment_provider_intents WHERE tenant_id=$1 AND id=$2`, tenant.id, intent).Scan(&deadline); err != nil {
		t.Fatal(err)
	}
	if !deadline.Valid || !deadline.Time.After(now.Add(30*time.Minute)) || deadline.Time.After(now.Add(23*time.Hour+30*time.Minute)) {
		t.Fatalf("reservation deadline lacks a safe provider clock-skew margin: %v", deadline)
	}
	if pending, err := pendingProviderCheckoutForTest(t.Context(), db, tenant.id); err != nil || !pending {
		t.Fatalf("live reservation did not fence: %v,%v", pending, err)
	}
	if _, err = db.Exec(`UPDATE purser.payment_provider_intents SET expires_at=NOW()-INTERVAL '1 second' WHERE tenant_id=$1 AND id=$2`, tenant.id, intent); err != nil {
		t.Fatal(err)
	}
	if pending, err := pendingProviderCheckoutForTest(t.Context(), db, tenant.id); err != nil || !pending {
		t.Fatalf("unreconciled reservation lost fence: %v %v", pending, err)
	}
	if err := tenant.svc.expireCheckoutReservation(t.Context(), checkoutReservationExpiry{TenantID: tenant.id, IntentID: intent}); err != nil {
		t.Fatal(err)
	}
	if pending, err := pendingProviderCheckoutForTest(t.Context(), db, tenant.id); err != nil || pending {
		t.Fatalf("confirmed abandoned reservation still fences: %v %v", pending, err)
	}
}

func TestProviderCheckoutAbandonedAttempts_RealPG(t *testing.T) {
	testProviderCheckoutDeadlinesRecoverAbandonedAttempts(t)
}

func testProviderCheckoutDeadlinesRecoverAbandonedAttempts(t *testing.T) {
	t.Helper()
	for _, state := range []string{"pending", "provider_open", "provider_call_failed", "terminal_failed"} {
		t.Run(state, func(t *testing.T) {
			db := startProviderReservationDatabase(t)
			now := time.Now().UTC()
			tenant := newPhaseTenant(t, db, "prepaid", now, now.AddDate(0, 1, 0), 0)
			intent, err := subscriptions.ReserveCheckout(t.Context(), db, tenant.id, func(tx *sql.Tx) (string, error) {
				return purserdb.New(tx).UpsertStripeTenantCheckoutIntent(t.Context(), purserdb.UpsertStripeTenantCheckoutIntentParams{TenantID: tenant.id, TierID: tenant.supporterID, Currency: "EUR", IdempotencyKey: uuid.NewString()})
			})
			if err != nil {
				t.Fatal(err)
			}
			var queued int
			if err = db.QueryRow(`SELECT COUNT(*) FROM purser.provider_webhook_inbox WHERE event_key=$1 AND next_attempt_at>NOW()`, "checkout-expiry:"+intent).Scan(&queued); err != nil || queued != 1 {
				t.Fatalf("reservation has no durable deadline recovery: %d,%v", queued, err)
			}
			if _, err = db.Exec(`UPDATE purser.payment_provider_intents SET status=$3,expires_at=NOW()-INTERVAL '1 second' WHERE tenant_id=$1 AND id=$2`, tenant.id, intent, state); err != nil {
				t.Fatal(err)
			}

			if err = tenant.svc.expireCheckoutReservation(t.Context(), checkoutReservationExpiry{TenantID: tenant.id, IntentID: intent}); err != nil {
				t.Fatalf("abandoned pre-call recovery: %v", err)
			}
			var recoveredStatus string
			wantStatus := "expired"
			if state == "terminal_failed" {
				wantStatus = state
			}
			if err = db.QueryRow(`SELECT status FROM purser.payment_provider_intents WHERE tenant_id=$1 AND id=$2`, tenant.id, intent).Scan(&recoveredStatus); err != nil || recoveredStatus != wantStatus {
				t.Fatalf("abandoned pre-call status=%s, want %s: %v", recoveredStatus, wantStatus, err)
			}

			if _, err = db.Exec(`UPDATE purser.provider_webhook_inbox SET status='processed' WHERE event_key=$1`, "checkout-expiry:"+intent); err != nil {
				t.Fatal(err)
			}
			payload, err := json.Marshal(map[string]any{"id": "late-" + intent, "customer": "customer", "subscription": "subscription", "payment_status": "paid", "metadata": map[string]string{"purpose": "subscription", "tenant_id": tenant.id, "tier_id": tenant.supporterID, "reference_id": tenant.supporterID, "checkout_reservation_id": intent}})
			if err != nil {
				t.Fatal(err)
			}
			if err = tenant.svc.DispatchStripeCheckoutCompleted(t.Context(), payload); err != nil {
				t.Fatalf("expired unbound callback kept retrying: %v", err)
			}

			if err = db.QueryRow(`SELECT COUNT(*) FROM purser.provider_webhook_inbox WHERE event_key=$1 AND status='pending'`, "checkout-expiry:"+intent).Scan(&queued); err != nil || queued != 1 {
				t.Fatalf("late receipt did not wake recovery: %d,%v", queued, err)
			}
			var tier string
			if err = db.QueryRow(`SELECT tier_id::text FROM purser.tenant_subscriptions WHERE tenant_id=$1`, tenant.id).Scan(&tier); err != nil || tier != tenant.paygID {
				t.Fatalf("late checkout changed tier: %s,%v", tier, err)
			}
			if pending, err := pendingProviderCheckoutForTest(t.Context(), db, tenant.id); err != nil || pending {
				t.Fatalf("abandoned attempt still fenced: %v,%v", pending, err)
			}
		})
	}
}

func TestPendingCheckoutPreservesEstablishedStripeSubscription_RealPG(t *testing.T) {
	testPendingCheckoutPreservesEstablishedStripeSubscription(t)
}

func testPendingCheckoutPreservesEstablishedStripeSubscription(t *testing.T) {
	t.Helper()
	db := startProviderReservationDatabase(t)
	now := time.Now().UTC()
	tenant := newPhaseTenant(t, db, "postpaid", now, now.AddDate(0, 1, 0), 0)
	customer, established, pending := "customer_"+tenant.id, "established_"+tenant.id, "pending_"+tenant.id
	if _, err := db.Exec(`UPDATE purser.tenant_subscriptions SET stripe_customer_id=$2,stripe_subscription_id=$3,payment_method='stripe' WHERE tenant_id=$1`, tenant.id, customer, established); err != nil {
		t.Fatal(err)
	}
	intent, err := subscriptions.ReserveCheckout(t.Context(), db, tenant.id, func(tx *sql.Tx) (string, error) {
		return purserdb.New(tx).UpsertStripeTenantCheckoutIntent(t.Context(), purserdb.UpsertStripeTenantCheckoutIntentParams{TenantID: tenant.id, TierID: tenant.supporterID, Currency: "EUR", IdempotencyKey: uuid.NewString()})
	})
	if err != nil {
		t.Fatal(err)
	}
	payload, err := json.Marshal(map[string]any{"id": "async_" + intent, "customer": customer, "subscription": pending, "payment_status": "unpaid", "metadata": map[string]string{"purpose": "subscription", "tenant_id": tenant.id, "reference_id": tenant.supporterID, "checkout_reservation_id": intent}})
	if err != nil {
		t.Fatal(err)
	}
	if err = tenant.svc.DispatchStripeCheckoutCompleted(t.Context(), payload); err != nil {
		t.Fatal(err)
	}
	var actualID, state string
	if err = db.QueryRow(`SELECT stripe_subscription_id,status FROM purser.tenant_subscriptions WHERE tenant_id=$1`, tenant.id).Scan(&actualID, &state); err != nil {
		t.Fatal(err)
	}
	if actualID != established {
		t.Errorf("unpaid checkout replaced the established subscription: %s", actualID)
	}
	event := StripeWebhookPayload{Type: "customer.subscription.updated"}
	event.Data.Object = json.RawMessage(fmt.Sprintf(`{"id":%q,"customer":%q,"status":"unpaid","metadata":{"tenant_id":%q,"checkout_reservation_id":%q}}`, pending, customer, tenant.id, intent))
	if err = tenant.svc.handleStripeSubscriptionEvent(event); err != nil {
		t.Fatal(err)
	}
	if err = db.QueryRow(`SELECT stripe_subscription_id,status FROM purser.tenant_subscriptions WHERE tenant_id=$1`, tenant.id).Scan(&actualID, &state); err != nil {
		t.Fatal(err)
	}
	if actualID != established || state != "active" {
		t.Errorf("unactivated checkout changed the established subscription: %s,%s", actualID, state)
	}
}

func TestProviderCompletionBeforeDeadlineDeliveredLate_RealPG(t *testing.T) {
	db := startProviderReservationDatabase(t)
	now := time.Now().UTC()
	tenant := newPhaseTenant(t, db, "prepaid", now, now.AddDate(0, 1, 0), 0)
	id := uuid.NewString()
	if _, err := db.Exec(`INSERT INTO purser.payment_provider_intents(id,tenant_id,provider,purpose,local_reference_id,status,currency,idempotency_key,provider_session_id,expires_at) VALUES($1::text::uuid,$2,'stripe','tenant_subscription_checkout',$3,'provider_open','EUR',$1::text,'late-paid',NOW()-INTERVAL '1 hour')`, id, tenant.id, tenant.supporterID); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(map[string]any{"id": "evt-" + id, "type": "checkout.session.completed", "created": now.Add(-2 * time.Hour).Unix(), "data": map[string]any{"object": map[string]any{"id": "late-paid", "customer": "customer", "subscription": "subscription", "payment_status": "paid", "metadata": map[string]string{"tenant_id": tenant.id, "purpose": "subscription", "reference_id": tenant.supporterID, "checkout_reservation_id": id}}}})
	var payload StripeWebhookPayload
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatal(err)
	}
	ok, msg, _ := tenant.svc.processStripeWebhookPayload(t.Context(), payload, "", raw)
	if !ok {
		t.Fatalf("on-time completion failed: %s", msg)
	}
	var tier string
	if err := db.QueryRow(`SELECT tier_id::text FROM purser.tenant_subscriptions WHERE tenant_id=$1`, tenant.id).Scan(&tier); err != nil {
		t.Fatal(err)
	}
	if tier != tenant.supporterID {
		t.Fatalf("payment completed before the deadline was discarded: tier=%s", tier)
	}
}

func TestProviderCheckoutLegacyNaturalWindow_RealPG(t *testing.T) {
	db := startProviderReservationDatabase(t)
	now := time.Now().UTC()
	tenant := newPhaseTenant(t, db, "prepaid", now, now.AddDate(0, 1, 0), 0)
	id := uuid.NewString()
	key := "shared-key-" + tenant.id
	if _, err := db.Exec(`INSERT INTO purser.payment_provider_intents(id,tenant_id,provider,purpose,local_reference_id,status,currency,idempotency_key,provider_session_id) VALUES($1,$2,'stripe','tenant_subscription_checkout',$3,'provider_open','EUR',$4,'old-session')`, id, tenant.id, tenant.supporterID, key); err != nil {
		t.Fatal(err)
	}
	pending, err := pendingProviderCheckoutForTest(t.Context(), db, tenant.id)
	if err != nil || !pending {
		t.Fatalf("recent unexpired checkout must fence: %v %v", pending, err)
	}
	reused, err := subscriptions.ReserveCheckout(t.Context(), db, tenant.id, func(tx *sql.Tx) (string, error) {
		return purserdb.New(tx).UpsertStripeTenantCheckoutIntent(t.Context(), purserdb.UpsertStripeTenantCheckoutIntentParams{TenantID: tenant.id, TierID: tenant.supporterID, Currency: "EUR", IdempotencyKey: key})
	})
	if err != nil || reused != id {
		t.Fatalf("retry duplicated an unexpired provider session: %s %v", reused, err)
	}
	if _, err = db.Exec(`UPDATE purser.payment_provider_intents SET created_at=NOW()-INTERVAL '25 hours' WHERE tenant_id=$1 AND id=$2`, tenant.id, id); err != nil {
		t.Fatal(err)
	}
	pending, err = pendingProviderCheckoutForTest(t.Context(), db, tenant.id)
	if err != nil || !pending {
		t.Fatalf("unreconciled historical checkout lost fence: %v %v", pending, err)
	}
	fixture, client := installStripeFixture(t)
	tenant.svc.stripeClient = client
	fixture.route("GET", "/v1/checkout/sessions/old-session", func(recordedProviderRequest) (int, any) {
		return 200, map[string]any{"id": "old-session", "status": "expired"}
	})
	if err := tenant.svc.expireCheckoutReservation(t.Context(), checkoutReservationExpiry{TenantID: tenant.id, IntentID: id}); err != nil {
		t.Fatal(err)
	}
	if pending, err := pendingProviderCheckoutForTest(t.Context(), db, tenant.id); err != nil || pending {
		t.Fatalf("confirmed historical expiry still fences: %v %v", pending, err)
	}
}

func TestRecoveryPreservesProviderCompletionAndOperatorCanCloseOpenCheckout_RealPG(t *testing.T) {
	for _, paid := range []bool{false, true} {
		t.Run(fmt.Sprint("completed=", paid), func(t *testing.T) {
			db := startProviderReservationDatabase(t)
			fixture, client := installStripeFixture(t)
			now := time.Now().UTC().Truncate(time.Second)
			fixture.route("GET", "/v1/subscriptions/subscription", func(recordedProviderRequest) (int, any) {
				return 200, map[string]any{"id": "subscription", "status": "active", "items": map[string]any{"data": []any{map[string]any{"current_period_start": now.Unix(), "current_period_end": now.AddDate(0, 1, 0).Unix()}}}}
			})
			tenant := newPhaseTenant(t, db, "prepaid", now, now.AddDate(0, 1, 0), 0)
			tenant.svc.stripeClient = client
			id := uuid.NewString()
			session := "cs_" + id
			deadline := now.Add(23 * time.Hour)
			if paid {
				deadline = now.Add(-time.Hour)
			}
			if _, err := db.Exec(`INSERT INTO purser.payment_provider_intents(id,tenant_id,provider,purpose,local_reference_id,status,currency,idempotency_key,provider_customer_id,provider_session_id,expires_at)VALUES($1::text::uuid,$2,'stripe','tenant_subscription_checkout',$3,'provider_open','EUR',$1::text,'customer',$4,$5)`, id, tenant.id, tenant.supporterID, session, deadline); err != nil {
				t.Fatal(err)
			}
			state := "open"
			if paid {
				state = "complete"
			}
			fixture.route("GET", "/v1/checkout/sessions/"+session, func(recordedProviderRequest) (int, any) {
				fixture.mu.Lock()
				defer fixture.mu.Unlock()
				return 200, map[string]any{"id": session, "status": state, "expires_at": deadline.Unix(), "customer": "customer", "subscription": map[bool]string{true: "subscription", false: ""}[paid], "payment_status": "paid", "metadata": map[string]string{"tenant_id": tenant.id, "purpose": "subscription", "reference_id": tenant.supporterID, "checkout_reservation_id": id}}
			})
			fixture.route("POST", "/v1/checkout/sessions/"+session+"/expire", func(recordedProviderRequest) (int, any) {
				fixture.mu.Lock()
				defer fixture.mu.Unlock()
				if paid {
					return 400, map[string]any{"error": map[string]string{"message": "paid checkout must not be expired"}}
				}
				state = "expired"
				return 200, map[string]any{"id": session, "status": state}
			})
			var err error
			if paid {
				err = tenant.svc.expireCheckoutReservation(t.Context(), checkoutReservationExpiry{TenantID: tenant.id, IntentID: id})
			} else {
				err = tenant.svc.RecoverCheckout(context.WithValue(t.Context(), ctxkeys.KeyUserID, uuid.NewString()), tenant.id, id, "abandoned tab")
			}
			if err != nil {
				t.Fatal(err)
			}
			var status string
			if err = db.QueryRow(`SELECT status FROM purser.payment_provider_intents WHERE tenant_id=$1 AND id=$2`, tenant.id, id).Scan(&status); err != nil {
				t.Fatal(err)
			}
			want := "expired"
			if paid {
				want = "succeeded"
			}
			if status != want {
				t.Fatalf("recovery status=%s, want %s", status, want)
			}
			if pending, err := pendingProviderCheckoutForTest(t.Context(), db, tenant.id); err != nil || pending {
				t.Fatalf("completed recovery still fences: %v %v", pending, err)
			}
			if len(fixture.matching("POST", "/v1/refunds")) != 0 || len(fixture.matching("DELETE", "/v1/subscriptions/subscription")) != 0 {
				t.Fatal("on-time payment was compensated")
			}
		})
	}
}

func TestStripeDelayedSettlementKeepsReservationUntilTerminal_RealPG(t *testing.T) {
	for _, mode := range []string{"direct", "recovery", "failure", "failure webhook"} {
		t.Run(mode, func(t *testing.T) {
			failed := strings.HasPrefix(mode, "failure")
			db := startProviderReservationDatabase(t)
			fixture, client := installStripeFixture(t)
			now := time.Now().UTC().Truncate(time.Second)
			tenant := newPhaseTenant(t, db, "prepaid", now, now.AddDate(0, 1, 0), 0)
			tenant.svc.stripeClient = client
			id := uuid.NewString()
			session := "cs_" + id
			deadline := now.Add(time.Hour)
			if _, err := db.Exec(`INSERT INTO purser.payment_provider_intents(id,tenant_id,provider,purpose,local_reference_id,status,currency,idempotency_key,provider_customer_id,provider_session_id,expires_at) VALUES($1::text::uuid,$2,'stripe','tenant_subscription_checkout',$3,'provider_open','EUR',$1::text,'customer',$4,$5)`, id, tenant.id, tenant.supporterID, session, deadline); err != nil {
				t.Fatal(err)
			}
			fixture.route("GET", "/v1/invoices", func(recordedProviderRequest) (int, any) {
				return 200, map[string]any{"object": "list", "data": []any{}, "has_more": false}
			})
			paymentStatus := "unpaid"
			object := func() map[string]any {
				return map[string]any{"id": session, "status": "complete", "created": now.Unix(), "expires_at": deadline.Unix(), "customer": "customer", "subscription": "subscription", "payment_status": paymentStatus, "mode": "subscription", "metadata": map[string]string{"tenant_id": tenant.id, "purpose": "subscription", "reference_id": tenant.supporterID, "checkout_reservation_id": id}}
			}
			fixture.route("GET", "/v1/checkout/sessions/"+session, func(recordedProviderRequest) (int, any) { return 200, object() })
			fixture.route("GET", "/v1/subscriptions/subscription", func(recordedProviderRequest) (int, any) {
				state := "incomplete"
				if failed {
					state = "incomplete_expired"
				} else if paymentStatus == "paid" {
					state = "active"
				}
				return 200, map[string]any{"id": "subscription", "status": state}
			})
			raw, _ := json.Marshal(object())
			if err := tenant.svc.DispatchStripeCheckoutCompleted(subscriptions.WithProviderCompletion(t.Context(), now.Add(-2*time.Hour)), raw); err != nil {
				t.Fatal(err)
			}
			deadline = now.Add(-time.Hour)
			if _, err := db.Exec(`UPDATE purser.payment_provider_intents SET expires_at=NOW()-INTERVAL '1 hour' WHERE tenant_id=$1 AND id=$2`, tenant.id, id); err != nil {
				t.Fatal(err)
			}
			if !failed {
				if pending, err := pendingProviderCheckoutForTest(t.Context(), db, tenant.id); err != nil || !pending {
					t.Fatalf("completed SEPA checkout lost its fence after checkout deadline: pending=%v err=%v", pending, err)
				}
				grantErr := database.WithRetryablePostgresTx(t.Context(), db, nil, func(tx *sql.Tx) error {
					if _, err := subscriptions.LockChange(t.Context(), tx, tenant.id, subscriptions.PreservePricing); err != nil {
						return err
					}
					return subscriptions.CheckGrantCheckout(t.Context(), tx, tenant.id)
				})
				if !errors.Is(grantErr, subscriptions.ErrPendingCheckout) {
					t.Fatalf("settling purchase allowed a grant: %v", grantErr)
				}
				_, checkoutErr := subscriptions.ReserveCheckout(t.Context(), db, tenant.id, func(tx *sql.Tx) (string, error) {
					return purserdb.New(tx).UpsertStripeTenantCheckoutIntent(t.Context(), purserdb.UpsertStripeTenantCheckoutIntentParams{TenantID: tenant.id, TierID: tenant.paygID, Currency: "EUR", IdempotencyKey: "second-purchase"})
				})
				if !errors.Is(checkoutErr, subscriptions.ErrPendingCheckout) {
					t.Fatalf("settling purchase allowed a second checkout: %v", checkoutErr)
				}
				paymentStatus = "paid"
				raw, _ = json.Marshal(object())
				if mode == "recovery" {
					if err := tenant.svc.expireCheckoutReservation(t.Context(), checkoutReservationExpiry{TenantID: tenant.id, IntentID: id}); err != nil {
						t.Fatal(err)
					}
				} else {
					event := StripeWebhookPayload{ID: "settled-" + id, Type: "checkout.session.async_payment_succeeded", Created: now.Add(4 * 24 * time.Hour).Unix()}
					event.Data.Object = raw
					body, _ := json.Marshal(event)
					if ok, message, _ := tenant.svc.processStripeWebhookPayload(t.Context(), event, "", body); !ok {
						t.Fatal(message)
					}
				}
			} else if mode == "failure webhook" {
				event := StripeWebhookPayload{ID: "failed-" + id, Type: "customer.subscription.updated", Created: now.Add(4 * 24 * time.Hour).Unix()}
				event.Data.Object = json.RawMessage(fmt.Sprintf(`{"id":"subscription","customer":"customer","status":"incomplete_expired","metadata":{"tenant_id":%q,"checkout_reservation_id":%q,"tier_id":%q}}`, tenant.id, id, tenant.supporterID))
				body, _ := json.Marshal(event)
				if ok, message, _ := tenant.svc.processStripeWebhookPayload(t.Context(), event, "", body); !ok {
					t.Fatal(message)
				}
			} else {
				if err := tenant.svc.expireCheckoutReservation(t.Context(), checkoutReservationExpiry{TenantID: tenant.id, IntentID: id}); err != nil {
					t.Fatalf("terminal failed SEPA checkout kept waiting: %v", err)
				}
			}
			var state string
			if err := db.QueryRow(`SELECT status FROM purser.payment_provider_intents WHERE tenant_id=$1 AND id=$2`, tenant.id, id).Scan(&state); err != nil {
				t.Fatal(err)
			}
			if failed {
				if state != "expired" && state != "terminal_failed" {
					t.Fatalf("failed settlement state=%s", state)
				}
			} else if state != "succeeded" {
				t.Fatalf("on-time completion settled late but state=%s", state)
			}
			if pending, err := pendingProviderCheckoutForTest(t.Context(), db, tenant.id); err != nil || pending {
				t.Fatalf("terminal checkout still fences: %v %v", pending, err)
			}
			if len(fixture.matching("POST", "/v1/refunds")) != 0 {
				t.Fatal("valid purchase was refunded")
			}
		})
	}
}

func TestHistoricalCompletedStripeCheckoutAndUnboundRecovery_RealPG(t *testing.T) {
	for _, historical := range []bool{false, true} {
		t.Run(fmt.Sprint("historical=", historical), func(t *testing.T) {
			db := startProviderReservationDatabase(t)
			fixture, client := installStripeFixture(t)
			now := time.Now().UTC().Truncate(time.Second)
			fixture.route("GET", "/v1/subscriptions/subscription", func(recordedProviderRequest) (int, any) {
				return 200, map[string]any{"id": "subscription", "status": "active", "items": map[string]any{"data": []any{map[string]any{"current_period_start": now.Unix(), "current_period_end": now.AddDate(0, 1, 0).Unix()}}}}
			})
			tenant := newPhaseTenant(t, db, "prepaid", now, now.AddDate(0, 1, 0), 0)
			tenant.svc.stripeClient = client
			id := uuid.NewString()
			session := "cs_" + id
			actor := uuid.NewString()
			ctx := context.WithValue(t.Context(), ctxkeys.KeyUserID, actor)
			if historical {
				created := now.Add(-25 * time.Hour)
				if _, err := db.Exec(`INSERT INTO purser.payment_provider_intents(id,tenant_id,provider,purpose,local_reference_id,status,currency,idempotency_key,provider_customer_id,provider_session_id,created_at) VALUES($1::text::uuid,$2,'stripe','tenant_subscription_checkout',$3,'provider_open','EUR',$1::text,'customer',$4,$5)`, id, tenant.id, tenant.supporterID, session, created); err != nil {
					t.Fatal(err)
				}
				fixture.route("GET", "/v1/checkout/sessions/"+session, func(recordedProviderRequest) (int, any) {
					return 200, map[string]any{"id": session, "status": "complete", "payment_status": "paid", "mode": "subscription", "created": created.Add(time.Minute).Unix(), "expires_at": created.Add(24*time.Hour + time.Minute).Unix(), "customer": "customer", "subscription": "subscription", "metadata": map[string]string{"tenant_id": tenant.id, "purpose": "subscription", "reference_id": tenant.supporterID, "checkout_reservation_id": id}}
				})
				if err := tenant.svc.expireCheckoutReservation(ctx, checkoutReservationExpiry{TenantID: tenant.id, IntentID: id}); err != nil {
					t.Fatal(err)
				}
				var state string
				if err := db.QueryRow(`SELECT status FROM purser.payment_provider_intents WHERE tenant_id=$1 AND id=$2`, tenant.id, id).Scan(&state); err != nil || state != "succeeded" {
					t.Fatalf("historical on-time purchase=%s err=%v", state, err)
				}
				if len(fixture.matching("POST", "/v1/refunds")) > 0 || len(fixture.matching("DELETE", "/v1/subscriptions/subscription")) > 0 {
					t.Fatal("on-time historical purchase was compensated")
				}
				return
			}
			if _, err := db.Exec(`INSERT INTO purser.payment_provider_intents(id,tenant_id,provider,purpose,local_reference_id,status,currency,idempotency_key,provider_customer_id,expires_at) VALUES($1::text::uuid,$2,'stripe','tenant_subscription_checkout',$3,'provider_call_failed','EUR',$1::text,NULL,NOW()+INTERVAL '23 hours')`, id, tenant.id, tenant.supporterID); err != nil {
				t.Fatal(err)
			}
			fixture.route("GET", "/v1/checkout/sessions", func(recordedProviderRequest) (int, any) {
				return 200, map[string]any{"object": "list", "data": []any{}, "has_more": false}
			})
			for n := 0; n < 2; n++ {
				if n == 1 {
					actor = uuid.NewString()
					ctx = context.WithValue(t.Context(), ctxkeys.KeyUserID, actor)
				}
				result, err := tenant.svc.RecoverCheckoutResult(ctx, tenant.id, id, fmt.Sprintf("abandoned attempt %d", n))
				if err != nil || result.GetOutcome() != "expired" || result.GetAuditId() == "" {
					t.Fatalf("unbound recovery result=%v err=%v", result, err)
				}
				var savedActor, reason, outcome string
				if err := db.QueryRow(`SELECT actor_id,reason,outcome FROM purser.checkout_recovery_audit WHERE tenant_id=$1 AND id=$2`, tenant.id, result.GetAuditId()).Scan(&savedActor, &reason, &outcome); err != nil || savedActor != actor || reason != fmt.Sprintf("abandoned attempt %d", n) || outcome != "expired" {
					t.Fatalf("audit actor=%s reason=%s outcome=%s err=%v", savedActor, reason, outcome, err)
				}
			}
			var audits, events int
			if err := db.QueryRow(`SELECT COUNT(*) FROM purser.checkout_recovery_audit WHERE tenant_id=$1 AND intent_id=$2`, tenant.id, id).Scan(&audits); err != nil {
				t.Fatal(err)
			}
			if err := db.QueryRow(`SELECT COUNT(*) FROM purser.domain_event_outbox WHERE tenant_id=$1 AND event_type='billing.checkout_recovery_action'`, tenant.id).Scan(&events); err != nil {
				t.Fatal(err)
			}
			if audits != 2 || events != 4 {
				t.Fatalf("operator requests overwritten or event missing: audits=%d events=%d", audits, events)
			}
			if pending, err := pendingProviderCheckoutForTest(ctx, db, tenant.id); err != nil || pending {
				t.Fatalf("confirmed recovery still blocks tenant: %v %v", pending, err)
			}
		})
	}
}
