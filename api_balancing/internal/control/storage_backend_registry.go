package control

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"strings"
)

// Storage-backend identity (RFC I2). A "backend" is where bytes physically live — (kind, bucket, endpoint, region,
// prefix). The backend_id is a deterministic FINGERPRINT of that physical identity (BackendFingerprint), so a given
// store always maps to the same id and a repoint would compute a NEW id. The backend_id columns on
// artifacts/assignments/cleanup rows are nullable fingerprint SNAPSHOTS computed at write time — there is no registry
// table and no descriptor lookup: under the immutable-one-backend-per-cell model cleanup compares a recorded id
// against THIS cell's current store (cell_storage_identity, enforced at boot by EnforceImmutableLocalBackend) and
// fails closed on a mismatch, rather than resolving an arbitrary backend's adapter. So cleanup can never delete from
// the wrong store, and a mismatch simply refuses (a forbidden repoint) instead of guessing.

// BackendFingerprint is the deterministic backend_id for a PHYSICAL storage-backend identity: (kind, bucket,
// endpoint, region, prefix). It DELIBERATELY excludes the logical cluster — the id names WHERE THE BYTES PHYSICALLY
// LIVE, so two clusters pointing at the same store share one id and cleanup can match a recorded backend to the
// currently-configured store regardless of which cluster wrote it. Any repoint (bucket/endpoint/region/prefix change)
// yields a DIFFERENT id.
//
// The descriptor is fingerprinted EXACTLY — matching Foghorn's byte-for-byte cell identity
// (EnforceImmutableLocalBackend) and the deploy-time agreement gate. Whitespace or case in bucket/endpoint/prefix
// names a DISTINCT physical keyspace, so it MUST fork the id: collapsing e.g. `prod` and ` prod` to one backend_id
// would let cleanup match a recorded id against the wrong store and skip its fail-closed mismatch. The ONLY
// normalization is region's omitted→us-east-1 default, an explicit equivalence Foghorn and the gate both apply. kind
// is an internal literal ("s3"), not part of the physical keyspace.
func BackendFingerprint(kind, bucket, endpoint, region, prefix string) string {
	parts := []string{
		strings.TrimSpace(kind),
		bucket,
		endpoint,
		effectiveFingerprintRegion(region),
		prefix,
	}
	sum := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return hex.EncodeToString(sum[:16]) // 128-bit, stable
}

// effectiveFingerprintRegion applies the ONLY descriptor normalization: a genuinely UNSET (empty) region defaults to
// us-east-1. A present region — INCLUDING a whitespace-only one — is used EXACTLY (never trimmed or lowercased), so the
// fingerprint matches Foghorn's byte-for-byte identity rather than masking a blank-but-present value as us-east-1. The
// deploy gate rejects a whitespace-only region up front, so this only ever sees "" or a real value in practice.
func effectiveFingerprintRegion(region string) string {
	if region == "" {
		return "us-east-1"
	}
	return region
}

// localBackendFingerprint returns the backend_id of the currently-configured local S3 store, computed IDENTICALLY to
// the Cleaner's LocalBackendID in cmd/foghorn/main.go (kind "s3" + the client's descriptor) — so a backend_id captured
// at a local promote equals the id cleanup later compares against. Empty only when no local S3 is wired; write-time
// callers that need to attribute a durable object REJECT that empty case (fail closed) rather than record an
// unattributed row the strict cleanup worker would refuse.
func localBackendFingerprint() string {
	if s3Client == nil {
		return ""
	}
	bucket, endpoint, region, prefix := s3Client.BackendDescriptor()
	return BackendFingerprint("s3", bucket, endpoint, region, prefix)
}

// EnforceImmutableLocalBackend records THIS cell's local S3 descriptor on first boot and, on every subsequent boot,
// REFUSES to start if the descriptor changed — the code-enforced "one immutable backend per cell" invariant. Backend
// repointing (bucket/endpoint/region/prefix change) is not supported: historical cleanup routes by an object's
// recorded backend and this cell wires only its current store, so a silent repoint would delete from — or fail closed
// on — the wrong backend. Credentials are NOT part of the descriptor, so key rotation passes. Requires a local S3
// client; the caller must fail closed BEFORE calling this if S3 is unconfigured but an identity was already committed.
// Returns a fatal-worthy error on a mismatch; the caller must abort startup.
//
// Identity is BYTE-EXACT: BackendFingerprint and this boot comparison both compare bucket/endpoint/region/prefix
// verbatim, the ONLY normalization being empty-region → us-east-1. A case or whitespace change addresses different
// objects, so it produces a different fingerprint AND fails the descriptor comparison — it is a repoint and is refused.
// The stored backend_id is retained only for cleanup-row attribution.
//
// The first-boot commit uses INSERT ... ON CONFLICT DO NOTHING and then UNCONDITIONALLY re-reads the committed row and
// compares: two HA replicas racing first boot with DIFFERENT descriptors serialize on the single-row PK, so the loser
// of the conflict reads the WINNER's descriptor and refuses to start rather than returning success against its own.
func EnforceImmutableLocalBackend(ctx context.Context, dbh *sql.DB) error {
	return EstablishOrEnforceLocalBackend(ctx, dbh, LocalBackendAuthority{})
}

// LocalBackendAuthority is the proven existing-backend descriptor used to gate first-boot establishment.
//   - Established=true means the cluster already has a backend (existing data): the Quartermaster cluster row carries a
//     descriptor, so a first boot MUST prove agreement before recording an identity, never establish a new one blindly.
//   - Complete=true means a FULL four-field descriptor (bucket/endpoint/region + prefix) was positively read from the
//     authority. Quartermaster owns the entire immutable tuple INCLUDING prefix, so it is the sole authority consulted
//     for validation — no serving component is queried at first boot, so establishment has no dependency on another service
//     being ready.
//
// An established cluster whose authority is not Complete (Quartermaster unreachable or its descriptor incomplete) fails
// first-boot closed — no identity is recorded.
type LocalBackendAuthority struct {
	Bucket, Endpoint, Region, Prefix string
	Established                      bool
	Complete                         bool
}

const defaultS3Region = "us-east-1"

// effectiveDescriptor applies the ONE legitimate normalization — the region default Foghorn/Chandler both use for an
// OMITTED region — so an env region of "us-east-1" never reads as a spurious mismatch against an empty stored/
// authoritative region. It deliberately does NOT trim or case-fold bucket/endpoint/prefix: whitespace/case there
// changes the addressed key space and is a real repoint that must still be refused (exact comparison).
func effectiveDescriptor(bucket, endpoint, region, prefix string) (string, string, string, string) {
	if region == "" {
		region = defaultS3Region
	}
	return bucket, endpoint, region, prefix
}

// EstablishOrEnforceLocalBackend gates first-boot establishment and enforces immutability thereafter. On a first boot (no
// committed identity) with auth.Established, it REQUIRES a complete, positively-read authority descriptor and exact
// agreement (effective values) before recording the identity — so a first boot can never establish a repointed or
// unproven descriptor while historical bytes live on the old store. Missing/incomplete authority when Established →
// refuse, no commit (fail closed).
//
// Established=false means no external authority was supplied to gate this call — the env-only EnforceImmutableLocalBackend
// entrypoint, which trusts THIS cell's env descriptor and establishes from it. This is NOT the production Foghorn
// first-boot path: there, buildFirstBootBackendAuthority reports Established=true for any S3-enabled cell — including one
// whose Quartermaster descriptor is absent or incomplete — so a production cell fails closed instead of establishing an
// unproven identity from env. After the identity exists, the exact-match immutability guard governs on every boot and no
// authority is consulted (no steady-state Quartermaster dependency).
func EstablishOrEnforceLocalBackend(ctx context.Context, dbh *sql.DB, auth LocalBackendAuthority) error {
	if dbh == nil || s3Client == nil {
		return nil
	}
	bucket, endpoint, region, prefix := effectiveDescriptor(s3Client.BackendDescriptor())

	committed, cErr := LocalBackendCommitted(ctx, dbh)
	if cErr != nil {
		return cErr
	}
	if !committed && auth.Established {
		if !auth.Complete {
			return fmt.Errorf("first-boot establishment for an existing cell requires a complete descriptor from Quartermaster (the full bucket/endpoint/region/prefix tuple); the authority was unavailable or its descriptor was incomplete (for example, a NULL prefix or an empty descriptor for an S3-enabled cell) — refusing to establish an identity")
		}
		ab, ae, ar, ap := effectiveDescriptor(auth.Bucket, auth.Endpoint, auth.Region, auth.Prefix)
		if bucket != ab || endpoint != ae || region != ar || prefix != ap {
			return fmt.Errorf("first-boot establishment: this Foghorn's env descriptor (bucket=%q endpoint=%q region=%q prefix=%q) disagrees with the Quartermaster descriptor (bucket=%q endpoint=%q region=%q prefix=%q) — refusing to establish a divergent identity",
				bucket, endpoint, region, prefix, ab, ae, ar, ap)
		}
	}
	if err := enforceImmutableLocalBackendDesc(ctx, dbh, bucket, endpoint, region, prefix); err != nil {
		return err
	}
	return nil
}

// enforceImmutableLocalBackendDesc is the descriptor-taking core (also the concurrency test seam).
func enforceImmutableLocalBackendDesc(ctx context.Context, dbh *sql.DB, bucket, endpoint, region, prefix string) error {
	currentID := BackendFingerprint("s3", bucket, endpoint, region, prefix)

	// Commit the current descriptor iff none is committed yet (idempotent, PK-serialized). A loser of a concurrent
	// first-boot race no-ops here and observes the winner's descriptor in the read-back below.
	if _, iErr := dbh.ExecContext(ctx, `
		INSERT INTO foghorn.cell_storage_identity (id, backend_id, bucket, endpoint, region, prefix)
		VALUES (true, $1, $2, $3, $4, $5)
		ON CONFLICT (id) DO NOTHING`, currentID, bucket, endpoint, region, prefix); iErr != nil {
		return iErr
	}

	// ALWAYS read back the committed row and compare the exact effective values.
	var sb, se, sr, sp string
	if err := dbh.QueryRowContext(ctx,
		`SELECT bucket, endpoint, region, prefix FROM foghorn.cell_storage_identity WHERE id = true`).
		Scan(&sb, &se, &sr, &sp); err != nil {
		return err // a missing row here is a real error (the INSERT above should have committed one)
	}
	if sb != bucket || se != endpoint || sr != region || sp != prefix {
		return fmt.Errorf("local S3 backend descriptor changed since this cell's first boot "+
			"(committed bucket=%q endpoint=%q region=%q prefix=%q; current bucket=%q endpoint=%q region=%q prefix=%q): "+
			"backend REPOINTING is not supported — historical cleanup would target the wrong store. "+
			"Restore the original descriptor (credentials may rotate freely) before restarting, or migrate this cell's "+
			"data off the old backend first",
			sb, se, sr, sp, bucket, endpoint, region, prefix)
	}
	return nil
}

// LocalBackendCommitted reports whether this cell has already committed an immutable S3 descriptor. The caller uses it
// to fail closed when S3 is unconfigured/uninitialized but an identity exists (removing STORAGE_S3_* after first boot,
// or an S3 client that failed to construct, must NOT silently start with durable storage disabled).
func LocalBackendCommitted(ctx context.Context, dbh *sql.DB) (bool, error) {
	if dbh == nil {
		return false, nil
	}
	var exists bool
	err := dbh.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM foghorn.cell_storage_identity WHERE id = true)`).Scan(&exists)
	return exists, err
}
