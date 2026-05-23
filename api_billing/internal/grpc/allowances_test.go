package grpc

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
)

func TestResolveCurrentPeriodUsesSubscriptionBoundsWhenSet(t *testing.T) {
	start := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)
	end := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	now := time.Date(2026, 4, 17, 12, 0, 0, 0, time.UTC)

	gotStart, gotEnd := resolveCurrentPeriod(
		sql.NullTime{Valid: true, Time: start},
		sql.NullTime{Valid: true, Time: end},
		now,
	)
	if !gotStart.Equal(start) || !gotEnd.Equal(end) {
		t.Fatalf("expected subscription bounds, got start=%v end=%v", gotStart, gotEnd)
	}
}

func TestResolveCurrentPeriodFallsBackToCalendarMonth(t *testing.T) {
	now := time.Date(2026, 4, 17, 12, 30, 0, 0, time.UTC)
	wantStart := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)
	wantEnd := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)

	cases := []struct {
		name  string
		start sql.NullTime
		end   sql.NullTime
	}{
		{"both null", sql.NullTime{}, sql.NullTime{}},
		{"start null", sql.NullTime{}, sql.NullTime{Valid: true, Time: wantEnd}},
		{"end null", sql.NullTime{Valid: true, Time: wantStart}, sql.NullTime{}},
		{"reversed bounds", sql.NullTime{Valid: true, Time: wantEnd}, sql.NullTime{Valid: true, Time: wantStart}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gotStart, gotEnd := resolveCurrentPeriod(tc.start, tc.end, now)
			if !gotStart.Equal(wantStart) || !gotEnd.Equal(wantEnd) {
				t.Fatalf("expected calendar-month fallback, got start=%v end=%v", gotStart, gotEnd)
			}
		})
	}
}

func TestComputeAllowancesFreeTierWithUsage(t *testing.T) {
	mockDB, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer mockDB.Close()
	server := &PurserServer{db: mockDB, logger: logging.NewLogger()}

	tenantID := "tenant-1"
	tierID := "tier-free"
	start := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)
	end := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)

	// Pricing rule lookup joins billing_tiers for tier_name.
	mock.ExpectQuery(`FROM purser\.tier_pricing_rules tpr`).
		WithArgs(tierID, "delivered_minutes").
		WillReturnRows(sqlmock.NewRows([]string{"included_quantity", "unit_price", "tier_name"}).
			AddRow(10000.0, 0.0, "free"))
	// Allowance usage includes canonical records plus applied corrections.
	mock.ExpectQuery(`FROM purser\.usage_records`).
		WithArgs(tenantID, start, end, tenantID, start, end).
		WillReturnRows(sqlmock.NewRows([]string{"sum"}).AddRow(3500.0))

	got := server.computeAllowances(context.Background(), tenantID, tierID, start, end)
	if len(got) != 1 {
		t.Fatalf("expected 1 allowance, got %d", len(got))
	}
	a := got[0]
	if a.Meter != "delivered_minutes" {
		t.Errorf("meter: got %q", a.Meter)
	}
	if a.Included != 10000 {
		t.Errorf("included: got %v want 10000", a.Included)
	}
	if a.Used != 3500 {
		t.Errorf("used: got %v want 3500", a.Used)
	}
	if a.Exhausted {
		t.Error("expected not exhausted")
	}
	if !a.IsFreeTier {
		t.Error("expected is_free_tier=true for tier_name=free")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet sqlmock expectations: %v", err)
	}
}

func TestComputeAllowancesFreeTierExhausted(t *testing.T) {
	mockDB, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer mockDB.Close()
	server := &PurserServer{db: mockDB, logger: logging.NewLogger()}

	tenantID := "tenant-2"
	tierID := "tier-free"
	start := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)
	end := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)

	mock.ExpectQuery(`FROM purser\.tier_pricing_rules tpr`).
		WithArgs(tierID, "delivered_minutes").
		WillReturnRows(sqlmock.NewRows([]string{"included_quantity", "unit_price", "tier_name"}).
			AddRow(10000.0, 0.0, "free"))
	// 15000 delivered minutes — over the 10000 included.
	mock.ExpectQuery(`FROM purser\.usage_records`).
		WithArgs(tenantID, start, end, tenantID, start, end).
		WillReturnRows(sqlmock.NewRows([]string{"sum"}).AddRow(15000.0))

	got := server.computeAllowances(context.Background(), tenantID, tierID, start, end)
	if len(got) != 1 {
		t.Fatalf("expected 1 allowance, got %d", len(got))
	}
	a := got[0]
	if !a.Exhausted {
		t.Errorf("expected exhausted=true (used %v > included %v)", a.Used, a.Included)
	}
	if a.Remaining != 0 {
		t.Errorf("expected remaining clamped to 0, got %v", a.Remaining)
	}
	if !a.IsFreeTier {
		t.Error("expected is_free_tier=true for tier_name=free")
	}
}

func TestComputeAllowancesPaidTierNotFreeFlag(t *testing.T) {
	mockDB, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer mockDB.Close()
	server := &PurserServer{db: mockDB, logger: logging.NewLogger()}

	tenantID := "tenant-3"
	tierID := "tier-supporter"
	start := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)
	end := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)

	mock.ExpectQuery(`FROM purser\.tier_pricing_rules tpr`).
		WithArgs(tierID, "delivered_minutes").
		WillReturnRows(sqlmock.NewRows([]string{"included_quantity", "unit_price", "tier_name"}).
			AddRow(120000.0, 0.002, "supporter"))
	mock.ExpectQuery(`FROM purser\.usage_records`).
		WithArgs(tenantID, start, end, tenantID, start, end).
		WillReturnRows(sqlmock.NewRows([]string{"sum"}).AddRow(833.333))

	got := server.computeAllowances(context.Background(), tenantID, tierID, start, end)
	if len(got) != 1 {
		t.Fatalf("expected 1 allowance, got %d", len(got))
	}
	if got[0].IsFreeTier {
		t.Error("expected is_free_tier=false for tier_name=supporter")
	}
}

// TestComputeAllowancesPaidTierWithZeroPricedMeter pins the policy that a
// paid tier with a zero-priced meter is not treated as free-tier for
// admission; admission policy follows the tier identity.
func TestComputeAllowancesPaidTierWithZeroPricedMeter(t *testing.T) {
	mockDB, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer mockDB.Close()
	server := &PurserServer{db: mockDB, logger: logging.NewLogger()}

	tenantID := "tenant-paid-zero-meter"
	tierID := "tier-supporter-promo"
	start := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)
	end := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)

	mock.ExpectQuery(`FROM purser\.tier_pricing_rules tpr`).
		WithArgs(tierID, "delivered_minutes").
		WillReturnRows(sqlmock.NewRows([]string{"included_quantity", "unit_price", "tier_name"}).
			AddRow(120000.0, 0.0, "supporter"))
	mock.ExpectQuery(`FROM purser\.usage_records`).
		WithArgs(tenantID, start, end, tenantID, start, end).
		WillReturnRows(sqlmock.NewRows([]string{"sum"}).AddRow(2200.0))

	got := server.computeAllowances(context.Background(), tenantID, tierID, start, end)
	if len(got) != 1 {
		t.Fatalf("expected 1 allowance, got %d", len(got))
	}
	if got[0].IsFreeTier {
		t.Error("paid tier with $0 meter must NOT be flagged as free-tier (admission policy applies to free plans only)")
	}
}

func TestParseStorageLimitBytes(t *testing.T) {
	const oneGB = int64(1) << 30
	cases := []struct {
		name string
		raw  sql.NullString
		want int64
	}{
		{"unset", sql.NullString{}, 0},
		{"empty string", sql.NullString{Valid: true, String: ""}, 0},
		{"ten gb", sql.NullString{Valid: true, String: "10"}, 10 * oneGB},
		{"one gb", sql.NullString{Valid: true, String: "1"}, oneGB},
		{"zero", sql.NullString{Valid: true, String: "0"}, 0},
		{"negative", sql.NullString{Valid: true, String: "-5"}, 0},
		{"garbage", sql.NullString{Valid: true, String: "ten"}, 0},
		{"wrapper rejected", sql.NullString{Valid: true, String: `{"value":10}`}, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := parseStorageLimitBytes(tc.raw); got != tc.want {
				t.Errorf("parseStorageLimitBytes(%+v) = %d, want %d", tc.raw, got, tc.want)
			}
		})
	}
}

func TestParseTenantResourceLimits(t *testing.T) {
	cases := []struct {
		name        string
		raw         sql.NullString
		wantStreams int32
		wantViewers int32
		wantNil     bool
	}{
		{"unset", sql.NullString{}, 0, 0, true},
		{"empty object", sql.NullString{Valid: true, String: `{}`}, 0, 0, true},
		{"free caps", sql.NullString{Valid: true, String: `{"max_concurrent_streams":3,"max_concurrent_viewers":200}`}, 3, 200, false},
		{"stream only", sql.NullString{Valid: true, String: `{"max_concurrent_streams":1}`}, 1, 0, false},
		{"reject wrappers", sql.NullString{Valid: true, String: `{"max_concurrent_streams":{"value":3}}`}, 0, 0, true},
		{"reject negative", sql.NullString{Valid: true, String: `{"max_concurrent_streams":-1,"max_concurrent_viewers":0}`}, 0, 0, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := parseTenantResourceLimits(tc.raw)
			if tc.wantNil {
				if got != nil {
					t.Fatalf("parseTenantResourceLimits() = %+v, want nil", got)
				}
				return
			}
			if got == nil {
				t.Fatal("parseTenantResourceLimits() = nil")
			}
			if got.GetMaxStreams() != tc.wantStreams || got.GetMaxViewers() != tc.wantViewers {
				t.Fatalf("limits = streams:%d viewers:%d, want streams:%d viewers:%d",
					got.GetMaxStreams(), got.GetMaxViewers(), tc.wantStreams, tc.wantViewers)
			}
		})
	}
}

func TestComputeAllowancesNoMatchingRule(t *testing.T) {
	mockDB, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer mockDB.Close()
	server := &PurserServer{db: mockDB, logger: logging.NewLogger()}

	tenantID := "tenant-4"
	tierID := "tier-no-rules"

	mock.ExpectQuery(`FROM purser\.tier_pricing_rules tpr`).
		WithArgs(tierID, "delivered_minutes").
		WillReturnError(sql.ErrNoRows)

	got := server.computeAllowances(context.Background(), tenantID, tierID, time.Now(), time.Now().Add(time.Hour))
	if got != nil {
		t.Errorf("expected nil allowances when no pricing rule exists, got %+v", got)
	}
}
