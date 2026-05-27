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
	if db == nil || s3Client == nil {
		resp.Error = "foghorn not configured for relay resolve"
		return
	}
	var (
		s3URL      string
		sizeBytes  sql.NullInt64
		format     sql.NullString
		dtshSynced sql.NullBool
		streamName sql.NullString
		syncStatus sql.NullString
	)
	err := db.QueryRowContext(ctx, `
		SELECT COALESCE(s3_url,''), size_bytes, format, dtsh_synced, stream_internal_name, sync_status
		FROM foghorn.artifacts
		WHERE artifact_hash = $1 AND status != 'deleted'
		LIMIT 1
	`, req.GetAssetHash()).Scan(&s3URL, &sizeBytes, &format, &dtshSynced, &streamName, &syncStatus)
	if errors.Is(err, sql.ErrNoRows) {
		return
	}
	if err != nil {
		resp.Error = "db lookup failed"
		logger.WithError(err).WithField("asset_hash", req.GetAssetHash()).Warn("RelayResolve DB lookup failed")
		return
	}
	if s3URL == "" {
		// Artifact exists but isn't in S3 yet (warm-only). Relay can't fetch;
		// caller falls through to alternate resolution (warm-node lookup).
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
	// warm-node lookup.
	if !syncStatus.Valid || syncStatus.String != "synced" {
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
