//go:build schema_verify

package mediaauthority

import (
	"context"
	"testing"
	"time"

	mediaauthoritypb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/media_authority"
)

// A peer cell that missed the origin's retire command, because it or the
// origin was unreachable at delete time, keeps its adopted pointer until the
// signed artifact tombstone reaches it; applying that tombstone retires the
// pointer.
func TestArtifactTombstoneRetiresAPointerThatMissedTheRetire_RealPG(t *testing.T) {
	conn := startMediaAuthorityRealPG(t)
	_, trust, _ := storeFixture(t, "cell-a")
	store, err := NewStore(conn, "cell-a", trust)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	store.now = func() time.Time { return storeFixtureNow.Add(time.Minute) }
	ctx := context.Background()
	if _, err := conn.ExecContext(ctx, `INSERT INTO foghorn.artifacts
		(artifact_hash, artifact_type, tenant_id, status, storage_location, sync_status, origin_cluster_id, federated_pointer)
		VALUES ('artifact-hash', 'vod', '10000000-0000-0000-0000-000000000001', 'ready', 's3', 'synced', 'cell-eu', true)`); err != nil {
		t.Fatal(err)
	}
	pointerStatus := func() string {
		t.Helper()
		var status string
		if err := conn.QueryRowContext(ctx, `SELECT status FROM foghorn.artifacts WHERE artifact_hash = 'artifact-hash'`).Scan(&status); err != nil {
			t.Fatal(err)
		}
		return status
	}

	active, _ := signedArtifactFixture(t, mediaauthoritypb.AuthorityLifecycle_AUTHORITY_LIFECYCLE_ACTIVE, 1)
	if _, err := store.Apply(ctx, active); err != nil {
		t.Fatalf("apply active authority: %v", err)
	}
	if got := pointerStatus(); got != "ready" {
		t.Fatalf("pointer under active authority = %q, want ready", got)
	}
	tombstone, _ := signedArtifactFixture(t, mediaauthoritypb.AuthorityLifecycle_AUTHORITY_LIFECYCLE_TOMBSTONE, 2)
	if _, err := store.Apply(ctx, tombstone); err != nil {
		t.Fatalf("apply tombstone: %v", err)
	}
	if got := pointerStatus(); got != "deleted" {
		t.Fatalf("pointer after the signed tombstone = %q, want deleted", got)
	}
}
