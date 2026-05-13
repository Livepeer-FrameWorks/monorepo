package worker

import (
	"context"
	"errors"
	"io"
	"testing"
	"time"

	"frameworks/api_dns/internal/provider/bunny"
	"frameworks/api_dns/internal/store"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
)

func TestPublishTenantAliasDowngradesUnpublishedInDNSRows(t *testing.T) {
	ctx := context.Background()
	st := newFakeTenantAliasStore()
	st.alias = &store.TenantAlias{TenantID: "tenant-1", Subdomain: "acme", Status: "cert_issued"}
	st.rows = []store.TenantEdgeApplyState{
		tenantEdge("tenant-1", "cluster-a", "node-a", "in_dns"),
		tenantEdge("tenant-1", "cluster-b", "node-b", "in_dns"),
	}
	dns := &fakeTenantAliasDNS{zoneFound: true}
	resolver := &fakeTenantEdgeResolver{
		addrs: map[string][]string{"node-a": {"203.0.113.10"}},
		active: map[string]bool{
			"tenant-1/cluster-a": true,
			"tenant-1/cluster-b": false,
		},
	}
	worker := newTestAliasWorker(st, dns, resolver)

	if err := worker.PublishTenantAlias(ctx, "tenant-1"); err != nil {
		t.Fatalf("PublishTenantAlias: %v", err)
	}

	if got := st.stateFor("node-a"); got != "in_dns" {
		t.Fatalf("node-a state = %q, want in_dns", got)
	}
	if got := st.stateFor("node-b"); got != "applied" {
		t.Fatalf("node-b state = %q, want applied", got)
	}
	for name, records := range dns.records {
		if len(records) != 1 || records[0].Value != "203.0.113.10" {
			t.Fatalf("record %s = %#v, want only node-a address", name, records)
		}
	}
}

func TestPublishTenantAliasPreservesDNSOnEligibilityLookupError(t *testing.T) {
	ctx := context.Background()
	st := newFakeTenantAliasStore()
	st.alias = &store.TenantAlias{TenantID: "tenant-1", Subdomain: "acme", Status: "cert_issued"}
	st.rows = []store.TenantEdgeApplyState{
		tenantEdge("tenant-1", "cluster-a", "node-a", "in_dns"),
	}
	dns := &fakeTenantAliasDNS{zoneFound: true}
	resolver := &fakeTenantEdgeResolver{
		eligibilityErr: errors.New("quartermaster unavailable"),
		active:         map[string]bool{"tenant-1/cluster-a": true},
	}
	worker := newTestAliasWorker(st, dns, resolver)

	if err := worker.PublishTenantAlias(ctx, "tenant-1"); err == nil {
		t.Fatal("expected eligibility error")
	}
	if dns.reconcileCalls != 0 {
		t.Fatalf("reconcile calls = %d, want 0", dns.reconcileCalls)
	}
	if got := st.stateFor("node-a"); got != "in_dns" {
		t.Fatalf("node-a state = %q, want in_dns", got)
	}
}

func TestPublishTenantAliasDowngradesInDNSRowWithoutAddresses(t *testing.T) {
	ctx := context.Background()
	st := newFakeTenantAliasStore()
	st.alias = &store.TenantAlias{TenantID: "tenant-1", Subdomain: "acme", Status: "cert_issued"}
	st.rows = []store.TenantEdgeApplyState{
		tenantEdge("tenant-1", "cluster-a", "node-a", "in_dns"),
	}
	dns := &fakeTenantAliasDNS{zoneFound: true}
	resolver := &fakeTenantEdgeResolver{
		addrs:  map[string][]string{"node-a": nil},
		active: map[string]bool{"tenant-1/cluster-a": true},
	}
	worker := newTestAliasWorker(st, dns, resolver)

	if err := worker.PublishTenantAlias(ctx, "tenant-1"); err != nil {
		t.Fatalf("PublishTenantAlias: %v", err)
	}
	if got := st.stateFor("node-a"); got != "applied" {
		t.Fatalf("node-a state = %q, want applied", got)
	}
	for name, records := range dns.records {
		if len(records) != 0 {
			t.Fatalf("record %s = %#v, want cleared", name, records)
		}
	}
}

func TestTeardownKeepsLocalStateWhenDNSClearFails(t *testing.T) {
	ctx := context.Background()
	st := newFakeTenantAliasStore()
	dns := &fakeTenantAliasDNS{
		zoneFound: true,
		failName:  "foghorn.acme",
	}
	worker := newTestAliasWorker(st, dns, &fakeTenantEdgeResolver{})

	worker.teardown(ctx, store.TenantAlias{TenantID: "tenant-1", Subdomain: "acme", Status: "tearing_down"})

	if st.deletedEdges || st.deletedAlias {
		t.Fatalf("deletedEdges=%v deletedAlias=%v, want both false after DNS failure", st.deletedEdges, st.deletedAlias)
	}
}

func TestTeardownDeletesLocalStateAfterDNSClearSucceeds(t *testing.T) {
	ctx := context.Background()
	st := newFakeTenantAliasStore()
	dns := &fakeTenantAliasDNS{zoneFound: true}
	worker := newTestAliasWorker(st, dns, &fakeTenantEdgeResolver{})

	worker.teardown(ctx, store.TenantAlias{TenantID: "tenant-1", Subdomain: "acme", Status: "tearing_down"})

	if !st.deletedEdges || !st.deletedAlias {
		t.Fatalf("deletedEdges=%v deletedAlias=%v, want both true", st.deletedEdges, st.deletedAlias)
	}
}

func newTestAliasWorker(st *fakeTenantAliasStore, dns *fakeTenantAliasDNS, resolver *fakeTenantEdgeResolver) *AliasApplyStateWorker {
	logger := logging.NewLogger()
	logger.SetOutput(io.Discard)
	return &AliasApplyStateWorker{
		store:           st,
		bunny:           dns,
		edges:           resolver,
		logger:          logger,
		interval:        time.Second,
		rootDomain:      "frameworks.network",
		tenantZoneLabel: "cdn",
	}
}

func tenantEdge(tenantID, clusterID, nodeID, state string) store.TenantEdgeApplyState {
	return store.TenantEdgeApplyState{
		TenantID:  tenantID,
		ClusterID: clusterID,
		NodeID:    nodeID,
		BundleID:  "tenant:" + tenantID,
		State:     state,
	}
}

type fakeTenantAliasStore struct {
	alias        *store.TenantAlias
	rows         []store.TenantEdgeApplyState
	deletedEdges bool
	deletedAlias bool
}

func newFakeTenantAliasStore() *fakeTenantAliasStore {
	return &fakeTenantAliasStore{}
}

func (s *fakeTenantAliasStore) ListPendingTenantAliases(context.Context) ([]store.TenantAlias, error) {
	return nil, nil
}

func (s *fakeTenantAliasStore) ListTenantAliasesByStatus(context.Context, []string) ([]store.TenantAlias, error) {
	if s.alias == nil {
		return nil, nil
	}
	return []store.TenantAlias{*s.alias}, nil
}

func (s *fakeTenantAliasStore) GetTenantAlias(context.Context, string) (*store.TenantAlias, error) {
	if s.alias == nil {
		return nil, store.ErrNotFound
	}
	return s.alias, nil
}

func (s *fakeTenantAliasStore) UpsertTenantEdgeApplyState(_ context.Context, st *store.TenantEdgeApplyState) error {
	for i := range s.rows {
		if s.rows[i].NodeID == st.NodeID && s.rows[i].BundleID == st.BundleID {
			s.rows[i] = *st
			return nil
		}
	}
	s.rows = append(s.rows, *st)
	return nil
}

func (s *fakeTenantAliasStore) ListTenantEdgeApplyState(_ context.Context, _ string, stateFilter string) ([]store.TenantEdgeApplyState, error) {
	out := make([]store.TenantEdgeApplyState, 0, len(s.rows))
	for _, row := range s.rows {
		if stateFilter == "" || row.State == stateFilter {
			out = append(out, row)
		}
	}
	return out, nil
}

func (s *fakeTenantAliasStore) DeleteTenantEdgeApplyState(context.Context, string) error {
	s.deletedEdges = true
	return nil
}

func (s *fakeTenantAliasStore) DeleteTenantAlias(context.Context, string) error {
	s.deletedAlias = true
	return nil
}

func (s *fakeTenantAliasStore) stateFor(nodeID string) string {
	for _, row := range s.rows {
		if row.NodeID == nodeID {
			return row.State
		}
	}
	return ""
}

type fakeTenantEdgeResolver struct {
	addrs          map[string][]string
	active         map[string]bool
	eligibilityErr error
}

func (r *fakeTenantEdgeResolver) ResolveEdgeAddresses(_ context.Context, nodeID string) ([]string, []string, error) {
	return r.addrs[nodeID], nil, nil
}

func (r *fakeTenantEdgeResolver) TenantActiveInCluster(_ context.Context, tenantID, clusterID string) (bool, error) {
	if r.eligibilityErr != nil {
		return false, r.eligibilityErr
	}
	if r.active == nil {
		return true, nil
	}
	return r.active[tenantID+"/"+clusterID], nil
}

type fakeTenantAliasDNS struct {
	zoneFound      bool
	findErr        error
	failName       string
	reconcileCalls int
	records        map[string][]bunny.Record
}

func (d *fakeTenantAliasDNS) FindZone(context.Context, string) (*bunny.Zone, bool, error) {
	if d.findErr != nil {
		return nil, false, d.findErr
	}
	if !d.zoneFound {
		return nil, false, nil
	}
	return &bunny.Zone{ID: 123, Domain: "cdn.frameworks.network"}, true, nil
}

func (d *fakeTenantAliasDNS) ReconcileRecordSet(_ context.Context, _ int64, name string, _ int, desired []bunny.Record) error {
	d.reconcileCalls++
	if name == d.failName {
		return errors.New("bunny failed")
	}
	if d.records == nil {
		d.records = map[string][]bunny.Record{}
	}
	d.records[name] = append([]bunny.Record(nil), desired...)
	return nil
}
