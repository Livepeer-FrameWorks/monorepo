//go:build schema_verify

package handlers

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"frameworks/api_billing/internal/appconfig/appconfigtest"
	"frameworks/api_billing/internal/database/purserdb"
	"frameworks/api_billing/internal/subscriptions"

	"github.com/VictorAvelar/mollie-api-go/v4/mollie"
)

type fallbackState struct {
	status string
	reason sql.NullString
	at     sql.NullTime
	sent   sql.NullTime
}

func readFallback(t *testing.T, db *sql.DB, tenantID string) fallbackState {
	t.Helper()
	var state fallbackState
	if err := db.QueryRow(`SELECT status, free_fallback_reason, free_fallback_at, free_fallback_notified_at
		FROM purser.tenant_subscriptions WHERE tenant_id=$1`, tenantID).Scan(&state.status, &state.reason, &state.at, &state.sent); err != nil {
		t.Fatal(err)
	}
	return state
}

func stripeSubscriptionEvent(t *testing.T, svc *Service, status, cancellationReason string, created time.Time) {
	t.Helper()
	payload := StripeWebhookPayload{Type: "customer.subscription.updated", Created: created.Unix()}
	payload.Data.Object = json.RawMessage(fmt.Sprintf(`{"id":"sub_fallback","customer":"cus_fallback","status":%q,"cancellation_details":{"reason":%q}}`, status, cancellationReason))
	if err := svc.handleStripeSubscriptionEvent(payload); err != nil {
		t.Fatal(err)
	}
}

// An established subscription that ends is never read as having no subscription:
// a payment failure gets the grace period, a voluntary cancellation moves now
// that its paid period ended, and a checkout that never activated gets neither.
func TestStripeSubscriptionEndPlansTheMoveToFree_RealPG(t *testing.T) {
	cases := []struct {
		name         string
		before       string
		stripeStatus string
		reason       string
		wantStatus   string
		wantFallback string
		wantGrace    bool
	}{
		{"unpaid", "active", "unpaid", "", "past_due", subscriptions.FallbackPaymentFailed, true},
		{"canceled by failed payment", "past_due", "canceled", "payment_failed", "past_due", subscriptions.FallbackPaymentFailed, true},
		{"canceled without a reason", "active", "canceled", "", "past_due", subscriptions.FallbackPaymentFailed, true},
		{"canceled at period end", "pending_cancellation", "canceled", "", "pending_cancellation", subscriptions.FallbackCancelled, false},
		{"canceled on request", "active", "canceled", "cancellation_requested", "pending_cancellation", subscriptions.FallbackCancelled, false},
		{"first payment expired", "active", "incomplete_expired", "", "cancelled", "", false},
		{"never activated", "pending", "canceled", "", "cancelled", "", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			db := startProviderReservationDatabase(t)
			now := time.Now().UTC().Truncate(time.Second)
			tenant := newPhaseTenant(t, db, "postpaid", now.Add(-24*time.Hour), now.AddDate(0, 1, 0), 0)
			if _, err := db.Exec(`UPDATE purser.tenant_subscriptions SET status=$2, stripe_subscription_id='sub_fallback', stripe_customer_id='cus_fallback', payment_method='stripe' WHERE tenant_id=$1`, tenant.id, c.before); err != nil {
				t.Fatal(err)
			}

			stripeSubscriptionEvent(t, tenant.svc, c.stripeStatus, c.reason, now)

			got := readFallback(t, db, tenant.id)
			if got.status != c.wantStatus || got.reason.String != c.wantFallback {
				t.Fatalf("status=%s fallback=%q, want %s %q", got.status, got.reason.String, c.wantStatus, c.wantFallback)
			}
			if c.wantFallback == "" {
				if got.at.Valid {
					t.Fatalf("a checkout that never activated was scheduled to move at %s", got.at.Time)
				}
				return
			}
			wantAt := now
			if c.wantGrace {
				wantAt = now.Add(subscriptions.PaymentFailureGrace)
			}
			if !got.at.Time.Equal(wantAt) {
				t.Fatalf("moves at %s, want %s", got.at.Time, wantAt)
			}
			if got.sent.Valid == c.wantGrace {
				t.Fatalf("notified=%v with grace=%v: only a grace period has a notice to send", got.sent.Valid, c.wantGrace)
			}
			if _, err := purserdb.New(db).GetTenantAdmissionStatus(context.Background(), purserdb.GetTenantAdmissionStatusParams{TenantID: tenant.id, Currency: "EUR"}); err != nil {
				t.Fatalf("admission no longer sees the subscription: %v", err)
			}
		})
	}
}

func TestPaymentFailureGraceIsNotExtendedAndRecoveryCancelsIt_RealPG(t *testing.T) {
	for _, before := range []string{"closed", "provider ended"} {
		t.Run("mandate revocation persists after "+before, func(t *testing.T) {
			db := startProviderReservationDatabase(t)
			now := time.Now().UTC()
			tenant := newPhaseTenant(t, db, "postpaid", now.Add(-time.Hour), now.AddDate(0, 1, 0), 0)
			if _, err := db.Exec(`INSERT INTO purser.mollie_mandates(tenant_id,mollie_customer_id,mollie_mandate_id,status,method) VALUES($1,'customer','mandate','valid','directdebit')`, tenant.id); err != nil {
				t.Fatal(err)
			}
			if before == "closed" {
				if _, err := db.Exec(`INSERT INTO purser.tenant_billing_closures(tenant_id,closed_at,source_event_id) VALUES($1,NOW(),'closed-fixture')`, tenant.id); err != nil {
					t.Fatal(err)
				}
			} else if _, err := db.Exec(`UPDATE purser.tenant_subscriptions SET free_fallback_provider_ended_at=NOW() WHERE tenant_id=$1`, tenant.id); err != nil {
				t.Fatal(err)
			}
			started, err := subscriptions.RecordMollieMandateRevoked(t.Context(), db, tenant.id, now)
			if err != nil || started {
				t.Fatalf("ended account started grace: %v %v", started, err)
			}
			var mandate string
			if err := db.QueryRow(`SELECT status FROM purser.mollie_mandates WHERE tenant_id=$1 AND mollie_mandate_id='mandate'`, tenant.id).Scan(&mandate); err != nil || mandate != "revoked" {
				t.Fatalf("lost collector revocation: %s %v", mandate, err)
			}
		})
	}
	t.Run("mandate loss and grace commit together", func(t *testing.T) {
		db := startProviderReservationDatabase(t)
		now := time.Now().UTC()
		tenant := newPhaseTenant(t, db, "postpaid", now.Add(-time.Hour), now.AddDate(0, 1, 0), 0)
		if _, err := db.Exec(`INSERT INTO purser.mollie_mandates(tenant_id,mollie_customer_id,mollie_mandate_id,status,method) VALUES($1,'customer','mandate','valid','directdebit')`, tenant.id); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`CREATE FUNCTION purser.reject_grace_write() RETURNS TRIGGER LANGUAGE plpgsql AS $$ BEGIN IF NEW.free_fallback_at IS NOT NULL THEN RAISE EXCEPTION 'injected grace write failure'; END IF; RETURN NEW; END $$; CREATE TRIGGER reject_grace_write BEFORE UPDATE ON purser.tenant_subscriptions FOR EACH ROW EXECUTE FUNCTION purser.reject_grace_write()`); err != nil {
			t.Fatal(err)
		}
		if _, err := subscriptions.RecordMollieMandateRevoked(t.Context(), db, tenant.id, now); err == nil {
			t.Fatal("injected grace failure was swallowed")
		}
		var mandate string
		if err := db.QueryRow(`SELECT status FROM purser.mollie_mandates WHERE tenant_id=$1 AND mollie_mandate_id='mandate'`, tenant.id).Scan(&mandate); err != nil || mandate != "valid" {
			t.Fatalf("lost retry trigger: status=%s err=%v", mandate, err)
		}
		if _, err := db.Exec(`DROP TRIGGER reject_grace_write ON purser.tenant_subscriptions`); err != nil {
			t.Fatal(err)
		}
		if _, err := subscriptions.RecordMollieMandateRevoked(t.Context(), db, tenant.id, now); err != nil {
			t.Fatal(err)
		}
		if got := readFallback(t, db, tenant.id); !got.at.Valid {
			t.Fatal("retry did not establish grace")
		}
	})

	db := startProviderReservationDatabase(t)
	now := time.Now().UTC().Truncate(time.Second)
	tenant := newPhaseTenant(t, db, "postpaid", now.Add(-24*time.Hour), now.AddDate(0, 1, 0), 0)
	if _, err := db.Exec(`UPDATE purser.tenant_subscriptions SET stripe_subscription_id='sub_fallback', stripe_customer_id='cus_fallback', payment_method='stripe' WHERE tenant_id=$1`, tenant.id); err != nil {
		t.Fatal(err)
	}
	stripeSubscriptionEvent(t, tenant.svc, "unpaid", "", now)
	stripeSubscriptionEvent(t, tenant.svc, "canceled", "payment_failed", now.Add(3*24*time.Hour))
	if got := readFallback(t, db, tenant.id); !got.at.Time.Equal(now.Add(subscriptions.PaymentFailureGrace)) {
		t.Fatalf("a later failure event moved the date to %s", got.at.Time)
	}
	stripeSubscriptionEvent(t, tenant.svc, "active", "", now.Add(4*24*time.Hour))
	if got := readFallback(t, db, tenant.id); got.status != "active" || got.at.Valid {
		t.Fatalf("recovered subscription: status=%s moves at %v", got.status, got.at)
	}
}

// Mollie reports no subscription end, so a failed installment reads it. A
// subscription Mollie still collects keeps its usual dunning.
func TestFailedMollieInstallmentStartsGraceOnlyWhenMollieStopped_RealPG(t *testing.T) {
	for _, c := range []struct {
		mollieStatus string
		wantGrace    bool
	}{{"active", false}, {"canceled", true}, {"suspended", true}} {
		t.Run(c.mollieStatus, func(t *testing.T) {
			db := startProviderReservationDatabase(t)
			now := time.Now().UTC()
			tenant := newPhaseTenant(t, db, "postpaid", now.Add(-24*time.Hour), now.AddDate(0, 1, 0), 0)
			if _, err := db.Exec(`UPDATE purser.tenant_subscriptions SET mollie_subscription_id='subscription', payment_method='mollie' WHERE tenant_id=$1`, tenant.id); err != nil {
				t.Fatal(err)
			}
			fixture, client := installMollieFixture(t)
			tenant.svc.mollieClient = client
			fixture.route("GET", "/v2/customers/customer/subscriptions/subscription", func(recordedProviderRequest) (int, any) {
				return 200, map[string]any{"id": "subscription", "status": c.mollieStatus}
			})
			if err := tenant.svc.startGraceIfMollieSubscriptionEnded(context.Background(), tenant.id, &mollie.Payment{CustomerID: "customer", SubscriptionID: "subscription"}); err != nil {
				t.Fatal(err)
			}
			got := readFallback(t, db, tenant.id)
			if got.at.Valid != c.wantGrace || (c.wantGrace && got.status != "past_due") {
				t.Fatalf("status=%s moves at %v, want grace=%v", got.status, got.at, c.wantGrace)
			}
		})
	}
}

func TestGraceNeverStartsOnFreeOrPrepaidTenants_RealPG(t *testing.T) {
	db := startProviderReservationDatabase(t)
	now := time.Now().UTC()
	prepaid := newPhaseTenant(t, db, "prepaid", now.Add(-24*time.Hour), now.AddDate(0, 1, 0), 0)
	if started, err := subscriptions.StartPaymentFailureGrace(context.Background(), db, prepaid.id, now); err != nil || started {
		t.Fatalf("prepaid tenant started grace: %v, %v", started, err)
	}
	if _, err := db.Exec(`UPDATE purser.billing_tiers SET tier_name='community',base_price=0,is_default_postpaid=true WHERE id=$1`, prepaid.supporterID); err != nil {
		t.Fatal(err)
	}
	free := prepaid.id
	if _, err := db.Exec(`UPDATE purser.tenant_subscriptions SET tier_id=$2, billing_model='postpaid' WHERE tenant_id=$1`, free, prepaid.supporterID); err != nil {
		t.Fatal(err)
	}
	if started, err := subscriptions.StartPaymentFailureGrace(context.Background(), db, free, now); err != nil || started {
		t.Fatalf("Free tenant started grace: %v, %v", started, err)
	}
}

// A voluntary Mollie cancellation keeps the paid period the customer paid for.
func TestMollieCancellationMovesToFreeAtPeriodEnd_RealPG(t *testing.T) {
	db := startProviderReservationDatabase(t)
	now := time.Now().UTC().Truncate(time.Second)
	periodEnd := now.AddDate(0, 0, 10)
	tenant := newPhaseTenant(t, db, "postpaid", now.Add(-24*time.Hour), periodEnd, 0)
	if _, err := db.Exec(`UPDATE purser.tenant_subscriptions SET mollie_subscription_id='subscription', payment_method='mollie' WHERE tenant_id=$1`, tenant.id); err != nil {
		t.Fatal(err)
	}
	if err := purserdb.New(db).CancelLocalMollieSubscription(context.Background(), purserdb.CancelLocalMollieSubscriptionParams{TenantID: tenant.id, SubscriptionID: "subscription"}); err != nil {
		t.Fatal(err)
	}
	got := readFallback(t, db, tenant.id)
	if got.status != "pending_cancellation" || got.reason.String != subscriptions.FallbackCancelled || !got.at.Time.Equal(periodEnd) {
		t.Fatalf("status=%s reason=%q moves at %s, want the Free move at %s", got.status, got.reason.String, got.at.Time, periodEnd)
	}
}

func TestReplacedMollieFailureCannotStartFreeFallback_RealPG(t *testing.T) {
	db := startProviderReservationDatabase(t)
	now := time.Now().UTC()
	tenant := newPhaseTenant(t, db, "postpaid", now.Add(-24*time.Hour), now.AddDate(0, 1, 0), 0)
	if _, err := db.Exec(`UPDATE purser.tenant_subscriptions SET mollie_subscription_id='new-subscription', payment_method='mollie' WHERE tenant_id=$1`, tenant.id); err != nil {
		t.Fatal(err)
	}
	fixture, client := installMollieFixture(t)
	tenant.svc.mollieClient = client
	for _, id := range []string{"old-subscription", "new-subscription"} {
		fixture.route("GET", "/v2/customers/customer/subscriptions/"+id, func(recordedProviderRequest) (int, any) { return 200, map[string]any{"id": id, "status": "canceled"} })
	}
	if err := tenant.svc.startGraceIfMollieSubscriptionEnded(t.Context(), tenant.id, &mollie.Payment{CustomerID: "customer", SubscriptionID: "old-subscription"}); err != nil {
		t.Fatal(err)
	}
	if got := readFallback(t, db, tenant.id); got.at.Valid || got.status != "active" {
		t.Fatalf("old failed payment changed new subscription: %+v", got)
	}
	if err := tenant.svc.startGraceIfMollieSubscriptionEnded(t.Context(), tenant.id, &mollie.Payment{CustomerID: "customer", SubscriptionID: "new-subscription"}); err != nil {
		t.Fatal(err)
	}
	if got := readFallback(t, db, tenant.id); !got.at.Valid || got.status != "past_due" {
		t.Fatalf("matching failed payment did not start grace: %+v", got)
	}
}

func TestFreeFallbackRecoveryKeepsUnbilledUsageThroughRenewalReplay_RealPG(t *testing.T) {
	appconfigtest.Set(t, "WAIVE_USAGE_CHARGES", "false")
	for _, entry := range []string{"past_due", "unpaid", "sync", "mollie"} {
		t.Run(entry, func(t *testing.T) {
			db := startProviderReservationDatabase(t)
			ctx := t.Context()
			now := time.Now().UTC()
			current := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
			start := current.AddDate(0, -3, 0)
			tenant := newPhaseTenant(t, db, "postpaid", start, start.AddDate(0, 1, 0), 0)
			seedCompleteMetering(t, db, start, current)
			if _, err := db.Exec(`UPDATE purser.tenant_subscriptions SET stripe_subscription_id='sub_fallback',stripe_customer_id='cus_fallback',payment_method='stripe' WHERE tenant_id=$1`, tenant.id); err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec(`UPDATE purser.tier_pricing_rules SET model='tiered_graduated',included_quantity=50,unit_price=0.02 WHERE tier_id=$1 AND meter='egress_gb'`, tenant.supporterID); err != nil {
				t.Fatal(err)
			}
			for i, quantity := range []float64{100, 150, 200} {
				at := start.AddDate(0, i, 0).Add(time.Hour)
				recordEgressReceived(t, db, tenant.id, strings.Repeat(fmt.Sprintf("%x", i+1), 64), at, quantity, at)
			}
			event := func(status string) {
				t.Helper()
				payload := StripeWebhookPayload{Type: "customer.subscription.updated", Created: now.Unix()}
				payload.Data.Object = json.RawMessage(fmt.Sprintf(`{"id":"sub_fallback","customer":"cus_fallback","status":%q,"items":{"data":[{"current_period_start":%d,"current_period_end":%d}]}}`, status, current.Unix(), current.AddDate(0, 1, 0).Unix()))
				if err := tenant.svc.handleStripeSubscriptionEvent(payload); err != nil {
					t.Fatal(err)
				}
			}
			assertPeriod := func(want time.Time) {
				t.Helper()
				var gotStart, gotEnd, next time.Time
				if err := db.QueryRow(`SELECT billing_period_start,billing_period_end,next_billing_date FROM purser.tenant_subscriptions WHERE tenant_id=$1`, tenant.id).Scan(&gotStart, &gotEnd, &next); err != nil {
					t.Fatal(err)
				}
				if !gotStart.Equal(want) || !gotEnd.Equal(want.AddDate(0, 1, 0)) || !next.Equal(gotEnd) {
					t.Fatalf("unbilled monthly phase changed: %s..%s next=%s want %s..%s", gotStart, gotEnd, next, want, want.AddDate(0, 1, 0))
				}
			}
			replay := func() { event("active") }
			switch entry {
			case "past_due", "unpaid":
				event(entry)
			case "sync":
				if err := purserdb.New(db).UpdateTenantStripeSubscriptionSync(ctx, purserdb.UpdateTenantStripeSubscriptionSyncParams{TenantID: tenant.id, SubscriptionID: "sub_fallback", Status: sql.NullString{String: "past_due", Valid: true}, PeriodStart: sql.NullTime{Time: current, Valid: true}, PeriodEnd: sql.NullTime{Time: current.AddDate(0, 1, 0), Valid: true}}); err != nil {
					t.Fatal(err)
				}
			case "mollie":
				if _, err := db.Exec(`UPDATE purser.tenant_subscriptions SET stripe_subscription_id=NULL,mollie_subscription_id='sub_mollie',mollie_next_payment_date=$2,payment_method='mollie' WHERE tenant_id=$1`, tenant.id, start.AddDate(0, 1, 0)); err != nil {
					t.Fatal(err)
				}
				if _, err := subscriptions.StartMolliePaymentFailureGrace(ctx, db, tenant.id, "sub_mollie", now); err != nil {
					t.Fatal(err)
				}
				replay = func() {
					if _, err := purserdb.New(db).ActivateMollieTenantSubscription(ctx, purserdb.ActivateMollieTenantSubscriptionParams{TenantID: tenant.id, TierID: tenant.supporterID, SubscriptionID: sql.NullString{String: "sub_mollie", Valid: true}, PeriodStart: sql.NullTime{Time: current, Valid: true}, PeriodEnd: sql.NullTime{Time: current.AddDate(0, 1, 0), Valid: true}, NextPaymentDate: current.AddDate(0, 1, 0)}); err != nil {
						t.Fatal(err)
					}
					if err := purserdb.New(db).UpdateMollieNextPaymentDate(ctx, purserdb.UpdateMollieNextPaymentDateParams{TenantID: tenant.id, MollieSubscriptionID: sql.NullString{String: "sub_mollie", Valid: true}, NextPaymentDate: current.AddDate(0, 1, 0).Format("2006-01-02")}); err != nil {
						t.Fatal(err)
					}
				}
			}
			assertPeriod(start)
			for i, want := range []string{"1.00", "2.00", "3.00"} {
				replay()
				replay()
				phase := start.AddDate(0, i, 0)
				assertPeriod(phase)
				tenant.periodEnd = phase.AddDate(0, 1, 0)
				tenant.monthEnd(t)
				invoice := readInvoice(t, db, tenant.id, phase)
				if invoice.metered != want {
					t.Fatalf("month %d usage fee=%s want %s: each month needs its own included allowance", i, invoice.metered, want)
				}
				assertPeriod(phase.AddDate(0, 1, 0))
			}
			replay()
			assertPeriod(current)
			var count int
			if err := db.QueryRow(`SELECT count(*) FROM purser.billing_invoices WHERE tenant_id=$1 AND status NOT IN ('draft','manual_review')`, tenant.id).Scan(&count); err != nil || count != 3 {
				t.Fatalf("finalized invoices=%d want 3 err=%v", count, err)
			}

			// A subsequent purchase from Free establishes its own paid month rather
			// than inheriting the recovered native subscription's protected cursor.
			if _, err := db.Exec(`UPDATE purser.tenant_subscriptions SET tier_id=$2,billing_model='prepaid',stripe_subscription_id=NULL,mollie_subscription_id=NULL,mollie_next_payment_date=NULL WHERE tenant_id=$1`, tenant.id, tenant.paygID); err != nil {
				t.Fatal(err)
			}
			fresh := now.Truncate(time.Second)
			if _, err := subscriptions.TransitionProvider(ctx, db, subscriptions.ProviderChange{TenantID: tenant.id, Operation: subscriptions.StripeCheckout, Stripe: purserdb.ActivateTenantSubscriptionFromStripeParams{TenantID: tenant.id, TierID: tenant.supporterID, CustomerID: "cus_fallback", SubscriptionID: "sub_new_purchase", PeriodStart: sql.NullTime{Time: fresh, Valid: true}, PeriodEnd: sql.NullTime{Time: fresh.AddDate(0, 1, 0), Valid: true}}}); err != nil {
				t.Fatal(err)
			}
			assertPeriod(fresh)
		})
	}
}

func TestFreeFallbackLifecycle_RealYugabyte(t *testing.T) {
	contracts := []struct {
		name string
		run  func(*testing.T)
	}{
		{"TestFreeFallbackRecoveryKeepsUnbilledUsageThroughRenewalReplay", TestFreeFallbackRecoveryKeepsUnbilledUsageThroughRenewalReplay_RealPG},
		{"TestStripeSubscriptionEndPlansTheMoveToFree", TestStripeSubscriptionEndPlansTheMoveToFree_RealPG},
		{"TestPaymentFailureGraceIsNotExtendedAndRecoveryCancelsIt", TestPaymentFailureGraceIsNotExtendedAndRecoveryCancelsIt_RealPG},
		{"TestFailedMollieInstallmentStartsGraceOnlyWhenMollieStopped", TestFailedMollieInstallmentStartsGraceOnlyWhenMollieStopped_RealPG},
		{"TestGraceNeverStartsOnFreeOrPrepaidTenants", TestGraceNeverStartsOnFreeOrPrepaidTenants_RealPG},
		{"TestMollieCancellationMovesToFreeAtPeriodEnd", TestMollieCancellationMovesToFreeAtPeriodEnd_RealPG},
		{"TestReplacedMollieFailureCannotStartFreeFallback", TestReplacedMollieFailureCannotStartFreeFallback_RealPG},
	}
	if !t.Run("selection parity", func(t *testing.T) {
		selection := os.Getenv("FRAMEWORKS_FREE_FALLBACK_TESTS")
		if selection == "" {
			t.Skip("selection parity requires the Makefile's canonical test list")
		}
		selected := make(map[string]bool)
		for _, name := range strings.Split(selection, "|") {
			selected[name] = true
		}
		for _, contract := range contracts {
			name := contract.name + "_RealPG"
			if !selected[name] {
				t.Fatalf("Yugabyte fallback case %s is missing from the Makefile selection", name)
			}
			delete(selected, name)
		}
		if len(selected) != 0 {
			t.Fatalf("Makefile fallback cases are absent from the Yugabyte matrix: %v", selected)
		}
	}) {
		return
	}
	for _, contract := range contracts {
		t.Run(contract.name, contract.run)
	}
}
