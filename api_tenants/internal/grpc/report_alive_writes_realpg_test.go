//go:build schema_verify

package grpc

import (
	"context"
	"database/sql"
	"fmt"
	"testing"
	"time"

	"frameworks/api_tenants/internal/database/quartermasterdb"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	quartermasterpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/quartermaster"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/testutil/dockerpg"
)

func TestUnchangedHealthReportsLeaveRowsAndPeerCensusAlone_RealPG(t *testing.T) {
	name := fmt.Sprintf("fw-qm-quiet-reports-%d", time.Now().UnixNano())
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
	verifyUnchangedHealthReportsLeaveRowsAndPeerCensusAlone(t, db)
}

func TestUnchangedHealthReportsLeaveRowsAndPeerCensusAlone_RealYugabyte(t *testing.T) {
	db, ok := dockerpg.OpenSharedYugabyteBaseline(t, "quartermaster_quiet_reports", "quartermaster")
	if !ok {
		t.Skip("requires shared Yugabyte contract fixture")
	}
	verifyUnchangedHealthReportsLeaveRowsAndPeerCensusAlone(t, db)
}

// Foghorn re-reports every edge and the health poller re-checks every service
// instance on a short cadence. A report that changes nothing must not rewrite
// the row until its check nears the DNS stale bound, and must not move the
// peer census fingerprint, which would wake every WatchPeers subscriber into a
// ListPeers re-read.
func verifyUnchangedHealthReportsLeaveRowsAndPeerCensusAlone(t *testing.T, db *sql.DB) { //nolint:funlen // One engine fixture walks edge reports, health verdicts, and census changes.
	t.Helper()
	ctx := context.Background()
	const tenant = "11111111-1111-4111-8111-111111111191"
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := db.ExecContext(ctx, query, args...); err != nil {
			t.Fatalf("%s: %v", query, err)
		}
	}
	exec(`INSERT INTO quartermaster.tenants (id, name, deployment_tier) VALUES ($1::uuid, 'Quiet reports', 'production')`, tenant)
	exec(`INSERT INTO quartermaster.infrastructure_clusters (cluster_id, cluster_name, cluster_type, base_url)
	      VALUES ('quiet-source', 'Quiet source', 'edge', 'https://quiet-source.example'),
	             ('quiet-peer', 'Quiet peer', 'edge', 'https://quiet-peer.example')`)
	exec(`INSERT INTO quartermaster.tenant_cluster_access (tenant_id, cluster_id, access_source, subscription_status, is_active)
	      VALUES ($1::uuid, 'quiet-source', 'platform_tier', 'active', true), ($1::uuid, 'quiet-peer', 'platform_tier', 'active', true)`, tenant)
	exec(`INSERT INTO quartermaster.services (service_id, name, plane, type, protocol) VALUES ('quiet-foghorn', 'Quiet Foghorn', 'control', 'foghorn', 'grpc')`)
	exec(`INSERT INTO quartermaster.service_instances (instance_id, cluster_id, service_id, protocol, advertise_host, port, status, health_status, last_health_check)
	      VALUES ('quiet-foghorn-1', 'quiet-peer', 'quiet-foghorn', 'grpc', 'foghorn.quiet', 18019, 'running', 'healthy', NOW())`)
	exec(`INSERT INTO quartermaster.service_cluster_assignments (service_instance_id, cluster_id)
	      SELECT id, 'quiet-peer' FROM quartermaster.service_instances WHERE instance_id = 'quiet-foghorn-1'`)
	exec(`INSERT INTO quartermaster.infrastructure_nodes (node_id, cluster_id, node_name, node_type, status, external_ip)
	      VALUES ('quiet-edge', 'quiet-source', 'Quiet edge', 'edge', 'active', '203.0.113.40')`)

	server := NewQuartermasterServer(db, logging.NewLogger(), nil, nil, nil, nil, nil)
	serviceCtx := context.WithValue(ctx, ctxkeys.KeyAuthType, "service")
	report := func(egress bool) {
		t.Helper()
		if _, err := server.ReportAliveNodes(serviceCtx, &quartermasterpb.ReportAliveNodesRequest{
			Nodes: []*quartermasterpb.NodeAliveness{{
				NodeId: "quiet-edge", ClusterId: "quiet-source", IsHealthy: true, ExternalIp: "203.0.113.40",
				Capabilities: &quartermasterpb.EdgeCapabilities{Ingest: true, Egress: egress},
			}},
		}); err != nil {
			t.Fatalf("ReportAliveNodes: %v", err)
		}
	}
	type rowState struct{ health, checked, updated string }
	rows := func() map[string]rowState {
		t.Helper()
		out := map[string]rowState{}
		result, err := db.QueryContext(ctx, `
			SELECT instance_id, health_status, last_health_check::text, updated_at::text
			FROM quartermaster.service_instances
			WHERE node_id = 'quiet-edge' OR instance_id = 'quiet-foghorn-1'`)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = result.Close() }()
		for result.Next() {
			var id string
			var state rowState
			if err := result.Scan(&id, &state.health, &state.checked, &state.updated); err != nil {
				t.Fatal(err)
			}
			out[id] = state
		}
		if err := result.Err(); err != nil {
			t.Fatal(err)
		}
		return out
	}
	census := func() string {
		t.Helper()
		fingerprint, err := server.peerCensusFingerprint(ctx)
		if err != nil {
			t.Fatalf("peer census fingerprint: %v", err)
		}
		return fingerprint
	}
	const ingest, egress = "edge-cap-quiet-edge-edge-ingest", "edge-cap-quiet-edge-edge-egress"

	report(true)
	before, quiet := rows(), census()
	if before[ingest].health != "healthy" || before[egress].health != "healthy" {
		t.Fatalf("first report did not materialize healthy capability rows: %+v", before)
	}
	// Each statement's NOW() is its own transaction start, so a rewrite in the
	// same microsecond as the original write is the only way a write could hide.
	time.Sleep(10 * time.Millisecond)
	report(true)
	report(true)
	if after := rows(); fmt.Sprint(after) != fmt.Sprint(before) {
		t.Fatalf("unchanged edge reports rewrote rows:\nbefore %v\nafter  %v", before, after)
	}
	if got := census(); got != quiet {
		t.Fatal("unchanged edge reports moved the peer census fingerprint")
	}

	report(false)
	dropped := rows()
	if dropped[egress].health != "unhealthy" || dropped[ingest] != before[ingest] {
		t.Fatalf("dropping egress must mark only egress unhealthy: %+v", dropped)
	}
	time.Sleep(10 * time.Millisecond)
	report(false)
	if got := rows(); got[egress] != dropped[egress] {
		t.Fatalf("repeating a dropped capability rewrote the unhealthy row: %+v -> %+v", dropped[egress], got[egress])
	}

	// An unchanged healthy edge row is refreshed once its check is old enough
	// that two more Foghorn snapshots could otherwise let it go stale for DNS.
	edgeRefresh := quartermasterdb.HealthRefreshAfterSeconds(defaultPhysicalEndpointStaleSeconds, foghornEdgeSnapshotInterval)
	if edgeRefresh <= 0 || int(edgeRefresh)+2*int(foghornEdgeSnapshotInterval/time.Second) > defaultPhysicalEndpointStaleSeconds {
		t.Fatalf("edge refresh bound %ds does not leave two %s snapshots before the %ds stale bound", edgeRefresh, foghornEdgeSnapshotInterval, defaultPhysicalEndpointStaleSeconds)
	}
	exec(`UPDATE quartermaster.service_instances SET last_health_check = NOW() - make_interval(secs => $1) WHERE instance_id = $2`, int(edgeRefresh)-10, ingest)
	young := rows()[ingest]
	report(false)
	if got := rows()[ingest]; got != young {
		t.Fatalf("a check younger than the refresh bound was rewritten: %+v -> %+v", young, got)
	}
	exec(`UPDATE quartermaster.service_instances SET last_health_check = NOW() - make_interval(secs => $1) WHERE instance_id = $2`, int(edgeRefresh)+1, ingest)
	report(false)
	var ingestAge float64
	if err := db.QueryRowContext(ctx, `SELECT EXTRACT(EPOCH FROM NOW() - last_health_check)::float8 FROM quartermaster.service_instances WHERE instance_id = $1`, ingest).Scan(&ingestAge); err != nil {
		t.Fatal(err)
	}
	if ingestAge > 5 {
		t.Fatalf("a check past the refresh bound was not refreshed: %.0fs old", ingestAge)
	}
	if got := census(); got != quiet {
		t.Fatal("edge refreshes moved the peer census fingerprint")
	}

	// The health poller's verdicts follow the same rule.
	const pollRefresh int32 = 165
	persist := func(status string) string {
		t.Helper()
		row, err := quartermasterdb.New(db).PersistServiceHealthStatus(ctx, quartermasterdb.PersistServiceHealthStatusParams{
			InstanceID: "quiet-foghorn-1", Status: status, RefreshAfterSeconds: pollRefresh,
		})
		if err != nil {
			t.Fatalf("PersistServiceHealthStatus: %v", err)
		}
		return row.OldStatus
	}
	foghornBefore := rows()["quiet-foghorn-1"]
	time.Sleep(10 * time.Millisecond)
	if previous := persist("healthy"); previous != "healthy" {
		t.Fatalf("unchanged verdict reported previous status %q, want healthy", previous)
	}
	if got := rows()["quiet-foghorn-1"]; got != foghornBefore {
		t.Fatalf("an unchanged healthy verdict rewrote the row: %+v -> %+v", foghornBefore, got)
	}
	exec(`UPDATE quartermaster.service_instances SET last_health_check = NOW() - make_interval(secs => $1) WHERE instance_id = 'quiet-foghorn-1'`, int(pollRefresh)+1)
	persist("healthy")
	var foghornAge float64
	if err := db.QueryRowContext(ctx, `SELECT EXTRACT(EPOCH FROM NOW() - last_health_check)::float8 FROM quartermaster.service_instances WHERE instance_id = 'quiet-foghorn-1'`).Scan(&foghornAge); err != nil {
		t.Fatal(err)
	}
	if foghornAge > 5 {
		t.Fatalf("a verdict past the refresh bound did not refresh the check: %.0fs old", foghornAge)
	}
	if got := census(); got != quiet {
		t.Fatal("a health refresh that changed no status moved the peer census fingerprint")
	}

	// Changes a peer set reads still move the fingerprint.
	if previous := persist("unhealthy"); previous != "healthy" {
		t.Fatalf("transition reported previous status %q, want healthy", previous)
	}
	unhealthy := census()
	if unhealthy == quiet {
		t.Fatal("a Foghorn health transition left the peer census fingerprint unchanged")
	}
	persist("healthy")
	if got := census(); got != quiet {
		t.Fatal("restoring the Foghorn's health did not restore the peer census fingerprint")
	}
	exec(`DELETE FROM quartermaster.tenant_cluster_access WHERE tenant_id = $1::uuid AND cluster_id = 'quiet-peer'`, tenant)
	if got := census(); got == quiet {
		t.Fatal("a revoked grant left the peer census fingerprint unchanged")
	}
	exec(`UPDATE quartermaster.infrastructure_clusters SET cluster_name = 'Renamed' WHERE cluster_id = 'quiet-source'`)
	renamed := census()
	exec(`UPDATE quartermaster.infrastructure_clusters SET cluster_name = 'Renamed', updated_at = NOW() + INTERVAL '1 hour' WHERE cluster_id = 'quiet-source'`)
	if got := census(); got != renamed {
		t.Fatal("a cluster rewrite that changed no peer value moved the peer census fingerprint")
	}
}
