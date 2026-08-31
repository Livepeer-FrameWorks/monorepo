package main

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"

	"frameworks/api_dns/internal/store"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	dnspb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/dns"
	quartermasterpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/quartermaster"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// TestLookupAbsenceOnlyTreatsNotFoundAsAuthoritative pins the Get* RPC
// contract: only store.ErrNotFound (wrapped or not) is authoritative absence
// with an empty Error; every other failure keeps its message so consumers
// preserve last-good local state.
func TestLookupAbsenceOnlyTreatsNotFoundAsAuthoritative(t *testing.T) {
	if absent, msg := lookupAbsence(store.ErrNotFound); !absent || msg != "" {
		t.Fatalf("plain not-found absent=%v msg=%q", absent, msg)
	}
	if absent, msg := lookupAbsence(fmt.Errorf("load root ca: %w", store.ErrNotFound)); !absent || msg != "" {
		t.Fatalf("wrapped not-found absent=%v msg=%q", absent, msg)
	}
	if absent, msg := lookupAbsence(errors.New("database unavailable")); absent || msg == "" {
		t.Fatalf("store failure absent=%v msg=%q", absent, msg)
	}
}

// TestTenantAckAuthorityErrorCodeTerminalOnMissingCluster pins the retry
// contract: a missing cluster identity is a permanent request defect Foghorn
// must quarantine, while any other authority failure stays retryable.
func TestTenantAckAuthorityErrorCodeTerminalOnMissingCluster(t *testing.T) {
	if code := tenantAckAuthorityErrorCode(errMissingClusterIdentity); code != codes.InvalidArgument {
		t.Fatalf("missing cluster identity code=%v", code)
	}
	if code := tenantAckAuthorityErrorCode(fmt.Errorf("wrap: %w", errMissingClusterIdentity)); code != codes.InvalidArgument {
		t.Fatalf("wrapped missing cluster identity code=%v", code)
	}
	if code := tenantAckAuthorityErrorCode(errors.New("quartermaster down")); code != codes.Unavailable {
		t.Fatalf("transient authority failure code=%v", code)
	}
}

// errorOnTenantClusterAuthority resolves one tenant active and fails for the
// rest, modelling a mid-list authority read failure.
type errorOnTenantClusterAuthority struct {
	active string
}

func (a errorOnTenantClusterAuthority) TenantAliasClusterAuthorityState(_ context.Context, tenantID, _ string) (string, error) {
	if tenantID == a.active {
		return "active", nil
	}
	return "", errors.New("authority store unavailable")
}

func (a errorOnTenantClusterAuthority) EnsureTenantAliasCluster(context.Context, string, string, int64) (bool, error) {
	return false, errors.New("authority store unavailable")
}

// TestFilterTenantBundlesMalformedDuplicateCannotGoNegative pins the counter
// invariant: a batch that repeats an already-allowed tenant during a mid-list
// authority failure must never produce negative terminal-filtered counts (a
// negative delta would panic the Prometheus counter from untrusted input).
func TestFilterTenantBundlesMalformedDuplicateCannotGoNegative(t *testing.T) {
	s := &NavigatorServer{
		TenantClusters: errorOnTenantClusterAuthority{active: "dup"},
		Logger:         logging.NewLogger(),
	}
	filter := s.filterTenantBundlesForCluster(
		context.Background(), "cluster-1",
		[]string{"tenant:dup", "tenant:other", "tenant:dup"},
		nil,
	)
	if filter.err == nil {
		t.Fatal("mid-list authority failure did not defer the batch")
	}
	if filter.filteredApplied < 0 || filter.filteredFailed < 0 {
		t.Fatalf("negative terminal-filtered counts: applied=%d failed=%d", filter.filteredApplied, filter.filteredFailed)
	}
	if filter.deferredApplied != 1 {
		t.Fatalf("deferredApplied=%d, want only the unresolved tenant", filter.deferredApplied)
	}
	if !reflect.DeepEqual(filter.applied, []string{"tenant:dup", "tenant:dup"}) {
		t.Fatalf("allowed duplicate handling changed: applied=%v", filter.applied)
	}
}

type failingTenantAckAuthority struct{}

func (failingTenantAckAuthority) ListAliasedTenantsForCluster(context.Context, string) (*quartermasterpb.ListAliasedTenantsForClusterResponse, error) {
	return nil, errors.New("quartermaster unavailable")
}

type fixedTenantAckAuthority struct {
	tenantIDs []string
}

type fixedTenantClusterAuthority map[string]string

func (a fixedTenantClusterAuthority) TenantAliasClusterAuthorityState(_ context.Context, tenantID, clusterID string) (string, error) {
	return a[tenantID+"/"+clusterID], nil
}

func (a fixedTenantClusterAuthority) EnsureTenantAliasCluster(_ context.Context, tenantID, clusterID string, _ int64) (bool, error) {
	key := tenantID + "/" + clusterID
	if a[key] == "revoked" {
		return false, nil
	}
	a[key] = "active"
	return true, nil
}

func (a fixedTenantAckAuthority) ListAliasedTenantsForCluster(context.Context, string) (*quartermasterpb.ListAliasedTenantsForClusterResponse, error) {
	resp := &quartermasterpb.ListAliasedTenantsForClusterResponse{}
	for _, tenantID := range a.tenantIDs {
		resp.Tenants = append(resp.Tenants, &quartermasterpb.AliasedTenantRef{TenantId: tenantID})
	}
	return resp, nil
}

func TestFilterTenantBundlesRequiresAuthority(t *testing.T) {
	s := &NavigatorServer{}
	result := s.filterTenantBundlesForCluster(context.Background(), "cluster-1", []string{"tenant:t1"}, nil)
	if result.err == nil {
		t.Fatal("tenant ACK was silently accepted without Quartermaster authority")
	}
}

func TestFilterNonTenantBundlesDoesNotDependOnQuartermaster(t *testing.T) {
	s := &NavigatorServer{}
	applied := []string{"cluster:c1", "platform:default"}
	failed := []string{"cluster:c2"}
	result := s.filterTenantBundlesForCluster(context.Background(), "", applied, failed)
	if result.err != nil {
		t.Fatalf("non-tenant ACK required Quartermaster: %v", result.err)
	}
	if !reflect.DeepEqual(result.applied, applied) || !reflect.DeepEqual(result.failed, failed) {
		t.Fatalf("non-tenant ACK changed: applied=%v failed=%v", result.applied, result.failed)
	}
	if result.filteredApplied != 0 || result.filteredFailed != 0 {
		t.Fatalf("non-tenant ACK reported filtered applied=%d failed=%d", result.filteredApplied, result.filteredFailed)
	}
}

func TestFilterTenantBundlesReportsAuthoritativeOmissions(t *testing.T) {
	s := &NavigatorServer{Quartermaster: fixedTenantAckAuthority{tenantIDs: []string{"allowed"}}}
	result := s.filterTenantBundlesForCluster(
		context.Background(), "cluster-1",
		[]string{"tenant:allowed", "tenant:removed", "cluster:c1"},
		[]string{"tenant:removed", "platform:default"},
	)
	if result.err != nil {
		t.Fatal(result.err)
	}
	wantApplied := []string{"tenant:allowed", "cluster:c1"}
	wantFailed := []string{"platform:default"}
	if !reflect.DeepEqual(result.applied, wantApplied) || !reflect.DeepEqual(result.failed, wantFailed) {
		t.Fatalf("filtered bundles: applied=%v failed=%v", result.applied, result.failed)
	}
	if result.filteredApplied != 1 || result.filteredFailed != 1 {
		t.Fatalf("filtered counts: applied=%d failed=%d, want 1/1", result.filteredApplied, result.filteredFailed)
	}
}

func TestFilterTenantBundlesUsesEstablishedLocalAuthorityDuringListGap(t *testing.T) {
	s := &NavigatorServer{
		Quartermaster:  failingTenantAckAuthority{},
		TenantClusters: fixedTenantClusterAuthority{"known/cluster-1": "active"},
		Logger:         logging.NewLogger(),
	}
	result := s.filterTenantBundlesForCluster(
		context.Background(), "cluster-1", []string{"tenant:known"}, []string{"tenant:known"},
	)
	if result.err != nil {
		t.Fatalf("established local tenant authority depended on Quartermaster: %v", result.err)
	}
	if !reflect.DeepEqual(result.applied, []string{"tenant:known"}) || !reflect.DeepEqual(result.failed, []string{"tenant:known"}) || result.filteredApplied != 0 || result.filteredFailed != 0 {
		t.Fatalf("established authority filtered: applied=%v failed=%v counts=%d/%d", result.applied, result.failed, result.filteredApplied, result.filteredFailed)
	}
}

func TestFilterTenantBundlesPreservesEstablishedResultsWhileFirstAdmissionRetries(t *testing.T) {
	s := &NavigatorServer{
		Quartermaster:  failingTenantAckAuthority{},
		TenantClusters: fixedTenantClusterAuthority{"known/cluster-1": "active", "revoked/cluster-1": "revoked"},
		Logger:         logging.NewLogger(),
	}
	result := s.filterTenantBundlesForCluster(
		context.Background(), "cluster-1",
		[]string{"tenant:known", "tenant:revoked", "tenant:unknown", "cluster:cluster-1"},
		[]string{"tenant:known", "tenant:revoked", "tenant:unknown", "platform:default"},
	)
	if result.err == nil {
		t.Fatal("unknown first admission did not retain the delivery for retry")
	}
	wantApplied := []string{"tenant:known", "cluster:cluster-1"}
	wantFailed := []string{"tenant:known", "platform:default"}
	if !reflect.DeepEqual(result.applied, wantApplied) || !reflect.DeepEqual(result.failed, wantFailed) {
		t.Fatalf("locally authorized results were stalled: applied=%v failed=%v", result.applied, result.failed)
	}
	if result.deferredApplied != 1 || result.deferredFailed != 1 || result.filteredApplied != 1 || result.filteredFailed != 1 {
		t.Fatalf("counts deferred=%d/%d filtered=%d/%d, want unknown 1/1 and revoked 1/1", result.deferredApplied, result.deferredFailed, result.filteredApplied, result.filteredFailed)
	}
}

func TestFilterTenantBundlesDoesNotReopenRevokedAuthority(t *testing.T) {
	authority := fixedTenantClusterAuthority{"revoked/cluster-1": "revoked"}
	s := &NavigatorServer{
		Quartermaster:  fixedTenantAckAuthority{tenantIDs: []string{"revoked"}},
		TenantClusters: authority,
	}
	result := s.filterTenantBundlesForCluster(
		context.Background(), "cluster-1", []string{"tenant:revoked"}, nil,
	)
	if result.err != nil {
		t.Fatal(result.err)
	}
	if len(result.applied) != 0 || result.filteredApplied != 1 || authority["revoked/cluster-1"] != "revoked" {
		t.Fatalf("revoked authority reopened: applied=%v filtered=%d state=%q", result.applied, result.filteredApplied, authority["revoked/cluster-1"])
	}
}

func TestReportConfigSeedApplyResultRetriesTenantACKWhenAuthorityIsUnavailable(t *testing.T) {
	s := &NavigatorServer{Quartermaster: failingTenantAckAuthority{}, Logger: logging.NewLogger()}
	resp, err := s.ReportConfigSeedApplyResult(context.Background(), &dnspb.ReportConfigSeedApplyResultRequest{
		NodeId: "node-1", ClusterId: "cluster-1", SeedVersion: 7,
		FailedBundleIds: []string{"tenant:t1"},
	})
	if resp != nil || status.Code(err) != codes.Unavailable {
		t.Fatalf("response=%v code=%s err=%v, want Unavailable", resp, status.Code(err), err)
	}
}
