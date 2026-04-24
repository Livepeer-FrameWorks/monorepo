package cmd

import (
	"testing"

	"frameworks/cli/pkg/inventory"
	pb "frameworks/pkg/proto"
)

func strPtr(s string) *string { return &s }
func i32Ptr(i int32) *int32   { return &i }

// testManifest and testCluster wire up the auto-generated cluster ID
// (Manifest.Type + "-" + Manifest.Profile) used by AllClusterIDs when no
// explicit `clusters:` block is present.
const testCluster = "cluster-test"

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

	f := auditMeshIdentity(manifest, []string{"core-1"}, qm, testClusterSet())
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

	f := auditMeshIdentity(manifest, []string{"core-1"}, qm, testClusterSet())
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

	f := auditMeshIdentity(manifest, []string{"core-1"}, qm, testClusterSet())
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
	f := auditMeshIdentity(manifest, []string{"core-1"}, nil, testClusterSet())
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
	f := auditMeshIdentity(manifest, []string{"core-1"}, qm, testClusterSet())
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
	f := auditMeshIdentity(manifest, []string{"core-1"}, qm, clusterSet)
	if f.hasErrors() {
		t.Fatalf("same-named-different-cluster should not register as drift: %+v", f.rows)
	}
}
