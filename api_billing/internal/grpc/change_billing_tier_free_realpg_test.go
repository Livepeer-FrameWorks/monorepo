//go:build schema_verify

package grpc

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
	"time"

	"frameworks/api_billing/internal/database/purserdb"
	"frameworks/api_billing/internal/handlers"
	billingmollie "frameworks/api_billing/internal/mollie"
	billingstripe "frameworks/api_billing/internal/stripe"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/billing"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	purserpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/purser"
	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type freeDowngradeStripe struct {
	stripeBillingClient
	end func(billing.SubscriptionEndRequest) (billing.SubscriptionEndResult, error)
}

func (p *freeDowngradeStripe) EndSubscriptionForFallback(_ context.Context, r billing.SubscriptionEndRequest) (billing.SubscriptionEndResult, error) {
	return p.end(r)
}

type freeDowngradeMollie struct {
	mollieBillingClient
	end func(billing.SubscriptionEndRequest) (billing.SubscriptionEndResult, error)
}

func (p *freeDowngradeMollie) EndSubscriptionForFallback(_ context.Context, r billing.SubscriptionEndRequest) (billing.SubscriptionEndResult, error) {
	return p.end(r)
}

type freeDowngrade struct {
	db                      *sql.DB
	server                  *PurserServer
	service                 *handlers.Service
	tenant, paid, low, free string
	calls                   []billing.SubscriptionEndRequest
	failure                 error
}

func newFreeDowngrade(t *testing.T, provider string) *freeDowngrade {
	t.Helper()
	db := startOperatorGrantDatabase(t)
	f := &freeDowngrade{db: db, tenant: uuid.NewString(), paid: uuid.NewString(), low: uuid.NewString(), free: uuid.NewString()}
	f.exec(t, `INSERT INTO purser.billing_tiers (id,tier_name,display_name,tier_level,base_price,currency,metering_enabled,stripe_price_id_monthly,is_default_postpaid) VALUES
		($1::text::uuid,$1::text,'Paid',4,50,'EUR',false,'paid-price',false),($2::text::uuid,$2::text,'Low',3,30,'EUR',false,'low-price',false),
		($3::text::uuid,'free','Free',1,0,'EUR',false,NULL,true)`, f.paid, f.low, f.free)
	stripeID, mollieID := "subscription", ""
	if provider == "mollie" {
		stripeID, mollieID = "", "subscription"
		f.exec(t, `INSERT INTO purser.mollie_customers(tenant_id,mollie_customer_id) VALUES($1,'customer')`, f.tenant)
		f.exec(t, `INSERT INTO purser.mollie_mandates (tenant_id,mollie_customer_id,mollie_mandate_id,status,method) VALUES ($1,'customer','mandate','valid','directdebit')`, f.tenant)
	}
	f.exec(t, `INSERT INTO purser.tenant_subscriptions (tenant_id,tier_id,status,billing_model,payment_method,stripe_customer_id,stripe_subscription_id,mollie_subscription_id,
		billing_name,billing_email,billing_address,presentment_currency,billing_period_start,billing_period_end,next_billing_date)
		VALUES ($1,$2,'active','postpaid',$3,'customer',NULLIF($4,''),NULLIF($5,''),'Customer','billing@example.com',
		'{"street":"Street 1","city":"Leiden","postal_code":"2332ED","country":"NL"}','EUR',NOW()-INTERVAL '10 days',NOW()+INTERVAL '20 days',NOW()+INTERVAL '20 days')`,
		f.tenant, f.paid, provider, stripeID, mollieID)
	logger := logging.NewLogger()
	mollieClient, err := billingmollie.NewClient(billingmollie.Config{APIKey: "test_fixture", Logger: logger})
	if err != nil {
		t.Fatal(err)
	}
	f.service = handlers.NewService(db, logger, nil, nil, mollieClient, billingstripe.NewClient(billingstripe.Config{SecretKey: "sk_test_fixture", Logger: logger}), nil)
	end := func(r billing.SubscriptionEndRequest) (billing.SubscriptionEndResult, error) {
		f.calls = append(f.calls, r)
		return billing.SubscriptionEndResult{}, f.failure
	}
	f.server = &PurserServer{db: db, logger: logger, billing: f.service, tierReconciler: &assignTierReconciler{}, commodoreClient: &recordingCommodoreCache{},
		stripeClient: &freeDowngradeStripe{end: end}, mollieClient: &freeDowngradeMollie{end: end},
		freeFallbackNoticeSender: func(handlers.FreeFallbackNotice) error { return nil }}
	return f
}

func (f *freeDowngrade) exec(t *testing.T, query string, args ...any) {
	t.Helper()
	if _, err := f.db.ExecContext(t.Context(), query, args...); err != nil {
		t.Fatalf("%v\n%s", err, query)
	}
}

func (f *freeDowngrade) change(t *testing.T, tier string) (*purserpb.ChangeBillingTierResponse, error) {
	t.Helper()
	return f.server.ChangeBillingTier(t.Context(), &purserpb.ChangeBillingTierRequest{TenantId: f.tenant, TierId: tier})
}

// approachRenewal brings the effective time inside the provider-stop lead.
func (f *freeDowngrade) approachRenewal(t *testing.T) time.Time {
	t.Helper()
	effective := time.Now().UTC().Add(24 * time.Hour).Truncate(time.Second)
	f.approachRenewalAt(t, effective)
	return effective
}

func (f *freeDowngrade) approachRenewalAt(t *testing.T, effective time.Time) {
	t.Helper()
	f.exec(t, `UPDATE purser.tenant_subscriptions SET pending_effective_at=$2::timestamptz,billing_period_end=$2::timestamptz AT TIME ZONE 'UTC',next_billing_date=$2::timestamptz AT TIME ZONE 'UTC' WHERE tenant_id=$1`, f.tenant, effective)
}

func (f *freeDowngrade) step(t *testing.T) {
	t.Helper()
	if err := purserdb.New(f.db).EnqueueUnconfirmedTierDowngrades(t.Context(), int64(handlers.FreeDowngradeStopLead/time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := f.service.ReconcileProviderTier(t.Context(), f.tenant); err != nil {
		t.Fatal(err)
	}
}

type freeDowngradeState struct {
	tier, pending, status string
	native                bool
	fallbackAt            sql.NullTime
	ended                 bool
}

func (f *freeDowngrade) state(t *testing.T) freeDowngradeState {
	t.Helper()
	var s freeDowngradeState
	if err := f.db.QueryRowContext(t.Context(), `SELECT tier_id::text,COALESCE(pending_tier_id::text,''),status,
		(stripe_subscription_id IS NOT NULL OR mollie_subscription_id IS NOT NULL),free_fallback_at,free_fallback_provider_ended_at IS NOT NULL
		FROM purser.tenant_subscriptions WHERE tenant_id=$1`, f.tenant).Scan(&s.tier, &s.pending, &s.status, &s.native, &s.fallbackAt, &s.ended); err != nil {
		t.Fatal(err)
	}
	return s
}

// The customer keeps the paid term, the provider stops before the renewal, and
// the tenant moves to Free at the term's end, on either provider.
func TestFreeDowngradeKeepsPaidTermThenMoves_RealPG(t *testing.T) {
	for _, provider := range []string{"stripe", "mollie"} {
		t.Run(provider, func(t *testing.T) {
			f := newFreeDowngrade(t, provider)
			resp, err := f.change(t, f.free)
			if err != nil || resp.GetPendingTierId() != f.free {
				t.Fatalf("schedule Free on a live %s subscription: %+v %v", provider, resp, err)
			}
			f.step(t)
			f.server.sweepFreeFallbackMovesOnce(t.Context())
			if s := f.state(t); len(f.calls) != 0 || s.status != "active" || s.fallbackAt.Valid {
				t.Fatalf("far from renewal: provider stops=%d state=%+v, want a live subscription", len(f.calls), s)
			}
			effective := f.approachRenewal(t)
			f.step(t)
			f.server.sweepFreeFallbackMovesOnce(t.Context())
			s := f.state(t)
			if len(f.calls) != 1 || f.calls[0].SubscriptionID != "subscription" {
				t.Fatalf("provider stops before the renewal = %+v, want one for the subscription", f.calls)
			}
			if s.tier != f.paid || s.status != "pending_cancellation" || !s.ended || !s.fallbackAt.Valid || !s.fallbackAt.Time.Equal(effective) {
				t.Fatalf("after the provider stop: %+v, want the paid tier retained until %s", s, effective)
			}
			for _, tier := range []string{f.low, f.paid} {
				_, changeErr := f.change(t, tier)
				if status.Code(changeErr) != codes.FailedPrecondition || !strings.Contains(changeErr.Error(), "already cancelled") {
					t.Fatalf("re-point after the provider stop = %v, want a clear refusal", changeErr)
				}
			}
			f.exec(t, `UPDATE purser.tenant_subscriptions SET free_fallback_at=NOW()-INTERVAL '1 second',pending_effective_at=NOW()-INTERVAL '1 second' WHERE tenant_id=$1`, f.tenant)
			f.server.sweepFreeFallbackMovesOnce(t.Context())
			if s = f.state(t); s.tier != f.free || s.pending != "" || s.native || s.status != "active" {
				t.Fatalf("at the end of the paid term: %+v, want the tenant on Free with no provider subscription", s)
			}
		})
	}
}

// Until the provider stop starts, a re-point or a cancel keeps the subscription.
func TestFreeDowngradeRepointKeepsSubscriptionLive_RealPG(t *testing.T) {
	for _, provider := range []string{"stripe", "mollie"} {
		t.Run(provider, func(t *testing.T) {
			f := newFreeDowngrade(t, provider)
			if _, err := f.change(t, f.free); err != nil {
				t.Fatal(err)
			}
			f.approachRenewal(t)
			f.step(t)
			if s := f.state(t); !s.fallbackAt.Valid {
				t.Fatalf("the step inside the lead scheduled no provider stop: %+v", s)
			}
			if resp, err := f.change(t, f.low); err != nil || resp.GetPendingTierId() != f.low {
				t.Fatalf("re-point before the provider stop: %+v %v", resp, err)
			}
			s := f.state(t)
			if s.fallbackAt.Valid || s.status != "active" || !s.native || s.pending != f.low {
				t.Fatalf("re-pointed downgrade: %+v, want a live subscription with no move to Free", s)
			}
			if _, err := f.change(t, f.free); err != nil {
				t.Fatal(err)
			}
			f.approachRenewal(t)
			f.step(t)
			if resp, err := f.change(t, f.paid); err != nil || resp.GetMessage() != "Scheduled downgrade cancelled" {
				t.Fatalf("cancel before the provider stop: %+v %v", resp, err)
			}
			if s = f.state(t); s.fallbackAt.Valid || s.pending != "" || s.status != "active" || !s.native {
				t.Fatalf("cancelled downgrade: %+v, want a live subscription", s)
			}
			f.server.sweepFreeFallbackMovesOnce(t.Context())
			if len(f.calls) != 0 {
				t.Fatalf("a kept subscription was stopped: %+v", f.calls)
			}
		})
	}
}

// A provider outage keeps the fence and the paid tier, then the sweep retries.
func TestFreeDowngradeProviderFailureRetries_RealPG(t *testing.T) {
	for _, provider := range []string{"stripe", "mollie"} {
		t.Run(provider, func(t *testing.T) {
			f := newFreeDowngrade(t, provider)
			if _, err := f.change(t, f.free); err != nil {
				t.Fatal(err)
			}
			effective := f.approachRenewal(t)
			f.step(t)
			f.failure = errors.New("provider outage")
			f.server.sweepFreeFallbackMovesOnce(t.Context())
			if s := f.state(t); s.tier != f.paid || s.ended || len(f.calls) != 1 {
				t.Fatalf("after a failed stop: calls=%d %+v", len(f.calls), s)
			}
			if _, err := f.change(t, f.low); status.Code(err) != codes.FailedPrecondition {
				t.Fatalf("re-point during an unsettled stop = %v, want FailedPrecondition", err)
			}
			f.failure = nil
			f.exec(t, `UPDATE purser.tenant_subscriptions SET free_fallback_attempted_at=NULL WHERE tenant_id=$1`, f.tenant)
			f.server.sweepFreeFallbackMovesOnce(t.Context())
			s := f.state(t)
			if len(f.calls) != 2 || !s.ended || s.status != "pending_cancellation" || !s.fallbackAt.Time.Equal(effective) {
				t.Fatalf("retry: calls=%d %+v, want the stop confirmed and the term retained until %s", len(f.calls), s, effective)
			}
		})
	}
}

// A staged Free downgrade cannot turn an unpaid term into a retained one, and an
// existing fallback keeps its date, reason and recovery check. Reason and status
// each withhold the staged effective time on their own.
func TestFreeDowngradeLeavesAPaymentFailureFallback_RealPG(t *testing.T) {
	for _, tc := range []struct {
		name, status, reason string
		recovery             bool
	}{
		{"past due payment failure", "past_due", "payment_failed", true},
		{"active payment failure", "active", "payment_failed", true},
		{"past due cancellation", "past_due", "cancelled", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFreeDowngrade(t, "stripe")
			grace := time.Now().UTC().Add(10 * 24 * time.Hour).Truncate(time.Second)
			if _, err := f.change(t, f.free); err != nil {
				t.Fatal(err)
			}
			f.exec(t, `UPDATE purser.tenant_subscriptions SET status=$2,free_fallback_at=$3,free_fallback_reason=$4,free_fallback_notified_at=NOW() WHERE tenant_id=$1`, f.tenant, tc.status, grace, tc.reason)
			f.approachRenewal(t)
			if err := f.service.ReconcileProviderTier(t.Context(), f.tenant); err != nil {
				t.Fatal(err)
			}
			var reason string
			var at time.Time
			if err := f.db.QueryRowContext(t.Context(), `SELECT free_fallback_reason,free_fallback_at FROM purser.tenant_subscriptions WHERE tenant_id=$1`, f.tenant).Scan(&reason, &at); err != nil {
				t.Fatal(err)
			}
			if reason != tc.reason || !at.Equal(grace) {
				t.Fatalf("Free step replaced the fallback: reason=%s at=%s, want %s at %s", reason, at, tc.reason, grace)
			}
			f.exec(t, `UPDATE purser.tenant_subscriptions SET free_fallback_at=NOW()-INTERVAL '1 second' WHERE tenant_id=$1`, f.tenant)
			f.server.sweepFreeFallbackMovesOnce(t.Context())
			if len(f.calls) != 1 || f.calls[0].AllowRecovery != tc.recovery {
				t.Fatalf("provider stop = %+v, want one with recovery=%t", f.calls, tc.recovery)
			}
			if s := f.state(t); s.tier != f.free || s.status != "active" {
				t.Fatalf("grace end with a staged Free downgrade: %+v, want the move now, not at the staged effective time", s)
			}
		})
	}
}

// Re-pointing a staged Free downgrade withdraws only the move it scheduled. A
// payment-failure fallback on an active subscription stays.
func TestRepointKeepsAPaymentFailureFallback_RealPG(t *testing.T) {
	f := newFreeDowngrade(t, "stripe")
	grace := time.Now().UTC().Add(10 * 24 * time.Hour).Truncate(time.Second)
	if _, err := f.change(t, f.free); err != nil {
		t.Fatal(err)
	}
	f.exec(t, `UPDATE purser.tenant_subscriptions SET free_fallback_at=$2,free_fallback_reason='payment_failed',free_fallback_notified_at=NOW() WHERE tenant_id=$1`, f.tenant, grace)
	if _, err := f.change(t, f.low); err != nil {
		t.Fatal(err)
	}
	if s := f.state(t); !s.fallbackAt.Valid || !s.fallbackAt.Time.Equal(grace) || s.pending != f.low {
		t.Fatalf("re-point to a paid tier: %+v, want the fallback kept at %s", s, grace)
	}
}

// Once a confirmed downgrade is effective the provider has renewed at its fee, so
// the customer cannot step back onto the old tier for that paid-at-lower term.
func TestConfirmedDowngradePastItsEffectiveTimeRefusesChanges_RealPG(t *testing.T) {
	for _, provider := range []string{"stripe", "mollie"} {
		t.Run(provider, func(t *testing.T) {
			f := newFreeDowngrade(t, provider)
			stage := func(effective string, confirmed bool) {
				t.Helper()
				f.exec(t, `UPDATE purser.tenant_subscriptions SET pending_tier_id=$2,pending_reason='downgrade',
					pending_provider_tier_id=CASE WHEN $3::boolean THEN $2::uuid END,pending_effective_at=NOW()+$4::interval WHERE tenant_id=$1`, f.tenant, f.low, confirmed, effective)
			}
			stage("-1 second", true)
			for _, tier := range []string{f.paid, f.free} {
				_, err := f.change(t, tier)
				if status.Code(err) != codes.FailedPrecondition || !strings.Contains(err.Error(), "being applied") {
					t.Fatalf("change to %s at effective+1s = %v, want a refusal to retry after", tier, err)
				}
			}
			if s := f.state(t); s.pending != f.low || s.tier != f.paid {
				t.Fatalf("refused change altered the downgrade: %+v", s)
			}
			// A held closing invoice keeps the refusal, and set-tier is the exit.
			if _, err := f.server.AdminAssignTier(operatorAssignCtx(uuid.NewString()), &purserpb.AdminAssignTierRequest{TenantId: f.tenant, TierName: f.low, Reason: "closing invoice held"}); err != nil {
				t.Fatalf("operator exit: %v", err)
			}
			if s := f.state(t); s.pending != "" || s.tier != f.low {
				t.Fatalf("operator exit: %+v, want the downgrade applied", s)
			}
			f.exec(t, `UPDATE purser.tenant_subscriptions SET tier_id=$2 WHERE tenant_id=$1`, f.tenant, f.paid)
			stage("1 hour", true)
			if resp, err := f.change(t, f.paid); err != nil || resp.GetMessage() != "Scheduled downgrade cancelled" {
				t.Fatalf("cancel before the effective time: %+v %v", resp, err)
			}
			stage("-1 second", false)
			if resp, err := f.change(t, f.paid); err != nil || resp.GetMessage() != "Scheduled downgrade cancelled" {
				t.Fatalf("cancel of an unconfirmed downgrade: %+v %v", resp, err)
			}
		})
	}
}
