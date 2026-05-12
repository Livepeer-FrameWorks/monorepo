package grpc

import (
	"context"
	"database/sql"
	"regexp"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
)

// TestParseRetentionDays_BareScalarOnly locks the canonical decoder contract:
// only a bare JSON integer is accepted. Wrapper objects are rejected. Anything
// else returns 0 so callers fall back to the system default.
func TestParseRetentionDays_BareScalarOnly(t *testing.T) {
	cases := []struct {
		name string
		raw  sql.NullString
		want int32
	}{
		{"unset null", sql.NullString{}, 0},
		{"empty string", sql.NullString{Valid: true, String: ""}, 0},
		{"bare integer", sql.NullString{Valid: true, String: "90"}, 90},
		{"days wrapper rejected", sql.NullString{Valid: true, String: `{"days":180}`}, 0},
		{"value wrapper rejected", sql.NullString{Valid: true, String: `{"value":365}`}, 0},
		{"zero integer", sql.NullString{Valid: true, String: "0"}, 0},
		{"negative integer", sql.NullString{Valid: true, String: "-5"}, 0},
		{"garbage", sql.NullString{Valid: true, String: "{not json"}, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := parseRetentionDays(tc.raw); got != tc.want {
				t.Errorf("parseRetentionDays(%+v) = %d, want %d", tc.raw, got, tc.want)
			}
		})
	}
}

func TestGetSubscriptionAndTierNoActiveSubscription(t *testing.T) {
	mockDB, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer mockDB.Close()

	server := &PurserServer{db: mockDB, logger: logging.NewLogger()}
	tenantID := "tenant-without-subscription"

	mock.ExpectQuery(`FROM purser\.tenant_subscriptions ts`).
		WithArgs(tenantID).
		WillReturnError(sql.ErrNoRows)

	subscription, tier, err := server.getSubscriptionAndTier(context.Background(), tenantID)
	if err != nil {
		t.Fatalf("getSubscriptionAndTier: %v", err)
	}
	if subscription != nil {
		t.Fatalf("expected nil subscription when no active subscription exists, got %+v", subscription)
	}
	if tier != nil {
		t.Fatalf("expected nil tier when no active subscription exists, got %+v", tier)
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet sqlmock expectations: %v", err)
	}
}

func TestGetSubscriptionAndTierAllowsNullBillingEmail(t *testing.T) {
	mockDB, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer mockDB.Close()

	server := &PurserServer{db: mockDB, logger: logging.NewLogger()}
	tenantID := "tenant-with-null-email"
	subID := "sub-1"
	tierID := "tier-free"
	now := testTime()

	mock.ExpectQuery(`FROM purser\.tenant_subscriptions ts`).
		WithArgs(tenantID).
		WillReturnRows(sqlmock.NewRows([]string{
			"ts_id", "tenant_id", "tier_id", "status", "billing_email",
			"started_at", "trial_ends_at", "next_billing_date", "cancelled_at",
			"billing_period_start", "billing_period_end",
			"custom_features",
			"payment_method", "payment_reference", "billing_address",
			"tax_id", "tax_rate",
			"billing_model",
			"stripe_customer_id", "stripe_subscription_id", "stripe_subscription_status", "stripe_current_period_end", "dunning_attempts",
			"mollie_subscription_id",
			"sub_created_at", "sub_updated_at",
			"bt_id", "tier_name", "display_name", "description",
			"base_price", "currency", "billing_period",
			"features", "support_level", "sla_level",
			"metering_enabled", "is_active",
			"tier_level", "is_enterprise", "tier_created_at", "tier_updated_at",
			"processes_live", "processes_vod",
		}).AddRow(
			subID, tenantID, tierID, "active", nil,
			now, nil, nil, nil,
			nil, nil,
			[]byte(`{}`),
			nil, nil, []byte(`{}`),
			nil, nil,
			"postpaid",
			nil, nil, nil, nil, nil,
			nil,
			now, now,
			tierID, "free", "Free", "Free tier",
			"0.00", "EUR", "monthly",
			[]byte(`{}`), "community", "none",
			false, true,
			int32(1), false, now, now,
			nil, nil,
		))
	mock.ExpectQuery(regexp.QuoteMeta(`SELECT meter, COALESCE(model, ''), COALESCE(currency, '')`)).
		WithArgs(subID).
		WillReturnRows(sqlmock.NewRows([]string{"meter", "model", "currency", "included_quantity", "unit_price", "config"}))
	mock.ExpectQuery(`SELECT key, value::text FROM purser\.subscription_entitlement_overrides`).
		WithArgs(subID).
		WillReturnRows(sqlmock.NewRows([]string{"key", "value"}))
	mock.ExpectQuery(`FROM purser\.tier_pricing_rules`).
		WithArgs(tierID).
		WillReturnRows(sqlmock.NewRows([]string{"meter", "model", "currency", "included_quantity", "unit_price", "config"}))
	mock.ExpectQuery(`SELECT key, value::text FROM purser\.tier_entitlements`).
		WithArgs(tierID).
		WillReturnRows(sqlmock.NewRows([]string{"key", "value"}))

	subscription, _, err := server.getSubscriptionAndTier(context.Background(), tenantID)
	if err != nil {
		t.Fatalf("getSubscriptionAndTier: %v", err)
	}
	if subscription == nil {
		t.Fatal("nil subscription")
	}
	if subscription.BillingEmail != "" {
		t.Fatalf("BillingEmail = %q, want empty string for NULL", subscription.BillingEmail)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet sqlmock expectations: %v", err)
	}
}
