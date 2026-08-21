package tieraccess

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"

	quartermasterpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/quartermaster"
)

const (
	sweepTenantA       = "81000000-0000-4000-8000-000000000001"
	sweepTenantB       = "81000000-0000-4000-8000-000000000002"
	sweepTenantC       = "81000000-0000-4000-8000-000000000003"
	sweepTenantUnknown = "81000000-0000-4000-8000-000000000004"
)

func subscriptionTierRows(rows ...[2]string) *sqlmock.Rows {
	r := sqlmock.NewRows([]string{"tenant_id", "tier_name"})
	for _, row := range rows {
		r.AddRow(row[0], row[1])
	}
	return r
}

func sweepTenant(id, tier string) *quartermasterpb.Tenant {
	return &quartermasterpb.Tenant{Id: id, DeploymentTier: tier}
}

// Mismatched stamps are repaired; matching ones are left alone (no alias-
// outbox churn in QM), and the repaired count reflects only actual writes.
func TestSweepDeploymentTiers_RepairsMismatchesOnly(t *testing.T) {
	qm := &fakeQM{tenantPages: [][]*quartermasterpb.Tenant{{
		sweepTenant(sweepTenantA, "global"), // frameworks-style stale bootstrap stamp
		sweepTenant(sweepTenantB, ""),       // pre-fix self-signup
		sweepTenant(sweepTenantC, "supporter"),
	}}}
	r, mock := newReconcilerWithMock(t, qm)
	mock.ExpectQuery(`FROM purser\.tenant_subscriptions`).
		WillReturnRows(subscriptionTierRows(
			[2]string{sweepTenantA, "free"},
			[2]string{sweepTenantB, "payg"},
			[2]string{sweepTenantC, "supporter"},
		))

	repaired, err := r.SweepDeploymentTiers(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if repaired != 2 {
		t.Errorf("repaired = %d, want 2", repaired)
	}
	want := []string{"tier:" + sweepTenantA + "=free", "tier:" + sweepTenantB + "=payg"}
	if strings.Join(qm.calls, "|") != strings.Join(want, "|") {
		t.Errorf("calls = %v, want %v", qm.calls, want)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet DB expectations: %v", err)
	}
}

// Tenants without a purser subscription are skipped — the sweep never invents
// a tier for a tenant billing knows nothing about.
func TestSweepDeploymentTiers_SkipsTenantsWithoutSubscription(t *testing.T) {
	qm := &fakeQM{tenantPages: [][]*quartermasterpb.Tenant{{
		sweepTenant(sweepTenantUnknown, "global"),
	}}}
	r, mock := newReconcilerWithMock(t, qm)
	mock.ExpectQuery(`FROM purser\.tenant_subscriptions`).
		WillReturnRows(subscriptionTierRows())

	repaired, err := r.SweepDeploymentTiers(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if repaired != 0 || len(qm.calls) != 0 {
		t.Errorf("expected no stamps, got repaired=%d calls=%v", repaired, qm.calls)
	}
}

// The sweep walks every ListTenants page, not just the first.
func TestSweepDeploymentTiers_Pages(t *testing.T) {
	qm := &fakeQM{tenantPages: [][]*quartermasterpb.Tenant{
		{sweepTenant(sweepTenantA, "")},
		{sweepTenant(sweepTenantB, "")},
	}}
	r, mock := newReconcilerWithMock(t, qm)
	mock.ExpectQuery(`FROM purser\.tenant_subscriptions`).
		WillReturnRows(subscriptionTierRows(
			[2]string{sweepTenantA, "free"},
			[2]string{sweepTenantB, "free"},
		))

	repaired, err := r.SweepDeploymentTiers(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if repaired != 2 {
		t.Errorf("repaired = %d, want 2 (both pages)", repaired)
	}
}

// One tenant's failing stamp must not wedge the sweep — it is logged, skipped,
// and the remaining tenants still converge.
func TestSweepDeploymentTiers_ContinuesPastStampFailure(t *testing.T) {
	qm := &fakeQM{
		tenantPages: [][]*quartermasterpb.Tenant{{
			sweepTenant(sweepTenantA, ""),
			sweepTenant(sweepTenantB, ""),
		}},
		updateErr: errors.New("qm down"),
	}
	r, mock := newReconcilerWithMock(t, qm)
	mock.ExpectQuery(`FROM purser\.tenant_subscriptions`).
		WillReturnRows(subscriptionTierRows(
			[2]string{sweepTenantA, "free"},
			[2]string{sweepTenantB, "free"},
		))

	repaired, err := r.SweepDeploymentTiers(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if repaired != 0 {
		t.Errorf("repaired = %d, want 0 (all stamps failed)", repaired)
	}
	if len(qm.calls) != 2 {
		t.Errorf("expected both tenants attempted, got %v", qm.calls)
	}
}
