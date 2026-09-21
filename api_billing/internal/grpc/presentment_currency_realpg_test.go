//go:build schema_verify

package grpc

import (
	"context"
	"strings"
	"testing"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	purserpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/purser"
	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestPresentmentCurrencyDerivationAndLock_RealPG(t *testing.T) {
	db := startPurserTransitionRealPG(t)
	ctx := context.Background()
	server := &PurserServer{db: db, logger: logging.NewLogger()}
	tierID := uuid.NewString()
	if _, err := db.ExecContext(ctx, `INSERT INTO purser.billing_tiers (id, tier_name, display_name) VALUES ($1, $2, 'Presentment contract')`,
		tierID, "presentment-"+tierID); err != nil {
		t.Fatal(err)
	}
	newTenant := func(t *testing.T) string {
		t.Helper()
		tenantID := uuid.NewString()
		if _, err := db.ExecContext(ctx, `INSERT INTO purser.tenant_subscriptions (tenant_id, tier_id, status, billing_model) VALUES ($1, $2, 'active', 'prepaid')`,
			tenantID, tierID); err != nil {
			t.Fatal(err)
		}
		return tenantID
	}
	presentment := func(t *testing.T, tenantID string) string {
		t.Helper()
		var currency string
		if err := db.QueryRowContext(ctx, `SELECT presentment_currency FROM purser.tenant_subscriptions WHERE tenant_id = $1`, tenantID).Scan(&currency); err != nil {
			t.Fatal(err)
		}
		return currency
	}
	setCountry := func(tenantID, country string) error {
		_, err := server.UpdateBillingDetails(ctx, &purserpb.UpdateBillingDetailsRequest{
			TenantId: tenantID,
			Address:  &purserpb.BillingAddress{Street: "1 Street", City: "City", PostalCode: "1000", Country: country},
		})
		return err
	}

	t.Run("billing country derives the presentment currency", func(t *testing.T) {
		tenantID := newTenant(t)
		if got := presentment(t, tenantID); got != "USD" {
			t.Fatalf("new tenant presentment = %s, want USD", got)
		}
		for _, tc := range []struct{ country, want string }{{"NL", "EUR"}, {"GB", "GBP"}, {"US", "USD"}, {"no", "EUR"}} {
			if err := setCountry(tenantID, tc.country); err != nil {
				t.Fatalf("country %s: %v", tc.country, err)
			}
			if got := presentment(t, tenantID); got != tc.want {
				t.Fatalf("country %s presentment = %s, want %s", tc.country, got, tc.want)
			}
		}
	})

	t.Run("an open invoice locks the currency in the service", func(t *testing.T) {
		tenantID := newTenant(t)
		if err := setCountry(tenantID, "NL"); err != nil {
			t.Fatal(err)
		}
		if _, err := db.ExecContext(ctx, `INSERT INTO purser.billing_invoices (tenant_id, status, currency, amount, due_date) VALUES ($1, 'pending', 'EUR', 10, NOW())`, tenantID); err != nil {
			t.Fatal(err)
		}
		err := setCountry(tenantID, "US")
		if status.Code(err) != codes.FailedPrecondition || !strings.Contains(err.Error(), "PRESENTMENT_CURRENCY_LOCKED") {
			t.Fatalf("locked change err = %v, want FailedPrecondition PRESENTMENT_CURRENCY_LOCKED", err)
		}
		var country string
		if err := db.QueryRowContext(ctx, `SELECT billing_address->>'country' FROM purser.tenant_subscriptions WHERE tenant_id = $1`, tenantID).Scan(&country); err != nil {
			t.Fatal(err)
		}
		if got := presentment(t, tenantID); got != "EUR" || country != "NL" {
			t.Fatalf("refused update changed state: presentment=%s country=%s", got, country)
		}
		// A country with the same presentment currency is still allowed.
		if err := setCountry(tenantID, "DE"); err != nil {
			t.Fatalf("same-currency country change refused: %v", err)
		}
	})

	t.Run("an open Stripe checkout holds the currency until expiry", func(t *testing.T) {
		for _, purpose := range []string{"tenant_subscription_checkout", "cluster_subscription_checkout"} {
			tenantID := newTenant(t)
			if _, err := db.ExecContext(ctx, `INSERT INTO purser.payment_provider_intents (tenant_id, provider, purpose, status, currency, idempotency_key)
				VALUES ($1::text::uuid, 'stripe', $2, 'provider_open', 'USD', $1::text)`, tenantID, purpose); err != nil {
				t.Fatal(err)
			}
			if err := setCountry(tenantID, "GB"); status.Code(err) != codes.FailedPrecondition {
				t.Fatalf("open %s: %v", purpose, err)
			}
			if _, err := db.ExecContext(ctx, `UPDATE purser.payment_provider_intents SET status = 'expired' WHERE tenant_id = $1`, tenantID); err != nil {
				t.Fatal(err)
			}
			if err := setCountry(tenantID, "GB"); err != nil {
				t.Fatal(err)
			}
		}
	})

	t.Run("the trigger refuses direct updates while locked", func(t *testing.T) {
		subscribed := newTenant(t)
		if _, err := db.ExecContext(ctx, `UPDATE purser.tenant_subscriptions SET presentment_currency = 'EUR', mollie_subscription_id = 'sub_contract' WHERE tenant_id = $1`, subscribed); err == nil ||
			!strings.Contains(err.Error(), "PRESENTMENT_CURRENCY_LOCKED") {
			t.Fatalf("update with a provider subscription err = %v, want PRESENTMENT_CURRENCY_LOCKED", err)
		}
		if _, err := db.ExecContext(ctx, `UPDATE purser.tenant_subscriptions SET mollie_subscription_id = 'sub_contract' WHERE tenant_id = $1`, subscribed); err != nil {
			t.Fatal(err)
		}
		if _, err := db.ExecContext(ctx, `UPDATE purser.tenant_subscriptions SET presentment_currency = 'GBP' WHERE tenant_id = $1`, subscribed); err == nil ||
			!strings.Contains(err.Error(), "PRESENTMENT_CURRENCY_LOCKED") {
			t.Fatalf("direct update with a Mollie subscription err = %v", err)
		}
		if err := setCountry(subscribed, "GB"); status.Code(err) != codes.FailedPrecondition {
			t.Fatalf("service change with a Mollie subscription err = %v", err)
		}

		clusterSubscribed := newTenant(t)
		if _, err := db.ExecContext(ctx, `INSERT INTO purser.cluster_subscriptions (tenant_id, cluster_id, status, stripe_subscription_id) VALUES ($1, 'cluster-a', 'active', 'sub_cluster')`, clusterSubscribed); err != nil {
			t.Fatal(err)
		}
		if _, err := db.ExecContext(ctx, `UPDATE purser.tenant_subscriptions SET presentment_currency = 'EUR' WHERE tenant_id = $1`, clusterSubscribed); err == nil {
			t.Fatal("direct update with a Stripe cluster subscription succeeded")
		}

		paid := newTenant(t)
		if _, err := db.ExecContext(ctx, `INSERT INTO purser.billing_invoices (tenant_id, status, currency, amount, due_date) VALUES ($1, 'paid', 'EUR', 10, NOW())`, paid); err != nil {
			t.Fatal(err)
		}
		if _, err := db.ExecContext(ctx, `UPDATE purser.tenant_subscriptions SET presentment_currency = 'GBP' WHERE tenant_id = $1`, paid); err != nil {
			t.Fatalf("paid invoices must not lock the currency: %v", err)
		}
		if _, err := db.ExecContext(ctx, `UPDATE purser.tenant_subscriptions SET presentment_currency = 'eur' WHERE tenant_id = $1`, paid); err == nil {
			t.Fatal("lowercase presentment currency accepted")
		}
	})
}
