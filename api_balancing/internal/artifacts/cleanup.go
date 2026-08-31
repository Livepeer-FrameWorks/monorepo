// Package artifacts provides shared cleanup helpers for foghorn.artifacts
// rows. Cleaner.Delete is the one place that decides — given an
// authoritative artifact row — whether bytes live in this cluster's S3
// (use local S3Client) or in a peer cluster's S3 (use the federation
// delegate). Both gRPC user-initiated delete paths and the background
// PurgeDeletedJob use this so they pick the same target the same way.
//
// We never reconstruct delete targets from the storage cluster's own
// foghorn.artifacts row at delete time: those rows may be cache-healed
// stubs created by MintStorageURLs and lack vod_metadata.s3_key, format,
// or other delete-critical fields. The caller has the authoritative
// data; pass it on the wire.
package artifacts

import (
	"context"
	"errors"
	"fmt"
	foghornfederationpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/foghorn_federation"
	"strings"
)

// BuildClipS3Key formats a clip's deterministic S3 key. Pure string
// formatting; mirrors storage.S3Client.BuildClipS3Key so callers don't
// need a local S3 client to compute remote targets.
func BuildClipS3Key(tenantID, streamName, clipHash, format string) string {
	return fmt.Sprintf("clips/%s/%s/%s.%s", tenantID, streamName, clipHash, format)
}

// BuildDVRS3Key formats a DVR recording's deterministic S3 prefix.
func BuildDVRS3Key(tenantID, internalName, dvrHash string) string {
	return fmt.Sprintf("dvr/%s/%s/%s", tenantID, internalName, dvrHash)
}

// BuildThumbnailPrefix is the S3 prefix under which ALL of an asset's thumbnail objects live — every
// versioned object (thumbnails/{asset}/v/{version}/...), every per-attempt staging object
// (thumbnails/{asset}/.staging/...), and any legacy fixed-key object (thumbnails/{asset}/...). Deleting the
// prefix frees them together. For a purged artifact the asset_key IS the artifact_hash. Mirrors
// control.ThumbnailStagingKey/ThumbnailVersionKey; kept here to avoid an import cycle onto internal/control.
func BuildThumbnailPrefix(assetKey string) string {
	return fmt.Sprintf("thumbnails/%s/", assetKey)
}

// ArtifactRef carries the authoritative metadata Cleaner needs to compute
// a deletion target. Populated from the gRPC delete handlers and the
// purge job's SELECT. Empty fields encode "we don't know" — Cleaner
// surfaces typed errors rather than guessing defaults.
type ArtifactRef struct {
	Hash             string
	Type             string // physical byte kind: "clip" | "dvr" | "vod" (chapter canonicalizes to "vod")
	TenantID         string
	StreamInternal   string
	Format           string
	StorageClusterID string
	OriginClusterID  string
	// DurableBackendLocal is foghorn.artifacts.durable_backend_local: the STABLE write-time fact that these
	// bytes live on THIS cell's local S3 backend (e.g. an official-alias cluster whose advertised backing is
	// local). It OVERRIDES cluster-id comparison for delete routing — cluster id is attribution, not backend
	// ownership, so a locally-backed alias must be deleted locally, never delegated to a peer.
	DurableBackendLocal bool
	// BackendID is foghorn.artifacts.backend_id: the physical store the bytes were written to. A cell's backend is
	// immutable (enforced at startup). A local delete is licensed ONLY on an EXACT match to this cell's current store;
	// an empty (unattributed) or mismatched recorded id fails closed (ErrRecordedBackendMismatch) rather than sweeping a
	// guessed store. Write sites attribute the id and legacy rows are adopted at boot; cross-backend repointing is not
	// supported.
	BackendID string
	VODS3Key  string // VOD only; from foghorn.vod_metadata.s3_key
	S3URL     string // fallback when active_object_key/vod_metadata.s3_key absent; foghorn.artifacts.s3_url
	// ActiveObjectKey is foghorn.artifacts.active_object_key: the AUTHORITATIVE published object key (the
	// attempt-versioned key completion flipped the pointer to). It is the exact stored object, so deletion
	// prefers it over any reconstructed/canonical key. Empty for legacy rows (fall back to VODS3Key/S3URL/
	// reconstruction). The co-located index at ActiveObjectKey+".dtsh" is deleted alongside it.
	ActiveObjectKey string
	// ActiveDtshKey is foghorn.artifacts.active_dtsh_key: the authoritative version-addressed .dtsh index
	// object. Deleted alongside the media object. Empty for legacy rows (fall back to <ActiveObjectKey>.dtsh).
	ActiveDtshKey string
	// PendingObjectKey is foghorn.artifacts.sync_object_key: the exact object an in-flight freeze
	// authorized a PUT for. Retained on the deleted row so a PUT that lands AFTER the terminal transition
	// is still freed even if it differs from (or the row can no longer derive) the main deletion target.
	PendingObjectKey string
}

// S3Client is the local-bucket subset Cleaner needs to actually free
// bytes from this Foghorn pool's S3. Optional on the Cleaner: when an
// artifact's storage_cluster_id points at a peer cluster, the cleaner
// uses Delegate instead and never touches this client. A Foghorn pool
// with no local S3 (storage-via-federation) wires Delegate without S3.
type S3Client interface {
	Delete(ctx context.Context, key string) error
	DeletePrefix(ctx context.Context, prefix string) (int, error)
	ParseS3URL(s3URL string) (string, error)
}

// DeleteDelegate sends a federation DeleteStorageObjects request to the
// Foghorn pool that owns targetClusterID's S3. Wired from main.go.
// Cleaner falls back to a typed error when the delegate is nil.
type DeleteDelegate func(ctx context.Context, targetClusterID string, req *foghornfederationpb.DeleteStorageObjectsRequest) (*foghornfederationpb.DeleteStorageObjectsResponse, error)

// Cleaner resolves and executes artifact byte deletion. Construct once
// and reuse; methods are safe for concurrent use.
//
// Either S3 (for local-bucket deletes) or Delegate (for cross-cluster
// deletes) may be nil, but at least one must be present for any given
// artifact's deletion to succeed: if a row's bytes live locally and
// S3 is nil, Delete returns ErrLocalS3Missing; if remote and Delegate
// is nil, ErrDelegateMissing.
type Cleaner struct {
	LocalCluster string
	S3           S3Client
	Delegate     DeleteDelegate
	// LocalBackendID is the fingerprint (control.BackendFingerprint) of THIS cell's currently-configured local S3
	// store. A locally-backed row's recorded backend_id is swept locally ONLY on an EXACT match against it. Everything
	// else fails closed: an empty recorded id (unattributed), an empty LocalBackendID (unwired fingerprint), or a
	// mismatch (a store this cell does not control) — none license deleting from a guessed current store. Write-time
	// sites attribute the id and legacy rows are adopted at boot, so an empty recorded id is a genuine anomaly, retained
	// rather than swept. Cross-backend repointing is not supported.
	LocalBackendID string
}

// Sentinel errors callers can switch on. Keep stable: gRPC handlers map
// these into "cleanup_pending" response messages, the purge job uses
// them to decide whether to retry next cycle vs. drop the row.
var (
	// ErrMissingTarget — required field absent (e.g. VOD without s3_key,
	// clip without format). The caller should not retry until the row
	// gets the missing field.
	ErrMissingTarget = errors.New("artifact cleanup: missing deletion target field")
	// ErrUnsupportedType — byte kind isn't one of clip/dvr/vod; callers
	// canonicalize chapter to vod before crossing this storage boundary.
	ErrUnsupportedType = errors.New("artifact cleanup: unsupported artifact type")
	// ErrDelegateMissing — delete needs to go to a peer cluster but the
	// federation delegate isn't wired.
	ErrDelegateMissing = errors.New("artifact cleanup: storage_delete_delegate not wired")
	// ErrLocalS3Missing — bytes live in this cluster but no local S3
	// client is configured.
	ErrLocalS3Missing = errors.New("artifact cleanup: local s3 client not configured")
	// ErrRemoteRejected — peer cluster returned accepted=false. The
	// wrapped error carries the reason string.
	ErrRemoteRejected = errors.New("artifact cleanup: remote cluster rejected delete")
	// ErrRecordedBackendMismatch — a locally-backed obligation's recorded backend_id does not match this cell's current
	// local store (LocalBackendID). Under the immutable-backend invariant this cannot occur for a live cell (the
	// recorded id is always the current store or empty); it is the defensive fail-closed path that refuses to sweep the
	// wrong store should a stale/foreign id ever be recorded. Cross-backend repointing is not supported.
	ErrRecordedBackendMismatch = errors.New("artifact cleanup: recorded storage backend is not this cell's current store")
)

// localAdapterFor resolves the S3 adapter for a locally-backed delete recorded under backendID. Ownership is read from
// recorded evidence, never reconstructed (invariant I2): the delete is licensed ONLY when the recorded backend_id
// EXACTLY equals this cell's current store (LocalBackendID). FAIL CLOSED otherwise — an EMPTY recorded id (an
// unattributed row: cleanup must not guess the current store), an unwired LocalBackendID, or a mismatch (a store this
// cell does not control). Every write-time site attributes backend_id at creation (artifacts, thumbnail assignments,
// stream-cleanup obligations) and legacy rows are adopted ONCE at boot, so a still-empty id here is a genuine anomaly;
// retaining the row (a safe leak) beats orphaning its bytes.
func (c *Cleaner) localAdapterFor(backendID string) (S3Client, error) {
	id := strings.TrimSpace(backendID)
	if id == "" || c.LocalBackendID == "" || id != c.LocalBackendID {
		return nil, ErrRecordedBackendMismatch
	}
	if c.S3 == nil {
		return nil, ErrLocalS3Missing
	}
	return c.S3, nil
}

// Delete removes the artifact's S3 bytes from whichever cluster owns
// them. NotFound on the resolved key/prefix is treated as success
// (idempotent retries). Auth/ownership/shape failures from the remote
// path return ErrRemoteRejected with the reason in the wrapped error.
func (c *Cleaner) Delete(ctx context.Context, ref ArtifactRef) error {
	pending := strings.TrimSpace(ref.PendingObjectKey)
	target, err := c.resolveTarget(ref)
	if err != nil {
		// No derivable main target. If a freeze bound a pending object, free THAT (an authorized-but-late
		// PUT can otherwise leak); only when there is nothing to clean do we surface the missing-target error.
		if pending == "" {
			return err
		}
		return c.deleteKey(ctx, ref, pending)
	}

	if c.isRemote(ref) {
		if delErr := c.deleteRemote(ctx, ref, target); delErr != nil {
			return delErr
		}
	} else {
		s3, rErr := c.localAdapterFor(ref.BackendID)
		if rErr != nil {
			return rErr
		}
		if delErr := c.deleteLocalVia(ctx, s3, target); delErr != nil {
			return delErr
		}
	}
	// Free the .dtsh index for a single-object clip/vod (never for a DVR prefix). Prefer the authoritative
	// version-addressed active_dtsh_key; legacy rows fall back to the co-located <media>.dtsh. NotFound is
	// idempotent-success, so deleting is safe even when no index was written.
	if target.S3Key != "" {
		dtshKey := strings.TrimSpace(ref.ActiveDtshKey)
		if dtshKey == "" {
			dtshKey = target.S3Key + ".dtsh"
		}
		if delErr := c.deleteKey(ctx, ref, dtshKey); delErr != nil {
			return delErr
		}
	}
	// Also free the pending freeze descriptor object when it differs from the main target.
	if pending != "" && pending != target.S3Key {
		return c.deleteKey(ctx, ref, pending)
	}
	return nil
}

// DeleteThumbnailsOnCluster frees an asset's thumbnail objects (all versions, staging, and legacy) by removing
// the thumbnails/{hash}/ prefix from the cluster that actually STORES them — the thumbnail publication's
// official-durable destination cluster, which is INDEPENDENT of where the parent artifact's own bytes live.
// Routing by the parent artifact would delegate to the wrong cluster for a BYOC-origin artifact whose thumbnails
// were stored on platform-official storage, leaking the locally-billed objects.
//
// backendLocal is the recorded write-time evidence (I2): true means the bytes are on THIS cell's local S3 even
// though the destination cluster id differs (a locally-backed official alias), so we delete LOCALLY and do NOT
// misroute to (disabled) federation. Otherwise an empty destination or one equal to this cluster deletes locally;
// a differing cluster delegates to its owner. DeletePrefix on an empty prefix is an idempotent no-op.
//
// backendID is the recorded backend_id snapshot: a local sweep is licensed ONLY when it exactly equals this cell's
// current store (LocalBackendID). An empty or mismatched id fails closed (ErrRecordedBackendMismatch) rather than
// sweeping the wrong store.
func (c *Cleaner) DeleteThumbnailsOnCluster(ctx context.Context, tenantID, artifactHash, destinationCluster string, backendLocal bool, backendID string) error {
	hash := strings.TrimSpace(artifactHash)
	if hash == "" {
		return nil
	}
	target := deletionTarget{S3Prefix: BuildThumbnailPrefix(hash)}
	owner := strings.TrimSpace(destinationCluster)
	if backendLocal || owner == "" || owner == c.LocalCluster {
		s3, err := c.localAdapterFor(backendID)
		if err != nil {
			return err
		}
		return c.deleteLocalVia(ctx, s3, target)
	}
	return c.deleteRemote(ctx, ArtifactRef{Hash: hash, Type: "thumbnail", TenantID: tenantID, StorageClusterID: owner}, target)
}

// deleteKey frees a single explicit S3 key (local pool or, for a peer-owned artifact, via the delegate). A local
// delete requires the recorded backend_id to exactly match this cell's current store (immutable-backend model): an
// exact match frees the co-located index / pending object from that store; an empty or mismatched id fails closed.
func (c *Cleaner) deleteKey(ctx context.Context, ref ArtifactRef, key string) error {
	t := deletionTarget{S3Key: key}
	if c.isRemote(ref) {
		return c.deleteRemote(ctx, ref, t)
	}
	s3, rErr := c.localAdapterFor(ref.BackendID)
	if rErr != nil {
		return rErr
	}
	return c.deleteLocalVia(ctx, s3, t)
}

// deletionTarget is what we send on the wire (and use locally). Exactly
// one of S3Key or S3Prefix is set.
type deletionTarget struct {
	S3Key    string
	S3Prefix string
}

func (c *Cleaner) resolveTarget(ref ArtifactRef) (deletionTarget, error) {
	switch strings.ToLower(strings.TrimSpace(ref.Type)) {
	case "clip":
		// Prefer the AUTHORITATIVE published key; a frozen clip lives at the attempt-versioned
		// active_object_key, NOT the reconstructed canonical key. Reconstruction is only the legacy fallback
		// for rows synced before active_object_key existed.
		if k := strings.TrimSpace(ref.ActiveObjectKey); k != "" {
			return deletionTarget{S3Key: k}, nil
		}
		if ref.TenantID == "" || ref.StreamInternal == "" || ref.Hash == "" || ref.Format == "" {
			return deletionTarget{}, fmt.Errorf("%w: clip needs active_object_key or tenant_id, stream_internal_name, artifact_hash, format", ErrMissingTarget)
		}
		return deletionTarget{S3Key: BuildClipS3Key(ref.TenantID, ref.StreamInternal, ref.Hash, ref.Format)}, nil
	case "dvr":
		if ref.TenantID == "" || ref.StreamInternal == "" || ref.Hash == "" {
			return deletionTarget{}, fmt.Errorf("%w: dvr needs tenant_id, stream_internal_name, artifact_hash", ErrMissingTarget)
		}
		return deletionTarget{S3Prefix: BuildDVRS3Key(ref.TenantID, ref.StreamInternal, ref.Hash)}, nil
	case "vod", "chapter":
		key, err := c.resolveVODKey(ref)
		if err != nil {
			return deletionTarget{}, err
		}
		return deletionTarget{S3Key: key}, nil
	default:
		return deletionTarget{}, fmt.Errorf("%w: %q", ErrUnsupportedType, ref.Type)
	}
}

// resolveVODKey picks the deletion target for a VOD row from a RECORDED object key only, in preference
// order:
//  1. vod_metadata.s3_key — the authoritative recorded key (uploads and freezes both write it).
//  2. Parsed from foghorn.artifacts.s3_url — the recorded canonical URL after a completed freeze.
//
// It does NOT reconstruct a key from tenant/hash/format: deletion consumes the exact recorded object, the
// same source of truth the write path persists. When no recorded key exists here, Delete falls back to
// the freeze descriptor (ref.PendingObjectKey) and otherwise fails closed — a VOD is never deleted at a
// guessed key.
func (c *Cleaner) resolveVODKey(ref ArtifactRef) (string, error) {
	if k := strings.TrimSpace(ref.ActiveObjectKey); k != "" {
		return k, nil
	}
	if k := strings.TrimSpace(ref.VODS3Key); k != "" {
		return k, nil
	}
	if u := strings.TrimSpace(ref.S3URL); u != "" && c.S3 != nil {
		if k, err := c.S3.ParseS3URL(u); err == nil && k != "" {
			return k, nil
		}
	}
	return "", fmt.Errorf("%w: vod needs a recorded vod_metadata.s3_key or s3_url", ErrMissingTarget)
}

// isRemote returns true when the artifact's bytes live on a cluster
// other than this one. storage_cluster_id is preferred when set;
// origin_cluster_id is the fallback (matches the playback-side
// authoritative-cluster lookup at api_balancing/internal/control/playback.go:177).
// Empty / unset / matches local → false (local).
func (c *Cleaner) isRemote(ref ArtifactRef) bool {
	// Persisted backend ownership WINS over cluster-id comparison: a locally-backed official alias has a
	// storage_cluster_id that differs from LocalCluster but its bytes are on THIS cell's S3, so it must be
	// deleted locally, not delegated.
	if ref.DurableBackendLocal {
		return false
	}
	owner := strings.TrimSpace(ref.StorageClusterID)
	if owner == "" {
		owner = strings.TrimSpace(ref.OriginClusterID)
	}
	if owner == "" {
		return false
	}
	return owner != c.LocalCluster
}

// deleteLocalVia frees a target through the resolved local S3 adapter (this cell's current store). A nil adapter is
// ErrLocalS3Missing.
func (c *Cleaner) deleteLocalVia(ctx context.Context, s3 S3Client, target deletionTarget) error {
	if s3 == nil {
		return ErrLocalS3Missing
	}
	if target.S3Key != "" {
		return s3.Delete(ctx, target.S3Key)
	}
	if _, err := s3.DeletePrefix(ctx, target.S3Prefix); err != nil {
		return err
	}
	return nil
}

func (c *Cleaner) deleteRemote(ctx context.Context, ref ArtifactRef, target deletionTarget) error {
	if c.Delegate == nil {
		return ErrDelegateMissing
	}
	owner := strings.TrimSpace(ref.StorageClusterID)
	if owner == "" {
		owner = strings.TrimSpace(ref.OriginClusterID)
	}
	req := &foghornfederationpb.DeleteStorageObjectsRequest{
		TenantId:          ref.TenantID,
		RequestingCluster: c.LocalCluster,
		TargetClusterId:   owner,
		ArtifactHash:      ref.Hash,
		ArtifactType:      strings.ToLower(strings.TrimSpace(ref.Type)),
	}
	if target.S3Key != "" {
		req.Target = &foghornfederationpb.DeleteStorageObjectsRequest_S3Key{S3Key: target.S3Key}
	} else {
		req.Target = &foghornfederationpb.DeleteStorageObjectsRequest_S3Prefix{S3Prefix: target.S3Prefix}
	}

	resp, err := c.Delegate(ctx, owner, req)
	if err != nil {
		return fmt.Errorf("artifact cleanup: delegate call failed: %w", err)
	}
	if resp == nil {
		return fmt.Errorf("%w: nil response", ErrRemoteRejected)
	}
	if !resp.GetAccepted() {
		return fmt.Errorf("%w: %s", ErrRemoteRejected, resp.GetReason())
	}
	return nil
}
