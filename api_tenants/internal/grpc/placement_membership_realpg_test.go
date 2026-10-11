//go:build schema_verify

package grpc

import (
	"context"
	"database/sql"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	quartermasterpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/quartermaster"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/testutil/dockerpg"
	"google.golang.org/grpc"
)

func TestWatchMembership_DrainWakesAndExcludesNode_RealPG(t *testing.T) {
	verifyWatchMembershipDrainWakesAndExcludesNode(t, openQuartermasterRealPG(t, "fw-qm-membership-drain"))
}

func TestWatchMembership_DrainWakesAndExcludesNode_RealYugabyte(t *testing.T) {
	db, ok := dockerpg.OpenSharedYugabyteBaseline(t, "quartermaster_membership_drain", "quartermaster")
	if !ok {
		t.Skip("requires shared Yugabyte contract fixture")
	}
	verifyWatchMembershipDrainWakesAndExcludesNode(t, db)
}

func TestWatchMembership_ReassignRemovesCluster_RealPG(t *testing.T) {
	verifyWatchMembershipReassignRemovesCluster(t, openQuartermasterRealPG(t, "fw-qm-membership-reassign"))
}

func TestWatchMembership_ReassignRemovesCluster_RealYugabyte(t *testing.T) {
	db, ok := dockerpg.OpenSharedYugabyteBaseline(t, "quartermaster_membership_reassign", "quartermaster")
	if !ok {
		t.Skip("requires shared Yugabyte contract fixture")
	}
	verifyWatchMembershipReassignRemovesCluster(t, db)
}

func openQuartermasterRealPG(t *testing.T, prefix string) *sql.DB {
	t.Helper()
	name := fmt.Sprintf("%s-%d", prefix, time.Now().UnixNano())
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
	return db
}

type membershipTestStream struct {
	grpc.ServerStream
	ctx  context.Context
	sent chan *quartermasterpb.PlacementMembership
}

func (stream *membershipTestStream) Context() context.Context { return stream.ctx }

func (stream *membershipTestStream) Send(membership *quartermasterpb.PlacementMembership) error {
	select {
	case stream.sent <- membership:
		return nil
	case <-stream.ctx.Done():
		return stream.ctx.Err()
	}
}

// membershipWatchFixture runs the fingerprint watcher and one subscription per
// cell against a real engine.
type membershipWatchFixture struct {
	t       *testing.T
	db      *sql.DB
	server  *QuartermasterServer
	ctx     context.Context
	streams map[string]*membershipTestStream
}

func newMembershipWatchFixture(t *testing.T, db *sql.DB, cells ...string) *membershipWatchFixture {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	server := NewQuartermasterServer(db, logging.NewLogger(), nil, nil, nil, nil, nil)
	server.membershipWatch.ctx = ctx
	go server.RunPlacementMembershipWatcher(ctx, logging.NewLogger())
	fixture := &membershipWatchFixture{t: t, db: db, server: server, ctx: ctx, streams: map[string]*membershipTestStream{}}
	serviceCtx := context.WithValue(ctx, ctxkeys.KeyAuthType, "service")
	for _, cell := range cells {
		stream := &membershipTestStream{ctx: serviceCtx, sent: make(chan *quartermasterpb.PlacementMembership, 8)}
		fixture.streams[cell] = stream
		go func() {
			_ = server.WatchPlacementMembership(&quartermasterpb.GetPlacementMembershipRequest{ControlCellId: cell}, stream)
		}()
	}
	return fixture
}

func (fixture *membershipWatchFixture) exec(query string, args ...any) {
	fixture.t.Helper()
	if _, err := fixture.db.ExecContext(fixture.ctx, query, args...); err != nil {
		fixture.t.Fatalf("%s: %v", query, err)
	}
}

// next waits for the cell's next membership. Wakes are fingerprint-driven on a
// 2 s cadence, so a change arrives within a few intervals.
func (fixture *membershipWatchFixture) next(cell string) *quartermasterpb.PlacementMembership {
	fixture.t.Helper()
	select {
	case membership := <-fixture.streams[cell].sent:
		return membership
	case <-time.After(4*peerChangeInterval + 2*time.Second):
		fixture.t.Fatalf("cell %s received no membership", cell)
		return nil
	}
}

// quiet asserts the cell receives nothing across several watcher intervals.
func (fixture *membershipWatchFixture) quiet(cell, reason string) {
	fixture.t.Helper()
	select {
	case membership := <-fixture.streams[cell].sent:
		fixture.t.Fatalf("%s woke cell %s with %v", reason, cell, membership)
	case <-time.After(2*peerChangeInterval + time.Second):
	}
}

func membershipNodes(membership *quartermasterpb.PlacementMembership) map[string]bool {
	out := map[string]bool{}
	for _, node := range membership.GetNodes() {
		out[node.GetClusterId()+"/"+node.GetNodeId()] = node.GetAdmissionEnabled()
	}
	return out
}

func membershipClusters(membership *quartermasterpb.PlacementMembership) []string {
	var out []string
	for _, cluster := range membership.GetClusters() {
		out = append(out, fmt.Sprintf("%s:%t", cluster.GetClusterId(), cluster.GetIsActive()))
	}
	return out
}

func seedMembershipCells(t *testing.T, db *sql.DB) {
	t.Helper()
	exec := func(query string) {
		t.Helper()
		if _, err := db.Exec(query); err != nil {
			t.Fatalf("%s: %v", query, err)
		}
	}
	exec(`INSERT INTO quartermaster.infrastructure_clusters (cluster_id, cluster_name, cluster_type, base_url, control_cell_id, cell_id)
	      VALUES ('member-a', 'Member A', 'edge', 'https://member-a.example', 'member-cell', NULL),
	             ('member-b', 'Member B', 'edge', 'https://member-b.example', NULL, 'member-cell'),
	             ('member-other', 'Member other', 'edge', 'https://member-other.example', 'other-cell', NULL)`)
	exec(`INSERT INTO quartermaster.infrastructure_nodes (node_id, cluster_id, node_name, node_type, status, external_ip)
	      VALUES ('member-a-1', 'member-a', 'A1', 'edge', 'active', '203.0.113.51'),
	             ('member-a-2', 'member-a', 'A2', 'edge', 'active', '203.0.113.52'),
	             ('member-b-1', 'member-b', 'B1', 'edge', 'active', '203.0.113.53'),
	             ('member-other-1', 'member-other', 'O1', 'edge', 'active', '203.0.113.54')`)
}

// A drained edge wakes the subscription and arrives as a non-admitting member;
// heartbeats and another cell's changes wake nobody in this cell.
func verifyWatchMembershipDrainWakesAndExcludesNode(t *testing.T, db *sql.DB) {
	t.Helper()
	seedMembershipCells(t, db)
	fixture := newMembershipWatchFixture(t, db, "member-cell")
	first := fixture.next("member-cell")
	if !first.GetComplete() || first.GetRevision() == "" || first.GetControlCellId() != "member-cell" ||
		!slices.Equal(membershipClusters(first), []string{"member-a:true", "member-b:true"}) {
		t.Fatalf("initial membership = %v", first)
	}
	if nodes := membershipNodes(first); len(nodes) != 3 || !nodes["member-a/member-a-1"] || !nodes["member-a/member-a-2"] || !nodes["member-b/member-b-1"] {
		t.Fatalf("initial nodes = %v", nodes)
	}

	fixture.exec(`UPDATE quartermaster.infrastructure_nodes SET last_heartbeat = NOW(), updated_at = NOW() WHERE cluster_id IN ('member-a', 'member-b')`)
	fixture.quiet("member-cell", "a heartbeat")
	fixture.exec(`UPDATE quartermaster.infrastructure_nodes SET status = 'maintenance' WHERE node_id = 'member-other-1'`)
	fixture.quiet("member-cell", "another cell's drain")

	serviceCtx := context.WithValue(context.Background(), ctxkeys.KeyAuthType, "service")
	if _, err := fixture.server.UpdateNodeStatus(serviceCtx, &quartermasterpb.UpdateNodeStatusRequest{NodeId: "member-a-2", Status: "maintenance"}); err != nil {
		t.Fatalf("UpdateNodeStatus: %v", err)
	}
	drained := fixture.next("member-cell")
	if drained.GetRevision() == first.GetRevision() {
		t.Fatal("a drained node left the membership revision unchanged")
	}
	if nodes := membershipNodes(drained); len(nodes) != 3 || nodes["member-a/member-a-2"] || !nodes["member-a/member-a-1"] || !nodes["member-b/member-b-1"] {
		t.Fatalf("drained membership = %v", nodes)
	}
	// Moving between two non-admitting states changes nothing a cell places on.
	fixture.exec(`UPDATE quartermaster.infrastructure_nodes SET status = 'offline' WHERE node_id = 'member-a-2'`)
	fixture.quiet("member-cell", "a non-admitting status change")

	got, err := fixture.server.GetPlacementMembership(serviceCtx, &quartermasterpb.GetPlacementMembershipRequest{ControlCellId: "member-cell"})
	if err != nil || got.GetRevision() != drained.GetRevision() {
		t.Fatalf("unary membership %v disagrees with the stream %s: %v", got, drained.GetRevision(), err)
	}
}

// Moving a cluster to another control cell removes it and its nodes from the
// previous cell's membership and adds them to the new cell's; deactivation is
// published with the cluster's flag.
func verifyWatchMembershipReassignRemovesCluster(t *testing.T, db *sql.DB) {
	t.Helper()
	seedMembershipCells(t, db)
	fixture := newMembershipWatchFixture(t, db, "member-cell", "other-cell")
	if got := membershipClusters(fixture.next("member-cell")); !slices.Equal(got, []string{"member-a:true", "member-b:true"}) {
		t.Fatalf("initial member-cell clusters = %v", got)
	}
	if got := membershipClusters(fixture.next("other-cell")); !slices.Equal(got, []string{"member-other:true"}) {
		t.Fatalf("initial other-cell clusters = %v", got)
	}

	fixture.exec(`UPDATE quartermaster.infrastructure_clusters SET control_cell_id = 'other-cell' WHERE cluster_id = 'member-b'`)
	moved := fixture.next("member-cell")
	if got := membershipClusters(moved); !slices.Equal(got, []string{"member-a:true"}) {
		t.Fatalf("member-cell clusters after reassignment = %v", got)
	}
	if nodes := membershipNodes(moved); len(nodes) != 2 || nodes["member-b/member-b-1"] {
		t.Fatalf("member-cell nodes after reassignment = %v", nodes)
	}
	gained := fixture.next("other-cell")
	if got := membershipClusters(gained); !slices.Equal(got, []string{"member-b:true", "member-other:true"}) {
		t.Fatalf("other-cell clusters after reassignment = %v", got)
	}
	if nodes := membershipNodes(gained); !nodes["member-b/member-b-1"] {
		t.Fatalf("other-cell nodes after reassignment = %v", nodes)
	}

	fixture.exec(`UPDATE quartermaster.infrastructure_clusters SET is_active = false WHERE cluster_id = 'member-a'`)
	if got := membershipClusters(fixture.next("member-cell")); !slices.Equal(got, []string{"member-a:false"}) {
		t.Fatalf("member-cell clusters after deactivation = %v", got)
	}
	fixture.quiet("other-cell", "another cell's deactivation")
}
