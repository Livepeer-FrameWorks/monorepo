package tieraccess

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"

	quartermasterpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/quartermaster"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const (
	sweepTenantA       = "81000000-0000-4000-8000-000000000001"
	sweepTenantB       = "81000000-0000-4000-8000-000000000002"
	sweepTenantC       = "81000000-0000-4000-8000-000000000003"
	sweepTenantUnknown = "81000000-0000-4000-8000-000000000004"
)

func subscriptionTierRows(rows ...[2]string) *sqlmock.Rows {
	r := sqlmock.NewRows([]string{"tenant_id", "tier_name", "custom_subdomain_enabled", "custom_domain_enabled"})
	for _, row := range rows {
		r.AddRow(row[0], row[1], false, false)
	}
	return r
}

func sweepTenant(id, tier string) *quartermasterpb.Tenant {
	return &quartermasterpb.Tenant{Id: id, DeploymentTier: tier, BillingEntitlementsObserved: true, IsActive: true}
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
	want := []string{"dns:" + sweepTenantA + "=free", "dns:" + sweepTenantB + "=payg", "dns-handoff"}
	if strings.Join(qm.calls, "|") != strings.Join(want, "|") {
		t.Errorf("calls = %v, want %v", qm.calls, want)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet DB expectations: %v", err)
	}
}

func TestSweepDeploymentTiers_StampsMatchingEpochTenant(t *testing.T) {
	tenant := sweepTenant(sweepTenantC, "supporter")
	tenant.BillingEntitlementsObserved = false
	qm := &fakeQM{tenantPages: [][]*quartermasterpb.Tenant{{tenant}}}
	r, mock := newReconcilerWithMock(t, qm)
	mock.ExpectQuery(`FROM purser\.tenant_subscriptions`).
		WillReturnRows(subscriptionTierRows([2]string{sweepTenantC, "supporter"}))

	repaired, err := r.SweepDeploymentTiers(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if repaired != 1 || len(qm.calls) != 2 || qm.calls[1] != "dns-handoff" {
		t.Fatalf("epoch tenant was not stamped: repaired=%d calls=%v", repaired, qm.calls)
	}
}

// A tenant absent from Purser is an authoritative fail-closed billing state.
// This covers unverified/abandoned signups and the bootstrap system tenant.
func TestSweepDeploymentTiers_ClosesSubscriptionlessTenantEntitlements(t *testing.T) {
	tenant := sweepTenant(sweepTenantUnknown, "global")
	tenant.BillingEntitlementsObserved = false
	tenant.CustomSubdomainEnabled = true
	tenant.CustomDomainEnabled = true
	qm := &fakeQM{tenantPages: [][]*quartermasterpb.Tenant{{tenant}}}
	r, mock := newReconcilerWithMock(t, qm)
	mock.ExpectQuery(`FROM purser\.tenant_subscriptions`).
		WillReturnRows(subscriptionTierRows())

	repaired, err := r.SweepDeploymentTiers(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if repaired != 1 || strings.Join(qm.calls, "|") != "dns:"+sweepTenantUnknown+"=free|dns-handoff" {
		t.Fatalf("subscriptionless convergence = repaired %d calls %v", repaired, qm.calls)
	}
	if qm.lastDNS.GetCustomSubdomainEnabled() || qm.lastDNS.GetCustomDomainEnabled() {
		t.Fatalf("subscriptionless tenant retained DNS grants: %+v", qm.lastDNS)
	}
}

func TestSweepDeploymentTiers_PreservesGrantedSubscriptionEntitlements(t *testing.T) {
	tenant := sweepTenant(sweepTenantA, "supporter")
	tenant.CustomSubdomainEnabled = false
	tenant.CustomDomainEnabled = false
	qm := &fakeQM{tenantPages: [][]*quartermasterpb.Tenant{{tenant}}}
	r, mock := newReconcilerWithMock(t, qm)
	mock.ExpectQuery(`FROM purser\.tenant_subscriptions`).WillReturnRows(
		sqlmock.NewRows([]string{"tenant_id", "tier_name", "custom_subdomain_enabled", "custom_domain_enabled"}).
			AddRow(sweepTenantA, "supporter", true, true),
	)

	repaired, err := r.SweepDeploymentTiers(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if repaired != 1 || qm.lastDNS == nil || !qm.lastDNS.GetCustomSubdomainEnabled() || !qm.lastDNS.GetCustomDomainEnabled() {
		t.Fatalf("granted subscription was not materialized: repaired=%d request=%+v", repaired, qm.lastDNS)
	}
}

func TestSweepDeploymentTiers_InactiveTenantOverridesGrantedSubscription(t *testing.T) {
	tenant := sweepTenant(sweepTenantA, "supporter")
	tenant.IsActive = false
	tenant.CustomSubdomainEnabled = true
	tenant.CustomDomainEnabled = true
	qm := &fakeQM{tenantPages: [][]*quartermasterpb.Tenant{{tenant}}}
	r, mock := newReconcilerWithMock(t, qm)
	mock.ExpectQuery(`FROM purser\.tenant_subscriptions`).WillReturnRows(
		sqlmock.NewRows([]string{"tenant_id", "tier_name", "custom_subdomain_enabled", "custom_domain_enabled"}).
			AddRow(sweepTenantA, "supporter", true, true),
	)

	if _, err := r.SweepDeploymentTiers(context.Background()); err != nil {
		t.Fatal(err)
	}
	if qm.lastDNS == nil || qm.lastDNS.GetCustomSubdomainEnabled() || qm.lastDNS.GetCustomDomainEnabled() {
		t.Fatalf("inactive tenant retained DNS grants: %+v", qm.lastDNS)
	}
}

func TestSweepDeploymentTiers_WithholdsHandoffForSurplusSubscription(t *testing.T) {
	qm := &fakeQM{tenantPages: [][]*quartermasterpb.Tenant{{}}}
	r, mock := newReconcilerWithMock(t, qm)
	mock.ExpectQuery(`FROM purser\.tenant_subscriptions`).
		WillReturnRows(subscriptionTierRows([2]string{sweepTenantUnknown, "free"}))
	if _, err := r.SweepDeploymentTiers(context.Background()); err == nil || !strings.Contains(err.Error(), "matched_subscriptions=0") {
		t.Fatalf("error = %v, want surplus-subscription census failure", err)
	}
	if len(qm.calls) != 0 {
		t.Fatalf("surplus subscription published handoff: %v", qm.calls)
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

// One tenant's failing stamp must not wedge later attempts, but it must withhold
// the release handoff so the compatibility migration remains fail-closed.
func TestSweepDeploymentTiers_ContinuesPastStampFailure(t *testing.T) {
	qm := &fakeQM{
		tenantPages: [][]*quartermasterpb.Tenant{{
			sweepTenant(sweepTenantA, ""),
			sweepTenant(sweepTenantB, ""),
		}},
		updateErr: errors.New("qm down"),
	}
	r, mock := newReconcilerWithMock(t, qm)
	r.reconciliationFailures = prometheus.NewCounterVec(prometheus.CounterOpts{Name: "test_sweep_reconciliation_failures_total"}, []string{"operation"})
	mock.ExpectQuery(`FROM purser\.tenant_subscriptions`).
		WillReturnRows(subscriptionTierRows(
			[2]string{sweepTenantA, "free"},
			[2]string{sweepTenantB, "free"},
		))

	repaired, err := r.SweepDeploymentTiers(context.Background())
	if err == nil {
		t.Fatal("expected failed sweep")
	}
	if repaired != 0 {
		t.Errorf("repaired = %d, want 0 (all stamps failed)", repaired)
	}
	if len(qm.calls) != 2 {
		t.Errorf("expected both tenants attempted, got %v", qm.calls)
	}
	for _, call := range qm.calls {
		if call == "dns-handoff" {
			t.Fatal("failed sweep published release handoff")
		}
	}
	if got := testutil.ToFloat64(r.reconciliationFailures.WithLabelValues("sweep_item")); got != 2 {
		t.Fatalf("sweep-item failure metric = %v, want 2", got)
	}
}

func TestSweepDeploymentTiersAcceptsNewerObservedWriter(t *testing.T) {
	qm := &fakeQM{
		tenantPages: [][]*quartermasterpb.Tenant{{sweepTenant(sweepTenantA, "free")}},
		applyReject: true,
	}
	qm.tenantPages[0][0].BillingEntitlementsObserved = false
	r, mock := newReconcilerWithMock(t, qm)
	mock.ExpectQuery(`FROM purser\.tenant_subscriptions`).
		WillReturnRows(subscriptionTierRows([2]string{sweepTenantA, "free"}))

	repaired, err := r.SweepDeploymentTiers(context.Background())
	if err != nil {
		t.Fatalf("newer observed writer withheld handoff: %v", err)
	}
	if repaired != 0 {
		t.Fatalf("newer writer counted as this sweep's repair: %d", repaired)
	}
	if got := strings.Join(qm.calls, "|"); got != "dns:"+sweepTenantA+"=free|dns-handoff" {
		t.Fatalf("calls = %q", got)
	}
}

func TestSweepDeploymentTiers_HandoffFailureIsReturned(t *testing.T) {
	qm := &fakeQM{tenantPages: [][]*quartermasterpb.Tenant{{}}, handoffErr: errors.New("receipt unavailable")}
	r, mock := newReconcilerWithMock(t, qm)
	mock.ExpectQuery(`FROM purser\.tenant_subscriptions`).WillReturnRows(subscriptionTierRows())
	if _, err := r.SweepDeploymentTiers(context.Background()); err == nil || !strings.Contains(err.Error(), "complete DNS entitlement handoff") {
		t.Fatalf("error = %v", err)
	}
}

func TestSweepDeploymentTiers_RetriesSerializableHandoff(t *testing.T) {
	qm := &fakeQM{
		tenantPages: [][]*quartermasterpb.Tenant{{}},
		handoffErrs: []error{status.Error(codes.Aborted, "retry transaction"), nil},
	}
	r, mock := newReconcilerWithMock(t, qm)
	mock.ExpectQuery(`FROM purser\.tenant_subscriptions`).WillReturnRows(subscriptionTierRows())
	if _, err := r.SweepDeploymentTiers(context.Background()); err != nil {
		t.Fatalf("retryable handoff failed: %v", err)
	}
	if got := strings.Join(qm.calls, "|"); got != "dns-handoff|dns-handoff" {
		t.Fatalf("handoff calls = %q, want one bounded retry", got)
	}
}

func TestSweepDeploymentTiers_StopsRetryingHandoffWhenContextEnds(t *testing.T) {
	qm := &fakeQM{handoffErr: status.Error(codes.Aborted, "retry transaction")}
	r := &Reconciler{qm: qm}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := r.completeTenantDNSEntitlementHandoff(ctx, 0); !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context cancellation", err)
	}
	if len(qm.calls) != 1 {
		t.Fatalf("handoff calls = %v, want no retry after cancellation", qm.calls)
	}
}

func TestSweepDeploymentTiers_UnconfirmedHandoffIsReturned(t *testing.T) {
	qm := &fakeQM{tenantPages: [][]*quartermasterpb.Tenant{{}}, handoffReject: true}
	r, mock := newReconcilerWithMock(t, qm)
	mock.ExpectQuery(`FROM purser\.tenant_subscriptions`).WillReturnRows(subscriptionTierRows())
	if _, err := r.SweepDeploymentTiers(context.Background()); err == nil || !strings.Contains(err.Error(), "did not confirm") {
		t.Fatalf("error = %v", err)
	}
}
