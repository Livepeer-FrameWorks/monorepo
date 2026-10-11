//go:build schema_verify

package grpc

import (
	"context"
	"database/sql"
	"fmt"
	"slices"
	"testing"
	"time"

	"frameworks/api_tenants/internal/database/quartermasterdb"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	quartermasterpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/quartermaster"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/testutil/dockerpg"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func TestUnchangedNodeLivenessLeavesNodeRowsAlone_RealPG(t *testing.T) {
	name := fmt.Sprintf("fw-qm-node-liveness-%d", time.Now().UnixNano())
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
	applyQuartermasterBaseline(t, db)
	verifyUnchangedNodeLivenessLeavesNodeRowsAlone(t, db)
}

func TestUnchangedNodeLivenessLeavesNodeRowsAlone_RealYugabyte(t *testing.T) {
	db, ok := dockerpg.OpenSharedYugabyteBaseline(t, "quartermaster_node_liveness", "quartermaster")
	if !ok {
		t.Skip("requires shared Yugabyte contract fixture")
	}
	verifyUnchangedNodeLivenessLeavesNodeRowsAlone(t, db)
}

// Foghorn re-reports every edge each minute and Privateer syncs every 30 s. A
// report or sync that changes nothing must leave the node row alone until a
// liveness stamp nears the freshness window of the readers that judge it, while
// a changed value is written at once and still moves the mesh topology
// revision when the mesh trigger watches it.
func verifyUnchangedNodeLivenessLeavesNodeRowsAlone(t *testing.T, db *sql.DB) { //nolint:funlen,gocognit // One engine fixture walks edge reports, control-cell observations and mesh syncs.
	t.Helper()
	ctx := context.Background()
	exec := func(t *testing.T, query string, args ...any) {
		t.Helper()
		if _, err := db.ExecContext(ctx, query, args...); err != nil {
			t.Fatalf("%s: %v", query, err)
		}
	}
	exec(t, `INSERT INTO quartermaster.infrastructure_clusters (cluster_id, cluster_name, cluster_type, base_url, cluster_class, cell_id, control_cell_id, eligible_serving_cell_ids)
	      VALUES ('liveness-cell-a', 'Cell A', 'edge', 'https://a.example', 'platform_official', 'liveness-cell-a', 'liveness-cell-a', ARRAY[]::TEXT[]),
	             ('liveness-private', 'Private', 'edge', 'https://a.example', 'tenant_private', 'liveness-private', 'liveness-cell-a', ARRAY['liveness-cell-a'])`)
	exec(t, `INSERT INTO quartermaster.infrastructure_nodes (node_id, cluster_id, node_name, node_type, status, external_ip)
	      VALUES ('liveness-edge', 'liveness-cell-a', 'Platform edge', 'edge', 'active', '203.0.113.50'),
	             ('liveness-private-edge', 'liveness-private', 'Private edge', 'edge', 'active', '203.0.113.51')`)
	exec(t, `INSERT INTO quartermaster.infrastructure_nodes (node_id, cluster_id, node_name, node_type, status, external_ip, wireguard_ip, wireguard_public_key, wireguard_listen_port)
	      VALUES ('liveness-core', 'liveness-cell-a', 'Core', 'core', 'active', '203.0.113.52', '10.88.0.52', 'liveness-core-key', 51820)`)

	server := NewQuartermasterServer(db, logging.NewLogger(), nil, nil, nil, nil, nil)
	serviceCtx := context.WithValue(ctx, ctxkeys.KeyAuthType, "service")

	type nodeRow struct {
		heartbeat, updated, externalIP, cell, observed, revision, status, snapshotAt, cpu string
	}
	row := func(t *testing.T, nodeID string) nodeRow {
		t.Helper()
		var out nodeRow
		if err := db.QueryRowContext(ctx, `
			SELECT COALESCE(last_heartbeat::text, ''), updated_at::text, COALESCE(host(external_ip), ''),
			       COALESCE(control_cell_id, ''), COALESCE(control_cell_observed_at::text, ''),
			       COALESCE(applied_mesh_revision, ''), COALESCE(status, ''),
			       COALESCE(snapshot_at::text, ''), COALESCE(snapshot_cpu_percent::text, '')
			FROM quartermaster.infrastructure_nodes WHERE node_id = $1`, nodeID).Scan(
			&out.heartbeat, &out.updated, &out.externalIP, &out.cell, &out.observed,
			&out.revision, &out.status, &out.snapshotAt, &out.cpu); err != nil {
			t.Fatal(err)
		}
		return out
	}
	age := func(t *testing.T, nodeID, column string) float64 {
		t.Helper()
		var seconds float64
		// last_heartbeat is a timestamp without time zone written from NOW() in
		// the session zone, so it compares against NOW() in that same zone.
		if err := db.QueryRowContext(ctx, `SELECT EXTRACT(EPOCH FROM NOW() - `+column+`)::float8 FROM quartermaster.infrastructure_nodes WHERE node_id = $1`, nodeID).Scan(&seconds); err != nil {
			t.Fatal(err)
		}
		return seconds
	}
	topology := func(t *testing.T) int64 {
		t.Helper()
		revision, err := quartermasterdb.New(db).CurrentMeshTopologyRevision(ctx)
		if err != nil {
			t.Fatal(err)
		}
		return revision
	}
	report := func(t *testing.T, cellID, nodeID, ip string, healthy bool, observedAt time.Time) {
		t.Helper()
		if _, err := server.ReportAliveNodes(serviceCtx, &quartermasterpb.ReportAliveNodesRequest{
			ReporterCellId: cellID,
			Nodes: []*quartermasterpb.NodeAliveness{{
				NodeId: nodeID, IsHealthy: healthy, ExternalIp: ip, ObservedAt: timestamppb.New(observedAt),
				Capabilities: &quartermasterpb.EdgeCapabilities{Ingest: true},
			}},
		}); err != nil {
			t.Fatalf("ReportAliveNodes: %v", err)
		}
	}
	// Each statement's NOW() is its own transaction start, so a rewrite in the
	// same microsecond as the previous write is the only way a write could hide.
	pause := func() { time.Sleep(10 * time.Millisecond) }

	// The refresh bounds leave one lost report before each reader's window.
	for _, bound := range []struct {
		name    string
		refresh int32
		gap     time.Duration
		window  time.Duration
	}{
		{"edge heartbeat", edgeHeartbeatRefreshAfterSeconds, foghornEdgeSnapshotInterval, edgeHeartbeatReaderWindow},
		{"control-cell observation", controlCellObservationRefreshAfterSeconds, foghornEdgeSnapshotInterval, controlCellObservationWindow},
		{"mesh heartbeat", meshHeartbeatRefreshAfterSeconds, privateerSyncInterval, meshHeartbeatReaderWindow},
	} {
		if bound.refresh <= 0 || time.Duration(bound.refresh)*time.Second+2*bound.gap > bound.window {
			t.Fatalf("%s refresh bound %ds does not leave two %s gaps inside the %s window", bound.name, bound.refresh, bound.gap, bound.window)
		}
	}

	t.Run("edge reports", func(t *testing.T) {
		const ipA, ipB = "203.0.113.50", "203.0.113.60"
		report(t, "liveness-cell-a", "liveness-edge", ipA, true, time.Now())
		report(t, "liveness-cell-a", "liveness-private-edge", "203.0.113.51", true, time.Now())
		edge, private, quietTopology := row(t, "liveness-edge"), row(t, "liveness-private-edge"), topology(t)
		if edge.heartbeat == "" || edge.cell != "liveness-cell-a" || edge.observed == "" || private.cell != "liveness-cell-a" {
			t.Fatalf("first reports did not record liveness and control cell: edge %+v private %+v", edge, private)
		}
		pause()
		report(t, "liveness-cell-a", "liveness-edge", ipA, true, time.Now())
		report(t, "liveness-cell-a", "liveness-private-edge", "203.0.113.51", true, time.Now())
		report(t, "liveness-cell-a", "liveness-edge", ipA, false, time.Now())
		if got := row(t, "liveness-edge"); got != edge {
			t.Fatalf("unchanged edge reports rewrote the node row:\nbefore %+v\nafter  %+v", edge, got)
		}
		if got := row(t, "liveness-private-edge"); got != private {
			t.Fatalf("unchanged private edge reports rewrote the node row:\nbefore %+v\nafter  %+v", private, got)
		}

		// A heartbeat younger than its bound is left alone; one past it is refreshed.
		exec(t, `UPDATE quartermaster.infrastructure_nodes SET last_heartbeat = NOW() - make_interval(secs => $1) WHERE node_id = 'liveness-edge'`, int(edgeHeartbeatRefreshAfterSeconds)-10)
		young := row(t, "liveness-edge")
		report(t, "liveness-cell-a", "liveness-edge", ipA, true, time.Now())
		if got := row(t, "liveness-edge"); got != young {
			t.Fatalf("a heartbeat younger than the refresh bound was rewritten: %+v -> %+v", young, got)
		}
		exec(t, `UPDATE quartermaster.infrastructure_nodes SET last_heartbeat = NOW() - make_interval(secs => $1) WHERE node_id = 'liveness-edge'`, int(edgeHeartbeatRefreshAfterSeconds)+1)
		report(t, "liveness-cell-a", "liveness-edge", ipA, true, time.Now())
		if got := age(t, "liveness-edge", "last_heartbeat"); got > 5 {
			t.Fatalf("a heartbeat past the refresh bound was not refreshed: %.0fs old", got)
		}

		// Only a tenant-private cluster can be reassigned, so only its edges keep a
		// fresh observation; it is refreshed once the newest report is the bound
		// past the stored one, and the reassignment reader still sees the edge.
		exec(t, `UPDATE quartermaster.infrastructure_nodes SET control_cell_observed_at = NOW() - make_interval(secs => $1) WHERE node_id = 'liveness-private-edge'`, int(controlCellObservationRefreshAfterSeconds)-10)
		young = row(t, "liveness-private-edge")
		report(t, "liveness-cell-a", "liveness-private-edge", "203.0.113.51", true, time.Now())
		if got := row(t, "liveness-private-edge"); got.observed != young.observed {
			t.Fatalf("an observation younger than the refresh bound was rewritten: %s -> %s", young.observed, got.observed)
		}
		pending, err := quartermasterdb.New(db).ListControlCellPendingNodes(ctx, "liveness-private", "liveness-cell-b", controlCellObservationWindow)
		if err != nil {
			t.Fatal(err)
		}
		if !slices.Equal(pending, []string{"liveness-private-edge"}) {
			t.Fatalf("an edge whose refresh was skipped dropped out of the reassignment window: pending %v", pending)
		}
		exec(t, `UPDATE quartermaster.infrastructure_nodes SET control_cell_observed_at = NOW() - make_interval(secs => $1) WHERE node_id = 'liveness-private-edge'`, int(controlCellObservationRefreshAfterSeconds)+1)
		report(t, "liveness-cell-a", "liveness-private-edge", "203.0.113.51", true, time.Now())
		if got := age(t, "liveness-private-edge", "control_cell_observed_at"); got > 5 {
			t.Fatalf("an observation past the refresh bound was not refreshed: %.0fs old", got)
		}
		exec(t, `UPDATE quartermaster.infrastructure_nodes SET control_cell_observed_at = NOW() - INTERVAL '1 hour' WHERE node_id = 'liveness-edge'`)
		stale := row(t, "liveness-edge")
		report(t, "liveness-cell-a", "liveness-edge", ipA, true, time.Now())
		if got := row(t, "liveness-edge"); got.observed != stale.observed {
			t.Fatalf("a platform edge observation no reader times was rewritten: %s -> %s", stale.observed, got.observed)
		}
		if got := topology(t); got != quietTopology {
			t.Fatalf("unchanged edge reports moved the mesh topology revision %d -> %d", quietTopology, got)
		}

		// Changed values are written at once.
		report(t, "liveness-cell-b", "liveness-edge", ipA, true, time.Now())
		if got := row(t, "liveness-edge"); got.cell != "liveness-cell-b" || age(t, "liveness-edge", "control_cell_observed_at") > 5 {
			t.Fatalf("a new reporting cell was not recorded at once: %+v", got)
		}
		report(t, "liveness-cell-b", "liveness-edge", ipB, true, time.Now())
		if got := row(t, "liveness-edge"); got.externalIP != ipB {
			t.Fatalf("a changed external IP was not written at once: %+v", got)
		}
		if got := topology(t); got == quietTopology {
			t.Fatal("a changed external IP did not move the mesh topology revision")
		}
	})

	// Privateer syncs.
	sync := func(t *testing.T, revision string, cpu float32) {
		t.Helper()
		if _, err := server.SyncMesh(serviceCtx, &quartermasterpb.InfrastructureSyncRequest{
			NodeId: "liveness-core", PublicKey: "liveness-core-key", ListenPort: 51820, AppliedMeshRevision: revision,
			ResourceSnapshot: &quartermasterpb.NodeResourceSnapshot{
				CpuPercent: cpu, RamUsedBytes: 512, RamTotalBytes: 1024, DiskUsedBytes: 2048, DiskTotalBytes: 4096, UptimeSeconds: 99,
			},
		}); err != nil {
			t.Fatalf("SyncMesh: %v", err)
		}
	}
	t.Run("mesh syncs", func(t *testing.T) {
		sync(t, "mesh-1", 10)
		core := row(t, "liveness-core")
		if core.heartbeat == "" || core.revision != "mesh-1" || core.snapshotAt == "" || core.status != "active" {
			t.Fatalf("first sync did not record liveness, revision and snapshot: %+v", core)
		}
		pause()
		sync(t, "mesh-1", 20)
		sync(t, "mesh-1", 30)
		if got := row(t, "liveness-core"); got != core {
			t.Fatalf("unchanged mesh syncs rewrote the node row:\nbefore %+v\nafter  %+v", core, got)
		}
		sync(t, "mesh-2", 30)
		if got := row(t, "liveness-core"); got.revision != "mesh-2" {
			t.Fatalf("a new applied revision was not written at once: %+v", got)
		}
		exec(t, `UPDATE quartermaster.infrastructure_nodes SET status = 'offline' WHERE node_id = 'liveness-core'`)
		offlineTopology := topology(t)
		sync(t, "mesh-2", 30)
		if got := row(t, "liveness-core"); got.status != "active" {
			t.Fatalf("a sync did not reactivate an offline node at once: %+v", got)
		}
		if got := topology(t); got == offlineTopology {
			t.Fatal("reactivating a node did not move the mesh topology revision")
		}
		exec(t, `UPDATE quartermaster.infrastructure_nodes
	      SET last_heartbeat = NOW() - make_interval(secs => $1), snapshot_at = NOW() - make_interval(secs => $1)
	      WHERE node_id = 'liveness-core'`, int(meshHeartbeatRefreshAfterSeconds)-10)
		young := row(t, "liveness-core")
		sync(t, "mesh-2", 40)
		if got := row(t, "liveness-core"); got != young {
			t.Fatalf("a mesh heartbeat younger than the refresh bound was rewritten: %+v -> %+v", young, got)
		}
		exec(t, `UPDATE quartermaster.infrastructure_nodes SET snapshot_at = NOW() - make_interval(secs => $1) WHERE node_id = 'liveness-core'`, int(meshHeartbeatRefreshAfterSeconds)+1)
		sync(t, "mesh-2", 50)
		if got := row(t, "liveness-core"); got.cpu != "50" || age(t, "liveness-core", "snapshot_at") > 5 || age(t, "liveness-core", "last_heartbeat") > 5 {
			t.Fatalf("a snapshot past the refresh bound was not refreshed with the heartbeat: %+v", got)
		}
		exec(t, `UPDATE quartermaster.infrastructure_nodes SET last_heartbeat = NOW() - make_interval(secs => $1) WHERE node_id = 'liveness-core'`, int(meshHeartbeatRefreshAfterSeconds)+1)
		sync(t, "mesh-2", 60)
		if got := row(t, "liveness-core"); got.cpu != "60" || age(t, "liveness-core", "last_heartbeat") > 5 {
			t.Fatalf("a heartbeat past the refresh bound was not refreshed: %+v", got)
		}
		steady := topology(t)
		pause()
		sync(t, "mesh-2", 70)
		if got := topology(t); got != steady {
			t.Fatalf("an unchanged mesh sync moved the mesh topology revision %d -> %d", steady, got)
		}
		// An operator's lifecycle status outlives the node's own syncs: a node
		// in maintenance, retired or evicted keeps reporting until it is
		// stopped, and its heartbeat must not put it back into routing.
		for _, held := range []string{"maintenance", "retired", "evicted"} {
			exec(t, `UPDATE quartermaster.infrastructure_nodes SET status = $1, last_heartbeat = NOW() - make_interval(secs => $2) WHERE node_id = 'liveness-core'`, held, int(meshHeartbeatRefreshAfterSeconds)+1)
			sync(t, "mesh-2", 80)
			if got := row(t, "liveness-core"); got.status != held || age(t, "liveness-core", "last_heartbeat") > 5 {
				t.Fatalf("a sync of a node in %s: %+v, want the status kept and the heartbeat refreshed", held, got)
			}
		}
	})
}
