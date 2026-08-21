package grpc

import (
	"context"
	"regexp"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	purserpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/purser"
)

func testTime() time.Time { return time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC) }

// TestGetSubscription_LoadsOverrides verifies that GetSubscription returns the
// per-tenant pricing/entitlement overrides instead of an empty list. This is
// the regression for "mutation/direct subscription reads can lie and show
// empty overrides" — the override-loader calls must always run.
func TestGetSubscription_LoadsOverrides(t *testing.T) {
	mockDB, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer mockDB.Close()

	server := &PurserServer{db: mockDB, logger: logging.NewLogger()}
	tenantID := "9d8ba75e-2f60-4f80-89eb-2619a9b3b966"
	subID := "f5c330b5-1473-42fc-8b60-c456dd2c8f5e"
	tierID := "b5694e55-44d1-43e1-a209-d88a1449cb94"

	// Header SELECT returns one subscription row.
	mock.ExpectQuery(`SELECT id, tenant_id, tier_id, status, billing_email`).
		WithArgs(tenantID).
		WillReturnRows(sqlmock.NewRows([]string{
			"id", "tenant_id", "tier_id", "status", "billing_email",
			"started_at",
			"trial_ends_at", "next_billing_date", "cancelled_at",
			"billing_period_start", "billing_period_end",
			"payment_method", "payment_reference", "tax_id", "tax_rate",
			"billing_model", "custom_features", "billing_address",
			"stripe_customer_id", "stripe_subscription_id",
			"stripe_subscription_status", "stripe_current_period_end", "dunning_attempts",
			"mollie_subscription_id",
			"pending_tier_id", "pending_effective_at", "pending_reason",
			"created_at", "updated_at",
		}).AddRow(
			subID, tenantID, tierID, "active", "demo@example.com",
			testTime(), nil, nil, nil,
			nil, nil,
			nil, nil, nil, nil,
			"postpaid", []byte(`{}`), []byte(`{}`),
			nil, nil, nil, nil, nil,
			nil,
			nil, nil, nil,
			testTime(), testTime(),
		))

	// Pricing overrides: one row.
	mock.ExpectQuery(regexp.QuoteMeta(`SELECT meter, model, currency`)).
		WithArgs(subID).
		WillReturnRows(sqlmock.NewRows([]string{"meter", "model", "currency", "included_quantity", "unit_price", "config"}).
			AddRow("delivered_minutes", "tiered_graduated", "EUR", "100000", "0.000400", []byte("{}")))
	// Entitlement overrides: one row.
	mock.ExpectQuery(`SELECT key, value::text AS value FROM purser\.subscription_entitlement_overrides`).
		WithArgs(subID).
		WillReturnRows(sqlmock.NewRows([]string{"key", "value"}).
			AddRow("recording_retention_days", "180"))

	resp, err := server.GetSubscription(context.Background(), &purserpb.GetSubscriptionRequest{TenantId: tenantID})
	if err != nil {
		t.Fatalf("GetSubscription: %v", err)
	}
	if resp.Subscription == nil {
		t.Fatal("nil subscription")
	}
	if got := len(resp.Subscription.PricingOverrides); got != 1 {
		t.Errorf("PricingOverrides = %d rows, want 1", got)
	} else if resp.Subscription.PricingOverrides[0].Meter != "delivered_minutes" {
		t.Errorf("override meter = %q, want delivered_minutes", resp.Subscription.PricingOverrides[0].Meter)
	}
	if got := resp.Subscription.EntitlementOverrides["recording_retention_days"]; got != "180" {
		t.Errorf("entitlement override = %q, want 180", got)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet sqlmock expectations: %v", err)
	}
}

func TestGetSubscription_AllowsNullBillingEmail(t *testing.T) {
	mockDB, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer mockDB.Close()

	server := &PurserServer{db: mockDB, logger: logging.NewLogger()}
	tenantID := "c2154a9b-c3cf-4500-9f2e-e378812d0c79"
	subID := "19f13e32-ec50-4c5d-8ba3-bce6e7ac1f0c"
	tierID := "5142399b-487c-482f-88d4-df9350960ba4"

	mock.ExpectQuery(`SELECT id, tenant_id, tier_id, status, billing_email`).
		WithArgs(tenantID).
		WillReturnRows(sqlmock.NewRows([]string{
			"id", "tenant_id", "tier_id", "status", "billing_email",
			"started_at",
			"trial_ends_at", "next_billing_date", "cancelled_at",
			"billing_period_start", "billing_period_end",
			"payment_method", "payment_reference", "tax_id", "tax_rate",
			"billing_model", "custom_features", "billing_address",
			"stripe_customer_id", "stripe_subscription_id",
			"stripe_subscription_status", "stripe_current_period_end", "dunning_attempts",
			"mollie_subscription_id",
			"pending_tier_id", "pending_effective_at", "pending_reason",
			"created_at", "updated_at",
		}).AddRow(
			subID, tenantID, tierID, "active", nil,
			testTime(), nil, nil, nil,
			nil, nil,
			nil, nil, nil, nil,
			"postpaid", []byte(`{}`), []byte(`{}`),
			nil, nil, nil, nil, nil,
			nil,
			nil, nil, nil,
			testTime(), testTime(),
		))
	mock.ExpectQuery(regexp.QuoteMeta(`SELECT meter, model, currency`)).
		WithArgs(subID).
		WillReturnRows(sqlmock.NewRows([]string{"meter", "model", "currency", "included_quantity", "unit_price", "config"}))
	mock.ExpectQuery(`SELECT key, value::text AS value FROM purser\.subscription_entitlement_overrides`).
		WithArgs(subID).
		WillReturnRows(sqlmock.NewRows([]string{"key", "value"}))

	resp, err := server.GetSubscription(context.Background(), &purserpb.GetSubscriptionRequest{TenantId: tenantID})
	if err != nil {
		t.Fatalf("GetSubscription: %v", err)
	}
	if resp.Subscription == nil {
		t.Fatal("nil subscription")
	}
	if resp.Subscription.BillingEmail != "" {
		t.Fatalf("BillingEmail = %q, want empty string for NULL", resp.Subscription.BillingEmail)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet sqlmock expectations: %v", err)
	}
}
