//go:build schema_verify

package handlers

import (
	"context"
	"database/sql"
	"frameworks/api_billing/internal/appconfig/appconfigtest"
	billingpkg "frameworks/api_billing/internal/billing"
	"frameworks/api_billing/internal/database/purserdb"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/models"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestFundedFreeActivatesCanonicalPAYG_RealPG(t *testing.T) {
	appconfigtest.Set(t, "WAIVE_USAGE_CHARGES", "false")
	db := startProviderReservationDatabase(t)
	now := time.Now().UTC()
	p := newPhaseTenant(t, db, "postpaid", now.Add(-time.Hour), now.AddDate(0, 1, 0), 912)
	freeID := uuid.NewString()
	if _, err := db.Exec(`INSERT INTO purser.billing_tiers (id,tier_name,display_name,tier_level,base_price,currency,metering_enabled) VALUES ($1,'free','Free',1,0,'EUR',true)`, freeID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE purser.tenant_subscriptions SET tier_id=$2,automatic_prepaid_usage=true WHERE tenant_id=$1`, p.id, freeID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO purser.tier_entitlements (tier_id,key,value) VALUES ($1,'max_concurrent_streams','3'),($1,'storage_limit_gb','10')`, freeID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO purser.subscription_entitlement_overrides (subscription_id,key,value) SELECT id,'custom_subdomain_enabled','true' FROM purser.tenant_subscriptions WHERE tenant_id=$1`, p.id); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO purser.tier_pricing_rules (tier_id,meter,model,currency,included_quantity,unit_price,config) VALUES ($1,'egress_gb','all_usage','EUR',0,0,'{}')`, freeID); err != nil {
		t.Fatal(err)
	}
	recordEgressReceived(t, db, p.id, "free-before-funding", now.Add(-30*time.Minute), 100, now.Add(-25*time.Minute))
	// Concurrent callbacks and the background backstop must commit one boundary.
	var wg sync.WaitGroup
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := p.svc.ActivateFundedFree(t.Context(), p.id); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	var tier, model string
	var balance, events int64
	if err := db.QueryRow(`SELECT tier_id::text,billing_model FROM purser.tenant_subscriptions WHERE tenant_id=$1`, p.id).Scan(&tier, &model); err != nil {
		t.Fatal(err)
	}
	if tier != p.paygID || model != "prepaid" {
		t.Fatalf("funded Free remained %s/%s; want canonical PAYG/prepaid", tier, model)
	}
	if err := db.QueryRow(`SELECT balance_cents FROM purser.prepaid_balances WHERE tenant_id=$1 AND currency='EUR'`, p.id).Scan(&balance); err != nil {
		t.Fatal(err)
	}
	effective, err := billingpkg.LoadSubscriptionEffectiveTier(t.Context(), db, p.id)
	if err != nil {
		t.Fatal(err)
	}
	if _, present := effective.Entitlements["max_concurrent_streams"]; present {
		t.Fatal("PAYG inherited Free stream cap")
	}
	if _, present := effective.Entitlements["storage_limit_gb"]; present {
		t.Fatal("PAYG inherited Free storage cap")
	}
	if effective.Entitlements["custom_subdomain_enabled"] != "true" {
		t.Fatal("explicit entitlement override lost")
	}
	if len(effective.Rules) != 1 || effective.Rules[0].UnitPrice.String() != "0.01" {
		t.Fatalf("canonical PAYG pricing not activated: %+v", effective.Rules)
	}
	if balance != 912 {
		t.Fatalf("funding changed: %d", balance)
	}
	if err := db.QueryRow(`SELECT count(*) FROM purser.domain_event_outbox WHERE tenant_id=$1 AND event_type='billing.subscription_updated'`, p.id).Scan(&events); err != nil {
		t.Fatal(err)
	}
	if events != 1 {
		t.Fatalf("funded activation events=%d; want one", events)
	}
	if changed, err := p.svc.ActivateFundedFree(t.Context(), p.id); err != nil || changed {
		t.Fatalf("replay changed=%v err=%v", changed, err)
	}
	p.receive(t, egressReport(p.id, "payg-after-funding", time.Now().UTC().Truncate(5*time.Minute).Add(5*time.Minute), 100))
	if got := p.balance(t); got != 812 {
		t.Fatalf("new usage did not use PAYG prices or historical Free usage was repriced: %d", got)
	}
	// Funding activation alone does not implement exhaustion fallback.
	// Funding a paid subscription never replaces its term.
	if _, err := db.Exec(`UPDATE purser.prepaid_balances SET balance_cents=0 WHERE tenant_id=$1`, p.id); err != nil {
		t.Fatal(err)
	}
	if changed, err := p.svc.ActivateFundedFree(t.Context(), p.id); err != nil || changed {
		t.Fatalf("empty PAYG changed=%v err=%v", changed, err)
	}
	paid := newPhaseTenant(t, db, "postpaid", now.Add(-time.Hour), now.AddDate(0, 1, 0), 912)
	if _, err := db.Exec(`UPDATE purser.tenant_subscriptions SET automatic_prepaid_usage=true WHERE tenant_id=$1`, paid.id); err != nil {
		t.Fatal(err)
	}
	if changed, err := paid.svc.ActivateFundedFree(t.Context(), paid.id); err != nil || changed {
		t.Fatalf("paid subscription changed=%v err=%v", changed, err)
	}
}

func TestFundedFreePreservesNonEligibleAccounts_RealPG(t *testing.T) {
	db := startProviderReservationDatabase(t)
	now := time.Now().UTC()
	freeID := uuid.NewString()
	if _, err := db.Exec(`INSERT INTO purser.billing_tiers (id,tier_name,display_name,tier_level,base_price,currency,metering_enabled) VALUES ($1,'free','Free',1,0,'EUR',true)`, freeID); err != nil {
		t.Fatal(err)
	}
	cases := []struct{ name, mutation string }{
		{"explicit Free choice", `UPDATE purser.tenant_subscriptions SET automatic_prepaid_usage=false WHERE tenant_id=$1`},
		{"unfunded", `UPDATE purser.prepaid_balances SET balance_cents=0 WHERE tenant_id=$1`},
		{"negative", `UPDATE purser.prepaid_balances SET balance_cents=-10 WHERE tenant_id=$1`},
		{"suspended", `UPDATE purser.tenant_subscriptions SET status='suspended' WHERE tenant_id=$1`},
		{"pending purchase", `UPDATE purser.tenant_subscriptions SET pending_tier_id=tier_id,pending_reason='stripe_checkout' WHERE tenant_id=$1`},
		{"native subscription", `UPDATE purser.tenant_subscriptions SET stripe_subscription_id='sub_keep' WHERE tenant_id=$1`},
		{"provider cancellation", `UPDATE purser.tenant_subscriptions SET free_fallback_provider_stopping=true WHERE tenant_id=$1`},
		{"operator grant", `INSERT INTO purser.subscription_operator_grants (subscription_id,reason) SELECT id,'preserve operator terms' FROM purser.tenant_subscriptions WHERE tenant_id=$1`},
		{"closed", `INSERT INTO purser.tenant_billing_closures (tenant_id,closed_at,source_event_id) VALUES ($1,NOW(),'closed')`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := newPhaseTenant(t, db, "postpaid", now.Add(-time.Hour), now.AddDate(0, 1, 0), 912)
			if _, err := db.Exec(`UPDATE purser.tenant_subscriptions SET tier_id=$2,automatic_prepaid_usage=true WHERE tenant_id=$1`, p.id, freeID); err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec(tc.mutation, p.id); err != nil {
				t.Fatal(err)
			}
			if changed, err := p.svc.ActivateFundedFree(t.Context(), p.id); err != nil || changed {
				t.Fatalf("noneligible activation: changed=%v err=%v", changed, err)
			}
		})
	}
}

func TestFundedFreeMigrationActivatesLegacyCredit_RealPG(t *testing.T) {
	db := startProviderReservationDatabase(t)
	now := time.Now().UTC()
	legacy := newPhaseTenant(t, db, "postpaid", now.Add(-time.Hour), now.AddDate(0, 1, 0), 912)
	freeID := uuid.NewString()
	if _, err := db.Exec(`INSERT INTO purser.billing_tiers (id,tier_name,display_name,tier_level,base_price,currency,metering_enabled) VALUES ($1,'free','Free',1,0,'EUR',true)`, freeID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE purser.tenant_subscriptions SET tier_id=$2 WHERE tenant_id=$1`, legacy.id, freeID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`ALTER TABLE purser.tenant_subscriptions DROP COLUMN automatic_prepaid_usage`); err != nil {
		t.Fatal(err)
	}
	migration, err := os.ReadFile(filepath.Join("..", "..", "..", "pkg", "database", "sql", "migrations", "purser", "v0.3.16", "expand", "002_automatic_prepaid_usage.sql"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(string(migration)); err != nil {
		t.Fatal(err)
	}
	if changed, err := legacy.svc.ActivateFundedFree(t.Context(), legacy.id); err != nil || !changed {
		t.Fatalf("migration did not activate existing funded Free: changed=%v err=%v", changed, err)
	}
	if got := legacy.balance(t); got != 912 {
		t.Fatalf("legacy credit changed: %d", got)
	}
	fresh := uuid.NewString()
	if _, err = db.Exec(`INSERT INTO purser.tenant_subscriptions (tenant_id,tier_id,status,billing_model) VALUES ($1,$2,'active','postpaid')`, fresh, freeID); err != nil {
		t.Fatal(err)
	}
	var enabled bool
	if err = db.QueryRow(`SELECT automatic_prepaid_usage FROM purser.tenant_subscriptions WHERE tenant_id=$1`, fresh).Scan(&enabled); err != nil || !enabled {
		t.Fatalf("old writer did not inherit automatic funding: %v %v", enabled, err)
	}
	currentWriter := uuid.NewString()
	if _, err = purserdb.New(db).EnsureDefaultTenantSubscription(t.Context(), purserdb.EnsureDefaultTenantSubscriptionParams{
		ID: uuid.New(), TenantID: currentWriter, TierID: uuid.MustParse(freeID), BillingModel: "postpaid", Now: sql.NullTime{Time: now, Valid: true},
	}); err != nil {
		t.Fatal(err)
	}
	if err = db.QueryRow(`SELECT automatic_prepaid_usage FROM purser.tenant_subscriptions WHERE tenant_id=$1`, currentWriter).Scan(&enabled); err != nil || !enabled {
		t.Fatalf("current writer did not enable its disclosed funding policy: %v %v", enabled, err)
	}
}

func TestFundedFreeExhaustionReturnsEligibleAccount_RealPG(t *testing.T) {
	appconfigtest.Set(t, "WAIVE_USAGE_CHARGES", "false")
	db := startProviderReservationDatabase(t)
	now := time.Now().UTC().Truncate(5 * time.Minute)
	p := newPhaseTenant(t, db, "prepaid", now.Add(-time.Hour), now.AddDate(0, 1, 0), 100)
	freeID := uuid.NewString()
	if _, err := db.Exec(`INSERT INTO purser.billing_tiers (id,tier_name,display_name,tier_level,base_price,currency,is_default_postpaid,is_active,metering_enabled) VALUES ($1,'free','Free',1,0,'EUR',true,true,true)`, freeID); err != nil {
		t.Fatal(err)
	}
	seedFreeResourceLimits(t, db, freeID)
	p.svc.SetFreeTierEligibilityReader(func(context.Context, string, FreeTierLimits) (bool, error) { return true, nil })
	p.receive(t, egressReport(p.id, "funded-then-spent", now.Add(-20*time.Minute), 100))
	if got := p.balance(t); got != 0 {
		t.Fatalf("balance=%d", got)
	}
	var wg sync.WaitGroup
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := p.svc.ReturnExhaustedPrepaidToFree(t.Context(), p.id); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	var tier, model string
	var automatic bool
	if err := db.QueryRow(`SELECT tier_id::text,billing_model,automatic_prepaid_usage FROM purser.tenant_subscriptions WHERE tenant_id=$1`, p.id).Scan(&tier, &model, &automatic); err != nil {
		t.Fatal(err)
	}
	if tier != freeID || model != "postpaid" || !automatic {
		t.Fatalf("exhausted eligible account=%s/%s automatic=%v; want Free with funding preference retained", tier, model, automatic)
	}
	var count int
	if err := db.QueryRow(`SELECT count(*) FROM purser.billing_invoices WHERE tenant_id=$1 AND document_kind='prepaid_statement'`, p.id).Scan(&count); err != nil || count != 1 {
		t.Fatalf("closing statements=%d err=%v", count, err)
	}
	if _, err := db.Exec(`UPDATE purser.prepaid_balances SET balance_cents=912 WHERE tenant_id=$1`, p.id); err != nil {
		t.Fatal(err)
	}
	if changed, err := p.svc.ActivateFundedFree(t.Context(), p.id); err != nil || !changed {
		t.Fatalf("next funding failed to restore PAYG: %v %v", changed, err)
	}
}

func TestFundedFreeExhaustionPreservesIneligibleAccounts_RealPG(t *testing.T) {
	appconfigtest.Set(t, "WAIVE_USAGE_CHARGES", "false")
	db := startProviderReservationDatabase(t)
	now := time.Now().UTC().Truncate(5 * time.Minute)
	freeID := uuid.NewString()
	if _, err := db.Exec(`INSERT INTO purser.billing_tiers (id,tier_name,display_name,tier_level,base_price,currency,is_default_postpaid,is_active) VALUES ($1,'free','Free',1,0,'EUR',true,true)`, freeID); err != nil {
		t.Fatal(err)
	}
	seedFreeResourceLimits(t, db, freeID)
	cases := []struct {
		name, mutation string
		verified       bool
	}{
		{"wallet-only", "", false},
		{"unavailable identity", "", false},
		{"monthly cluster contract", `INSERT INTO purser.cluster_subscriptions(tenant_id,cluster_id,status,stripe_subscription_id) VALUES($1,'paid-contract','active','sub_paid_contract')`, true},
		{"collection carry", `INSERT INTO purser.billing_collection_balances(tenant_id,currency,balance_cents) VALUES($1,'EUR',49)`, true},
		{"unpaid invoice", `INSERT INTO purser.billing_invoices(tenant_id,invoice_number,status,amount,currency,document_kind,due_date) SELECT tenant_id,'held-'||tenant_id::text,'pending',1,'EUR','invoice',NOW() FROM purser.tenant_subscriptions WHERE tenant_id=$1`, true},
		{"unsettled priced usage", "", true},
		{"debt", `UPDATE purser.prepaid_balances SET balance_cents=-1 WHERE tenant_id=$1`, true},
		{"sub-cent debt", `UPDATE purser.prepaid_balances SET balance_remainder_micro=1 WHERE tenant_id=$1`, true},
		{"funded", `UPDATE purser.prepaid_balances SET balance_cents=1 WHERE tenant_id=$1`, true},
		{"pending purchase", `UPDATE purser.tenant_subscriptions SET pending_tier_id=tier_id WHERE tenant_id=$1`, true},
		{"grant", `INSERT INTO purser.subscription_operator_grants(subscription_id,reason) SELECT id,'preserve terms' FROM purser.tenant_subscriptions WHERE tenant_id=$1`, true},
		{"custom rate", `INSERT INTO purser.subscription_pricing_overrides(subscription_id,meter,unit_price) SELECT id,'egress_gb',0.1 FROM purser.tenant_subscriptions WHERE tenant_id=$1`, true},
		{"closed", `INSERT INTO purser.tenant_billing_closures(tenant_id,closed_at,source_event_id) VALUES($1,NOW(),'test')`, true},
		{"provider", `UPDATE purser.tenant_subscriptions SET stripe_subscription_id='sub_retained' WHERE tenant_id=$1`, true},
		{"unsettled reservation", `INSERT INTO purser.usage_reservations(tenant_id,source_id,cluster_id,sequence,report_id,period_start,period_end,meters,reserved_amount_micro,currency) VALUES($1,'live','cluster',1,'open',NOW()-INTERVAL '5 minutes',NOW(),'[]',1,'EUR')`, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := newPhaseTenant(t, db, "prepaid", now.Add(-time.Hour), now.AddDate(0, 1, 0), 0)
			if tc.name != "unavailable identity" {
				p.svc.SetFreeTierEligibilityReader(func(context.Context, string, FreeTierLimits) (bool, error) { return tc.verified, nil })
			}
			if tc.mutation != "" {
				if _, err := db.Exec(tc.mutation, p.id); err != nil {
					t.Fatal(err)
				}
			}
			if tc.name == "unsettled priced usage" {
				recordEgressReceived(t, db, p.id, "received-not-yet-settled", now.Add(-20*time.Minute), 100, now.Add(-10*time.Minute))
			}
			before := p.balance(t)
			eligibleDespiteDebt := map[string]bool{"collection carry": true, "unpaid invoice": true, "unsettled priced usage": true, "debt": true, "sub-cent debt": true, "unsettled reservation": true}[tc.name]
			changed, err := p.svc.ReturnExhaustedPrepaidToFree(t.Context(), p.id)
			if err != nil || changed != eligibleDespiteDebt {
				t.Fatalf("fallback=%v want=%v err=%v", changed, eligibleDespiteDebt, err)
			}
			if got := p.balance(t); (!changed && got != before) || got > before {
				t.Fatalf("fallback erased debt or changed a held wallet: before=%d after=%d", before, got)
			}
			var statements int
			wantStatements := 0
			if changed {
				wantStatements = 1
			}
			if err := db.QueryRow(`SELECT count(*) FROM purser.billing_invoices WHERE tenant_id=$1 AND document_kind='prepaid_statement'`, p.id).Scan(&statements); err != nil || statements != wantStatements {
				t.Fatalf("phase boundary count=%d want=%d err=%v", statements, wantStatements, err)
			}
		})
	}
}

func TestFundedFreeAllowanceSurvivesFundingCycle_RealPG(t *testing.T) {
	appconfigtest.Set(t, "WAIVE_USAGE_CHARGES", "false")
	db := startProviderReservationDatabase(t)
	now := time.Now().UTC().Truncate(5 * time.Minute)
	p := newPhaseTenant(t, db, "postpaid", now.Add(-time.Hour), now.AddDate(0, 1, 0), 100)
	free := uuid.NewString()
	if _, err := db.Exec(`INSERT INTO purser.billing_tiers(id,tier_name,display_name,tier_level,base_price,currency,is_default_postpaid,is_active,metering_enabled) VALUES($1,'free','Free',1,0,'EUR',true,true,true)`, free); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE purser.tenant_subscriptions SET tier_id=$2 WHERE tenant_id=$1`, p.id, free); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO purser.tier_pricing_rules(tier_id,meter,model,currency,included_quantity,unit_price,config) VALUES($1,'delivered_minutes','tiered_graduated','EUR',10000,0,'{}')`, free); err != nil {
		t.Fatal(err)
	}
	seedFreeResourceLimits(t, db, free)
	p.svc.SetFreeTierEligibilityReader(func(context.Context, string, FreeTierLimits) (bool, error) { return true, nil })
	delivery := func(id string, at time.Time, qty float64) models.UsageSummary {
		return models.UsageSummary{TenantID: p.id, ClusterID: phaseCluster, SourceID: "periscope-default", ReportID: id, PeriodStart: at, PeriodEnd: at.Add(5 * time.Minute), Meters: []models.MeterQuantity{{Meter: "delivered_minutes", Unit: "minute", Quantity: qty}}}
	}
	p.receive(t, delivery("first-free", now.Add(-40*time.Minute), 9000))
	if changed, err := p.svc.ActivateFundedFree(t.Context(), p.id); err != nil || !changed {
		t.Fatalf("funded=%v err=%v", changed, err)
	}
	p.receive(t, egressReport(p.id, "paid-consumption", now.Add(-20*time.Minute), 100))
	if changed, err := p.svc.ReturnExhaustedPrepaidToFree(t.Context(), p.id); err != nil || !changed {
		t.Fatalf("fallback=%v err=%v balance=%d", changed, err, p.balance(t))
	}
	p.receive(t, delivery("returned-free", now.Add(-5*time.Minute), 1500))
	var included, quantity string
	if err := db.QueryRow(`SELECT line.included_quantity::text,line.quantity::text FROM purser.invoice_line_items line JOIN purser.billing_invoices invoice ON invoice.id=line.invoice_id AND invoice.tenant_id=line.tenant_id WHERE invoice.tenant_id=$1 AND invoice.status='draft' AND line.meter='delivered_minutes'`, p.id).Scan(&included, &quantity); err != nil {
		t.Fatal(err)
	}
	if included != "1000.000000" || quantity != "1500.000000" {
		t.Fatalf("Free allowance reset across funding: included=%s quantity=%s", included, quantity)
	}
	var start time.Time
	if err := db.QueryRow(`SELECT billing_period_start FROM purser.tenant_subscriptions WHERE tenant_id=$1`, p.id).Scan(&start); err != nil {
		t.Fatal(err)
	}
	prior, err := PriorFreeAllowanceUsage(t.Context(), db, p.id, start, "delivered_minutes")
	if err != nil || prior.String() != "9000" {
		t.Fatalf("prior Free usage=%s err=%v", prior, err)
	}
}

func TestFundedFreePolicy_RealYugabyte(t *testing.T) {
	for _, tc := range []struct {
		name string
		run  func(*testing.T)
	}{
		{"activation", TestFundedFreeActivatesCanonicalPAYG_RealPG},
		{"protected arrangements", TestFundedFreePreservesNonEligibleAccounts_RealPG},
		{"legacy funding", TestFundedFreeMigrationActivatesLegacyCredit_RealPG},
		{"exhaustion", TestFundedFreeExhaustionReturnsEligibleAccount_RealPG},
		{"ineligible fallback", TestFundedFreeExhaustionPreservesIneligibleAccounts_RealPG},
		{"allowance conservation", TestFundedFreeAllowanceSurvivesFundingCycle_RealPG},
	} {
		t.Run(tc.name, tc.run)
	}
}

func seedFreeResourceLimits(t *testing.T, db *sql.DB, tierID string) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO purser.tier_entitlements(tier_id,key,value) VALUES($1,'storage_limit_gb','10'),($1,'recording_retention_days','30') ON CONFLICT(tier_id,key) DO NOTHING`, tierID); err != nil {
		t.Fatal(err)
	}
}
