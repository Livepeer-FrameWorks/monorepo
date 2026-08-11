//go:build schema_verify

package control

import (
	"context"
	"database/sql"
	"sync"
	"testing"
)

// The one-time adoption stamps the cell fingerprint onto UNAMBIGUOUSLY-LOCAL rows that predate backend_id, by recorded
// evidence (not durable_backend_local, which postdeploy sets only later). It must adopt: synced rows whose effective
// cluster is empty/empty-string/localClusterID; in-flight VOD multiparts; and in-flight FREEZE attempts (local by
// write-path invariant even under an alias cluster id). It must leave remote-origin and already-attributed rows
// untouched, and — via the durable marker committed atomically with the backfill — never run a second time (a NULL row
// created after the cutoff stays NULL, a regression, not silently repaired).
func TestAdoptLegacyLocalBackends_RealPG(t *testing.T) {
	conn := startRealPG(t)
	ctx := context.Background()
	const tid = "11111111-1111-1111-1111-111111111111"

	nul := func(s string) any {
		if s == "" {
			return nil
		}
		return s
	}
	seed := func(hash, artifactType, status, syncStatus, storageCluster, originCluster, backendID string) {
		if _, err := conn.Exec(`
			INSERT INTO foghorn.artifacts
			  (artifact_hash, artifact_type, tenant_id, status, sync_status, storage_location,
			   storage_cluster_id, origin_cluster_id, backend_id)
			VALUES ($1, $2, $3::uuid, $4, $5, 'local', $6, $7, $8)`,
			hash, artifactType, tid, status, syncStatus, nul(storageCluster), nul(originCluster), nul(backendID)); err != nil {
			t.Fatalf("seed %s: %v", hash, err)
		}
	}
	// Recorded-evidence locality has THREE representations (all must adopt): both cluster columns NULL, empty strings,
	// and the explicit local cluster id — matching ReconcileBillingAttribution. A remote origin is retained.
	prevCluster := localClusterID
	t.Cleanup(func() { localClusterID = prevCluster })
	localClusterID = "cell-eu"

	// seedStr inserts storage/origin as LITERAL strings (no NULL coercion) so the empty-string representation is tested.
	seedStr := func(hash, storageCluster, originCluster string) {
		if _, err := conn.Exec(`
			INSERT INTO foghorn.artifacts
			  (artifact_hash, artifact_type, tenant_id, status, sync_status, storage_location,
			   storage_cluster_id, origin_cluster_id, backend_id)
			VALUES ($1, 'clip', $2::uuid, 'ready', 'synced', 'local', $3, $4, NULL)`,
			hash, tid, storageCluster, originCluster); err != nil {
			t.Fatalf("seedStr %s: %v", hash, err)
		}
	}
	// seedFreezing inserts an in-flight freeze attempt (storage_location='freezing', sync_status='in_progress').
	seedFreezing := func(hash, storageCluster string) {
		if _, err := conn.Exec(`
			INSERT INTO foghorn.artifacts
			  (artifact_hash, artifact_type, tenant_id, status, sync_status, storage_location, storage_cluster_id, backend_id)
			VALUES ($1, 'clip', $2::uuid, 'ready', 'in_progress', 'freezing', $3, NULL)`,
			hash, tid, nul(storageCluster)); err != nil {
			t.Fatalf("seedFreezing %s: %v", hash, err)
		}
	}
	seed("synced-local", "clip", "ready", "synced", "", "", "")                    // both clusters NULL -> adopted
	seed("remote-origin", "clip", "ready", "synced", "", "media-us-1", "")         // remote origin -> retained NULL
	seed("inflight-vod", "vod", "uploading", "in_progress", "", "", "")            // in-flight multipart -> adopted
	seed("already-attributed", "clip", "ready", "synced", "", "", "other-backend") // has an id -> untouched
	seedFreezing("freeze-local", "")                                               // in-flight local freeze (no cluster) -> adopted
	seedFreezing("freeze-alias", "official-us")                                    // in-flight local freeze whose OFFICIAL/ALIAS cluster != localClusterID but same local S3 backing -> adopted (the attempt itself is local evidence)
	seedStr("empty-string-local", "", "")                                          // empty-string clusters -> adopted
	seedStr("local-cluster", "cell-eu", "")                                        // explicit local cluster id -> adopted

	if err := adoptLegacyLocalBackends(ctx, conn, "cell-eu-backend"); err != nil {
		t.Fatalf("adoptLegacyLocalBackends: %v", err)
	}

	backendOf := func(hash string) string {
		var b sql.NullString
		if err := conn.QueryRow(`SELECT backend_id FROM foghorn.artifacts WHERE artifact_hash = $1`, hash).Scan(&b); err != nil {
			t.Fatalf("read %s: %v", hash, err)
		}
		return b.String
	}
	if got := backendOf("synced-local"); got != "cell-eu-backend" {
		t.Errorf("synced-local backend_id = %q, want cell-eu-backend", got)
	}
	if got := backendOf("inflight-vod"); got != "cell-eu-backend" {
		t.Errorf("inflight-vod backend_id = %q, want cell-eu-backend", got)
	}
	if got := backendOf("remote-origin"); got != "" {
		t.Errorf("remote-origin must NOT be adopted, backend_id = %q", got)
	}
	if got := backendOf("already-attributed"); got != "other-backend" {
		t.Errorf("already-attributed must be untouched, backend_id = %q", got)
	}
	if got := backendOf("empty-string-local"); got != "cell-eu-backend" {
		t.Errorf("empty-string-local backend_id = %q, want cell-eu-backend", got)
	}
	if got := backendOf("local-cluster"); got != "cell-eu-backend" {
		t.Errorf("local-cluster backend_id = %q, want cell-eu-backend", got)
	}
	if got := backendOf("freeze-local"); got != "cell-eu-backend" {
		t.Errorf("in-flight local freeze must be adopted, backend_id = %q", got)
	}
	if got := backendOf("freeze-alias"); got != "cell-eu-backend" {
		t.Errorf("in-flight local freeze with an alias cluster id must be adopted (the attempt is local evidence), backend_id = %q", got)
	}

	// The marker is now committed: a second run is a no-op even for a fresh NULL local row (a post-cutoff regression
	// must fail closed downstream, not be re-attributed).
	seed("post-cutoff", "clip", "ready", "synced", "", "", "")
	if err := adoptLegacyLocalBackends(ctx, conn, "cell-eu-backend"); err != nil {
		t.Fatalf("second adoptLegacyLocalBackends: %v", err)
	}
	if got := backendOf("post-cutoff"); got != "" {
		t.Errorf("post-cutoff row must NOT be adopted after the one-time cutoff, backend_id = %q", got)
	}
}

// Two replicas boot CONCURRENTLY against a fresh cell with DIFFERENT descriptors. The single-row PK serializes the
// inserts; exactly one descriptor is committed, and the OTHER replica — the loser of ON CONFLICT DO NOTHING — must
// read back the winner's committed descriptor and REFUSE to start rather than returning success against its own. Pins
// that the insert-then-reread closes the first-boot race under true concurrency, not just a serialized loser path.
func TestEnforceImmutableLocalBackend_ConcurrentFirstBootRace_RealPG(t *testing.T) {
	conn := startRealPG(t)
	ctx := context.Background()

	var wg sync.WaitGroup
	errs := make([]error, 2)
	descs := [2][4]string{
		{"bucket-A", "https://a.s3", "us-east-1", "artifacts"},
		{"bucket-B", "https://b.s3", "eu-west-1", "media"},
	}
	wg.Add(2)
	for i := 0; i < 2; i++ {
		go func(i int) {
			defer wg.Done()
			d := descs[i]
			errs[i] = enforceImmutableLocalBackendDesc(ctx, conn, d[0], d[1], d[2], d[3])
		}(i)
	}
	wg.Wait()

	// Exactly one committed row, and exactly one of the two calls refused.
	var n int
	if err := conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM foghorn.cell_storage_identity`).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 1 {
		t.Fatalf("exactly one descriptor must commit under the race, got %d rows", n)
	}
	refused := 0
	for _, e := range errs {
		if e != nil {
			refused++
		}
	}
	if refused != 1 {
		t.Fatalf("exactly one replica must refuse (the race loser), got %d refusals (errs=%v)", refused, errs)
	}

	// The committed descriptor must be one of the two, and the winner's own call must have SUCCEEDED (not refused).
	var wb string
	if err := conn.QueryRowContext(ctx, `SELECT bucket FROM foghorn.cell_storage_identity WHERE id = true`).Scan(&wb); err != nil {
		t.Fatalf("read winner: %v", err)
	}
	winner := -1
	for i := range descs {
		if descs[i][0] == wb {
			winner = i
		}
	}
	if winner < 0 {
		t.Fatalf("committed bucket %q is neither candidate", wb)
	}
	if errs[winner] != nil {
		t.Fatalf("the replica whose descriptor committed must NOT have refused, got %v", errs[winner])
	}
}

// TestEnforceImmutableLocalBackend_RealPG proves the code-enforced "one immutable backend per cell" invariant against
// the real foghorn.sql schema: first boot commits the descriptor; an unchanged descriptor (even with the credentials
// that are not part of identity) passes; a changed bucket/endpoint/region/prefix is refused so a repoint can never
// silently misroute historical cleanup.
func TestEnforceImmutableLocalBackend_RealPG(t *testing.T) {
	conn := startRealPG(t)
	ctx := context.Background()

	prev := s3Client
	t.Cleanup(func() { s3Client = prev })

	// First boot with descriptor A → commits, no error.
	s3Client = &mockS3Client{descBucket: "bucket-A", descEndpoint: "https://a.s3", descRegion: "us-east-1", descPrefix: "artifacts"}
	if err := EnforceImmutableLocalBackend(ctx, conn); err != nil {
		t.Fatalf("first boot must commit the descriptor, got: %v", err)
	}
	// The committed row exists.
	var n int
	if err := conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM foghorn.cell_storage_identity WHERE id = true`).Scan(&n); err != nil {
		t.Fatalf("count identity: %v", err)
	}
	if n != 1 {
		t.Fatalf("expected exactly one committed identity row, got %d", n)
	}

	// Same descriptor again → passes (idempotent, no repoint).
	if err := EnforceImmutableLocalBackend(ctx, conn); err != nil {
		t.Fatalf("unchanged descriptor must pass, got: %v", err)
	}

	// A DIFFERENT prefix (same bucket) is a repoint → refuse start.
	s3Client = &mockS3Client{descBucket: "bucket-A", descEndpoint: "https://a.s3", descRegion: "us-east-1", descPrefix: "artifacts-v2"}
	if err := EnforceImmutableLocalBackend(ctx, conn); err == nil {
		t.Fatal("a changed descriptor (prefix) must be refused")
	}

	// A different bucket is likewise refused.
	s3Client = &mockS3Client{descBucket: "bucket-B", descEndpoint: "https://a.s3", descRegion: "us-east-1", descPrefix: "artifacts"}
	if err := EnforceImmutableLocalBackend(ctx, conn); err == nil {
		t.Fatal("a changed descriptor (bucket) must be refused")
	}

	// The committed row is UNCHANGED by the refused checks (still descriptor A).
	var bucket, prefix string
	if err := conn.QueryRowContext(ctx, `SELECT bucket, prefix FROM foghorn.cell_storage_identity WHERE id = true`).Scan(&bucket, &prefix); err != nil {
		t.Fatalf("read committed identity: %v", err)
	}
	if bucket != "bucket-A" || prefix != "artifacts" {
		t.Fatalf("committed identity mutated: bucket=%q prefix=%q, want bucket-A/artifacts", bucket, prefix)
	}
}

// First-boot ADOPTION of an established cluster: the env descriptor must EXACTLY match a complete, proven authority
// (bucket/endpoint/region + prefix) before an identity is committed; a disagreement, or an authority that is
// established-but-not-complete (Quartermaster unavailable/incomplete), refuses with NO commit. The low-level
// unestablished path still commits from env (used by EnforceImmutableLocalBackend); the production fail-closed for an
// empty Quartermaster descriptor lives in buildFirstBootBackendAuthority. Region defaulting is applied on both sides.
func TestAdoptOrEnforceLocalBackend_FirstBootAdoption_RealPG(t *testing.T) {
	prev := s3Client
	t.Cleanup(func() { s3Client = prev })
	// This Foghorn's env points at bucket-B / prefix artifacts, with an OMITTED region (defaults to us-east-1).
	s3Client = &mockS3Client{descBucket: "bucket-B", descEndpoint: "https://b.s3", descRegion: "", descPrefix: "artifacts"}

	uncommitted := func(t *testing.T, conn interface {
		QueryRowContext(ctx context.Context, q string, a ...any) *sql.Row
	}) {
		var n int
		if err := conn.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM foghorn.cell_storage_identity`).Scan(&n); err != nil {
			t.Fatalf("count: %v", err)
		}
		if n != 0 {
			t.Fatalf("no identity may be committed, got %d rows", n)
		}
	}

	t.Run("established+complete but disagreeing env is refused (no commit)", func(t *testing.T) {
		conn := startRealPG(t)
		auth := LocalBackendAuthority{Bucket: "bucket-A", Endpoint: "https://a.s3", Region: "us-east-1", Prefix: "artifacts", Established: true, Complete: true}
		if err := AdoptOrEnforceLocalBackend(context.Background(), conn, auth); err == nil {
			t.Fatal("a first boot whose env disagrees with the proven backend must be refused")
		}
		uncommitted(t, conn)
	})

	t.Run("established but NOT complete (authority unavailable) is refused (no commit)", func(t *testing.T) {
		conn := startRealPG(t)
		if err := AdoptOrEnforceLocalBackend(context.Background(), conn, LocalBackendAuthority{Established: true, Complete: false}); err == nil {
			t.Fatal("an established cluster with incomplete authority must fail first boot closed")
		}
		uncommitted(t, conn)
	})

	t.Run("established+complete and matching env commits (region defaulted both sides)", func(t *testing.T) {
		conn := startRealPG(t)
		// Authority region is explicit us-east-1; env region is omitted → both normalize to us-east-1.
		auth := LocalBackendAuthority{Bucket: "bucket-B", Endpoint: "https://b.s3", Region: "us-east-1", Prefix: "artifacts", Established: true, Complete: true}
		if err := AdoptOrEnforceLocalBackend(context.Background(), conn, auth); err != nil {
			t.Fatalf("a matching env must adopt/commit, got: %v", err)
		}
	})

	t.Run("prefix mismatch is refused", func(t *testing.T) {
		conn := startRealPG(t)
		auth := LocalBackendAuthority{Bucket: "bucket-B", Endpoint: "https://b.s3", Region: "us-east-1", Prefix: "artifacts-v2", Established: true, Complete: true}
		if err := AdoptOrEnforceLocalBackend(context.Background(), conn, auth); err == nil {
			t.Fatal("a prefix disagreement must be refused")
		}
		uncommitted(t, conn)
	})

	// The low-level AdoptOrEnforceLocalBackend with an UNestablished authority still commits from env — this is the
	// mechanism EnforceImmutableLocalBackend uses. The PRODUCTION fail-closed (an S3-enabled Foghorn whose Quartermaster
	// descriptor is empty must not establish from env) lives in buildFirstBootBackendAuthority, which returns an
	// established-but-incomplete authority for an empty QM row so the guard above fires.
	t.Run("unestablished authority commits from env (low-level path)", func(t *testing.T) {
		conn := startRealPG(t)
		if err := AdoptOrEnforceLocalBackend(context.Background(), conn, LocalBackendAuthority{Established: false}); err != nil {
			t.Fatalf("the low-level unestablished path must commit from env, got: %v", err)
		}
	})
}

// A descriptor that differs from the committed one ONLY by endpoint case (or prefix whitespace) addresses DIFFERENT
// objects through the S3 client, so it MUST be refused at boot. The fingerprint is byte-exact (region-default is the
// only normalization), so such a change is a DISTINCT backend identity — the guard catches it. This test proves both
// halves: (1) the byte-exact fingerprint genuinely differs, and (2) the committed-descriptor guard refuses the change.
func TestEnforceImmutableLocalBackend_ExactMatchNotNormalized_RealPG(t *testing.T) {
	conn := startRealPG(t)
	ctx := context.Background()
	prev := s3Client
	t.Cleanup(func() { s3Client = prev })

	// Commit a lowercase endpoint + un-padded prefix.
	s3Client = &mockS3Client{descBucket: "bucket-A", descEndpoint: "https://store.example", descRegion: "us-east-1", descPrefix: "artifacts"}
	if err := EnforceImmutableLocalBackend(ctx, conn); err != nil {
		t.Fatalf("first boot: %v", err)
	}

	// (1) Byte-exact identity: an endpoint-case or prefix-whitespace change must produce a DIFFERENT fingerprint —
	// normalization must not collapse a material repoint into the committed identity.
	base := BackendFingerprint("s3", "bucket-A", "https://store.example", "us-east-1", "artifacts")
	if BackendFingerprint("s3", "bucket-A", "https://Store.Example", "us-east-1", "artifacts") == base {
		t.Fatal("endpoint-case change must change the fingerprint (byte-exact identity), but it collapsed to the committed id")
	}
	if BackendFingerprint("s3", "bucket-A", "https://store.example", "us-east-1", "artifacts ") == base {
		t.Fatal("prefix-whitespace change must change the fingerprint (byte-exact identity), but it collapsed to the committed id")
	}

	// (2) The committed-descriptor guard refuses BOTH changes at boot.
	s3Client = &mockS3Client{descBucket: "bucket-A", descEndpoint: "https://Store.Example", descRegion: "us-east-1", descPrefix: "artifacts"}
	if err := EnforceImmutableLocalBackend(ctx, conn); err == nil {
		t.Fatal("an endpoint-case change (distinct byte-exact identity, different addressing) must be refused")
	}
	s3Client = &mockS3Client{descBucket: "bucket-A", descEndpoint: "https://store.example", descRegion: "us-east-1", descPrefix: "artifacts "}
	if err := EnforceImmutableLocalBackend(ctx, conn); err == nil {
		t.Fatal("a prefix-whitespace change must be refused")
	}
}
