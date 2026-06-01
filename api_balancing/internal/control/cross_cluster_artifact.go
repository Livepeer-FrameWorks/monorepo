package control

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"sync"

	pb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto"
)

// adoptRemoteArtifactRow upserts the local foghorn.artifacts pointer for a
// cross-cluster artifact. This is the SINGLE adoption path shared by /play
// (resolveRemoteArtifact) and the STREAM_SOURCE direct-edge resolver, so both
// record the pointer identically — RelayResolve then reads this one row.
//
// storage_location is always 's3' (the artifact's home is the authoritative
// cluster's S3). sync_status is 'synced' only when the origin returned a
// durable S3 URL, else 'pending' (origin holds it hot on a node, not yet on
// S3) — recording 'synced' there would claim an S3 copy that doesn't exist.
// On re-adoption sync_status only ratchets up to 'synced', never back down;
// storage_location='s3' keeps the row out of the freeze reconciler's
// advancePending (which requires 'local').
//
// Adoption is load-bearing, not best-effort: RelayResolve deliberately does
// NOT federate-by-hash on a missing row (it has no tenant context to enforce
// the allowlist), so a row that fails to persist here 404s on the next byte
// GET for the whole resolve-cache TTL with no self-heal. The error is therefore
// returned and the front door fails closed (callers surface a retryable
// "unavailable" instead of handing back a relay URL that will 404).
func adoptRemoteArtifactRow(ctx context.Context, db *sql.DB, hash, contentType, tenantID, internalName, streamInternalName, format, originClusterID, storageClusterID string, synced bool) error {
	if db == nil {
		return fmt.Errorf("adoptRemoteArtifactRow: no db handle")
	}
	syncStatus := "pending"
	if synced {
		syncStatus = "synced"
	}
	storageCluster := sql.NullString{String: storageClusterID, Valid: storageClusterID != ""}
	if _, err := db.ExecContext(ctx, `
		INSERT INTO foghorn.artifacts (artifact_hash, artifact_type, tenant_id, internal_name, stream_internal_name, format, status, storage_location, sync_status, origin_cluster_id, storage_cluster_id)
		VALUES ($1, $2, $3, $4, $5, $6, 'active', 's3', $7, $8, $9)
		ON CONFLICT (artifact_hash) DO UPDATE
		SET storage_location = 's3',
		    sync_status = CASE WHEN EXCLUDED.sync_status = 'synced' THEN 'synced' ELSE foghorn.artifacts.sync_status END,
		    internal_name = CASE WHEN COALESCE(foghorn.artifacts.internal_name, '') = '' AND EXCLUDED.internal_name <> '' THEN EXCLUDED.internal_name ELSE foghorn.artifacts.internal_name END,
		    stream_internal_name = CASE WHEN COALESCE(foghorn.artifacts.stream_internal_name, '') = '' AND EXCLUDED.stream_internal_name <> '' THEN EXCLUDED.stream_internal_name ELSE foghorn.artifacts.stream_internal_name END,
		    format = CASE WHEN COALESCE(foghorn.artifacts.format, '') = '' AND EXCLUDED.format <> '' THEN EXCLUDED.format ELSE foghorn.artifacts.format END,
		    origin_cluster_id = CASE WHEN COALESCE(foghorn.artifacts.origin_cluster_id, '') = '' THEN EXCLUDED.origin_cluster_id ELSE foghorn.artifacts.origin_cluster_id END,
		    storage_cluster_id = CASE WHEN COALESCE(foghorn.artifacts.storage_cluster_id, '') = '' AND EXCLUDED.storage_cluster_id IS NOT NULL THEN EXCLUDED.storage_cluster_id ELSE foghorn.artifacts.storage_cluster_id END
	`, hash, contentType, tenantID, internalName, streamInternalName, format, syncStatus, originClusterID, storageCluster); err != nil {
		controlLogger().WithError(err).WithField("artifact_hash", hash).WithField("origin_cluster_id", originClusterID).Warn("adoptRemoteArtifactRow: upsert failed; failing resolution closed (no row → byte GET would 404)")
		return fmt.Errorf("adoptRemoteArtifactRow upsert: %w", err)
	}
	return nil
}

// ErrCrossClusterArtifactUnavailable is returned when the artifact's
// origin cluster is unknown, federation isn't wired, the peer doesn't
// know the artifact, or the peer has neither a durable S3 copy nor a
// servable hot peer-relay node for it yet. Callers fall back to their
// normal not-local behavior (`offline:not_uploaded` for STREAM_SOURCE
// vod+/processing+).
var ErrCrossClusterArtifactUnavailable = errors.New("cross-cluster artifact unavailable")

// CrossClusterArtifactURL carries the result of a successful
// federation lookup. Origin returns one of two shapes:
//   - URL: a presigned S3 GET minted by the origin (or
//     storage-redirected) cluster using its own S3 credentials, used
//     when the artifact is synced.
//   - PeerRelayURL + PeerRelayGrantID: a node-specific HTTPS URL into
//     the origin node's Helmsman + an opaque capability grant, used when
//     the canonical file is on disk but S3 sync is pending.
//
// Helmsman's relay block-cache fetches blocks from whichever upstream
// is set and presents the grant id as Authorization: Bearer only when a
// peer URL is present. No replication, no bulk copy.
type CrossClusterArtifactURL struct {
	URL              string
	SegmentURLs      map[string]string // multi-file artifacts (mainly DVR); usually empty for vod+
	OriginClusterID  string
	StorageClusterID string // empty unless single-hop storage redirect happened
	// Format is the container/extension origin reported (mp4, mkv,
	// m3u8, …). Threaded through so direct-edge STREAM_SOURCE
	// resolution can build the local Helmsman relay URL with the
	// right extension for cross-cluster artifacts that have no local
	// foghorn.artifacts row yet.
	Format string
	// StreamInternalName is the source stream's routing name (origin-reported).
	// Required to build the nested clip relay route (clip/<stream>/<hash>.<ext>)
	// for direct-edge cross-cluster clips, which have no local descriptor.
	StreamInternalName string
	// SizeBytes is the artifact's total size as reported by origin.
	// The block cache needs this up-front to plan range splits;
	// without it, serveViaBlockCache degrades to no-cache pass-
	// through (handler.go:38). Zero means "not reported".
	SizeBytes uint64
	// PeerRelayURL / PeerRelayDtshURL are populated when origin returned a
	// hot-but-unsynced peer-relay fallback instead of an S3 URL. When set,
	// URL is empty — the relay fetches from the peer node directly.
	PeerRelayURL     string
	PeerRelayDtshURL string
	// PeerRelayGrantID is the opaque capability the origin cluster's Foghorn
	// minted; presented as Authorization: Bearer and validated only by that
	// origin Foghorn at pull time. Covers both peer-relay URLs.
	PeerRelayGrantID string
}

// ResolveAndAdoptRemoteArtifact is the single cross-cluster resolve →
// authorize → adopt entrypoint for the STREAM_SOURCE direct-edge path (the
// media front door). It enforces the tenant peer allowlist on BOTH the origin
// and any storage-redirect cluster — exactly like /play's resolveRemoteArtifact
// — federates via PrepareArtifact, and adopts the local pointer row through the
// shared adoptRemoteArtifactRow so RelayResolve serves from that one row.
// Returns the federation result (format, stream name) for building the relay
// URL. originClusterID/tenantID/peers come from Commodore's by-internal-name
// resolve at STREAM_SOURCE time.
func ResolveAndAdoptRemoteArtifact(ctx context.Context, artifactHash, contentType, internalName, originClusterID, tenantID string, peers []*pb.TenantClusterPeer) (*CrossClusterArtifactURL, error) {
	if originClusterID == "" || originClusterID == GetLocalClusterID() || isServedCluster(originClusterID) {
		// Local or also-served by this Foghorn: bytes are servable locally, so
		// this is not a cross-cluster federate — fail with the sentinel and let
		// the caller fall through to the local path rather than dialing a cluster
		// we ourselves serve.
		return nil, ErrCrossClusterArtifactUnavailable
	}
	if !isAuthorizedPeerCluster(originClusterID, peers) {
		return nil, fmt.Errorf("origin cluster %s is not authorized for tenant", originClusterID)
	}
	// The redirect (if any) is authorized inside Resolve, before its dial.
	result, err := ResolveCrossClusterArtifactURL(ctx, artifactHash, contentType, tenantID, originClusterID, func(c string) bool {
		return isAuthorizedPeerCluster(c, peers)
	})
	if err != nil || result == nil {
		return nil, err
	}
	// Adoption is load-bearing: RelayResolve serves only the adopted row and
	// never re-federates by hash, so a failed upsert must fail this resolution
	// rather than return a relay URL whose byte GET will 404.
	if adoptErr := adoptRemoteArtifactRow(ctx, db, artifactHash, contentType, tenantID, internalName, result.StreamInternalName, result.Format, originClusterID, result.StorageClusterID, result.URL != ""); adoptErr != nil {
		return nil, fmt.Errorf("%w: %w", ErrCrossClusterArtifactUnavailable, adoptErr)
	}
	return result, nil
}

// PrepareArtifactCaller is the federation-client surface
// ResolveCrossClusterArtifactURL needs. *federation.FederationClient
// satisfies it.
type PrepareArtifactCaller interface {
	PrepareArtifact(ctx context.Context, peerClusterID, peerAddr string, req *pb.PrepareArtifactRequest) (*pb.PrepareArtifactResponse, error)
}

// CrossClusterPeerResolver is the peer-address lookup surface. The
// federation PeerManager satisfies it.
type CrossClusterPeerResolver interface {
	GetPeerAddr(clusterID string) string
}

// CrossClusterArtifactDeps captures the process-wide dependencies the
// resolver needs. Set once via SetCrossClusterArtifactDeps at startup.
type CrossClusterArtifactDeps struct {
	FedClient      PrepareArtifactCaller
	PeerResolver   CrossClusterPeerResolver
	LocalClusterID string
}

var (
	crossClusterDepsMu sync.RWMutex
	crossClusterDeps   *CrossClusterArtifactDeps
)

// SetCrossClusterArtifactDeps installs the process-wide deps. Called
// from main.go after FederationClient + PeerManager are constructed.
func SetCrossClusterArtifactDeps(d *CrossClusterArtifactDeps) {
	crossClusterDepsMu.Lock()
	crossClusterDeps = d
	crossClusterDepsMu.Unlock()
}

// ResolveCrossClusterArtifactURL is the package-level entry point.
// Returns ErrCrossClusterArtifactUnavailable when deps aren't wired,
// the artifact's origin cluster is local or unknown, the peer doesn't
// know the artifact, or any RPC step fails. Otherwise returns the
// presigned URL the local relay should read from.
// authorizeRedirect, when non-nil, gates a single-hop storage redirect: the
// redirect cluster must satisfy it BEFORE we dial it (a tenant_id-bearing
// PrepareArtifact). nil means "redirect already trusted" — used by the
// adopted-row relay path, whose origin/storage clusters were allowlist-checked
// at adopt time. The direct-edge front door passes the tenant peer allowlist.
func ResolveCrossClusterArtifactURL(ctx context.Context, artifactHash, contentType, tenantID, originClusterID string, authorizeRedirect func(string) bool) (*CrossClusterArtifactURL, error) {
	crossClusterDepsMu.RLock()
	d := crossClusterDeps
	crossClusterDepsMu.RUnlock()
	if d == nil {
		return nil, ErrCrossClusterArtifactUnavailable
	}
	return d.Resolve(ctx, artifactHash, contentType, tenantID, originClusterID, authorizeRedirect)
}

// Resolve performs the PrepareArtifact RPC chain with single-hop
// storage-redirect support. It is the pure federation lookup — no local row
// adoption. Both front doors (STREAM_SOURCE and /play) adopt separately via
// ResolveAndAdoptRemoteArtifact, which calls this and then writes the pointer
// row; the URL it returns is the read-through target, no bytes are copied.
func (d *CrossClusterArtifactDeps) Resolve(ctx context.Context, artifactHash, contentType, tenantID, originClusterID string, authorizeRedirect func(string) bool) (*CrossClusterArtifactURL, error) {
	if d.FedClient == nil || d.PeerResolver == nil {
		return nil, ErrCrossClusterArtifactUnavailable
	}
	if originClusterID == "" || originClusterID == d.LocalClusterID {
		return nil, ErrCrossClusterArtifactUnavailable
	}
	addr := d.PeerResolver.GetPeerAddr(originClusterID)
	if addr == "" {
		return nil, ErrCrossClusterArtifactUnavailable
	}
	resp, err := d.FedClient.PrepareArtifact(ctx, originClusterID, addr, &pb.PrepareArtifactRequest{
		ArtifactId:        artifactHash,
		RequestingCluster: d.LocalClusterID,
		ArtifactType:      contentType,
		TenantId:          tenantID,
	})
	if err != nil {
		return nil, fmt.Errorf("PrepareArtifact: %w", err)
	}
	storageClusterID := ""
	if redirect := strings.TrimSpace(resp.GetRedirectClusterId()); redirect != "" {
		if redirect == originClusterID || redirect == d.LocalClusterID {
			return nil, fmt.Errorf("storage redirect loop: origin %s -> %s", originClusterID, redirect)
		}
		// Authorize the redirect target BEFORE dialing it — the second
		// PrepareArtifact carries tenant_id, so a compromised origin must not
		// be able to point us at a cluster outside the tenant's allowlist.
		if authorizeRedirect != nil && !authorizeRedirect(redirect) {
			return nil, fmt.Errorf("storage redirect cluster %s is not authorized for tenant", redirect)
		}
		redirectAddr := d.PeerResolver.GetPeerAddr(redirect)
		if redirectAddr == "" {
			return nil, ErrCrossClusterArtifactUnavailable
		}
		resp, err = d.FedClient.PrepareArtifact(ctx, redirect, redirectAddr, &pb.PrepareArtifactRequest{
			ArtifactId:        artifactHash,
			RequestingCluster: d.LocalClusterID,
			ArtifactType:      contentType,
			TenantId:          tenantID,
		})
		if err != nil {
			return nil, fmt.Errorf("PrepareArtifact (storage cluster %s): %w", redirect, err)
		}
		if chained := strings.TrimSpace(resp.GetRedirectClusterId()); chained != "" {
			return nil, fmt.Errorf("chained storage redirect rejected: %s -> %s -> %s", originClusterID, redirect, chained)
		}
		storageClusterID = redirect
	}
	if resp.GetError() != "" {
		return nil, fmt.Errorf("origin error: %s", resp.GetError())
	}
	if !resp.GetReady() {
		return nil, ErrCrossClusterArtifactUnavailable
	}
	hasS3URL := resp.GetUrl() != ""
	hasPeerRelay := resp.GetPeerRelayUrl() != ""
	if !hasS3URL && !hasPeerRelay {
		return nil, ErrCrossClusterArtifactUnavailable
	}
	return &CrossClusterArtifactURL{
		URL:                resp.GetUrl(),
		SegmentURLs:        resp.GetSegmentUrls(),
		OriginClusterID:    originClusterID,
		StorageClusterID:   storageClusterID,
		Format:             resp.GetFormat(),
		StreamInternalName: resp.GetStreamInternalName(),
		SizeBytes:          resp.GetSizeBytes(),
		PeerRelayURL:       resp.GetPeerRelayUrl(),
		PeerRelayDtshURL:   resp.GetPeerRelayDtshUrl(),
		PeerRelayGrantID:   resp.GetPeerRelayGrantId(),
	}, nil
}
