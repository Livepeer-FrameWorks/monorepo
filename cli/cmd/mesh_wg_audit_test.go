package cmd

import (
	"testing"
	"time"

	"frameworks/cli/pkg/inventory"
	pb "frameworks/pkg/proto"

	"google.golang.org/protobuf/types/known/timestamppb"
)

func strPtr(s string) *string { return &s }
func i32Ptr(i int32) *int32   { return &i }

// testManifest and testCluster wire up the auto-generated cluster ID
// (Manifest.Type + "-" + Manifest.Profile) used by AllClusterIDs when no
// explicit `clusters:` block is present.
const testCluster = "cluster-test"

// testNow is a fixed reference time so liveness tests are deterministic.
var testNow = time.Date(2026, 4, 25, 12, 0, 0, 0, time.UTC)

func testManifest(hosts map[string]inventory.Host) *inventory.Manifest {
	return &inventory.Manifest{
		Type:    "cluster",
		Profile: "test",
		Hosts:   hosts,
	}
}

func testClusterSet() map[string]bool {
	return map[string]bool{testCluster: true}
}

func TestAuditMeshIdentity_CleanMatch(t *testing.T) {
	manifest := testManifest(map[string]inventory.Host{
		"core-1": {
			Name:                "core-1",
			WireguardIP:         "10.88.0.2",
			WireguardPublicKey:  "pubkey-1",
			WireguardPort:       51820,
			WireguardPrivateKey: "priv-1",
		},
	})
	qm := []*pb.InfrastructureNode{{
		NodeName:           "core-1",
		ClusterId:          testCluster,
		WireguardIp:        strPtr("10.88.0.2"),
		WireguardPublicKey: strPtr("pubkey-1"),
		WireguardPort:      i32Ptr(51820),
		EnrollmentOrigin:   "gitops_seed",
	}}

	f := auditMeshIdentity(manifest, []string{"core-1"}, qm, testClusterSet(), testNow, defaultLivenessWindow)
	if f.hasErrors() {
		t.Fatalf("unexpected errors: %+v", f.rows)
	}
	if len(f.rows) != 1 || f.rows[0].severity != auditOK {
		t.Fatalf("expected single ok row, got %+v", f.rows)
	}
}

func TestAuditMeshIdentity_SeedMismatchIsError(t *testing.T) {
	manifest := testManifest(map[string]inventory.Host{
		"core-1": {
			Name:                "core-1",
			WireguardIP:         "10.88.0.2",
			WireguardPublicKey:  "pubkey-gitops",
			WireguardPort:       51820,
			WireguardPrivateKey: "priv-1",
		},
	})
	qm := []*pb.InfrastructureNode{{
		NodeName:           "core-1",
		ClusterId:          testCluster,
		WireguardIp:        strPtr("10.88.0.2"),
		WireguardPublicKey: strPtr("pubkey-DIVERGED"),
		WireguardPort:      i32Ptr(51820),
		EnrollmentOrigin:   "gitops_seed",
	}}

	f := auditMeshIdentity(manifest, []string{"core-1"}, qm, testClusterSet(), testNow, defaultLivenessWindow)
	if !f.hasErrors() || f.errorCount() != 1 {
		t.Fatalf("expected 1 error, got %d: %+v", f.errorCount(), f.rows)
	}
}

func TestAuditMeshIdentity_RuntimeEnrolledIsInfoNotError(t *testing.T) {
	manifest := testManifest(map[string]inventory.Host{
		"core-1": {
			Name:                "core-1",
			WireguardIP:         "10.88.0.2",
			WireguardPublicKey:  "pubkey-1",
			WireguardPort:       51820,
			WireguardPrivateKey: "priv-1",
		},
	})
	qm := []*pb.InfrastructureNode{
		{
			NodeName:           "core-1",
			ClusterId:          testCluster,
			WireguardIp:        strPtr("10.88.0.2"),
			WireguardPublicKey: strPtr("pubkey-1"),
			WireguardPort:      i32Ptr(51820),
			EnrollmentOrigin:   "gitops_seed",
		},
		{
			NodeName:           "replica-2",
			ClusterId:          testCluster,
			WireguardIp:        strPtr("10.88.0.20"),
			WireguardPublicKey: strPtr("pubkey-2"),
			WireguardPort:      i32Ptr(51820),
			EnrollmentOrigin:   "runtime_enrolled",
		},
	}

	f := auditMeshIdentity(manifest, []string{"core-1"}, qm, testClusterSet(), testNow, defaultLivenessWindow)
	if f.hasErrors() {
		t.Fatalf("runtime-enrolled extra node should not produce errors: %+v", f.rows)
	}
	foundInfo := false
	for _, r := range f.rows {
		if r.host == "replica-2" && r.severity == auditInfo {
			foundInfo = true
		}
	}
	if !foundInfo {
		t.Fatalf("expected info row for runtime-enrolled replica-2, got %+v", f.rows)
	}
}

func TestAuditMeshIdentity_UnseededGitOpsHostIsWarn(t *testing.T) {
	manifest := testManifest(map[string]inventory.Host{
		"core-1": {
			Name:                "core-1",
			WireguardIP:         "10.88.0.2",
			WireguardPublicKey:  "pubkey-1",
			WireguardPort:       51820,
			WireguardPrivateKey: "priv-1",
		},
	})
	f := auditMeshIdentity(manifest, []string{"core-1"}, nil, testClusterSet(), testNow, defaultLivenessWindow)
	if f.hasErrors() {
		t.Fatalf("unseeded host should be warn, not error: %+v", f.rows)
	}
	if len(f.rows) != 1 || f.rows[0].severity != auditWarn {
		t.Fatalf("expected single warn row, got %+v", f.rows)
	}
}

func TestAuditMeshIdentity_AdoptedLocalDivergenceIsError(t *testing.T) {
	manifest := testManifest(map[string]inventory.Host{
		"core-1": {
			Name:                "core-1",
			WireguardIP:         "10.88.0.2",
			WireguardPublicKey:  "pubkey-gitops",
			WireguardPort:       51820,
			WireguardPrivateKey: "priv-1",
		},
	})
	qm := []*pb.InfrastructureNode{{
		NodeName:           "core-1",
		ClusterId:          testCluster,
		WireguardIp:        strPtr("10.88.0.2"),
		WireguardPublicKey: strPtr("pubkey-DIVERGED"),
		WireguardPort:      i32Ptr(51820),
		EnrollmentOrigin:   "adopted_local",
	}}
	f := auditMeshIdentity(manifest, []string{"core-1"}, qm, testClusterSet(), testNow, defaultLivenessWindow)
	if !f.hasErrors() {
		t.Fatalf("adopted_local diverging from GitOps must error: %+v", f.rows)
	}
}

// Verifies the multi-cluster path: hosts in cluster A should only be joined
// to QM rows in cluster A, even when a same-named row exists in cluster B.
func TestAuditMeshIdentity_MultiClusterIsolation(t *testing.T) {
	manifest := &inventory.Manifest{
		Clusters: map[string]inventory.ClusterConfig{
			"prod-platform": {},
			"prod-edge":     {},
		},
		Hosts: map[string]inventory.Host{
			"core-1": {
				Name:                "core-1",
				Cluster:             "prod-platform",
				WireguardIP:         "10.88.0.2",
				WireguardPublicKey:  "pubkey-platform",
				WireguardPort:       51820,
				WireguardPrivateKey: "priv-1",
			},
		},
	}
	qm := []*pb.InfrastructureNode{
		{
			NodeName:           "core-1",
			ClusterId:          "prod-platform",
			WireguardIp:        strPtr("10.88.0.2"),
			WireguardPublicKey: strPtr("pubkey-platform"),
			WireguardPort:      i32Ptr(51820),
			EnrollmentOrigin:   "gitops_seed",
		},
		// Same node_name, different cluster — must not be used to match core-1.
		{
			NodeName:           "core-1",
			ClusterId:          "prod-edge",
			WireguardIp:        strPtr("10.99.0.2"),
			WireguardPublicKey: strPtr("pubkey-edge"),
			WireguardPort:      i32Ptr(51820),
			EnrollmentOrigin:   "gitops_seed",
		},
	}
	clusterSet := map[string]bool{"prod-platform": true, "prod-edge": true}
	f := auditMeshIdentity(manifest, []string{"core-1"}, qm, clusterSet, testNow, defaultLivenessWindow)
	if f.hasErrors() {
		t.Fatalf("same-named-different-cluster should not register as drift: %+v", f.rows)
	}
}

// auditQMCoreOne returns a baseline QM row for core-1 matching the
// testManifest fixture, so liveness tests focus on heartbeat handling.
func auditQMCoreOne(hb *timestamppb.Timestamp) []*pb.InfrastructureNode {
	return []*pb.InfrastructureNode{{
		NodeName:           "core-1",
		ClusterId:          testCluster,
		WireguardIp:        strPtr("10.88.0.2"),
		WireguardPublicKey: strPtr("pubkey-1"),
		WireguardPort:      i32Ptr(51820),
		EnrollmentOrigin:   "gitops_seed",
		LastHeartbeat:      hb,
	}}
}

func auditCoreOneManifest() *inventory.Manifest {
	return testManifest(map[string]inventory.Host{
		"core-1": {
			Name:                "core-1",
			WireguardIP:         "10.88.0.2",
			WireguardPublicKey:  "pubkey-1",
			WireguardPort:       51820,
			WireguardPrivateKey: "priv-1",
		},
	})
}

func TestAuditLiveness_FreshHeartbeat(t *testing.T) {
	hb := timestamppb.New(testNow.Add(-30 * time.Second)) // within default 90s window
	f := auditMeshIdentity(auditCoreOneManifest(), []string{"core-1"}, auditQMCoreOne(hb), testClusterSet(), testNow, defaultLivenessWindow)
	if len(f.rows) != 1 {
		t.Fatalf("expected 1 row, got %d", len(f.rows))
	}
	if got := f.rows[0].live; got != livenessFresh {
		t.Fatalf("live = %v, want livenessFresh", got)
	}
	if f.rows[0].severity != auditOK {
		t.Fatalf("severity should remain ok when identity matches; got %v", f.rows[0].severity)
	}
}

func TestAuditLiveness_StaleHeartbeat(t *testing.T) {
	hb := timestamppb.New(testNow.Add(-10 * time.Minute)) // far past 90s window
	f := auditMeshIdentity(auditCoreOneManifest(), []string{"core-1"}, auditQMCoreOne(hb), testClusterSet(), testNow, defaultLivenessWindow)
	if len(f.rows) != 1 {
		t.Fatalf("expected 1 row, got %d", len(f.rows))
	}
	if got := f.rows[0].live; got != livenessStale {
		t.Fatalf("live = %v, want livenessStale", got)
	}
	// Stale heartbeat alone must not turn an identity-clean row into an error
	// — the LIVE column is informational, separate from severity.
	if f.rows[0].severity != auditOK {
		t.Fatalf("severity should not escalate on stale heartbeat; got %v", f.rows[0].severity)
	}
}

func TestAuditLiveness_MissingHeartbeat(t *testing.T) {
	f := auditMeshIdentity(auditCoreOneManifest(), []string{"core-1"}, auditQMCoreOne(nil), testClusterSet(), testNow, defaultLivenessWindow)
	if got := f.rows[0].live; got != livenessUnknown {
		t.Fatalf("nil last_heartbeat should be livenessUnknown, got %v", got)
	}
}

func TestAuditLiveness_BoundaryAtWindow(t *testing.T) {
	// Heartbeat exactly at the window boundary is considered fresh: the
	// classification uses now.Sub(hb) > window, so a heartbeat at the edge
	// is still inside.
	hb := timestamppb.New(testNow.Add(-defaultLivenessWindow))
	f := auditMeshIdentity(auditCoreOneManifest(), []string{"core-1"}, auditQMCoreOne(hb), testClusterSet(), testNow, defaultLivenessWindow)
	if got := f.rows[0].live; got != livenessFresh {
		t.Fatalf("heartbeat at window boundary should be fresh, got %v", got)
	}
}

func TestFilterAuditFindings_NoFilter(t *testing.T) {
	f := auditFindings{rows: []auditRow{
		{host: "a", clusterID: "c1"},
		{host: "b", clusterID: "c2"},
	}}
	out := filterAuditFindings(f, "", "")
	if len(out.rows) != 2 {
		t.Fatalf("no filter should pass through unchanged, got %d rows", len(out.rows))
	}
}

func TestFilterAuditFindings_ByCluster(t *testing.T) {
	f := auditFindings{rows: []auditRow{
		{host: "a", clusterID: "c1"},
		{host: "b", clusterID: "c2"},
		{host: "c", clusterID: "c1"},
	}}
	out := filterAuditFindings(f, "c1", "")
	if len(out.rows) != 2 || out.rows[0].host != "a" || out.rows[1].host != "c" {
		t.Fatalf("cluster filter mismatch: %+v", out.rows)
	}
}

func TestFilterAuditFindings_ByHost(t *testing.T) {
	f := auditFindings{rows: []auditRow{
		{host: "a", clusterID: "c1"},
		{host: "b", clusterID: "c2"},
	}}
	out := filterAuditFindings(f, "", "b")
	if len(out.rows) != 1 || out.rows[0].host != "b" {
		t.Fatalf("host filter mismatch: %+v", out.rows)
	}
}

func TestFilterAuditFindings_BothFilters(t *testing.T) {
	f := auditFindings{rows: []auditRow{
		{host: "a", clusterID: "c1"},
		{host: "a", clusterID: "c2"},
	}}
	out := filterAuditFindings(f, "c2", "a")
	if len(out.rows) != 1 || out.rows[0].clusterID != "c2" {
		t.Fatalf("combined filter mismatch: %+v", out.rows)
	}
}

func TestAuditRevision_SurfacesQMValue(t *testing.T) {
	qm := auditQMCoreOne(timestamppb.New(testNow))
	qm[0].AppliedMeshRevision = strPtr("rev-abc123")
	f := auditMeshIdentity(auditCoreOneManifest(), []string{"core-1"}, qm, testClusterSet(), testNow, defaultLivenessWindow)
	if got := f.rows[0].revision; got != "rev-abc123" {
		t.Errorf("revision = %q, want rev-abc123", got)
	}
}

func TestAuditRevision_EmptyWhenAgentNeverReported(t *testing.T) {
	qm := auditQMCoreOne(timestamppb.New(testNow))
	// AppliedMeshRevision left nil — older client or fresh row.
	f := auditMeshIdentity(auditCoreOneManifest(), []string{"core-1"}, qm, testClusterSet(), testNow, defaultLivenessWindow)
	if got := f.rows[0].revision; got != "" {
		t.Errorf("revision should be empty when QM has no value, got %q", got)
	}
}

func TestAuditLiveness_NoQMRowReportsUnknown(t *testing.T) {
	f := auditMeshIdentity(auditCoreOneManifest(), []string{"core-1"}, nil, testClusterSet(), testNow, defaultLivenessWindow)
	if got := f.rows[0].live; got != livenessUnknown {
		t.Fatalf("missing QM row should be livenessUnknown, got %v", got)
	}
}

// Runtime-enrolled nodes appear in QM but not in the manifest, so they
// reach the audit via the unmatched-QM loop. The LIVE column has to be
// classified there too, not only on manifest-matched rows.
func TestAuditLiveness_UnmatchedQMRowSurfacesHeartbeat(t *testing.T) {
	hb := timestamppb.New(testNow.Add(-30 * time.Second))
	qm := append(auditQMCoreOne(timestamppb.New(testNow)), &pb.InfrastructureNode{
		NodeName:           "edge-runtime",
		ClusterId:          testCluster,
		WireguardIp:        strPtr("10.88.0.20"),
		WireguardPublicKey: strPtr("pubkey-runtime"),
		WireguardPort:      i32Ptr(51820),
		EnrollmentOrigin:   "runtime_enrolled",
		LastHeartbeat:      hb,
	})
	f := auditMeshIdentity(auditCoreOneManifest(), []string{"core-1"}, qm, testClusterSet(), testNow, defaultLivenessWindow)

	var found bool
	for _, r := range f.rows {
		if r.host == "edge-runtime" {
			found = true
			if r.live != livenessFresh {
				t.Fatalf("runtime-enrolled extra node should report fresh heartbeat, got %v", r.live)
			}
		}
	}
	if !found {
		t.Fatalf("expected runtime-enrolled row in audit output, got %+v", f.rows)
	}
}
