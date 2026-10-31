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
	"frameworks/api_billing/internal/subscriptions"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/billing"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/database"
	dbsql "github.com/Livepeer-FrameWorks/monorepo/pkg/database/sql"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	publicv1 "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/events/public/v1"
	purserpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/purser"
	mollielib "github.com/VictorAvelar/mollie-api-go/v4/mollie"
	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type fallbackLifecycleProvider struct {
	stripeBillingClient
	call func(context.Context, billing.SubscriptionEndRequest) (billing.SubscriptionEndResult, error)
}

func (p *fallbackLifecycleProvider) EndSubscriptionForFallback(ctx context.Context, r billing.SubscriptionEndRequest) (billing.SubscriptionEndResult, error) {
	return p.call(ctx, r)
}

func seedFallbackLifecycle(t *testing.T, db *sql.DB) (tenant, paid, free string) {
	t.Helper()
	tenant, paid, free = uuid.NewString(), uuid.NewString(), uuid.NewString()
	if _, err := db.Exec(`INSERT INTO purser.billing_tiers(id,tier_name,display_name,base_price,currency,metering_enabled) VALUES($1::text::uuid,$1::text,'Paid',20,'EUR',false),($2::text::uuid,$2::text,'Free fixture',0,'EUR',false)`, paid, free); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE purser.billing_tiers SET tier_name='free',is_default_postpaid=true WHERE id=$1`, free); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO purser.tenant_subscriptions(tenant_id,tier_id,status,billing_model,payment_method,presentment_currency,billing_period_start,billing_period_end,stripe_customer_id,stripe_subscription_id,free_fallback_at,free_fallback_reason,free_fallback_notified_at) VALUES($1,$2,'past_due','postpaid','stripe','EUR',NOW()-INTERVAL '20 days',NOW()-INTERVAL '1 hour','customer','subscription',NOW()-INTERVAL '1 hour','payment_failed',NOW())`, tenant, paid); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO purser.tier_entitlements(tier_id,key,value) VALUES($1,'custom_domain_enabled','true'),($1,'custom_subdomain_enabled','true')`, paid); err != nil {
		t.Fatal(err)
	}
	return
}
func fallbackLifecycleServer(db *sql.DB, p *fallbackLifecycleProvider) *PurserServer {
	return &PurserServer{db: db, logger: logging.NewLogger(), stripeClient: p, tierReconciler: &assignTierReconciler{}, commodoreClient: &recordingCommodoreCache{}}
}

func TestFreeFallbackProviderFailureKeepsDurableFence_RealPG(t *testing.T) {
	t.Run("postdeploy validates the notice retry index contract", func(t *testing.T) {
		for _, defect := range []string{"key", "predicate"} {
			t.Run(defect, func(t *testing.T) {
				db := startOperatorGrantDatabase(t)
				validation, err := dbsql.Content.ReadFile("migrations/purser/v0.3.14/postdeploy/004_validate_billing_recovery_query_indexes.sql")
				if err != nil {
					t.Fatal(err)
				}
				if _, err = db.ExecContext(t.Context(), string(validation)); err != nil {
					var definition, predicate string
					if readErr := db.QueryRowContext(t.Context(), `SELECT pg_get_indexdef(indexrelid),pg_get_expr(indpred,indrelid) FROM pg_index WHERE indexrelid='purser.idx_subscription_fallback_notice_retry'::regclass`).Scan(&definition, &predicate); readErr != nil {
						t.Fatal(readErr)
					}
					t.Fatalf("valid notice index rejected: %v; definition=%s predicate=%s", err, definition, predicate)
				}
				definition := `CREATE INDEX idx_subscription_fallback_notice_retry ON purser.tenant_subscriptions (tenant_id,free_fallback_at) WHERE free_fallback_at IS NOT NULL AND (free_fallback_moved_at IS NOT NULL OR free_fallback_notified_at IS NULL) AND free_fallback_notice_abandoned_at IS NULL`
				if defect == "predicate" {
					definition = `CREATE INDEX idx_subscription_fallback_notice_retry ON purser.tenant_subscriptions (free_fallback_notice_attempted_at ASC NULLS FIRST,free_fallback_at,tenant_id) WHERE free_fallback_at IS NOT NULL`
				}
				if _, err = db.ExecContext(t.Context(), `DROP INDEX purser.idx_subscription_fallback_notice_retry; `+definition); err != nil {
					t.Fatal(err)
				}
				if _, err = db.ExecContext(t.Context(), string(validation)); err == nil || !strings.Contains(err.Error(), "idx_subscription_fallback_notice_retry") {
					t.Fatalf("postdeploy accepted wrong notice retry %s: %v", defect, err)
				}
			})
		}
	})
	t.Run("paid grant and explicit revoke clear ended fallback ownership", func(t *testing.T) {
		db := startOperatorGrantDatabase(t)
		tenant, paid, free := seedFallbackLifecycle(t, db)
		if _, err := db.Exec(`UPDATE purser.tenant_subscriptions SET tier_id=$2,status='active',payment_method=NULL,stripe_subscription_id=NULL,stripe_customer_id=NULL,free_fallback_moved_at=NOW(),free_fallback_provider_ended_at=NOW() WHERE tenant_id=$1`, tenant, free); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`UPDATE purser.billing_tiers SET tier_level=3 WHERE id=$1`, paid); err != nil {
			t.Fatal(err)
		}
		server := fallbackLifecycleServer(db, &fallbackLifecycleProvider{})
		ctx := operatorAssignCtx(uuid.NewString())
		if _, err := server.AdminSetBillingGrant(ctx, &purserpb.AdminSetBillingGrantRequest{TenantId: tenant, TierName: paid, WaiveUsage: true, Collection: "invoice", Reason: "paid support arrangement"}); err != nil {
			t.Fatal(err)
		}
		var ended sql.NullTime
		if err := db.QueryRow(`SELECT free_fallback_provider_ended_at FROM purser.tenant_subscriptions WHERE tenant_id=$1`, tenant).Scan(&ended); err != nil || ended.Valid {
			t.Fatalf("paid grant retained dead collector ownership: %v %v", ended, err)
		}
		if _, err := db.Exec(`UPDATE purser.tenant_subscriptions SET free_fallback_provider_ended_at=NOW() WHERE tenant_id=$1`, tenant); err != nil {
			t.Fatal(err)
		}
		if _, err := server.AdminRevokeBillingGrant(ctx, &purserpb.AdminRevokeBillingGrantRequest{TenantId: tenant, TierName: paid, Reason: "restore paid collection"}); err != nil {
			t.Fatal(err)
		}
		if err := db.QueryRow(`SELECT free_fallback_provider_ended_at FROM purser.tenant_subscriptions WHERE tenant_id=$1`, tenant).Scan(&ended); err != nil || ended.Valid {
			t.Fatalf("paid revoke retained dead collector ownership: %v %v", ended, err)
		}
		started, err := subscriptions.StartPaymentFailureGrace(ctx, db, tenant, time.Now())
		if err != nil || !started {
			t.Fatalf("next collector failure lost its grace obligation: %v %v", started, err)
		}
	})
	t.Run("undeliverable notices stop retrying and remain inspectable", func(t *testing.T) {
		db := startOperatorGrantDatabase(t)
		tenant, _, _ := seedFallbackLifecycle(t, db)
		if _, err := db.Exec(`UPDATE purser.tenant_subscriptions SET free_fallback_notified_at=NULL,billing_email='broken@example.test' WHERE tenant_id=$1`, tenant); err != nil {
			t.Fatal(err)
		}
		calls := 0
		server := fallbackLifecycleServer(db, &fallbackLifecycleProvider{})
		server.freeFallbackNoticeSender = func(handlers.FreeFallbackNotice) error { calls++; return errors.New("recipient rejected") }
		for attempt := 1; attempt <= 5; attempt++ {
			if err := server.deliverFreeFallbackNotice(t.Context(), tenant); err == nil {
				t.Fatal("mail rejection was swallowed")
			}
			counts, err := purserdb.New(db).CountRepeatedFreeFallbackFailures(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			if attempt == 2 && counts.Notices != 1 {
				t.Fatalf("repeated mail failure was not alerted: %+v", counts)
			}
			if _, err := db.Exec(`UPDATE purser.tenant_subscriptions SET free_fallback_notice_attempted_at=NOW()-INTERVAL '2 days' WHERE tenant_id=$1`, tenant); err != nil {
				t.Fatal(err)
			}
		}
		if err := server.deliverFreeFallbackNotice(t.Context(), tenant); err != nil || calls != 5 {
			t.Fatalf("terminal mail retried forever: calls=%d err=%v", calls, err)
		}
		counts, err := purserdb.New(db).CountRepeatedFreeFallbackFailures(t.Context())
		if err != nil || counts.Notices != 0 {
			t.Fatalf("terminal mail retained stuck alert: %+v %v", counts, err)
		}
		items, err := server.AdminListCheckoutRecovery(operatorAssignCtx(uuid.NewString()), &purserpb.AdminListCheckoutRecoveryRequest{TenantId: tenant})
		if err != nil || len(items.GetItems()) != 1 || items.Items[0].Status != "notice_abandoned" || !strings.Contains(items.Items[0].LastError, "recipient rejected") {
			t.Fatalf("terminal notice lost operator inventory: %+v %v", items, err)
		}
	})
	t.Run("fallback target must exist before stopping collection", func(t *testing.T) {
		db := startOperatorGrantDatabase(t)
		tenant, _, free := seedFallbackLifecycle(t, db)
		if _, err := db.ExecContext(t.Context(), `UPDATE purser.billing_tiers SET is_active=false WHERE id=$1`, free); err != nil {
			t.Fatal(err)
		}
		calls := 0
		provider := &fallbackLifecycleProvider{call: func(context.Context, billing.SubscriptionEndRequest) (billing.SubscriptionEndResult, error) {
			calls++
			return billing.SubscriptionEndResult{}, nil
		}}
		server := fallbackLifecycleServer(db, provider)
		if err := server.moveTenantToFree(t.Context(), tenant); err == nil || calls != 0 {
			t.Fatalf("invalid fallback cancelled a provider first: calls=%d err=%v", calls, err)
		}
	})
	t.Run("pricing holds clear stale move alerts", func(t *testing.T) {
		db := startOperatorGrantDatabase(t)
		tenant, _, _ := seedFallbackLifecycle(t, db)
		target, id := uuid.NewString(), uuid.NewString()
		if _, err := db.Exec(`INSERT INTO purser.billing_tiers(id,tier_name,display_name,tier_level,base_price,currency) VALUES($1::uuid,$1::text,'Replacement',3,50,'EUR')`, target); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`INSERT INTO purser.subscription_tier_upgrades(id,tenant_id,old_tier_id,tier_id,actor_id,old_period_start,old_period_end,requested_at,provider,collection_provider,customer_id,provider_subscription_id,stripe_price_id,old_base_cents,base_cents)
SELECT $1,tenant_id,tier_id,$3,'customer',billing_period_start,billing_period_end,NOW()-INTERVAL '2 hours','stripe','stripe','customer','subscription','replacement-price',2000,5000 FROM purser.tenant_subscriptions WHERE tenant_id=$2`, id, tenant, target); err != nil {
			t.Fatal(err)
		}
		if err := purserdb.New(db).EnqueueTierUpgrade(t.Context(), purserdb.EnqueueTierUpgradeParams{ID: id, TenantID: tenant}); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`UPDATE purser.tenant_subscriptions SET pending_reason='tier_upgrade',pending_tier_id=$2,pending_effective_at=NOW()-INTERVAL '2 hours',free_fallback_failures=2,free_fallback_error='temporary outage' WHERE tenant_id=$1`, tenant, target); err != nil {
			t.Fatal(err)
		}
		server := fallbackLifecycleServer(db, &fallbackLifecycleProvider{call: func(context.Context, billing.SubscriptionEndRequest) (billing.SubscriptionEndResult, error) {
			t.Fatal("held upgrade stopped its provider")
			return billing.SubscriptionEndResult{}, nil
		}})
		if err := server.moveTenantToFree(t.Context(), tenant); !errors.Is(err, errFreeFallbackHeld) && !errors.Is(err, subscriptions.ErrPendingTierUpgrade) {
			t.Fatal(err)
		}
		counts, err := purserdb.New(db).CountRepeatedFreeFallbackFailures(t.Context())
		if err != nil || counts.Moves != 0 {
			t.Fatalf("held upgrade retained old move alert: %+v %v", counts, err)
		}
		items, err := server.AdminListCheckoutRecovery(operatorAssignCtx(uuid.NewString()), &purserpb.AdminListCheckoutRecoveryRequest{TenantId: tenant})
		if err != nil || len(items.GetItems()) != 2 || items.Items[0].Provider != "free_fallback" || items.Items[1].Provider != "tier_upgrade" {
			t.Fatalf("fallback not inspectable: %+v %v", items, err)
		}
	})

	db := startOperatorGrantDatabase(t)
	tenant, paid, free := seedFallbackLifecycle(t, db)
	calls := 0
	provider := &fallbackLifecycleProvider{call: func(ctx context.Context, r billing.SubscriptionEndRequest) (billing.SubscriptionEndResult, error) {
		calls++
		if r.SubscriptionID != "subscription" || r.CustomerID != "customer" || !r.AllowRecovery {
			t.Fatalf("wrong native ownership: %+v", r)
		}
		// Provider I/O must run after the staging transaction releases its row lock.
		probe, cancel := context.WithTimeout(ctx, time.Second)
		defer cancel()
		if err := database.WithRetryablePostgresTx(probe, db, nil, func(tx *sql.Tx) error {
			locked, err := subscriptions.LockChange(probe, tx, tenant, subscriptions.PreservePricing)
			if err != nil {
				return err
			}
			if !locked.Current.FreeFallbackProviderStopping {
				return errors.New("no committed fallback fence")
			}
			if err = subscriptions.CheckGrantCheckout(probe, tx, tenant); !errors.Is(err, subscriptions.ErrPendingFreeFallback) {
				t.Fatalf("grant passed fallback fence: %v", err)
			}
			_, err = subscriptions.LockChange(probe, tx, tenant, subscriptions.ChangePricing)
			if !errors.Is(err, subscriptions.ErrPendingFreeFallback) {
				t.Fatalf("pricing passed fallback fence: %v", err)
			}
			return nil
		}); err != nil {
			t.Fatalf("provider call held subscription lock: %v", err)
		}
		if calls == 1 {
			return billing.SubscriptionEndResult{}, errors.New("provider unavailable")
		}
		return billing.SubscriptionEndResult{}, nil
	}}
	server := fallbackLifecycleServer(db, provider)
	if err := server.moveTenantToFree(t.Context(), tenant); err == nil {
		t.Fatal("provider outage reported a move")
	}
	var tier, native string
	var stopping bool
	if err := db.QueryRow(`SELECT tier_id::text,stripe_subscription_id,free_fallback_provider_stopping FROM purser.tenant_subscriptions WHERE tenant_id=$1`, tenant).Scan(&tier, &native, &stopping); err != nil {
		t.Fatal(err)
	}
	if tier != paid || native != "subscription" || !stopping {
		t.Fatalf("outage discarded ownership: %s %s %v", tier, native, stopping)
	}
	if _, err := db.Exec(`UPDATE purser.tenant_subscriptions SET free_fallback_attempted_at=NOW()-INTERVAL '6 minutes' WHERE tenant_id=$1`, tenant); err != nil {
		t.Fatal(err)
	}
	if err := server.moveTenantToFree(t.Context(), tenant); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT tier_id::text FROM purser.tenant_subscriptions WHERE tenant_id=$1`, tenant).Scan(&tier); err != nil || tier != free || calls != 2 {
		t.Fatalf("retry tier=%s calls=%d err=%v", tier, calls, err)
	}
	// An unowned native renewal cannot resurrect the subscription after moving Free.
	result, err := subscriptions.TransitionProvider(t.Context(), db, subscriptions.ProviderChange{TenantID: tenant, Operation: subscriptions.StripeRenewal, Stripe: purserdb.ActivateTenantSubscriptionFromStripeParams{SubscriptionID: "subscription", CustomerID: "customer", TierID: paid}})
	if err != nil || result.Rows != 0 {
		t.Fatalf("old renewal reinstalled ended subscription: %+v %v", result, err)
	}
}

func TestFreeFallbackRetainsPendingAndPaidTerm_RealPG(t *testing.T) {
	for _, kind := range []string{"pending", "paid", "recovered", "closure"} {
		t.Run(kind, func(t *testing.T) {
			db := startOperatorGrantDatabase(t)
			tenant, paid, _ := seedFallbackLifecycle(t, db)
			end := time.Now().UTC().Truncate(time.Second).AddDate(0, 1, 0)
			var start time.Time
			if err := db.QueryRow(`SELECT billing_period_start FROM purser.tenant_subscriptions WHERE tenant_id=$1`, tenant).Scan(&start); err != nil {
				t.Fatal(err)
			}
			provider := &fallbackLifecycleProvider{call: func(ctx context.Context, r billing.SubscriptionEndRequest) (billing.SubscriptionEndResult, error) {
				switch kind {
				case "pending":
					return billing.SubscriptionEndResult{Pending: true}, nil
				case "paid":
					return billing.SubscriptionEndResult{PaidUntil: end}, nil
				case "recovered":
					return billing.SubscriptionEndResult{Recovered: true}, nil
				case "closure":
					// The closure consumer owns the permanent marker; a late provider result must not reopen it.
					if _, err := db.Exec(`INSERT INTO purser.tenant_billing_closures(tenant_id,closed_at,source_event_id) VALUES($1,NOW(),'fallback-race')`, tenant); err != nil {
						t.Fatal(err)
					}
					if _, err := db.Exec(`UPDATE purser.tenant_subscriptions SET status='cancelled' WHERE tenant_id=$1`, tenant); err != nil {
						t.Fatal(err)
					}
					return billing.SubscriptionEndResult{Recovered: true}, nil
				}
				return billing.SubscriptionEndResult{}, nil
			}}
			server := fallbackLifecycleServer(db, provider)
			err := server.moveTenantToFree(t.Context(), tenant)
			if kind == "closure" {
				if !errors.Is(err, errFreeFallbackHeld) {
					t.Fatalf("closure did not win: %v", err)
				}
			} else if err != nil {
				t.Fatal(err)
			}
			var tier, state, native string
			var at, wait, ended sql.NullTime
			var stopping bool
			var gotStart time.Time
			if err := db.QueryRow(`SELECT tier_id::text,status,stripe_subscription_id,free_fallback_at,free_fallback_wait_since,free_fallback_provider_ended_at,free_fallback_provider_stopping,billing_period_start FROM purser.tenant_subscriptions WHERE tenant_id=$1`, tenant).Scan(&tier, &state, &native, &at, &wait, &ended, &stopping, &gotStart); err != nil {
				t.Fatal(err)
			}
			if tier != paid || native != "subscription" || !gotStart.Equal(start) {
				t.Fatalf("paid/unbilled state lost: %s %s start=%s want=%s", tier, native, gotStart, start)
			}
			dns, readErr := purserdb.New(db).LoadEffectiveDNSEntitlements(t.Context(), tenant)
			wantDNS := kind != "closure"
			if readErr != nil || dns.CustomDomainEnabled != wantDNS || dns.CustomSubdomainEnabled != wantDNS {
				t.Fatalf("paid DNS entitlements lost during %s: %+v %v", kind, dns, readErr)
			}

			switch kind {
			case "pending":
				if !stopping || !wait.Valid || !at.Valid || ended.Valid {
					t.Fatalf("pending debit lost its fence")
				}
			case "paid":
				if err := server.requireProviderCheckout(t.Context(), tenant); status.Code(err) != codes.FailedPrecondition {
					t.Fatalf("new checkout during paid cancellation term=%v, want FailedPrecondition", err)
				}
				if state != "pending_cancellation" || !at.Time.Equal(end) || stopping || !ended.Valid {
					t.Fatalf("paid term state=%s until=%s stopping=%v ended=%v", state, at.Time, stopping, ended)
				}
				result, err := subscriptions.TransitionProvider(t.Context(), db, subscriptions.ProviderChange{TenantID: tenant, Operation: subscriptions.StripeStatus, StripeStatus: purserdb.UpdateTenantStripeSubscriptionStatusParams{SubscriptionID: "subscription", Status: "cancelled"}})
				if err != nil || result.Rows != 0 {
					t.Fatalf("late cancellation shortened paid term: %+v %v", result, err)
				}
			case "recovered":
				if state != "active" || at.Valid || stopping || ended.Valid {
					t.Fatalf("recovery state=%s fallback=%v stopping=%v", state, at, stopping)
				}
			case "closure":
				if state != "cancelled" {
					t.Fatal("provider callback reopened closed account")
				}
			}
		})
	}
}

func TestFreeFallbackOldRetriesCannotStarveUntouchedWork_RealPG(t *testing.T) {
	db := startOperatorGrantDatabase(t)
	tenant, paid, _ := seedFallbackLifecycle(t, db)
	// Every older retry is eligible again after a slow batch. Untouched work
	// must precede them even when its fallback date is newer.
	if _, err := db.Exec(`INSERT INTO purser.tenant_subscriptions(tenant_id,tier_id,status,billing_model,free_fallback_at,free_fallback_reason,free_fallback_attempted_at,free_fallback_notice_attempted_at) SELECT gen_random_uuid(),$1,'past_due','postpaid',NOW()-INTERVAL '2 days','payment_failed',NOW()-INTERVAL '6 minutes',NOW()-INTERVAL '6 minutes' FROM generate_series(1,100)`, paid); err != nil {
		t.Fatal(err)
	}
	q := purserdb.New(db)
	due, err := q.ListDueFreeFallbacks(t.Context(), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if len(due) != 100 || due[0] != tenant {
		t.Fatalf("untouched fallback starved behind %d old retries", len(due))
	}
	if _, err := db.Exec(`UPDATE purser.tenant_subscriptions SET free_fallback_notified_at=NULL WHERE tenant_id=$1`, tenant); err != nil {
		t.Fatal(err)
	}
	notices, err := q.ListFreeFallbackNotices(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(notices) != 100 || notices[0] != tenant {
		t.Fatalf("untouched notice starved behind %d retries", len(notices))
	}
}

type fallbackMollieCancel struct {
	mollieBillingClient
	cancel func()
}

func (p *fallbackMollieCancel) CancelSubscription(context.Context, string, string) error {
	p.cancel()
	return nil
}

func TestMollieCancellationKeepsReplacementSubscription_RealPG(t *testing.T) {
	db := startOperatorGrantDatabase(t)
	tenant, _, _ := seedFallbackLifecycle(t, db)
	if _, err := db.Exec(`INSERT INTO purser.mollie_customers(tenant_id,mollie_customer_id) VALUES($1,'customer')`, tenant); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE purser.tenant_subscriptions SET stripe_subscription_id=NULL,mollie_subscription_id='old',status='active',free_fallback_at=NULL WHERE tenant_id=$1`, tenant); err != nil {
		t.Fatal(err)
	}
	calls := 0
	provider := &fallbackMollieCancel{cancel: func() {
		calls++
		if _, err := db.Exec(`UPDATE purser.tenant_subscriptions SET mollie_subscription_id='replacement',status='active' WHERE tenant_id=$1`, tenant); err != nil {
			t.Fatal(err)
		}
	}}
	server := &PurserServer{db: db, logger: logging.NewLogger(), mollieClient: provider}
	if _, err := server.CancelMollieSubscription(t.Context(), &purserpb.CancelMollieSubscriptionRequest{TenantId: tenant, SubscriptionId: "unowned"}); err == nil || calls != 0 {
		t.Fatalf("unowned cancellation=%v calls=%d", err, calls)
	}
	if _, err := server.CancelMollieSubscription(t.Context(), &purserpb.CancelMollieSubscriptionRequest{TenantId: tenant, SubscriptionId: "old"}); err != nil {
		t.Fatal(err)
	}
	var state, native string
	var at sql.NullTime
	if err := db.QueryRow(`SELECT status,mollie_subscription_id,free_fallback_at FROM purser.tenant_subscriptions WHERE tenant_id=$1`, tenant).Scan(&state, &native, &at); err != nil {
		t.Fatal(err)
	}
	if state != "active" || native != "replacement" || at.Valid {
		t.Fatalf("old cancellation changed replacement: %s %s fallback=%v", state, native, at)
	}
}

func TestMollieFallbackFindsFundingThroughActualSubscriptionCreation_RealPG(t *testing.T) {
	assertMollieFundingRecoveryOrdersWithActivation(t)
	for _, history := range []string{"fresh", "already delivered", "rejected before provider", "compensated before activation"} {
		t.Run(history, func(t *testing.T) {
			db := startOperatorGrantDatabase(t)
			tenant, paid, _ := seedFallbackLifecycle(t, db)
			ctx := t.Context()
			q := purserdb.New(db)
			if _, err := db.Exec(`UPDATE purser.tenant_subscriptions SET stripe_subscription_id=NULL,stripe_customer_id=NULL,free_fallback_at=NULL,status='active',billing_name='Customer',billing_email='billing@example.test',billing_address='{"street":"Main 1","city":"Amsterdam","postal_code":"1000AA","country":"NL"}' WHERE tenant_id=$1`, tenant); err != nil {
				t.Fatal(err)
			}
			if err := q.UpsertMollieCustomer(ctx, purserdb.UpsertMollieCustomerParams{TenantID: tenant, MollieCustomerID: "customer"}); err != nil {
				t.Fatal(err)
			}
			server := &PurserServer{db: db, logger: logging.NewLogger(), mollieClient: &fakeMollieBillingClient{subscription: &mollielib.Subscription{ID: "native", Status: "active"}}}
			funding, err := q.UpsertMollieFirstPaymentIntent(ctx, purserdb.UpsertMollieFirstPaymentIntentParams{TenantID: tenant, TierID: paid, Currency: "EUR", AmountCents: 2000, IdempotencyKey: "initial:" + tenant})
			if err != nil {
				t.Fatal(err)
			}
			if err = q.LinkProviderIntentCustomer(ctx, purserdb.LinkProviderIntentCustomerParams{TenantID: tenant, IntentID: funding, CustomerID: sql.NullString{String: "customer", Valid: true}}); err != nil {
				t.Fatal(err)
			}
			if err = server.recordMollieFirstPaymentOpen(ctx, tenant, funding, "initial", &publicv1.Money{AmountMinor: 2000, Currency: "EUR"}); err != nil {
				t.Fatal(err)
			}
			if err = q.ResolveMollieFirstPaymentIntent(ctx, purserdb.ResolveMollieFirstPaymentIntentParams{TenantID: tenant, ProviderPaymentID: sql.NullString{String: "initial", Valid: true}, Status: "succeeded"}); err != nil {
				t.Fatal(err)
			}
			if history != "fresh" {
				prior, err := q.UpsertMollieSubscriptionIntent(ctx, purserdb.UpsertMollieSubscriptionIntentParams{TenantID: tenant, TierID: paid, CustomerID: sql.NullString{String: "customer", Valid: true}, Currency: "EUR", AmountCents: 2000, IdempotencyKey: "prior:" + tenant})
				if err != nil {
					t.Fatal(err)
				}
				if history == "compensated before activation" {
					if err = q.SetProviderIntentSubscriptionOpen(ctx, purserdb.SetProviderIntentSubscriptionOpenParams{TenantID: tenant, IntentID: prior, SubscriptionID: sql.NullString{String: "unactivated-native", Valid: true}}); err != nil {
						t.Fatal(err)
					}
				}
				if history == "already delivered" {
					if err = q.SetProviderIntentSubscriptionOpen(ctx, purserdb.SetProviderIntentSubscriptionOpenParams{TenantID: tenant, IntentID: prior, SubscriptionID: sql.NullString{String: "old-native", Valid: true}}); err != nil {
						t.Fatal(err)
					}
					if err = q.CompleteMollieSubscriptionCheckout(ctx, purserdb.CompleteMollieSubscriptionCheckoutParams{TenantID: tenant, SubscriptionID: "old-native"}); err != nil {
						t.Fatal(err)
					}
				} else if err = q.FinishProviderCheckoutIntent(ctx, purserdb.FinishProviderCheckoutIntentParams{TenantID: tenant, IntentID: prior, Status: "terminal_failed"}); err != nil {
					t.Fatal(err)
				}
			}
			response, err := server.CreateMollieSubscription(ctx, &purserpb.CreateMollieSubscriptionRequest{TenantId: tenant, TierId: paid, MandateId: "mandate"})
			if err != nil {
				t.Fatal(err)
			}
			if response.SubscriptionId != "native" {
				t.Fatalf("native activation=%+v", response)
			}
			if _, err = subscriptions.StartMolliePaymentFailureGrace(ctx, db, tenant, "native", time.Now().Add(-15*24*time.Hour)); err != nil {
				t.Fatal(err)
			}
			row, err := q.GetSubscriptionFreeFallback(ctx, tenant)
			if err != nil {
				t.Fatal(err)
			}
			want := "initial"
			if history == "already delivered" {
				want = ""
			}
			if row.MollieInitialPaymentID != want {
				t.Fatalf("%s: first funding=%q want %q", history, row.MollieInitialPaymentID, want)
			}
		})
	}
}

func TestProviderFailureUnderGrantKeepsFallbackObligation_RealPG(t *testing.T) {
	for _, provider := range []string{"stripe", "mollie"} {
		t.Run(provider, func(t *testing.T) {
			db := startOperatorGrantDatabase(t)
			tenant, paid, free := seedFallbackLifecycle(t, db)
			ctx := t.Context()
			if _, err := db.Exec(`UPDATE purser.tenant_subscriptions SET status='active',free_fallback_at=NULL,mollie_subscription_id=CASE WHEN $2='mollie' THEN 'mollie-native' END WHERE tenant_id=$1`, tenant, provider); err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec(`INSERT INTO purser.subscription_operator_grants(subscription_id,waive_usage,collection,reason,assigned_tier_id,fallback_tier_id,granted_at,expires_at) SELECT id,true,'provider','trial',tier_id,$2,NOW()-INTERVAL '2 hours',NOW()-INTERVAL '1 hour' FROM purser.tenant_subscriptions WHERE tenant_id=$1`, tenant, paid); err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec(`UPDATE purser.tenant_subscriptions SET free_fallback_notice_failures=2,free_fallback_notice_error='SMTP unavailable' WHERE tenant_id=$1`, tenant); err != nil {
				t.Fatal(err)
			}
			at := time.Now().Add(-15 * 24 * time.Hour)
			if provider == "stripe" {
				_, err := subscriptions.TransitionProvider(subscriptions.WithProviderCompletion(ctx, at), db, subscriptions.ProviderChange{TenantID: tenant, Operation: subscriptions.StripeStatus, StripeStatus: purserdb.UpdateTenantStripeSubscriptionStatusParams{SubscriptionID: "subscription", Status: "cancelled", StripeStatus: sql.NullString{String: "unpaid", Valid: true}}})
				if err != nil {
					t.Fatal(err)
				}
			} else if started, err := subscriptions.StartMolliePaymentFailureGrace(ctx, db, tenant, "mollie-native", at); err != nil || !started {
				t.Fatalf("failure under grant was dropped: %v %v", started, err)
			}
			var fallback sql.NullTime
			if err := db.QueryRow(`SELECT free_fallback_at FROM purser.tenant_subscriptions WHERE tenant_id=$1`, tenant).Scan(&fallback); err != nil || !fallback.Valid {
				t.Fatalf("grant lost provider-end obligation: %v %v", fallback, err)
			}
			ender := &fallbackLifecycleProvider{call: func(context.Context, billing.SubscriptionEndRequest) (billing.SubscriptionEndResult, error) {
				return billing.SubscriptionEndResult{}, nil
			}}
			server := fallbackLifecycleServer(db, ender)
			if provider == "mollie" {
				server.mollieClient = &fallbackMollieEnder{end: ender}
			}
			counts, countErr := purserdb.New(db).CountRepeatedFreeFallbackFailures(ctx)
			if countErr != nil || counts.Notices != 0 {
				t.Fatalf("grant-held notices retained an alert: %+v %v", counts, countErr)
			}
			server.sweepFreeFallbacksOnce(ctx)
			var got string
			if err := db.QueryRow(`SELECT tier_id::text FROM purser.tenant_subscriptions WHERE tenant_id=$1`, tenant).Scan(&got); err != nil || got != paid {
				t.Fatalf("grant did not hold tier: %s %v", got, err)
			}
			if err := server.endOperatorGrant(ctx, tenant, "trial deadline", true); err != nil {
				t.Fatal(err)
			}
			server.sweepFreeFallbacksOnce(ctx)
			if err := db.QueryRow(`SELECT tier_id::text FROM purser.tenant_subscriptions WHERE tenant_id=$1`, tenant).Scan(&got); err != nil || got != free {
				t.Fatalf("ending grant lost fallback: %s %v", got, err)
			}
		})
	}
}

type fallbackMollieEnder struct {
	mollieBillingClient
	end *fallbackLifecycleProvider
}

func (p *fallbackMollieEnder) EndSubscriptionForFallback(ctx context.Context, r billing.SubscriptionEndRequest) (billing.SubscriptionEndResult, error) {
	return p.end.call(ctx, r)
}
