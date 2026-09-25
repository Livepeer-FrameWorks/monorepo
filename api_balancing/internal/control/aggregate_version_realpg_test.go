//go:build schema_verify

package control

import (
	"context"
	"database/sql"
	"errors"
	"sort"
	"sync"
	"testing"
	"time"

	"frameworks/api_balancing/internal/artifactoutbox"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	publicv1 "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/events/public/v1"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
	"google.golang.org/protobuf/proto"
)

// aggregateVersions returns the aggregate versions of aggregateID's domain
// events in outbox order.
func aggregateVersions(t *testing.T, conn *sql.DB, aggregateID string) []int64 {
	t.Helper()
	rows, err := conn.Query(`
		SELECT aggregate_version FROM foghorn.domain_event_outbox
		WHERE aggregate_id = $1 ORDER BY enqueued_at, event_id`, aggregateID)
	if err != nil {
		t.Fatalf("read aggregate versions: %v", err)
	}
	defer rows.Close()
	var out []int64
	for rows.Next() {
		var v int64
		if err := rows.Scan(&v); err != nil {
			t.Fatal(err)
		}
		out = append(out, v)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

func artifactRevision(t *testing.T, conn *sql.DB, hash string) int64 {
	t.Helper()
	var r int64
	if err := conn.QueryRow(`SELECT revision FROM foghorn.artifacts WHERE artifact_hash = $1`, hash).Scan(&r); err != nil {
		t.Fatal(err)
	}
	return r
}

func insertClipArtifact(t *testing.T, conn *sql.DB, hash, status string) {
	t.Helper()
	if _, err := conn.Exec(`
		INSERT INTO foghorn.artifacts (artifact_hash, artifact_type, tenant_id, stream_id, status)
		VALUES ($1, 'clip', $2::uuid, $3::uuid, $4)`, hash, domainTenant, domainStream, status); err != nil {
		t.Fatalf("insert clip: %v", err)
	}
}

// applyClipTransition moves the clip to status and enqueues fact in tx, the way
// the production transition sites do.
func applyClipTransition(ctx context.Context, tx *sql.Tx, hash, status string, fact proto.Message) error {
	res, err := tx.ExecContext(ctx, `UPDATE foghorn.artifacts SET status = $2 WHERE artifact_hash = $1`, hash, status)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return errUnexpectedRows
	}
	tenant := domainTenant
	return artifactoutbox.EnqueueClipTransitionTx(ctx, tx, &ipcpb.ClipLifecycleData{
		TenantId: &tenant, ClipHash: hash,
	}, fact)
}

// Every artifact lifecycle event carries its artifact's revision: successive
// transitions of one artifact carry strictly increasing versions, a rolled-back
// transition consumes none, and a chapter event advances its parent recording.
func TestArtifactAggregateVersions_RealPG(t *testing.T) {
	conn := startRealPG(t)
	prev := db
	SetDB(conn)
	t.Cleanup(func() { SetDB(prev) })
	ctx := context.Background()
	ended := time.Unix(1_700_000_000, 0)

	// A transition whose domain outbox write fails rolls back its revision with it.
	const dvr = "dvraggversion0000000000000000001"
	insertFinalizingDVR(t, conn, dvr)
	if _, err := conn.Exec(`ALTER TABLE foghorn.domain_event_outbox RENAME TO domain_event_outbox_offline`); err != nil {
		t.Fatal(err)
	}
	if _, err := setArtifactFailed(ctx, dvr, "writer crashed", nil, ended, domainTenant, false); err == nil {
		t.Fatal("setArtifactFailed must fail when the domain event cannot be written")
	}
	if _, err := conn.Exec(`ALTER TABLE foghorn.domain_event_outbox_offline RENAME TO domain_event_outbox`); err != nil {
		t.Fatal(err)
	}
	if r := artifactRevision(t, conn, dvr); r != 0 {
		t.Fatalf("a rolled-back transition must not consume a revision, revision=%d", r)
	}

	// The production recording.failed path stamps the artifact's first revision.
	if applied, err := setArtifactFailed(ctx, dvr, "writer crashed", nil, ended, domainTenant, false); err != nil || !applied {
		t.Fatalf("setArtifactFailed applied=%v err=%v", applied, err)
	}
	if got := aggregateVersions(t, conn, dvr); len(got) != 1 || got[0] != 1 {
		t.Fatalf("recording.failed versions = %v, want [1]", got)
	}

	// Two committed clip transitions carry strictly increasing versions; an explicitly
	// rolled-back one between them consumes none.
	const clip = "clipaggversion000000000000000001"
	insertClipArtifact(t, conn, clip, "requested")
	commitTransition := func(status string, fact proto.Message) {
		t.Helper()
		tx, err := conn.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback() //nolint:errcheck // no-op after Commit
		if err := applyClipTransition(ctx, tx, clip, status, fact); err != nil {
			t.Fatalf("transition to %s: %v", status, err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
	}
	commitTransition("processing", &publicv1.ClipRequested{Artifact: artifactoutbox.ClipArtifact(clip, domainStream)})

	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := applyClipTransition(ctx, tx, clip, "failed", &publicv1.ClipFailed{Artifact: artifactoutbox.ClipArtifact(clip, domainStream)}); err != nil {
		t.Fatalf("transition to failed: %v", err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}

	commitTransition("ready", &publicv1.ClipReady{Artifact: artifactoutbox.ClipArtifact(clip, domainStream)})
	if got := aggregateVersions(t, conn, clip); len(got) != 2 || got[0] != 1 || got[1] != 2 {
		t.Fatalf("clip versions = %v, want [1 2] (the rolled-back transition consumes none)", got)
	}
	if r := artifactRevision(t, conn, clip); r != 2 {
		t.Fatalf("clip revision = %d, want 2", r)
	}

	// The production chapter finalization stamps recording.chapter_ready with the parent
	// recording's next revision.
	const (
		parent    = "dvraggchapterparent0000000000001"
		chapter   = "chapaggversion000000000000000001"
		chapterID = "chapidaggversion0000000000000001"
	)
	insertFinalizingDVR(t, conn, parent)
	if _, err := conn.Exec(`UPDATE foghorn.artifacts SET revision = 4 WHERE artifact_hash = $1`, parent); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(`
		INSERT INTO foghorn.artifacts (artifact_hash, artifact_type, tenant_id, status, origin_type, origin_id, library_visible)
		VALUES ($1, 'vod', $2::uuid, 'finalizing', 'dvr_chapter', $3, false)`, chapter, domainTenant, chapterID); err != nil {
		t.Fatalf("insert chapter artifact: %v", err)
	}
	if _, err := conn.Exec(`
		INSERT INTO foghorn.dvr_chapters (chapter_id, artifact_hash, mode, start_ms, end_ms, state,
			playback_artifact_hash, finalize_attempts, finalize_node_id)
		VALUES ($1, $2, 'fixed_interval', 0, 60000, 'finalizing', $3, 1, 'node-a')`, chapterID, parent, chapter); err != nil {
		t.Fatalf("insert chapter: %v", err)
	}
	if _, err := finalizeChapterArtifactTx(ctx, chapterID, chapter, "", "node-a", 1, 1024, 3, false, 0, 60000, 60000,
		false, "", nil, logging.NewLogger(), logging.Fields{}); err != nil {
		t.Fatalf("finalize chapter: %v", err)
	}
	// The recording's first finalized chapter also announces recording.ready,
	// which takes the revision after recording.chapter_ready.
	if got := aggregateVersions(t, conn, parent); len(got) != 2 || got[0] != 5 || got[1] != 6 {
		t.Fatalf("recording.chapter_ready + recording.ready versions = %v, want [5 6] (the parent's next revisions)", got)
	}
	if r := artifactRevision(t, conn, chapter); r != 0 {
		t.Fatalf("the chapter's own playback artifact carries no event revision, got %d", r)
	}
}

// Two Foghorn replicas transitioning one artifact at once: the replica that
// commits first holds the lower version, whichever started first.
func TestArtifactAggregateVersionReplicaRace_RealPG(t *testing.T) {
	conn := startRealPG(t)
	prev := db
	SetDB(conn)
	t.Cleanup(func() { SetDB(prev) })
	ctx := context.Background()
	const clip = "clipaggrace000000000000000000001"
	insertClipArtifact(t, conn, clip, "processing")

	// Each replica runs on its own database session.
	replicaA, err := conn.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer replicaA.Close()
	replicaB, err := conn.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer replicaB.Close()

	// Replica A opens its transaction first but its transition is still in flight.
	txA, err := replicaA.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer txA.Rollback() //nolint:errcheck // no-op after Commit
	txB, err := replicaB.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer txB.Rollback() //nolint:errcheck // no-op after Commit

	// Replica B transitions and holds its uncommitted revision.
	if err := applyClipTransition(ctx, txB, clip, "failed", &publicv1.ClipFailed{Artifact: artifactoutbox.ClipArtifact(clip, domainStream)}); err != nil {
		t.Fatalf("replica B transition: %v", err)
	}
	// Replica A's transition must wait for B instead of reading B's uncommitted revision.
	doneA := make(chan error, 1)
	go func() {
		doneA <- applyClipTransition(ctx, txA, clip, "ready", &publicv1.ClipReady{Artifact: artifactoutbox.ClipArtifact(clip, domainStream)})
	}()
	waitForLockWaiter(t, conn)
	select {
	case err := <-doneA:
		t.Fatalf("replica A finished while replica B held the artifact: %v", err)
	default:
	}
	if err := txB.Commit(); err != nil {
		t.Fatal(err)
	}
	if err := <-doneA; err != nil {
		t.Fatalf("replica A transition: %v", err)
	}
	if err := txA.Commit(); err != nil {
		t.Fatal(err)
	}

	rows := domainRows(t, conn, "clip.failed")
	rows = append(rows, domainRows(t, conn, "clip.ready")...)
	if len(rows) != 2 {
		t.Fatalf("want one clip.failed and one clip.ready, got %d", len(rows))
	}
	versions := map[string]int64{}
	for _, typ := range []string{"clip.failed", "clip.ready"} {
		var v int64
		if err := conn.QueryRow(`SELECT aggregate_version FROM foghorn.domain_event_outbox WHERE event_type = $1`, typ).Scan(&v); err != nil {
			t.Fatal(err)
		}
		versions[typ] = v
	}
	if versions["clip.failed"] != 1 || versions["clip.ready"] != 2 {
		t.Fatalf("versions = %v; B committed first and must hold the lower version", versions)
	}
	if s := artifactStatus(t, conn, clip); s != "ready" {
		t.Fatalf("the last committed transition owns the state, status=%q", s)
	}

	// Many replicas racing on one artifact never share or skip a version.
	const racers = 8
	var wg sync.WaitGroup
	errs := make(chan error, racers)
	for i := 0; i < racers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			tx, err := conn.BeginTx(ctx, nil)
			if err != nil {
				errs <- err
				return
			}
			defer tx.Rollback() //nolint:errcheck // no-op after Commit
			if err := applyClipTransition(ctx, tx, clip, "ready", &publicv1.ClipReady{Artifact: artifactoutbox.ClipArtifact(clip, domainStream)}); err != nil {
				errs <- err
				return
			}
			errs <- tx.Commit()
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("racing transition: %v", err)
		}
	}
	got := aggregateVersions(t, conn, clip)
	sort.Slice(got, func(i, j int) bool { return got[i] < got[j] })
	if len(got) != racers+2 {
		t.Fatalf("want %d events, got %d", racers+2, len(got))
	}
	for i, v := range got {
		if v != int64(i+1) {
			t.Fatalf("versions %v must be exactly 1..%d", got, racers+2)
		}
	}
}

// waitForLockWaiter blocks until some session waits on a row lock.
func waitForLockWaiter(t *testing.T, conn *sql.DB) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		var waiting bool
		if err := conn.QueryRow(`SELECT EXISTS (SELECT 1 FROM pg_stat_activity WHERE wait_event_type = 'Lock')`).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("no session waited on the artifact row lock")
}

var errUnexpectedRows = errors.New("clip transition matched no row")
