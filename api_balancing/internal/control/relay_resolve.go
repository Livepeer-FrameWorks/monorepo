// Read-through artifact relay resolution.
//
// Helmsman's /internal/artifact/* HTTP relay encodes only (kind, hash,
// ext) in the URL — to serve bytes it needs the durable S3 source,
// optional .dtsh sidecar URLs, and the expected size.
// RelayResolveRequest is the on-demand pull that delivers these.

package control

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/auth"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	pb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// relayURLTTL is the presigned-URL lifetime Helmsman caches. Picked larger
// than the relay's own refresh window (url_ttl_seconds * 0.8) so a single
// resolve survives normal playback without thrashing.
const relayURLTTL = 1 * time.Hour

// processRelayResolveRequest looks up the asset Helmsman is about to
// serve and responds with presigned URLs + metadata. State on the
// response disambiguates SOURCE_MISSING (404 at the relay) and
// FREEZING (503 + Retry-After) from PLAYABLE. Supported asset kinds:
// vod, clip, upload. Finalized DVR chapters resolve as normal VOD
// artifacts (their .mkv has its own foghorn.artifacts row); the
// rolling-DVR surface (dvr+<dvr_internal_name>) plays directly from
// the recording origin and never goes through RelayResolve.
func processRelayResolveRequest(req *pb.RelayResolveRequest, nodeID string, stream pb.HelmsmanControl_ConnectServer, logger logging.Logger) {
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()

	requestID := req.GetRequestId()
	resp := &pb.RelayResolveResponse{
		RequestId: requestID,
		AssetHash: req.GetAssetHash(),
		State:     pb.AssetState_ASSET_STATE_SOURCE_MISSING,
	}

	switch req.GetAssetKind() {
	case "vod", "clip":
		fillFileArtifactResolve(ctx, req, resp, logger)
	case "upload":
		fillUploadResolve(ctx, req, resp, logger)
	default:
		resp.Error = fmt.Sprintf("unknown asset_kind %q", req.GetAssetKind())
	}
	_ = nodeID

	sendRelayResolveResponse(stream, resp, logger)
}

func fillFileArtifactResolve(ctx context.Context, req *pb.RelayResolveRequest, resp *pb.RelayResolveResponse, logger logging.Logger) {
	if db == nil {
		resp.Error = "foghorn not configured for relay resolve"
		return
	}
	// s3Client is checked at the call sites that need it (presigned URL
	// minting in the synced branch, cross-cluster fallback). The
	// peer-relay branch is DB + JWT only — gating that on s3Client
	// would defeat the no-S3-wait intent.
	var (
		s3URL            string
		sizeBytes        sql.NullInt64
		format           sql.NullString
		dtshSynced       sql.NullBool
		streamName       sql.NullString
		syncStatus       sql.NullString
		originClusterID  sql.NullString
		storageClusterID sql.NullString
		tenantID         sql.NullString
		artifactType     sql.NullString
	)
	err := db.QueryRowContext(ctx, `
		SELECT COALESCE(s3_url,''), size_bytes, format, dtsh_synced, stream_internal_name, sync_status,
		       origin_cluster_id, storage_cluster_id, tenant_id, artifact_type
		FROM foghorn.artifacts
		WHERE artifact_hash = $1 AND status != 'deleted'
		LIMIT 1
	`, req.GetAssetHash()).Scan(&s3URL, &sizeBytes, &format, &dtshSynced, &streamName, &syncStatus,
		&originClusterID, &storageClusterID, &tenantID, &artifactType)
	if errors.Is(err, sql.ErrNoRows) {
		// Direct-dial case: no prior playback adoption, no local row.
		// Ask Commodore who owns this artifact, federate to that
		// cluster's Foghorn for a presigned read URL.
		fillCrossClusterArtifactFromCommodore(ctx, req, resp, logger)
		return
	}
	if err != nil {
		resp.Error = "db lookup failed"
		logger.WithError(err).WithField("asset_hash", req.GetAssetHash()).Warn("RelayResolve DB lookup failed")
		return
	}
	if s3URL == "" {
		// Row was adopted (typically by playback.go's resolveRemoteArtifact)
		// but the bytes aren't on local S3. Two recovery paths apply in
		// order:
		//   1. Local origin node has the canonical file on disk (hot but
		//      unsynced) — serve via peer-relay JWT pointing at that node.
		//   2. Bytes live on a peer cluster — federate via PrepareArtifact.
		// Path (1) covers the case where the artifact was finalized here
		// recently and the S3 sync just hasn't landed; without it the row
		// would 404 via the cross-cluster path's "originClusterID ==
		// LocalClusterID → ErrCrossClusterArtifactUnavailable" check.
		if fillPeerRelayFromLocalOrigin(ctx, req, resp, sizeBytes, format, streamName, logger) {
			return
		}
		peerCluster := strings.TrimSpace(storageClusterID.String)
		if peerCluster == "" {
			peerCluster = strings.TrimSpace(originClusterID.String)
		}
		if peerCluster != "" {
			fillCrossClusterArtifact(ctx, req, resp, logger, peerCluster, tenantID.String, artifactType.String)
		}
		return
	}
	// Post-processing race: when a job rewrites an artifact to a new
	// container, format/size/sync_status flip immediately but s3_url
	// keeps pointing at the *original upload* until the new file is
	// durably synced (server.go's "Keep original upload URL in s3_url
	// until the replacement upload is durably synced"). A resolving
	// node without a local processed copy would otherwise stream the
	// stale upload — wrong codec, wrong size, wrong contents. Gate S3
	// authority on sync_status='synced'; anything else falls through to
	// peer-relay fallback then warm-node lookup.
	if !syncStatus.Valid || syncStatus.String != "synced" {
		// Peer-relay fallback: an origin node in this cluster may hold
		// the canonical full file locally even though S3 sync is
		// pending. Hand the requester a node-specific peer URL with a
		// short-lived JWT instead of falling through to 404. When no
		// local origin row qualifies, return empty — the resolver path
		// MUST NOT chain to another peer URL (recursion invariant).
		if fillPeerRelayFromLocalOrigin(ctx, req, resp, sizeBytes, format, streamName, logger) {
			return
		}
		return
	}

	if s3Client == nil {
		resp.Error = "s3 client not configured"
		return
	}
	mediaURL, err := GeneratePresignedGETForArtifact(ctx, s3URL)
	if err != nil {
		resp.Error = "mint media presigned"
		logger.WithError(err).Warn("RelayResolve mint media presigned failed")
		return
	}
	resp.State = pb.AssetState_ASSET_STATE_PLAYABLE
	resp.MediaPresignedUrl = mediaURL
	resp.UrlTtlSeconds = int64(relayURLTTL.Seconds())

	if sizeBytes.Valid && sizeBytes.Int64 > 0 {
		resp.ExpectedSizeBytes = uint64(sizeBytes.Int64)
	}
	if format.Valid {
		resp.ContentType = contentTypeForFormat(format.String)
	}

	// Sidecar S3 key follows the <media_key>.dtsh convention written by
	// freeze and by the relay's direct .dtsh PUT. When the artifacts row
	// reports dtsh_synced=true, mint a GET for it; otherwise omit so the
	// relay returns 404 and Mist generates+PUTs a new one. PUT URL is
	// always minted so externalWriter can persist freshly-generated
	// sidecars.
	if dtshSynced.Valid && dtshSynced.Bool {
		if u, mintErr := generateDtshPresignedGET(s3URL, relayURLTTL); mintErr == nil {
			resp.DtshPresignedGet = u
		}
	}
	if putURL, err := generateDtshPresignedPUT(s3URL, relayURLTTL); err == nil {
		resp.DtshPresignedPut = putURL
	}
	// Clips nest under storage/clips/<stream_internal_name>/<hash>.<ext>
	// when the clip writer knows the source stream; passing the stream
	// name lets the relay probe that path before falling back to flat.
	if streamName.Valid {
		resp.StreamInternalName = streamName.String
	}

	resp.PolicyHint = pb.RelayResolveResponse_CACHE_HINT_PREFER_DISK
}

func fillUploadResolve(ctx context.Context, req *pb.RelayResolveRequest, resp *pb.RelayResolveResponse, logger logging.Logger) {
	if db == nil || s3Client == nil {
		resp.Error = "foghorn not configured for relay resolve"
		return
	}
	// Uploaded VOD ingest metadata lives in foghorn.vod_metadata, keyed by
	// the same artifact_hash assigned at multipart-upload finalization.
	var s3Key sql.NullString
	err := db.QueryRowContext(ctx, `
		SELECT s3_key
		FROM foghorn.vod_metadata
		WHERE artifact_hash = $1
		LIMIT 1
	`, req.GetAssetHash()).Scan(&s3Key)
	if errors.Is(err, sql.ErrNoRows) || !s3Key.Valid || s3Key.String == "" {
		// Direct-dial: no local upload metadata. Source artifact for
		// the processing input might be on a peer cluster — federate
		// via Commodore lookup + PrepareArtifact.
		fillCrossClusterArtifactFromCommodore(ctx, req, resp, logger)
		return
	}
	if err != nil {
		resp.Error = "db lookup failed"
		logger.WithError(err).WithField("asset_hash", req.GetAssetHash()).Warn("RelayResolve upload lookup failed")
		return
	}
	mediaURL, err := s3Client.GeneratePresignedGET(s3Key.String, relayURLTTL)
	if err != nil {
		resp.Error = "mint upload presigned"
		logger.WithError(err).Warn("RelayResolve mint upload presigned failed")
		return
	}
	resp.State = pb.AssetState_ASSET_STATE_PLAYABLE
	resp.MediaPresignedUrl = mediaURL
	resp.UrlTtlSeconds = int64(relayURLTTL.Seconds())
	resp.PolicyHint = pb.RelayResolveResponse_CACHE_HINT_PREFER_MEM
	if putURL, err := s3Client.GeneratePresignedPUT(s3Key.String+".dtsh", relayURLTTL); err == nil {
		resp.DtshPresignedPut = putURL
	}
}

// fillPeerRelayFromLocalOrigin attempts to construct a peer-relay URL
// pointing at a local-cluster origin node that holds the canonical
// full file for the requested artifact. Returns true and populates
// resp.PeerRelayUrl + PeerRelayAuthToken on success.
//
// The query gate is strict: role='origin', is_complete=true,
// is_orphaned=false, and recently-seen. is_complete is
// writer-authoritative — only finalizer RPCs flip it true, so the row
// reflects "this node's disk definitely holds the entire file" and
// not a sparse cache.
//
// Recursion invariant: this path returns a URL that the requester
// will hit directly; it never delegates to another peer relay.
func fillPeerRelayFromLocalOrigin(
	ctx context.Context,
	req *pb.RelayResolveRequest,
	resp *pb.RelayResolveResponse,
	sizeBytes sql.NullInt64,
	format sql.NullString,
	streamName sql.NullString,
	logger logging.Logger,
) bool {
	secret := GetArtifactRelaySecret()
	if len(secret) == 0 || db == nil {
		return false
	}
	var (
		originNodeID string
		baseURL      string
	)
	// COALESCE base_url from node_outputs because RegisterOriginArtifact
	// doesn't write per-row base_url (the StoredArtifact heartbeat
	// proto is per-artifact, not per-node). node_outputs is the
	// canonical per-node URL store updated by NodeLifecycle. Without
	// the JOIN, processed VOD output + DVR chapter VOD rows would
	// have empty base_url here and silently fall through to S3.
	//
	// last_seen_at > NOW() - 90s is the active-node freshness rule —
	// stricter than the 10min orphan threshold on purpose: peer-relay
	// has to dial the node immediately, so we want recent liveness,
	// not "hasn't been declared dead yet." A short heartbeat stall
	// produces a brief false negative; viewer falls back to S3 (synced)
	// or 503 (unsynced), and the next attempt recovers as soon as the
	// heartbeat resumes.
	err := db.QueryRowContext(ctx, `
		SELECT an.node_id, COALESCE(NULLIF(an.base_url, ''), no.base_url, '')
		FROM foghorn.artifact_nodes an
		LEFT JOIN foghorn.node_outputs no ON no.node_id = an.node_id
		WHERE an.artifact_hash = $1
		  AND an.role = 'origin'
		  AND an.is_complete = true
		  AND an.is_orphaned = false
		  AND an.last_seen_at > NOW() - INTERVAL '90 seconds'
		ORDER BY an.last_seen_at DESC
		LIMIT 1
	`, req.GetAssetHash()).Scan(&originNodeID, &baseURL)
	if errors.Is(err, sql.ErrNoRows) {
		return false
	}
	if err != nil {
		logger.WithError(err).WithField("asset_hash", req.GetAssetHash()).Warn("RelayResolve peer-relay lookup failed")
		return false
	}
	baseURL = strings.TrimSpace(baseURL)
	if baseURL == "" {
		return false
	}
	// Build path that mirrors the origin Helmsman's route layout:
	// flat for vod/upload, nested under stream for clip.
	ext := strings.TrimPrefix(req.GetExt(), ".")
	if ext == "" && format.Valid {
		ext = strings.TrimPrefix(format.String, ".")
	}
	if ext == "" {
		return false
	}
	var path string
	switch req.GetAssetKind() {
	case "clip":
		stream := strings.TrimSpace(streamName.String)
		if stream == "" {
			return false
		}
		path = "/internal/artifact/clip/" + stream + "/" + req.GetAssetHash() + "." + ext
	case "vod", "upload":
		path = "/internal/artifact/" + req.GetAssetKind() + "/" + req.GetAssetHash() + "." + ext
	default:
		return false
	}
	peerURL := strings.TrimRight(baseURL, "/") + path
	token, exp, mintErr := auth.GenerateArtifactRelayJWT(
		originNodeID, req.GetAssetHash(), path,
		localClusterID, localClusterID,
		0, secret,
	)
	if mintErr != nil {
		logger.WithError(mintErr).Warn("RelayResolve mint peer-relay token failed")
		return false
	}
	resp.State = pb.AssetState_ASSET_STATE_PLAYABLE
	resp.PeerRelayUrl = peerURL
	resp.PeerRelayAuthToken = token
	// TTL slightly under JWT exp so the relay refreshes before the
	// token expires mid-fetch.
	ttl := max(time.Until(exp)-30*time.Second, time.Minute)
	resp.UrlTtlSeconds = int64(ttl.Seconds())
	if sizeBytes.Valid && sizeBytes.Int64 > 0 {
		resp.ExpectedSizeBytes = uint64(sizeBytes.Int64)
	}
	if format.Valid {
		resp.ContentType = contentTypeForFormat(format.String)
	}
	if streamName.Valid {
		resp.StreamInternalName = streamName.String
	}
	resp.PolicyHint = pb.RelayResolveResponse_CACHE_HINT_PREFER_DISK
	return true
}

func sendRelayResolveResponse(stream pb.HelmsmanControl_ConnectServer, resp *pb.RelayResolveResponse, logger logging.Logger) {
	msg := &pb.ControlMessage{
		RequestId: resp.GetRequestId(),
		SentAt:    timestamppb.Now(),
		Payload:   &pb.ControlMessage_RelayResolveResponse{RelayResolveResponse: resp},
	}
	if err := stream.Send(msg); err != nil {
		logger.WithError(err).WithField("asset_hash", resp.GetAssetHash()).Warn("Failed to send RelayResolveResponse")
	}
}

// generateDtshPresignedPUT builds a sidecar PUT URL alongside the artifact's
// media key. The sidecar is stored at <media_key>.dtsh; freeze and the
// relay's direct .dtsh PUT both target this key.
func generateDtshPresignedPUT(mediaS3URL string, expiry time.Duration) (string, error) {
	if s3Client == nil {
		return "", fmt.Errorf("s3 client not configured")
	}
	key, err := keyFromMediaS3URL(mediaS3URL)
	if err != nil {
		return "", err
	}
	return s3Client.GeneratePresignedPUT(key+".dtsh", expiry)
}

// generateDtshPresignedGET mirrors the PUT helper for sidecar reads.
// Only called when foghorn.artifacts.dtsh_synced=true on the artifact row,
// so the key is known to exist in S3.
func generateDtshPresignedGET(mediaS3URL string, expiry time.Duration) (string, error) {
	if s3Client == nil {
		return "", fmt.Errorf("s3 client not configured")
	}
	key, err := keyFromMediaS3URL(mediaS3URL)
	if err != nil {
		return "", err
	}
	return s3Client.GeneratePresignedGET(key+".dtsh", expiry)
}

func keyFromMediaS3URL(mediaS3URL string) (string, error) {
	if strings.HasPrefix(mediaS3URL, "s3://") {
		return s3Client.ParseS3URL(mediaS3URL)
	}
	return mediaS3URL, nil
}

// fillCrossClusterArtifactFromCommodore handles direct-dial reads
// where no local foghorn.artifacts row exists. Asks Commodore for the
// artifact's origin cluster, then federates via PrepareArtifact. The
// peer cluster's Foghorn mints the presigned URL using its own S3
// credentials; relay's block cache reads from peer S3 transparently.
//
// Hash IS the artifact internal name for vod+ and chapter artifacts
// (the runtime stream name vod+<hash> uses the hash directly). For
// other shapes ResolveArtifactInternalName returns Found=false; caller
// falls through to a 404 response.
func fillCrossClusterArtifactFromCommodore(ctx context.Context, req *pb.RelayResolveRequest, resp *pb.RelayResolveResponse, logger logging.Logger) {
	if CommodoreClient == nil {
		return
	}
	commodoreResp, err := CommodoreClient.ResolveArtifactInternalName(ctx, req.GetAssetHash())
	if err != nil || commodoreResp == nil || !commodoreResp.GetFound() {
		return
	}
	originCluster := commodoreResp.GetOriginClusterId()
	if originCluster == "" {
		return
	}
	fillCrossClusterArtifact(ctx, req, resp, logger, originCluster, commodoreResp.GetTenantId(), commodoreResp.GetContentType())
}

// fillCrossClusterArtifact federates to a known peer cluster for a
// presigned read URL. Used both when an adopted row points at a peer
// (storage_cluster_id set, no local s3_url) and when no local row
// exists at all (direct-dial via fillCrossClusterArtifactFromCommodore).
func fillCrossClusterArtifact(ctx context.Context, req *pb.RelayResolveRequest, resp *pb.RelayResolveResponse, logger logging.Logger, peerClusterID, tenantID, artifactType string) {
	result, err := ResolveCrossClusterArtifactURL(ctx, req.GetAssetHash(), artifactType, tenantID, peerClusterID)
	if err != nil || result == nil {
		// Includes ErrCrossClusterArtifactUnavailable (deps unwired,
		// peer unreachable, peer doesn't know the artifact). Silent —
		// relay falls through to 404, same as today's miss.
		return
	}
	resp.State = pb.AssetState_ASSET_STATE_PLAYABLE
	// Origin may have returned an S3 presigned URL (synced case) OR a
	// peer-relay URL + token (hot-but-unsynced case). Forward whichever
	// the requesting Helmsman should hit. Helmsman's fetcher will set
	// Authorization: Bearer when the token field is non-empty.
	if result.PeerRelayURL != "" {
		resp.PeerRelayUrl = result.PeerRelayURL
		resp.PeerRelayAuthToken = result.PeerRelayToken
	} else {
		resp.MediaPresignedUrl = result.URL
	}
	// Peer's presigned URL has its own S3 expiry. We don't know it
	// precisely; use the local TTL so the relay refreshes via
	// re-resolve before peer's URL expires (peer mints a fresh one
	// on the next RelayResolve).
	resp.UrlTtlSeconds = int64(relayURLTTL.Seconds())
	if artifactType == "vod" || artifactType == "clip" {
		// vod/clip implies a single media file — no special hint.
		resp.PolicyHint = pb.RelayResolveResponse_CACHE_HINT_PREFER_DISK
	}
	if result.Format != "" {
		resp.ContentType = contentTypeForFormat(result.Format)
	}
	if result.SizeBytes > 0 {
		// Block cache needs total size up-front to plan range splits;
		// without it, the relay degrades to no-cache pass-through.
		resp.ExpectedSizeBytes = result.SizeBytes
	}
	logger.WithFields(logging.Fields{
		"asset_hash":         req.GetAssetHash(),
		"peer_cluster":       peerClusterID,
		"storage_cluster_id": result.StorageClusterID,
		"peer_relay":         result.PeerRelayURL != "",
	}).Debug("RelayResolve: federated URL from peer cluster")
}

// contentTypeForFormat is a tiny map from artifact format strings to MIME
// types so the relay can echo a usable Content-Type on first response.
func contentTypeForFormat(format string) string {
	switch strings.ToLower(strings.TrimPrefix(format, ".")) {
	case "mp4", "mov", "m4v":
		return "video/mp4"
	case "mkv":
		return "video/x-matroska"
	case "webm":
		return "video/webm"
	case "ts", "m2ts":
		return "video/mp2t"
	case "m3u8", "m3u":
		return "application/vnd.apple.mpegurl"
	default:
		return ""
	}
}
