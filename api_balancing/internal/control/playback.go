package control

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"net"
	"net/url"
	"sort"
	"strings"
	"time"

	"frameworks/api_balancing/internal/balancer"
	"frameworks/api_balancing/internal/geo"
	"frameworks/api_balancing/internal/state"
	"frameworks/api_balancing/internal/storage"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	pb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/pullsource"

	"google.golang.org/protobuf/types/known/timestamppb"
)

// ContentResolution contains the result of resolving a playback request input
type ContentResolution struct {
	ContentType  string // "live", "clip", "dvr"
	ContentId    string // Public view key (playback_id) for live/clip/dvr/vod
	FixedNode    string // Storage node URL for VOD content, empty for live
	FixedNodeID  string // Storage node ID for VOD content
	TenantId     string
	StreamId     string
	InternalName string                  // Original stream internal name (for clips/DVR: the source stream)
	IngestMode   string                  // "push" or "pull" for live streams
	ClusterPeers []*pb.TenantClusterPeer // Tenant's cluster context from Commodore (free with every resolve)
	RequiresAuth bool
}

// DVRChapterPolicyPlaybackID resolves a chapter playback ID to its
// parent DVR's playback ID so policy enforcement evaluates the
// parent's protected-playback object. Non-chapter inputs pass
// through unchanged.
//
// commodore.dvr_chapter_playback is the single authority for chapter
// playback IDs (see docs/standards/dvr-chapters.md). Anything not in
// that table is not a chapter playback ID and needs no walk.
func DVRChapterPolicyPlaybackID(ctx context.Context, contentID string) string {
	if contentID == "" || CommodoreClient == nil {
		return contentID
	}
	chapterPB, err := CommodoreClient.ResolveChapterPlaybackID(ctx, contentID)
	if err != nil || chapterPB == nil || !chapterPB.GetFound() {
		return contentID
	}
	return chapterParentPlaybackID(ctx, chapterPB.GetChapterId(), contentID)
}

// chapterParentPlaybackID walks chapter row → parent DVR → Commodore
// playback ID. Returns fallback when any hop fails so policy
// enforcement defaults to the original input (treated as protected per
// the existing fail-closed contract).
func chapterParentPlaybackID(ctx context.Context, chapterID, fallback string) string {
	chapter, err := GetChapter(ctx, chapterID)
	if err != nil {
		return fallback
	}
	dvr, err := CommodoreClient.ResolveDVRHash(ctx, chapter.ArtifactHash)
	if err != nil || !dvr.GetFound() || dvr.GetPlaybackId() == "" {
		return fallback
	}
	return dvr.GetPlaybackId()
}

// DVRChapterPolicyInternalName mirrors DVRChapterPolicyPlaybackID for
// the Mist internal_name surface (USER_NEW). Chapter artifacts are
// stored in foghorn.artifacts with internal_name == artifact_hash and
// origin_type='dvr_chapter'; that row is the single authority for
// "this internal_name is a chapter". Non-chapter inputs pass through
// unchanged.
func DVRChapterPolicyInternalName(ctx context.Context, internalName string) string {
	if internalName == "" || CommodoreClient == nil {
		return internalName
	}
	bare := strings.TrimPrefix(internalName, "vod+")
	chapterID := chapterOriginIDForArtifact(ctx, bare)
	if chapterID == "" {
		return internalName
	}
	return chapterParentInternalName(ctx, chapterID, internalName)
}

func chapterParentInternalName(ctx context.Context, chapterID, fallback string) string {
	chapter, err := GetChapter(ctx, chapterID)
	if err != nil {
		return fallback
	}
	dvr, err := CommodoreClient.ResolveDVRHash(ctx, chapter.ArtifactHash)
	if err != nil || !dvr.GetFound() || dvr.GetInternalName() == "" {
		return fallback
	}
	return dvr.GetInternalName()
}

// chapterOriginIDForArtifact returns origin_id when the artifact is a
// chapter-origin VOD (origin_type='dvr_chapter'), else empty string.
func chapterOriginIDForArtifact(ctx context.Context, artifactHash string) string {
	if db == nil || artifactHash == "" {
		return ""
	}
	var originType, originID sql.NullString
	if err := db.QueryRowContext(ctx, `
		SELECT origin_type, origin_id
		  FROM foghorn.artifacts
		 WHERE artifact_hash = $1
	`, artifactHash).Scan(&originType, &originID); err != nil {
		return ""
	}
	if originType.Valid && originType.String == "dvr_chapter" && originID.Valid {
		return originID.String
	}
	return ""
}

type PlaybackPolicyTarget struct {
	ContentID    string
	InternalName string
}

func ResolvePlaybackPolicyTarget(ctx context.Context, contentID, internalName string) PlaybackPolicyTarget {
	target := PlaybackPolicyTarget{
		ContentID:    strings.TrimSpace(contentID),
		InternalName: strings.TrimSpace(internalName),
	}
	if policyContentID := DVRChapterPolicyPlaybackID(ctx, target.ContentID); policyContentID != "" {
		target.ContentID = policyContentID
	}
	if policyInternalName := DVRChapterPolicyInternalName(ctx, target.InternalName); policyInternalName != "" {
		target.InternalName = policyInternalName
	}
	return target
}

func (r *ContentResolution) RoutingInternalName() string {
	if r == nil {
		return ""
	}
	internalName := strings.TrimSpace(r.InternalName)
	if strings.EqualFold(r.IngestMode, "pull") && internalName != "" &&
		!strings.HasPrefix(internalName, "pull+") && !strings.HasPrefix(internalName, "live+") {
		return "pull+" + internalName
	}
	return internalName
}

// ResolveContent determines content type and resolution strategy for a playback request.
func ResolveContent(ctx context.Context, input string) (*ContentResolution, error) {
	if input == "" {
		return nil, fmt.Errorf("empty input")
	}

	// 0. Chapter playback. Chapters are hidden VOD artifacts
	// (origin_type='dvr_chapter', library_visible=false) addressed by
	// the Commodore-minted chapter playback_id stored in
	// commodore.dvr_chapter_playback. Auth + tenant context inherit
	// from the parent DVR via the chapter row; viewer URLs use the
	// public playback_id, never the raw artifact_hash.
	if res := resolveChapterArtifactContent(ctx, input); res != nil {
		return res, nil
	}

	// 1. Artifact playback_id (clip/dvr/vod)
	if CommodoreClient != nil {
		if resp, err := CommodoreClient.ResolveArtifactPlaybackID(ctx, input); err == nil && resp.Found {
			contentType := strings.ToLower(strings.TrimSpace(resp.ContentType))
			switch contentType {
			case "clip", "dvr", "vod":
			default:
				return nil, fmt.Errorf("invalid artifact content_type %q", resp.ContentType)
			}
			// InternalName must carry the Mist namespace prefix so that
			// downstream resolvers (resolveDVRViewerEndpoint,
			// ResolveLivePlayback, USER_NEW policy lookups) see the same
			// stream identity Mist will see at PLAY_REWRITE time. DVR
			// playback uses dvr+<dvr_internal_name>; clip/VOD share
			// vod+<internal_name>. This mirrors what ResolveStream
			// (trigger-time resolution) returns.
			internalName := resp.InternalName
			switch contentType {
			case "dvr":
				internalName = "dvr+" + resp.InternalName
			case "clip", "vod":
				internalName = "vod+" + resp.InternalName
			}
			res := &ContentResolution{
				ContentType:  contentType,
				ContentId:    input,
				TenantId:     resp.TenantId,
				StreamId:     resp.StreamId,
				InternalName: internalName,
				RequiresAuth: resp.GetRequiresAuth(),
			}
			if resp.ArtifactHash != "" {
				if host, _ := state.DefaultManager().FindNodeByArtifactHash(resp.ArtifactHash); host != "" {
					res.FixedNode = host
					if loadBalancerInstance != nil {
						res.FixedNodeID = loadBalancerInstance.GetNodeIDByHost(host)
					}
				}
			}
			return res, nil
		}
	}

	// The web viewer can be opened from DVR registry rows that carry the
	// artifact hash. Normalize that hash through Commodore before routing;
	// chapter artifacts remain playback_id-only.
	if CommodoreClient != nil && isArtifactHashCandidate(input) {
		if resp, err := CommodoreClient.ResolveDVRHash(ctx, input); err == nil && resp.GetFound() {
			contentID := resp.GetPlaybackId()
			if contentID == "" {
				contentID = input
			}
			requiresAuth := false
			if artifact, artErr := CommodoreClient.ResolveArtifactInternalName(ctx, resp.GetInternalName()); artErr == nil && artifact.GetFound() {
				requiresAuth = artifact.GetRequiresAuth()
			}
			return &ContentResolution{
				ContentType:  "dvr",
				ContentId:    contentID,
				TenantId:     resp.GetTenantId(),
				StreamId:     resp.GetStreamId(),
				InternalName: "dvr+" + resp.GetInternalName(),
				RequiresAuth: requiresAuth,
			}, nil
		}
	}

	// 2. Live playback_id resolution
	if CommodoreClient != nil {
		if resp, err := CommodoreClient.ResolvePlaybackID(ctx, input); err == nil && resp.InternalName != "" {
			return &ContentResolution{
				ContentType:  "live",
				ContentId:    input,
				TenantId:     resp.TenantId,
				StreamId:     resp.StreamId,
				InternalName: resp.InternalName,
				IngestMode:   resp.GetIngestMode(),
				ClusterPeers: resp.ClusterPeers,
				RequiresAuth: resp.GetRequiresAuth(),
			}, nil
		}
	}

	return nil, fmt.Errorf("content not found")
}

func isArtifactHashCandidate(input string) bool {
	if len(input) != 32 {
		return false
	}
	for _, ch := range input {
		if (ch >= '0' && ch <= '9') || (ch >= 'a' && ch <= 'f') || (ch >= 'A' && ch <= 'F') {
			continue
		}
		return false
	}
	return true
}

// ArtifactFederationClient is the subset of federation.FederationClient needed for cross-cluster artifact resolution.
type ArtifactFederationClient interface {
	PrepareArtifact(ctx context.Context, clusterID, addr string, req *pb.PrepareArtifactRequest) (*pb.PrepareArtifactResponse, error)
}

// PeerAddressResolver resolves gRPC addresses for peer clusters.
type PeerAddressResolver interface {
	GetPeerAddr(clusterID string) string
}

// RemoteArtifactLookup queries the federation cache for hot artifacts on peer edges.
type RemoteArtifactLookup interface {
	GetRemoteArtifacts(ctx context.Context, artifactHash string) ([]*RemoteArtifactInfo, error)
}

// RemoteArtifactInfo describes a hot artifact on a peer cluster's edge node.
type RemoteArtifactInfo struct {
	PeerCluster  string
	NodeID       string
	BaseURL      string
	SizeBytes    uint64
	AccessCount  uint32
	LastAccessed int64
	GeoLat       float64
	GeoLon       float64
}

// PlaybackDependencies contains dependencies needed for playback resolution
// filterPullCandidatesByEligibility constrains the candidate node set for a
// pull+ cold-start to clusters that satisfy the URI's eligibility class.
// This routing-side gate sends viewers to a cluster that is eligible to run
// the pull instead of letting STREAM_SOURCE reject the selected cluster later.
// Public URIs return the input unchanged; private
// URIs filter to clusters whose Quartermaster row has
// allow_private_pull_sources=true. Quartermaster lookup failures fail
// closed (drop the node) — better to route nowhere than to the wrong
// cluster.
//
// Local nodes (NodeWithScore.ClusterID == "") share deps.LocalClusterID;
// remote edges carry their own ClusterID. The shared cluster lookup
// guarantees a single GetCluster call per cluster_id seen.
func filterPullCandidatesByEligibility(ctx context.Context, nodes []balancer.NodeWithScore, internalName string, deps *PlaybackDependencies) ([]balancer.NodeWithScore, error) {
	if len(nodes) == 0 {
		return nodes, nil
	}
	if CommodoreClient == nil {
		// No Commodore = can't classify. Defensive trigger-time check still
		// catches the bad case; let it through here.
		return nodes, nil
	}
	src, lookupErr := CommodoreClient.ResolvePullSourceByInternalName(ctx, internalName)
	if lookupErr != nil || src == nil || !src.GetFound() {
		// No pull-source row → this isn't a managed pull stream we can
		// classify. Let downstream paths (artifact resolution, etc) handle.
		return nodes, nil //nolint:nilerr // lookupErr is "not a pull stream", not a failure
	}
	class, _ := pullsource.Classify(src.GetSourceUri()) //nolint:errcheck // class encodes the rejection
	allowed := src.GetAllowedClusterIds()
	// Fast path: public source with no placement pin — legacy behavior, any cluster.
	if class == pullsource.ClassPublic && len(allowed) == 0 {
		return nodes, nil
	}
	localClusterID := ""
	if deps != nil {
		localClusterID = deps.LocalClusterID
	}
	return filterPullCandidatesByClass(ctx, nodes, internalName, localClusterID, class, allowed, ClusterAllowsPrivatePulls)
}

func filterPullCandidatesByClass(
	ctx context.Context,
	nodes []balancer.NodeWithScore,
	internalName, localClusterID string,
	class pullsource.Class,
	allowedClusterIDs []string,
	allowsPrivatePulls func(context.Context, string) bool,
) ([]balancer.NodeWithScore, error) {
	if class == pullsource.ClassBlocked {
		return nil, fmt.Errorf("pull source for stream %q is in the always-blocked set; refusing to route", internalName)
	}
	if class == pullsource.ClassPublic && len(allowedClusterIDs) == 0 {
		return nodes, nil
	}

	// Collect the unique cluster set seen across candidate nodes, build a
	// ClusterCapability slice with each cluster's flag, then run the shared
	// pullsource.FilterPlacementClusters helper so this path uses the same
	// rules as bootstrap render, Commodore reconcile, /source, and
	// STREAM_SOURCE. Capability lookup is per cluster_id, not per node.
	clusterIDFor := func(n balancer.NodeWithScore) string {
		if n.ClusterID != "" {
			return n.ClusterID
		}
		return localClusterID
	}
	seen := map[string]bool{}
	candidates := make([]pullsource.ClusterCapability, 0)
	for _, n := range nodes {
		clusterID := clusterIDFor(n)
		if clusterID == "" || seen[clusterID] {
			continue
		}
		seen[clusterID] = true
		allowPrivate := false
		if class == pullsource.ClassPrivate && allowsPrivatePulls != nil {
			allowPrivate = allowsPrivatePulls(ctx, clusterID)
		}
		candidates = append(candidates, pullsource.ClusterCapability{
			ID:                      clusterID,
			AllowPrivatePullSources: allowPrivate,
		})
	}
	eligible, rejects := pullsource.FilterPlacementClusters(class, allowedClusterIDs, candidates)
	if len(eligible) == 0 {
		// Distinguish the two failure modes operators care about: "you forgot
		// to configure placement" vs "the cluster set Commodore picked has
		// nothing reachable". The first reject carries the canonical reason.
		if len(rejects) > 0 {
			return nil, fmt.Errorf("pull source for stream %q rejected: %s", internalName, summarizePlacementRejects(rejects))
		}
		return nil, fmt.Errorf("pull source for stream %q has no eligible media cluster reachable", internalName)
	}
	allow := make(map[string]bool, len(eligible))
	for _, c := range eligible {
		allow[c.ID] = true
	}
	out := make([]balancer.NodeWithScore, 0, len(nodes))
	for _, n := range nodes {
		if allow[clusterIDFor(n)] {
			out = append(out, n)
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("pull source for stream %q has no eligible candidate node in the allowed cluster set", internalName)
	}
	return out, nil
}

// summarizePlacementRejects flattens FilterPlacementClusters rejections into
// a single line for runtime errors. Mirrored in handlers and triggers.
func summarizePlacementRejects(rejects []pullsource.PlacementReject) string {
	parts := make([]string, 0, len(rejects))
	for _, r := range rejects {
		switch r.Reason {
		case pullsource.PlacementRejectEmptyForPrivate:
			parts = append(parts, "private/multicast source has no allowed_cluster_ids configured")
		case pullsource.PlacementRejectUnknownCluster:
			parts = append(parts, fmt.Sprintf("cluster %q is not an eligible media cluster", r.ClusterID))
		case pullsource.PlacementRejectMissingPrivateCapability:
			parts = append(parts, fmt.Sprintf("cluster %q does not allow private pull sources", r.ClusterID))
		default:
			parts = append(parts, fmt.Sprintf("cluster %q rejected: %s", r.ClusterID, r.Reason))
		}
	}
	return strings.Join(parts, "; ")
}

// ClusterAllowsPrivatePulls returns Quartermaster's allow_private_pull_sources
// flag for the cluster, fail-closed on lookup error or missing client.
// Exported so the /source HTTP handler shares the same single source of truth.
func ClusterAllowsPrivatePulls(ctx context.Context, clusterID string) bool {
	if clusterID == "" || quartermasterClient == nil {
		return false
	}
	lookupCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	resp, err := quartermasterClient.GetCluster(lookupCtx, clusterID)
	if err != nil || resp == nil || resp.GetCluster() == nil {
		return false
	}
	return resp.GetCluster().GetAllowPrivatePullSources()
}

type PlaybackDependencies struct {
	DB              *sql.DB
	LB              *balancer.LoadBalancer
	GeoLat          float64
	GeoLon          float64
	RemoteEdges     []balancer.RemoteEdgeCandidate // optional: pre-collected remote edge candidates from federation
	FedClient       ArtifactFederationClient       // optional: for cross-cluster artifact resolution
	PeerResolver    PeerAddressResolver            // optional: resolves peer cluster addresses
	LocalClusterID  string                         // this cluster's ID
	RemoteArtifacts RemoteArtifactLookup           // optional: hot artifact locations from peering
}

// ResolveArtifactPlayback resolves playback endpoints for any artifact (clip/dvr/vod) using playback ID
func ResolveArtifactPlayback(ctx context.Context, deps *PlaybackDependencies, playbackID string) (*pb.ViewerEndpointResponse, error) {
	if deps.DB == nil {
		return nil, fmt.Errorf("database not available")
	}
	if playbackID == "" {
		return nil, fmt.Errorf("playback_id is required")
	}
	if CommodoreClient == nil {
		return nil, fmt.Errorf("commodore client not available")
	}

	// Chapter playback IDs resolve before the generic VOD registry path
	// because chapter artifacts inherit auth + stream context from the
	// parent DVR even though Commodore also keeps a hidden VOD registry row.
	if artifactResp, ok := resolveChapterArtifactPlaybackResp(ctx, playbackID); ok {
		return resolveArtifactPlaybackWithResp(ctx, deps, playbackID, artifactResp)
	}

	artifactResp, err := CommodoreClient.ResolveArtifactPlaybackID(ctx, playbackID)
	if err != nil || !artifactResp.Found || artifactResp.ArtifactHash == "" || artifactResp.ContentType == "" {
		return nil, fmt.Errorf("content not found")
	}
	return resolveArtifactPlaybackWithResp(ctx, deps, playbackID, artifactResp)
}

// resolveArtifactPlaybackWithResp completes the artifact playback
// resolution from a pre-resolved artifactResp (Commodore-backed or
// chapter-synthesized).
func resolveArtifactPlaybackWithResp(ctx context.Context, deps *PlaybackDependencies, playbackID string, artifactResp *pb.ResolveArtifactPlaybackIDResponse) (*pb.ViewerEndpointResponse, error) {
	if artifactResp == nil {
		return nil, fmt.Errorf("content not found")
	}
	if artifactResp.TenantId == "" {
		return nil, fmt.Errorf("tenant_id missing for artifact")
	}

	contentType := strings.ToLower(artifactResp.ContentType)
	artifactType := contentType
	tenantID := artifactResp.TenantId
	originClusterID := artifactResp.GetOriginClusterId()
	allowedClusters := artifactResp.GetClusterPeers()

	// Query foghorn.artifacts for lifecycle state
	var internalName string
	var status string
	var durationSeconds sql.NullInt64
	var sizeBytes sql.NullInt64
	var createdAt sql.NullTime
	var format sql.NullString
	var storageLocation sql.NullString
	var syncStatus sql.NullString
	var hasThumbnails bool
	var authoritativeCluster sql.NullString

	err := deps.DB.QueryRowContext(ctx, `
		SELECT COALESCE(internal_name, ''),
		       status,
		       duration_seconds,
		       size_bytes,
		       created_at,
		       format,
		       COALESCE(storage_location, ''),
		       COALESCE(sync_status, ''),
		       COALESCE(has_thumbnails, false),
		       COALESCE(storage_cluster_id, origin_cluster_id)
		FROM foghorn.artifacts
		WHERE artifact_hash = $1 AND artifact_type = $2 AND status != 'deleted' AND tenant_id = $3
	`, artifactResp.ArtifactHash, artifactType, tenantID).Scan(&internalName, &status, &durationSeconds, &sizeBytes, &createdAt, &format, &storageLocation, &syncStatus, &hasThumbnails, &authoritativeCluster)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			if originClusterID != "" && originClusterID != deps.LocalClusterID && deps.FedClient != nil {
				return resolveRemoteArtifact(ctx, deps, artifactResp.ArtifactHash, originClusterID, contentType, tenantID, allowedClusters)
			}
			return nil, fmt.Errorf("%s not found", contentType)
		}
		return nil, fmt.Errorf("failed to query artifact: %w", err)
	}

	var artifactNodes []state.ArtifactNodeInfo
	if manager := state.DefaultManager(); manager != nil {
		artifactNodes = manager.FindNodesByArtifactHash(artifactResp.ArtifactHash)
	}
	if len(artifactNodes) == 0 {
		if rows, rowErr := artifactNodesFromDB(ctx, deps.DB, artifactResp.ArtifactHash, artifactType); rowErr == nil {
			artifactNodes = rows
		}
	}
	if len(artifactNodes) == 0 {
		// Check peer clusters for hot copies before falling through to S3/defrost
		if deps.RemoteArtifacts != nil {
			remoteHits, _ := deps.RemoteArtifacts.GetRemoteArtifacts(ctx, artifactResp.ArtifactHash)
			var authorizedHits []*RemoteArtifactInfo
			for _, h := range remoteHits {
				if isAuthorizedPeerCluster(h.PeerCluster, allowedClusters) {
					authorizedHits = append(authorizedHits, h)
				}
			}
			if len(authorizedHits) > 0 {
				best := pickBestRemoteArtifact(authorizedHits, deps.GeoLat, deps.GeoLon)
				if best != nil {
					return &pb.ViewerEndpointResponse{
						Primary: &pb.ViewerEndpoint{
							NodeId:      best.NodeID,
							BaseUrl:     best.BaseURL,
							Protocol:    "https",
							GeoDistance: CalculateGeoDistance(deps.GeoLat, deps.GeoLon, best.GeoLat, best.GeoLon),
							ClusterId:   best.PeerCluster,
						},
					}, nil
				}
			}
		}

		// No warm nodes. The read-through artifact relay can stream from
		// S3 to disk on demand, so we don't gate playback on defrost
		// anymore — pick any storage-capable edge and let Mist
		// STREAM_SOURCE → Helmsman relay → block cache materialize the
		// bytes on the first viewer request.
		location := strings.ToLower(strings.TrimSpace(storageLocation.String))
		sync := strings.ToLower(strings.TrimSpace(syncStatus.String))
		if location == "defrosting" {
			// Existing in-flight defrost; let it finish.
			return nil, NewDefrostingError(10, "defrost in progress")
		}
		if sync == "synced" || location == "s3" {
			lbctx := context.WithValue(ctx, ctxkeys.KeyCapability, "edge,storage")
			if tenantID != "" {
				lbctx = context.WithValue(lbctx, ctxkeys.KeyClusterScope, tenantID)
			}
			nodes, lbErr := deps.LB.GetTopNodesWithScores(lbctx, "", deps.GeoLat, deps.GeoLon, make(map[string]int), "", 5, false)
			if lbErr != nil || len(nodes) == 0 {
				return nil, fmt.Errorf("no suitable edge for cold artifact playback: %w", lbErr)
			}
			coldRanked := rankNodeScoresForArtifact(nodes, deps.GeoLat, deps.GeoLon)
			artifactNodes = coldRanked
		} else if originClusterID != "" && originClusterID != deps.LocalClusterID && deps.FedClient != nil {
			// Federation fallback: artifact exists locally but not on any node and not in S3
			return resolveRemoteArtifact(ctx, deps, artifactResp.ArtifactHash, originClusterID, contentType, tenantID, allowedClusters)
		} else {
			return nil, fmt.Errorf("storage node unknown: no node assignment found")
		}
	}

	streamID := artifactResp.StreamId
	streamInternalName := ""
	title := ""
	description := ""
	clipDurationMs := int64(0)
	resolvedPlaybackID := playbackID

	switch contentType {
	case "clip":
		if resp, err := CommodoreClient.ResolveClipHash(ctx, artifactResp.ArtifactHash); err == nil && resp.Found {
			if resp.TenantId != "" {
				tenantID = resp.TenantId
			}
			if resp.StreamId != "" {
				streamID = resp.StreamId
			}
			if resp.InternalName != "" {
				internalName = resp.InternalName
			}
			title = resp.Title
			description = resp.Description
			clipDurationMs = resp.Duration
			if resp.PlaybackId != "" {
				resolvedPlaybackID = resp.PlaybackId
			}
		}
	case "dvr":
		if resp, err := CommodoreClient.ResolveDVRHash(ctx, artifactResp.ArtifactHash); err == nil && resp.Found {
			if resp.TenantId != "" {
				tenantID = resp.TenantId
			}
			if resp.StreamId != "" {
				streamID = resp.StreamId
			}
			streamInternalName = resp.GetStreamInternalName()
			if resp.PlaybackId != "" {
				resolvedPlaybackID = resp.PlaybackId
			}
		}
	case "vod":
		if resp, err := CommodoreClient.ResolveVodHash(ctx, artifactResp.ArtifactHash); err == nil && resp.Found {
			if resp.TenantId != "" {
				tenantID = resp.TenantId
			}
			title = resp.Title
			description = resp.Description
			if resp.PlaybackId != "" {
				resolvedPlaybackID = resp.PlaybackId
			}
		}
	}

	rankedNodes := rankArtifactNodes(artifactNodes, deps.GeoLat, deps.GeoLon, 5)
	if len(rankedNodes) == 0 {
		return nil, fmt.Errorf("storage node outputs not available")
	}

	var endpoints []*pb.ViewerEndpoint
	for _, node := range rankedNodes {
		nodeOutputs, exists := GetNodeOutputs(node.NodeID)
		if !exists || nodeOutputs.Outputs == nil {
			continue
		}

		// Build URLs using playback ID (MistServer resolves via PLAY_REWRITE trigger)
		var protocol, endpointURL string
		formatValue := ""
		if format.Valid {
			formatValue = format.String
		}
		protocol, endpointURL = selectPrimaryArtifactOutput(nodeOutputs.Outputs, nodeOutputs.BaseURL, resolvedPlaybackID, formatValue)
		if endpointURL == "" {
			endpointURL = EnsureTrailingSlash(nodeOutputs.BaseURL) + resolvedPlaybackID + ".html"
			protocol = "html"
		}

		geoDistance := 0.0
		if geo.IsValidLatLon(deps.GeoLat, deps.GeoLon) && geo.IsValidLatLon(node.GeoLatitude, node.GeoLongitude) {
			geoDistance = CalculateGeoDistance(deps.GeoLat, deps.GeoLon, node.GeoLatitude, node.GeoLongitude)
		}

		endpoints = append(endpoints, &pb.ViewerEndpoint{
			NodeId:      node.NodeID,
			BaseUrl:     nodeOutputs.BaseURL,
			Protocol:    protocol,
			Url:         endpointURL,
			GeoDistance: geoDistance,
			LoadScore:   float64(node.Score),
			Outputs:     BuildOutputsMap(nodeOutputs.BaseURL, nodeOutputs.Outputs, resolvedPlaybackID, false),
		})
	}

	if len(endpoints) == 0 {
		return nil, fmt.Errorf("storage node outputs not available")
	}

	metadata := &pb.PlaybackMetadata{
		Status:      status,
		IsLive:      contentType == "dvr" && status == "recording",
		TenantId:    tenantID,
		ContentId:   resolvedPlaybackID,
		ContentType: contentType,
	}
	if streamID != "" {
		metadata.StreamId = &streamID
	}
	if contentType == "dvr" {
		metadata.DvrStatus = status
	}
	if contentType == "clip" && internalName != "" {
		metadata.ClipSource = &internalName
	}
	if title != "" {
		metadata.Title = &title
	}
	if description != "" {
		metadata.Description = &description
	}
	if contentType == "clip" && clipDurationMs > 0 {
		d := int32(clipDurationMs / 1000)
		metadata.DurationSeconds = &d
	} else if durationSeconds.Valid {
		d := int32(durationSeconds.Int64)
		metadata.DurationSeconds = &d
	}
	if sizeBytes.Valid {
		metadata.RecordingSizeBytes = &sizeBytes.Int64
	}
	if createdAt.Valid {
		metadata.CreatedAt = timestamppb.New(createdAt.Time)
	}
	if format.Valid && format.String != "" {
		metadata.Format = &format.String
	}
	// Chapter artifacts resolve as contentType="vod" but are produced
	// through the chapter finalization processing pipeline that emits
	// thumbnail/sprite assets the same way DVR/clip does. Expose them
	// on chapter playback so the player has a poster/storyboard.
	if hasThumbnails && (contentType == "dvr" || contentType == "clip" || contentType == "vod") {
		// Pick the Chandler whose S3 actually serves this artifact's
		// thumbnail. authoritativeCluster = COALESCE(storage_cluster_id,
		// origin_cluster_id) — NULL on storage_cluster_id falls back to
		// origin. Empty or unresolvable cluster context falls back to the
		// local Chandler URL.
		chandlerBase := getChandlerBaseURL()
		if authoritativeCluster.Valid && authoritativeCluster.String != "" {
			if perCluster := getChandlerBaseURLForCluster(authoritativeCluster.String); perCluster != "" {
				chandlerBase = perCluster
			}
		}
		metadata.ThumbnailAssets = buildThumbnailAssets(chandlerBase, artifactResp.ArtifactHash)
	}
	if metadata.ThumbnailAssets == nil && contentType == "dvr" && streamID != "" && streamInternalName != "" {
		chandlerBase := resolveLiveThumbnailChandlerBase(ctx, tenantID, streamInternalName)
		metadata.ThumbnailAssets = buildPosterThumbnailAssets(chandlerBase, streamID)
	}

	return &pb.ViewerEndpointResponse{
		Primary:   endpoints[0],
		Fallbacks: endpoints[1:],
		Metadata:  metadata,
	}, nil
}

// rankNodeScoresForArtifact projects load-balancer node candidates
// into the ArtifactNodeInfo shape so the cold-artifact resolution path
// can hand off to the same endpoint-building code as warm playback.
// Used when sync_status='synced' / location='s3' and no warm copies
// exist — Helmsman's relay+blockcache pulls bytes from S3 on demand.
func rankNodeScoresForArtifact(nodes []balancer.NodeWithScore, viewerLat, viewerLon float64) []state.ArtifactNodeInfo {
	out := make([]state.ArtifactNodeInfo, 0, len(nodes))
	for _, n := range nodes {
		// Skip remote-cluster candidates: cold-artifact relay reads run
		// on the local cluster's edge against the artifact's S3 source.
		if n.ClusterID != "" {
			continue
		}
		out = append(out, state.ArtifactNodeInfo{
			NodeID:       n.NodeID,
			Host:         n.Host,
			Score:        int64(n.Score),
			GeoLatitude:  n.GeoLatitude,
			GeoLongitude: n.GeoLongitude,
		})
	}
	return rankArtifactNodes(out, viewerLat, viewerLon, 5)
}

func artifactNodesFromDB(ctx context.Context, db *sql.DB, artifactHash, artifactType string) ([]state.ArtifactNodeInfo, error) {
	if db == nil || strings.TrimSpace(artifactHash) == "" {
		return nil, nil
	}
	rows, err := db.QueryContext(ctx, `
		SELECT an.node_id,
		       COALESCE(an.file_path, ''),
		       COALESCE(an.size_bytes, a.size_bytes, 0),
		       COALESCE(NULLIF(a.format, ''), ''),
		       COALESCE(NULLIF(a.stream_internal_name, ''), '')
		  FROM foghorn.artifact_nodes an
		  JOIN foghorn.artifacts a ON a.artifact_hash = an.artifact_hash
		 WHERE an.artifact_hash = $1
		   AND a.artifact_type = $2
		   AND an.is_orphaned = false
		 ORDER BY an.last_seen_at DESC NULLS LAST
	`, artifactHash, artifactType)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var nodes []state.ArtifactNodeInfo
	for rows.Next() {
		var nodeID, filePath, format, streamName string
		var sizeBytes int64
		if scanErr := rows.Scan(&nodeID, &filePath, &sizeBytes, &format, &streamName); scanErr != nil {
			return nil, scanErr
		}
		if sizeBytes < 0 {
			sizeBytes = 0
		}
		nodes = append(nodes, state.ArtifactNodeInfo{
			NodeID: nodeID,
			Score:  0,
			Artifact: &pb.StoredArtifact{
				ClipHash:     artifactHash,
				StreamName:   streamName,
				FilePath:     filePath,
				SizeBytes:    uint64(sizeBytes),
				Format:       format,
				ArtifactType: playbackArtifactTypeToProto(artifactType),
			},
		})
	}
	return nodes, rows.Err()
}

func playbackArtifactTypeToProto(artifactType string) pb.ArtifactEvent_ArtifactType {
	switch strings.ToLower(strings.TrimSpace(artifactType)) {
	case "clip":
		return pb.ArtifactEvent_ARTIFACT_TYPE_CLIP
	case "dvr":
		return pb.ArtifactEvent_ARTIFACT_TYPE_DVR
	case "vod":
		return pb.ArtifactEvent_ARTIFACT_TYPE_VOD
	default:
		return pb.ArtifactEvent_ARTIFACT_TYPE_UNSPECIFIED
	}
}

func rankArtifactNodes(nodes []state.ArtifactNodeInfo, viewerLat, viewerLon float64, maxNodes int) []state.ArtifactNodeInfo {
	if len(nodes) == 0 {
		return nil
	}
	ranked := append([]state.ArtifactNodeInfo(nil), nodes...)
	useGeo := viewerLat != 0 || viewerLon != 0
	sort.Slice(ranked, func(i, j int) bool {
		if useGeo {
			di := artifactGeoDistance(viewerLat, viewerLon, ranked[i])
			dj := artifactGeoDistance(viewerLat, viewerLon, ranked[j])
			if di != dj {
				return di < dj
			}
		}
		if ranked[i].Score != ranked[j].Score {
			return ranked[i].Score < ranked[j].Score
		}
		return ranked[i].NodeID < ranked[j].NodeID
	})
	if maxNodes > 0 && len(ranked) > maxNodes {
		return ranked[:maxNodes]
	}
	return ranked
}

func artifactGeoDistance(viewerLat, viewerLon float64, node state.ArtifactNodeInfo) float64 {
	if node.GeoLatitude == 0 && node.GeoLongitude == 0 {
		return math.MaxFloat64
	}
	return CalculateGeoDistance(viewerLat, viewerLon, node.GeoLatitude, node.GeoLongitude)
}

func selectPrimaryArtifactOutput(outputs map[string]any, baseURL, playbackID, format string) (string, string) {
	if outputs == nil {
		return "", ""
	}
	for _, key := range preferredArtifactOutputKeys(format) {
		raw, ok := outputs[key]
		if !ok {
			continue
		}
		if u := ResolveTemplateURL(raw, baseURL, playbackID); u != "" {
			return strings.ToLower(key), u
		}
	}
	return "", ""
}

func preferredArtifactOutputKeys(format string) []string {
	switch strings.ToLower(strings.TrimSpace(format)) {
	case "m3u8":
		return []string{"HLS", "DASH", "CMAF", "HDS"}
	case "mp4":
		return []string{"HTTP", "MP4", "HLS", "DASH", "CMAF"}
	case "webm":
		return []string{"HTTP", "WEBM", "HLS", "DASH", "CMAF"}
	default:
		return []string{"HTTP", "HLS", "DASH", "CMAF"}
	}
}

// ResolveLivePlayback resolves playback endpoints for a live stream using load balancing
func ResolveLivePlayback(ctx context.Context, deps *PlaybackDependencies, viewKey string, internalName string, streamID string, tenantID string) (*pb.ViewerEndpointResponse, error) {
	if deps.LB == nil {
		return nil, fmt.Errorf("load balancer not available")
	}

	// Unified state tracks live streams by their bare internal_name (e.g.
	// "demo_live_stream_001"), while MistServer uses wildcard names
	// (e.g. "live+...", "pull+...", "dvr+..."). Normalize here so load
	// balancing doesn't incorrectly filter out healthy nodes. Two cases
	// need the active-stream-presence filter dropped:
	//   - pull cold start: no node has the stream active yet because no
	//     PUSH_REWRITE precedent fills the cache.
	//   - active DVR: the dvr+<dvr_internal_name> Mist stream is only
	//     materialized on a node when a viewer connects; no node
	//     advertises it in the state manager up front. STREAM_SOURCE
	//     on the chosen edge resolves to local manifest (recording
	//     origin) or dtsc:// pull (any other edge).
	trimmed := strings.TrimSpace(internalName)
	isPull := strings.HasPrefix(trimmed, "pull+")
	isDVR := strings.HasPrefix(trimmed, "dvr+")
	trimmed = strings.TrimPrefix(trimmed, "live+")
	trimmed = strings.TrimPrefix(trimmed, "pull+")
	trimmed = strings.TrimPrefix(trimmed, "dvr+")
	internalName = trimmed

	// Use load balancer with internal name to find nodes that have the stream
	lbctx := context.WithValue(ctx, ctxkeys.KeyCapability, "edge")
	if tenantID != "" {
		lbctx = context.WithValue(lbctx, ctxkeys.KeyClusterScope, tenantID)
	}
	nodes, err := deps.LB.GetTopNodesWithScores(lbctx, internalName, deps.GeoLat, deps.GeoLon, make(map[string]int), "", 5, false)
	if (isPull || isDVR) && (err != nil || len(nodes) == 0) {
		// Cold-start fallback: drop the active-stream-presence filter
		// so we can pick an eligible edge by capacity/geo. The chosen
		// edge materializes the stream via STREAM_SOURCE on first
		// viewer.
		var coldErr error
		nodes, coldErr = deps.LB.GetTopNodesWithScores(lbctx, "", deps.GeoLat, deps.GeoLon, make(map[string]int), "", 5, false)
		if coldErr == nil {
			err = nil
		}
	}
	if err != nil && len(deps.RemoteEdges) == 0 {
		return nil, fmt.Errorf("no suitable edge nodes available: %w", err)
	}

	// Score remote edges and merge with local results
	if len(deps.RemoteEdges) > 0 {
		remoteNodes := deps.LB.ScoreRemoteEdges(deps.RemoteEdges, deps.GeoLat, deps.GeoLon)
		nodes = append(nodes, remoteNodes...)
		sort.Slice(nodes, func(i, j int) bool { return nodes[i].Score > nodes[j].Score })
		if len(nodes) > 5 {
			nodes = nodes[:5]
		}
	}
	// Pull cold-start eligibility applies after local and remote candidates are
	// merged. A private source URI may only run on a media cluster with
	// allow_private_pull_sources=true, including redirected peer clusters.
	if isPull && len(nodes) > 0 {
		filtered, filterErr := filterPullCandidatesByEligibility(ctx, nodes, internalName, deps)
		if filterErr != nil {
			return nil, filterErr
		}
		nodes = filtered
	}

	var endpoints []*pb.ViewerEndpoint

	for _, node := range nodes {
		// Remote edges: produce a redirect endpoint to the peer cluster's play domain
		if node.ClusterID != "" {
			geoDistance := 0.0
			if geo.IsValidLatLon(deps.GeoLat, deps.GeoLon) && geo.IsValidLatLon(node.GeoLatitude, node.GeoLongitude) {
				geoDistance = CalculateGeoDistance(deps.GeoLat, deps.GeoLon, node.GeoLatitude, node.GeoLongitude)
			}
			endpoints = append(endpoints, &pb.ViewerEndpoint{
				NodeId:      node.NodeID,
				BaseUrl:     node.Host,
				Protocol:    "redirect",
				Url:         "https://" + node.Host + "/play/" + viewKey,
				GeoDistance: geoDistance,
				LoadScore:   float64(node.Score),
				ClusterId:   node.ClusterID,
			})
			continue
		}

		nodeOutputs, exists := GetNodeOutputs(node.NodeID)
		if !exists || nodeOutputs.Outputs == nil {
			continue
		}

		// Build URLs with view key (MistServer resolves via PLAY_REWRITE trigger)
		// With correct pubaddr/pubhost, MistServer fills HTTP-based outputs with full URLs.
		// Only direct protocols (RTMP, RTSP, SRT, DTSC) keep HOST placeholder.
		var protocol, endpointURL string

		// Extract public host from HTTP outputs for HOST replacement in direct protocols
		publicHost := ExtractPublicHostFromOutputs(nodeOutputs.Outputs)

		if webrtcURL, ok := nodeOutputs.Outputs["WebRTC"]; ok {
			protocol = "webrtc"
			endpointURL = ResolveTemplateURLWithHost(webrtcURL, nodeOutputs.BaseURL, viewKey, publicHost)
		} else if hlsURL, ok := nodeOutputs.Outputs["HLS"]; ok {
			protocol = "hls"
			endpointURL = ResolveTemplateURL(hlsURL, nodeOutputs.BaseURL, viewKey)
		}

		if endpointURL == "" {
			continue
		}

		// Calculate geo distance
		geoDistance := 0.0
		if geo.IsValidLatLon(deps.GeoLat, deps.GeoLon) && geo.IsValidLatLon(node.GeoLatitude, node.GeoLongitude) {
			geoDistance = CalculateGeoDistance(deps.GeoLat, deps.GeoLon, node.GeoLatitude, node.GeoLongitude)
		}

		endpoint := &pb.ViewerEndpoint{
			NodeId:      node.NodeID,
			BaseUrl:     nodeOutputs.BaseURL,
			Protocol:    protocol,
			Url:         endpointURL,
			GeoDistance: geoDistance,
			LoadScore:   float64(node.Score),
			Outputs:     BuildOutputsMap(nodeOutputs.BaseURL, nodeOutputs.Outputs, viewKey, true),
		}
		endpoints = append(endpoints, endpoint)
	}

	if len(endpoints) == 0 {
		return nil, fmt.Errorf("no eligible edge nodes have HLS/WebRTC outputs configured for stream %q", internalName)
	}

	// Build metadata from stream state
	metadata := &pb.PlaybackMetadata{
		Status:      "live",
		IsLive:      true,
		TenantId:    tenantID,
		ContentId:   viewKey,
		ContentType: "live",
	}
	if streamID != "" {
		metadata.StreamId = &streamID
		// Live streams: Helmsman uploads thumbnails to Chandler whenever
		// Mist's process_thumbs runs. The asset may 404 for streams that
		// have never been live; the player's fallback chain handles that.
		chandlerBase := resolveLiveThumbnailChandlerBase(ctx, tenantID, internalName)
		metadata.ThumbnailAssets = buildThumbnailAssets(chandlerBase, streamID)
	}

	// Enrich with stream state if available
	st := state.DefaultManager().GetStreamState(internalName)
	if st != nil {
		metadata.IsLive = st.Status == "live"
		metadata.Status = st.Status
		metadata.Viewers = int32(st.Viewers)
		metadata.BufferState = st.BufferState
	}

	// Add protocol hints
	if len(endpoints) > 0 && endpoints[0].Outputs != nil {
		for proto := range endpoints[0].Outputs {
			metadata.ProtocolHints = append(metadata.ProtocolHints, proto)
		}
	}

	return &pb.ViewerEndpointResponse{
		Primary:   endpoints[0],
		Fallbacks: endpoints[1:],
		Metadata:  metadata,
	}, nil
}

// resolveLiveThumbnailChandlerBase picks the Chandler base URL whose S3
// the thumbnail upload path will write to for this stream. Mirrors the
// chain processThumbnailUploadRequest uses — origin from Commodore plus
// official from cached Quartermaster routing — so write and read end up
// at the same cluster's Chandler. Uses the local Chandler base URL when
// cluster context is unavailable so single-cluster deployments keep serving
// thumbnails.
func resolveLiveThumbnailChandlerBase(ctx context.Context, tenantID, internalName string) string {
	originCluster := ""
	if CommodoreClient != nil && internalName != "" {
		rctx, cancel := context.WithTimeout(ctx, 1*time.Second)
		defer cancel()
		if resp, err := CommodoreClient.ResolveInternalName(rctx, internalName); err == nil && resp != nil {
			originCluster = resp.GetOriginClusterId()
			if tenantID == "" {
				tenantID = resp.GetTenantId()
			}
		}
	}
	storageCluster, mode := resolveThumbnailStorageCluster(ctx, tenantID, originCluster)
	if mode == storage.StorageUnavailable || storageCluster == "" {
		return getChandlerBaseURL()
	}
	if perCluster := getChandlerBaseURLForCluster(storageCluster); perCluster != "" {
		return perCluster
	}
	return getChandlerBaseURL()
}

func buildThumbnailAssets(chandlerBase, assetKey string) *pb.ThumbnailAssets {
	if chandlerBase == "" || assetKey == "" {
		return nil
	}
	base := strings.TrimRight(chandlerBase, "/") + "/assets/" + assetKey
	return &pb.ThumbnailAssets{
		PosterUrl:    base + "/poster.jpg",
		SpriteVttUrl: base + "/sprite.vtt",
		SpriteJpgUrl: base + "/sprite.jpg",
		AssetKey:     assetKey,
	}
}

func buildPosterThumbnailAssets(chandlerBase, assetKey string) *pb.ThumbnailAssets {
	if chandlerBase == "" || assetKey == "" {
		return nil
	}
	base := strings.TrimRight(chandlerBase, "/") + "/assets/" + assetKey
	return &pb.ThumbnailAssets{
		PosterUrl: base + "/poster.jpg",
		AssetKey:  assetKey,
	}
}

type dvrThumbnailTarget struct {
	artifactHash         string
	tenantID             sql.NullString
	authoritativeCluster sql.NullString
	hasThumbnails        bool
}

type queryRower interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func resolveDVRThumbnailTarget(ctx context.Context, conn queryRower, token string) (dvrThumbnailTarget, error) {
	var target dvrThumbnailTarget
	if conn == nil || token == "" {
		return target, sql.ErrNoRows
	}

	err := conn.QueryRowContext(ctx, `
		SELECT artifact_hash, tenant_id::text, COALESCE(storage_cluster_id, origin_cluster_id), COALESCE(has_thumbnails, false)
		  FROM foghorn.artifacts
		 WHERE internal_name = $1
		   AND artifact_type = 'dvr'
	`, token).Scan(&target.artifactHash, &target.tenantID, &target.authoritativeCluster, &target.hasThumbnails)
	if err == nil {
		return target, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return target, err
	}

	err = conn.QueryRowContext(ctx, `
		SELECT a.artifact_hash, a.tenant_id::text, COALESCE(a.storage_cluster_id, a.origin_cluster_id), COALESCE(a.has_thumbnails, false)
		  FROM foghorn.dvr_chapters c
		  JOIN foghorn.artifacts a ON a.artifact_hash = c.artifact_hash
		 WHERE c.chapter_id = $1
		   AND a.artifact_type = 'dvr'
	`, token).Scan(&target.artifactHash, &target.tenantID, &target.authoritativeCluster, &target.hasThumbnails)
	return target, err
}

// AppendViewerCorrelationID adds the virtual viewer ID to every playback URL in a response.
func AppendViewerCorrelationID(resp *pb.ViewerEndpointResponse, viewerID string) {
	if resp == nil || viewerID == "" {
		return
	}
	appendToEndpoint := func(endpoint *pb.ViewerEndpoint) {
		if endpoint == nil {
			return
		}
		endpoint.Url = AppendCorrelationID(endpoint.GetUrl(), viewerID)
		for _, output := range endpoint.GetOutputs() {
			if output != nil {
				output.Url = AppendCorrelationID(output.GetUrl(), viewerID)
			}
		}
	}
	appendToEndpoint(resp.Primary)
	for _, endpoint := range resp.Fallbacks {
		appendToEndpoint(endpoint)
	}
}

// AppendCorrelationID adds a virtual viewer ID to a playback URL.
func AppendCorrelationID(rawURL, viewerID string) string {
	if viewerID == "" || rawURL == "" {
		return rawURL
	}
	parsedURL, err := url.Parse(rawURL)
	if err != nil {
		return rawURL
	}
	query := parsedURL.Query()
	query.Set("fwcid", viewerID)
	parsedURL.RawQuery = query.Encode()
	return parsedURL.String()
}

// EnsureTrailingSlash adds a trailing slash if not present.
func EnsureTrailingSlash(s string) string {
	if !strings.HasSuffix(s, "/") {
		return s + "/"
	}
	return s
}

// ExtractPublicHostFromOutputs extracts the public hostname:port from MistServer outputs.
// MistServer outputs like HLS contain the actual public-facing host (e.g., "localhost:18090")
// while WebRTC uses "HOST" placeholder. This function extracts the public host from outputs
// that already contain it, so we can use it for HOST replacement.
func ExtractPublicHostFromOutputs(outputs map[string]any) string {
	// Try to extract from HLS, HTTP, or other outputs that typically have full URLs
	for _, keys := range [][]string{
		{"HLS", "HLS (TS)"},
		{"HTTP", "MP4", "MP4 progressive"},
		{"CMAF", "HLS (CMAF)"},
		{"HDS", "Flash Dynamic (HDS)"},
	} {
		raw, ok := findOutputRaw(outputs, keys...)
		if !ok {
			continue
		}
		var s string
		switch v := raw.(type) {
		case string:
			s = v
		case []any:
			if len(v) > 0 {
				if ss, ok := v[0].(string); ok {
					s = ss
				}
			}
		}
		if s == "" {
			continue
		}
		// Parse URL patterns like "//["localhost:18090]/view/..." or "//localhost:18090/..."
		s = strings.Trim(s, "[]\"")
		// Handle protocol-relative URLs
		if strings.HasPrefix(s, "//") {
			s = "http:" + s
		}
		if u, err := url.Parse(s); err == nil && u.Host != "" {
			return u.Host
		}
	}
	return ""
}

func findOutputRaw(outputs map[string]any, keys ...string) (any, bool) {
	for _, key := range keys {
		if raw, ok := outputs[key]; ok {
			return raw, true
		}
	}
	for outputKey, raw := range outputs {
		for _, key := range keys {
			if strings.EqualFold(outputKey, key) {
				return raw, true
			}
		}
	}
	return nil, false
}

func hostOnlyForMistTemplate(value string) string {
	host := strings.TrimSpace(value)
	if host == "" {
		return ""
	}
	if strings.HasPrefix(host, "//") {
		host = "http:" + host
	}
	if u, err := url.Parse(host); err == nil && u.Host != "" {
		host = u.Host
	}
	if before, _, ok := strings.Cut(host, "/"); ok {
		host = before
	}
	if before, _, ok := strings.Cut(host, "?"); ok {
		host = before
	}
	if h, _, err := net.SplitHostPort(host); err == nil {
		return strings.Trim(h, "[]")
	}
	return strings.Trim(host, "[]")
}

// ResolveTemplateURL replaces placeholders in Mist outputs ($ for stream name, HOST for hostname)
func ResolveTemplateURL(raw any, baseURL, streamName string) string {
	return ResolveTemplateURLWithHost(raw, baseURL, streamName, "")
}

func ResolveTemplateURLWithHost(raw any, baseURL, streamName string, hostOverride string) string {
	var s string
	switch v := raw.(type) {
	case string:
		s = v
	case []any:
		if len(v) > 0 {
			if ss, ok := v[0].(string); ok {
				s = ss
			}
		}
	default:
		return ""
	}
	if s == "" {
		return ""
	}
	s = strings.ReplaceAll(s, "$", streamName)
	if strings.Contains(s, "HOST") {
		host := hostOnlyForMistTemplate(hostOverride)
		if host == "" {
			host = hostOnlyForMistTemplate(baseURL)
		}
		if host == "" {
			return ""
		}
		s = strings.ReplaceAll(s, "HOST", host)
	}
	s = strings.Trim(s, "[]\"")
	return s
}

func toWebSocketURL(rawURL string, secureDefault bool) string {
	if rawURL == "" {
		return ""
	}
	if strings.HasPrefix(rawURL, "ws://") || strings.HasPrefix(rawURL, "wss://") {
		return rawURL
	}
	if strings.HasPrefix(rawURL, "//") {
		if secureDefault {
			return "wss:" + rawURL
		}
		return "ws:" + rawURL
	}
	if rest, ok := strings.CutPrefix(rawURL, "https://"); ok {
		return "wss://" + rest
	}
	if rest, ok := strings.CutPrefix(rawURL, "http://"); ok {
		return "ws://" + rest
	}
	return rawURL
}

func addResolvedOutput(outputs map[string]*pb.OutputEndpoint, rawOutputs map[string]any, protocol string, base string, streamName string, isLive bool, keys ...string) bool {
	raw, ok := findOutputRaw(rawOutputs, keys...)
	if !ok {
		return false
	}
	u := ResolveTemplateURL(raw, base, streamName)
	if u == "" {
		return false
	}
	outputs[protocol] = &pb.OutputEndpoint{Protocol: protocol, Url: u, Capabilities: BuildOutputCapabilities(protocol, isLive)}
	return true
}

func addWebSocketOutput(outputs map[string]*pb.OutputEndpoint, rawOutputs map[string]any, protocol string, base string, streamName string, secureDefault bool, isLive bool, keys ...string) bool {
	raw, ok := findOutputRaw(rawOutputs, keys...)
	if !ok {
		return false
	}
	u := ResolveTemplateURL(raw, base, streamName)
	if u == "" {
		return false
	}
	wsURL := toWebSocketURL(u, secureDefault)
	if wsURL == "" {
		return false
	}
	outputs[protocol] = &pb.OutputEndpoint{Protocol: protocol, Url: wsURL, Capabilities: BuildOutputCapabilities(protocol, isLive)}
	return true
}

func addDerivedOutput(outputs map[string]*pb.OutputEndpoint, protocol string, base string, streamName string, isLive bool, path string) {
	if _, exists := outputs[protocol]; exists {
		return
	}
	outputs[protocol] = &pb.OutputEndpoint{Protocol: protocol, Url: base + streamName + path, Capabilities: BuildOutputCapabilities(protocol, isLive)}
}

func addDerivedWebSocketOutput(outputs map[string]*pb.OutputEndpoint, protocol string, base string, streamName string, secureDefault bool, isLive bool, path string) {
	if _, exists := outputs[protocol]; exists {
		return
	}
	wsBase := toWebSocketURL(base, secureDefault)
	if wsBase == "" {
		return
	}
	outputs[protocol] = &pb.OutputEndpoint{Protocol: protocol, Url: wsBase + streamName + path, Capabilities: BuildOutputCapabilities(protocol, isLive)}
}

// BuildOutputsMap constructs the per-protocol outputs for a node/stream
func BuildOutputsMap(baseURL string, rawOutputs map[string]any, streamName string, isLive bool) map[string]*pb.OutputEndpoint {
	outputs := make(map[string]*pb.OutputEndpoint)

	base := EnsureTrailingSlash(baseURL)
	html := base + streamName + ".html"
	outputs["MIST_HTML"] = &pb.OutputEndpoint{Protocol: "MIST_HTML", Url: html, Capabilities: BuildOutputCapabilities("MIST_HTML", isLive)}
	outputs["PLAYER_JS"] = &pb.OutputEndpoint{Protocol: "PLAYER_JS", Url: base + "player.js", Capabilities: BuildOutputCapabilities("PLAYER_JS", isLive)}

	// Extract public host from HTTP outputs for HOST replacement in direct protocols
	publicHost := ExtractPublicHostFromOutputs(rawOutputs)

	// WHEP
	addResolvedOutput(outputs, rawOutputs, "WHEP", base, streamName, isLive, "WHEP", "WebRTC with WHEP signalling")
	if _, ok := outputs["WHEP"]; !ok {
		if u := DeriveWHEPFromHTML(html); u != "" {
			outputs["WHEP"] = &pb.OutputEndpoint{Protocol: "WHEP", Url: u, Capabilities: BuildOutputCapabilities("WHEP", isLive)}
		}
	}

	if raw, ok := findOutputRaw(rawOutputs, "WebRTC", "WebRTC with WebSocket signalling"); ok {
		if u := ResolveTemplateURLWithHost(raw, base, streamName, publicHost); u != "" {
			outputs["MIST_WEBRTC"] = &pb.OutputEndpoint{Protocol: "MIST_WEBRTC", Url: u, Capabilities: BuildOutputCapabilities("MIST_WEBRTC", isLive)}
		}
	}
	addResolvedOutput(outputs, rawOutputs, "HLS", base, streamName, isLive, "HLS", "HLS (TS)")
	addResolvedOutput(outputs, rawOutputs, "DASH", base, streamName, isLive, "DASH")
	addResolvedOutput(outputs, rawOutputs, "HLS_CMAF", base, streamName, isLive, "HLS (CMAF)", "CMAF")
	addResolvedOutput(outputs, rawOutputs, "MP4", base, streamName, isLive, "MP4", "MP4 progressive")
	addResolvedOutput(outputs, rawOutputs, "WEBM", base, streamName, isLive, "WEBM", "EBML", "MKV", "MKV progressive", "WebM progressive")
	addResolvedOutput(outputs, rawOutputs, "AAC", base, streamName, isLive, "AAC", "AAC progressive")
	addResolvedOutput(outputs, rawOutputs, "TS", base, streamName, isLive, "TS", "HTTPTS", "TS HTTP progressive")
	addResolvedOutput(outputs, rawOutputs, "H264", base, streamName, isLive, "H264", "Annex B progressive")
	if !isLive {
		addResolvedOutput(outputs, rawOutputs, "HTTP", base, streamName, isLive, "HTTP")
	}

	secureDefault := strings.HasPrefix(strings.ToLower(base), "https://")
	addWebSocketOutput(outputs, rawOutputs, "MEWS", base, streamName, secureDefault, isLive, "MP4 WebSocket", "MP4")
	addWebSocketOutput(outputs, rawOutputs, "MEWS_WEBM", base, streamName, secureDefault, isLive, "WebM WebSocket", "WEBM", "MKV", "MKV progressive")
	addWebSocketOutput(outputs, rawOutputs, "RAW_WS", base, streamName, secureDefault, isLive, "WSRaw", "WSRAW", "Raw WebSocket")
	addWebSocketOutput(outputs, rawOutputs, "H264_WS", base, streamName, secureDefault, isLive, "Annex B WebSocket", "H264")
	addWebSocketOutput(outputs, rawOutputs, "JSON_WS", base, streamName, secureDefault, isLive, "JSON WebSocket")

	if isLive {
		addDerivedWebSocketOutput(outputs, "RAW_WS", base, streamName, secureDefault, isLive, ".raw")
		addDerivedWebSocketOutput(outputs, "MEWS", base, streamName, secureDefault, isLive, ".mp4")
		addDerivedWebSocketOutput(outputs, "H264_WS", base, streamName, secureDefault, isLive, ".h264")
	}
	addDerivedOutput(outputs, "MP4", base, streamName, isLive, ".mp4")
	addDerivedOutput(outputs, "HLS", base, "hls/"+streamName, isLive, "/index.m3u8")

	directProtocols := []struct {
		protocol string
		keys     []string
	}{
		{protocol: "RTMP", keys: []string{"RTMP"}},
		{protocol: "RTSP", keys: []string{"RTSP"}},
		{protocol: "SRT", keys: []string{"SRT", "TSSRT"}},
		{protocol: "DTSC", keys: []string{"DTSC"}},
	}
	for _, direct := range directProtocols {
		raw, ok := findOutputRaw(rawOutputs, direct.keys...)
		if !ok {
			continue
		}
		if u := ResolveTemplateURLWithHost(raw, base, streamName, publicHost); u != "" {
			outputs[direct.protocol] = &pb.OutputEndpoint{Protocol: direct.protocol, Url: u, Capabilities: BuildOutputCapabilities(direct.protocol, isLive)}
		}
	}

	return outputs
}

// BuildOutputCapabilities returns default capabilities for a given protocol and content type
func BuildOutputCapabilities(protocol string, isLive bool) *pb.OutputCapability {
	caps := &pb.OutputCapability{
		SupportsSeek:          !isLive,
		SupportsQualitySwitch: true,
		HasAudio:              true,
		HasVideo:              true,
	}
	switch strings.ToUpper(protocol) {
	case "WHEP":
		caps.SupportsQualitySwitch = false
		caps.SupportsSeek = false
	case "MP4", "WEBM":
		caps.SupportsQualitySwitch = false
		caps.SupportsSeek = true
	case "AAC", "MP3", "FLAC", "WAV":
		caps.SupportsQualitySwitch = false
		caps.HasVideo = false
	case "H264":
		caps.SupportsQualitySwitch = false
		caps.HasAudio = false
	case "JSON", "JSON_WS":
		caps.SupportsSeek = false
		caps.SupportsQualitySwitch = false
		caps.HasAudio = false
		caps.HasVideo = false
	}
	return caps
}

// DeriveWHEPFromHTML derives a WHEP URL by replacing the trailing .../stream.html with .../webrtc/stream
func DeriveWHEPFromHTML(htmlURL string) string {
	u, err := url.Parse(htmlURL)
	if err != nil {
		return ""
	}
	path := strings.Trim(u.Path, "/")
	parts := strings.Split(path, "/")
	if len(parts) == 0 {
		return ""
	}
	last := parts[len(parts)-1]
	if !strings.HasSuffix(last, ".html") {
		return ""
	}
	stream := strings.TrimSuffix(last, ".html")
	base := parts[:len(parts)-1]
	base = append(base, "webrtc", stream)
	u.Path = "/" + strings.Join(base, "/")
	return u.String()
}

// CalculateGeoDistance calculates distance in km between two lat/lon points using Haversine formula
func CalculateGeoDistance(lat1, lon1, lat2, lon2 float64) float64 {
	const toRad = math.Pi / 180.0
	lat1Rad := lat1 * toRad
	lon1Rad := lon1 * toRad
	lat2Rad := lat2 * toRad
	lon2Rad := lon2 * toRad
	val := math.Sin(lat1Rad)*math.Sin(lat2Rad) + math.Cos(lat1Rad)*math.Cos(lat2Rad)*math.Cos(lon1Rad-lon2Rad)
	if val > 1 {
		val = 1
	}
	if val < -1 {
		val = -1
	}
	angle := math.Acos(val)
	return 6371.0 * angle
}

// DeriveMistHTTPBase converts a base URL to MistServer HTTP base
func DeriveMistHTTPBase(base string) string {
	u, err := url.Parse(base)
	if err != nil || u.Host == "" {
		host := strings.TrimPrefix(base, "http://")
		host = strings.TrimPrefix(host, "https://")
		parts := strings.Split(host, ":")
		hostname := parts[0]
		port := "8080"
		return "http://" + hostname + ":" + port
	}
	hostname := u.Hostname()
	port := u.Port()
	if port == "" || port == "4242" {
		port = "8080"
	}
	return u.Scheme + "://" + hostname + ":" + port
}

// pickBestRemoteArtifact selects the best remote artifact location by geo distance
// to the viewer. Applies a CrossClusterPenalty in the scoring so local edges
// (if they existed) would have been preferred — this function is only called
// when there are zero local nodes with the artifact.
func pickBestRemoteArtifact(hits []*RemoteArtifactInfo, viewerLat, viewerLon float64) *RemoteArtifactInfo {
	if len(hits) == 0 {
		return nil
	}
	var best *RemoteArtifactInfo
	bestDist := math.MaxFloat64
	for _, h := range hits {
		dist := CalculateGeoDistance(viewerLat, viewerLon, h.GeoLat, h.GeoLon)
		if dist < bestDist {
			bestDist = dist
			best = h
		}
	}
	return best
}

func isAuthorizedPeerCluster(clusterID string, peers []*pb.TenantClusterPeer) bool {
	if clusterID == "" {
		return false
	}
	if len(peers) == 0 {
		return false
	}
	for _, peer := range peers {
		if peer.GetClusterId() == clusterID {
			return true
		}
	}
	return false
}

// resolveRemoteArtifact handles cross-cluster artifact resolution by calling
// PrepareArtifact on the origin cluster's Foghorn. If the artifact is ready,
// it creates a local adoption record and triggers defrost from the presigned URLs.
func resolveRemoteArtifact(ctx context.Context, deps *PlaybackDependencies, artifactHash, originClusterID, contentType, tenantID string, clusterPeers []*pb.TenantClusterPeer) (*pb.ViewerEndpointResponse, error) {
	if strings.EqualFold(contentType, "dvr") {
		return nil, fmt.Errorf("DVR archive playback requires a bounded chapter request; use dvrChapter for cross-cluster DVR replay")
	}
	if deps.PeerResolver == nil {
		return nil, fmt.Errorf("peer resolver not available for cross-cluster artifact")
	}
	if !isAuthorizedPeerCluster(originClusterID, clusterPeers) {
		return nil, fmt.Errorf("origin cluster %s is not authorized for tenant", originClusterID)
	}
	addr := deps.PeerResolver.GetPeerAddr(originClusterID)
	if addr == "" {
		return nil, fmt.Errorf("origin cluster %s address unknown", originClusterID)
	}

	resp, err := deps.FedClient.PrepareArtifact(ctx, originClusterID, addr, &pb.PrepareArtifactRequest{
		ArtifactId:        artifactHash,
		RequestingCluster: deps.LocalClusterID,
		ArtifactType:      contentType,
		TenantId:          tenantID,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to prepare artifact from origin cluster: %w", err)
	}

	// Single-hop redirect: when the origin cluster reports the bytes live on
	// a different storage cluster, re-issue PrepareArtifact against that
	// cluster. Chained redirects fail closed — a redirected response that
	// itself carries redirect_cluster_id is rejected to avoid loops.
	// origin_cluster_id stays as the artifact's authoritative producer;
	// storage_cluster_id captures where the bytes actually live.
	storageClusterID := ""
	if redirect := strings.TrimSpace(resp.GetRedirectClusterId()); redirect != "" {
		if redirect == originClusterID || redirect == deps.LocalClusterID {
			return nil, fmt.Errorf("storage redirect loop: origin %s -> %s", originClusterID, redirect)
		}
		if !isAuthorizedPeerCluster(redirect, clusterPeers) {
			return nil, fmt.Errorf("storage redirect cluster %s is not authorized for tenant", redirect)
		}
		redirectAddr := deps.PeerResolver.GetPeerAddr(redirect)
		if redirectAddr == "" {
			return nil, fmt.Errorf("storage redirect cluster %s address unknown", redirect)
		}
		resp, err = deps.FedClient.PrepareArtifact(ctx, redirect, redirectAddr, &pb.PrepareArtifactRequest{
			ArtifactId:        artifactHash,
			RequestingCluster: deps.LocalClusterID,
			ArtifactType:      contentType,
			TenantId:          tenantID,
		})
		if err != nil {
			return nil, fmt.Errorf("failed to prepare artifact from storage cluster %s: %w", redirect, err)
		}
		if chained := strings.TrimSpace(resp.GetRedirectClusterId()); chained != "" {
			return nil, fmt.Errorf("chained storage redirect rejected: %s -> %s -> %s", originClusterID, redirect, chained)
		}
		storageClusterID = redirect
	}

	if resp.GetError() != "" {
		return nil, fmt.Errorf("origin cluster error: %s", resp.GetError())
	}
	if !resp.GetReady() {
		est := resp.GetEstReadySeconds()
		if est == 0 {
			est = 15
		}
		return nil, NewDefrostingError(int(est), "remote artifact being prepared")
	}

	// Adopt the artifact locally (INSERT ON CONFLICT DO NOTHING). When the
	// origin cluster redirected us to a different storage cluster, persist
	// both: origin_cluster_id stays as the producer, storage_cluster_id
	// records where the bytes live so future read paths can resolve the
	// right S3/Chandler without re-walking the redirect.
	if deps.DB != nil {
		storageCluster := sql.NullString{String: storageClusterID, Valid: storageClusterID != ""}
		if _, dbErr := deps.DB.ExecContext(ctx, `
			INSERT INTO foghorn.artifacts (artifact_hash, artifact_type, tenant_id, internal_name, stream_internal_name, format, status, storage_location, sync_status, origin_cluster_id, storage_cluster_id)
			VALUES ($1, $2, $3, $4, $5, $6, 'active', 's3', 'synced', $7, $8)
			ON CONFLICT (artifact_hash) DO UPDATE
			SET storage_location = 's3',
			    sync_status = 'synced',
			    internal_name = CASE WHEN COALESCE(foghorn.artifacts.internal_name, '') = '' AND EXCLUDED.internal_name <> '' THEN EXCLUDED.internal_name ELSE foghorn.artifacts.internal_name END,
			    stream_internal_name = CASE WHEN COALESCE(foghorn.artifacts.stream_internal_name, '') = '' AND EXCLUDED.stream_internal_name <> '' THEN EXCLUDED.stream_internal_name ELSE foghorn.artifacts.stream_internal_name END,
			    format = CASE WHEN COALESCE(foghorn.artifacts.format, '') = '' AND EXCLUDED.format <> '' THEN EXCLUDED.format ELSE foghorn.artifacts.format END,
			    origin_cluster_id = CASE WHEN COALESCE(foghorn.artifacts.origin_cluster_id, '') = '' THEN EXCLUDED.origin_cluster_id ELSE foghorn.artifacts.origin_cluster_id END,
			    storage_cluster_id = CASE WHEN COALESCE(foghorn.artifacts.storage_cluster_id, '') = '' AND EXCLUDED.storage_cluster_id IS NOT NULL THEN EXCLUDED.storage_cluster_id ELSE foghorn.artifacts.storage_cluster_id END
		`, artifactHash, contentType, tenantID, resp.GetInternalName(), resp.GetStreamInternalName(), resp.GetFormat(), originClusterID, storageCluster); dbErr != nil {
			// Adoption is best-effort — defrost can still proceed using the
			// presigned URLs we already have. Failing the playback request
			// would be worse than serving a one-off defrost without a
			// persisted lifecycle row.
			controlLogger().WithError(dbErr).WithFields(logging.Fields{
				"artifact_hash":      artifactHash,
				"tenant_id":          tenantID,
				"origin_cluster_id":  originClusterID,
				"storage_cluster_id": storageClusterID,
			}).Warn("resolveRemoteArtifact: adoption upsert failed; subsequent reads will re-walk PrepareArtifact")
		}
	}

	// Trigger local defrost using the origin cluster's presigned URLs
	nodeID, err := pickStorageNodeID()
	if err != nil {
		return nil, fmt.Errorf("no local storage node for remote artifact defrost: %w", err)
	}
	if _, err := StartRemoteDefrost(ctx, contentType, artifactHash, nodeID, 30*time.Second, controlLogger(), resp.GetUrl(), resp.GetSegmentUrls()); err != nil {
		if defrostErr, ok := errors.AsType[*DefrostingError](err); ok {
			return nil, defrostErr
		}
		return nil, fmt.Errorf("failed to start remote artifact defrost: %w", err)
	}
	return nil, NewDefrostingError(10, "remote artifact defrost started")
}
