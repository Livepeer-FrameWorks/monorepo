//go:build schema_verify

package grpc

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"frameworks/api_billing/internal/database/purserdb"
	billingstripe "frameworks/api_billing/internal/stripe"
	"frameworks/api_billing/internal/subscriptions"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/database"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	purserpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/purser"
	"github.com/google/uuid"
	stripelib "github.com/stripe/stripe-go/v85"
)

type failedRetryCustomer struct {
	fakeStripeBillingClient
	calls int
}

func (c *failedRetryCustomer) CreateOrGetCustomer(context.Context, billingstripe.CustomerInfo) (*stripelib.Customer, error) {
	c.calls++
	return nil, errors.New("customer lookup temporarily unavailable")
}

func TestCheckoutRetryKeepsPayableReservation_RealPG(t *testing.T) {
	db := startOperatorGrantDatabase(t)
	tenant, tier, _ := seedGrantRace(t, db)
	ctx := t.Context()
	if _, err := db.Exec(`DELETE FROM purser.subscription_operator_grants WHERE subscription_id=(SELECT id FROM purser.tenant_subscriptions WHERE tenant_id=$1)`, tenant); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE purser.tenant_subscriptions SET billing_name='Fixture',billing_email='fixture@example.com',billing_address='{"street":"Street 1","city":"Amsterdam","postal_code":"1000AA","country":"NL"}' WHERE tenant_id=$1`, tenant); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE purser.billing_tiers SET stripe_price_id_monthly='price_fixture' WHERE id=$1`, tier); err != nil {
		t.Fatal(err)
	}
	id := uuid.NewString()
	key := "stripe-tenant-checkout:" + tenant + ":" + tier
	if _, err := db.Exec(`INSERT INTO purser.payment_provider_intents(id,tenant_id,provider,purpose,status,submission_state,currency,idempotency_key,provider_customer_id,provider_session_id,expires_at)
VALUES($1,$2,'stripe','tenant_subscription_checkout','provider_open','submitted','EUR',$3,'customer','payable-session',NOW()+INTERVAL '1 hour')`, id, tenant, key); err != nil {
		t.Fatal(err)
	}
	client := &failedRetryCustomer{}
	server := &PurserServer{db: db, logger: logging.NewLogger(), stripeClient: client, commodoreClient: &fakeCommodorePrimaryUser{}}
	if _, err := server.CreateCheckoutSession(ctx, &purserpb.CreateStripeCheckoutRequest{TenantId: tenant, TierId: tier, BillingPeriod: "monthly", SuccessUrl: "https://example.com/ok", CancelUrl: "https://example.com/cancel"}); err == nil || client.calls != 1 {
		t.Fatalf("retry did not reach customer lookup: calls=%d err=%v", client.calls, err)
	}
	err := database.WithRetryablePostgresTx(ctx, db, nil, func(tx *sql.Tx) error {
		if _, err := subscriptions.LockChange(ctx, tx, tenant, subscriptions.PreservePricing); err != nil {
			return err
		}
		return subscriptions.CheckGrantCheckout(ctx, tx, tenant)
	})
	if !errors.Is(err, subscriptions.ErrPendingCheckout) {
		t.Fatalf("customer lookup failure released a payable checkout: %v", err)
	}
	pendingMessage := err.Error()
	row, err := purserdb.New(db).GetProviderCheckoutReservation(ctx, purserdb.GetProviderCheckoutReservationParams{TenantID: tenant, IntentID: id})
	if err != nil || row.Status == "terminal_failed" || row.ProviderSessionID.String != "payable-session" {
		t.Fatalf("retry destroyed payable receipt: %+v err=%v", row, err)
	}
	wantMessage := subscriptions.ErrPendingCheckout.Error() + "; checkout deadline: " + row.ExpiresAt.Time.UTC().Format(time.RFC3339) + "; completed purchases remain reserved during settlement. Inspect admin billing recovery list or run admin billing recovery checkout"
	if pendingMessage != wantMessage {
		t.Fatalf("pending checkout guidance: got %q, want %q", pendingMessage, wantMessage)
	}
}

func TestCheckoutRetryKeepsPayableReservation_RealYugabyte(t *testing.T) {
	TestCheckoutRetryKeepsPayableReservation_RealPG(t)
}
