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
		serviceAddrs: map[string][]ServiceAddress{
			"foghorn": {{NodeID: "foghorn-a", IP: "198.51.100.20"}},
		},
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
	for _, name := range []string{"acme", "edge.acme", "edge-ingest.acme", "edge-egress.acme", "edge-storage.acme", "edge-processing.acme"} {
		records := dns.records[name]
		if len(records) != 1 || records[0].Value != "203.0.113.10" {
			t.Fatalf("record %s = %#v, want only node-a address", name, records)
		}
		if records[0].SmartRoutingType != bunny.SmartRoutingNone {
			t.Fatalf("record %s SmartRoutingType = %d, want none without coordinates", name, records[0].SmartRoutingType)
		}
		if records[0].GeolocationLatitude != nil || records[0].GeolocationLongitude != nil {
			t.Fatalf("record %s has unexpected coordinates: %#v", name, records[0])
		}
	}
	if records := dns.records["foghorn.acme"]; len(records) != 1 || records[0].Value != "198.51.100.20" {
		t.Fatalf("foghorn.acme = %#v, want Foghorn service address", records)
	}
	for _, name := range []string{"chandler.acme", "livepeer.acme"} {
		if records := dns.records[name]; len(records) != 0 {
			t.Fatalf("%s = %#v, want retired tenant alias label cleared", name, records)
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

func TestRetirementPassClearsRetiredLabel(t *testing.T) {
	ctx := context.Background()
	st := newFakeTenantAliasStore()
	// Active alias is the NEW label; the OLD label is retired.
	st.alias = &store.TenantAlias{TenantID: "tenant-1", Subdomain: "newlabel", Status: "cert_issued", UpdatedAt: time.Now()}
	st.retirements = []store.TenantAliasRetirement{
		{TenantID: "tenant-1", Subdomain: "oldlabel", RequestedAt: time.Now()},
	}
	dns := &fakeTenantAliasDNS{zoneFound: true}
	worker := newTestAliasWorker(st, dns, &fakeTenantEdgeResolver{})

	worker.processRetirements(ctx)

	if dns.reconcileCalls == 0 {
		t.Fatal("expected old-label records to be cleared")
	}
	for name := range dns.records {
		if name == "newlabel" || name == "foghorn.newlabel" {
			t.Fatalf("active label %q must not be touched", name)
		}
	}
	if len(st.deletedRetirements) != 1 || st.deletedRetirements[0] != "oldlabel" {
		t.Fatalf("deletedRetirements = %v, want [oldlabel]", st.deletedRetirements)
	}
	if len(st.retirementFailures) != 0 {
		t.Fatalf("retirementFailures = %v, want none", st.retirementFailures)
	}
}

func TestRetirementPassDropsStaleRepointedLabel(t *testing.T) {
	ctx := context.Background()
	st := newFakeTenantAliasStore()
	// a -> b -> a: the label is active again, re-pointed AFTER the retirement
	// was requested. The retirement is stale: drop it without clearing live
	// records.
	st.alias = &store.TenantAlias{TenantID: "tenant-1", Subdomain: "acme", Status: "cert_issued", UpdatedAt: time.Now()}
	st.retirements = []store.TenantAliasRetirement{
		{TenantID: "tenant-1", Subdomain: "acme", RequestedAt: time.Now().Add(-time.Hour)},
	}
	dns := &fakeTenantAliasDNS{zoneFound: true}
	worker := newTestAliasWorker(st, dns, &fakeTenantEdgeResolver{})

	worker.processRetirements(ctx)

	if dns.reconcileCalls != 0 {
		t.Fatalf("reconcile calls = %d, want 0 (live label must not be cleared)", dns.reconcileCalls)
	}
	if len(st.deletedRetirements) != 1 || st.deletedRetirements[0] != "acme" {
		t.Fatalf("deletedRetirements = %v, want [acme] (stale retirement dropped)", st.deletedRetirements)
	}
}

func TestRetirementPassKeepsUnsupersededActiveLabel(t *testing.T) {
	ctx := context.Background()
	st := newFakeTenantAliasStore()
	// R == active but NOT superseded (alias updated before the retirement was
	// requested): an upstream bug. Keep pending, never clear the live label.
	st.alias = &store.TenantAlias{TenantID: "tenant-1", Subdomain: "acme", Status: "cert_issued", UpdatedAt: time.Now().Add(-time.Hour)}
	st.retirements = []store.TenantAliasRetirement{
		{TenantID: "tenant-1", Subdomain: "acme", RequestedAt: time.Now()},
	}
	dns := &fakeTenantAliasDNS{zoneFound: true}
	worker := newTestAliasWorker(st, dns, &fakeTenantEdgeResolver{})

	worker.processRetirements(ctx)

	if dns.reconcileCalls != 0 {
		t.Fatalf("reconcile calls = %d, want 0", dns.reconcileCalls)
	}
	if len(st.deletedRetirements) != 0 {
		t.Fatalf("deletedRetirements = %v, want none (kept pending)", st.deletedRetirements)
	}
	if len(st.retirements) != 1 {
		t.Fatalf("retirements length = %d, want 1 (still pending)", len(st.retirements))
	}
}

func TestRetirementPassRecordsFailureOnBunnyError(t *testing.T) {
	ctx := context.Background()
	st := newFakeTenantAliasStore()
	st.alias = &store.TenantAlias{TenantID: "tenant-1", Subdomain: "newlabel", Status: "cert_issued", UpdatedAt: time.Now()}
	st.retirements = []store.TenantAliasRetirement{
		{TenantID: "tenant-1", Subdomain: "oldlabel", RequestedAt: time.Now()},
	}
	dns := &fakeTenantAliasDNS{zoneFound: true, failName: "foghorn.oldlabel"}
	worker := newTestAliasWorker(st, dns, &fakeTenantEdgeResolver{})

	worker.processRetirements(ctx)

	if len(st.retirementFailures) != 1 || st.retirementFailures[0] != "oldlabel" {
		t.Fatalf("retirementFailures = %v, want [oldlabel]", st.retirementFailures)
	}
	if len(st.deletedRetirements) != 0 {
		t.Fatalf("deletedRetirements = %v, want none (failed clear stays pending)", st.deletedRetirements)
	}
}

func newTestAliasWorker(st *fakeTenantAliasStore, dns *fakeTenantAliasDNS, resolver *fakeTenantEdgeResolver) *AliasApplyStateWorker {
	logger := logging.NewLogger()
	logger.SetOutput(io.Discard)
	return &AliasApplyStateWorker{
		store:              st,
		bunny:              dns,
		edges:              resolver,
		logger:             logger,
		interval:           time.Second,
		rootDomain:         "frameworks.network",
		tenantZoneLabel:    "cdn",
		healthStaleSeconds: 300,
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
	alias              *store.TenantAlias
	rows               []store.TenantEdgeApplyState
	deletedEdges       bool
	deletedAlias       bool
	retirements        []store.TenantAliasRetirement
	deletedRetirements []string
	retirementFailures []string
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

func (s *fakeTenantAliasStore) ListTenantAliasRetirements(context.Context) ([]store.TenantAliasRetirement, error) {
	return s.retirements, nil
}

func (s *fakeTenantAliasStore) DeleteTenantAliasRetirement(_ context.Context, _, subdomain string) error {
	s.deletedRetirements = append(s.deletedRetirements, subdomain)
	for i := range s.retirements {
		if s.retirements[i].Subdomain == subdomain {
			s.retirements = append(s.retirements[:i], s.retirements[i+1:]...)
			break
		}
	}
	return nil
}

func (s *fakeTenantAliasStore) RecordTenantAliasRetirementFailure(_ context.Context, _, subdomain, _ string) error {
	s.retirementFailures = append(s.retirementFailures, subdomain)
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
	serviceAddrs   map[string][]ServiceAddress
	serviceErr     error
	active         map[string]bool
	eligibilityErr error
}

func (r *fakeTenantEdgeResolver) ResolveEdgeAddresses(_ context.Context, nodeID string) ([]string, []string, error) {
	return r.addrs[nodeID], nil, nil
}

func (r *fakeTenantEdgeResolver) ResolveServiceAddressesForClusters(_ context.Context, serviceType string, _ []string, _ int) ([]ServiceAddress, error) {
	if r.serviceErr != nil {
		return nil, r.serviceErr
	}
	if addrs, ok := r.serviceAddrs[serviceType]; ok {
		return addrs, nil
	}
	if !isTenantEdgeServiceType(serviceType) {
		return nil, nil
	}
	var out []ServiceAddress
	for nodeID, ips := range r.addrs {
		for _, ip := range ips {
			out = append(out, ServiceAddress{NodeID: nodeID, IP: ip})
		}
	}
	return out, nil
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
