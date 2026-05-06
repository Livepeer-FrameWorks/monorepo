package grpc

import (
	"context"
	"database/sql"
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
