package control

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	pb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto"
)

// ErrCrossClusterArtifactUnavailable is returned when the artifact's
// origin cluster is unknown, federation isn't wired, the peer doesn't
// know the artifact, or the peer hasn't finished preparing it.
// Callers fall back to their normal not-local behavior
// (`offline:not_uploaded` for STREAM_SOURCE vod+/processing+).
var ErrCrossClusterArtifactUnavailable = errors.New("cross-cluster artifact unavailable")

// CrossClusterArtifactURL carries the result of a successful
// federation lookup. Origin returns one of two shapes:
//   - URL: a presigned S3 GET minted by the origin (or
//     storage-redirected) cluster using its own S3 credentials, used
//     when the artifact is synced.
//   - PeerRelayURL + PeerRelayToken: a node-specific HTTPS URL into
//     the origin node's Helmsman + a short-lived artifact_relay JWT,
//     used when the canonical file is on disk but S3 sync is pending.
//
// Helmsman's relay block-cache fetches blocks from whichever upstream
// is set and attaches Authorization: Bearer only when a peer token
// is present. No replication, no bulk copy.
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
	// SizeBytes is the artifact's total size as reported by origin.
	// The block cache needs this up-front to plan range splits;
	// without it, serveViaBlockCache degrades to no-cache pass-
	// through (handler.go:38). Zero means "not reported".
	SizeBytes uint64
	// PeerRelayURL + PeerRelayToken are populated when origin returned
	// a hot-but-unsynced peer-relay fallback instead of an S3 URL.
	// When non-empty, URL is empty — the relay fetches from the peer
	// node directly using the token as Authorization: Bearer.
	PeerRelayURL   string
	PeerRelayToken string
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
func ResolveCrossClusterArtifactURL(ctx context.Context, artifactHash, contentType, tenantID, originClusterID string) (*CrossClusterArtifactURL, error) {
	crossClusterDepsMu.RLock()
	d := crossClusterDeps
	crossClusterDepsMu.RUnlock()
	if d == nil {
		return nil, ErrCrossClusterArtifactUnavailable
	}
	return d.Resolve(ctx, artifactHash, contentType, tenantID, originClusterID)
}

// Resolve performs the PrepareArtifact RPC chain with single-hop
// storage-redirect support. Mirrors playback.go's resolveRemoteArtifact
// minus the local adoption step — STREAM_SOURCE wants the URL itself
// for read-through, not bytes-on-disk.
func (d *CrossClusterArtifactDeps) Resolve(ctx context.Context, artifactHash, contentType, tenantID, originClusterID string) (*CrossClusterArtifactURL, error) {
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
		URL:              resp.GetUrl(),
		SegmentURLs:      resp.GetSegmentUrls(),
		OriginClusterID:  originClusterID,
		StorageClusterID: storageClusterID,
		Format:           resp.GetFormat(),
		SizeBytes:        resp.GetSizeBytes(),
		PeerRelayURL:     resp.GetPeerRelayUrl(),
		PeerRelayToken:   resp.GetPeerRelayToken(),
	}, nil
}
