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
	"strings"

	pb "frameworks/pkg/proto"
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

// BuildVodS3Key formats a VOD's deterministic S3 key. Mirrors
// storage.S3Client.BuildVodS3Key. Used as a fallback when
// vod_metadata.s3_key is absent for VODs whose freeze produced a
// deterministic key (e.g. federated freezes use hash+"."+format as the
// filename).
func BuildVodS3Key(tenantID, artifactHash, filename string) string {
	return fmt.Sprintf("vod/%s/%s/%s", tenantID, artifactHash, filename)
}

// ArtifactRef carries the authoritative metadata Cleaner needs to compute
// a deletion target. Populated from the gRPC delete handlers and the
// purge job's SELECT. Empty fields encode "we don't know" — Cleaner
// surfaces typed errors rather than guessing defaults.
type ArtifactRef struct {
	Hash             string
	Type             string // "clip" | "dvr" | "vod"
	TenantID         string
	StreamInternal   string
	Format           string
	StorageClusterID string
	OriginClusterID  string
	VODS3Key         string // VOD only; from foghorn.vod_metadata.s3_key
	S3URL            string // VOD fallback when vod_metadata.s3_key is absent; foghorn.artifacts.s3_url
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
type DeleteDelegate func(ctx context.Context, targetClusterID string, req *pb.DeleteStorageObjectsRequest) (*pb.DeleteStorageObjectsResponse, error)

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
}

// Sentinel errors callers can switch on. Keep stable: gRPC handlers map
// these into "cleanup_pending" response messages, the purge job uses
// them to decide whether to retry next cycle vs. drop the row.
var (
	// ErrMissingTarget — required field absent (e.g. VOD without s3_key,
	// clip without format). The caller should not retry until the row
	// gets the missing field.
	ErrMissingTarget = errors.New("artifact cleanup: missing deletion target field")
	// ErrUnsupportedType — artifact_type isn't one of clip/dvr/vod.
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
)

// Delete removes the artifact's S3 bytes from whichever cluster owns
// them. NotFound on the resolved key/prefix is treated as success
// (idempotent retries). Auth/ownership/shape failures from the remote
// path return ErrRemoteRejected with the reason in the wrapped error.
func (c *Cleaner) Delete(ctx context.Context, ref ArtifactRef) error {
	target, err := c.resolveTarget(ref)
	if err != nil {
		return err
	}

	if c.isRemote(ref) {
		return c.deleteRemote(ctx, ref, target)
	}
	return c.deleteLocal(ctx, target)
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
		if ref.TenantID == "" || ref.StreamInternal == "" || ref.Hash == "" || ref.Format == "" {
			return deletionTarget{}, fmt.Errorf("%w: clip needs tenant_id, stream_internal_name, artifact_hash, format", ErrMissingTarget)
		}
		return deletionTarget{S3Key: BuildClipS3Key(ref.TenantID, ref.StreamInternal, ref.Hash, ref.Format)}, nil
	case "dvr":
		if ref.TenantID == "" || ref.StreamInternal == "" || ref.Hash == "" {
			return deletionTarget{}, fmt.Errorf("%w: dvr needs tenant_id, stream_internal_name, artifact_hash", ErrMissingTarget)
		}
		return deletionTarget{S3Prefix: BuildDVRS3Key(ref.TenantID, ref.StreamInternal, ref.Hash)}, nil
	case "vod":
		key, err := c.resolveVODKey(ref)
		if err != nil {
			return deletionTarget{}, err
		}
		return deletionTarget{S3Key: key}, nil
	default:
		return deletionTarget{}, fmt.Errorf("%w: %q", ErrUnsupportedType, ref.Type)
	}
}

// resolveVODKey picks the best deletion target for a VOD row, in
// preference order:
//  1. vod_metadata.s3_key — authoritative when present (user uploads).
//  2. Parsed from foghorn.artifacts.s3_url — present after a freeze even
//     when vod_metadata.s3_key was never written (legacy / non-upload
//     paths).
//  3. Derived BuildVodS3Key(tenant, hash, hash+"."+format) — the
//     deterministic shape used by federated freezes; safe fallback when
//     format is known.
//
// Returns ErrMissingTarget only when none of these are derivable, so a
// VOD row with bytes on S3 is never silently dropped by the purge job.
func (c *Cleaner) resolveVODKey(ref ArtifactRef) (string, error) {
	if k := strings.TrimSpace(ref.VODS3Key); k != "" {
		return k, nil
	}
	if u := strings.TrimSpace(ref.S3URL); u != "" && c.S3 != nil {
		if k, err := c.S3.ParseS3URL(u); err == nil && k != "" {
			return k, nil
		}
	}
	if ref.TenantID != "" && ref.Hash != "" && strings.TrimSpace(ref.Format) != "" {
		return BuildVodS3Key(ref.TenantID, ref.Hash, ref.Hash+"."+ref.Format), nil
	}
	return "", fmt.Errorf("%w: vod needs vod_metadata.s3_key, s3_url, or format", ErrMissingTarget)
}

// isRemote returns true when the artifact's bytes live on a cluster
// other than this one. storage_cluster_id is preferred when set;
// origin_cluster_id is the fallback (matches the playback-side
// authoritative-cluster lookup at api_balancing/internal/control/playback.go:177).
// Empty / unset / matches local → false (local).
func (c *Cleaner) isRemote(ref ArtifactRef) bool {
	owner := strings.TrimSpace(ref.StorageClusterID)
	if owner == "" {
		owner = strings.TrimSpace(ref.OriginClusterID)
	}
	if owner == "" {
		return false
	}
	return owner != c.LocalCluster
}

func (c *Cleaner) deleteLocal(ctx context.Context, target deletionTarget) error {
	if c.S3 == nil {
		return ErrLocalS3Missing
	}
	if target.S3Key != "" {
		return c.S3.Delete(ctx, target.S3Key)
	}
	if _, err := c.S3.DeletePrefix(ctx, target.S3Prefix); err != nil {
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
	req := &pb.DeleteStorageObjectsRequest{
		TenantId:          ref.TenantID,
		RequestingCluster: c.LocalCluster,
		TargetClusterId:   owner,
		ArtifactHash:      ref.Hash,
		ArtifactType:      strings.ToLower(strings.TrimSpace(ref.Type)),
	}
	if target.S3Key != "" {
		req.Target = &pb.DeleteStorageObjectsRequest_S3Key{S3Key: target.S3Key}
	} else {
		req.Target = &pb.DeleteStorageObjectsRequest_S3Prefix{S3Prefix: target.S3Prefix}
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
