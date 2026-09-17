//go:build schema_verify

package bootstrap

import (
	"context"
	"database/sql"
	"fmt"
	"os/exec"
	"testing"
	"time"

	dbsql "github.com/Livepeer-FrameWorks/monorepo/pkg/database/sql"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/testutil/dockerpg"
	_ "github.com/lib/pq"
)

func TestBootstrapRepositoryReplay_RealPG(t *testing.T) {
	db := startQuartermasterBootstrapRealPG(t)
	ctx := context.Background()
	desired := QuartermasterSection{
		SystemTenant: &Tenant{Alias: SystemTenantAlias, Name: "FrameWorks"},
		Clusters: []Cluster{{
			ID: "core-test", Name: "Core test", Type: "central", Region: "eu-west",
			BaseURL:     "https://core-test.example.com",
			OwnerTenant: TenantRef{Ref: "quartermaster.system_tenant"},
			IsDefault:   true, IsPlatformOfficial: true, PublicTopology: true,
			Mesh: ClusterMesh{CIDR: "10.88.0.0/24", ListenPort: 51820},
			Cell: "core-test", Class: "platform_official", ControlCell: "core-test",
			EligibleServingCells: []string{"core-test"},
		}},
		Nodes: []Node{{
			ID: "core-test-1", ClusterID: "core-test", Type: "core", ExternalIP: "203.0.113.10",
			WireGuard: NodeWireGuard{IP: "10.88.0.10", PublicKey: "contract-public-key", Port: 51820},
		}},
		Ingress: IngressSection{
			TLSBundles: []TLSBundle{{
				ID: "physical-contract", ClusterID: "core-test", Domains: []string{"contract.example.com"},
				Email: "ops@example.com",
			}},
			Sites: []IngressSite{{
				ID: "physical-contract", ClusterID: "core-test", NodeID: "core-test-1",
				Domains: []string{"contract.example.com"}, TLSBundleID: "physical-contract", Kind: "physical",
				Upstream: IngressUpstream{Host: "10.88.0.10", Port: 8080},
			}},
		},
		ServiceRegistry: []ServiceRegistryEntry{{
			ServiceName: "contract-service", Type: "control", Protocol: "http",
			ClusterID: "core-test", NodeID: "core-test-1", Port: 8080,
			HealthEndpoint: "/health", Metadata: map[string]string{"contract": "true"},
		}},
		SystemTenantClusterAccess: &SystemTenantClusterAccess{DefaultClusters: true, PlatformOfficialClusters: true},
	}

	first := reconcileQuartermasterInTransaction(t, ctx, db, desired)
	if len(first.Tenants.Created) != 1 || len(first.Clusters.Created) != 1 || len(first.Nodes.Created) != 1 ||
		len(first.Ingress.Created) != 2 || len(first.ServiceRegistry.Created) != 1 || len(first.SystemTenantAccess.Created) != 1 {
		t.Fatalf("unexpected first reconcile: %+v", first)
	}
	// Replaying identical desired state is a noop for every section, including
	// the default cluster, so it emits no cluster events.
	second := reconcileQuartermasterInTransaction(t, ctx, db, desired)
	if len(second.Tenants.Noop) != 1 || len(second.Clusters.Noop) != 1 || len(second.Nodes.Noop) != 1 ||
		len(second.Ingress.Noop) != 2 || len(second.ServiceRegistry.Noop) != 1 || len(second.SystemTenantAccess.Noop) != 1 {
		t.Fatalf("unexpected replay reconcile: %+v", second)
	}
	changed := desired
	changed.Clusters = append([]Cluster(nil), desired.Clusters...)
	changed.Clusters[0].IsPlatformOfficial = false
	changed.Clusters[0].Class = "tenant_private"
	if third := reconcileQuartermasterInTransaction(t, ctx, db, changed); len(third.Clusters.Updated) != 1 {
		t.Fatalf("unexpected changed-cluster reconcile: %+v", third.Clusters)
	}
	// Bootstrap writes cluster events through the outbox like the gRPC handlers:
	// one per created or updated cluster, none for a noop.
	var created, updated int
	if err := db.QueryRowContext(ctx, `
		SELECT
			count(*) FILTER (WHERE event_type = 'cluster_created'),
			count(*) FILTER (WHERE event_type = 'cluster_updated')
		FROM quartermaster.service_event_outbox
		WHERE resource_type = 'cluster' AND resource_id = 'core-test' AND scope = 'tenant' AND tenant_id IS NOT NULL
	`).Scan(&created, &updated); err != nil {
		t.Fatal(err)
	}
	if created != 1 || updated != 1 {
		t.Fatalf("cluster outbox events: created=%d updated=%d, want 1 and 1", created, updated)
	}

	var tenantCount, clusterCount, nodeCount, serviceCount int
	if err := db.QueryRowContext(ctx, `
		SELECT
			(SELECT count(*) FROM quartermaster.tenants),
			(SELECT count(*) FROM quartermaster.infrastructure_clusters),
			(SELECT count(*) FROM quartermaster.infrastructure_nodes),
			(SELECT count(*) FROM quartermaster.service_instances)
	`).Scan(&tenantCount, &clusterCount, &nodeCount, &serviceCount); err != nil {
		t.Fatal(err)
	}
	if tenantCount != 1 || clusterCount != 1 || nodeCount != 1 || serviceCount != 1 {
		t.Fatalf("unexpected replay counts: tenants=%d clusters=%d nodes=%d services=%d", tenantCount, clusterCount, nodeCount, serviceCount)
	}
}

func reconcileQuartermasterInTransaction(t *testing.T, ctx context.Context, db *sql.DB, desired QuartermasterSection) *Sections {
	t.Helper()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	result, err := Reconcile(ctx, tx, desired)
	if err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	return result
}

func startQuartermasterBootstrapRealPG(t *testing.T) *sql.DB {
	t.Helper()
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker not available")
	}
	name := fmt.Sprintf("fw-quartermaster-bootstrap-realpg-%d", time.Now().UnixNano())
	t.Cleanup(func() { _, _ = dockerpg.CLI("rm", "-fv", name) })
	image, err := dockerpg.PostgresImage()
	if err != nil {
		t.Fatal(err)
	}
	if output, err := dockerpg.Run("run", "-d", "--name", name, "-P", "-e", "POSTGRES_PASSWORD=harness", image); err != nil {
		t.Fatalf("docker run: %v\n%s", err, output)
	}
	port, err := dockerpg.DiscoverPublishedHostPort(name, "5432/tcp")
	if err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("postgres", fmt.Sprintf("postgres://postgres:harness@127.0.0.1:%s/postgres?sslmode=disable", port))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := dockerpg.WaitReady(db, name); err != nil {
		t.Fatal(err)
	}
	schema, err := dbsql.Content.ReadFile("schema/quartermaster.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(string(schema)); err != nil {
		t.Fatal(err)
	}
	return db
}
