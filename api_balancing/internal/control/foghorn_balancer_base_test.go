package control

import (
	"errors"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	dnspb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/dns"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
	quartermasterpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/quartermaster"
	"google.golang.org/protobuf/proto"
)

func TestFoghornBalancerBaseUsesClusterScopedDNS(t *testing.T) {
	t.Setenv("BRAND_DOMAIN", "frameworks.network")

	got := foghornBalancerBase("core-central-primary")
	want := "https://foghorn.core-central-primary.frameworks.network"
	if got != want {
		t.Fatalf("foghornBalancerBase() = %q, want %q", got, want)
	}
}

func TestFoghornBalancerBaseNormalizesBrandDomain(t *testing.T) {
	t.Setenv("BRAND_DOMAIN", "https://frameworks.network/")

	got := foghornBalancerBase("media-eu-1")
	want := "https://foghorn.media-eu-1.frameworks.network"
	if got != want {
		t.Fatalf("foghornBalancerBase() = %q, want %q", got, want)
	}
}

func TestFoghornBalancerBaseUsesExplicitPublicBase(t *testing.T) {
	t.Setenv("FOGHORN_PUBLIC_BASE", "https://foghorn.example")

	got := foghornBalancerBase("core-central-primary")
	want := "https://foghorn.example"
	if got != want {
		t.Fatalf("foghornBalancerBase() = %q, want %q", got, want)
	}
}

func TestFoghornBalancerBaseUsesLocalComposeURL(t *testing.T) {
	t.Setenv("BUILD_ENV", "development")
	t.Setenv("FOGHORN_URL", "http://foghorn:18008")

	got := foghornBalancerBase("central-primary")
	want := "http://foghorn:18008"
	if got != want {
		t.Fatalf("foghornBalancerBase() = %q, want %q", got, want)
	}
}

func TestComposeConfigSeedScopesRealtimeToProcessing(t *testing.T) {
	seed := composeConfigSeed("node-1", nil, "", 0, "")

	realtimeByName := map[string]bool{}
	processControlledByName := map[string]bool{}
	for _, template := range seed.GetTemplates() {
		def := template.GetDef()
		realtimeByName[def.GetName()] = def.GetRealtime()
		processControlledByName[def.GetName()] = def.GetProcessControlledRealtime()
	}

	want := map[string]bool{
		"live":       false,
		"vod":        false,
		"dvr":        false,
		"processing": true,
		"pull":       false,
	}
	for name, realtime := range want {
		got, ok := realtimeByName[name]
		if !ok {
			t.Fatalf("template %q missing from ConfigSeed", name)
		}
		if got != realtime {
			t.Fatalf("template %q realtime = %v, want %v", name, got, realtime)
		}
	}
	if !processControlledByName["processing"] {
		t.Fatal("processing template must enable process-controlled realtime")
	}
	for name, processControlled := range processControlledByName {
		if name != "processing" && processControlled {
			t.Fatalf("template %q process_controlled_realtime = true, want false", name)
		}
	}
}

func TestMergeConfigSeedFallbackPreservesLastGoodCentralState(t *testing.T) {
	lastGood := &ipcpb.ConfigSeed{
		NodeId: "node-1", SeedVersion: 41, TenantId: "tenant-a",
		Site:                &ipcpb.SiteConfig{EdgeDomain: "edge.example"},
		TlsBundles:          []*ipcpb.TLSCertBundle{{BundleId: "bundle-a", CertPem: "cert", KeyPem: "key"}},
		FoghornBalancerBase: "https://old.example/source?cap=old",
	}
	current := &ipcpb.ConfigSeed{
		NodeId: "node-1", SeedVersion: 42,
		Templates:           []*ipcpb.StreamTemplate{{Id: "live"}},
		OperationalMode:     ipcpb.NodeOperationalMode_NODE_OPERATIONAL_MODE_DRAINING,
		FoghornBalancerBase: "https://local.example/source?cap=new",
	}
	got := mergeConfigSeedFallback(current, lastGood, configSeedFallback{
		preserveTenant: true,
		preserveSite:   true,
		preserveTLS:    true,
	})
	if got.GetSeedVersion() != 42 || got.GetOperationalMode() != ipcpb.NodeOperationalMode_NODE_OPERATIONAL_MODE_DRAINING ||
		got.GetTenantId() != "tenant-a" || got.GetSite().GetEdgeDomain() != "edge.example" || len(got.GetTlsBundles()) != 1 ||
		got.GetFoghornBalancerBase() != current.GetFoghornBalancerBase() || !proto.Equal(got.GetTemplates()[0], current.GetTemplates()[0]) {
		t.Fatalf("merged fallback = %+v", got)
	}
	if lastGood.GetSeedVersion() != 41 {
		t.Fatal("merge mutated persisted seed")
	}
}

func TestComposeConfigSeedPreservesDurableCentralStateWhenControlPlaneIsAbsent(t *testing.T) {
	mockDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	t.Cleanup(func() { _ = mockDB.Close() })

	oldDB := db
	oldQM := quartermasterClient
	oldNavigator := navigatorClient
	oldOwner := getNodeOwnerFn
	t.Cleanup(func() {
		db = oldDB
		quartermasterClient = oldQM
		navigatorClient = oldNavigator
		getNodeOwnerFn = oldOwner
	})
	db = mockDB
	quartermasterClient = nil
	navigatorClient = nil
	getNodeOwnerFn = nil

	lastGood := &ipcpb.ConfigSeed{
		NodeId:      "node-restart",
		SeedVersion: 41,
		TenantId:    "tenant-a",
		Site:        &ipcpb.SiteConfig{EdgeDomain: "edge.media-a.example"},
		Tls:         &ipcpb.TLSCertBundle{BundleId: "cluster:media-a", CertPem: "cluster-cert", KeyPem: "cluster-key"},
		TlsBundles: []*ipcpb.TLSCertBundle{
			{BundleId: "cluster:media-a", CertPem: "cluster-cert", KeyPem: "cluster-key"},
			{BundleId: "tenant:tenant-a", CertPem: "tenant-cert", KeyPem: "tenant-key"},
		},
	}
	payload, err := proto.MarshalOptions{Deterministic: true}.Marshal(lastGood)
	if err != nil {
		t.Fatalf("marshal last good seed: %v", err)
	}
	mock.ExpectQuery(`SELECT COALESCE\(seed_version, 0\)::bigint AS seed_version, seed_payload`).
		WithArgs("node-restart").
		WillReturnRows(sqlmock.NewRows([]string{"seed_version", "seed_payload"}).AddRow(int64(41), payload))
	got := composeConfigSeed("node-restart", nil, "", ipcpb.NodeOperationalMode_NODE_OPERATIONAL_MODE_NORMAL, "")
	if got.GetSeedVersion() != 0 || got.GetTenantId() != "tenant-a" ||
		got.GetSite().GetEdgeDomain() != "edge.media-a.example" || !proto.Equal(got.GetTls(), lastGood.GetTls()) ||
		!proto.Equal(&ipcpb.ConfigSeed{TlsBundles: got.GetTlsBundles()}, &ipcpb.ConfigSeed{TlsBundles: lastGood.GetTlsBundles()}) {
		t.Fatalf("restart seed discarded durable central state: %+v", got)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}

func TestMergeConfigSeedFallbackAppliesAuthoritativeRemoval(t *testing.T) {
	lastGood := &ipcpb.ConfigSeed{
		NodeId:      "node-1",
		SeedVersion: 41,
		TenantId:    "tenant-a",
		TlsBundles: []*ipcpb.TLSCertBundle{{
			BundleId: "tenant:tenant-a", ExpiresAt: time.Now().Add(90 * 24 * time.Hour).Unix(),
		}},
	}
	current := &ipcpb.ConfigSeed{NodeId: "node-1", SeedVersion: 42}

	got := mergeConfigSeedFallback(current, lastGood, configSeedFallback{
		removedTLSBundleIDs: map[string]struct{}{"tenant:tenant-a": {}},
	})
	if got.GetTenantId() != "" || got.GetTls() != nil || len(got.GetTlsBundles()) != 0 {
		t.Fatalf("authoritative removal was replaced with stale state: %+v", got)
	}
}

func TestNavigatorTLSBundleFoundDistinguishesAbsenceFromFailure(t *testing.T) {
	tests := []struct {
		name    string
		resp    *dnspb.GetTLSBundleResponse
		found   bool
		wantErr bool
	}{
		{name: "present", resp: &dnspb.GetTLSBundleResponse{Found: true}, found: true},
		{name: "explicit absence", resp: &dnspb.GetTLSBundleResponse{Found: false}},
		{name: "store failure", resp: &dnspb.GetTLSBundleResponse{Found: false, Error: "database unavailable"}, wantErr: true},
		{name: "malformed empty response", resp: nil, wantErr: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			found, err := navigatorTLSBundleFound(test.resp)
			if found != test.found || (err != nil) != test.wantErr {
				t.Fatalf("found=%v err=%v, want found=%v wantErr=%v", found, err, test.found, test.wantErr)
			}
		})
	}
}

func TestCollectTenantBundlesPendingIssuanceDoesNotFreezeReadyTenant(t *testing.T) {
	tenants := []*quartermasterpb.AliasedTenantRef{
		{TenantId: "pending"},
		{TenantId: "ready"},
	}
	bundles, removals, err := collectTenantBundles(tenants, "tenant", "example.test", func(bundleID string) (*dnspb.GetTLSBundleResponse, error) {
		if bundleID == "tenant:pending" {
			return &dnspb.GetTLSBundleResponse{Found: false}, nil
		}
		return &dnspb.GetTLSBundleResponse{
			Found: true, BundleId: bundleID, Domains: []string{"ready.tenant.example.test", "*.ready.tenant.example.test"},
			CertPem: "cert", KeyPem: "key", Version: "v1",
		}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(bundles) != 1 || bundles[0].GetBundleId() != "tenant:ready" {
		t.Fatalf("ready tenant was frozen by pending issuance: %#v", bundles)
	}
	if _, removed := removals["tenant:pending"]; !removed {
		t.Fatalf("authoritative absence was not carried to merge: %#v", removals)
	}
}

// TestCollectTenantBundlesLookupFailureAbortsWithoutPartialOutput pins the
// preserve half of the response contract: any lookup failure (transport error,
// reported store error, malformed response) aborts the whole collection with
// no partial bundle list and no removal set, so the caller falls back to
// preserveTLS and last-good material survives.
func TestCollectTenantBundlesLookupFailureAbortsWithoutPartialOutput(t *testing.T) {
	tenants := []*quartermasterpb.AliasedTenantRef{
		{TenantId: "ready"},
		{TenantId: "broken"},
	}
	readyResp := &dnspb.GetTLSBundleResponse{
		Found: true, BundleId: "tenant:ready", Domains: []string{"ready.tenant.example.test"},
		CertPem: "cert", KeyPem: "key", Version: "v1",
	}
	cases := []struct {
		name  string
		fetch func(string) (*dnspb.GetTLSBundleResponse, error)
	}{
		{name: "transport error", fetch: func(bundleID string) (*dnspb.GetTLSBundleResponse, error) {
			if bundleID == "tenant:broken" {
				return nil, errors.New("navigator unreachable")
			}
			return readyResp, nil
		}},
		{name: "reported store error", fetch: func(bundleID string) (*dnspb.GetTLSBundleResponse, error) {
			if bundleID == "tenant:broken" {
				return &dnspb.GetTLSBundleResponse{Found: false, Error: "database unavailable"}, nil
			}
			return readyResp, nil
		}},
		{name: "malformed response", fetch: func(bundleID string) (*dnspb.GetTLSBundleResponse, error) {
			if bundleID == "tenant:broken" {
				return nil, nil
			}
			return readyResp, nil
		}},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			bundles, removals, err := collectTenantBundles(tenants, "tenant", "example.test", test.fetch)
			if err == nil {
				t.Fatal("lookup failure did not abort the collection")
			}
			if bundles != nil || removals != nil {
				t.Fatalf("partial output escaped an aborted collection: bundles=%v removals=%v", bundles, removals)
			}
		})
	}
}

// TestCollectServedClusterTLSBundlesAllOrNothing pins the served-cluster
// refresh contract: one cluster's lookup failure fails the whole set (no
// partial shrink reaches the listener), while an error-free absence skips only
// that cluster.
func TestCollectServedClusterTLSBundlesAllOrNothing(t *testing.T) {
	healthy := &ipcpb.TLSCertBundle{BundleId: "cluster:alpha", CertPem: "cert", KeyPem: "key"}
	bundles, err := collectServedClusterTLSBundles([]string{"alpha", "broken"}, func(clusterID string) (*ipcpb.TLSCertBundle, bool, error) {
		if clusterID == "broken" {
			return nil, false, errors.New("navigator: database unavailable")
		}
		return healthy, true, nil
	})
	if err == nil || bundles != nil {
		t.Fatalf("cluster lookup failure yielded a partial set: bundles=%v err=%v", bundles, err)
	}

	bundles, err = collectServedClusterTLSBundles([]string{"alpha", "absent"}, func(clusterID string) (*ipcpb.TLSCertBundle, bool, error) {
		if clusterID == "absent" {
			return nil, false, nil
		}
		return healthy, true, nil
	})
	if err != nil || len(bundles) != 1 || bundles[0].GetBundleId() != "cluster:alpha" {
		t.Fatalf("error-free absence handling: bundles=%v err=%v, want only cluster:alpha", bundles, err)
	}
}

func TestMergeConfigSeedFallbackRetainsOmittedTenantTLSOnlyWhileValid(t *testing.T) {
	now := time.Now()
	lastGood := &ipcpb.ConfigSeed{
		NodeId: "node-1", SeedVersion: 41,
		TlsBundles: []*ipcpb.TLSCertBundle{
			{BundleId: "cluster:old", CertPem: "old-cluster"},
			{BundleId: "tenant:retained", CertPem: "retained", ExpiresAt: now.Add(time.Hour).Unix()},
			{BundleId: "tenant:expired", CertPem: "expired", ExpiresAt: now.Add(-time.Hour).Unix()},
			{BundleId: "tenant:rotated", CertPem: "old", ExpiresAt: now.Add(time.Hour).Unix()},
		},
	}
	current := &ipcpb.ConfigSeed{
		NodeId: "node-1", SeedVersion: 42,
		TlsBundles: []*ipcpb.TLSCertBundle{
			{BundleId: "cluster:new", CertPem: "new-cluster"},
			{BundleId: "tenant:rotated", CertPem: "new", ExpiresAt: now.Add(2 * time.Hour).Unix()},
		},
	}

	got := mergeConfigSeedFallback(current, lastGood, configSeedFallback{})
	byID := map[string]*ipcpb.TLSCertBundle{}
	for _, bundle := range got.GetTlsBundles() {
		byID[bundle.GetBundleId()] = bundle
	}
	if byID["tenant:retained"].GetCertPem() != "retained" {
		t.Fatalf("valid omitted tenant authority was not retained: %#v", got.GetTlsBundles())
	}
	if byID["tenant:rotated"].GetCertPem() != "new" {
		t.Fatalf("current tenant revision did not win: %#v", got.GetTlsBundles())
	}
	if _, ok := byID["tenant:expired"]; ok {
		t.Fatalf("expired tenant authority was retained: %#v", got.GetTlsBundles())
	}
	if _, ok := byID["cluster:old"]; ok {
		t.Fatalf("obsolete non-tenant bundle was retained from a complete view: %#v", got.GetTlsBundles())
	}
}
