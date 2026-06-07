package bootstrap

import (
	"context"
	"database/sql"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
)

// Each validator test starts from a fully-valid struct and zeroes/breaks one
// field per case, asserting the validator rejects it. This walks every required
// clause, not just the happy path, so a dropped check is caught.

func TestValidateCluster(t *testing.T) {
	valid := Cluster{
		ID:          "c1",
		Name:        "Cluster One",
		Type:        "edge",
		OwnerTenant: TenantRef{Ref: "tenant-a"},
		Mesh:        ClusterMesh{CIDR: "10.0.0.0/24"},
	}
	if err := validateCluster(valid); err != nil {
		t.Fatalf("valid cluster rejected: %v", err)
	}

	breakers := map[string]func(*Cluster){
		"missing_id":    func(c *Cluster) { c.ID = "" },
		"missing_name":  func(c *Cluster) { c.Name = "" },
		"bad_type":      func(c *Cluster) { c.Type = "satellite" },
		"empty_type":    func(c *Cluster) { c.Type = "" },
		"missing_owner": func(c *Cluster) { c.OwnerTenant.Ref = "" },
		"missing_mesh":  func(c *Cluster) { c.Mesh.CIDR = "" },
	}
	for name, brk := range breakers {
		t.Run(name, func(t *testing.T) {
			c := valid
			brk(&c)
			if err := validateCluster(c); err == nil {
				t.Fatalf("expected error for %s", name)
			}
		})
	}

	// "central" is the other accepted type.
	central := valid
	central.Type = "central"
	if err := validateCluster(central); err != nil {
		t.Fatalf("central cluster rejected: %v", err)
	}
}

func TestValidateNode(t *testing.T) {
	valid := Node{
		ID:         "n1",
		ClusterID:  "c1",
		Type:       "core",
		ExternalIP: "203.0.113.1",
		WireGuard:  NodeWireGuard{IP: "10.0.0.2", PublicKey: "pubkey"},
	}
	if err := validateNode(valid); err != nil {
		t.Fatalf("valid node rejected: %v", err)
	}

	breakers := map[string]func(*Node){
		"missing_id":        func(n *Node) { n.ID = "" },
		"missing_cluster":   func(n *Node) { n.ClusterID = "" },
		"bad_type":          func(n *Node) { n.Type = "gateway" },
		"missing_ext_ip":    func(n *Node) { n.ExternalIP = "" },
		"missing_wg_ip":     func(n *Node) { n.WireGuard.IP = "" },
		"missing_wg_pubkey": func(n *Node) { n.WireGuard.PublicKey = "" },
	}
	for name, brk := range breakers {
		t.Run(name, func(t *testing.T) {
			n := valid
			brk(&n)
			if err := validateNode(n); err == nil {
				t.Fatalf("expected error for %s", name)
			}
		})
	}
}

func TestValidateTLSBundle(t *testing.T) {
	valid := TLSBundle{
		ID:        "b1",
		ClusterID: "c1",
		Domains:   []string{"example.com"},
		Email:     "ops@example.com",
	}
	if err := validateTLSBundle(valid); err != nil {
		t.Fatalf("valid bundle rejected: %v", err)
	}

	breakers := map[string]func(*TLSBundle){
		"missing_id":      func(b *TLSBundle) { b.ID = "" },
		"missing_cluster": func(b *TLSBundle) { b.ClusterID = "" },
		"no_domains":      func(b *TLSBundle) { b.Domains = nil },
		"missing_email":   func(b *TLSBundle) { b.Email = "" },
	}
	for name, brk := range breakers {
		t.Run(name, func(t *testing.T) {
			b := valid
			brk(&b)
			if err := validateTLSBundle(b); err == nil {
				t.Fatalf("expected error for %s", name)
			}
		})
	}
}

func TestValidateIngressSite(t *testing.T) {
	valid := IngressSite{
		ID:          "s1",
		ClusterID:   "c1",
		NodeID:      "n1",
		Domains:     []string{"example.com"},
		TLSBundleID: "b1",
		Kind:        "http",
		Upstream:    IngressUpstream{Host: "127.0.0.1", Port: 8080},
	}
	if err := validateIngressSite(valid); err != nil {
		t.Fatalf("valid site rejected: %v", err)
	}

	breakers := map[string]func(*IngressSite){
		"missing_id":      func(s *IngressSite) { s.ID = "" },
		"missing_cluster": func(s *IngressSite) { s.ClusterID = "" },
		"missing_node":    func(s *IngressSite) { s.NodeID = "" },
		"no_domains":      func(s *IngressSite) { s.Domains = nil },
		"missing_bundle":  func(s *IngressSite) { s.TLSBundleID = "" },
		"missing_kind":    func(s *IngressSite) { s.Kind = "" },
		"missing_up_host": func(s *IngressSite) { s.Upstream.Host = "" },
		"missing_up_port": func(s *IngressSite) { s.Upstream.Port = 0 },
	}
	for name, brk := range breakers {
		t.Run(name, func(t *testing.T) {
			s := valid
			brk(&s)
			if err := validateIngressSite(s); err == nil {
				t.Fatalf("expected error for %s", name)
			}
		})
	}
}

func TestValidateServiceEntry(t *testing.T) {
	valid := ServiceRegistryEntry{
		ServiceName: "vmauth",
		Type:        "auth",
		ClusterID:   "c1",
		NodeID:      "n1",
		Port:        8427,
	}
	if err := validateServiceEntry(valid); err != nil {
		t.Fatalf("valid entry rejected: %v", err)
	}

	breakers := map[string]func(*ServiceRegistryEntry){
		"missing_name":    func(e *ServiceRegistryEntry) { e.ServiceName = "" },
		"missing_type":    func(e *ServiceRegistryEntry) { e.Type = "" },
		"missing_cluster": func(e *ServiceRegistryEntry) { e.ClusterID = "" },
		"missing_node":    func(e *ServiceRegistryEntry) { e.NodeID = "" },
		"missing_port":    func(e *ServiceRegistryEntry) { e.Port = 0 },
	}
	for name, brk := range breakers {
		t.Run(name, func(t *testing.T) {
			e := valid
			brk(&e)
			if err := validateServiceEntry(e); err == nil {
				t.Fatalf("expected error for %s", name)
			}
		})
	}
}

// TestSelectMatchingClusters pins the predicate built from the access flags.
// NOTE: the both-false case is guarded upstream in
// ReconcileSystemTenantClusterAccess (early return), so selectMatchingClusters
// is only ever reached with at least one flag set.
func TestSelectMatchingClusters(t *testing.T) {
	tests := []struct {
		name        string
		cfg         SystemTenantClusterAccess
		wantClause  string
		returnedIDs []string
	}{
		{
			name:        "default_only",
			cfg:         SystemTenantClusterAccess{DefaultClusters: true},
			wantClause:  "is_default_cluster = true",
			returnedIDs: []string{"c1", "c2"},
		},
		{
			name:        "platform_only",
			cfg:         SystemTenantClusterAccess{PlatformOfficialClusters: true},
			wantClause:  "is_platform_official = true",
			returnedIDs: []string{"c3"},
		},
		{
			name:        "both_flags_ored",
			cfg:         SystemTenantClusterAccess{DefaultClusters: true, PlatformOfficialClusters: true},
			wantClause:  "is_default_cluster = true OR is_platform_official = true",
			returnedIDs: []string{"c1", "c3"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db, mock, err := sqlmock.New()
			if err != nil {
				t.Fatalf("sqlmock: %v", err)
			}
			defer db.Close() //nolint:errcheck

			rows := sqlmock.NewRows([]string{"cluster_id"})
			for _, id := range tt.returnedIDs {
				rows.AddRow(id)
			}
			mock.ExpectQuery(tt.wantClause).WillReturnRows(rows)

			got, err := selectMatchingClusters(context.Background(), db, &tt.cfg)
			if err != nil {
				t.Fatalf("selectMatchingClusters: %v", err)
			}
			if len(got) != len(tt.returnedIDs) {
				t.Fatalf("got %v, want %v", got, tt.returnedIDs)
			}
			if mockErr := mock.ExpectationsWereMet(); mockErr != nil {
				t.Fatalf("clause %q not matched: %v", tt.wantClause, mockErr)
			}
		})
	}
}

func TestResolveNodeAdvertiseHost(t *testing.T) {
	t.Run("returns_wireguard_ip", func(t *testing.T) {
		db, mock, err := sqlmock.New()
		if err != nil {
			t.Fatalf("sqlmock: %v", err)
		}
		defer db.Close() //nolint:errcheck
		mock.ExpectQuery("FROM quartermaster.infrastructure_nodes").
			WithArgs("n1").
			WillReturnRows(sqlmock.NewRows([]string{"cluster_id", "wireguard_ip"}).AddRow("c1", "10.0.0.2"))

		got, err := resolveNodeAdvertiseHost(context.Background(), db, "c1", "n1")
		if err != nil {
			t.Fatalf("resolveNodeAdvertiseHost: %v", err)
		}
		if got != "10.0.0.2" {
			t.Fatalf("host = %q, want 10.0.0.2", got)
		}
	})

	t.Run("not_found", func(t *testing.T) {
		db, mock, err := sqlmock.New()
		if err != nil {
			t.Fatalf("sqlmock: %v", err)
		}
		defer db.Close() //nolint:errcheck
		mock.ExpectQuery("FROM quartermaster.infrastructure_nodes").
			WithArgs("n1").
			WillReturnError(sql.ErrNoRows)
		_, err = resolveNodeAdvertiseHost(context.Background(), db, "c1", "n1")
		if err == nil {
			t.Fatal("expected error when node missing")
		}
	})

	t.Run("cluster_mismatch", func(t *testing.T) {
		db, mock, err := sqlmock.New()
		if err != nil {
			t.Fatalf("sqlmock: %v", err)
		}
		defer db.Close() //nolint:errcheck
		mock.ExpectQuery("FROM quartermaster.infrastructure_nodes").
			WithArgs("n1").
			WillReturnRows(sqlmock.NewRows([]string{"cluster_id", "wireguard_ip"}).AddRow("other", "10.0.0.2"))

		_, err = resolveNodeAdvertiseHost(context.Background(), db, "c1", "n1")
		if err == nil {
			t.Fatal("expected error for cluster mismatch")
		}
	})

	t.Run("empty_wireguard_ip", func(t *testing.T) {
		db, mock, err := sqlmock.New()
		if err != nil {
			t.Fatalf("sqlmock: %v", err)
		}
		defer db.Close() //nolint:errcheck
		mock.ExpectQuery("FROM quartermaster.infrastructure_nodes").
			WithArgs("n1").
			WillReturnRows(sqlmock.NewRows([]string{"cluster_id", "wireguard_ip"}).AddRow("c1", ""))

		_, err = resolveNodeAdvertiseHost(context.Background(), db, "c1", "n1")
		if err == nil {
			t.Fatal("expected error when wireguard_ip empty")
		}
	})
}
