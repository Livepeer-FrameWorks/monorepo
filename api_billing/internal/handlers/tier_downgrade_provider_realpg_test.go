//go:build schema_verify

package handlers

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"maps"
	"strings"
	"testing"
	"time"

	"frameworks/api_billing/internal/database/purserdb"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/billing"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/database"
	internalv1 "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/events/internalv1"
	"google.golang.org/protobuf/proto"
)

type downgradeProvider struct {
	tenant *phaseTenant
	target string
	price  string
	tierID string
	fail   bool
	// metadata holds keys beyond the fixture's tenant and tier.
	metadata map[string]string
	onUpdate func()
	periods  [2]time.Time
	stripe   *providerFixture
	mollie   *providerFixture
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
	p := &downgradeProvider{tenant: tenant, target: target, price: downgradeOldPrice, tierID: tenant.supporterID, periods: [2]time.Time{start, end}, stripe: stripeFixture, metadata: map[string]string{}}
	mustExec(t, db, `UPDATE purser.billing_tiers SET stripe_price_id_monthly=$2 WHERE id=$1`, target, priceID)
	mustExec(t, db, `UPDATE purser.billing_tiers SET stripe_price_id_monthly=$2,base_price=79 WHERE id=$1`, tenant.supporterID, downgradeOldPrice)
	mustExec(t, db, `UPDATE purser.tenant_subscriptions SET payment_method='stripe',stripe_customer_id='customer',stripe_subscription_id='subscription',stripe_subscription_status='active',stripe_current_period_end=$2 WHERE tenant_id=$1`, tenant.id, end)
	resource := func() map[string]any {
		metadata := map[string]string{"tenant_id": tenant.id, "tier_id": p.tierID}
		maps.Copy(metadata, p.metadata)
		return map[string]any{"id": "subscription", "object": "subscription", "customer": "customer", "status": "active",
			"metadata": metadata,
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
		if p.onUpdate != nil {
			p.onUpdate()
		}
		p.price, p.tierID = r.form.Get("items[0][price]"), r.form.Get("metadata[tier_id]")
		if p.price == "" {
			p.price = "inline-price"
		}
		for key, values := range r.form {
			name, found := strings.CutPrefix(key, "metadata[")
			if !found {
				continue
			}
			name = strings.TrimSuffix(name, "]")
			if values[0] == "" {
				delete(p.metadata, name)
			} else if name != "tier_id" && name != "tenant_id" {
				p.metadata[name] = values[0]
			}
		}
		return 200, resource()
	})
	return p
}

// useGrandfatheredPrice puts the subscriber on a price the catalog has replaced.
func (p *downgradeProvider) useGrandfatheredPrice(active bool) {
	p.price = "grandfathered-price"
	p.stripe.route("GET", "/v1/prices/grandfathered-price", func(recordedProviderRequest) (int, any) {
		return 200, map[string]any{"id": "grandfathered-price", "active": active, "currency": "eur", "unit_amount": 6500, "product": "product", "recurring": map[string]any{"interval": "month", "interval_count": 1}}
	})
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
	// An unfinished provider step fences metadata whatever the event's age.
	for _, created := range []time.Time{stale, time.Now().Add(2 * providerEventToleranceForTest)} {
		p.replaySubscriptionUpdated(t, other, created)
		if tier, _ := p.tier(t); tier != p.tenant.supporterID {
			t.Fatalf("event at %s from the cancelled downgrade moved the tier to %s", created, tier)
		}
	}
	if err := p.tenant.svc.ReconcileProviderTier(ctx, p.tenant.id); err != nil {
		t.Errorf("provider step: %v", err)
	}
	if p.price != downgradeOldPrice || p.tierID != p.tenant.supporterID {
		t.Errorf("cancelled downgrade left the provider on %s/%s, want the paid tier restored", p.price, p.tierID)
	}
	mustExec(t, p.tenant.db, `UPDATE purser.provider_webhook_inbox SET status='processed' WHERE provider='tier_downgrade' AND headers->>'tenant_id'=$1`, p.tenant.id)
	p.replaySubscriptionUpdated(t, other, stale)
	if tier, _ := p.tier(t); tier != p.tenant.supporterID {
		t.Fatalf("delayed event from the cancelled downgrade moved the tier to %s", tier)
	}
	// Past the tolerance the fence ends: this is a change made at the provider.
	p.replaySubscriptionUpdated(t, other, time.Now().Add(2*providerEventToleranceForTest))
	if tier, _ := p.tier(t); tier != other {
		t.Fatalf("event past the fence tolerance left tier %s, want the provider's %s", tier, other)
	}
}

const providerEventToleranceForTest = time.Minute

func (p *downgradeProvider) restoreKeys() int {
	count := 0
	for key := range p.metadata {
		if strings.HasPrefix(key, "billing_restore_") {
			count++
		}
	}
	return count
}

// Staging and cancelling before the provider moved leaves a subscriber on a price
// the catalog has replaced exactly where they are.
func TestCancelledStageKeepsAReplacedPrice_RealPG(t *testing.T) {
	p := newStripeDowngrade(t, "20", "new-price", 2000)
	p.useGrandfatheredPrice(true)
	p.stage(t, p.target)
	p.cancelStaged(t)
	if err := p.tenant.svc.ReconcileProviderTier(t.Context(), p.tenant.id); err != nil {
		t.Fatal(err)
	}
	if updates := p.stripe.matching("POST", "/v1/subscriptions/subscription"); len(updates) != 0 || p.price != "grandfathered-price" {
		t.Fatalf("cancelled stage wrote the provider: updates=%d price=%s, want the grandfathered price untouched", len(updates), p.price)
	}
}

// A confirmed downgrade that is cancelled returns the subscriber to what they
// paid before it, not to the current catalog price.
func TestCancelledDowngradeRestoresTheOriginalPrice_RealPG(t *testing.T) {
	for _, active := range []bool{true, false} {
		name := "active price"
		if !active {
			name = "archived price"
		}
		t.Run(name, func(t *testing.T) {
			p := newStripeDowngrade(t, "20", "new-price", 2000)
			p.useGrandfatheredPrice(active)
			ctx := t.Context()
			p.stage(t, p.target)
			if err := p.tenant.svc.ReconcileProviderTier(ctx, p.tenant.id); err != nil {
				t.Fatal(err)
			}
			p.cancelStaged(t)
			if err := p.tenant.svc.ReconcileProviderTier(ctx, p.tenant.id); err != nil {
				t.Fatal(err)
			}
			updates := p.stripe.matching("POST", "/v1/subscriptions/subscription")
			if len(updates) != 2 {
				t.Fatalf("provider updates = %d, want the move and its restore", len(updates))
			}
			form := updates[1].form
			if active && (p.price != "grandfathered-price" || form.Get("items[0][price]") != "grandfathered-price") {
				t.Fatalf("restored price = %s (%v), want the grandfathered price", p.price, form)
			}
			if !active && (form.Get("items[0][price_data][unit_amount]") != "6500" || form.Get("items[0][price_data][product]") != "product" || form.Get("items[0][price_data][recurring][interval]") != "month") {
				t.Fatalf("restore of an archived price = %v, want its amount, product and interval inline", form)
			}
			if form.Get("proration_behavior") != "none" || p.tierID != p.tenant.supporterID || p.restoreKeys() != 0 {
				t.Fatalf("restore: form=%v tier=%s restore keys=%d", form, p.tierID, p.restoreKeys())
			}
			if err := p.tenant.svc.ReconcileProviderTier(ctx, p.tenant.id); err != nil {
				t.Fatal(err)
			}
			if got := len(p.stripe.matching("POST", "/v1/subscriptions/subscription")); got != 2 {
				t.Fatalf("restored subscription was written again: %d updates", got)
			}
		})
	}
}

// A Stripe subscription whose item is not one monthly EUR unit cannot be re-priced.
func TestTierSyncRefusesANonMonthlyItem_RealPG(t *testing.T) {
	p := newStripeDowngrade(t, "20", "new-price", 2000)
	p.stripe.route("GET", "/v1/subscriptions/subscription", func(recordedProviderRequest) (int, any) {
		return 200, map[string]any{"id": "subscription", "object": "subscription", "customer": "customer", "status": "active",
			"metadata": map[string]string{"tenant_id": p.tenant.id, "tier_id": p.tenant.supporterID},
			"items": map[string]any{"data": []any{map[string]any{"id": "item", "quantity": 1,
				"price":                map[string]any{"id": downgradeOldPrice, "currency": "eur", "recurring": map[string]any{"interval": "year", "interval_count": 1}},
				"current_period_start": p.periods[0].Unix(), "current_period_end": p.periods[1].Unix()}}}}
	})
	p.stage(t, p.target)
	if err := p.tenant.svc.ReconcileProviderTier(t.Context(), p.tenant.id); !errors.Is(err, billing.ErrProviderTierRefused) {
		t.Fatalf("yearly item = %v, want a refusal", err)
	}
	if updates := p.stripe.matching("POST", "/v1/subscriptions/subscription"); len(updates) != 0 {
		t.Fatalf("yearly item was re-priced: %d updates", len(updates))
	}
}

// A disagreement with no recoverable price stays visible to the inbox alert and
// keeps metadata from moving the local tier.
func TestProviderDisagreementWithoutARestoreAlerts_RealPG(t *testing.T) {
	p := newStripeDowngrade(t, "20", "new-price", 2000)
	ctx := t.Context()
	mustExec(t, p.tenant.db, `UPDATE purser.billing_tiers SET stripe_price_id_monthly=NULL WHERE id=$1`, p.tenant.supporterID)
	p.tierID = p.target
	p.cancelStaged(t)
	err := p.tenant.svc.ReconcileProviderTier(ctx, p.tenant.id)
	if !errors.Is(err, billing.ErrProviderTierRefused) {
		t.Fatalf("disagreement with no price to restore = %v, want a refusal for the inbox to retry and alert", err)
	}
	p.replaySubscriptionUpdated(t, p.target, time.Now().Add(2*providerEventToleranceForTest))
	if tier, _ := p.tier(t); tier != p.tenant.supporterID {
		t.Fatalf("metadata moved the tier to %s during an open disagreement", tier)
	}
}

// A tier moved during the provider call never confirms the stale target: the
// step wakes and converges on the newest tier.
func TestTierMovedDuringTheProviderCallIsNotConfirmed_RealPG(t *testing.T) {
	p := newStripeDowngrade(t, "20", "new-price", 2000)
	ctx := t.Context()
	other := insertTierChangeTier(t, p.tenant.db, "40")
	mustExec(t, p.tenant.db, `UPDATE purser.billing_tiers SET stripe_price_id_monthly='other-price' WHERE id=$1`, other)
	p.stripe.route("GET", "/v1/prices/other-price", func(recordedProviderRequest) (int, any) {
		return 200, map[string]any{"id": "other-price", "active": true, "currency": "eur", "unit_amount": 4000, "recurring": map[string]any{"interval": "month", "interval_count": 1}}
	})
	p.stage(t, p.target)
	store := &providerWebhookInboxStore{db: p.tenant.db}
	claims, err := store.ClaimBatch(ctx, 1, time.Minute)
	if err != nil || len(claims) != 1 {
		t.Fatalf("claim: %v %v", claims, err)
	}
	p.onUpdate = func() {
		p.onUpdate = nil
		assertSubscriptionUnlockedDuringProviderCall(t, p.tenant.db, p.tenant.id)
		p.stage(t, other)
	}
	if _, err = (&providerWebhookDispatcher{service: p.tenant.svc}).Dispatch(ctx, claims[0].Payload); err != nil {
		t.Fatal(err)
	}
	var confirmed sql.NullString
	if err = p.tenant.db.QueryRowContext(ctx, `SELECT pending_provider_tier_id::text FROM purser.tenant_subscriptions WHERE tenant_id=$1`, p.tenant.id).Scan(&confirmed); err != nil || confirmed.Valid {
		t.Fatalf("stale target confirmed: %v %v", confirmed, err)
	}
	// The re-point woke the row the worker holds, so settling it keeps the work.
	if err = store.MarkCompletedToken(ctx, claims[0].ID, claims[0].LeaseToken); err != nil {
		t.Fatal(err)
	}
	var state string
	if err = p.tenant.db.QueryRowContext(ctx, `SELECT status FROM purser.provider_webhook_inbox WHERE provider='tier_downgrade' AND headers->>'tenant_id'=$1`, p.tenant.id).Scan(&state); err != nil || state != "pending" {
		t.Fatalf("woken step settled as %q, %v, want pending", state, err)
	}
	if err = p.tenant.svc.ReconcileProviderTier(ctx, p.tenant.id); err != nil {
		t.Fatal(err)
	}
	if p.price != "other-price" || p.tierID != other {
		t.Fatalf("after the wake: provider on %s/%s, want other-price/%s", p.price, p.tierID, other)
	}
}

// Stripe reporting the end of a subscription Purser is stopping leaves the fence,
// the paid tier and the move's schedule to the sweep.
func TestStripeDeletedEventDuringTheStopKeepsTheSchedule_RealPG(t *testing.T) {
	p := newStripeDowngrade(t, "20", "new-price", 2000)
	ctx := t.Context()
	p.stage(t, p.target)
	mustExec(t, p.tenant.db, `UPDATE purser.tenant_subscriptions SET free_fallback_at=NOW()-INTERVAL '1 minute',free_fallback_reason='cancelled',free_fallback_provider_stopping=TRUE WHERE tenant_id=$1`, p.tenant.id)
	var before time.Time
	if err := p.tenant.db.QueryRowContext(ctx, `SELECT free_fallback_at FROM purser.tenant_subscriptions WHERE tenant_id=$1`, p.tenant.id).Scan(&before); err != nil {
		t.Fatal(err)
	}
	object, err := json.Marshal(map[string]any{"id": "subscription", "customer": "customer", "status": "canceled",
		"metadata": map[string]string{"tenant_id": p.tenant.id, "tier_id": p.tenant.supporterID}})
	if err != nil {
		t.Fatal(err)
	}
	payload := StripeWebhookPayload{ID: "evt", Type: "customer.subscription.deleted", Created: time.Now().Unix()}
	payload.Data.Object = object
	if err = p.tenant.svc.handleStripeSubscriptionEvent(payload); err != nil {
		t.Fatal(err)
	}
	var stopping bool
	var after time.Time
	var tier, reason string
	if err = p.tenant.db.QueryRowContext(ctx, `SELECT free_fallback_provider_stopping,free_fallback_at,tier_id::text,free_fallback_reason FROM purser.tenant_subscriptions WHERE tenant_id=$1`, p.tenant.id).Scan(&stopping, &after, &tier, &reason); err != nil {
		t.Fatal(err)
	}
	if !stopping || !after.Equal(before) || tier != p.tenant.supporterID || reason != "cancelled" {
		t.Fatalf("deleted event during the stop: stopping=%t at=%s (was %s) tier=%s reason=%s", stopping, after, before, tier, reason)
	}
}

// Mollie keeps a suspended subscription, which still renews once its mandate
// recovers, so it takes the new amount.
func TestSuspendedMollieSubscriptionTakesTheNewAmount_RealPG(t *testing.T) {
	db := startProviderReservationDatabase(t)
	now := time.Now().UTC().Truncate(time.Second)
	end := now.Add(15 * 24 * time.Hour)
	tenant := newPhaseTenant(t, db, "postpaid", now.Add(-15*24*time.Hour), end, 0)
	fixture, client := installMollieFixture(t)
	tenant.svc.mollieClient = client
	target := insertTierChangeTier(t, db, "20")
	mustExec(t, db, `INSERT INTO purser.mollie_customers(tenant_id,mollie_customer_id) VALUES($1,'customer')`, tenant.id)
	mustExec(t, db, `UPDATE purser.tenant_subscriptions SET payment_method='mollie',mollie_subscription_id='subscription' WHERE tenant_id=$1`, tenant.id)
	path := "/v2/customers/customer/subscriptions/subscription"
	amount := "79.00"
	resource := func() map[string]any {
		return map[string]any{"id": "subscription", "status": "suspended", "metadata": map[string]any{"tenant_id": tenant.id, "tier_id": tenant.supporterID}, "amount": map[string]string{"value": amount, "currency": "EUR"}, "nextPaymentDate": end.Format(time.DateOnly)}
	}
	fixture.route("GET", path, func(recordedProviderRequest) (int, any) { return 200, resource() })
	fixture.route("PATCH", path, func(r recordedProviderRequest) (int, any) {
		var body struct {
			Amount map[string]string `json:"amount"`
		}
		if err := json.Unmarshal(r.body, &body); err != nil {
			t.Error(err)
		}
		amount = body.Amount["value"]
		return 200, resource()
	})
	p := &downgradeProvider{tenant: tenant, target: target, periods: [2]time.Time{now.Add(-15 * 24 * time.Hour), end}, mollie: fixture}
	p.stage(t, target)
	if err := tenant.svc.ReconcileProviderTier(t.Context(), tenant.id); err != nil {
		t.Fatal(err)
	}
	if amount != "20.00" {
		t.Fatalf("suspended subscription amount = %s, want 20.00 or a retryable error, not a silent confirmation", amount)
	}
}

// Mollie restores the amount the subscriber paid, not the catalog fee.
func TestCancelledMollieDowngradeRestoresTheOriginalAmount_RealPG(t *testing.T) {
	db := startProviderReservationDatabase(t)
	now := time.Now().UTC().Truncate(time.Second)
	end := now.Add(15 * 24 * time.Hour)
	tenant := newPhaseTenant(t, db, "postpaid", now.Add(-15*24*time.Hour), end, 0)
	fixture, client := installMollieFixture(t)
	tenant.svc.mollieClient = client
	target := insertTierChangeTier(t, db, "20")
	mustExec(t, db, `INSERT INTO purser.mollie_customers(tenant_id,mollie_customer_id) VALUES($1,'customer')`, tenant.id)
	mustExec(t, db, `UPDATE purser.tenant_subscriptions SET payment_method='mollie',mollie_subscription_id='subscription' WHERE tenant_id=$1`, tenant.id)
	path := "/v2/customers/customer/subscriptions/subscription"
	amount, metadata := "65.00", map[string]any{"tenant_id": tenant.id, "tier_id": tenant.supporterID}
	resource := func() map[string]any {
		return map[string]any{"id": "subscription", "status": "active", "metadata": metadata, "amount": map[string]string{"value": amount, "currency": "EUR"}, "nextPaymentDate": end.Format(time.DateOnly)}
	}
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
	p := &downgradeProvider{tenant: tenant, target: target, periods: [2]time.Time{now.Add(-15 * 24 * time.Hour), end}, mollie: fixture}
	p.stage(t, target)
	p.cancelStaged(t)
	if err := tenant.svc.ReconcileProviderTier(t.Context(), tenant.id); err != nil {
		t.Fatal(err)
	}
	if patches := fixture.matching("PATCH", path); len(patches) != 0 {
		t.Fatalf("cancelled stage patched Mollie %d times", len(patches))
	}
	p.stage(t, target)
	if err := tenant.svc.ReconcileProviderTier(t.Context(), tenant.id); err != nil {
		t.Fatal(err)
	}
	p.cancelStaged(t)
	if err := tenant.svc.ReconcileProviderTier(t.Context(), tenant.id); err != nil {
		t.Fatal(err)
	}
	if amount != "65.00" || metadata["tier_id"] != tenant.supporterID || metadata["billing_restore_amount"] != nil {
		t.Fatalf("after cancelling: amount=%s metadata=%v, want the original 65.00", amount, metadata)
	}
}

// An upgrade waits for a cancelled downgrade's provider step, or its credit and
// the step would each act on a price the other is moving.
func TestUpgradeWaitsForAnUnfinishedProviderStep_RealPG(t *testing.T) {
	p := newStripeDowngrade(t, "20", "new-price", 2000)
	ctx := t.Context()
	higher := insertTierChangeTier(t, p.tenant.db, "120")
	mustExec(t, p.tenant.db, `UPDATE purser.billing_tiers SET stripe_price_id_monthly='higher-price' WHERE id=$1`, higher)
	p.stage(t, p.target)
	if err := p.tenant.svc.ReconcileProviderTier(ctx, p.tenant.id); err != nil {
		t.Fatal(err)
	}
	p.cancelStaged(t)
	err := database.WithRetryablePostgresTx(ctx, p.tenant.db, nil, func(tx *sql.Tx) error {
		_, stageErr := p.tenant.svc.StageTierUpgradeTx(ctx, tx, p.tenant.id, higher, p.periods[0], p.periods[1])
		return stageErr
	})
	if !errors.Is(err, billing.ErrTierUpgradeRejected) || !strings.Contains(err.Error(), "retry") {
		t.Fatalf("upgrade during an unfinished provider step = %v, want a retryable refusal", err)
	}
}

// A Purser-billed tenant moved to Free has no later collectable invoice either.
func TestAppliedFreeDowngradeWritesOffTheCarry_RealPG(t *testing.T) {
	db := startProviderReservationDatabase(t)
	now := time.Now().UTC()
	tenant := newPhaseTenant(t, db, "postpaid", now.Add(-time.Hour), now.Add(time.Hour), 0)
	tenant.jobs.tierReconciler = &stubTierReconciler{}
	free := insertTierChangeTier(t, db, "0")
	mustExec(t, db, `UPDATE purser.billing_tiers SET tier_name='free',is_default_postpaid=true,tier_level=1 WHERE id=$1`, free)
	mustExec(t, db, `INSERT INTO purser.billing_collection_balances(tenant_id,currency,balance_cents) VALUES($1,'EUR',150)`, tenant.id)
	mustExec(t, db, `UPDATE purser.tenant_subscriptions SET pending_tier_id=$2,pending_reason='downgrade',pending_effective_at=NOW()-INTERVAL '1 second' WHERE tenant_id=$1`, tenant.id, free)
	tenant.jobs.applyPendingDowngrade(t.Context(), tenant.id)
	var tier, reason string
	var balance, written int64
	if err := db.QueryRowContext(t.Context(), `SELECT s.tier_id::text,b.balance_cents,
		(SELECT COALESCE(SUM(amount_cents),0)::bigint FROM purser.billing_collection_writeoffs w WHERE w.tenant_id=$1),
		COALESCE((SELECT MAX(reason) FROM purser.billing_collection_writeoffs w WHERE w.tenant_id=$1),'')
		FROM purser.tenant_subscriptions s JOIN purser.billing_collection_balances b ON b.tenant_id=s.tenant_id WHERE s.tenant_id=$1`, tenant.id).Scan(&tier, &balance, &written, &reason); err != nil {
		t.Fatal(err)
	}
	if tier != free || balance != 0 || written != 150 || reason != "moved_to_free" {
		t.Fatalf("Free downgrade carry: tier=%s balance=%d written=%d reason=%q, want balance=0 written=150 reason=moved_to_free", tier, balance, written, reason)
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
