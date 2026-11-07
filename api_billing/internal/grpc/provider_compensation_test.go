package grpc

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"frameworks/api_billing/internal/appconfig/appconfigtest"
	billingmollie "frameworks/api_billing/internal/mollie"
	billingstripe "frameworks/api_billing/internal/stripe"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	commodorepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"
	foghorncontrolpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/foghorn_control"
	purserpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/purser"
	mollielib "github.com/VictorAvelar/mollie-api-go/v4/mollie"
	stripelib "github.com/stripe/stripe-go/v85"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// expectCollectionProfile answers the tenant presentment-currency lookup.
func expectCollectionProfile(mock sqlmock.Sqlmock, tenantID, presentmentCurrency string) {
	mock.ExpectQuery(`-- name: GetTenantCollectionProfile`).
		WithArgs(tenantID).
		WillReturnRows(sqlmock.NewRows([]string{
			"presentment_currency", "status", "billing_model", "payment_method", "stripe_customer_id",
			"stripe_subscription_id", "mollie_subscription_id", "billing_period_start", "billing_period_end", "tier_id",
		}).AddRow(presentmentCurrency, "active", "postpaid", nil, nil, nil, nil, nil, nil, "11111111-1111-1111-1111-111111111111"))
}

func TestCreateCheckoutSessionExpiresStripeSessionWhenLocalStageFails(t *testing.T) {
	for key, value := range map[string]string{"SUPPLIER_NAME": "DDVTech B.V.", "SUPPLIER_ADDRESS": "Amsterdam, NL", "SUPPLIER_VAT_NUMBER": "NL123456789B01", "SUPPLIER_REGISTRATION_NUMBER": "12345678", "SUPPLIER_COUNTRY": "NL"} {
		appconfigtest.Set(t, key, value)
	}

	mockDB, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer mockDB.Close()

	stripeClient := &fakeStripeBillingClient{
		customer: &stripelib.Customer{ID: "cus_123"},
		session:  &stripelib.CheckoutSession{ID: "cs_test_123", URL: "https://checkout.stripe.test/cs_test_123"},
	}
	server := &PurserServer{
		db:              mockDB,
		logger:          logging.NewLogger(),
		stripeClient:    stripeClient,
		commodoreClient: &fakeCommodorePrimaryUser{},
	}

	tierID := "11111111-1111-1111-1111-111111111111"
	mock.ExpectQuery(`SELECT billing_email, billing_name, billing_company, tax_id,\s+COALESCE\(billing_address`).
		WithArgs("tenant-a").
		WillReturnRows(sqlmock.NewRows([]string{"billing_email", "billing_name", "billing_company", "tax_id", "billing_address", "updated_at", "presentment_currency", "customer_type", "tax_review_required"}).
			AddRow("billing@example.com", "Example Customer", "Example", nil, []byte(`{"street":"Main 1","city":"Amsterdam","postal_code":"1000AA","country":"NL"}`), time.Now(), "EUR", "unspecified", false))
	expectProviderCheckoutWithoutGrant(mock, "tenant-a")
	expectSelfServiceCheckout(mock, "tenant-a", tierID, "stripe")
	mock.ExpectQuery(`SELECT tier_name, COALESCE\(currency, 'EUR'\)::text AS currency`).
		WithArgs(false, tierID).
		WillReturnRows(sqlmock.NewRows([]string{"tier_name", "currency", "stripe_price_id_monthly"}).AddRow("Pro", "USD", "price_123"))
	expectCollectionProfile(mock, "tenant-a", "EUR")
	mock.ExpectQuery(`SELECT pending_reason, COALESCE\(pending_tier_id::text, ''\)::text AS pending_tier_id`).
		WithArgs("tenant-a").
		WillReturnRows(sqlmock.NewRows([]string{"pending_reason", "pending_tier_id"}).AddRow(nil, ""))
	// Durable intent insert before any Stripe API call.
	mock.ExpectBegin()
	expectProviderSubscriptionLock(mock, "tenant-a")
	mock.ExpectQuery("GetSubscriptionOperatorGrant").WithArgs("tenant-a").WillReturnError(sql.ErrNoRows)
	mock.ExpectQuery("HasPendingTierUpgrade").WillReturnRows(sqlmock.NewRows([]string{"pending"}).AddRow(false))
	mock.ExpectQuery(`INSERT INTO purser\.payment_provider_intents`).
		WithArgs("tenant-a", tierID, "USD", "stripe-tenant-checkout:tenant-a:"+tierID).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow("intent-tenant"))
	mock.ExpectQuery("GetPendingProviderCheckout").WillReturnRows(sqlmock.NewRows([]string{"pending", "deadline", "kind"}).AddRow(false, time.Unix(0, 0), "checkout"))
	mock.ExpectExec("EnqueueCheckoutReservationExpiry").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	mock.ExpectQuery("GetProviderCheckoutReservationKey").WillReturnRows(sqlmock.NewRows([]string{"idempotency_key"}).AddRow("reserved-key"))
	mock.ExpectQuery("GetProviderCheckoutReservation").WillReturnRows(checkoutReservationTestRows("intent", "tenant-a", "stripe", "tenant_subscription_checkout", nil, nil, nil, nil, "pending", time.Now().Add(24*time.Hour), 0))

	// Record provider_customer_id once Stripe returns the customer.
	mock.ExpectExec(`UPDATE purser\.payment_provider_intents\s+SET provider_customer_id`).
		WithArgs("cus_123", "intent-tenant", "tenant-a").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery("SELECT billing_email, billing_name, billing_company").WithArgs("tenant-a").WillReturnRows(sqlmock.NewRows([]string{"billing_email", "billing_name", "billing_company", "billing_address", "tax_id", "customer_type"}).AddRow("billing@example.com", "Example Customer", "Example", []byte(`{"street":"Main 1","city":"Amsterdam","postal_code":"1000AA","country":"NL"}`), nil, "consumer"))
	mock.ExpectQuery("SELECT rate_bps, source, source_checked_on::text AS source_checked_on").WillReturnRows(sqlmock.NewRows([]string{"rate_bps", "source", "source_checked_on", "effective_from"}).AddRow(2100, "Dutch VAT", "2026-01-01", "2026-01-01"))
	mock.ExpectExec("-- name: StoreProviderTaxProfile").WithArgs("tenant-a", sqlmock.AnyArg(), sqlmock.AnyArg()).WillReturnResult(sqlmock.NewResult(0, 1))
	expectProviderRequestJournal(mock, "tenant-a", "stripe_checkout", "test-stripe")
	// Record provider_session_id once Stripe returns the session.
	mock.ExpectExec(`UPDATE purser\.payment_provider_intents\s+SET provider_session_id`).
		WithArgs("cs_test_123", "intent-tenant", "tenant-a").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`UPDATE purser\.tenant_subscriptions\s+SET pending_tier_id = \$1::text::uuid`).
		WithArgs(tierID, "intent-tenant", "tenant-a").
		WillReturnError(errors.New("db down"))

	mock.ExpectExec("FinishProviderCheckoutIntent").WillReturnResult(sqlmock.NewResult(0, 1))

	_, err = server.CreateCheckoutSession(context.Background(), &purserpb.CreateStripeCheckoutRequest{
		TenantId:      "tenant-a",
		TierId:        tierID,
		BillingPeriod: "monthly",
		SuccessUrl:    "https://app.test/success",
		CancelUrl:     "https://app.test/cancel",
	})
	if err == nil {
		t.Fatal("expected staging failure")
	}
	if len(stripeClient.expiredSessions) != 1 || stripeClient.expiredSessions[0] != "cs_test_123" {
		t.Fatalf("expired sessions = %#v, want [cs_test_123]; err=%v", stripeClient.expiredSessions, err)
	}
	if stripeClient.checkoutKey != "reserved-key" {
		t.Fatalf("provider did not receive reserved idempotency key: %s", stripeClient.checkoutKey)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet sqlmock expectations: %v", err)
	}
}

func TestCreateMollieSubscriptionCancelsProviderSubscriptionWhenLocalPersistFails(t *testing.T) {
	for key, value := range map[string]string{"SUPPLIER_NAME": "DDVTech B.V.", "SUPPLIER_ADDRESS": "Amsterdam NL", "SUPPLIER_VAT_NUMBER": "NL123456789B01", "SUPPLIER_REGISTRATION_NUMBER": "12345678", "SUPPLIER_COUNTRY": "NL"} {
		appconfigtest.Set(t, key, value)
	}
	mockDB, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer mockDB.Close()

	mollieClient := &fakeMollieBillingClient{
		subscription: &mollielib.Subscription{ID: "sub_123"},
	}
	server := &PurserServer{
		db:           mockDB,
		logger:       logging.NewLogger(),
		mollieClient: mollieClient,
	}

	tierID := "11111111-1111-1111-1111-111111111111"
	mock.ExpectQuery(`SELECT billing_email, billing_name, billing_company, tax_id,\s+COALESCE\(billing_address`).
		WithArgs("tenant-a").
		WillReturnRows(sqlmock.NewRows([]string{"billing_email", "billing_name", "billing_company", "tax_id", "billing_address", "updated_at", "presentment_currency", "customer_type", "tax_review_required"}).
			AddRow("billing@example.com", "Example Customer", "Example", nil, []byte(`{"street":"Main 1","city":"Amsterdam","postal_code":"1000AA","country":"NL"}`), time.Now(), "EUR", "unspecified", false))
	expectCollectionProfile(mock, "tenant-a", "EUR")
	expectProviderCheckoutWithoutGrant(mock, "tenant-a")
	expectSelfServiceCheckout(mock, "tenant-a", tierID, "mollie")
	mock.ExpectQuery(`SELECT EXISTS\(`).
		WithArgs("tenant-a").
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(true))
	mock.ExpectQuery(`SELECT mollie_customer_id FROM purser\.mollie_customers WHERE tenant_id = \$1`).
		WithArgs("tenant-a").
		WillReturnRows(sqlmock.NewRows([]string{"mollie_customer_id"}).AddRow("cst_123"))
	mock.ExpectQuery(`SELECT tier_name, COALESCE\(base_price::text, '0'\)::text AS base_price`).
		WithArgs(tierID).
		WillReturnRows(sqlmock.NewRows([]string{"tier_name", "base_price", "currency"}).AddRow("Pro", "20.00", "EUR"))
	// Durable subscription-intent insert before the Mollie API call.
	mock.ExpectBegin()
	expectProviderSubscriptionLock(mock, "tenant-a")
	mock.ExpectQuery("GetSubscriptionOperatorGrant").WithArgs("tenant-a").WillReturnError(sql.ErrNoRows)
	mock.ExpectQuery("HasPendingTierUpgrade").WillReturnRows(sqlmock.NewRows([]string{"pending"}).AddRow(false))
	mock.ExpectQuery(`INSERT INTO purser\.payment_provider_intents`).
		WithArgs("tenant-a", tierID, "cst_123", "EUR", int64(2000), "mollie-subscription:tenant-a:"+tierID+":mdt_123").
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow("intent-mollie-sub"))
	mock.ExpectExec("LinkMollieCheckoutFunding").WithArgs("mdt_123", "tenant-a", "intent-mollie-sub").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery("GetProviderCheckoutReservation").WillReturnRows(checkoutReservationRowsWithFunding(false, "funding", "intent-mollie-sub", "tenant-a", "mollie", "mollie_subscription_create", "cst_123", nil, nil, nil, "pending", time.Now().Add(time.Hour), 0))
	mock.ExpectQuery("GetProviderCheckoutReservation").WithArgs("tenant-a", "funding").WillReturnRows(checkoutReservationTestRows("funding", "tenant-a", "mollie", "mollie_first_payment", "cst_123", nil, "paid-first", nil, "succeeded", time.Now().Add(time.Hour), 0))
	mock.ExpectQuery("MollieCheckoutFundingMatches").WithArgs("tenant-a", "intent-mollie-sub").WillReturnRows(sqlmock.NewRows([]string{"matches"}).AddRow(true))
	mock.ExpectQuery("GetPendingProviderCheckout").WillReturnRows(sqlmock.NewRows([]string{"pending", "deadline", "kind"}).AddRow(false, time.Unix(0, 0), "checkout"))
	mock.ExpectExec("EnqueueCheckoutReservationExpiry").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	mock.ExpectQuery("GetProviderCheckoutReservationKey").WillReturnRows(sqlmock.NewRows([]string{"idempotency_key"}).AddRow("reserved-key"))
	mock.ExpectQuery("GetProviderCheckoutReservation").WillReturnRows(checkoutReservationTestRows("intent", "tenant-a", "stripe", "tenant_subscription_checkout", nil, nil, nil, nil, "pending", time.Now().Add(24*time.Hour), 0))

	mock.ExpectQuery("SELECT billing_email, billing_name, billing_company").WithArgs("tenant-a").WillReturnRows(sqlmock.NewRows([]string{"billing_email", "billing_name", "billing_company", "billing_address", "tax_id", "customer_type"}).AddRow("billing@example.com", "Example Customer", "Example", []byte(`{"street":"Main 1","city":"Amsterdam","postal_code":"1000AA","country":"NL"}`), nil, "consumer"))
	mock.ExpectQuery("SELECT rate_bps, source, source_checked_on::text AS source_checked_on").WillReturnRows(sqlmock.NewRows([]string{"rate_bps", "source", "source_checked_on", "effective_from"}).AddRow(2100, "Dutch VAT", "2026-01-01", "2026-01-01"))
	mock.ExpectExec("-- name: StoreProviderTaxProfile").WithArgs("tenant-a", sqlmock.AnyArg(), sqlmock.AnyArg()).WillReturnResult(sqlmock.NewResult(0, 1))
	expectProviderRequestJournal(mock, "tenant-a", "mollie_subscription", "test-mollie")
	// Record provider_subscription_id once Mollie returns the subscription.
	mock.ExpectBegin()
	expectProviderSubscriptionLock(mock, "tenant-a")
	mock.ExpectQuery("GetSubscriptionOperatorGrant").WithArgs("tenant-a").WillReturnError(sql.ErrNoRows)
	mock.ExpectQuery("GetProviderCheckoutReservation").WillReturnRows(checkoutReservationRowsWithFunding(false, "funding", "intent-mollie-sub", "tenant-a", "mollie", "mollie_subscription_create", "cst_123", nil, nil, nil, "pending", time.Now().Add(24*time.Hour), 0))
	mock.ExpectQuery("GetProviderCheckoutReservation").WithArgs("tenant-a", "funding").WillReturnRows(checkoutReservationTestRows("funding", "tenant-a", "mollie", "mollie_first_payment", "cst_123", nil, "paid-first", nil, "succeeded", time.Now().Add(time.Hour), 0))
	mock.ExpectQuery("MollieCheckoutFundingMatches").WithArgs("tenant-a", "intent-mollie-sub").WillReturnRows(sqlmock.NewRows([]string{"matches"}).AddRow(true))
	mock.ExpectQuery("HasPendingTierUpgrade").WillReturnRows(sqlmock.NewRows([]string{"pending"}).AddRow(false))
	expectSelfServiceCheckout(mock, "tenant-a", tierID, "mollie")
	mock.ExpectExec(`UPDATE purser\.tenant_subscriptions\s+SET mollie_subscription_id = \$1`).
		WillReturnError(errors.New("db down"))
	mock.ExpectRollback()

	mock.ExpectExec("FinishProviderCheckoutIntent").WillReturnResult(sqlmock.NewResult(0, 1))

	_, err = server.CreateMollieSubscription(context.Background(), &purserpb.CreateMollieSubscriptionRequest{
		TenantId:  "tenant-a",
		TierId:    tierID,
		MandateId: "mdt_123",
	})
	if err == nil {
		t.Fatal("expected local persist failure")
	}
	if len(mollieClient.cancelledSubscriptions) != 1 || mollieClient.cancelledSubscriptions[0] != "cst_123/sub_123" {
		t.Fatalf("cancelled subscriptions = %#v, want [cst_123/sub_123]; err=%v", mollieClient.cancelledSubscriptions, err)
	}
	if mollieClient.checkoutKey != "reserved-key" {
		t.Fatalf("provider did not receive reserved idempotency key: %s", mollieClient.checkoutKey)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet sqlmock expectations: %v", err)
	}
}

type fakeStripeBillingClient struct {
	customer        *stripelib.Customer
	session         *stripelib.CheckoutSession
	expiredSessions []string
	checkoutKey     string
}

func (f *fakeStripeBillingClient) PrepareCustomerTax(context.Context, string, string, billingstripe.TaxProfile) (string, error) {
	return "txr_test", nil
}

func (f *fakeStripeBillingClient) CreateOrGetCustomer(context.Context, billingstripe.CustomerInfo) (*stripelib.Customer, error) {
	return f.customer, nil
}

func (f *fakeStripeBillingClient) CreateCheckoutSession(_ context.Context, params billingstripe.CheckoutSessionParams) (*stripelib.CheckoutSession, error) {
	f.checkoutKey = params.IdempotencyKey
	return f.session, nil
}

func (f *fakeStripeBillingClient) CreateSetupCheckoutSession(context.Context, billingstripe.SetupCheckoutParams) (*stripelib.CheckoutSession, error) {
	return nil, errors.New("unexpected setup checkout")
}

func (f *fakeStripeBillingClient) ExpireCheckoutSession(_ context.Context, sessionID string) error {
	f.expiredSessions = append(f.expiredSessions, sessionID)
	return nil
}

func (f *fakeStripeBillingClient) CreateBillingPortalSession(context.Context, string, string) (*stripelib.BillingPortalSession, error) {
	panic("unexpected CreateBillingPortalSession call")
}

func (f *fakeStripeBillingClient) GetSubscription(context.Context, string) (*stripelib.Subscription, error) {
	panic("unexpected GetSubscription call")
}

func (f *fakeStripeBillingClient) CancelSubscription(context.Context, string) (*stripelib.Subscription, error) {
	panic("unexpected CancelSubscription call")
}

func (f *fakeStripeBillingClient) ExtractSubscriptionInfo(*stripelib.Subscription) billingstripe.SubscriptionInfo {
	panic("unexpected ExtractSubscriptionInfo call")
}

type fakeMollieBillingClient struct {
	subscription           *mollielib.Subscription
	cancelledSubscriptions []string
	checkoutKey            string
}

func (f *fakeMollieBillingClient) CreateOrGetCustomer(context.Context, billingmollie.CustomerInfo) (*mollielib.Customer, error) {
	panic("unexpected CreateOrGetCustomer call")
}

func (f *fakeMollieBillingClient) CreateFirstPayment(context.Context, billingmollie.FirstPaymentParams) (*mollielib.Payment, error) {
	panic("unexpected CreateFirstPayment call")
}

func (f *fakeMollieBillingClient) ListMandates(context.Context, string) ([]*mollielib.Mandate, error) {
	panic("unexpected ListMandates call")
}

func (f *fakeMollieBillingClient) GetMandate(_ context.Context, _ string, mandateID string) (*mollielib.Mandate, error) {
	return &mollielib.Mandate{ID: mandateID, Status: "valid"}, nil
}

func (f *fakeMollieBillingClient) CreateSubscription(_ context.Context, params billingmollie.SubscriptionParams) (*mollielib.Subscription, error) {
	f.checkoutKey = params.IdempotencyKey
	return f.subscription, nil
}

func (f *fakeMollieBillingClient) CancelSubscription(_ context.Context, customerID, subscriptionID string) error {
	f.cancelledSubscriptions = append(f.cancelledSubscriptions, customerID+"/"+subscriptionID)
	return nil
}

func (f *fakeMollieBillingClient) GetSubscription(context.Context, string, string) (*mollielib.Subscription, error) {
	panic("unexpected GetSubscription call")
}

func (f *fakeMollieBillingClient) ExtractSubscriptionInfo(*mollielib.Subscription, string) billingmollie.SubscriptionInfo {
	panic("unexpected ExtractSubscriptionInfo call")
}

func (f *fakeMollieBillingClient) ExtractMandateInfo(*mollielib.Mandate, string) billingmollie.MandateInfo {
	panic("unexpected ExtractMandateInfo call")
}

type fakeCommodorePrimaryUser struct{}

func (f *fakeCommodorePrimaryUser) TerminateTenantStreams(context.Context, string, string) (*foghorncontrolpb.TerminateTenantStreamsResponse, error) {
	panic("unexpected TerminateTenantStreams call")
}

func (f *fakeCommodorePrimaryUser) InvalidateTenantCache(context.Context, string, string) (*foghorncontrolpb.InvalidateTenantCacheResponse, error) {
	panic("unexpected InvalidateTenantCache call")
}

func (f *fakeCommodorePrimaryUser) GetTenantUserCount(context.Context, string) (*commodorepb.GetTenantUserCountResponse, error) {
	panic("unexpected GetTenantUserCount call")
}

func (f *fakeCommodorePrimaryUser) GetTenantPrimaryUser(context.Context, string) (*commodorepb.GetTenantPrimaryUserResponse, error) {
	return &commodorepb.GetTenantPrimaryUserResponse{
		Email: "billing@example.com",
		Name:  "Billing User",
	}, nil
}

func TestCreateMollieSubscriptionRefusesOutstandingGrantBeforeProvider(t *testing.T) {
	for _, active := range []bool{true, false} {
		t.Run(map[bool]string{true: "active", false: "awaiting expiry"}[active], func(t *testing.T) {
			db, mock, dbErr := sqlmock.New()
			if dbErr != nil {
				t.Fatal(dbErr)
			}
			defer db.Close()
			server := &PurserServer{db: db, logger: logging.NewLogger(), mollieClient: &fakeMollieBillingClient{}}
			mock.ExpectQuery(`SELECT billing_email, billing_name, billing_company, tax_id,\s+COALESCE\(billing_address`).WithArgs("tenant-a").WillReturnRows(sqlmock.NewRows([]string{"billing_email", "billing_name", "billing_company", "tax_id", "billing_address", "updated_at", "presentment_currency", "customer_type", "tax_review_required"}).AddRow("billing@example.com", "Example", "Example", nil, []byte(`{"street":"Main 1","city":"Amsterdam","postal_code":"1000AA","country":"NL"}`), time.Now(), "EUR", "unspecified", false))
			expectCollectionProfile(mock, "tenant-a", "EUR")
			mock.ExpectBegin()
			expectProviderSubscriptionLock(mock, "tenant-a")
			mock.ExpectQuery(`-- name: GetSubscriptionOperatorGrant`).WithArgs("tenant-a").WillReturnRows(sqlmock.NewRows([]string{"subscription_id", "base_price", "waive_usage", "collection", "expires_at", "reason", "granted_by", "granted_at", "fallback_tier_id", "assigned_tier_id", "fallback_tier_name", "active", "expiry_error", "expiry_failures"}).AddRow("sub", nil, true, "provider", time.Now().Add(-time.Minute), "trial", nil, time.Now().Add(-time.Hour), "fallback", "tier", "payg", active, nil, int32(0)))
			mock.ExpectQuery("HasPendingTierUpgrade").WillReturnRows(sqlmock.NewRows([]string{"pending"}).AddRow(false))
			mock.ExpectRollback()
			mock.ExpectBegin()
			expectProviderSubscriptionLock(mock, "tenant-a")
			mock.ExpectQuery("GetSubscriptionOperatorGrant").WithArgs("tenant-a").WillReturnError(sql.ErrNoRows)
			mock.ExpectQuery("ListRefusedMollieFundingIntents").WillReturnRows(sqlmock.NewRows([]string{"id"}))
			mock.ExpectCommit()
			_, callErr := server.CreateMollieSubscription(t.Context(), &purserpb.CreateMollieSubscriptionRequest{TenantId: "tenant-a", TierId: "11111111-1111-1111-1111-111111111111", MandateId: "mdt_123"})
			assertTenantRefusal(t, callErr, RefusalOutstandingGrant)
			if unmet := mock.ExpectationsWereMet(); unmet != nil {
				t.Fatal(unmet)
			}
		})
	}
}

func TestStripeCheckoutRefusesOutstandingGrantBeforeProvider(t *testing.T) {
	for _, active := range []bool{true, false} {
		t.Run(map[bool]string{true: "active", false: "awaiting expiry"}[active], func(t *testing.T) {
			db, mock, dbErr := sqlmock.New()
			if dbErr != nil {
				t.Fatal(dbErr)
			}
			defer db.Close()
			server := &PurserServer{db: db, logger: logging.NewLogger(), stripeClient: &fakeStripeBillingClient{}}
			mock.ExpectQuery(`SELECT billing_email, billing_name, billing_company, tax_id,\s+COALESCE\(billing_address`).WithArgs("tenant-a").WillReturnRows(sqlmock.NewRows([]string{"billing_email", "billing_name", "billing_company", "tax_id", "billing_address", "updated_at", "presentment_currency", "customer_type", "tax_review_required"}).AddRow("billing@example.com", "Example", "Example", nil, []byte(`{"street":"Main 1","city":"Amsterdam","postal_code":"1000AA","country":"NL"}`), time.Now(), "EUR", "unspecified", false))
			mock.ExpectBegin()
			expectProviderSubscriptionLock(mock, "tenant-a")
			mock.ExpectQuery(`-- name: GetSubscriptionOperatorGrant`).WithArgs("tenant-a").WillReturnRows(sqlmock.NewRows([]string{"subscription_id", "base_price", "waive_usage", "collection", "expires_at", "reason", "granted_by", "granted_at", "fallback_tier_id", "assigned_tier_id", "fallback_tier_name", "active", "expiry_error", "expiry_failures"}).AddRow("sub", nil, true, "provider", time.Now().Add(-time.Minute), "trial", nil, time.Now().Add(-time.Hour), "fallback", "tier", "payg", active, nil, int32(0)))
			mock.ExpectQuery("HasPendingTierUpgrade").WillReturnRows(sqlmock.NewRows([]string{"pending"}).AddRow(false))
			mock.ExpectRollback()
			_, callErr := server.CreateCheckoutSession(t.Context(), &purserpb.CreateStripeCheckoutRequest{TenantId: "tenant-a", TierId: "11111111-1111-1111-1111-111111111111", SuccessUrl: "https://example.test/ok", CancelUrl: "https://example.test/cancel"})
			assertTenantRefusal(t, callErr, RefusalOutstandingGrant)
			if unmet := mock.ExpectationsWereMet(); unmet != nil {
				t.Fatal(unmet)
			}
		})
	}
}

func expectProviderSubscriptionLock(mock sqlmock.Sqlmock, tenant string) {
	mock.ExpectQuery("LockTenantSubscriptionForProviderChange").WithArgs(tenant).WillReturnRows(sqlmock.NewRows([]string{"subscription_id", "tier_id", "status", "billing_model", "stripe_subscription_id", "mollie_subscription_id", "pending_reason", "free_fallback_provider_stopping", "free_fallback_provider_ended_at"}).AddRow("sub-row", "tier", "active", "postpaid", nil, nil, nil, false, nil))
	mock.ExpectQuery("IsTenantBillingClosed").WillReturnRows(sqlmock.NewRows([]string{"closed"}).AddRow(false))
}

func expectProviderCheckoutWithoutGrant(mock sqlmock.Sqlmock, tenant string) {
	mock.ExpectBegin()
	expectProviderSubscriptionLock(mock, tenant)
	mock.ExpectQuery("GetSubscriptionOperatorGrant").WithArgs(tenant).WillReturnError(sql.ErrNoRows)
	mock.ExpectQuery("HasPendingTierUpgrade").WillReturnRows(sqlmock.NewRows([]string{"pending"}).AddRow(false))
	mock.ExpectCommit()
	expectSettledOnFree(mock)
}

func TestCreateFirstPaymentRefusesOutstandingGrantBeforeProvider(t *testing.T) {
	db, mock, dbErr := sqlmock.New()
	if dbErr != nil {
		t.Fatal(dbErr)
	}
	defer db.Close()
	server := &PurserServer{db: db, logger: logging.NewLogger(), mollieClient: &fakeMollieBillingClient{}}
	mock.ExpectQuery(`SELECT billing_email, billing_name, billing_company, tax_id,\s+COALESCE\(billing_address`).WithArgs("tenant-a").WillReturnRows(sqlmock.NewRows([]string{"billing_email", "billing_name", "billing_company", "tax_id", "billing_address", "updated_at", "presentment_currency", "customer_type", "tax_review_required"}).AddRow("billing@example.com", "Example", "Example", nil, []byte(`{"street":"Main 1","city":"Amsterdam","postal_code":"1000AA","country":"NL"}`), time.Now(), "USD", "unspecified", false))
	mock.ExpectBegin()
	expectProviderSubscriptionLock(mock, "tenant-a")
	mock.ExpectQuery("GetSubscriptionOperatorGrant").WithArgs("tenant-a").WillReturnRows(sqlmock.NewRows([]string{"subscription_id", "base_price", "waive_usage", "collection", "expires_at", "reason", "granted_by", "granted_at", "fallback_tier_id", "assigned_tier_id", "fallback_tier_name", "active", "expiry_error", "expiry_failures"}).AddRow("sub", nil, true, "provider", nil, "trial", nil, time.Now(), "", "tier", "", true, nil, int32(0)))
	mock.ExpectQuery("HasPendingTierUpgrade").WillReturnRows(sqlmock.NewRows([]string{"pending"}).AddRow(false))
	mock.ExpectRollback()
	_, callErr := server.CreateFirstPayment(t.Context(), &purserpb.CreateMollieFirstPaymentRequest{TenantId: "tenant-a", TierId: "tier", Method: "creditcard", RedirectUrl: "https://example.test/return"})
	if status.Code(callErr) != codes.FailedPrecondition {
		t.Fatalf("first-payment grant refusal=%v", callErr)
	}
	if unmet := mock.ExpectationsWereMet(); unmet != nil {
		t.Fatal(unmet)
	}
}

func (*fakeStripeBillingClient) CredentialFingerprint() string { return "test-stripe" }
func (*fakeMollieBillingClient) CredentialFingerprint() string { return "test-mollie" }

func expectProviderRequestJournal(mock sqlmock.Sqlmock, tenant, kind, credential string) {
	mock.ExpectBegin()
	expectProviderSubscriptionLock(mock, tenant)
	mock.ExpectQuery("GetSubscriptionOperatorGrant").WithArgs(tenant).WillReturnError(sql.ErrNoRows)
	mock.ExpectQuery("HasPendingTierUpgrade").WillReturnRows(sqlmock.NewRows([]string{"pending"}).AddRow(false))
	raw, _ := json.Marshal(map[string]any{"kind": kind, "credential": credential, "replay_before": time.Now().Add(time.Minute), "params": map[string]any{"IdempotencyKey": "reserved-key"}})
	mock.ExpectQuery("SaveCheckoutProviderRequest").WillReturnRows(sqlmock.NewRows([]string{"provider_request", "submission_state"}).AddRow(raw, "submitted"))
	mock.ExpectCommit()
}
