package tieraccess

import (
	"context"
	"database/sql/driver"
	"errors"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	commonpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/common"
	quartermasterpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/quartermaster"
	tenantlimitspb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/tenant_limits"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

// fakeQM records the order of mutating Quartermaster calls so tests can assert
// the grant → set-primary → suspend → stamp sequence. Reads are programmed via
// the official / accessRows / primary / deploymentTier / tenantPages fields.
type fakeQM struct {
	official               []string
	accessRows             []*quartermasterpb.TenantClusterAccessRow
	primary                string
	deploymentTier         string
	customSubdomainEnabled bool
	customDomainEnabled    bool
	entitlementsUnobserved bool
	tenantPages            [][]*quartermasterpb.Tenant

	calls         []string
	lastDNS       *quartermasterpb.ApplyTenantBillingEntitlementsRequest
	listTenantsAt int
	bootstrapErr  error
	updateErr     error
	applyReject   bool
	getTenantErr  error
	deactivateErr error
	handoffErr    error
	handoffErrs   []error
	handoffReject bool
	handoffCount  int64
}

func (f *fakeQM) ListOfficialClusters(ctx context.Context) (*quartermasterpb.ListClustersResponse, error) {
	cs := make([]*quartermasterpb.InfrastructureCluster, 0, len(f.official))
	for _, id := range f.official {
		cs = append(cs, &quartermasterpb.InfrastructureCluster{ClusterId: id})
	}
	return &quartermasterpb.ListClustersResponse{Clusters: cs}, nil
}

func (f *fakeQM) ListTenantClusterAccess(ctx context.Context, tenantID string) (*quartermasterpb.ListTenantClusterAccessResponse, error) {
	return &quartermasterpb.ListTenantClusterAccessResponse{Rows: f.accessRows}, nil
}

func (f *fakeQM) GetTenant(ctx context.Context, tenantID string) (*quartermasterpb.GetTenantResponse, error) {
	if f.getTenantErr != nil {
		return nil, f.getTenantErr
	}
	tenant := &quartermasterpb.Tenant{
		Id:                          tenantID,
		DeploymentTier:              f.deploymentTier,
		CustomSubdomainEnabled:      f.customSubdomainEnabled,
		CustomDomainEnabled:         f.customDomainEnabled,
		BillingEntitlementsObserved: !f.entitlementsUnobserved,
	}
	if f.primary != "" {
		p := f.primary
		tenant.PrimaryClusterId = &p
	}
	return &quartermasterpb.GetTenantResponse{Tenant: tenant}, nil
}

func (f *fakeQM) ListTenants(ctx context.Context, _ *commonpb.CursorPaginationRequest) (*quartermasterpb.ListTenantsResponse, error) {
	if f.listTenantsAt >= len(f.tenantPages) {
		return &quartermasterpb.ListTenantsResponse{}, nil
	}
	page := f.tenantPages[f.listTenantsAt]
	f.listTenantsAt++
	hasNext := f.listTenantsAt < len(f.tenantPages)
	cursor := "cursor"
	resp := &quartermasterpb.ListTenantsResponse{
		Tenants:    page,
		Pagination: &commonpb.CursorPaginationResponse{HasNextPage: hasNext},
	}
	if hasNext {
		resp.Pagination.EndCursor = &cursor
	}
	return resp, nil
}

func (f *fakeQM) UpdateTenantCluster(ctx context.Context, req *quartermasterpb.UpdateTenantClusterRequest) error {
	f.calls = append(f.calls, "primary:"+req.GetPrimaryClusterId())
	if f.updateErr != nil {
		return f.updateErr
	}
	return nil
}

func (f *fakeQM) ApplyTenantBillingEntitlements(ctx context.Context, req *quartermasterpb.ApplyTenantBillingEntitlementsRequest) (*quartermasterpb.ApplyTenantBillingEntitlementsResponse, error) {
	f.calls = append(f.calls, "dns:"+req.GetTenantId()+"="+req.GetDeploymentTier())
	f.lastDNS = req
	if f.updateErr != nil {
		return nil, f.updateErr
	}
	return &quartermasterpb.ApplyTenantBillingEntitlementsResponse{Applied: !f.applyReject}, nil
}

func (f *fakeQM) CompleteTenantDNSEntitlementHandoff(_ context.Context, subscriptionCount int64) (*quartermasterpb.CompleteTenantDNSEntitlementHandoffResponse, error) {
	f.calls = append(f.calls, "dns-handoff")
	f.handoffCount = subscriptionCount
	if len(f.handoffErrs) != 0 {
		err := f.handoffErrs[0]
		f.handoffErrs = f.handoffErrs[1:]
		if err != nil {
			return nil, err
		}
	}
	if f.handoffErr != nil {
		return nil, f.handoffErr
	}
	return &quartermasterpb.CompleteTenantDNSEntitlementHandoffResponse{Recorded: !f.handoffReject}, nil
}

func TestRevokeDNSEntitlementsAdvancesFalseObservation(t *testing.T) {
	qm := &fakeQM{deploymentTier: "production", customSubdomainEnabled: true, customDomainEnabled: true}
	r, mock := newReconcilerWithMock(t, qm)
	if err := r.RevokeDNSEntitlements(context.Background(), "tenant-1"); err != nil {
		t.Fatalf("RevokeDNSEntitlements: %v", err)
	}
	if qm.lastDNS == nil || qm.lastDNS.GetCustomSubdomainEnabled() || qm.lastDNS.GetCustomDomainEnabled() {
		t.Fatalf("revocation request = %+v, want both grants false", qm.lastDNS)
	}
	if qm.lastDNS.GetObservedAt() == nil || qm.lastDNS.GetDeploymentTier() != "production" {
		t.Fatalf("revocation did not preserve tier/advance observation: %+v", qm.lastDNS)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func (f *fakeQM) BootstrapClusterAccess(ctx context.Context, tenantID, clusterID string, _ *tenantlimitspb.TenantResourceLimits, _ *commonpb.RequestActor) error {
	f.calls = append(f.calls, "grant:"+clusterID)
	return f.bootstrapErr
}

func (f *fakeQM) DeactivateClusterAccess(ctx context.Context, tenantID, clusterID, reason string) error {
	f.calls = append(f.calls, "suspend:"+clusterID+"("+reason+")")
	return f.deactivateErr
}

func activeOfficialRow(clusterID string) *quartermasterpb.TenantClusterAccessRow {
	return &quartermasterpb.TenantClusterAccessRow{
		ClusterId:          clusterID,
		IsActive:           true,
		IsPlatformOfficial: true,
	}
}

// pricingRows is the cluster_pricing result the reconciler reads, already in
// the SELECT's order (required_tier_level DESC, cluster_id ASC).
func pricingRows(rows ...[2]any) *sqlmock.Rows {
	r := sqlmock.NewRows([]string{"cluster_id", "required_tier_level"})
	for _, row := range rows {
		r.AddRow(row[0], row[1])
	}
	return r
}

func expectDNSEntitlements(mock sqlmock.Sqlmock, tier string, customSubdomain, customDomain bool) {
	mock.ExpectQuery(`FROM purser\.tenant_subscriptions ts`).
		WithArgs("tenant-1").
		WillReturnRows(sqlmock.NewRows([]string{"tier_name", "custom_subdomain_enabled", "custom_domain_enabled"}).
			AddRow(tier, customSubdomain, customDomain))
}

func newReconcilerWithMock(t *testing.T, qm quartermasterAPI) (*Reconciler, sqlmock.Sqlmock) {
	t.Helper()
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return &Reconciler{db: db, qm: qm, logger: logging.NewLogger()}, mock
}

// Empty official inventory cannot wipe access, but independent DNS entitlement
// state still converges from Purser.
func TestReconcile_NoOfficialClustersPreservesAccessAndChecksDNSEntitlements(t *testing.T) {
	qm := &fakeQM{official: nil, deploymentTier: "supporter", customSubdomainEnabled: true, customDomainEnabled: true}
	r, mock := newReconcilerWithMock(t, qm)
	expectDNSEntitlements(mock, "supporter", true, true)

	eligible, primary, err := r.Reconcile(context.Background(), "tenant-1", 2, "supporter")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(eligible) != 0 || primary != "" {
		t.Errorf("expected empty result, got eligible=%v primary=%q", eligible, primary)
	}
	if len(qm.calls) != 0 {
		t.Errorf("expected no mutations, got %v", qm.calls)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unexpected DB activity: %v", err)
	}
}

func TestReconcile_NoOfficialClustersStillMaterializesUnobservedDNSEntitlements(t *testing.T) {
	qm := &fakeQM{official: nil, deploymentTier: "supporter", entitlementsUnobserved: true}
	r, mock := newReconcilerWithMock(t, qm)
	expectDNSEntitlements(mock, "supporter", true, false)

	if _, _, err := r.Reconcile(context.Background(), "tenant-1", 2, "supporter"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(qm.calls) != 1 || qm.calls[0] != "dns:tenant-1=supporter" {
		t.Fatalf("DNS convergence calls = %v", qm.calls)
	}
	if qm.lastDNS == nil || !qm.lastDNS.GetCustomSubdomainEnabled() || qm.lastDNS.GetCustomDomainEnabled() {
		t.Fatalf("DNS materialization = %+v", qm.lastDNS)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// The core ordering guarantee: grants happen first, the primary is repointed
// second, suspensions third, and the deployment-tier stamp last — so
// primary_cluster_id is never left pointing at a row that has just been
// suspended, and the stamp's alias side-effects in QM see the final
// cluster-access state.
func TestReconcile_OrderingGrantThenPrimaryThenSuspendThenStamp(t *testing.T) {
	qm := &fakeQM{
		official: []string{"c1", "c2", "c3"},
		// c2 already active+official; c4 active+official but no longer eligible.
		accessRows: []*quartermasterpb.TenantClusterAccessRow{
			activeOfficialRow("c2"),
			activeOfficialRow("c4"),
		},
		primary:        "c4",   // not in the top-level subset → must move
		deploymentTier: "free", // stale stamp → must be rewritten
	}
	r, mock := newReconcilerWithMock(t, qm)
	mock.ExpectQuery(`SELECT cluster_id, required_tier_level`).
		WithArgs(sqlmock.AnyArg(), sqlmock.AnyArg()).
		WillReturnRows(pricingRows([2]any{"c1", 2}, [2]any{"c2", 1}, [2]any{"c3", 0}))
	expectDNSEntitlements(mock, "supporter", true, true)

	eligible, primary, err := r.Reconcile(context.Background(), "tenant-1", 2, "supporter")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if primary != "c1" {
		t.Errorf("primary = %q, want c1 (top required_tier_level)", primary)
	}
	if got, want := strings.Join(eligible, ","), "c1,c2,c3"; got != want {
		t.Errorf("eligible = %q, want %q", got, want)
	}

	want := []string{"grant:c1", "grant:c3", "primary:c1", "suspend:c4(tier_downgrade)", "dns:tenant-1=supporter"}
	if strings.Join(qm.calls, "|") != strings.Join(want, "|") {
		t.Errorf("call order = %v, want %v", qm.calls, want)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet DB expectations: %v", err)
	}
}

// A stamp that already matches the tenant's deployment_tier is skipped — QM
// enqueues an alias action on every tier write, so unconditional stamping
// would churn the alias outbox on every reconcile.
func TestReconcile_StampSkippedWhenTierMatches(t *testing.T) {
	qm := &fakeQM{
		official:               []string{"c1"},
		accessRows:             []*quartermasterpb.TenantClusterAccessRow{activeOfficialRow("c1")},
		primary:                "c1",
		deploymentTier:         "supporter",
		customSubdomainEnabled: true,
		customDomainEnabled:    true,
	}
	r, mock := newReconcilerWithMock(t, qm)
	mock.ExpectQuery(`SELECT cluster_id, required_tier_level`).
		WithArgs(sqlmock.AnyArg(), sqlmock.AnyArg()).
		WillReturnRows(pricingRows([2]any{"c1", 2}))
	expectDNSEntitlements(mock, "supporter", true, true)

	if _, _, err := r.Reconcile(context.Background(), "tenant-1", 2, "supporter"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(qm.calls) != 0 {
		t.Errorf("expected zero mutations, got %v", qm.calls)
	}
}

// A failing stamp is wrapped and surfaced like the other reconcile sub-steps;
// the sweep is the retry mechanism.
func TestReconcile_StampErrorIsWrapped(t *testing.T) {
	qm := &fakeQM{
		official:       []string{"c1"},
		accessRows:     []*quartermasterpb.TenantClusterAccessRow{activeOfficialRow("c1")},
		primary:        "c1",
		deploymentTier: "free",
		updateErr:      errors.New("qm down"),
	}
	r, mock := newReconcilerWithMock(t, qm)
	r.reconciliationFailures = prometheus.NewCounterVec(prometheus.CounterOpts{Name: "test_reconciliation_failures_total"}, []string{"operation"})
	mock.ExpectQuery(`SELECT cluster_id, required_tier_level`).
		WithArgs(sqlmock.AnyArg(), sqlmock.AnyArg()).
		WillReturnRows(pricingRows([2]any{"c1", 2}))
	expectDNSEntitlements(mock, "supporter", true, true)

	_, _, err := r.Reconcile(context.Background(), "tenant-1", 2, "supporter")
	if err == nil || !strings.Contains(err.Error(), "apply DNS entitlements") {
		t.Fatalf("expected wrapped stamp error, got %v", err)
	}
	if got := testutil.ToFloat64(r.reconciliationFailures.WithLabelValues("reconcile")); got != 1 {
		t.Fatalf("reconcile failure metric = %v, want 1", got)
	}
}

// Tie-break: when the current primary is still among the highest-tier
// clusters, keep it — no UpdateTenant churn on an equivalent reshuffle.
func TestReconcile_PrimaryTieBreakKeepsExistingPrimary(t *testing.T) {
	qm := &fakeQM{
		official: []string{"c1", "c2"},
		accessRows: []*quartermasterpb.TenantClusterAccessRow{
			activeOfficialRow("c1"),
			activeOfficialRow("c2"),
		},
		primary:        "c2", // already a top-level candidate
		deploymentTier: "free",
	}
	r, mock := newReconcilerWithMock(t, qm)
	// Both clusters at the same required_tier_level → topLevelCandidates = [c1, c2].
	mock.ExpectQuery(`SELECT cluster_id, required_tier_level`).
		WithArgs(sqlmock.AnyArg(), sqlmock.AnyArg()).
		WillReturnRows(pricingRows([2]any{"c1", 2}, [2]any{"c2", 2}))
	expectDNSEntitlements(mock, "free", false, false)

	_, primary, err := r.Reconcile(context.Background(), "tenant-1", 2, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if primary != "c2" {
		t.Errorf("primary = %q, want c2 (existing primary retained)", primary)
	}
	// No grants (both already active), no suspends (both eligible), and crucially
	// no primary update — the existing primary was kept.
	if len(qm.calls) != 0 {
		t.Errorf("expected zero mutations, got %v", qm.calls)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet DB expectations: %v", err)
	}
}

// The tenant's tier level is forwarded to the eligibility query verbatim — not
// a hardcoded default — so a tier-0 (free) reconcile asks for the free-eligible
// set rather than the paid set.
func TestReconcile_TierLevelForwardedToQuery(t *testing.T) {
	qm := &fakeQM{official: []string{"c1"}, primary: "c1", accessRows: []*quartermasterpb.TenantClusterAccessRow{activeOfficialRow("c1")}}
	r, mock := newReconcilerWithMock(t, qm)

	tierArg := argMatch(func(v driver.Value) bool {
		n, ok := asInt64(v)
		return ok && n == 0
	})
	mock.ExpectQuery(`FROM purser\.cluster_pricing`).
		WithArgs(sqlmock.AnyArg(), tierArg).
		WillReturnRows(pricingRows([2]any{"c1", 0}))
	expectDNSEntitlements(mock, "free", false, false)

	if _, _, err := r.Reconcile(context.Background(), "tenant-1", 0, ""); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("tierLevel arg not forwarded as expected: %v", err)
	}
}

// A failing Quartermaster RPC mid-reconcile is wrapped and surfaced; the
// partially-computed eligible set is still returned so the caller can log it.
func TestReconcile_GrantErrorIsWrappedAndPartialReturned(t *testing.T) {
	qm := &fakeQM{
		official:     []string{"c1", "c2"},
		bootstrapErr: errors.New("qm down"),
	}
	r, mock := newReconcilerWithMock(t, qm)
	mock.ExpectQuery(`SELECT cluster_id, required_tier_level`).
		WithArgs(sqlmock.AnyArg(), sqlmock.AnyArg()).
		WillReturnRows(pricingRows([2]any{"c1", 1}, [2]any{"c2", 0}))

	eligible, _, err := r.Reconcile(context.Background(), "tenant-1", 1, "supporter")
	if err == nil || !strings.Contains(err.Error(), "grant cluster access") {
		t.Fatalf("expected wrapped grant error, got %v", err)
	}
	// eligible was fully scanned before the grant loop, so it is returned intact.
	if got, want := strings.Join(eligible, ","), "c1,c2"; got != want {
		t.Errorf("partial eligible = %q, want %q", got, want)
	}
}

type argMatch func(driver.Value) bool

func (f argMatch) Match(v driver.Value) bool { return f(v) }

func asInt64(v driver.Value) (int64, bool) {
	switch n := v.(type) {
	case int64:
		return n, true
	case int32:
		return int64(n), true
	case int:
		return int64(n), true
	default:
		return 0, false
	}
}
