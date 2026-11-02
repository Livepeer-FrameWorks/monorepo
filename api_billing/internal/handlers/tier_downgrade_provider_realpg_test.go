//go:build schema_verify

package handlers

import (
	"context"
	"database/sql"
	"encoding/json"
	"testing"
	"time"

	"frameworks/api_billing/internal/database/purserdb"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/database"
	internalv1 "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/events/internalv1"
	"google.golang.org/protobuf/proto"
)

type downgradeProvider struct {
	tenant  *phaseTenant
	target  string
	price   string
	tierID  string
	fail    bool
	periods [2]time.Time
	stripe  *providerFixture
	mollie  *providerFixture
}

const downgradeOldPrice = "old-price"

func newStripeDowngrade(t *testing.T, base, priceID string, cents int64) *downgradeProvider {
	t.Helper()
	db := startProviderReservationDatabase(t)
	now := time.Now().UTC().Truncate(time.Second)
	start, end := now.Add(-15*24*time.Hour), now.Add(15*24*time.Hour)
	tenant := newPhaseTenant(t, db, "postpaid", start, end, 0)
	stripeFixture, client := installStripeFixture(t)
	tenant.svc.stripeClient = client
	tenant.jobs.tierReconciler = &stubTierReconciler{}
	target := insertTierChangeTier(t, db, base)
	p := &downgradeProvider{tenant: tenant, target: target, price: downgradeOldPrice, tierID: tenant.supporterID, periods: [2]time.Time{start, end}, stripe: stripeFixture}
	mustExec(t, db, `UPDATE purser.billing_tiers SET stripe_price_id_monthly=$2 WHERE id=$1`, target, priceID)
	mustExec(t, db, `UPDATE purser.billing_tiers SET stripe_price_id_monthly=$2,base_price=79 WHERE id=$1`, tenant.supporterID, downgradeOldPrice)
	mustExec(t, db, `UPDATE purser.tenant_subscriptions SET payment_method='stripe',stripe_customer_id='customer',stripe_subscription_id='subscription',stripe_subscription_status='active',stripe_current_period_end=$2 WHERE tenant_id=$1`, tenant.id, end)
	resource := func() map[string]any {
		return map[string]any{"id": "subscription", "object": "subscription", "customer": "customer", "status": "active",
			"metadata": map[string]string{"tenant_id": tenant.id, "tier_id": p.tierID},
			"items": map[string]any{"data": []any{map[string]any{"id": "item", "quantity": 1,
				"price":                map[string]any{"id": p.price, "currency": "eur", "recurring": map[string]any{"interval": "month", "interval_count": 1}},
				"current_period_start": p.periods[0].Unix(), "current_period_end": p.periods[1].Unix()}}}}
	}
	stripeFixture.route("GET", "/v1/subscriptions/subscription", func(recordedProviderRequest) (int, any) { return 200, resource() })
	stripeFixture.route("GET", "/v1/prices/"+priceID, func(recordedProviderRequest) (int, any) {
		return 200, map[string]any{"id": priceID, "active": true, "currency": "eur", "unit_amount": cents, "recurring": map[string]any{"interval": "month", "interval_count": 1}}
	})
	stripeFixture.route("GET", "/v1/prices/"+downgradeOldPrice, func(recordedProviderRequest) (int, any) {
		return 200, map[string]any{"id": downgradeOldPrice, "active": true, "currency": "eur", "unit_amount": 7900, "recurring": map[string]any{"interval": "month", "interval_count": 1}}
	})
	stripeFixture.route("POST", "/v1/subscriptions/subscription", func(r recordedProviderRequest) (int, any) {
		if p.fail {
			return 500, map[string]any{"error": map[string]any{"type": "api_error", "message": "provider outage"}}
		}
		p.price, p.tierID = r.form.Get("items[0][price]"), r.form.Get("metadata[tier_id]")
		return 200, resource()
	})
	return p
}

func mustExec(t *testing.T, db *sql.DB, query string, args ...any) {
	t.Helper()
	if _, err := db.ExecContext(t.Context(), query, args...); err != nil {
		t.Fatalf("%v\n%s", err, query)
	}
}

func (p *downgradeProvider) stage(t *testing.T, tierID string) {
	t.Helper()
	err := database.WithRetryablePostgresTx(t.Context(), p.tenant.db, nil, func(tx *sql.Tx) error {
		if err := purserdb.New(tx).ScheduleTenantTierDowngrade(t.Context(), purserdb.ScheduleTenantTierDowngradeParams{
			TierID: tierID, EffectiveAt: sql.NullTime{Time: p.periods[1], Valid: true}, TenantID: p.tenant.id,
			PeriodStart: sql.NullTime{Time: p.periods[0], Valid: true}, PeriodEnd: sql.NullTime{Time: p.periods[1], Valid: true},
		}); err != nil {
			return err
		}
		return p.tenant.svc.RequestProviderTierSyncTx(t.Context(), tx, p.tenant.id)
	})
	if err != nil {
		t.Fatal(err)
	}
}

func (p *downgradeProvider) cancelStaged(t *testing.T) {
	t.Helper()
	err := database.WithRetryablePostgresTx(t.Context(), p.tenant.db, nil, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(t.Context(), `UPDATE purser.tenant_subscriptions SET pending_tier_id=NULL,pending_effective_at=NULL,pending_reason=NULL WHERE tenant_id=$1`, p.tenant.id); err != nil {
			return err
		}
		return p.tenant.svc.RequestProviderTierSyncTx(t.Context(), tx, p.tenant.id)
	})
	if err != nil {
		t.Fatal(err)
	}
}

func (p *downgradeProvider) reachEffectiveTime(t *testing.T) {
	t.Helper()
	mustExec(t, p.tenant.db, `UPDATE purser.tenant_subscriptions SET pending_effective_at=NOW()-INTERVAL '1 second' WHERE tenant_id=$1`, p.tenant.id)
}

func (p *downgradeProvider) replaySubscriptionUpdated(t *testing.T, metadataTier string, created time.Time) {
	t.Helper()
	object, err := json.Marshal(map[string]any{"id": "subscription", "customer": "customer", "status": "active",
		"metadata": map[string]string{"tenant_id": p.tenant.id, "tier_id": metadataTier},
		"items":    map[string]any{"data": []any{map[string]any{"id": "item", "current_period_start": p.periods[0].Unix(), "current_period_end": p.periods[1].Unix()}}}})
	if err != nil {
		t.Fatal(err)
	}
	payload := StripeWebhookPayload{ID: "evt", Type: "customer.subscription.updated", Created: created.Unix()}
	payload.Data.Object = object
	if err = p.tenant.svc.handleStripeSubscriptionEvent(payload); err != nil {
		t.Fatal(err)
	}
}

func (p *downgradeProvider) tier(t *testing.T) (tierID, pending string) {
	t.Helper()
	if err := p.tenant.db.QueryRowContext(t.Context(), `SELECT tier_id::text,COALESCE(pending_tier_id::text,'') FROM purser.tenant_subscriptions WHERE tenant_id=$1`, p.tenant.id).Scan(&tierID, &pending); err != nil {
		t.Fatal(err)
	}
	return
}

// A downgrade changes the renewal price before the renewal, keeps the paid tier
// until it is effective, and survives a replayed event carrying the old tier.
func TestTierDowngradeReachesStripeBeforeRenewal_RealPG(t *testing.T) {
	p := newStripeDowngrade(t, "20", "new-price", 2000)
	ctx := t.Context()
	p.stage(t, p.target)
	if err := p.tenant.svc.ReconcileProviderTier(ctx, p.tenant.id); err != nil {
		t.Errorf("provider step: %v", err)
	}
	updates := p.stripe.matching("POST", "/v1/subscriptions/subscription")
	if len(updates) != 1 {
		t.Errorf("Stripe subscription updates = %d, want 1 before the renewal", len(updates))
	} else {
		form := updates[0].form
		if form.Get("items[0][id]") != "item" || form.Get("items[0][price]") != "new-price" || form.Get("proration_behavior") != "none" ||
			form.Get("metadata[tier_id]") != p.target || form.Get("billing_cycle_anchor") != "" {
			t.Errorf("Stripe downgrade update = %v", form)
		}
	}
	// The worker's own change arrives while the customer still owns the paid term.
	p.replaySubscriptionUpdated(t, p.target, time.Now())
	if tier, pending := p.tier(t); tier != p.tenant.supporterID || pending != p.target {
		t.Fatalf("event with the staged tier before the effective time: tier=%s pending=%s, want the paid tier %s kept and pending %s", tier, pending, p.tenant.supporterID, p.target)
	}
	p.reachEffectiveTime(t)
	p.tenant.jobs.applyPendingDowngrade(ctx, p.tenant.id)
	if tier, pending := p.tier(t); tier != p.target || pending != "" {
		t.Fatalf("after the effective time: tier=%s pending=%s, want %s applied", tier, pending, p.target)
	}
	p.replaySubscriptionUpdated(t, p.tenant.supporterID, p.periods[0].Add(time.Hour))
	if tier, _ := p.tier(t); tier != p.target {
		t.Fatalf("customer.subscription.updated replay with old metadata restored tier %s, want %s", tier, p.target)
	}
	if err := p.tenant.svc.ReconcileProviderTier(ctx, p.tenant.id); err != nil {
		t.Fatal(err)
	}
	if got := len(p.stripe.matching("POST", "/v1/subscriptions/subscription")); got > 1 {
		t.Fatalf("a converged provider was updated again: %d updates", got)
	}
}

// Mollie charges a fixed amount, so only that future amount changes.
func TestTierDowngradeReachesMollieBeforeRenewal_RealPG(t *testing.T) {
	db := startProviderReservationDatabase(t)
	now := time.Now().UTC().Truncate(time.Second)
	start, end := now.Add(-15*24*time.Hour), now.Add(15*24*time.Hour)
	tenant := newPhaseTenant(t, db, "postpaid", start, end, 0)
	fixture, client := installMollieFixture(t)
	tenant.svc.mollieClient = client
	tenant.jobs.tierReconciler = &stubTierReconciler{}
	target := insertTierChangeTier(t, db, "20")
	mustExec(t, db, `INSERT INTO purser.mollie_customers(tenant_id,mollie_customer_id) VALUES($1,'customer')`, tenant.id)
	mustExec(t, db, `UPDATE purser.tenant_subscriptions SET payment_method='mollie',mollie_subscription_id='subscription',mollie_next_payment_date=$2 WHERE tenant_id=$1`, tenant.id, end)
	amount, metadata := "79.00", map[string]any{"tenant_id": tenant.id, "tier_id": tenant.supporterID}
	resource := func() map[string]any {
		return map[string]any{"id": "subscription", "status": "active", "metadata": metadata, "amount": map[string]string{"value": amount, "currency": "EUR"}, "nextPaymentDate": end.Format(time.DateOnly)}
	}
	path := "/v2/customers/customer/subscriptions/subscription"
	fixture.route("GET", path, func(recordedProviderRequest) (int, any) { return 200, resource() })
	fixture.route("PATCH", path, func(r recordedProviderRequest) (int, any) {
		var body struct {
			Amount   map[string]string `json:"amount"`
			Metadata map[string]any    `json:"metadata"`
		}
		if err := json.Unmarshal(r.body, &body); err != nil {
			t.Error(err)
		}
		amount, metadata = body.Amount["value"], body.Metadata
		return 200, resource()
	})
	p := &downgradeProvider{tenant: tenant, target: target, periods: [2]time.Time{start, end}, mollie: fixture}
	p.stage(t, target)
	if err := tenant.svc.ReconcileProviderTier(t.Context(), tenant.id); err != nil {
		t.Errorf("provider step: %v", err)
	}
	patches := fixture.matching("PATCH", path)
	if len(patches) != 1 || amount != "20.00" || metadata["tier_id"] != target {
		t.Errorf("Mollie patches=%d amount=%s metadata=%v, want one patch to 20.00 with tier %s", len(patches), amount, metadata, target)
	}
	if len(patches) == 1 {
		var body map[string]any
		if err := json.Unmarshal(patches[0].body, &body); err != nil || body["startDate"] != nil || body["interval"] != nil {
			t.Errorf("Mollie patch must keep the paid schedule: %v %v", body, err)
		}
	}
	if tier, pending := p.tier(t); tier != tenant.supporterID || pending != target {
		t.Fatalf("before the effective time: tier=%s pending=%s", tier, pending)
	}
	p.reachEffectiveTime(t)
	tenant.jobs.applyPendingDowngrade(t.Context(), tenant.id)
	if tier, pending := p.tier(t); tier != target || pending != "" {
		t.Fatalf("after the effective time: tier=%s pending=%s, want %s", tier, pending, target)
	}
}

// Every re-point converges on the newest desired tier, including back to the
// paid tier when the customer cancels the downgrade.
func TestTierDowngradeRepointConvergesOnLatestTier_RealPG(t *testing.T) {
	p := newStripeDowngrade(t, "20", "new-price", 2000)
	ctx := t.Context()
	p.stage(t, p.target)
	if err := p.tenant.svc.ReconcileProviderTier(ctx, p.tenant.id); err != nil {
		t.Errorf("provider step: %v", err)
	}
	other := insertTierChangeTier(t, p.tenant.db, "40")
	mustExec(t, p.tenant.db, `UPDATE purser.billing_tiers SET stripe_price_id_monthly='other-price' WHERE id=$1`, other)
	p.stripe.route("GET", "/v1/prices/other-price", func(recordedProviderRequest) (int, any) {
		return 200, map[string]any{"id": "other-price", "active": true, "currency": "eur", "unit_amount": 4000, "recurring": map[string]any{"interval": "month", "interval_count": 1}}
	})
	p.stage(t, other)
	p.reachEffectiveTime(t)
	p.tenant.jobs.applyPendingDowngrade(ctx, p.tenant.id)
	if tier, _ := p.tier(t); tier != p.tenant.supporterID {
		t.Fatalf("local tier moved to %s before the provider confirmed the re-pointed tier", tier)
	}
	if err := p.tenant.svc.ReconcileProviderTier(ctx, p.tenant.id); err != nil {
		t.Errorf("provider step: %v", err)
	}
	if p.price != "other-price" || p.tierID != other {
		t.Errorf("re-pointed provider = %s/%s, want other-price/%s", p.price, p.tierID, other)
	}
	stale := time.Now()
	p.cancelStaged(t)
	p.replaySubscriptionUpdated(t, other, stale)
	if tier, _ := p.tier(t); tier != p.tenant.supporterID {
		t.Fatalf("event from the cancelled downgrade moved the tier to %s", tier)
	}
	if err := p.tenant.svc.ReconcileProviderTier(ctx, p.tenant.id); err != nil {
		t.Errorf("provider step: %v", err)
	}
	if p.price != downgradeOldPrice || p.tierID != p.tenant.supporterID {
		t.Errorf("cancelled downgrade left the provider on %s/%s, want the paid tier restored", p.price, p.tierID)
	}
	p.replaySubscriptionUpdated(t, other, stale)
	if tier, _ := p.tier(t); tier != p.tenant.supporterID {
		t.Fatalf("delayed event from the cancelled downgrade moved the tier to %s", tier)
	}
}

// A provider outage holds the local flip, retries through the durable inbox and
// surfaces on the gauge behind PurserProviderWebhookInboxStuck.
func TestTierDowngradeProviderFailureHoldsAndRetries_RealPG(t *testing.T) {
	p := newStripeDowngrade(t, "20", "new-price", 2000)
	ctx := t.Context()
	store := &providerWebhookInboxStore{db: p.tenant.db}
	dispatcher := &providerWebhookDispatcher{service: p.tenant.svc}
	p.fail = true
	p.stage(t, p.target)
	p.reachEffectiveTime(t)
	drain := func() error {
		t.Helper()
		claims, err := store.ClaimBatch(ctx, 1, time.Minute)
		if err != nil {
			t.Fatal(err)
		}
		if len(claims) != 1 {
			t.Fatalf("durable provider steps claimed = %d, want 1", len(claims))
		}
		if _, err = dispatcher.Dispatch(ctx, claims[0].Payload); err != nil {
			if recordErr := store.RecordFailureToken(ctx, claims[0].ID, claims[0].Attempts, nil, err, 0, claims[0].LeaseToken); recordErr != nil {
				t.Fatal(recordErr)
			}
			return err
		}
		if err = store.MarkCompletedToken(ctx, claims[0].ID, claims[0].LeaseToken); err != nil {
			t.Fatal(err)
		}
		return nil
	}
	for range 2 {
		if err := drain(); err == nil {
			t.Fatal("provider outage settled the durable step")
		}
	}
	failures, err := purserdb.New(p.tenant.db).ProviderWebhookInboxRepeatedFailures(ctx)
	if err != nil || failures != 1 {
		t.Fatalf("stalled step inventory = %d, %v, want one alerting item", failures, err)
	}
	p.tenant.jobs.applyPendingDowngrade(ctx, p.tenant.id)
	if tier, pending := p.tier(t); tier != p.tenant.supporterID || pending != p.target {
		t.Fatalf("local tier flipped while the provider disagrees: tier=%s pending=%s", tier, pending)
	}
	p.fail = false
	if err := drain(); err != nil {
		t.Fatalf("retry after the outage: %v", err)
	}
	if p.price != "new-price" {
		t.Fatalf("provider price after retry = %s", p.price)
	}
	// The confirmation arrived after the local deadline, while the provider's
	// paid term runs on: the customer keeps that term.
	var effective time.Time
	if err := p.tenant.db.QueryRowContext(ctx, `SELECT pending_effective_at FROM purser.tenant_subscriptions WHERE tenant_id=$1`, p.tenant.id).Scan(&effective); err != nil || !effective.Equal(p.periods[1]) {
		t.Fatalf("late confirmation effective time = %s, %v, want the provider period end %s", effective, err, p.periods[1])
	}
	p.tenant.jobs.applyPendingDowngrade(ctx, p.tenant.id)
	if tier, _ := p.tier(t); tier != p.tenant.supporterID {
		t.Fatalf("flip inside the provider's paid term: %s", tier)
	}
	p.reachEffectiveTime(t)
	p.tenant.jobs.applyPendingDowngrade(ctx, p.tenant.id)
	if tier, pending := p.tier(t); tier != p.target || pending != "" {
		t.Fatalf("after the provider confirmed: tier=%s pending=%s", tier, pending)
	}
	var state string
	if err := p.tenant.db.QueryRowContext(context.Background(), `SELECT status FROM purser.provider_webhook_inbox WHERE provider='tier_downgrade' AND headers->>'tenant_id'=$1`, p.tenant.id).Scan(&state); err != nil || state != "processed" {
		t.Fatalf("durable step state = %s, %v", state, err)
	}
}

// A staged downgrade without a provider step gets its durable work from
// discovery, and stays held until the provider confirms it.
func TestUnconfirmedDowngradeIsDiscoveredAndHeld_RealPG(t *testing.T) {
	p := newStripeDowngrade(t, "20", "new-price", 2000)
	ctx := t.Context()
	mustExec(t, p.tenant.db, `UPDATE purser.tenant_subscriptions SET pending_tier_id=$2,pending_reason='downgrade',pending_effective_at=NOW()-INTERVAL '1 second' WHERE tenant_id=$1`, p.tenant.id, p.target)
	p.tenant.jobs.applyPendingDowngrade(ctx, p.tenant.id)
	if tier, _ := p.tier(t); tier != p.tenant.supporterID {
		t.Fatalf("legacy pending downgrade flipped without a provider step: %s", tier)
	}
	q := purserdb.New(p.tenant.db)
	for range 2 {
		if err := q.EnqueueUnconfirmedTierDowngrades(ctx, int64(FreeDowngradeStopLead/time.Second)); err != nil {
			t.Fatal(err)
		}
	}
	var rows int
	if err := p.tenant.db.QueryRowContext(ctx, `SELECT count(*) FROM purser.provider_webhook_inbox WHERE provider='tier_downgrade' AND headers->>'tenant_id'=$1 AND status='pending'`, p.tenant.id).Scan(&rows); err != nil || rows != 1 {
		t.Fatalf("discovered provider steps = %d, %v", rows, err)
	}
	if err := p.tenant.svc.ReconcileProviderTier(ctx, p.tenant.id); err != nil {
		t.Fatal(err)
	}
	p.reachEffectiveTime(t)
	p.tenant.jobs.applyPendingDowngrade(ctx, p.tenant.id)
	if tier, pending := p.tier(t); tier != p.target || pending != "" {
		t.Fatalf("confirmed legacy downgrade: tier=%s pending=%s", tier, pending)
	}
}

// A fenced Stripe event leaves the tier alone, so its domain event reports the
// tier the tenant is on rather than the metadata Purser just wrote.
func TestFencedStripeEventReportsTheLocalTier_RealPG(t *testing.T) {
	p := newStripeDowngrade(t, "20", "new-price", 2000)
	ctx := t.Context()
	p.stage(t, p.target)
	if err := p.tenant.svc.ReconcileProviderTier(ctx, p.tenant.id); err != nil {
		t.Fatal(err)
	}
	p.replaySubscriptionUpdated(t, p.target, time.Now())
	var payload []byte
	if err := p.tenant.db.QueryRowContext(ctx, `SELECT payload FROM purser.domain_event_outbox WHERE tenant_id=$1::uuid AND event_type='billing.subscription_updated' ORDER BY occurred_at DESC LIMIT 1`, p.tenant.id).Scan(&payload); err != nil {
		t.Fatal(err)
	}
	event := &internalv1.SubscriptionUpdated{}
	if err := proto.Unmarshal(payload, event); err != nil {
		t.Fatal(err)
	}
	if event.GetTierId() != p.tenant.supporterID {
		t.Fatalf("billing.subscription_updated tier = %s, want the local tier %s, not metadata %s", event.GetTierId(), p.tenant.supporterID, p.target)
	}
}
