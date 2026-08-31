//go:build schema_verify

package jobs

import (
	"context"
	"sync"
	"testing"
	"time"

	"frameworks/api_balancing/internal/artifacts"
	"frameworks/api_balancing/internal/database/foghorndb"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
)

type blockingThumbnailS3 struct {
	*fakeS3
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

type selectiveBlockingThumbnailS3 struct {
	*fakeS3
	slowPrefix string
	started    chan struct{}
	release    chan struct{}
	once       sync.Once
	mu         sync.Mutex
	calls      []string
}

func (b *selectiveBlockingThumbnailS3) DeletePrefix(ctx context.Context, prefix string) (int, error) {
	if prefix == b.slowPrefix {
		b.once.Do(func() { close(b.started) })
		select {
		case <-b.release:
		case <-ctx.Done():
			return 0, ctx.Err()
		}
	}
	b.mu.Lock()
	b.calls = append(b.calls, prefix)
	b.mu.Unlock()
	return 0, nil
}

func (b *blockingThumbnailS3) DeletePrefix(ctx context.Context, prefix string) (int, error) {
	b.once.Do(func() { close(b.started) })
	select {
	case <-b.release:
		b.deletePrefixCalls = append(b.deletePrefixCalls, prefix)
		return 0, nil
	case <-ctx.Done():
		return 0, ctx.Err()
	}
}

// TestPurgeOwnershipFilter_RealPG exercises the BACKEND-AFFINE ownership filter against a REAL Postgres with
// representative rows (sqlmock can only regex-match the SQL, not its NULL semantics). Production runs each cell on its
// OWN physical Foghorn database (foghorn_eu / foghorn_us) and immutable store, so a worker claims ONLY rows recorded on
// its own store. Ownership is read from recorded evidence, never reconstructed (invariant I2): a NULL backend is NOT
// claimed by cluster. A remaining NULL is unattributed and RETAINED (a safe row leak), never deleted from a guessed
// store.
// The contract:
//   - backend_id = this cell's store             -> claimed (reaped)
//   - backend_id = another cell's store           -> retained (never a no-op wrong-store delete then hard-delete)
//   - backend_id NULL (any cluster / durable flag) -> retained (unattributed; fail closed, never claimed by cluster)
func TestPurgeOwnershipFilter_RealPG(t *testing.T) {
	conn := startRealPGForCleanup(t)
	ctx := context.Background()
	const tid = "11111111-1111-1111-1111-111111111111"

	// seed a purge-eligible deleted clip: past retention, catalog deletion acked, no non-orphaned node copies.
	seed := func(hash, storageCluster, backendID string, durableLocal bool) {
		nullable := func(s string) interface{} {
			if s == "" {
				return nil
			}
			return s
		}
		if _, err := conn.Exec(`
			INSERT INTO foghorn.artifacts
			  (artifact_hash, artifact_type, tenant_id, status, sync_status, storage_location,
			   storage_cluster_id, origin_cluster_id, durable_backend_local, backend_id,
			   catalog_revision, catalog_synced_rev, updated_at)
			VALUES ($1, 'clip', $2::uuid, 'deleted', 'synced', 'local',
			   $3, NULL, $4, $5,
			   1, 1, NOW() - INTERVAL '40 days')`, hash, tid, nullable(storageCluster), durableLocal, nullable(backendID)); err != nil {
			t.Fatalf("seed %s: %v", hash, err)
		}
		// The catalog-revision trigger bumps catalog_revision on insert (monotonic). Ack the deletion so the
		// coverage gate (catalog_synced_rev >= catalog_revision) is satisfied and the row is purge-eligible.
		if _, err := conn.Exec(`UPDATE foghorn.artifacts SET catalog_synced_rev = catalog_revision WHERE artifact_hash = $1`, hash); err != nil {
			t.Fatalf("ack catalog %s: %v", hash, err)
		}
	}
	seed("hash-backend-local", "media-eu-1", "backend-eu", false)   // our store -> reaped
	seed("hash-backend-foreign", "media-us-1", "backend-us", false) // another cell's store -> retained
	seed("hash-null-local", "platform-eu", "", false)               // NULL, local cluster -> retained (fail closed, no cluster claim)
	seed("hash-null-remote", "remote-x", "", false)                 // NULL, remote cluster -> retained
	seed("hash-null-unattributed", "", "", false)                   // NULL, no cluster -> retained
	seed("hash-null-durable-remote", "remote-x", "", true)          // NULL, durable flag must NOT attribute -> retained

	fake := &fakeS3{}
	job := NewPurgeDeletedJob(PurgeDeletedConfig{
		DB:           conn,
		Logger:       logging.NewLogger(),
		RetentionAge: 30 * 24 * time.Hour,
		Cleaner:      &artifacts.Cleaner{LocalCluster: "platform-eu", LocalBackendID: "backend-eu", S3: fake},
		// AllowCrossClusterDelete omitted → false, so the backend-affinity filter applies.
	})
	job.purgeArtifactBytesAndRows(ctx)

	exists := func(hash string) bool {
		var n int
		if err := conn.QueryRow(`SELECT count(*) FROM foghorn.artifacts WHERE artifact_hash = $1`, hash).Scan(&n); err != nil {
			t.Fatalf("count %s: %v", hash, err)
		}
		return n > 0
	}
	reaped := map[string]bool{"hash-backend-local": true}
	retained := map[string]bool{
		"hash-backend-foreign":     true,
		"hash-null-local":          true,
		"hash-null-remote":         true,
		"hash-null-unattributed":   true,
		"hash-null-durable-remote": true,
	}
	for h := range reaped {
		if exists(h) {
			t.Errorf("%s must be reaped (owned by this cell's store/cluster) but survived", h)
		}
	}
	for h := range retained {
		if !exists(h) {
			t.Errorf("%s must be retained (not this cell's store) but was hard-deleted", h)
		}
	}
}

func TestFederatedPointerPurgeDefersActiveRestoreUntilCleanupSettlement_RealPG(t *testing.T) {
	conn := startRealPGForCleanup(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	const tenantID = "11111111-1111-1111-1111-111111111111"
	const hash = "active-authority-purge-race"
	if _, err := conn.ExecContext(ctx, `
INSERT INTO foghorn.artifacts (
    artifact_hash, artifact_type, tenant_id, status, federated_pointer,
    has_thumbnails, backend_id, catalog_revision, catalog_synced_rev,
    updated_at, federated_purge_eligible_at
) VALUES ($1, 'vod', $2::uuid, 'ready', true, true, 'backend-eu', 1, 1,
          NOW() - INTERVAL '40 days', NOW() - INTERVAL '40 days')`, hash, tenantID); err != nil {
		t.Fatal(err)
	}

	storage := &blockingThumbnailS3{fakeS3: &fakeS3{}, started: make(chan struct{}), release: make(chan struct{})}
	job := NewPurgeDeletedJob(PurgeDeletedConfig{
		DB: conn, Logger: logging.NewLogger(), RetentionAge: 30 * 24 * time.Hour,
		Cleaner: &artifacts.Cleaner{LocalCluster: "platform-eu", LocalBackendID: "backend-eu", S3: storage},
	})
	done := make(chan struct{})
	go func() {
		defer close(done)
		job.purgeFederatedPointers(ctx)
	}()

	select {
	case <-storage.started:
	case <-ctx.Done():
		t.Fatal("purge did not reach thumbnail cleanup")
	}
	if err := foghorndb.New(conn).UpsertMediaObjectAuthorityProjection(ctx, foghorndb.UpsertMediaObjectAuthorityProjectionParams{
		AuthorityID: "active-during-purge", AuthorityVersion: 2, ObjectKind: "artifact",
		TenantID: tenantID, InternalName: hash, PlaybackID: "active-during-purge-playback",
		Lifecycle: "active", PlaybackPolicyKind: "public", PlaybackPolicy: []byte{},
		ArtifactID: hash, ArtifactHash: hash, ArtifactKind: "vod", ValidUntil: time.Now().Add(time.Hour),
	}); err != nil {
		t.Fatal(err)
	}
	var duringStatus string
	var duringTokenPresent bool
	if err := conn.QueryRowContext(ctx, `
SELECT status, federated_purge_token IS NOT NULL
FROM foghorn.artifacts WHERE artifact_hash=$1`, hash).Scan(&duringStatus, &duringTokenPresent); err != nil {
		t.Fatal(err)
	}
	if duringStatus != "deleted" || !duringTokenPresent {
		t.Fatalf("mid-cleanup pointer = status:%q token:%v, want deleted/true", duringStatus, duringTokenPresent)
	}

	close(storage.release)
	select {
	case <-done:
	case <-ctx.Done():
		t.Fatal("purge did not settle")
	}
	var status string
	var hasThumbnails, tokenPresent bool
	if err := conn.QueryRowContext(ctx, `
SELECT status, COALESCE(has_thumbnails, false), federated_purge_token IS NOT NULL
FROM foghorn.artifacts WHERE artifact_hash=$1`, hash).Scan(&status, &hasThumbnails, &tokenPresent); err != nil {
		t.Fatal(err)
	}
	if status != "ready" || hasThumbnails || tokenPresent {
		t.Fatalf("settled active pointer = status:%q thumbnails:%v token:%v, want ready/false/false", status, hasThumbnails, tokenPresent)
	}
	if len(storage.deletePrefixCalls) != 1 {
		t.Fatalf("thumbnail cleanup calls = %v, want one", storage.deletePrefixCalls)
	}
}

func TestFederatedPointerRecoveryDoesNotSerializeBehindSlowDestination_RealPG(t *testing.T) {
	conn := startRealPGForCleanup(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	const tenantID = "11111111-1111-1111-1111-111111111111"
	const slowHash = "recover-slow-pointer"
	const fastHash = "recover-fast-pointer"
	if _, err := conn.ExecContext(ctx, `
INSERT INTO foghorn.artifacts (
    artifact_hash, artifact_type, tenant_id, status, federated_pointer,
    has_thumbnails, backend_id, federated_purge_token,
    federated_purge_lease_until, catalog_revision, catalog_synced_rev,
    updated_at, federated_purge_eligible_at
) VALUES
    ($2, 'vod', $1::uuid, 'deleted', true, true, 'backend-eu',
     '51111111-1111-1111-1111-111111111111'::uuid, NOW() - INTERVAL '1 second',
     1, 1, NOW() - INTERVAL '40 days', NOW() - INTERVAL '40 days'),
    ($3, 'vod', $1::uuid, 'deleted', true, true, 'backend-eu',
     '52222222-2222-2222-2222-222222222222'::uuid, NOW() - INTERVAL '1 second',
     1, 1, NOW() - INTERVAL '40 days', NOW() - INTERVAL '40 days')`, tenantID, slowHash, fastHash); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.ExecContext(ctx, `
INSERT INTO foghorn.media_object_authority_projection (
    authority_id, authority_version, object_kind, tenant_id, internal_name, playback_id,
    lifecycle, playback_policy_kind, playback_policy, artifact_id, artifact_hash,
    artifact_kind, valid_until
) VALUES
    ('recover-slow-authority', 1, 'artifact', $1::uuid, $2, 'recover-slow-playback',
     'active', 'public', '\x'::bytea, $2, $2, 'vod', NOW() + INTERVAL '1 day'),
    ('recover-fast-authority', 1, 'artifact', $1::uuid, $3, 'recover-fast-playback',
     'active', 'public', '\x'::bytea, $3, $3, 'vod', NOW() + INTERVAL '1 day')`, tenantID, slowHash, fastHash); err != nil {
		t.Fatal(err)
	}
	storage := &selectiveBlockingThumbnailS3{
		fakeS3: &fakeS3{}, slowPrefix: artifacts.BuildThumbnailPrefix(slowHash),
		started: make(chan struct{}), release: make(chan struct{}),
	}
	job := NewPurgeDeletedJob(PurgeDeletedConfig{
		DB: conn, Logger: logging.NewLogger(), RetentionAge: 30 * 24 * time.Hour,
		Cleaner: &artifacts.Cleaner{LocalCluster: "platform-eu", LocalBackendID: "backend-eu", S3: storage},
	})
	done := make(chan struct{})
	go func() {
		defer close(done)
		job.purgeRecoverableFederatedPointers(ctx)
	}()
	select {
	case <-storage.started:
	case <-ctx.Done():
		t.Fatal("slow recovery candidate never reached byte cleanup")
	}
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	for {
		var status string
		if err := conn.QueryRowContext(ctx, `SELECT status FROM foghorn.artifacts WHERE artifact_hash=$1`, fastHash).Scan(&status); err != nil {
			t.Fatal(err)
		}
		if status == "ready" {
			break
		}
		select {
		case <-deadline.C:
			t.Fatal("fast recovery candidate serialized behind slow destination")
		case <-time.After(20 * time.Millisecond):
		}
	}
	var slowStatus string
	var slowToken bool
	if err := conn.QueryRowContext(ctx, `
SELECT status, federated_purge_token IS NOT NULL
FROM foghorn.artifacts WHERE artifact_hash=$1`, slowHash).Scan(&slowStatus, &slowToken); err != nil {
		t.Fatal(err)
	}
	if slowStatus != "deleted" || !slowToken {
		t.Fatalf("slow candidate lost terminal fence during cleanup: status=%q token=%v", slowStatus, slowToken)
	}
	close(storage.release)
	select {
	case <-done:
	case <-ctx.Done():
		t.Fatal("recovery pass did not settle after slow destination resumed")
	}
	if err := conn.QueryRowContext(ctx, `SELECT status FROM foghorn.artifacts WHERE artifact_hash=$1`, slowHash).Scan(&slowStatus); err != nil {
		t.Fatal(err)
	}
	if slowStatus != "ready" {
		t.Fatalf("slow candidate was not restored after cleanup: status=%q", slowStatus)
	}
	storage.mu.Lock()
	defer storage.mu.Unlock()
	if len(storage.calls) != 2 {
		t.Fatalf("thumbnail cleanup calls = %v, want both candidates", storage.calls)
	}
}

func TestDailyFederatedPointerPurgeDoesNotSerializeBehindSlowDestination_RealPG(t *testing.T) {
	conn := startRealPGForCleanup(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	const tenantID = "11111111-1111-1111-1111-111111111111"
	const slowHash = "daily-a-slow-pointer"
	const fastHash = "daily-z-fast-pointer"
	if _, err := conn.ExecContext(ctx, `
INSERT INTO foghorn.artifacts (
    artifact_hash, artifact_type, tenant_id, status, federated_pointer,
    has_thumbnails, backend_id, catalog_revision, catalog_synced_rev,
    updated_at, federated_purge_eligible_at
) VALUES
    ($2, 'vod', $1::uuid, 'ready', true, true, 'backend-eu', 1, 1,
     NOW() - INTERVAL '40 days', NOW() - INTERVAL '40 days'),
    ($3, 'vod', $1::uuid, 'ready', true, true, 'backend-eu', 1, 1,
     NOW() - INTERVAL '40 days', NOW() - INTERVAL '40 days')`, tenantID, slowHash, fastHash); err != nil {
		t.Fatal(err)
	}
	storage := &selectiveBlockingThumbnailS3{
		fakeS3: &fakeS3{}, slowPrefix: artifacts.BuildThumbnailPrefix(slowHash),
		started: make(chan struct{}), release: make(chan struct{}),
	}
	job := NewPurgeDeletedJob(PurgeDeletedConfig{
		DB: conn, Logger: logging.NewLogger(), RetentionAge: 30 * 24 * time.Hour,
		Cleaner: &artifacts.Cleaner{LocalCluster: "platform-eu", LocalBackendID: "backend-eu", S3: storage},
	})
	done := make(chan struct{})
	go func() {
		defer close(done)
		job.purgeFederatedPointers(ctx)
	}()
	select {
	case <-storage.started:
	case <-ctx.Done():
		t.Fatal("slow daily candidate never reached byte cleanup")
	}
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	for {
		var count int
		if err := conn.QueryRowContext(ctx, `SELECT count(*) FROM foghorn.artifacts WHERE artifact_hash=$1`, fastHash).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count == 0 {
			break
		}
		select {
		case <-deadline.C:
			t.Fatal("fast daily candidate serialized behind slow destination")
		case <-time.After(20 * time.Millisecond):
		}
	}
	close(storage.release)
	select {
	case <-done:
	case <-ctx.Done():
		t.Fatal("daily purge did not settle after slow destination resumed")
	}
}
