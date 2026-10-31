//go:build schema_verify

package grpc

import (
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"frameworks/api_billing/internal/database/purserdb"
	"frameworks/api_billing/internal/handlers"
	billingmollie "frameworks/api_billing/internal/mollie"
	billingstripe "frameworks/api_billing/internal/stripe"
	"frameworks/api_billing/internal/subscriptions"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/billing"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	purserpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/purser"
	"github.com/google/uuid"
	stripeapi "github.com/stripe/stripe-go/v85"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type upgradeProviderFixture struct {
	mu               sync.Mutex
	tenant, oldTier  string
	start, end       time.Time
	metadata         map[string]any
	stripeMetadata   map[string]string
	updates, charges int
	cancels          int
	cancelled        bool
	amount, currency string
	loseResponse     bool
	unpaid           bool
	rejectFirstPrice bool
	priceReads       int
	roundBoundary    time.Time
	unpaidRenewal    bool
	stripeParams     url.Values
}

func (f *upgradeProviderFixture) serve(t *testing.T, w http.ResponseWriter, r *http.Request) {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	respond := func(body any) {
		if err := json.NewEncoder(w).Encode(body); err != nil {
			t.Error(err)
		}
	}
	switch r.Method + " " + r.URL.Path {
	case "GET /v1/invoices":
		respond(map[string]any{"object": "list", "data": []any{map[string]any{"id": "upgrade-invoice", "billing_reason": "subscription_update", "parent": map[string]any{"type": "subscription_details", "subscription_details": map[string]any{"metadata": f.stripeMetadata, "subscription": "sub"}}, "lines": map[string]any{"object": "list", "has_more": false, "data": []any{map[string]any{"id": "base", "amount": 5000, "pricing": map[string]any{"price_details": map[string]any{"price": "new-price"}}, "period": map[string]any{"start": f.start.Unix(), "end": f.end.Unix()}}}}}}})
	case "GET /v1/invoice_payments":
		respond(map[string]any{"object": "list", "data": []any{map[string]any{"id": "old-payment", "amount_paid": 2000, "payment": map[string]any{"type": "charge", "charge": "old-charge"}}}})
	case "GET /v1/charges/old-charge":
		respond(map[string]any{"id": "old-charge", "amount": 2000, "amount_refunded": 0, "refunded": false, "disputed": false})
	case "GET /v1/prices/new-price":
		f.priceReads++
		amount := 5000
		if f.rejectFirstPrice && f.priceReads == 1 {
			amount = 6000
		}
		respond(map[string]any{"id": "new-price", "object": "price", "active": true, "unit_amount": amount, "currency": "eur", "recurring": map[string]any{"interval": "month", "interval_count": 1}})
	case "GET /v1/customers/customer":
		respond(map[string]any{"id": "customer", "object": "customer", "invoice_settings": map[string]any{"default_payment_method": map[string]any{"id": "pm", "object": "payment_method"}}})
	case "GET /v1/subscriptions/sub", "POST /v1/subscriptions/sub":
		price := "old-price"
		invoiceStatus := "paid"
		if f.unpaid {
			invoiceStatus = "open"
		}
		if r.Method == http.MethodPost {
			if err := r.ParseForm(); err != nil {
				t.Error(err)
				return
			}
			f.updates++
			f.stripeParams = r.Form
			f.stripeMetadata = map[string]string{"tenant_id": f.tenant, "tier_id": r.Form.Get("metadata[tier_id]"), "billing_tier_upgrade_id": r.Form.Get("metadata[billing_tier_upgrade_id]")}
			f.start = time.Now().UTC().Truncate(time.Second)
			if !f.roundBoundary.IsZero() {
				f.start = f.roundBoundary.Truncate(time.Second)
			}
			f.end = f.start.AddDate(0, 1, 0)
			if f.loseResponse {
				f.loseResponse = false
				w.Header().Set("Stripe-Should-Retry", "false")
				w.WriteHeader(http.StatusBadGateway)
				respond(map[string]any{"error": map[string]string{"type": "api_error", "message": "update committed but response was lost"}})
				return
			}
		}
		if f.stripeMetadata["billing_tier_upgrade_id"] != "" {
			price = "new-price"
		}
		respond(map[string]any{"id": "sub", "object": "subscription", "customer": "customer", "status": "active", "metadata": f.stripeMetadata,
			"latest_invoice": map[string]any{"id": "old-invoice", "object": "invoice", "status": invoiceStatus, "amount_paid": 2000},
			"items":          map[string]any{"object": "list", "data": []any{map[string]any{"id": "item", "object": "subscription_item", "price": map[string]any{"id": price, "object": "price"}, "current_period_start": f.start.Unix(), "current_period_end": f.end.Unix()}}}})
	case "POST /v1/payment_intents":
		if err := r.ParseForm(); err != nil {
			t.Error(err)
			return
		}
		f.charges++
		f.amount = r.Form.Get("amount")
		f.currency = r.Form.Get("currency")
		respond(map[string]any{"id": "pi_upgrade", "object": "payment_intent", "status": "processing"})
	case "DELETE /v2/customers/customer/subscriptions/sub":
		f.cancels++
		f.cancelled = true
		respond(map[string]any{"id": "sub", "status": "canceled"})
	case "GET /v2/customers/customer/subscriptions/sub", "PATCH /v2/customers/customer/subscriptions/sub":
		if r.Method == http.MethodPatch {
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Error(err)
				return
			}
			f.updates++
			next, parseErr := time.Parse(time.DateOnly, body["startDate"].(string))
			if parseErr != nil {
				t.Error(parseErr)
				return
			}
			f.end = next
			f.metadata = body["metadata"].(map[string]any)
			if body["amount"].(map[string]any)["value"] != "50.00" {
				t.Errorf("wrong Mollie renewal amount: %v", body)
			}
		}
		value := "20.00"
		if f.metadata["billing_tier_upgrade_id"] != "" {
			value = "50.00"
		}
		state := "active"
		if f.cancelled {
			state = "canceled"
		}
		respond(map[string]any{"id": "sub", "resource": "subscription", "status": state, "createdAt": f.start.Format(time.RFC3339), "nextPaymentDate": f.end.Format(time.DateOnly), "metadata": f.metadata, "amount": map[string]string{"value": value, "currency": "EUR"}})
	case "GET /v2/customers/customer/payments":
		state, created := "paid", f.start
		if f.unpaidRenewal {
			state, created = "pending", f.end.Add(time.Hour)
		}
		respond(map[string]any{"_embedded": map[string]any{"payments": []any{map[string]any{"id": "old-payment", "subscriptionId": "sub", "customerId": "customer", "status": state, "createdAt": created.Format(time.RFC3339), "amount": map[string]string{"value": "20.00", "currency": "EUR"}}}}, "_links": map[string]any{}})
	case "GET /v2/customers/customer/mandates/mandate":
		respond(map[string]any{"id": "mandate", "resource": "mandate", "status": "valid", "method": "directdebit"})
	case "POST /v2/customers/customer/payments":
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
			return
		}
		f.charges++
		amount := body["amount"].(map[string]any)
		f.amount = amount["value"].(string)
		f.currency = amount["currency"].(string)
		respond(map[string]any{"id": "tr_upgrade", "resource": "payment", "status": "pending", "amount": amount})
	default:
		t.Errorf("unexpected financial operation: %s %s", r.Method, r.URL.Path)
		w.WriteHeader(http.StatusNotFound)
		respond(map[string]any{"error": "unexpected operation"})
	}
}

type upgradeMollieTransport struct {
	base     http.RoundTripper
	endpoint *url.URL
}

func (tr upgradeMollieTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	if request.URL.Host != "api.mollie.com" {
		return tr.base.RoundTrip(request)
	}
	copy := request.Clone(request.Context())
	u := *request.URL
	u.Scheme = tr.endpoint.Scheme
	u.Host = tr.endpoint.Host
	copy.URL = &u
	copy.Host = u.Host
	return tr.base.RoundTrip(copy)
}

func TestTierUpgradeProviders_RealPG(t *testing.T) {
	for _, mode := range []string{"advance Stripe", "advance missing invoice", "fixed Stripe", "fixed Mollie", "Mollie cancellation after provider receipt", "lost Stripe response", "metering delay", "cancelled subscription during metering wait", "cancelled subscription after advance receipt", "account closure after advance receipt", "refusal write failure", "stale Stripe price", "unpaid Stripe fee", "rejected price transaction retry", "Stripe rounded grant boundary", "Stripe same second callback", "unpaid Mollie renewal", "paid Mollie renewal before rollover", "declined upgrade with scheduled downgrade", "operator cancels metering wait", "operator completes lost Stripe response"} {
		t.Run(mode, func(t *testing.T) {
			db := startOperatorGrantDatabase(t)
			if strings.Contains(t.Name(), "_RealYugabyte") {
				var engine string
				if err := db.QueryRowContext(t.Context(), `SELECT version()`).Scan(&engine); err != nil || !strings.Contains(engine, "-YB-") {
					t.Fatalf("Yugabyte case reached the wrong engine: %s (%v)", engine, err)
				}
			}
			ctx := t.Context()
			tenant, oldTier, newTier := uuid.NewString(), uuid.NewString(), uuid.NewString()
			start := time.Now().UTC().Add(-15 * 24 * time.Hour).Truncate(time.Second)
			end := start.Add(30 * 24 * time.Hour)
			if mode == "Stripe rounded grant boundary" {
				start = time.Now().UTC().Add(-time.Second).Truncate(time.Second).Add(123456 * time.Microsecond)
				end = start.Add(15 * 24 * time.Hour)
			}
			if mode == "Stripe same second callback" {
				start = time.Now().UTC().Truncate(time.Second).Add(-time.Second)
				end = start.AddDate(0, 1, 0)
			}
			if mode == "unpaid Mollie renewal" || mode == "paid Mollie renewal before rollover" {
				end = time.Now().UTC().Add(-2 * time.Hour).Truncate(time.Second)
				start = end.AddDate(0, -1, 0)
			}
			fixture := &upgradeProviderFixture{tenant: tenant, oldTier: oldTier, start: start, end: end, metadata: map[string]any{"tenant_id": tenant}, stripeMetadata: map[string]string{"tenant_id": tenant, "tier_id": oldTier}, loseResponse: mode == "lost Stripe response" || mode == "operator completes lost Stripe response", unpaid: mode == "unpaid Stripe fee", rejectFirstPrice: mode == "rejected price transaction retry", unpaidRenewal: mode == "unpaid Mollie renewal"}
			if mode == "Stripe same second callback" {
				fixture.roundBoundary = start
			}
			if mode == "Stripe rounded grant boundary" {
				fixture.roundBoundary = start
				fixture.start = start.Add(-15 * 24 * time.Hour)
			}
			httpServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fixture.serve(t, w, r) }))
			t.Cleanup(httpServer.Close)
			oldBackend := stripeapi.GetBackend(stripeapi.APIBackend)
			stripeapi.SetBackend(stripeapi.APIBackend, stripeapi.GetBackendWithConfig(stripeapi.APIBackend, &stripeapi.BackendConfig{URL: stripeapi.String(httpServer.URL), HTTPClient: httpServer.Client(), MaxNetworkRetries: stripeapi.Int64(0)}))
			t.Cleanup(func() { stripeapi.SetBackend(stripeapi.APIBackend, oldBackend) })
			logger := logging.NewLogger()
			stripeClient := billingstripe.NewClient(billingstripe.Config{SecretKey: "sk_test_fixture", Logger: logger})
			var mollieClient *billingmollie.Client
			if strings.Contains(mode, "Mollie") {
				endpoint, err := url.Parse(httpServer.URL)
				if err != nil {
					t.Fatal(err)
				}
				transport := upgradeMollieTransport{base: http.DefaultTransport, endpoint: endpoint}
				mollieClient, err = billingmollie.NewClient(billingmollie.Config{APIKey: "test_fixture", Logger: logger, HTTPClient: &http.Client{Transport: transport}})
				if err != nil {
					t.Fatal(err)
				}
			}
			exec := func(query string, args ...any) {
				t.Helper()
				if _, err := db.ExecContext(ctx, query, args...); err != nil {
					t.Fatal(err)
				}
			}
			exec(`INSERT INTO purser.billing_tiers (id,tier_name,display_name,tier_level,base_price,currency,metering_enabled,stripe_price_id_monthly)
VALUES ($1,$3,'Old',2,20,'EUR',false,'old-price'),($2,$4,'New',3,50,'EUR',false,'new-price')`, oldTier, newTier, "old-"+oldTier, "new-"+newTier)
			currency, method, stripeSub, mollieSub := "EUR", "stripe", "sub", ""
			advance := strings.HasPrefix(mode, "advance ") || mode == "metering delay" || mode == "cancelled subscription during metering wait" || mode == "cancelled subscription after advance receipt" || mode == "account closure after advance receipt" || mode == "operator cancels metering wait"
			if advance {
				currency = "USD"
				stripeSub = ""
			}
			if strings.Contains(mode, "Mollie") {
				method = "mollie"
				stripeSub = ""
				mollieSub = "sub"
			}
			exec(`INSERT INTO purser.tenant_subscriptions (tenant_id,tier_id,status,billing_model,payment_method,stripe_customer_id,stripe_subscription_id,mollie_subscription_id,
billing_name,billing_email,billing_address,presentment_currency,billing_period_start,billing_period_end,next_billing_date)
VALUES ($1,$2,'active','postpaid',$3,'customer',NULLIF($4,''),NULLIF($5,''),'Customer','billing@example.com',
'{"street":"Street 1","city":"Leiden","postal_code":"2332ED","country":"NL"}',$6,$7,$8,$8)`, tenant, oldTier, method, stripeSub, mollieSub, currency, start, end)
			if advance {
				exec(`INSERT INTO purser.fx_rates (currency,reference_date,units_per_eur,source) VALUES ('USD',CURRENT_DATE,1.25,'ecb') ON CONFLICT DO NOTHING`)
				if mode != "advance missing invoice" {
					exec(`INSERT INTO purser.billing_invoices (tenant_id,status,amount,base_amount,due_date,base_fee_period_start,base_fee_period_end,presentment_amount_cents,presentment_currency,presentment_units_per_eur,presentment_reference_date,finalized_at)
VALUES ($1,'paid',20,20,NOW(),$2,$3,2500,'USD',1.25,CURRENT_DATE,NOW())`, tenant, start, end)
				}
			}
			if strings.Contains(mode, "Mollie") {
				exec(`INSERT INTO purser.mollie_customers (tenant_id,mollie_customer_id) VALUES ($1,'customer')`, tenant)
				exec(`INSERT INTO purser.mollie_mandates (tenant_id,mollie_customer_id,mollie_mandate_id,status,method) VALUES ($1,'customer','mandate','valid','directdebit')`, tenant)
			}
			if mode == "metering delay" || mode == "cancelled subscription during metering wait" || mode == "operator cancels metering wait" {
				exec(`UPDATE purser.billing_tiers SET metering_enabled=true WHERE id IN ($1,$2)`, oldTier, newTier)
			}
			if mode == "stale Stripe price" || mode == "refusal write failure" || mode == "declined upgrade with scheduled downgrade" {
				exec(`UPDATE purser.billing_tiers SET base_price=60 WHERE id=$1`, newTier)
			}

			if mode == "rejected price transaction retry" {
				exec(`CREATE SEQUENCE purser.upgrade_refusal_retry`)
				exec(`CREATE FUNCTION purser.retry_upgrade_refusal() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF NEW.state='cancelled' AND nextval('purser.upgrade_refusal_retry')=1 THEN
  RAISE EXCEPTION 'retry refusal transaction' USING ERRCODE='40001';
 END IF;
 RETURN NEW;
END $$`)
				exec(`CREATE TRIGGER retry_upgrade_refusal BEFORE UPDATE ON purser.subscription_tier_upgrades FOR EACH ROW EXECUTE FUNCTION purser.retry_upgrade_refusal()`)
			}
			if mode == "declined upgrade with scheduled downgrade" {
				exec(`UPDATE purser.tenant_subscriptions SET pending_tier_id=$2,pending_reason='downgrade',pending_effective_at=$3 WHERE tenant_id=$1`, tenant, oldTier, end)
			}
			if mode == "cancelled subscription after advance receipt" || mode == "account closure after advance receipt" {
				if mode == "account closure after advance receipt" {
					exec(`CREATE FUNCTION purser.close_at_local_upgrade_receipt() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF NEW.state='provider_applied' THEN
  INSERT INTO purser.tenant_billing_closures(tenant_id,closed_at,source_event_id) VALUES(NEW.tenant_id,clock_timestamp(),'upgrade-closure-race');
 END IF;
 RETURN NEW;
END $$`)
					exec(`CREATE TRIGGER close_at_local_upgrade_receipt AFTER UPDATE ON purser.subscription_tier_upgrades FOR EACH ROW EXECUTE FUNCTION purser.close_at_local_upgrade_receipt()`)
				}
				exec(`CREATE FUNCTION purser.cancel_at_local_upgrade_receipt() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF NEW.state='provider_applied' THEN
  UPDATE purser.tenant_subscriptions SET status='cancelled',cancelled_at=NOW() WHERE tenant_id=NEW.tenant_id;
 END IF;
 RETURN NEW;
END $$`)
				exec(`CREATE TRIGGER cancel_at_local_upgrade_receipt AFTER UPDATE ON purser.subscription_tier_upgrades FOR EACH ROW EXECUTE FUNCTION purser.cancel_at_local_upgrade_receipt()`)
			}
			if mode == "refusal write failure" {
				exec(`CREATE FUNCTION purser.fail_upgrade_cancellation() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF NEW.state='cancelled' THEN RAISE EXCEPTION 'upgrade cancellation write unavailable' USING ERRCODE='58000'; END IF;
 RETURN NEW;
END $$`)
				exec(`CREATE TRIGGER fail_upgrade_cancellation BEFORE UPDATE ON purser.subscription_tier_upgrades FOR EACH ROW EXECUTE FUNCTION purser.fail_upgrade_cancellation()`)
			}
			if mode == "Mollie cancellation after provider receipt" {
				exec(`CREATE FUNCTION purser.hold_mollie_upgrade_boundary() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF NEW.tier_id<>OLD.tier_id THEN RAISE EXCEPTION 'hold local pricing boundary' USING ERRCODE='58000'; END IF;
 RETURN NEW;
END $$`)
				exec(`CREATE TRIGGER hold_mollie_upgrade_boundary BEFORE UPDATE ON purser.tenant_subscriptions FOR EACH ROW EXECUTE FUNCTION purser.hold_mollie_upgrade_boundary()`)
			}
			service := handlers.NewService(db, logger, nil, nil, mollieClient, stripeClient, nil)
			handlers.NewJobManager(db, logger, nil, nil, nil, nil, service, nil)
			server := &PurserServer{db: db, logger: logger, billing: service, mollieClient: mollieClient}
			request := &purserpb.ChangeBillingTierRequest{TenantId: tenant, TierId: newTier}
			response, err := server.ChangeBillingTier(ctx, request)
			if mode == "Mollie cancellation after provider receipt" {
				if err != nil {
					t.Fatal(err)
				}
				pending, readErr := purserdb.New(db).GetPendingTierUpgrade(ctx, tenant)
				if readErr != nil || pending.State != "provider_applied" {
					t.Fatalf("native update did not reach held local boundary: %+v %v", pending, readErr)
				}
				if _, cancelErr := server.CancelMollieSubscription(ctx, &purserpb.CancelMollieSubscriptionRequest{TenantId: tenant, SubscriptionId: "sub"}); cancelErr != nil {
					t.Fatal(cancelErr)
				}
				exec(`DROP TRIGGER hold_mollie_upgrade_boundary ON purser.tenant_subscriptions`)
				_, processErr := service.ProcessTierUpgrade(ctx, tenant, pending.ID)
				if processErr != nil && !errors.Is(processErr, billing.ErrTierUpgradeRejected) {
					t.Fatal(processErr)
				}
				row, readErr := purserdb.New(db).GetTierUpgrade(ctx, purserdb.GetTierUpgradeParams{TenantID: tenant, ID: pending.ID})
				fixture.mu.Lock()
				updates, charges, cancels := fixture.updates, fixture.charges, fixture.cancels
				fixture.mu.Unlock()
				if readErr != nil || row.State != "cancelled" || updates != 1 || charges != 0 || cancels != 1 {
					t.Fatalf("cancelled Mollie upgrade state=%s updates=%d charges=%d cancellations=%d err=%v", row.State, updates, charges, cancels, readErr)
				}
				var actualTier string
				var actualEnd, fallbackAt time.Time
				if readErr = db.QueryRowContext(ctx, `SELECT tier_id::text,billing_period_end,free_fallback_at FROM purser.tenant_subscriptions WHERE tenant_id=$1`, tenant).Scan(&actualTier, &actualEnd, &fallbackAt); readErr != nil || actualTier != oldTier || !actualEnd.Equal(end) || !fallbackAt.Equal(end) {
					t.Fatalf("old paid term lost: tier=%s end=%s fallback=%s err=%v", actualTier, actualEnd, fallbackAt, readErr)
				}
				return
			}
			if mode == "refusal write failure" {
				pending, readErr := purserdb.New(db).GetPendingTierUpgrade(ctx, tenant)
				if readErr != nil {
					t.Fatal(readErr)
				}
				_, processErr := service.ProcessTierUpgrade(ctx, tenant, pending.ID)
				if processErr == nil || errors.Is(processErr, billing.ErrTierUpgradeRejected) {
					t.Fatalf("rolled-back cancellation was classified as terminal: %v", processErr)
				}
				exec(`DROP TRIGGER fail_upgrade_cancellation ON purser.subscription_tier_upgrades`)
				_, processErr = service.ProcessTierUpgrade(ctx, tenant, pending.ID)
				if !errors.Is(processErr, billing.ErrTierUpgradeRejected) {
					t.Fatalf("committed refusal was not terminal: %v", processErr)
				}
				if _, readErr = purserdb.New(db).GetPendingTierUpgrade(ctx, tenant); readErr != sql.ErrNoRows {
					t.Fatalf("refusal retry retained fence: %v", readErr)
				}
				return
			}
			if mode == "advance missing invoice" || mode == "stale Stripe price" || mode == "unpaid Stripe fee" || mode == "unpaid Mollie renewal" || mode == "paid Mollie renewal before rollover" || mode == "declined upgrade with scheduled downgrade" {
				if status.Code(err) != codes.FailedPrecondition {
					t.Fatalf("uncollectable provider upgrade returned %v (%+v)", err, response)
				}
				fixture.mu.Lock()
				movedMoney := fixture.updates != 0 || fixture.charges != 0
				fixture.mu.Unlock()
				if movedMoney {
					t.Fatal("refused provider upgrade moved money")
				}
				if _, pendingErr := purserdb.New(db).GetPendingTierUpgrade(ctx, tenant); pendingErr != sql.ErrNoRows {
					t.Fatalf("definite refusal retained a pricing fence: %v", pendingErr)
				}
				if mode == "declined upgrade with scheduled downgrade" {
					var reason, tier string
					if e := db.QueryRowContext(ctx, `SELECT COALESCE(pending_reason,''),COALESCE(pending_tier_id::text,'') FROM purser.tenant_subscriptions WHERE tenant_id=$1`, tenant).Scan(&reason, &tier); e != nil || reason != "downgrade" || tier != oldTier {
						t.Fatalf("declined upgrade erased downgrade: reason=%s tier=%s err=%v", reason, tier, e)
					}
				}
				return
			}
			if err != nil && !((mode == "cancelled subscription after advance receipt" || mode == "account closure after advance receipt") && status.Code(err) == codes.FailedPrecondition) {
				t.Fatal(err)
			}
			if mode == "cancelled subscription after advance receipt" || mode == "account closure after advance receipt" {
				if status.Code(err) != codes.FailedPrecondition {
					t.Fatalf("cancelled purchase was reported as accepted: %v", err)
				}
				var upgradeState, subscriptionState string
				if readErr := db.QueryRowContext(ctx, `SELECT u.state,s.status FROM purser.subscription_tier_upgrades u JOIN purser.tenant_subscriptions s ON s.tenant_id=u.tenant_id WHERE s.tenant_id=$1`, tenant).Scan(&upgradeState, &subscriptionState); readErr != nil {
					t.Fatal(readErr)
				}
				fixture.mu.Lock()
				updates, charges := fixture.updates, fixture.charges
				fixture.mu.Unlock()
				if upgradeState != "cancelled" || subscriptionState != "cancelled" || updates != 0 || charges != 0 {
					t.Fatalf("cancelled local receipt did not settle: upgrade=%s subscription=%s updates=%d charges=%d", upgradeState, subscriptionState, updates, charges)
				}
				return
			}
			if mode == "cancelled subscription during metering wait" {
				pending, readErr := purserdb.New(db).GetPendingTierUpgrade(ctx, tenant)
				if readErr != nil {
					t.Fatal(readErr)
				}
				if _, cancelErr := server.CancelSubscription(ctx, &purserpb.CancelSubscriptionRequest{TenantId: tenant}); cancelErr != nil {
					t.Fatal(cancelErr)
				}
				exec(`INSERT INTO purser.metering_sources (source_id,region,active_from,required) VALUES ('cancel-upgrade-source','',$1,true)`, start)
				exec(`INSERT INTO purser.metering_windows (source_id,period_start,period_end,complete)
SELECT 'cancel-upgrade-source',w,w+INTERVAL '5 minutes',true FROM generate_series($1::timestamptz,$2::timestamptz,INTERVAL '5 minutes') w`, start.Truncate(5*time.Minute), time.Now().UTC().Truncate(5*time.Minute).Add(-5*time.Minute))
				done, processErr := service.ProcessTierUpgrade(ctx, tenant, pending.ID)
				if processErr != nil && !errors.Is(processErr, billing.ErrTierUpgradeRejected) {
					t.Fatal(processErr)
				}
				row, readErr := purserdb.New(db).GetTierUpgrade(ctx, purserdb.GetTierUpgradeParams{TenantID: tenant, ID: pending.ID})
				fixture.mu.Lock()
				updates, charges := fixture.updates, fixture.charges
				fixture.mu.Unlock()
				if readErr != nil || row.State != "cancelled" || done || updates != 0 || charges != 0 {
					t.Fatalf("cancelled tenant bought queued upgrade: state=%s done=%v updates=%d charges=%d err=%v", row.State, done, updates, charges, readErr)
				}
				var replacement int
				if readErr = db.QueryRowContext(ctx, `SELECT count(*) FROM purser.billing_invoices WHERE tenant_id=$1 AND base_fee_period_start >= $2`, tenant, row.RequestedAt).Scan(&replacement); readErr != nil || replacement != 0 {
					t.Fatalf("cancelled upgrade replacement invoices=%d err=%v", replacement, readErr)
				}
				return
			}
			if mode == "operator cancels metering wait" || mode == "operator completes lost Stripe response" {
				pending, e := purserdb.New(db).GetPendingTierUpgrade(ctx, tenant)
				if e != nil {
					t.Fatal(e)
				}
				operator := operatorAssignCtx(uuid.NewString())
				preview, e := server.AdminRecoverTierUpgrade(operator, &purserpb.AdminRecoverTierUpgradeRequest{TenantId: tenant, UpgradeId: pending.ID})
				if e != nil || preview.GetState() != "requested" {
					t.Fatalf("preview=%+v err=%v", preview, e)
				}
				req := &purserpb.AdminRecoverTierUpgradeRequest{TenantId: tenant, UpgradeId: pending.ID, Confirmed: true, Reason: "reconcile purchase"}
				result, e := server.AdminRecoverTierUpgrade(operator, req)
				if e != nil {
					t.Fatal(e)
				}
				want := "cancelled"
				if mode == "operator completes lost Stripe response" {
					want = "completed"
				}
				if result.GetState() != want || result.GetRequestedBy() == "" || result.GetReason() != "reconcile purchase" {
					t.Fatalf("recovery=%+v want %s", result, want)
				}
				req.Reason = "repeat cannot replace attribution"
				repeat, e := server.AdminRecoverTierUpgrade(operator, req)
				if e != nil || repeat.GetReason() != "reconcile purchase" || repeat.GetState() != want {
					t.Fatalf("recovery attribution changed: %+v %v", repeat, e)
				}
				fixture.mu.Lock()
				updates, charges := fixture.updates, fixture.charges
				fixture.mu.Unlock()
				if mode == "operator cancels metering wait" {
					if updates != 0 || charges != 0 {
						t.Fatalf("locate-only cancellation moved money: updates=%d charges=%d", updates, charges)
					}
					return
				}
				if updates != 1 || charges != 0 {
					t.Fatalf("recovery repeated purchase: updates=%d charges=%d", updates, charges)
				}
				response, err = server.ChangeBillingTier(ctx, request)
				if err != nil {
					t.Fatal(err)
				}
			}
			if mode == "metering delay" {
				fixture.mu.Lock()
				movedMoney := fixture.updates != 0 || fixture.charges != 0
				fixture.mu.Unlock()
				if movedMoney {
					t.Fatal("missing metering allowed a provider mutation")
				}
				if response.GetPendingTierId() != newTier {
					t.Fatalf("missing metering did not hold the boundary: %+v", response)
				}
				var pendingAt time.Time
				if err = db.QueryRowContext(ctx, `SELECT requested_at FROM purser.subscription_tier_upgrades WHERE tenant_id=$1`, tenant).Scan(&pendingAt); err != nil {
					t.Fatal(err)
				}
				exec(`INSERT INTO purser.metering_sources (source_id,region,active_from,required) VALUES ('upgrade-source','',$1,true)`, start)
				exec(`INSERT INTO purser.metering_windows (source_id,period_start,period_end,complete)
SELECT 'upgrade-source',w,w+INTERVAL '5 minutes',true FROM generate_series($1::timestamptz,$2::timestamptz,INTERVAL '5 minutes') w`, start.Truncate(5*time.Minute), time.Now().UTC().Truncate(5*time.Minute).Add(-5*time.Minute))
				response, err = server.ChangeBillingTier(ctx, request)
				if err != nil {
					t.Fatal(err)
				}
				var appliedAt time.Time
				if err = db.QueryRowContext(ctx, `SELECT billing_period_start FROM purser.tenant_subscriptions WHERE tenant_id=$1`, tenant).Scan(&appliedAt); err != nil || !appliedAt.Equal(pendingAt) {
					t.Fatalf("retry moved the metering boundary: at=%s requested=%s err=%v", appliedAt, pendingAt, err)
				}
			}
			if mode == "lost Stripe response" {
				if response.GetPendingTierId() != newTier {
					t.Fatalf("ambiguous update lost its fence: %+v", response)
				}
				_, grantErr := server.AdminSetBillingGrant(operatorAssignCtx(uuid.NewString()), &purserpb.AdminSetBillingGrantRequest{TenantId: tenant, WaiveUsage: true, Reason: "must wait for checkout"})
				if status.Code(grantErr) != codes.FailedPrecondition {
					t.Fatalf("grant crossed ambiguous price update: %v", grantErr)
				}
				response, err = server.ChangeBillingTier(ctx, request)
				if err != nil {
					t.Fatal(err)
				}
			}
			if response.GetAppliedTierId() != newTier {
				t.Fatalf("upgrade did not apply: %+v", response)
			}
			var actualStart, actualEnd time.Time
			if err = db.QueryRowContext(ctx, `SELECT billing_period_start,billing_period_end FROM purser.tenant_subscriptions WHERE tenant_id=$1`, tenant).Scan(&actualStart, &actualEnd); err != nil {
				t.Fatal(err)
			}
			sameSecond := mode == "Stripe same second callback"
			roundedGrant := mode == "Stripe rounded grant boundary"
			if actualStart.Before(start) || actualEnd.Before(end) ||
				actualStart.Equal(start) && !sameSecond && !roundedGrant ||
				actualEnd.Equal(end) && !sameSecond {
				t.Fatalf("new month was not installed: %s..%s", actualStart, actualEnd)
			}
			if _, err = server.ChangeBillingTier(ctx, request); err != nil {
				t.Fatal(err)
			}
			fixture.mu.Lock()
			updates, charges, amount, chargedCurrency, params := fixture.updates, fixture.charges, fixture.amount, fixture.currency, fixture.stripeParams
			fixture.mu.Unlock()
			wantUpdates, wantCharges := 1, 0
			if advance {
				wantUpdates, wantCharges = 0, 1
			}
			if strings.Contains(mode, "Mollie") {
				wantCharges = 1
			}
			if updates != wantUpdates || charges != wantCharges {
				t.Fatalf("duplicate/missing financial operation: updates=%d charges=%d", updates, charges)
			}
			if wantCharges == 1 {
				var invoice, credit string
				if err = db.QueryRowContext(ctx, `SELECT i.amount::text,(-l.amount)::text FROM purser.billing_invoices i JOIN purser.invoice_line_items l ON l.invoice_id=i.id AND l.line_key='unused_base_fee_credit' WHERE i.tenant_id=$1 AND i.base_fee_period_start=$2`, tenant, actualStart).Scan(&invoice, &credit); err != nil {
					t.Fatal(err)
				}
				if invoice != "40.00" || credit != "10.00" {
					t.Fatalf("unused paid fee did not net once: invoice=%s credit=%s", invoice, credit)
				}
				if advance && (amount != "5000" || !strings.EqualFold(chargedCurrency, "USD")) {
					t.Fatalf("wrong converted charge: %s %s", amount, chargedCurrency)
				}
				if mode == "fixed Mollie" && (amount != "40.00" || chargedCurrency != "EUR") {
					t.Fatalf("wrong net Mollie charge: %s %s", amount, chargedCurrency)
				}
			} else if params.Get("billing_cycle_anchor") != "now" || params.Get("proration_behavior") != "always_invoice" || params.Get("items[0][id]") != "item" {
				t.Fatalf("Stripe failed to reset and credit its subscription: %v", params)
			}
			if stripeSub != "" {
				oldProviderStart := start
				if mode == "Stripe rounded grant boundary" {
					oldProviderStart = start.Add(-15 * 24 * time.Hour)
				}
				_, err = subscriptions.TransitionProvider(ctx, db, subscriptions.ProviderChange{TenantID: tenant, Operation: subscriptions.StripeRenewal, Stripe: purserdb.ActivateTenantSubscriptionFromStripeParams{TenantID: tenant, CustomerID: "customer", SubscriptionID: "sub", TierID: oldTier, PeriodStart: sql.NullTime{Time: oldProviderStart, Valid: true}, PeriodEnd: sql.NullTime{Time: end, Valid: true}}})
				if err != nil {
					t.Fatal(err)
				}
				var tier string
				if err = db.QueryRowContext(ctx, `SELECT tier_id::text FROM purser.tenant_subscriptions WHERE tenant_id=$1`, tenant).Scan(&tier); err != nil || tier != newTier {
					t.Fatalf("old Stripe event rewound the purchased tier: tier=%s err=%v", tier, err)
				}
				if mode == "Stripe same second callback" {
					result, currentErr := subscriptions.TransitionProvider(ctx, db, subscriptions.ProviderChange{TenantID: tenant, Operation: subscriptions.StripeRenewal, Stripe: purserdb.ActivateTenantSubscriptionFromStripeParams{TenantID: tenant, CustomerID: "customer", SubscriptionID: "sub", TierID: newTier, PeriodStart: sql.NullTime{Time: start, Valid: true}, PeriodEnd: sql.NullTime{Time: end, Valid: true}}})
					if currentErr != nil || result.Rows != 1 {
						t.Fatalf("current Stripe callback was refused: %+v %v", result, currentErr)
					}
					return
				}
				result, statusErr := subscriptions.TransitionProvider(ctx, db, subscriptions.ProviderChange{TenantID: tenant, Operation: subscriptions.StripeStatus, StripeStatus: purserdb.UpdateTenantStripeSubscriptionStatusParams{TenantID: tenant, SubscriptionID: "sub", Status: "cancelled", StripeStatus: sql.NullString{String: "unpaid", Valid: true}, PeriodStart: sql.NullTime{Time: oldProviderStart, Valid: true}, PeriodEnd: sql.NullTime{Time: end, Valid: true}}})
				if statusErr != nil || result.Rows != 0 {
					t.Fatalf("old Stripe status event was admitted: %+v %v", result, statusErr)
				}
				if mode == "fixed Stripe" {
					missingPeriod := subscriptions.ProviderChange{TenantID: tenant, Operation: subscriptions.StripeStatus, StripeStatus: purserdb.UpdateTenantStripeSubscriptionStatusParams{TenantID: tenant, SubscriptionID: "sub", Status: "past_due", StripeStatus: sql.NullString{String: "past_due", Valid: true}}}
					old, err := subscriptions.TransitionProvider(subscriptions.WithProviderCompletion(ctx, start), db, missingPeriod)
					if err != nil || old.Rows != 0 {
						t.Fatalf("old dateless status crossed upgrade: %+v %v", old, err)
					}
					current, err := subscriptions.TransitionProvider(subscriptions.WithProviderCompletion(ctx, time.Now().UTC()), db, missingPeriod)
					if err != nil || current.Rows != 1 {
						t.Fatalf("fresh authenticated dateless status was dropped: %+v %v", current, err)
					}
				}
			}
		})
	}
}

func TestTierUpgradeProviders_RealYugabyte(t *testing.T) { TestTierUpgradeProviders_RealPG(t) }
