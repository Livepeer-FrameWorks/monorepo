//go:build schema_verify

package federation

import (
	"context"
	"database/sql"
	"fmt"
	"os/exec"
	"testing"
	"time"

	"frameworks/api_balancing/internal/control"

	dbsql "github.com/Livepeer-FrameWorks/monorepo/pkg/database/sql"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/testutil/dockerpg"
	_ "github.com/lib/pq"
)

func startFederationRealPG(t *testing.T) *sql.DB {
	t.Helper()
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker not available")
	}
	name := fmt.Sprintf("fw-federation-realpg-%d", time.Now().UnixNano())
	run := dockerpg.CLI
	t.Cleanup(func() { _, _ = run("rm", "-fv", name) })
	image, err := dockerpg.PostgresImage()
	if err != nil {
		t.Fatalf("resolve PostgreSQL test image: %v", err)
	}
	if out, err := dockerpg.Run("run", "-d", "--name", name, "-P", "-e", "POSTGRES_PASSWORD=harness", image); err != nil {
		t.Fatalf("docker run: %v\n%s", err, out)
	}
	port, err := dockerpg.DiscoverPublishedHostPort(name, "5432/tcp")
	if err != nil {
		t.Fatal(err)
	}
	conn, err := sql.Open("postgres", fmt.Sprintf("postgres://postgres:harness@127.0.0.1:%s/postgres?sslmode=disable", port))
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	if err = dockerpg.WaitReady(conn, name); err != nil {
		t.Fatal(err)
	}
	schema, err := dbsql.Content.ReadFile("schema/foghorn.sql")
	if err != nil {
		t.Fatalf("read foghorn schema: %v", err)
	}
	if _, err = conn.Exec(string(schema)); err != nil {
		t.Fatalf("apply foghorn schema: %v", err)
	}
	return conn
}

func TestMembershipTombstoneCleanup_PostgresProofToRedisPurge_RealPG(t *testing.T) {
	conn := startFederationRealPG(t)
	control.SetDB(conn)
	t.Cleanup(func() { control.SetDB(nil) })

	const (
		tenantA = "11111111-1111-1111-1111-111111111111"
		tenantB = "22222222-2222-2222-2222-222222222222"
	)
	pending := []struct {
		tenant     string
		stream     string
		generation string
		revision   int64
	}{
		{tenantA, "live+lower", "00000000-0000-0000-0000-000000000019", 19},
		{tenantA, "live+equal", "00000000-0000-0000-0000-000000000020", 20},
		{tenantA, "live+higher", "00000000-0000-0000-0000-000000000021", 21},
		{tenantB, "live+higher", "00000000-0000-0000-0000-000000000119", 19},
	}
	for _, effect := range pending {
		if _, err := conn.Exec(`
			INSERT INTO foghorn.ingest_admission_effects
			       (tenant_id, stream_internal_name, node_id, source_generation, source_revision)
			VALUES ($1::uuid, $2, 'node-a', $3::uuid, $4)
		`, effect.tenant, effect.stream, effect.generation, effect.revision); err != nil {
			t.Fatalf("seed pending effect for %s/%s: %v", effect.tenant, effect.stream, err)
		}
	}

	cache, _ := setupTestCache(t)
	ctx := context.Background()
	endedAt := time.Now().Add(-2 * streamPeerTombstoneRetention).UnixMilli()
	for index, stream := range []string{"live+lower", "live+equal", "live+higher"} {
		record := StreamPeerMembership{
			StreamName: stream, TenantID: tenantA,
			SourceGeneration: fmt.Sprintf("generation-%d", index), SourceRevision: 20,
			EndedAtUnixMilli: endedAt,
		}
		if current, err := cache.EndStreamPeerMembership(ctx, record); err != nil || !current {
			t.Fatalf("seed ended membership %s: current=%v err=%v", stream, current, err)
		}
	}

	pm := newTestPeerManager(t, "local-cluster", cache, true)
	pm.canPurgeMemberships = control.PurgeableAdmissionEffectFences
	if err := pm.cleanupStreamMembershipTombstones(ctx); err != nil {
		t.Fatalf("cleanupStreamMembershipTombstones: %v", err)
	}

	memberships, err := cache.LoadAllStreamPeerMemberships(ctx)
	if err != nil {
		t.Fatalf("LoadAllStreamPeerMemberships: %v", err)
	}
	for _, stream := range []string{"live+lower", "live+equal"} {
		if _, ok := memberships[stream]; !ok {
			t.Fatalf("pending revision at or below the fence did not retain %s: %+v", stream, memberships)
		}
	}
	if _, ok := memberships["live+higher"]; ok {
		t.Fatalf("only a higher same-tenant revision should permit purge: %+v", memberships)
	}
	for _, key := range []string{
		cache.keyStreamPeerMemberships(), cache.keyStreamPeerMembershipRevisions(),
		cache.keyStreamPeerMembershipGenerations(), cache.keyStreamPeerMembershipStates(),
	} {
		if exists, err := cache.client.HExists(ctx, key, "live+higher").Result(); err != nil || exists {
			t.Fatalf("approved cleanup left field in %s: exists=%v err=%v", key, exists, err)
		}
	}
}
