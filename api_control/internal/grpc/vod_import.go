package grpc

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"path"
	"strings"
	"time"

	"frameworks/api_control/internal/database/commodoredb"
	fwdb "github.com/Livepeer-FrameWorks/monorepo/pkg/database"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/grpcutil"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	commodorepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"
	sharedpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/shared"
	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const maxVODImportURLBytes = 4096

// importVideoExtensions are the containers MistServer can read over HTTP from
// the Helmsman relay (triggers.isRelaySafeFormat). Wrappers it can only open
// from a local file (avi, flv, m4v) need an uploaded copy and are refused.
var importVideoExtensions = map[string]bool{"mp4": true, "mov": true, "mkv": true, "webm": true, "ts": true}

// ImportVodAsset imports a video from a public http(s) URL as a VOD asset. It
// registers the asset with its creation intent like CreateVodUpload and has
// the tenant's Foghorn record the source and queue processing; the processing
// node's Helmsman relay fetches the source.
func (s *CommodoreServer) ImportVodAsset(ctx context.Context, req *sharedpb.ImportVodAssetRequest) (*sharedpb.ImportVodAssetResponse, error) {
	userID, tenantID, err := extractUserContext(ctx)
	if err != nil {
		return nil, err
	}
	source, err := parseVODImportSource(req.GetSourceUrl())
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid import URL: %v", err)
	}
	filename, err := vodImportFilename(req.GetFilename(), source)
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	if validationErr := validatePublicDestinationHost(ctx, s.webhookDestinationPolicy, source); validationErr != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid import URL: %v", validationErr)
	}

	foghornClient, vodRoute, err := s.resolveFoghornForTenant(ctx, tenantID)
	if err != nil {
		return nil, err
	}
	if suspended, suspendErr := s.isTenantSuspended(ctx, tenantID); suspendErr != nil {
		s.logger.WithError(suspendErr).Warn("Failed to check tenant suspension status")
	} else if suspended {
		return nil, status.Error(codes.PermissionDenied, "account suspended - please top up your balance to import videos")
	}

	vodHash, err := generateVodHash()
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to generate VOD hash: %v", err)
	}
	vodID := uuid.New().String()
	artifactInternalName, playbackID, err := s.generateUniqueArtifactIdentifiers(ctx)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to generate VOD identifiers: %v", err)
	}
	resolvedDays, retentionErr := s.resolveInitialRetention(ctx, commodorepb.MediaRetentionTarget_MEDIA_RETENTION_TARGET_VOD, tenantID, "")
	if retentionErr != nil {
		s.logger.WithError(retentionErr).WithField("tenant_id", tenantID).Warn("VOD retention resolution failed; falling back to 30-day horizon for safety")
		resolvedDays = 30
	}
	var retentionUntil sql.NullTime
	if resolvedDays > 0 {
		retentionUntil = sql.NullTime{Valid: true, Time: time.Now().UTC().Add(time.Duration(resolvedDays) * 24 * time.Hour)}
	}

	// The asset row and its creation intent commit together before the Foghorn
	// call, as for an upload, so the intent sweep converges an import whose
	// accept response was lost.
	intentRequestID := uuid.New().String()
	regErr := fwdb.WithRetryablePostgresTx(ctx, s.db, nil, func(tx *sql.Tx) error {
		if execErr := commodoredb.New(tx).InsertVODUploadRegistration(ctx, commodoredb.InsertVODUploadRegistrationParams{
			ID:              vodID,
			TenantID:        tenantID,
			UserID:          userID,
			VodHash:         vodHash,
			InternalName:    artifactInternalName,
			PlaybackID:      playbackID,
			Title:           sql.NullString{String: req.GetTitle(), Valid: true},
			Description:     sql.NullString{String: req.GetDescription(), Valid: true},
			Filename:        filename,
			OriginClusterID: sql.NullString{String: vodRoute.clusterID, Valid: true},
			RetentionUntil:  retentionUntil,
		}); execErr != nil {
			return execErr
		}
		persisted, upErr := upsertCreationIntent(ctx, tx, tenantID, creationIntentKindVOD, vodHash, intentRequestID, vodRoute.clusterID, nil)
		if upErr != nil {
			return upErr
		}
		intentRequestID = persisted
		return nil
	})
	if regErr != nil {
		s.logger.WithError(regErr).WithField("tenant_id", tenantID).Error("Failed to register imported VOD asset + creation intent")
		return nil, status.Errorf(codes.Internal, "failed to register VOD asset: %v", regErr)
	}

	processesJSON := s.resolveProcessesJSON(ctx, tenantID, "", vodRoute.clusterID, "vod")
	foghornReq := &sharedpb.ImportVodAssetRequest{
		TenantId:      tenantID,
		UserId:        userID,
		SourceUrl:     source.String(),
		Filename:      filename,
		Title:         req.Title,
		Description:   req.Description,
		VodHash:       &vodHash,
		PlaybackId:    &playbackID,
		InternalName:  &artifactInternalName,
		ClusterId:     vodRoute.clusterID,
		RetentionDays: &resolvedDays,
		RequestId:     &intentRequestID,
		Actor:         s.requestActor(ctx),
		ProcessesJson: &processesJSON,
	}
	resp, trailers, err := foghornClient.ImportVodAsset(ctx, foghornReq)
	if err != nil {
		s.logger.WithError(err).WithField("vod_hash", vodHash).Error("Failed to import VOD via Foghorn")
		if creationCreateErrorIsDefinitive(err) {
			if abErr := s.abortCreationIntent(context.Background(),
				creationIntentRow{tenantID: tenantID, kind: creationIntentKindVOD, artifactHash: vodHash, originClusterID: vodRoute.clusterID},
				"", "foghorn rejected vod import", true); abErr != nil && !errors.Is(abErr, errIntentCASMiss) {
				s.logger.WithError(abErr).WithField("vod_hash", vodHash).Warn("Failed to abort VOD import creation intent")
			}
		}
		return nil, grpcutil.PropagateError(ctx, err, trailers)
	}
	if cErr := s.commitCreationIntent(ctx,
		creationIntentRow{tenantID: tenantID, kind: creationIntentKindVOD, artifactHash: vodHash, originClusterID: vodRoute.clusterID},
		"", nil); cErr != nil && !errors.Is(cErr, errIntentCASMiss) {
		s.logger.WithError(cErr).WithField("vod_hash", vodHash).Warn("Failed to mark VOD import creation intent committed")
	}
	if asset := resp.GetAsset(); asset != nil {
		asset.PlaybackId = &playbackID
	}
	s.logger.WithFields(logging.Fields{
		"tenant_id":   tenantID,
		"vod_hash":    vodHash,
		"source_host": source.Host,
	}).Info("Accepted VOD import")
	return resp, nil
}

// parseVODImportSource accepts an http or https URL without credentials. The
// query is kept (presigned links need it); the fragment is never sent.
func parseVODImportSource(raw string) (*url.URL, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, errors.New("url is required")
	}
	if len(raw) > maxVODImportURLBytes {
		return nil, fmt.Errorf("url is longer than %d bytes", maxVODImportURLBytes)
	}
	u, err := url.Parse(raw)
	if err != nil {
		return nil, errors.New("url does not parse")
	}
	if scheme := strings.ToLower(u.Scheme); scheme != "http" && scheme != "https" {
		return nil, errors.New("scheme must be https or http")
	}
	if u.User != nil {
		return nil, errors.New("credentials in the url are not allowed")
	}
	if u.Hostname() == "" {
		return nil, errors.New("url has no host")
	}
	u.Fragment = ""
	return u, nil
}

// vodImportFilename returns the requested filename, else the URL path's base
// name. Its extension selects MistServer's input, so it must be one the relay
// can serve.
func vodImportFilename(requested string, source *url.URL) (string, error) {
	name := strings.TrimSpace(requested)
	if name == "" {
		name = source.Path
	}
	name = path.Base(strings.ReplaceAll(name, "\\", "/"))
	ext := strings.TrimPrefix(strings.ToLower(path.Ext(name)), ".")
	if name == "." || name == "/" || !importVideoExtensions[ext] {
		return "", errors.New("a filename with a video extension (mp4, mov, mkv, webm, ts) is required when the URL path has none")
	}
	if len(name) > 255 {
		return "", errors.New("filename is longer than 255 characters")
	}
	return name, nil
}
