//go:build schema_verify

package bootstrap

import (
	"context"
	"database/sql"
	"fmt"
	"os/exec"
	"strings"
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
	// Each of them is also a platform-scoped domain event under the same ID,
	// with no actor because bootstrap runs outside an API call.
	var domainCreated, domainUpdated int
	if err := db.QueryRowContext(ctx, `
		SELECT
			count(*) FILTER (WHERE d.event_type = 'cluster.created'),
			count(*) FILTER (WHERE d.event_type = 'cluster.updated')
		FROM quartermaster.domain_event_outbox d
		JOIN quartermaster.service_event_outbox s ON s.event_id = d.event_id
		WHERE d.aggregate_type = 'clusters' AND d.aggregate_id = 'core-test'
		  AND d.scope = 'platform' AND d.tenant_id IS NULL AND d.actor_auth_type = ''
	`).Scan(&domainCreated, &domainUpdated); err != nil {
		t.Fatal(err)
	}
	if domainCreated != 1 || domainUpdated != 1 {
		t.Fatalf("cluster domain events: created=%d updated=%d, want 1 and 1", domainCreated, domainUpdated)
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
	if strings.Contains(t.Name(), "_RealYugabyte") {
		db, ok := dockerpg.OpenSharedYugabyteBaseline(t, "manifest_registry", "quartermaster")
		if !ok {
			t.Skip("requires the shared Yugabyte fixture")
		}
		return db
	}
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

func TestManifestRegistryRetiresOnlyOwnedScope_RealPG(t *testing.T) {
	db := startQuartermasterBootstrapRealPG(t)
	desired := QuartermasterSection{
		SystemTenant:    &Tenant{Alias: SystemTenantAlias, Name: "FrameWorks"},
		Clusters:        []Cluster{{ID: "registry-test", Name: "Registry", Type: "central", BaseURL: "https://registry.example.com", OwnerTenant: TenantRef{Ref: "quartermaster.system_tenant"}, Mesh: ClusterMesh{CIDR: "10.89.0.0/24", ListenPort: 51820}, Cell: "registry-test", Class: "platform_official", ControlCell: "registry-test", EligibleServingCells: []string{"registry-test"}}},
		Nodes:           []Node{{ID: "registry-node", ClusterID: "registry-test", Type: "core", ExternalIP: "203.0.113.10", WireGuard: NodeWireGuard{IP: "10.89.0.10", PublicKey: "registry-key", Port: 51820}}},
		RegistryScope:   &ServiceRegistryScope{Owner: "production-registry", Clusters: []string{"registry-test"}, Complete: true},
		ServiceRegistry: []ServiceRegistryEntry{{ServiceName: "registry-service", Type: "control", Protocol: "http", ClusterID: "registry-test", NodeID: "registry-node", Port: 8080}},
	}
	originalEntries := append([]ServiceRegistryEntry(nil), desired.ServiceRegistry...)
	reconcileQuartermasterInTransaction(t, t.Context(), db, desired)
	if _, err := db.ExecContext(t.Context(), `INSERT INTO quartermaster.service_instances(instance_id,service_id,cluster_id,node_id,protocol,port,status,health_status) VALUES('runtime-instance','registry-service','registry-test','registry-node','http',8081,'running','unknown')`); err != nil {
		t.Fatal(err)
	}
	desired.ServiceRegistry = nil
	reconcileQuartermasterInTransaction(t, t.Context(), db, desired)
	var managed, runtime string
	if err := db.QueryRowContext(t.Context(), `SELECT status FROM quartermaster.service_instances WHERE cluster_id='registry-test' AND instance_id='inst-registry-service-registry-node-http-8080'`).Scan(&managed); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(t.Context(), `SELECT status FROM quartermaster.service_instances WHERE cluster_id='registry-test' AND instance_id='runtime-instance'`).Scan(&runtime); err != nil {
		t.Fatal(err)
	}
	if managed != "stopped" || runtime != "running" {
		t.Fatalf("removed manifest registration=%s, independent runtime=%s", managed, runtime)
	}
	// A partial upload cannot retire existing inventory, and restoring a desired
	// tuple revives its historical row rather than creating another registration.
	desired.ServiceRegistry = originalEntries
	reconcileQuartermasterInTransaction(t, t.Context(), db, desired)
	desired.ServiceRegistry = nil
	desired.RegistryScope.Complete = false
	reconcileQuartermasterInTransaction(t, t.Context(), db, desired)
	if err := db.QueryRowContext(t.Context(), `SELECT status FROM quartermaster.service_instances WHERE cluster_id='registry-test' AND port=8080`).Scan(&managed); err != nil || managed != "running" {
		t.Fatalf("partial upload retired an owned row: %s %v", managed, err)
	}
	desired.RegistryScope.Complete = true

	// A known runtime registrar is never adopted by a matching manifest tuple.
	desired.ServiceRegistry = append([]ServiceRegistryEntry(nil), originalEntries...)
	desired.ServiceRegistry[0].Port = 8081
	tx, err := db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Reconcile(t.Context(), tx, desired); err == nil {
		t.Error("manifest took over runtime registration")
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	desired.ServiceRegistry = nil

	// Legacy IDs are opaque: adoption verifies all inventory evidence, not a
	// string format. A wrong tuple aborts the whole bootstrap transaction.
	if _, err := db.ExecContext(t.Context(), `INSERT INTO quartermaster.service_instances(instance_id,service_id,cluster_id,node_id,protocol,port,status,health_status,registration_source) VALUES('inst-a88f18a7','registry-service','registry-test','registry-node','http',8082,'running','unknown','legacy')`); err != nil {
		t.Fatal(err)
	}
	desired.RegistryScope.Adopt = []ServiceRegistryAdoption{{InstanceID: "inst-a88f18a7", ServiceName: "registry-service", ClusterID: "registry-test", NodeID: "registry-node", Protocol: "http", Port: 8083}}
	tx, err = db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Reconcile(t.Context(), tx, desired); err == nil {
		t.Error("adoption accepted mismatched historical inventory")
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	desired.RegistryScope.Adopt[0].Port = 8082
	result := reconcileQuartermasterInTransaction(t, t.Context(), db, desired)
	if len(result.ServiceRegistry.Stopped) != 2 {
		t.Fatalf("reviewed retired inventory: %+v", result.ServiceRegistry)
	}
	var owner, source string
	if err := db.QueryRowContext(t.Context(), `SELECT status,registry_owner,registration_source FROM quartermaster.service_instances WHERE instance_id='inst-a88f18a7' AND cluster_id='registry-test'`).Scan(&managed, &owner, &source); err != nil {
		t.Fatal(err)
	}
	if managed != "stopped" || owner != "production-registry" || source != "manifest" {
		t.Fatalf("legacy provenance/status=%s/%s/%s", managed, owner, source)
	}
	if replay := reconcileQuartermasterInTransaction(t, t.Context(), db, desired); len(replay.ServiceRegistry.Stopped) != 0 {
		t.Fatalf("retirement was not idempotent: %+v", replay.ServiceRegistry)
	}

}

func TestManifestRegistryRetiresOnlyOwnedScope_RealYugabyte(t *testing.T) {
	TestManifestRegistryRetiresOnlyOwnedScope_RealPG(t)
}
