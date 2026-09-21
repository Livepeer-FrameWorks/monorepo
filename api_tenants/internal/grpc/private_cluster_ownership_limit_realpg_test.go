//go:build schema_verify

package grpc

import (
	"context"
	"database/sql"
	"fmt"
	"sync"
	"testing"
	"time"

	"frameworks/api_tenants/internal/database/quartermasterdb"
	dbsql "github.com/Livepeer-FrameWorks/monorepo/pkg/database/sql"
	quartermasterpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/quartermaster"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/testutil/dockerpg"
	"github.com/sirupsen/logrus"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestPrivateClusterOwnershipLimitSerializes_RealPG(t *testing.T) {
	name := fmt.Sprintf("fw-private-cluster-ownership-%d", time.Now().UnixNano())
	t.Cleanup(func() { _, _ = dockerpg.CLI("rm", "-fv", name) })
	image, err := dockerpg.PostgresImage()
	if err != nil {
		t.Fatal(err)
	}
	if output, err := dockerpg.Run("run", "-d", "--name", name, "-P", "-e", "POSTGRES_PASSWORD=harness", image); err != nil {
		t.Fatalf("start PostgreSQL: %v\n%s", err, output)
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
	verifyPrivateClusterOwnershipLimitSerializes(t, db)
}

func TestPrivateClusterOwnershipLimitSerializes_RealYugabyte(t *testing.T) {
	db, ok := dockerpg.OpenSharedYugabyteDatabase(t, "quartermaster_owned_cluster_limit")
	if !ok {
		t.Skip("requires shared Yugabyte contract fixture")
	}
	verifyPrivateClusterOwnershipLimitSerializes(t, db)
}

func verifyPrivateClusterOwnershipLimitSerializes(t *testing.T, db *sql.DB) {
	t.Helper()
	ctx := context.Background()
	schema, err := dbsql.Content.ReadFile("schema/quartermaster.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, string(schema)); err != nil {
		t.Fatal(err)
	}
	const (
		racingTenant  = "11111111-1111-4111-8111-111111111181"
		holderTenant  = "11111111-1111-4111-8111-111111111182"
		providerOwner = "11111111-1111-4111-8111-111111111183"
	)
	if _, err := db.ExecContext(ctx, `
		INSERT INTO quartermaster.tenants (id, name, max_owned_clusters, is_provider)
		VALUES ($1::uuid, 'Racing tenant', 1, false),
		       ($2::uuid, 'Lock holder tenant', 1, false),
		       ($3::uuid, 'Provider tenant', 1, true)`, racingTenant, holderTenant, providerOwner); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `
		INSERT INTO quartermaster.infrastructure_clusters (cluster_id, cluster_name, cluster_type, base_url, cluster_class, cell_id, control_cell_id, region_id)
		VALUES ('owned-limit-cell', 'Owned limit cell', 'edge', 'owned-limit.example', 'platform_official', 'owned-limit-cell', 'owned-limit-cell', 'eu');
		INSERT INTO quartermaster.services (service_id, name, plane, type, protocol)
		VALUES ('owned-limit-foghorn', 'Owned limit Foghorn', 'control', 'foghorn', 'grpc');
		INSERT INTO quartermaster.service_instances (instance_id, cluster_id, service_id, protocol, advertise_host, port, status, health_status)
		VALUES ('owned-limit-foghorn-1', 'owned-limit-cell', 'owned-limit-foghorn', 'grpc', 'foghorn-1.internal', 18019, 'running', 'healthy');
		INSERT INTO quartermaster.service_cluster_assignments (service_instance_id, cluster_id, source)
		SELECT id, 'owned-limit-cell', 'gitops_seed' FROM quartermaster.service_instances WHERE instance_id = 'owned-limit-foghorn-1';
	`); err != nil {
		t.Fatal(err)
	}
	s := NewQuartermasterServer(db, logrus.New(), nil, nil, nil, nil, nil)
	s.SetEventTokenHasher(testEventTokenHasher(t))
	ownedClusters := func(tenantID string) int {
		t.Helper()
		var count int
		if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM quartermaster.infrastructure_clusters WHERE owner_tenant_id = $1::uuid`, tenantID).Scan(&count); err != nil {
			t.Fatal(err)
		}
		return count
	}
	create := func(tenantID string, index int) error {
		callCtx := consentActor(tenantID, "owned-limit-actor", "owner")
		clusterName := fmt.Sprintf("Owned Edge %d", index)
		if index%2 == 0 {
			_, err := s.EnableSelfHosting(callCtx, &quartermasterpb.EnableSelfHostingRequest{TenantId: tenantID, ClusterName: clusterName})
			return err
		}
		_, err := s.CreatePrivateCluster(callCtx, &quartermasterpb.CreatePrivateClusterRequest{TenantId: tenantID, ClusterName: clusterName})
		return err
	}

	const racers = 6
	start := make(chan struct{})
	results := make(chan error, racers)
	var wg sync.WaitGroup
	for index := range racers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			results <- create(racingTenant, index)
		}()
	}
	close(start)
	wg.Wait()
	close(results)
	succeeded := 0
	for err := range results {
		switch status.Code(err) {
		case codes.OK:
			succeeded++
		case codes.ResourceExhausted:
		default:
			t.Fatalf("concurrent owned-cluster create: %v", err)
		}
	}
	if succeeded != 1 {
		t.Fatalf("concurrent owned-cluster creates succeeded %d times, want 1", succeeded)
	}
	if got := ownedClusters(racingTenant); got != 1 {
		t.Fatalf("racing tenant owns %d clusters, want 1", got)
	}

	// The holder takes the ownership lock before the RPC's transaction begins
	// and commits its insert while the RPC waits, so the RPC's count must
	// observe a row committed after its first statement started.
	holder, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer holder.Rollback() //nolint:errcheck
	if _, err := quartermasterdb.New(holder).LockTenantClusterOwnershipLimit(ctx, holderTenant); err != nil {
		t.Fatal(err)
	}
	if _, err := holder.ExecContext(ctx, `
		INSERT INTO quartermaster.infrastructure_clusters (cluster_id, cluster_name, cluster_type, base_url, cluster_class, owner_tenant_id)
		VALUES ('owned-limit-holder', 'Holder cluster', 'edge', 'owned-limit.example', 'tenant_private', $1::uuid)`, holderTenant); err != nil {
		t.Fatal(err)
	}
	waiter := make(chan error, 1)
	go func() { waiter <- create(holderTenant, 1) }()
	select {
	case err := <-waiter:
		t.Fatalf("create returned while the tenant row was locked: %v", err)
	case <-time.After(time.Second):
	}
	if err := holder.Commit(); err != nil {
		t.Fatalf("commit lock holder: %v", err)
	}
	if err := <-waiter; status.Code(err) != codes.ResourceExhausted {
		t.Fatalf("create after lock holder committed: %v, want ResourceExhausted", err)
	}
	if got := ownedClusters(holderTenant); got != 1 {
		t.Fatalf("lock holder tenant owns %d clusters, want 1", got)
	}

	for index := range 2 {
		if err := create(providerOwner, index); err != nil {
			t.Fatalf("provider create %d: %v", index, err)
		}
	}
	if got := ownedClusters(providerOwner); got != 2 {
		t.Fatalf("provider owns %d clusters, want 2", got)
	}
}
