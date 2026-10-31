//go:build schema_verify

package grpc

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"frameworks/api_billing/internal/appconfig/appconfigtest"
	"frameworks/api_billing/internal/database/purserdb"
	"frameworks/api_billing/internal/handlers"
	"frameworks/api_billing/internal/subscriptions"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/billing"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	purserpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/purser"
	"github.com/google/uuid"
	stripelib "github.com/stripe/stripe-go/v85"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// A tenant whose payment provider gave up keeps its paid tier for the grace
// period, then moves to Free with a final invoice for the usage up to the move.
// Service keeps running there, the invoice stays owed with reminders, and a
// paid tier needs it settled first.
func TestPaymentFailureMovesToFreeWithFinalInvoice_RealPG(t *testing.T) {
	appconfigtest.Set(t, "WAIVE_USAGE_CHARGES", "false")
	db := startOperatorGrantDatabase(t)
	ctx := context.Background()
	tenantID, freeID, supporterID := uuid.NewString(), uuid.NewString(), uuid.NewString()
	periodStart := time.Now().UTC().Truncate(time.Hour).Add(-20 * 24 * time.Hour)
	periodEnd := periodStart.AddDate(0, 1, 0)
	exec := func(t *testing.T, query string, args ...any) {
		t.Helper()
		if _, err := db.ExecContext(ctx, query, args...); err != nil {
			t.Fatalf("%v\n%s", err, query)
		}
	}
	exec(t, `INSERT INTO purser.billing_tiers (id, tier_name, display_name, base_price, currency, tier_level, is_default_postpaid, metering_enabled)
		VALUES ($1, 'free', 'Free', 0, 'EUR', 1, true, true), ($2, 'supporter', 'Supporter', 79.00, 'EUR', 2, false, true)`, freeID, supporterID)
	exec(t, `INSERT INTO purser.tier_pricing_rules (tier_id, meter, model, currency, included_quantity, unit_price, config)
		VALUES ($1, 'egress_gb', 'all_usage', 'EUR', 0, 0.02, '{}')`, supporterID)
	exec(t, `INSERT INTO purser.tenant_subscriptions (tenant_id, tier_id, status, billing_model, billing_email, billing_period_start, billing_period_end, presentment_currency,
			payment_method, stripe_customer_id, stripe_subscription_id, stripe_subscription_status)
		VALUES ($1, $2, 'active', 'postpaid', 'billing@example.com', $3, $4, 'EUR', 'stripe', 'cus_fallback', 'sub_fallback', 'active')`, tenantID, supporterID, periodStart, periodEnd)
	exec(t, `INSERT INTO purser.metering_sources (source_id, region, active_from, required) VALUES ('periscope-default', '', $1, TRUE)`, periodStart.AddDate(0, -1, 0))
	exec(t, `INSERT INTO purser.metering_windows (source_id, period_start, period_end, complete)
		SELECT 'periscope-default', window_start, window_start + INTERVAL '5 minutes', TRUE
		FROM generate_series($1::timestamptz, $2::timestamptz, INTERVAL '5 minutes') AS window_start`, periodStart.AddDate(0, -1, 0), periodEnd.AddDate(0, 1, 0))
	exec(t, `INSERT INTO purser.usage_records (
			tenant_id, cluster_id, usage_type, unit, dimensions, dimension_key,
			source_id, report_id, usage_value, usage_details,
			period_start, period_end, granularity, value_kind
		) VALUES ($1, '', 'egress_gb', 'gibibyte', '{}', $2, 'periscope-default', $3, 5000, '{}', $4, $5, 'minute_5', 'delta')`,
		tenantID, fmt.Sprintf("%x", sha256.Sum256([]byte("{}"))), strings.Repeat("9", 64), periodStart.Add(2*time.Hour), periodStart.Add(2*time.Hour+5*time.Minute))
	exec(t, `UPDATE purser.tenant_subscriptions SET status='past_due', stripe_subscription_status='canceled', free_fallback_reason=$2, free_fallback_at=$3 WHERE tenant_id=$1`,
		tenantID, subscriptions.FallbackPaymentFailed, time.Now().Add(time.Hour))

	var notices []handlers.FreeFallbackNotice
	if _, err := db.ExecContext(ctx, `UPDATE purser.billing_tiers SET is_default_postpaid=true WHERE tier_name='free'`); err != nil {
		t.Fatal(err)
	}
	server := &PurserServer{db: db, logger: logging.NewLogger(), stripeClient: &fallbackUnpaidStripe{}, tierReconciler: &assignTierReconciler{}, commodoreClient: &recordingCommodoreCache{},
		freeFallbackNoticeSender: func(notice handlers.FreeFallbackNotice) error {
			notices = append(notices, notice)
			return nil
		}}

	// Inside the grace period: the notice goes out once and the tier stays.
	server.sweepFreeFallbacksOnce(ctx)
	server.sweepFreeFallbacksOnce(ctx)
	if len(notices) != 1 || notices[0].Moved || notices[0].Recipient != "billing@example.com" {
		t.Fatalf("grace notices = %+v, want one grace notice", notices)
	}
	var tierName string
	if err := db.QueryRowContext(ctx, `SELECT tier.tier_name FROM purser.tenant_subscriptions s JOIN purser.billing_tiers tier ON tier.id = s.tier_id WHERE s.tenant_id=$1`, tenantID).Scan(&tierName); err != nil || tierName != "supporter" {
		t.Fatalf("tier inside the grace period = %s, %v", tierName, err)
	}

	exec(t, `UPDATE purser.tenant_subscriptions SET free_fallback_at=NOW() - INTERVAL '1 minute' WHERE tenant_id=$1`, tenantID)
	var cutoff time.Time
	if err := db.QueryRowContext(ctx, `SELECT free_fallback_at FROM purser.tenant_subscriptions WHERE tenant_id=$1`, tenantID).Scan(&cutoff); err != nil {
		t.Fatal(err)
	}
	server.sweepFreeFallbacksOnce(ctx)
	server.sweepFreeFallbacksOnce(ctx)

	var subStatus string
	var stripeSubscription sql.NullString
	var fallbackAt sql.NullTime
	if err := db.QueryRowContext(ctx, `SELECT tier.tier_name, s.status, s.stripe_subscription_id, s.free_fallback_at
		FROM purser.tenant_subscriptions s JOIN purser.billing_tiers tier ON tier.id = s.tier_id WHERE s.tenant_id=$1`, tenantID).Scan(&tierName, &subStatus, &stripeSubscription, &fallbackAt); err != nil {
		t.Fatal(err)
	}
	if tierName != "free" || subStatus != "active" || stripeSubscription.Valid || fallbackAt.Valid {
		t.Fatalf("after the move: tier=%s status=%s stripe=%v fallback=%v", tierName, subStatus, stripeSubscription, fallbackAt)
	}
	if len(notices) != 2 || !notices[1].Moved {
		t.Fatalf("notices = %+v, want the move notice once", notices)
	}
	if admission, err := purserdb.New(db).GetTenantAdmissionStatus(ctx, purserdb.GetTenantAdmissionStatusParams{TenantID: tenantID, Currency: "EUR"}); err != nil || admission.BillingModel != "postpaid" || admission.SubscriptionStatus != "active" {
		t.Fatalf("admission on Free = %+v, %v", admission, err)
	}

	var invoiceID, invoiceStatus, metered string
	var invoices int
	if err := db.QueryRowContext(ctx, `SELECT count(*) OVER (), id::text, status, metered_amount::text FROM purser.billing_invoices WHERE tenant_id=$1 AND document_kind='invoice'`, tenantID).Scan(&invoices, &invoiceID, &invoiceStatus, &metered); err != nil {
		t.Fatalf("read the final invoice: %v", err)
	}
	if invoices != 1 || invoiceStatus != "pending" || metered != "100.00" {
		t.Fatalf("final invoices=%d status=%s usage=%s, want one pending invoice with 100.00 of usage", invoices, invoiceStatus, metered)
	}

	var invoiceEnd time.Time
	if err := db.QueryRowContext(ctx, `SELECT period_end FROM purser.billing_invoices WHERE tenant_id=$1 AND id=$2`, tenantID, invoiceID).Scan(&invoiceEnd); err != nil {
		t.Fatal(err)
	}
	if !invoiceEnd.Equal(cutoff) {
		t.Fatalf("final invoice ends at %s, want the durable fallback cutoff %s", invoiceEnd, cutoff)
	}

	// Owed on Free: a reminder reaches the tenant and paid tiers wait.
	exec(t, `UPDATE purser.billing_invoices SET status='overdue', due_date=NOW() - INTERVAL '2 days' WHERE id=$1`, invoiceID)
	if reminders, err := purserdb.New(db).StageOverdueInvoiceReminders(ctx); err != nil || reminders != 1 {
		t.Fatalf("reminders staged on Free = %d, %v", reminders, err)
	}
	if err := server.requireProviderCheckout(ctx, tenantID); status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("checkout with an unpaid invoice = %v, want FailedPrecondition", err)
	}
	if _, err := server.AdminRecordInvoicePayment(operatorAssignCtx("7a000000-0000-4000-8000-000000000003"), &purserpb.AdminRecordInvoicePaymentRequest{
		TenantId: tenantID, InvoiceId: invoiceID, Reference: "bank transfer", Reason: "paid by transfer",
	}); err != nil {
		t.Fatal(err)
	}
	if err := server.requireProviderCheckout(ctx, tenantID); err != nil {
		t.Fatalf("checkout after settling = %v", err)
	}
}

// An operator arrangement made during the grace period stays until it ends.
func TestOperatorGrantHoldsTheMoveToFree_RealPG(t *testing.T) {
	db := startOperatorGrantDatabase(t)
	ctx := context.Background()
	tenantID, freeID, supporterID := uuid.NewString(), uuid.NewString(), uuid.NewString()
	if _, err := db.ExecContext(ctx, `INSERT INTO purser.billing_tiers (id, tier_name, display_name, base_price, currency, tier_level, metering_enabled)
		VALUES ($1, 'free', 'Free', 0, 'EUR', 1, false), ($2, 'supporter', 'Supporter', 79.00, 'EUR', 2, false)`, freeID, supporterID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO purser.tenant_subscriptions (tenant_id, tier_id, status, billing_model, free_fallback_reason, free_fallback_at, free_fallback_notified_at)
		VALUES ($1, $2, 'past_due', 'postpaid', 'payment_failed', NOW() - INTERVAL '1 minute', NOW())`, tenantID, supporterID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO purser.subscription_operator_grants (subscription_id, assigned_tier_id, collection, reason, granted_by)
		SELECT id, tier_id, 'invoice', 'support arrangement', '7a000000-0000-4000-8000-000000000003' FROM purser.tenant_subscriptions WHERE tenant_id=$1`, tenantID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE purser.billing_tiers SET is_default_postpaid=true WHERE tier_name='free'`); err != nil {
		t.Fatal(err)
	}
	server := &PurserServer{db: db, logger: logging.NewLogger(), tierReconciler: &assignTierReconciler{}, commodoreClient: &recordingCommodoreCache{}}
	server.sweepFreeFallbacksOnce(ctx)
	var tierID string
	if err := db.QueryRowContext(ctx, `SELECT tier_id::text FROM purser.tenant_subscriptions WHERE tenant_id=$1`, tenantID).Scan(&tierID); err != nil || tierID != supporterID {
		t.Fatalf("tier under an operator grant = %s, %v", tierID, err)
	}
}

// A failed final invoice must not monopolize the worker's first batch.
func TestFreeFallbackFailuresDoNotStarveLaterTenants_RealPG(t *testing.T) {
	db := startOperatorGrantDatabase(t)
	ctx := t.Context()
	free, metered, unmetered := uuid.NewString(), uuid.NewString(), uuid.NewString()
	if _, err := db.ExecContext(ctx, `INSERT INTO purser.billing_tiers
		(id,tier_name,display_name,base_price,currency,metering_enabled)
		VALUES ($1,'free','Free',0,'EUR',false),($2,'metered','Metered',20,'EUR',true),($3,'unmetered','Unmetered',20,'EUR',false)`, free, metered, unmetered); err != nil {
		t.Fatal(err)
	}
	// Their required metering is missing, so all hundred oldest rows fail.
	if _, err := db.ExecContext(ctx, `INSERT INTO purser.tenant_subscriptions
		(tenant_id,tier_id,status,billing_model,presentment_currency,billing_period_start,billing_period_end,free_fallback_at,free_fallback_reason)
		SELECT gen_random_uuid(),$1,'past_due','postpaid','EUR',NOW()-INTERVAL '20 days',NOW()+INTERVAL '10 days',NOW()-INTERVAL '2 days','payment_failed'
		FROM generate_series(1,100)`, metered); err != nil {
		t.Fatal(err)
	}
	tenant := uuid.NewString()
	if _, err := db.ExecContext(ctx, `INSERT INTO purser.tenant_subscriptions
		(tenant_id,tier_id,status,billing_model,presentment_currency,billing_period_start,billing_period_end,free_fallback_at,free_fallback_reason)
		VALUES ($1,$2,'past_due','postpaid','EUR',NOW()-INTERVAL '20 days',NOW()+INTERVAL '10 days',NOW()-INTERVAL '1 day','payment_failed')`, tenant, unmetered); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE purser.billing_tiers SET is_default_postpaid=true WHERE tier_name='free'`); err != nil {
		t.Fatal(err)
	}
	server := &PurserServer{db: db, logger: logging.NewLogger(), tierReconciler: &assignTierReconciler{}, commodoreClient: &recordingCommodoreCache{}}
	server.sweepFreeFallbacksOnce(ctx)
	server.sweepFreeFallbacksOnce(ctx)
	var tier string
	if err := db.QueryRowContext(ctx, `SELECT tier_id::text FROM purser.tenant_subscriptions WHERE tenant_id=$1`, tenant).Scan(&tier); err != nil {
		t.Fatal(err)
	}
	if tier != free {
		t.Fatal("a hundred failed final invoices starved the next eligible fallback")
	}
}

// A recoverable unpaid Stripe subscription must stop billing before its local
// identity is discarded; otherwise neither a retry nor later closure owns it.
func TestFreeFallbackStopsUnpaidProviderSubscription_RealPG(t *testing.T) {
	db := startOperatorGrantDatabase(t)
	ctx := t.Context()
	free, paid, tenant := uuid.NewString(), uuid.NewString(), uuid.NewString()
	if _, err := db.ExecContext(ctx, `INSERT INTO purser.billing_tiers
		(id,tier_name,display_name,base_price,currency,metering_enabled)
		VALUES ($1,'free','Free',0,'EUR',false),($2,'supporter','Supporter',20,'EUR',false)`, free, paid); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO purser.tenant_subscriptions
		(tenant_id,tier_id,status,billing_model,presentment_currency,billing_period_start,billing_period_end,
		stripe_customer_id,stripe_subscription_id,stripe_subscription_status,free_fallback_at,free_fallback_reason)
		VALUES ($1,$2,'past_due','postpaid','EUR',NOW()-INTERVAL '20 days',NOW()+INTERVAL '10 days',
		'customer','unpaid-subscription','unpaid',NOW()-INTERVAL '1 hour','payment_failed')`, tenant, paid); err != nil {
		t.Fatal(err)
	}
	provider := &fallbackUnpaidStripe{live: true}
	if _, err := db.ExecContext(ctx, `UPDATE purser.billing_tiers SET is_default_postpaid=true WHERE tier_name='free'`); err != nil {
		t.Fatal(err)
	}
	server := &PurserServer{db: db, logger: logging.NewLogger(), stripeClient: provider, tierReconciler: &assignTierReconciler{}, commodoreClient: &recordingCommodoreCache{}}
	server.sweepFreeFallbacksOnce(ctx)
	var tier string
	if err := db.QueryRowContext(ctx, `SELECT tier_id::text FROM purser.tenant_subscriptions WHERE tenant_id=$1`, tenant).Scan(&tier); err != nil {
		t.Fatal(err)
	}
	if tier != free {
		t.Fatal("fallback did not move the eligible tenant to Free")
	}
	if provider.live {
		t.Fatal("Free fallback discarded a provider subscription that can still collect payments")
	}
}

type fallbackUnpaidStripe struct {
	stripeBillingClient
	live bool
}

func (f *fallbackUnpaidStripe) GetSubscription(context.Context, string) (*stripelib.Subscription, error) {
	return &stripelib.Subscription{ID: "unpaid-subscription", Status: stripelib.SubscriptionStatusUnpaid}, nil
}

func (f *fallbackUnpaidStripe) CancelEstablishedSubscription(context.Context, string) error {
	f.live = false
	return nil
}

func (f *fallbackUnpaidStripe) EndSubscriptionForFallback(ctx context.Context, request billing.SubscriptionEndRequest) (billing.SubscriptionEndResult, error) {
	return billing.SubscriptionEndResult{}, f.CancelEstablishedSubscription(ctx, request.SubscriptionID)
}

func TestFreeFallbackKeepsMissedMonthsSeparate_RealPG(t *testing.T) {
	appconfigtest.Set(t, "WAIVE_USAGE_CHARGES", "false")
	db := startOperatorGrantDatabase(t)
	ctx := t.Context()
	now := time.Now().UTC()
	month := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
	start, cutoff := month.AddDate(0, -2, 0), month.Add(15*24*time.Hour)
	if cutoff.After(now) {
		month = month.AddDate(0, -1, 0)
		start = month.AddDate(0, -2, 0)
		cutoff = month.Add(15 * 24 * time.Hour)
	}
	tenant, free, paid := uuid.NewString(), uuid.NewString(), uuid.NewString()
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := db.ExecContext(ctx, query, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec(`INSERT INTO purser.billing_tiers(id,tier_name,display_name,base_price,currency,tier_level,metering_enabled) VALUES($1,'free','Free',0,'EUR',0,true),($2,'paid','Paid',79,'EUR',1,true)`, free, paid)
	exec(`INSERT INTO purser.tier_pricing_rules(tier_id,meter,model,currency,included_quantity,unit_price,config) VALUES($1,'egress_gb','tiered_graduated','EUR',50,0.02,'{}')`, paid)
	exec(`INSERT INTO purser.tenant_subscriptions(tenant_id,tier_id,status,billing_model,billing_email,payment_method,stripe_customer_id,stripe_subscription_id,billing_period_start,billing_period_end,next_billing_date,presentment_currency,free_fallback_at,free_fallback_reason,free_fallback_notified_at) VALUES($1,$2,'past_due','postpaid','billing@example.com','stripe','cus','sub',$3,$4,$4,'EUR',$5,'payment_failed',NOW())`, tenant, paid, start, start.AddDate(0, 1, 0), cutoff)
	exec(`INSERT INTO purser.metering_sources(source_id,region,active_from,required) VALUES('fallback-monthly','',$1,true)`, start)
	exec(`INSERT INTO purser.metering_windows(source_id,period_start,period_end,complete) SELECT 'fallback-monthly',at,at+INTERVAL '5 minutes',true FROM generate_series($1::timestamptz,$2::timestamptz,INTERVAL '5 minutes') at`, start, cutoff)
	for i, quantity := range []float64{100, 150, 100} {
		at := start.AddDate(0, i, 0).Add(time.Hour)
		exec(`INSERT INTO purser.usage_records(tenant_id,cluster_id,usage_type,unit,dimensions,dimension_key,source_id,report_id,usage_value,usage_details,period_start,period_end,granularity,value_kind,created_at) VALUES($1,'','egress_gb','gibibyte','{}',$2,'fallback-monthly',$3,$4,'{}',$5,$5::timestamptz+INTERVAL '5 minutes','minute_5','delta',$5)`, tenant, fmt.Sprintf("%x", sha256.Sum256([]byte("{}"))), strings.Repeat(fmt.Sprintf("%x", i+1), 64), quantity, at)
	}
	if _, err := db.ExecContext(ctx, `UPDATE purser.billing_tiers SET is_default_postpaid=true WHERE tier_name='free'`); err != nil {
		t.Fatal(err)
	}
	server := &PurserServer{db: db, logger: logging.NewLogger(), stripeClient: &fallbackUnpaidStripe{}, tierReconciler: &assignTierReconciler{}, commodoreClient: &recordingCommodoreCache{}, freeFallbackNoticeSender: func(handlers.FreeFallbackNotice) error { return nil }}
	for range 4 {
		if err := server.moveTenantToFree(ctx, tenant); err != nil {
			t.Fatal(err)
		}
		exec(`UPDATE purser.tenant_subscriptions SET free_fallback_attempted_at=NOW()-INTERVAL '6 minutes' WHERE tenant_id=$1`, tenant)
	}
	var tier string
	if err := db.QueryRow(`SELECT tier.tier_name FROM purser.tenant_subscriptions subscription JOIN purser.billing_tiers tier ON tier.id=subscription.tier_id WHERE subscription.tenant_id=$1`, tenant).Scan(&tier); err != nil || tier != "free" {
		t.Fatalf("fallback tier=%s err=%v", tier, err)
	}
	rows, err := db.Query(`SELECT period_start,period_end,metered_amount::text,base_amount::text FROM purser.billing_invoices WHERE tenant_id=$1 AND document_kind='invoice' ORDER BY period_start`, tenant)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for i, want := range []string{"1.00", "2.00", "1.00"} {
		if !rows.Next() {
			t.Fatalf("missing monthly invoice %d; catch-up must not merge allowances", i)
		}
		var from, to time.Time
		var metered, base string
		if err = rows.Scan(&from, &to, &metered, &base); err != nil {
			t.Fatal(err)
		}
		wantStart, wantEnd := start.AddDate(0, i, 0), start.AddDate(0, i+1, 0)
		if i == 2 {
			wantEnd = cutoff
		}
		if !from.Equal(wantStart) || !to.Equal(wantEnd) || metered != want || base != "0.00" {
			t.Fatalf("invoice %d=%s..%s usage=%s base=%s want %s..%s usage=%s with no new native base fee", i, from, to, metered, base, wantStart, wantEnd, want)
		}
	}
	if rows.Next() {
		t.Fatal("catch-up duplicated an invoice")
	}
	if err = rows.Err(); err != nil {
		t.Fatal(err)
	}
}

func TestFreeFallbackLifecycle_RealYugabyte(t *testing.T) {
	contracts := []struct {
		name string
		run  func(*testing.T)
	}{
		{"TestFreeFallbackKeepsMissedMonthsSeparate", TestFreeFallbackKeepsMissedMonthsSeparate_RealPG},
		{"TestMollieCancellationKeepsReplacementSubscription", TestMollieCancellationKeepsReplacementSubscription_RealPG},
		{"TestMollieFallbackFindsFundingThroughActualSubscriptionCreation", TestMollieFallbackFindsFundingThroughActualSubscriptionCreation_RealPG},
		{"TestProviderFailureUnderGrantKeepsFallbackObligation", TestProviderFailureUnderGrantKeepsFallbackObligation_RealPG},
		{"TestFreeFallbackProviderFailureKeepsDurableFence", TestFreeFallbackProviderFailureKeepsDurableFence_RealPG},
		{"TestFreeFallbackRetainsPendingAndPaidTerm", TestFreeFallbackRetainsPendingAndPaidTerm_RealPG},
		{"TestFreeFallbackOldRetriesCannotStarveUntouchedWork", TestFreeFallbackOldRetriesCannotStarveUntouchedWork_RealPG},
		{"TestPaymentFailureMovesToFreeWithFinalInvoice", TestPaymentFailureMovesToFreeWithFinalInvoice_RealPG},
		{"TestOperatorGrantHoldsTheMoveToFree", TestOperatorGrantHoldsTheMoveToFree_RealPG},
		{"TestFreeFallbackFailuresDoNotStarveLaterTenants", TestFreeFallbackFailuresDoNotStarveLaterTenants_RealPG},
		{"TestFreeFallbackStopsUnpaidProviderSubscription", TestFreeFallbackStopsUnpaidProviderSubscription_RealPG},
	}
	if !t.Run("selection parity", func(t *testing.T) {
		selection := os.Getenv("FRAMEWORKS_FREE_FALLBACK_TESTS")
		if selection == "" {
			t.Fatal("selection parity requires the Makefile's canonical test list; run the registered Make target")
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

func TestAdminAssignTier_RealYugabyte(t *testing.T) { TestAdminAssignTier_RealPG(t) }

func (s *PurserServer) sweepFreeFallbacksOnce(ctx context.Context) {
	s.sweepFreeFallbackMovesOnce(ctx)
	s.sweepFreeFallbackNoticesOnce(ctx)
}
