package worker

import (
	"context"
	"database/sql"
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

func TestPublishTenantAliasDoesNotReplaySnapshotAdvancedDuringDNSWrite(t *testing.T) {
	ctx := context.Background()
	st := newFakeTenantAliasStore()
	st.alias = &store.TenantAlias{TenantID: "tenant-1", Subdomain: "acme", Status: "cert_issued"}
	snapshot := tenantEdge("tenant-1", "cluster-a", "node-a", "applied")
	snapshot.LastSeedVersion = sql.NullInt64{Int64: 7, Valid: true}
	snapshot.LastDeliverySequence = 11
	st.rows = []store.TenantEdgeApplyState{snapshot}
	dns := &fakeTenantAliasDNS{zoneFound: true}
	dns.onReconcile = func() {
		// A newer failure ACK lands while the worker is reconciling external DNS.
		// The subsequent promotion must compare against the old snapshot and lose.
		st.rows[0].State = "pending_apply"
		st.rows[0].LastDeliverySequence = 12
		st.rows[0].InDNSAt = sql.NullTime{}
	}
	resolver := &fakeTenantEdgeResolver{
		addrs: map[string][]string{"node-a": {"203.0.113.10"}},
	}
	worker := newTestAliasWorker(st, dns, resolver)

	if err := worker.PublishTenantAlias(ctx, "tenant-1"); err != nil {
		t.Fatalf("PublishTenantAlias: %v", err)
	}
	got := st.rows[0]
	if got.State != "pending_apply" || got.LastSeedVersion != snapshot.LastSeedVersion || got.LastDeliverySequence != 12 || got.InDNSAt.Valid {
		t.Fatalf("newer failure ACK was overwritten by stale DNS write-back: %#v", got)
	}
}

func TestPublishTenantAliasDoesNotShrinkDNSOnIncompleteTenantListView(t *testing.T) {
	ctx := context.Background()
	st := newFakeTenantAliasStore()
	st.alias = &store.TenantAlias{TenantID: "tenant-1", Subdomain: "acme", Status: "cert_issued"}
	st.rows = []store.TenantEdgeApplyState{
		tenantEdge("tenant-1", "cluster-a", "node-a", "in_dns"),
	}
	dns := &fakeTenantAliasDNS{zoneFound: true}
	resolver := &fakeTenantEdgeResolver{
		addrs: map[string][]string{"node-a": {"203.0.113.10"}},
	}
	worker := newTestAliasWorker(st, dns, resolver)

	if err := worker.PublishTenantAlias(ctx, "tenant-1"); err != nil {
		t.Fatalf("PublishTenantAlias: %v", err)
	}
	if records := dns.records["acme"]; len(records) != 1 || records[0].Value != "203.0.113.10" {
		t.Fatalf("incomplete control-plane tenant list shrank DNS: %#v", records)
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
		addrs: map[string][]string{"node-a": nil},
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

func TestPublishTenantAliasExcludesVersionlessAndStaleBundleACKs(t *testing.T) {
	ctx := context.Background()
	st := newFakeTenantAliasStore()
	st.alias = &store.TenantAlias{TenantID: "tenant-1", Subdomain: "acme", Status: "cert_issued"}
	versionless := tenantEdge("tenant-1", "cluster-a", "node-a", "applied")
	versionless.BundleVersion = ""
	staleRevision := tenantEdge("tenant-1", "cluster-a", "node-b", "in_dns")
	staleRevision.BundleVersion = "revision-old"
	grandfathered := tenantEdge("tenant-1", "cluster-a", "node-c", "in_dns")
	grandfathered.BundleVersion = ""
	// Expand precedes the data backfill, so the current bundle can exist while
	// its compatibility revision is still empty. Presence, not revision text,
	// preserves an already-published row through that bounded window.
	grandfathered.CurrentBundleVersion = ""
	st.rows = []store.TenantEdgeApplyState{versionless, staleRevision, grandfathered}
	dns := &fakeTenantAliasDNS{zoneFound: true}
	resolver := &fakeTenantEdgeResolver{
		addrs: map[string][]string{
			"node-a": {"203.0.113.10"},
			"node-b": {"203.0.113.11"},
			"node-c": {"203.0.113.12"},
		},
	}
	worker := newTestAliasWorker(st, dns, resolver)

	if err := worker.PublishTenantAlias(ctx, "tenant-1"); err != nil {
		t.Fatalf("PublishTenantAlias: %v", err)
	}
	if records := dns.records["acme"]; len(records) != 1 || records[0].Value != "203.0.113.12" {
		t.Fatalf("entry gate or rolling-upgrade continuity is wrong: %#v", records)
	}
	if got := st.stateFor("node-a"); got != "applied" {
		t.Fatalf("versionless node state=%q, want applied but DNS-ineligible", got)
	}
	if got := st.stateFor("node-b"); got != "applied" {
		t.Fatalf("stale-revision in_dns node state=%q, want applied", got)
	}
	if got := st.stateFor("node-c"); got != "in_dns" {
		t.Fatalf("versionless established node state=%q, want grandfathered in_dns", got)
	}
}

func TestTeardownKeepsLocalStateWhenDNSClearFails(t *testing.T) {
	ctx := context.Background()
	st := newFakeTenantAliasStore()
	st.alias = &store.TenantAlias{TenantID: "tenant-1", Subdomain: "acme", Status: "tearing_down"}
	dns := &fakeTenantAliasDNS{
		zoneFound: true,
		failName:  "foghorn.acme",
	}
	worker := newTestAliasWorker(st, dns, &fakeTenantEdgeResolver{})

	worker.teardown(ctx, store.TenantAlias{TenantID: "tenant-1", Subdomain: "acme", Status: "tearing_down"})

	if st.deletedAlias {
		t.Fatal("alias deleted after DNS failure")
	}
}

func TestTeardownDeletesLocalStateAfterDNSClearSucceeds(t *testing.T) {
	ctx := context.Background()
	st := newFakeTenantAliasStore()
	st.alias = &store.TenantAlias{TenantID: "tenant-1", Subdomain: "acme", Status: "tearing_down"}
	dns := &fakeTenantAliasDNS{zoneFound: true}
	worker := newTestAliasWorker(st, dns, &fakeTenantEdgeResolver{})

	worker.teardown(ctx, store.TenantAlias{TenantID: "tenant-1", Subdomain: "acme", Status: "tearing_down"})

	if !st.deletedAlias {
		t.Fatal("alias was not deleted after DNS teardown")
	}
}

func TestTeardownRechecksAuthorityBeforeClearingDNS(t *testing.T) {
	ctx := context.Background()
	st := newFakeTenantAliasStore()
	st.alias = &store.TenantAlias{TenantID: "tenant-1", Subdomain: "acme", Status: "tearing_down"}
	st.getAliasHook = func() { st.alias.Status = "cert_issuing" }
	dns := &fakeTenantAliasDNS{zoneFound: true}
	worker := newTestAliasWorker(st, dns, &fakeTenantEdgeResolver{})

	worker.teardown(ctx, store.TenantAlias{TenantID: "tenant-1", Subdomain: "acme", Status: "tearing_down"})

	if dns.reconcileCalls != 0 || st.deletedAlias {
		t.Fatalf("reactivated alias teardown touched DNS=%d deleted=%v", dns.reconcileCalls, st.deletedAlias)
	}
}

func TestRetirementPassClearsRetiredLabel(t *testing.T) {
	ctx := context.Background()
	st := newFakeTenantAliasStore()
	// Active alias is the NEW label; the OLD label is retired.
	st.alias = &store.TenantAlias{TenantID: "tenant-1", Subdomain: "newlabel", Status: "cert_issued", UpdatedAt: time.Now()}
	st.dnsReady = true
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

func TestRetirementPassWaitsForReplacementBundleInDNS(t *testing.T) {
	for _, test := range []struct {
		name     string
		status   string
		dnsReady bool
	}{
		{name: "certificate pending", status: "cert_issuing", dnsReady: false},
		{name: "certificate failed", status: "cert_failed", dnsReady: false},
		{name: "edge apply pending", status: "cert_issued", dnsReady: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			st := newFakeTenantAliasStore()
			st.alias = &store.TenantAlias{TenantID: "tenant-1", Subdomain: "newlabel", Status: test.status}
			st.dnsReady = test.dnsReady
			st.retirements = []store.TenantAliasRetirement{{TenantID: "tenant-1", Subdomain: "oldlabel"}}
			dns := &fakeTenantAliasDNS{zoneFound: true}
			worker := newTestAliasWorker(st, dns, &fakeTenantEdgeResolver{})

			worker.processRetirements(context.Background())

			if dns.reconcileCalls != 0 || len(st.deletedRetirements) != 0 {
				t.Fatalf("replacement not ready but DNS calls=%d deleted=%v", dns.reconcileCalls, st.deletedRetirements)
			}
		})
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
	st.dnsReady = true
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

func TestPublishTenantAliasDemotesExpiredLegacyContinuityMember(t *testing.T) {
	ctx := context.Background()
	st := newFakeTenantAliasStore()
	st.alias = &store.TenantAlias{TenantID: "tenant-1", Subdomain: "acme", Status: "cert_issued"}
	expired := tenantEdge("tenant-1", "cluster-a", "node-a", "in_dns")
	expired.BundleVersion = ""
	expired.CurrentBundleVersion = ""
	expired.UpdatedAt = time.Now().Add(-legacyContinuityMaxAge - time.Hour)
	current := tenantEdge("tenant-1", "cluster-a", "node-b", "in_dns")
	st.rows = []store.TenantEdgeApplyState{expired, current}
	dns := &fakeTenantAliasDNS{zoneFound: true}
	resolver := &fakeTenantEdgeResolver{
		addrs: map[string][]string{
			"node-a": {"203.0.113.10"},
			"node-b": {"203.0.113.11"},
		},
	}
	worker := newTestAliasWorker(st, dns, resolver)

	if err := worker.PublishTenantAlias(ctx, "tenant-1"); err != nil {
		t.Fatalf("PublishTenantAlias: %v", err)
	}
	if records := dns.records["acme"]; len(records) != 1 || records[0].Value != "203.0.113.11" {
		t.Fatalf("expired continuity member kept DNS membership: %#v", records)
	}
	if got := st.stateFor("node-a"); got != "applied" {
		t.Fatalf("expired continuity node state=%q, want demoted to applied", got)
	}
}

// tenantEdge mirrors a persisted row: updated_at is always set by the store.
func tenantEdge(tenantID, clusterID, nodeID, state string) store.TenantEdgeApplyState {
	return store.TenantEdgeApplyState{
		TenantID:             tenantID,
		ClusterID:            clusterID,
		NodeID:               nodeID,
		BundleID:             "tenant:" + tenantID,
		BundleVersion:        "revision-1",
		CurrentBundleVersion: "revision-1",
		CurrentBundlePresent: true,
		State:                state,
		UpdatedAt:            time.Now(),
	}
}

type fakeTenantAliasStore struct {
	alias              *store.TenantAlias
	rows               []store.TenantEdgeApplyState
	deletedAlias       bool
	retirements        []store.TenantAliasRetirement
	deletedRetirements []string
	retirementFailures []string
	dnsReady           bool
	getAliasHook       func()
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
	if s.getAliasHook != nil {
		hook := s.getAliasHook
		s.getAliasHook = nil
		hook()
	}
	if s.alias == nil {
		return nil, store.ErrNotFound
	}
	return s.alias, nil
}

func (s *fakeTenantAliasStore) TenantAliasHasDNS(context.Context, string) (bool, error) {
	return s.dnsReady, nil
}

func (s *fakeTenantAliasStore) MarkTenantEdgeInDNS(_ context.Context, st *store.TenantEdgeApplyState) (bool, error) {
	for i := range s.rows {
		if sameTenantEdgeSnapshot(s.rows[i], *st) && s.rows[i].State == "applied" {
			s.rows[i].State = "in_dns"
			s.rows[i].InDNSAt = sql.NullTime{Time: time.Now(), Valid: true}
			return true, nil
		}
	}
	return false, nil
}

func (s *fakeTenantAliasStore) MarkTenantEdgeNotInDNS(_ context.Context, st *store.TenantEdgeApplyState) (bool, error) {
	for i := range s.rows {
		if sameTenantEdgeSnapshot(s.rows[i], *st) && s.rows[i].State == "in_dns" {
			s.rows[i].State = "applied"
			s.rows[i].InDNSAt = sql.NullTime{}
			return true, nil
		}
	}
	return false, nil
}

func sameTenantEdgeSnapshot(current, snapshot store.TenantEdgeApplyState) bool {
	return current.TenantID == snapshot.TenantID &&
		current.NodeID == snapshot.NodeID &&
		current.BundleID == snapshot.BundleID &&
		current.BundleVersion == snapshot.BundleVersion &&
		current.LastSeedVersion == snapshot.LastSeedVersion &&
		current.LastDeliverySequence == snapshot.LastDeliverySequence
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

func (s *fakeTenantAliasStore) DeleteTenantAlias(context.Context, string) (bool, error) {
	s.deletedAlias = true
	return true, nil
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
	addrs        map[string][]string
	serviceAddrs map[string][]ServiceAddress
	serviceErr   error
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

type fakeTenantAliasDNS struct {
	zoneFound      bool
	findErr        error
	failName       string
	onReconcile    func()
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
	if d.onReconcile != nil {
		onReconcile := d.onReconcile
		d.onReconcile = nil
		onReconcile()
	}
	if name == d.failName {
		return errors.New("bunny failed")
	}
	if d.records == nil {
		d.records = map[string][]bunny.Record{}
	}
	d.records[name] = append([]bunny.Record(nil), desired...)
	return nil
}
