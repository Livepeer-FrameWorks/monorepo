package bootstrap

import (
	"strings"
	"testing"
)

func TestCheckRejectsIngressSiteWithNodeInDifferentCluster(t *testing.T) {
	qm := validCheckFixture()
	qm.Ingress.Sites[0].ClusterID = "media-central-primary"

	err := Check(qm)
	if err == nil {
		t.Fatal("expected ingress node/cluster mismatch error")
	}
	if !strings.Contains(err.Error(), `ingress_site "vmauth-regional-eu-1-media-central-primary"`) ||
		!strings.Contains(err.Error(), `belongs to cluster_id "core-central-primary", not "media-central-primary"`) {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestCheckRejectsServiceRegistryWithNodeInDifferentCluster(t *testing.T) {
	qm := validCheckFixture()
	qm.ServiceRegistry[0].ClusterID = "media-central-primary"

	err := Check(qm)
	if err == nil {
		t.Fatal("expected service registry node/cluster mismatch error")
	}
	if !strings.Contains(err.Error(), `service "vmauth"`) ||
		!strings.Contains(err.Error(), `belongs to cluster_id "core-central-primary", not "media-central-primary"`) {
		t.Fatalf("unexpected error: %v", err)
	}
}

func validCheckFixture() QuartermasterSection {
	return QuartermasterSection{
		SystemTenant: &Tenant{
			Alias: "frameworks",
			Name:  "FrameWorks",
		},
		Clusters: []Cluster{
			{
				ID:   "core-central-primary",
				Name: "Core Central Primary",
				Type: "central",
				OwnerTenant: TenantRef{
					Ref: "quartermaster.system_tenant",
				},
				Mesh: ClusterMesh{CIDR: "10.88.0.0/16"},
			},
			{
				ID:   "media-central-primary",
				Name: "Media Central Primary",
				Type: "edge",
				OwnerTenant: TenantRef{
					Ref: "quartermaster.system_tenant",
				},
				Mesh: ClusterMesh{CIDR: "10.89.0.0/16"},
			},
		},
		Nodes: []Node{
			{
				ID:         "regional-eu-1",
				ClusterID:  "core-central-primary",
				Type:       "core",
				ExternalIP: "203.0.113.10",
				WireGuard: NodeWireGuard{
					IP:        "10.88.0.10",
					PublicKey: "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=",
				},
			},
		},
		Ingress: IngressSection{
			TLSBundles: []TLSBundle{
				{
					ID:        "wildcard-media-central-primary-frameworks-network",
					ClusterID: "media-central-primary",
					Domains: []string{
						"media-central-primary.frameworks.network",
						"*.media-central-primary.frameworks.network",
					},
					Email: "ops@example.com",
				},
			},
			Sites: []IngressSite{
				{
					ID:          "vmauth-regional-eu-1-media-central-primary",
					ClusterID:   "core-central-primary",
					NodeID:      "regional-eu-1",
					Domains:     []string{"telemetry.media-central-primary.frameworks.network"},
					TLSBundleID: "wildcard-media-central-primary-frameworks-network",
					Kind:        "http",
					Upstream: IngressUpstream{
						Host: "10.88.0.10",
						Port: 8427,
					},
				},
			},
		},
		ServiceRegistry: []ServiceRegistryEntry{
			{
				ServiceName: "vmauth",
				Type:        "telemetry",
				Protocol:    "http",
				ClusterID:   "core-central-primary",
				NodeID:      "regional-eu-1",
				Port:        8427,
			},
		},
	}
}
