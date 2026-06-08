package grpc

import (
	"context"
	"errors"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	commodorepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"
	foghorncontrolpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/foghorn_control"
	purserpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/purser"
	mollielib "github.com/VictorAvelar/mollie-api-go/v4/mollie"
	stripelib "github.com/stripe/stripe-go/v85"

	billingmollie "frameworks/api_billing/internal/mollie"
	billingstripe "frameworks/api_billing/internal/stripe"
)

func TestCreateCheckoutSessionExpiresStripeSessionWhenLocalStageFails(t *testing.T) {
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
	mock.ExpectQuery(`SELECT tier_name, currency, stripe_price_id_monthly FROM purser\.billing_tiers WHERE id = \$1`).
		WithArgs(tierID).
		WillReturnRows(sqlmock.NewRows([]string{"tier_name", "currency", "stripe_price_id_monthly"}).AddRow("Pro", "USD", "price_123"))
	mock.ExpectQuery(`SELECT pending_reason, pending_tier_id::text\s+FROM purser\.tenant_subscriptions`).
		WithArgs("tenant-a").
		WillReturnRows(sqlmock.NewRows([]string{"pending_reason", "pending_tier_id"}).AddRow(nil, nil))
	// Durable intent insert before any Stripe API call.
	mock.ExpectQuery(`INSERT INTO purser\.payment_provider_intents`).
		WithArgs("tenant-a", tierID, "USD", "stripe-tenant-checkout:tenant-a:"+tierID).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow("intent-tenant"))
	// Record provider_customer_id once Stripe returns the customer.
	mock.ExpectExec(`UPDATE purser\.payment_provider_intents\s+SET provider_customer_id`).
		WithArgs("cus_123", "intent-tenant").
		WillReturnResult(sqlmock.NewResult(0, 1))
	// Record provider_session_id once Stripe returns the session.
	mock.ExpectExec(`UPDATE purser\.payment_provider_intents\s+SET provider_session_id`).
		WithArgs("cs_test_123", "intent-tenant").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`UPDATE purser\.tenant_subscriptions\s+SET pending_tier_id = \$1::uuid`).
		WithArgs(tierID, "tenant-a", "intent-tenant").
		WillReturnError(errors.New("db down"))

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
		t.Fatalf("expired sessions = %#v, want [cs_test_123]", stripeClient.expiredSessions)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet sqlmock expectations: %v", err)
	}
}

func TestCreateMollieSubscriptionCancelsProviderSubscriptionWhenLocalPersistFails(t *testing.T) {
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
	mock.ExpectQuery(`SELECT 1 FROM purser\.tenant_subscriptions WHERE tenant_id = \$1`).
		WithArgs("tenant-a").
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(1))
	mock.ExpectQuery(`SELECT mollie_customer_id FROM purser\.mollie_customers WHERE tenant_id = \$1`).
		WithArgs("tenant-a").
		WillReturnRows(sqlmock.NewRows([]string{"mollie_customer_id"}).AddRow("cst_123"))
	mock.ExpectQuery(`SELECT tier_name, base_price::text, currency FROM purser\.billing_tiers WHERE id = \$1`).
		WithArgs(tierID).
		WillReturnRows(sqlmock.NewRows([]string{"tier_name", "base_price", "currency"}).AddRow("Pro", "20.00", "EUR"))
	// Durable subscription-intent insert before the Mollie API call.
	mock.ExpectQuery(`INSERT INTO purser\.payment_provider_intents`).
		WithArgs("tenant-a", tierID, "cst_123", "EUR", int64(2000), "mollie-subscription:tenant-a:"+tierID+":mdt_123").
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow("intent-mollie-sub"))
	// Record provider_subscription_id once Mollie returns the subscription.
	mock.ExpectExec(`UPDATE purser\.payment_provider_intents\s+SET provider_subscription_id`).
		WithArgs("sub_123", "intent-mollie-sub").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`UPDATE purser\.tenant_subscriptions\s+SET mollie_subscription_id = \$1`).
		WillReturnError(errors.New("db down"))

	_, err = server.CreateMollieSubscription(context.Background(), &purserpb.CreateMollieSubscriptionRequest{
		TenantId:  "tenant-a",
		TierId:    tierID,
		MandateId: "mdt_123",
	})
	if err == nil {
		t.Fatal("expected local persist failure")
	}
	if len(mollieClient.cancelledSubscriptions) != 1 || mollieClient.cancelledSubscriptions[0] != "cst_123/sub_123" {
		t.Fatalf("cancelled subscriptions = %#v, want [cst_123/sub_123]", mollieClient.cancelledSubscriptions)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet sqlmock expectations: %v", err)
	}
}

type fakeStripeBillingClient struct {
	customer        *stripelib.Customer
	session         *stripelib.CheckoutSession
	expiredSessions []string
}

func (f *fakeStripeBillingClient) CreateOrGetCustomer(context.Context, billingstripe.CustomerInfo) (*stripelib.Customer, error) {
	return f.customer, nil
}

func (f *fakeStripeBillingClient) CreateCheckoutSession(context.Context, billingstripe.CheckoutSessionParams) (*stripelib.CheckoutSession, error) {
	return f.session, nil
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

func (f *fakeMollieBillingClient) CreateSubscription(context.Context, billingmollie.SubscriptionParams) (*mollielib.Subscription, error) {
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
