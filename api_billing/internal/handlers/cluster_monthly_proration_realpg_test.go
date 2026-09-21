//go:build schema_verify

package handlers

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	billingpkg "frameworks/api_billing/internal/billing"
	"frameworks/api_billing/internal/database/purserdb"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	quartermasterpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/quartermaster"
	"github.com/google/uuid"
	"github.com/shopspring/decimal"
)

type marketplaceClusterResolver struct {
	clusterID, ownerTenantID string
}

func (r marketplaceClusterResolver) GetCluster(_ context.Context, clusterID string) (*quartermasterpb.ClusterResponse, error) {
	if clusterID != r.clusterID {
		return nil, errors.New("cluster not found")
	}
	owner := r.ownerTenantID
	return &quartermasterpb.ClusterResponse{Cluster: &quartermasterpb.InfrastructureCluster{
		ClusterId: clusterID, OwnerTenantId: &owner,
	}}, nil
}

// proratedMonthlyCents is monthlyCents times the seconds of [activeFrom,
// activeUntil) inside [periodStart, periodEnd) over the seconds of the month
// starting at periodStart, rounded half away from zero.
func proratedMonthlyCents(monthlyCents int64, periodStart, periodEnd, activeFrom, activeUntil time.Time) int64 {
	from, until := periodStart, periodEnd
	if activeFrom.After(from) {
		from = activeFrom
	}
	if !activeUntil.IsZero() && activeUntil.Before(until) {
		until = activeUntil
	}
	if !until.After(from) {
		return 0
	}
	overlap := decimal.NewFromInt(int64(until.Sub(from) / time.Second))
	month := decimal.NewFromInt(int64(periodStart.AddDate(0, 1, 0).Sub(periodStart) / time.Second))
	return decimal.NewFromInt(monthlyCents).Mul(overlap).Div(month).Round(0).IntPart()
}

// A Purser-invoiced monthly cluster is billed for the share of each invoice
// period it was active, so an early-closed period and the period that follows
// it never both carry the whole month, and a cluster added, cancelled, or
// reactivated inside a period pays only for its active time.
func TestPurserInvoicedClusterMonthlyFeeProratesByActiveTime_RealPG(t *testing.T) { //nolint:funlen // One table covers every overlap shape.
	db := startPurserUsageRealPG(t)
	ctx := context.Background()
	clusterID := "cluster-monthly-" + uuid.NewString()[:8]
	owner := uuid.NewString()
	if _, err := db.ExecContext(ctx, `
		INSERT INTO purser.cluster_pricing_history (cluster_id, pricing_model, base_price, currency, effective_from)
		VALUES ($1, 'monthly', 30.00, 'EUR', NOW() - INTERVAL '2 years')
	`, clusterID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `
		INSERT INTO purser.platform_fee_policy (id, cluster_kind, cluster_owner_tenant_id, pricing_source, fee_basis_points, effective_from)
		VALUES ($1, 'third_party_marketplace', $2, 'cluster_monthly', 1000, NOW() - INTERVAL '2 years')
	`, uuid.NewString(), owner); err != nil {
		t.Fatal(err)
	}
	resolver := marketplaceClusterResolver{clusterID: clusterID, ownerTenantID: owner}
	jobs := &JobManager{db: db, logger: logging.NewLogger(), billing: &Service{db: db, logger: logging.NewLogger()}}
	tier := &billingpkg.EffectiveTier{Currency: "EUR"}

	insertSubscription := func(t *testing.T, createdAt time.Time, cancelledAt time.Time) string {
		t.Helper()
		tenantID := uuid.NewString()
		status := "active"
		cancelled := sql.NullTime{}
		if !cancelledAt.IsZero() {
			status, cancelled = "cancelled", sql.NullTime{Time: cancelledAt, Valid: true}
		}
		if _, err := db.ExecContext(ctx, `
			INSERT INTO purser.cluster_subscriptions (tenant_id, cluster_id, status, created_at, cancelled_at)
			VALUES ($1, $2, $3, $4, $5)
		`, tenantID, clusterID, status, createdAt, cancelled); err != nil {
			t.Fatal(err)
		}
		return tenantID
	}
	monthlyLine := func(t *testing.T, tenantID string, periodStart, periodEnd time.Time) (int64, int64) {
		t.Helper()
		lines, err := jobs.purserInvoicedClusterMonthlyLines(ctx, tenantID, periodStart, periodEnd, tier, resolver, periodStart.Format("200601"))
		if err != nil {
			t.Fatalf("purserInvoicedClusterMonthlyLines: %v", err)
		}
		if len(lines) == 0 {
			return 0, 0
		}
		if len(lines) != 1 {
			t.Fatalf("monthly lines = %d, want one", len(lines))
		}
		return lines[0].Amount.Shift(2).IntPart(), lines[0].OperatorCreditCents
	}
	assertLine := func(t *testing.T, name string, gotCents, gotCredit, wantCents int64) {
		t.Helper()
		wantCredit := wantCents - (wantCents*1000+5000)/10000
		if gotCents != wantCents || gotCredit != wantCredit {
			t.Errorf("%s: monthly line %d cents with operator credit %d, want %d with credit %d", name, gotCents, gotCredit, wantCents, wantCredit)
		}
	}

	start := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	end := start.AddDate(0, 1, 0)

	tenant := insertSubscription(t, start.AddDate(0, 0, -10), time.Time{})
	cents, credit := monthlyLine(t, tenant, start, end)
	assertLine(t, "whole period", cents, credit, 3000)

	change := start.Add(10*24*time.Hour + 7*time.Hour)
	closed, closedCredit := monthlyLine(t, tenant, start, change)
	assertLine(t, "period closed early by a tier change", closed, closedCredit, proratedMonthlyCents(3000, start, change, start, time.Time{}))
	next, nextCredit := monthlyLine(t, tenant, change, change.AddDate(0, 1, 0))
	assertLine(t, "period started at the change", next, nextCredit, 3000)

	lateStart := start.Add(15*24*time.Hour + 3*time.Hour)
	late := insertSubscription(t, lateStart, time.Time{})
	cents, credit = monthlyLine(t, late, start, end)
	assertLine(t, "subscribed inside the period", cents, credit, proratedMonthlyCents(3000, start, end, lateStart, time.Time{}))

	cancelAt := start.Add(6*24*time.Hour + 12*time.Hour)
	cancelled := insertSubscription(t, start.AddDate(0, 0, -40), cancelAt)
	cents, credit = monthlyLine(t, cancelled, start, end)
	assertLine(t, "cancelled inside the period", cents, credit, proratedMonthlyCents(3000, start, end, start, cancelAt))

	now := time.Now().UTC()
	reactivated := insertSubscription(t, now.AddDate(0, 0, -90), now.AddDate(0, 0, -60))
	if _, err := purserdb.New(db).ActivatePurserInvoicedClusterSubscription(ctx, purserdb.ActivatePurserInvoicedClusterSubscriptionParams{
		TenantID: reactivated, ClusterID: clusterID,
	}); err != nil {
		t.Fatalf("reactivate cluster subscription: %v", err)
	}
	var reactivatedAt time.Time
	if err := db.QueryRowContext(ctx, `SELECT updated_at FROM purser.cluster_subscriptions WHERE tenant_id = $1`, reactivated).Scan(&reactivatedAt); err != nil {
		t.Fatal(err)
	}
	periodStart := now.AddDate(0, 0, -10).Truncate(time.Second)
	periodEnd := periodStart.AddDate(0, 1, 0)
	cents, credit = monthlyLine(t, reactivated, periodStart, periodEnd)
	assertLine(t, "reactivated inside the period", cents, credit, proratedMonthlyCents(3000, periodStart, periodEnd, reactivatedAt, time.Time{}))

	// Both active spans are owed when cancellation and reactivation share an invoice period.
	cancelledInside := now.Add(-48 * time.Hour).Truncate(time.Second)
	twiceActive := insertSubscription(t, periodStart, cancelledInside)
	for range 2 {
		if _, err := purserdb.New(db).ActivatePurserInvoicedClusterSubscription(ctx, purserdb.ActivatePurserInvoicedClusterSubscriptionParams{
			TenantID: twiceActive, ClusterID: clusterID,
		}); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.QueryRowContext(ctx, `SELECT activated_at FROM purser.cluster_subscriptions WHERE tenant_id = $1`, twiceActive).Scan(&reactivatedAt); err != nil {
		t.Fatal(err)
	}
	cents, credit = monthlyLine(t, twiceActive, periodStart, periodEnd)
	activeSeconds := int64(cancelledInside.Sub(periodStart)/time.Second) + int64(periodEnd.Sub(reactivatedAt)/time.Second)
	monthSeconds := int64(periodEnd.Sub(periodStart) / time.Second)
	want := decimal.NewFromInt(3000).Mul(decimal.NewFromInt(activeSeconds)).Div(decimal.NewFromInt(monthSeconds)).Round(0).IntPart()
	assertLine(t, "cancelled and reactivated in the same period", cents, credit, want)
	var periods int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM purser.cluster_subscription_active_periods WHERE tenant_id = $1`, twiceActive).Scan(&periods); err != nil || periods != 1 {
		t.Fatalf("archived periods = %d, error %v; want one", periods, err)
	}
}
