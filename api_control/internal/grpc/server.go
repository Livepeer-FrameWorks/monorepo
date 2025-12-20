package grpc

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/smtp"
	"net/url"
	"os"
	"strings"
	"time"

	"frameworks/pkg/auth"
	foghornclient "frameworks/pkg/clients/foghorn"
	"frameworks/pkg/clients/listmonk"
	purserclient "frameworks/pkg/clients/purser"
	qmclient "frameworks/pkg/clients/quartermaster"
	"frameworks/pkg/config"
	"frameworks/pkg/logging"
	"frameworks/pkg/middleware"
	"frameworks/pkg/pagination"
	pb "frameworks/pkg/proto"
	"frameworks/pkg/turnstile"

	"github.com/google/uuid"
	"github.com/lib/pq"
	"github.com/prometheus/client_golang/prometheus"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/health"
	"google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// botProtectionRequest interface for requests with bot protection fields
type botProtectionRequest interface {
	GetPhoneNumber() string
	GetHumanCheck() string
	GetBehavior() *pb.BehaviorData
}

// validateBehavior checks behavioral signals (fallback when Turnstile not configured)
func validateBehavior(req botProtectionRequest) bool {
	// Honeypot: phone_number should be empty
	if req.GetPhoneNumber() != "" {
		return false
	}
	// Human checkbox
	if req.GetHumanCheck() != "human" {
		return false
	}
	// Timing and interaction
	b := req.GetBehavior()
	if b == nil {
		return false
	}
	timeSpent := b.GetSubmittedAt() - b.GetFormShownAt()
	if timeSpent < 3000 || timeSpent > 30*60*1000 {
		return false
	}
	if !b.GetMouse() && !b.GetTyped() {
		return false
	}
	return true
}

// ServerMetrics holds Prometheus metrics for the gRPC server
type ServerMetrics struct {
	AuthOperations   *prometheus.CounterVec
	AuthDuration     *prometheus.HistogramVec
	ActiveSessions   *prometheus.GaugeVec
	StreamOperations *prometheus.CounterVec
}

// CommodoreServer implements the Commodore gRPC services
type CommodoreServer struct {
	pb.UnimplementedInternalServiceServer
	pb.UnimplementedUserServiceServer
	pb.UnimplementedStreamServiceServer
	pb.UnimplementedStreamKeyServiceServer
	pb.UnimplementedDeveloperServiceServer
	pb.UnimplementedClipServiceServer
	pb.UnimplementedDVRServiceServer
	pb.UnimplementedViewerServiceServer
	pb.UnimplementedVodServiceServer
	db                   *sql.DB
	logger               logging.Logger
	foghornClient        *foghornclient.GRPCClient
	quartermasterClient  *qmclient.GRPCClient
	purserClient         *purserclient.GRPCClient
	listmonkClient       *listmonk.Client
	defaultMailingListID int
	metrics              *ServerMetrics
	turnstileValidator   *turnstile.Validator
	passwordResetSecret  []byte // Secret for HMAC signing of password reset tokens
}

// CommodoreServerConfig contains all dependencies for CommodoreServer
type CommodoreServerConfig struct {
	DB                   *sql.DB
	Logger               logging.Logger
	FoghornClient        *foghornclient.GRPCClient
	QuartermasterClient  *qmclient.GRPCClient
	PurserClient         *purserclient.GRPCClient
	ListmonkClient       *listmonk.Client
	DefaultMailingListID int
	Metrics              *ServerMetrics
	// Auth config for gRPC interceptor
	ServiceToken string
	JWTSecret    []byte
	// Bot protection
	TurnstileSecretKey string
	// Password reset token signing
	PasswordResetSecret []byte
}

// NewCommodoreServer creates a new Commodore gRPC server
func NewCommodoreServer(cfg CommodoreServerConfig) *CommodoreServer {
	var tv *turnstile.Validator
	if cfg.TurnstileSecretKey != "" {
		tv = turnstile.NewValidator(cfg.TurnstileSecretKey)
	}
	return &CommodoreServer{
		db:                   cfg.DB,
		logger:               cfg.Logger,
		foghornClient:        cfg.FoghornClient,
		quartermasterClient:  cfg.QuartermasterClient,
		purserClient:         cfg.PurserClient,
		listmonkClient:       cfg.ListmonkClient,
		defaultMailingListID: cfg.DefaultMailingListID,
		metrics:              cfg.Metrics,
		turnstileValidator:   tv,
		passwordResetSecret:  cfg.PasswordResetSecret,
	}
}

// recordAuthOp records an authentication operation metric
func (s *CommodoreServer) recordAuthOp(operation, status string, duration time.Duration) {
	if s.metrics == nil {
		return
	}
	if s.metrics.AuthOperations != nil {
		s.metrics.AuthOperations.WithLabelValues(operation, status).Inc()
	}
	if s.metrics.AuthDuration != nil {
		s.metrics.AuthDuration.WithLabelValues(operation).Observe(duration.Seconds())
	}
}

// recordStreamOp records a stream operation metric
func (s *CommodoreServer) recordStreamOp(operation, status string) {
	if s.metrics == nil || s.metrics.StreamOperations == nil {
		return
	}
	s.metrics.StreamOperations.WithLabelValues(operation, status).Inc()
}

// ============================================================================
// INTERNAL SERVICE (Foghorn, Decklog → Commodore)
// ============================================================================

// ValidateStreamKey validates a stream key for RTMP ingest (called by Foghorn on PUSH_REWRITE)
func (s *CommodoreServer) ValidateStreamKey(ctx context.Context, req *pb.ValidateStreamKeyRequest) (*pb.ValidateStreamKeyResponse, error) {
	streamKey := req.GetStreamKey()
	if streamKey == "" {
		return &pb.ValidateStreamKeyResponse{
			Valid: false,
			Error: "stream_key required",
		}, nil
	}

	var streamID, userID, tenantID, internalName string
	var isActive, isRecordingEnabled bool
	err := s.db.QueryRowContext(ctx, `
		SELECT s.id, s.user_id, s.tenant_id, s.internal_name, u.is_active, s.is_recording_enabled
		FROM commodore.streams s
		JOIN commodore.users u ON s.user_id = u.id
		WHERE LOWER(s.stream_key) = LOWER($1)
	`, streamKey).Scan(&streamID, &userID, &tenantID, &internalName, &isActive, &isRecordingEnabled)

	if err == sql.ErrNoRows {
		return &pb.ValidateStreamKeyResponse{
			Valid: false,
			Error: "Invalid stream key",
		}, nil
	}

	if err != nil {
		s.logger.WithFields(logging.Fields{
			"stream_key": streamKey,
			"error":      err,
		}).Error("Database error validating stream key")
		return nil, status.Errorf(codes.Internal, "database error: %v", err)
	}

	if !isActive {
		return &pb.ValidateStreamKeyResponse{
			Valid: false,
			Error: "User account is inactive",
		}, nil
	}

	return &pb.ValidateStreamKeyResponse{
		Valid:              true,
		UserId:             userID,
		TenantId:           tenantID,
		InternalName:       internalName,
		IsRecordingEnabled: isRecordingEnabled,
	}, nil
}

// ResolvePlaybackID resolves a playback ID to internal name for MistServer PLAY_REWRITE trigger
func (s *CommodoreServer) ResolvePlaybackID(ctx context.Context, req *pb.ResolvePlaybackIDRequest) (*pb.ResolvePlaybackIDResponse, error) {
	playbackID := req.GetPlaybackId()
	if playbackID == "" {
		return nil, status.Error(codes.InvalidArgument, "playback_id required")
	}

	var internalName, tenantID string
	err := s.db.QueryRowContext(ctx, `
		SELECT internal_name, tenant_id FROM commodore.streams WHERE LOWER(playback_id) = LOWER($1)
	`, playbackID).Scan(&internalName, &tenantID)

	if err == sql.ErrNoRows {
		return nil, status.Error(codes.NotFound, "Stream not found")
	}

	if err != nil {
		s.logger.WithFields(logging.Fields{
			"playback_id": playbackID,
			"error":       err,
		}).Error("Database error resolving playback ID")
		return nil, status.Errorf(codes.Internal, "database error: %v", err)
	}

	// Note: Status check removed - operational state now comes from Periscope (Data Plane)
	// Foghorn handles real-time stream state through its own state management

	return &pb.ResolvePlaybackIDResponse{
		InternalName: internalName,
		TenantId:     tenantID,
		PlaybackId:   playbackID,
	}, nil
}

// ResolveInternalName resolves an internal_name to tenant context for event enrichment
func (s *CommodoreServer) ResolveInternalName(ctx context.Context, req *pb.ResolveInternalNameRequest) (*pb.ResolveInternalNameResponse, error) {
	internalName := req.GetInternalName()
	if internalName == "" {
		return nil, status.Error(codes.InvalidArgument, "internal_name required")
	}

	var tenantID, userID string
	var isRecordingEnabled bool
	err := s.db.QueryRowContext(ctx, `
		SELECT tenant_id, user_id, is_recording_enabled FROM commodore.streams WHERE internal_name = $1
	`, internalName).Scan(&tenantID, &userID, &isRecordingEnabled)

	if err == sql.ErrNoRows {
		return nil, status.Error(codes.NotFound, "Stream not found")
	}

	if err != nil {
		s.logger.WithFields(logging.Fields{
			"internal_name": internalName,
			"error":         err,
		}).Error("Database error resolving internal name")
		return nil, status.Errorf(codes.Internal, "database error: %v", err)
	}

	return &pb.ResolveInternalNameResponse{
		InternalName:       internalName,
		TenantId:           tenantID,
		UserId:             userID,
		IsRecordingEnabled: isRecordingEnabled,
	}, nil
}

// ValidateAPIToken validates a developer API token (called by Gateway middleware)
func (s *CommodoreServer) ValidateAPIToken(ctx context.Context, req *pb.ValidateAPITokenRequest) (*pb.ValidateAPITokenResponse, error) {
	token := req.GetToken()
	if token == "" {
		return &pb.ValidateAPITokenResponse{Valid: false}, nil
	}

	var tokenID, userID, tenantID string
	var permissions []string
	err := s.db.QueryRowContext(ctx, `
		SELECT id, user_id, tenant_id, permissions
		FROM commodore.api_tokens
		WHERE token_value = $1 AND is_active = true AND (expires_at IS NULL OR expires_at > NOW())
	`, token).Scan(&tokenID, &userID, &tenantID, pq.Array(&permissions))

	if err == sql.ErrNoRows {
		return &pb.ValidateAPITokenResponse{Valid: false}, nil
	}

	if err != nil {
		s.logger.WithFields(logging.Fields{
			"error": err,
		}).Error("Database error validating API token")
		return nil, status.Errorf(codes.Internal, "database error: %v", err)
	}

	// Update last used timestamp (best effort)
	_, _ = s.db.ExecContext(ctx, `UPDATE commodore.api_tokens SET last_used_at = NOW() WHERE id = $1`, tokenID)

	// Look up user email and role for context
	var email, role string
	err = s.db.QueryRowContext(ctx, `SELECT email, role FROM commodore.users WHERE id = $1`, userID).Scan(&email, &role)
	if err != nil && err != sql.ErrNoRows {
		s.logger.WithFields(logging.Fields{
			"user_id": userID,
			"error":   err,
		}).Warn("Failed to fetch user details for API token")
	}

	return &pb.ValidateAPITokenResponse{
		Valid:       true,
		UserId:      userID,
		TenantId:    tenantID,
		Email:       email,
		Role:        role,
		Permissions: permissions,
	}, nil
}

// StartDVR initiates DVR recording for a stream (called by Foghorn when recording is enabled)
func (s *CommodoreServer) StartDVR(ctx context.Context, req *pb.StartDVRRequest) (*pb.StartDVRResponse, error) {
	tenantID := req.GetTenantId()
	internalName := req.GetInternalName()
	userID := req.GetUserId()

	if internalName == "" {
		return &pb.StartDVRResponse{
			Status: "error",
		}, nil
	}

	s.logger.WithFields(logging.Fields{
		"tenant_id":     tenantID,
		"internal_name": internalName,
		"user_id":       userID,
	}).Info("Starting DVR recording via gRPC")

	// Enforce 30-day default retention if not specified
	if req.ExpiresAt == nil {
		expiry := time.Now().Add(30 * 24 * time.Hour).Unix()
		req.ExpiresAt = &expiry
	}

	// Generate DVR hash - use crypto/rand for secure hash
	dvrHash := generateDVRHash()

	// For now, just return the DVR hash - actual DVR orchestration
	// happens through Foghorn's control plane
	return &pb.StartDVRResponse{
		DvrHash: dvrHash,
		Status:  "requested",
	}, nil
}

// ============================================================================
// CLIP/DVR REGISTRY (Foghorn → Commodore)
// Business registry for clips and DVR recordings.
// See: docs/architecture/CLIP_DVR_REGISTRY.md
// ============================================================================

// RegisterClip creates a new clip in the business registry
// Called by Foghorn during the CreateClip flow
func (s *CommodoreServer) RegisterClip(ctx context.Context, req *pb.RegisterClipRequest) (*pb.RegisterClipResponse, error) {
	tenantID := req.GetTenantId()
	userID := req.GetUserId()
	streamID := req.GetStreamId()

	if tenantID == "" || userID == "" || streamID == "" {
		return nil, status.Error(codes.InvalidArgument, "tenant_id, user_id, and stream_id are required")
	}

	// Generate clip hash
	clipHash := generateClipHash()
	clipID := uuid.New().String()

	// Insert into business registry
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO commodore.clips (
			id, tenant_id, user_id, stream_id, clip_hash,
			title, description, start_time, duration, clip_mode, requested_params,
			created_at, updated_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, NOW(), NOW())
	`, clipID, tenantID, userID, streamID, clipHash,
		req.GetTitle(), req.GetDescription(), req.GetStartTime(), req.GetDuration(),
		req.GetClipMode(), req.GetRequestedParams())

	if err != nil {
		s.logger.WithFields(logging.Fields{
			"tenant_id": tenantID,
			"stream_id": streamID,
			"error":     err,
		}).Error("Failed to register clip in business registry")
		return nil, status.Errorf(codes.Internal, "failed to register clip: %v", err)
	}

	s.logger.WithFields(logging.Fields{
		"tenant_id": tenantID,
		"clip_hash": clipHash,
		"clip_id":   clipID,
	}).Info("Registered clip in business registry")

	return &pb.RegisterClipResponse{
		ClipHash: clipHash,
		ClipId:   clipID,
	}, nil
}

// RegisterDVR creates a new DVR recording in the business registry
// Called by Foghorn during the StartDVR flow
func (s *CommodoreServer) RegisterDVR(ctx context.Context, req *pb.RegisterDVRRequest) (*pb.RegisterDVRResponse, error) {
	tenantID := req.GetTenantId()
	userID := req.GetUserId()
	internalName := req.GetInternalName()

	if tenantID == "" || userID == "" || internalName == "" {
		return nil, status.Error(codes.InvalidArgument, "tenant_id, user_id, and internal_name are required")
	}

	// Generate DVR hash
	dvrHash := generateDVRHash()
	dvrID := uuid.New().String()

	// stream_id is optional (legacy DVRs may not have it)
	var streamID *string
	if req.GetStreamId() != "" {
		sid := req.GetStreamId()
		streamID = &sid
	}

	// Insert into business registry
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO commodore.dvr_recordings (
			id, tenant_id, user_id, stream_id, dvr_hash, internal_name,
			created_at, updated_at
		) VALUES ($1, $2, $3, NULLIF($4, '')::uuid, $5, $6, NOW(), NOW())
	`, dvrID, tenantID, userID, streamID, dvrHash, internalName)

	if err != nil {
		s.logger.WithFields(logging.Fields{
			"tenant_id":     tenantID,
			"internal_name": internalName,
			"error":         err,
		}).Error("Failed to register DVR in business registry")
		return nil, status.Errorf(codes.Internal, "failed to register DVR: %v", err)
	}

	s.logger.WithFields(logging.Fields{
		"tenant_id":     tenantID,
		"dvr_hash":      dvrHash,
		"dvr_id":        dvrID,
		"internal_name": internalName,
	}).Info("Registered DVR in business registry")

	return &pb.RegisterDVRResponse{
		DvrHash: dvrHash,
		DvrId:   dvrID,
	}, nil
}

// ResolveClipHash resolves a clip hash to tenant context
// Used for analytics enrichment and playback authorization
func (s *CommodoreServer) ResolveClipHash(ctx context.Context, req *pb.ResolveClipHashRequest) (*pb.ResolveClipHashResponse, error) {
	clipHash := req.GetClipHash()
	if clipHash == "" {
		return nil, status.Error(codes.InvalidArgument, "clip_hash is required")
	}

	var tenantID, userID, streamID, title, description, clipMode string
	var startTime, duration int64
	var internalName sql.NullString

	err := s.db.QueryRowContext(ctx, `
		SELECT c.tenant_id, c.user_id, c.stream_id, c.title, c.description,
			   c.start_time, c.duration, c.clip_mode, s.internal_name
		FROM commodore.clips c
		LEFT JOIN commodore.streams s ON c.stream_id = s.id
		WHERE c.clip_hash = $1
	`, clipHash).Scan(&tenantID, &userID, &streamID, &title, &description,
		&startTime, &duration, &clipMode, &internalName)

	if err == sql.ErrNoRows {
		return &pb.ResolveClipHashResponse{
			Found: false,
		}, nil
	}

	if err != nil {
		s.logger.WithFields(logging.Fields{
			"clip_hash": clipHash,
			"error":     err,
		}).Error("Database error resolving clip hash")
		return nil, status.Errorf(codes.Internal, "database error: %v", err)
	}

	return &pb.ResolveClipHashResponse{
		Found:        true,
		TenantId:     tenantID,
		UserId:       userID,
		StreamId:     streamID,
		InternalName: internalName.String,
		Title:        title,
		Description:  description,
		StartTime:    startTime,
		Duration:     duration,
		ClipMode:     clipMode,
	}, nil
}

// ResolveDVRHash resolves a DVR hash to tenant context
// Used for analytics enrichment and playback authorization
func (s *CommodoreServer) ResolveDVRHash(ctx context.Context, req *pb.ResolveDVRHashRequest) (*pb.ResolveDVRHashResponse, error) {
	dvrHash := req.GetDvrHash()
	if dvrHash == "" {
		return nil, status.Error(codes.InvalidArgument, "dvr_hash is required")
	}

	var tenantID, userID, internalName string
	var streamID sql.NullString

	err := s.db.QueryRowContext(ctx, `
		SELECT tenant_id, user_id, stream_id, internal_name
		FROM commodore.dvr_recordings
		WHERE dvr_hash = $1
	`, dvrHash).Scan(&tenantID, &userID, &streamID, &internalName)

	if err == sql.ErrNoRows {
		return &pb.ResolveDVRHashResponse{
			Found: false,
		}, nil
	}

	if err != nil {
		s.logger.WithFields(logging.Fields{
			"dvr_hash": dvrHash,
			"error":    err,
		}).Error("Database error resolving DVR hash")
		return nil, status.Errorf(codes.Internal, "database error: %v", err)
	}

	return &pb.ResolveDVRHashResponse{
		Found:        true,
		TenantId:     tenantID,
		UserId:       userID,
		StreamId:     streamID.String,
		InternalName: internalName,
	}, nil
}

// RegisterVod registers a new VOD asset in the business registry
// Called by Foghorn during CreateVodUpload flow (mirrors DVR/clip pattern)
func (s *CommodoreServer) RegisterVod(ctx context.Context, req *pb.RegisterVodRequest) (*pb.RegisterVodResponse, error) {
	tenantID := req.GetTenantId()
	userID := req.GetUserId()
	filename := req.GetFilename()

	if tenantID == "" || userID == "" || filename == "" {
		return nil, status.Error(codes.InvalidArgument, "tenant_id, user_id, and filename are required")
	}

	// Generate VOD hash
	vodHash := generateVodHash()
	vodID := uuid.New().String()

	// Resolve retention (default 90 days for VOD)
	retentionUntil := time.Now().Add(90 * 24 * time.Hour)

	// Insert into business registry
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO commodore.vod_assets (
			id, tenant_id, user_id, vod_hash,
			title, description, filename, content_type, size_bytes,
			retention_until, created_at, updated_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, NOW(), NOW())
	`, vodID, tenantID, userID, vodHash,
		req.GetTitle(), req.GetDescription(), filename, req.GetContentType(), req.GetSizeBytes(),
		retentionUntil)

	if err != nil {
		s.logger.WithFields(logging.Fields{
			"tenant_id": tenantID,
			"filename":  filename,
			"error":     err,
		}).Error("Failed to register VOD in business registry")
		return nil, status.Errorf(codes.Internal, "failed to register VOD: %v", err)
	}

	s.logger.WithFields(logging.Fields{
		"tenant_id": tenantID,
		"vod_hash":  vodHash,
		"vod_id":    vodID,
		"filename":  filename,
	}).Info("Registered VOD in business registry")

	return &pb.RegisterVodResponse{
		VodHash: vodHash,
		VodId:   vodID,
	}, nil
}

// ResolveVodHash resolves a VOD hash to tenant context
// Used for analytics enrichment, playback authorization, and lifecycle operations
func (s *CommodoreServer) ResolveVodHash(ctx context.Context, req *pb.ResolveVodHashRequest) (*pb.ResolveVodHashResponse, error) {
	vodHash := req.GetVodHash()
	if vodHash == "" {
		return nil, status.Error(codes.InvalidArgument, "vod_hash is required")
	}

	var tenantID, userID, filename string
	var title, description sql.NullString

	err := s.db.QueryRowContext(ctx, `
		SELECT tenant_id, user_id, filename, title, description
		FROM commodore.vod_assets
		WHERE vod_hash = $1
	`, vodHash).Scan(&tenantID, &userID, &filename, &title, &description)

	if err == sql.ErrNoRows {
		return &pb.ResolveVodHashResponse{
			Found: false,
		}, nil
	}

	if err != nil {
		s.logger.WithFields(logging.Fields{
			"vod_hash": vodHash,
			"error":    err,
		}).Error("Database error resolving VOD hash")
		return nil, status.Errorf(codes.Internal, "database error: %v", err)
	}

	return &pb.ResolveVodHashResponse{
		Found:       true,
		TenantId:    tenantID,
		UserId:      userID,
		Filename:    filename,
		Title:       title.String,
		Description: description.String,
	}, nil
}

// ResolveIdentifier provides unified resolution across all Commodore registries
// Lookup order: streams (internal_name), streams (playback_id), clips, DVR, VOD
func (s *CommodoreServer) ResolveIdentifier(ctx context.Context, req *pb.ResolveIdentifierRequest) (*pb.ResolveIdentifierResponse, error) {
	identifier := req.GetIdentifier()
	if identifier == "" {
		return nil, status.Error(codes.InvalidArgument, "identifier is required")
	}

	// 1. Try streams by internal_name (most common for live stream events)
	var tenantID, userID string
	var isRecordingEnabled bool
	err := s.db.QueryRowContext(ctx, `
		SELECT tenant_id, user_id, is_recording_enabled FROM commodore.streams WHERE internal_name = $1
	`, identifier).Scan(&tenantID, &userID, &isRecordingEnabled)
	if err == nil {
		return &pb.ResolveIdentifierResponse{
			Found:              true,
			TenantId:           tenantID,
			UserId:             userID,
			InternalName:       identifier,
			IdentifierType:     "stream",
			IsRecordingEnabled: isRecordingEnabled,
		}, nil
	} else if err != sql.ErrNoRows {
		s.logger.WithError(err).Error("Database error checking streams by internal_name")
	}

	// 2. Try streams by playback_id
	var internalName string
	err = s.db.QueryRowContext(ctx, `
		SELECT tenant_id, user_id, internal_name, is_recording_enabled
		FROM commodore.streams WHERE LOWER(playback_id) = LOWER($1)
	`, identifier).Scan(&tenantID, &userID, &internalName, &isRecordingEnabled)
	if err == nil {
		return &pb.ResolveIdentifierResponse{
			Found:              true,
			TenantId:           tenantID,
			UserId:             userID,
			InternalName:       internalName,
			IdentifierType:     "playback_id",
			IsRecordingEnabled: isRecordingEnabled,
		}, nil
	} else if err != sql.ErrNoRows {
		s.logger.WithError(err).Error("Database error checking streams by playback_id")
	}

	// 3. Try clips by clip_hash
	var parentInternalName sql.NullString
	err = s.db.QueryRowContext(ctx, `
		SELECT c.tenant_id, c.user_id, s.internal_name
		FROM commodore.clips c
		LEFT JOIN commodore.streams s ON c.stream_id = s.id
		WHERE c.clip_hash = $1
	`, identifier).Scan(&tenantID, &userID, &parentInternalName)
	if err == nil {
		return &pb.ResolveIdentifierResponse{
			Found:          true,
			TenantId:       tenantID,
			UserId:         userID,
			InternalName:   parentInternalName.String,
			IdentifierType: "clip",
		}, nil
	} else if err != sql.ErrNoRows {
		s.logger.WithError(err).Error("Database error checking clips")
	}

	// 4. Try DVR by dvr_hash
	err = s.db.QueryRowContext(ctx, `
		SELECT tenant_id, user_id, internal_name FROM commodore.dvr_recordings WHERE dvr_hash = $1
	`, identifier).Scan(&tenantID, &userID, &internalName)
	if err == nil {
		return &pb.ResolveIdentifierResponse{
			Found:          true,
			TenantId:       tenantID,
			UserId:         userID,
			InternalName:   internalName,
			IdentifierType: "dvr",
		}, nil
	} else if err != sql.ErrNoRows {
		s.logger.WithError(err).Error("Database error checking DVR")
	}

	// 5. Try VOD by vod_hash
	err = s.db.QueryRowContext(ctx, `
		SELECT tenant_id, user_id FROM commodore.vod_assets WHERE vod_hash = $1
	`, identifier).Scan(&tenantID, &userID)
	if err == nil {
		return &pb.ResolveIdentifierResponse{
			Found:          true,
			TenantId:       tenantID,
			UserId:         userID,
			IdentifierType: "vod",
		}, nil
	} else if err != sql.ErrNoRows {
		s.logger.WithError(err).Error("Database error checking VOD")
	}

	// Not found in any registry
	return &pb.ResolveIdentifierResponse{
		Found: false,
	}, nil
}

// ============================================================================
// USER SERVICE (Gateway → Commodore for auth flows)
// ============================================================================

// Login authenticates a user and returns a JWT token
func (s *CommodoreServer) Login(ctx context.Context, req *pb.LoginRequest) (*pb.AuthResponse, error) {
	email := req.GetEmail()
	password := req.GetPassword()

	if email == "" || password == "" {
		return nil, status.Error(codes.InvalidArgument, "email and password required")
	}

	// Bot protection: Turnstile (primary) or behavioral (fallback)
	if s.turnstileValidator != nil {
		clientIP := ""
		if md, ok := metadata.FromIncomingContext(ctx); ok {
			if ips := md.Get("x-client-ip"); len(ips) > 0 {
				clientIP = ips[0]
			} else if ips := md.Get("x-forwarded-for"); len(ips) > 0 {
				clientIP = strings.Split(ips[0], ",")[0]
			}
		}

		turnstileResp, err := s.turnstileValidator.Verify(ctx, req.GetTurnstileToken(), clientIP)
		if err != nil {
			s.logger.WithError(err).Warn("Turnstile verification request failed")
		} else if !turnstileResp.Success {
			s.logger.WithFields(logging.Fields{
				"email":       email,
				"client_ip":   clientIP,
				"error_codes": turnstileResp.ErrorCodes,
			}).Warn("Login Turnstile verification failed")
			return nil, status.Error(codes.InvalidArgument, "bot verification failed")
		}
	} else {
		// Fallback: behavioral validation when Turnstile not configured
		if !validateBehavior(req) {
			s.logger.WithField("email", email).Warn("Login behavioral bot check failed")
			return nil, status.Error(codes.InvalidArgument, "bot verification failed")
		}
	}

	// Find user by email
	var user struct {
		ID           string
		TenantID     string
		Email        string
		PasswordHash string
		FirstName    sql.NullString
		LastName     sql.NullString
		Role         string
		Permissions  []string
		IsActive     bool
		IsVerified   bool
		CreatedAt    time.Time
		UpdatedAt    time.Time
	}
	err := s.db.QueryRowContext(ctx, `
		SELECT id, tenant_id, email, password_hash, first_name, last_name, role, permissions, is_active, verified, created_at, updated_at
		FROM commodore.users WHERE email = $1
	`, email).Scan(&user.ID, &user.TenantID, &user.Email, &user.PasswordHash,
		&user.FirstName, &user.LastName, &user.Role, pq.Array(&user.Permissions),
		&user.IsActive, &user.IsVerified, &user.CreatedAt, &user.UpdatedAt)

	if err == sql.ErrNoRows {
		return nil, status.Error(codes.Unauthenticated, "invalid credentials")
	}
	if err != nil {
		s.logger.WithError(err).Error("Database error during login")
		return nil, status.Errorf(codes.Internal, "database error: %v", err)
	}

	// Check account status
	if !user.IsActive {
		return nil, status.Error(codes.Unauthenticated, "account deactivated")
	}
	if !user.IsVerified {
		return nil, status.Error(codes.Unauthenticated, "email not verified")
	}

	// Verify password
	if !auth.CheckPassword(password, user.PasswordHash) {
		return nil, status.Error(codes.Unauthenticated, "invalid credentials")
	}

	// Update last login
	_, _ = s.db.ExecContext(ctx, `UPDATE commodore.users SET last_login_at = NOW() WHERE id = $1`, user.ID)

	// Generate JWT access token
	jwtSecret := []byte(config.RequireEnv("JWT_SECRET"))
	token, err := auth.GenerateJWT(user.ID, user.TenantID, user.Email, user.Role, jwtSecret)
	if err != nil {
		s.logger.WithError(err).Error("Failed to generate JWT")
		return nil, status.Errorf(codes.Internal, "failed to generate token: %v", err)
	}

	// Generate refresh token and store in DB
	refreshToken := generateRandomString(40)
	refreshHash := hashToken(refreshToken)
	refreshExpiry := time.Now().Add(30 * 24 * time.Hour) // 30 days

	_, err = s.db.ExecContext(ctx, `
		INSERT INTO commodore.refresh_tokens (tenant_id, user_id, token_hash, expires_at)
		VALUES ($1, $2, $3, $4)
	`, user.TenantID, user.ID, refreshHash, refreshExpiry)
	if err != nil {
		s.logger.WithError(err).Error("Failed to store refresh token")
		return nil, status.Errorf(codes.Internal, "failed to create session: %v", err)
	}

	expiresAt := time.Now().Add(15 * time.Minute)

	return &pb.AuthResponse{
		Token:        token,
		RefreshToken: refreshToken,
		User: &pb.User{
			Id:          user.ID,
			TenantId:    user.TenantID,
			Email:       user.Email,
			FirstName:   user.FirstName.String,
			LastName:    user.LastName.String,
			Role:        user.Role,
			Permissions: user.Permissions,
			IsActive:    user.IsActive,
			IsVerified:  user.IsVerified,
			CreatedAt:   timestamppb.New(user.CreatedAt),
			UpdatedAt:   timestamppb.New(user.UpdatedAt),
		},
		ExpiresAt: timestamppb.New(expiresAt),
	}, nil
}

// Register creates a new user account
func (s *CommodoreServer) Register(ctx context.Context, req *pb.RegisterRequest) (*pb.RegisterResponse, error) {
	email := req.GetEmail()
	password := req.GetPassword()

	if email == "" || password == "" {
		return nil, status.Error(codes.InvalidArgument, "email and password required")
	}

	// Bot protection: Turnstile (primary) or behavioral (fallback)
	if s.turnstileValidator != nil {
		// Get client IP from gRPC metadata if available
		clientIP := ""
		if md, ok := metadata.FromIncomingContext(ctx); ok {
			if ips := md.Get("x-client-ip"); len(ips) > 0 {
				clientIP = ips[0]
			} else if ips := md.Get("x-forwarded-for"); len(ips) > 0 {
				clientIP = strings.Split(ips[0], ",")[0]
			}
		}

		turnstileResp, err := s.turnstileValidator.Verify(ctx, req.GetTurnstileToken(), clientIP)
		if err != nil {
			s.logger.WithError(err).Warn("Turnstile verification request failed")
		} else if !turnstileResp.Success {
			s.logger.WithFields(logging.Fields{
				"email":       email,
				"client_ip":   clientIP,
				"error_codes": turnstileResp.ErrorCodes,
			}).Warn("Turnstile verification failed")
			return nil, status.Error(codes.InvalidArgument, "bot verification failed")
		}
	} else {
		// Fallback: behavioral validation when Turnstile not configured
		if !validateBehavior(req) {
			s.logger.WithField("email", email).Warn("Behavioral bot check failed")
			return nil, status.Error(codes.InvalidArgument, "bot verification failed")
		}
	}

	// Check if user already exists
	var existingID string
	err := s.db.QueryRowContext(ctx, `SELECT id FROM commodore.users WHERE email = $1`, email).Scan(&existingID)
	if err == nil {
		return &pb.RegisterResponse{
			Success: false,
			Message: "user already exists",
		}, nil
	}
	if err != sql.ErrNoRows {
		return nil, status.Errorf(codes.Internal, "database error: %v", err)
	}

	// Create tenant via Quartermaster
	var tenantID string
	if s.quartermasterClient != nil {
		resp, err := s.quartermasterClient.CreateTenant(ctx, &pb.CreateTenantRequest{
			Name: email, // Use email as initial tenant name
		})
		if err != nil {
			s.logger.WithError(err).Error("Failed to create tenant via Quartermaster")
			return nil, status.Errorf(codes.Internal, "failed to create tenant: %v", err)
		}
		tenantID = resp.GetTenant().GetId()
	} else {
		// Fallback for testing without Quartermaster
		tenantID = uuid.New().String()
		s.logger.Warn("Quartermaster client not available, using generated tenant ID")
	}

	// Check user limit via Purser (if available)
	if s.purserClient != nil {
		limitCheck, err := s.purserClient.CheckUserLimit(ctx, tenantID, email)
		if err != nil {
			s.logger.WithError(err).Warn("Failed to check user limit with Purser, proceeding anyway")
		} else if !limitCheck.GetAllowed() {
			return &pb.RegisterResponse{
				Success: false,
				Message: "tenant user limit reached",
			}, nil
		}
	}

	// Hash password
	hashedPassword, err := auth.HashPassword(password)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to hash password: %v", err)
	}

	// Generate verification token
	verificationToken := generateSecureToken(32)
	tokenHash := hashToken(verificationToken) // Store hash, send raw in email
	tokenExpiry := time.Now().Add(24 * time.Hour)

	// Check if this is the first user for the tenant (becomes owner)
	var userCount int
	err = s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM commodore.users WHERE tenant_id = $1`, tenantID).Scan(&userCount)
	role := "member"
	if err == nil && userCount == 0 {
		role = "owner"
	}

	// Create user
	userID := uuid.New().String()
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO commodore.users (id, tenant_id, email, password_hash, first_name, last_name, role, permissions, is_active, verified, verification_token, token_expires_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, true, false, $9, $10)
	`, userID, tenantID, email, hashedPassword, req.GetFirstName(), req.GetLastName(), role, pq.Array(getDefaultPermissions(role)), tokenHash, tokenExpiry)

	if err != nil {
		s.logger.WithError(err).Error("Failed to create user")
		return nil, status.Errorf(codes.Internal, "failed to create user: %v", err)
	}

	// Send verification email (best effort, don't fail registration)
	if err := s.sendVerificationEmail(email, verificationToken); err != nil {
		s.logger.WithFields(logging.Fields{
			"user_id":   userID,
			"tenant_id": tenantID,
			"email":     email,
			"error":     err,
		}).Error("Failed to send verification email")
	}

	// Sync to Listmonk (async, best effort)
	if s.listmonkClient != nil {
		go func(email, first, last string) {
			name := strings.TrimSpace(first + " " + last)
			if name == "" {
				name = "Friend"
			}
			if err := s.listmonkClient.Subscribe(context.Background(), email, name, s.defaultMailingListID, true); err != nil {
				s.logger.WithError(err).Warn("Failed to sync new user to Listmonk")
			}
		}(email, req.GetFirstName(), req.GetLastName())
	}

	s.logger.WithFields(logging.Fields{
		"user_id":   userID,
		"tenant_id": tenantID,
		"email":     email,
		"role":      role,
	}).Info("User registered successfully via gRPC")

	return &pb.RegisterResponse{
		Success: true,
		Message: "Registration successful. Please check your email to verify your account.",
	}, nil
}

// GetMe returns the current user's profile
func (s *CommodoreServer) GetMe(ctx context.Context, req *pb.GetMeRequest) (*pb.User, error) {
	userID, tenantID, err := extractUserContext(ctx)
	if err != nil {
		return nil, err
	}

	var user struct {
		ID          string
		TenantID    string
		Email       string
		FirstName   sql.NullString
		LastName    sql.NullString
		Role        string
		Permissions []string
		IsActive    bool
		IsVerified  bool
		LastLoginAt sql.NullTime
		CreatedAt   time.Time
		UpdatedAt   time.Time
	}

	err = s.db.QueryRowContext(ctx, `
		SELECT id, tenant_id, email, first_name, last_name, role, permissions, is_active, verified, last_login_at, created_at, updated_at
		FROM commodore.users WHERE id = $1 AND tenant_id = $2
	`, userID, tenantID).Scan(&user.ID, &user.TenantID, &user.Email, &user.FirstName, &user.LastName,
		&user.Role, pq.Array(&user.Permissions), &user.IsActive, &user.IsVerified, &user.LastLoginAt,
		&user.CreatedAt, &user.UpdatedAt)

	if err == sql.ErrNoRows {
		return nil, status.Error(codes.NotFound, "user not found")
	}
	if err != nil {
		return nil, status.Errorf(codes.Internal, "database error: %v", err)
	}

	result := &pb.User{
		Id:          user.ID,
		TenantId:    user.TenantID,
		Email:       user.Email,
		FirstName:   user.FirstName.String,
		LastName:    user.LastName.String,
		Role:        user.Role,
		Permissions: user.Permissions,
		IsActive:    user.IsActive,
		IsVerified:  user.IsVerified,
		CreatedAt:   timestamppb.New(user.CreatedAt),
		UpdatedAt:   timestamppb.New(user.UpdatedAt),
	}
	if user.LastLoginAt.Valid {
		result.LastLoginAt = timestamppb.New(user.LastLoginAt.Time)
	}

	return result, nil
}

// Logout invalidates user session (token blacklisting handled at Gateway)
func (s *CommodoreServer) Logout(ctx context.Context, req *pb.LogoutRequest) (*pb.LogoutResponse, error) {
	// Get user context to delete their refresh tokens
	userID, tenantID, err := extractUserContext(ctx)
	if err != nil {
		// Still acknowledge logout even without user context
		return &pb.LogoutResponse{
			Success: true,
			Message: "logged out successfully",
		}, nil
	}

	// Delete all refresh tokens for this user (logs them out of all devices)
	_, err = s.db.ExecContext(ctx, `
		DELETE FROM commodore.refresh_tokens WHERE user_id = $1 AND tenant_id = $2
	`, userID, tenantID)
	if err != nil {
		s.logger.WithError(err).Warn("Failed to delete refresh tokens during logout")
	}

	return &pb.LogoutResponse{
		Success: true,
		Message: "logged out successfully",
	}, nil
}

// RefreshToken exchanges a refresh token for a new access token
func (s *CommodoreServer) RefreshToken(ctx context.Context, req *pb.RefreshTokenRequest) (*pb.AuthResponse, error) {
	refreshToken := req.GetRefreshToken()
	if refreshToken == "" {
		return nil, status.Error(codes.InvalidArgument, "refresh token required")
	}

	// Hash the token and look it up in the database
	tokenHash := hashToken(refreshToken)

	var tokenID, userID, tenantID string
	var revoked bool
	err := s.db.QueryRowContext(ctx, `
		SELECT id, user_id, tenant_id, revoked FROM commodore.refresh_tokens
		WHERE token_hash = $1 AND expires_at > NOW()
	`, tokenHash).Scan(&tokenID, &userID, &tenantID, &revoked)

	if err == sql.ErrNoRows {
		return nil, status.Error(codes.Unauthenticated, "invalid or expired refresh token")
	}
	if err != nil {
		s.logger.WithError(err).Error("Database error validating refresh token")
		return nil, status.Errorf(codes.Internal, "database error: %v", err)
	}

	// Token reuse detection: if token was already revoked, revoke ALL user tokens (security)
	if revoked {
		s.logger.WithFields(logging.Fields{
			"user_id":   userID,
			"tenant_id": tenantID,
		}).Warn("Refresh token reuse detected, revoking all user sessions")
		_, _ = s.db.ExecContext(ctx, `
			UPDATE commodore.refresh_tokens SET revoked = true
			WHERE user_id = $1 AND tenant_id = $2
		`, userID, tenantID)
		return nil, status.Error(codes.Unauthenticated, "session invalidated")
	}

	// Revoke the old refresh token (don't delete - keep for reuse detection)
	_, _ = s.db.ExecContext(ctx, `
		UPDATE commodore.refresh_tokens SET revoked = true WHERE id = $1
	`, tokenID)

	// Look up user details
	var user struct {
		Email       string
		Role        string
		Permissions []string
		FirstName   sql.NullString
		LastName    sql.NullString
		IsActive    bool
		IsVerified  bool
		CreatedAt   time.Time
		UpdatedAt   time.Time
	}
	err = s.db.QueryRowContext(ctx, `
		SELECT email, role, permissions, first_name, last_name, is_active, verified, created_at, updated_at
		FROM commodore.users WHERE id = $1 AND tenant_id = $2
	`, userID, tenantID).Scan(&user.Email, &user.Role, pq.Array(&user.Permissions),
		&user.FirstName, &user.LastName, &user.IsActive, &user.IsVerified,
		&user.CreatedAt, &user.UpdatedAt)

	if err != nil {
		return nil, status.Error(codes.Unauthenticated, "user not found")
	}

	if !user.IsActive {
		return nil, status.Error(codes.Unauthenticated, "account deactivated")
	}

	// Generate new access token
	jwtSecret := []byte(config.RequireEnv("JWT_SECRET"))
	token, err := auth.GenerateJWT(userID, tenantID, user.Email, user.Role, jwtSecret)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to generate token: %v", err)
	}

	// Generate new refresh token
	newRefreshToken := generateRandomString(40)
	newRefreshHash := hashToken(newRefreshToken)
	refreshExpiry := time.Now().Add(30 * 24 * time.Hour)

	_, err = s.db.ExecContext(ctx, `
		INSERT INTO commodore.refresh_tokens (tenant_id, user_id, token_hash, expires_at)
		VALUES ($1, $2, $3, $4)
	`, tenantID, userID, newRefreshHash, refreshExpiry)
	if err != nil {
		s.logger.WithError(err).Error("Failed to store new refresh token")
		// Don't fail - access token is still valid
	}

	expiresAt := time.Now().Add(15 * time.Minute)

	return &pb.AuthResponse{
		Token:        token,
		RefreshToken: newRefreshToken,
		User: &pb.User{
			Id:          userID,
			TenantId:    tenantID,
			Email:       user.Email,
			FirstName:   user.FirstName.String,
			LastName:    user.LastName.String,
			Role:        user.Role,
			Permissions: user.Permissions,
			IsActive:    user.IsActive,
			IsVerified:  user.IsVerified,
			CreatedAt:   timestamppb.New(user.CreatedAt),
			UpdatedAt:   timestamppb.New(user.UpdatedAt),
		},
		ExpiresAt: timestamppb.New(expiresAt),
	}, nil
}

// VerifyEmail verifies a user's email address with a token
func (s *CommodoreServer) VerifyEmail(ctx context.Context, req *pb.VerifyEmailRequest) (*pb.VerifyEmailResponse, error) {
	token := req.GetToken()
	if token == "" {
		return nil, status.Error(codes.InvalidArgument, "verification token required")
	}

	// Hash token for lookup (stored hashed in DB)
	tokenHash := hashToken(token)

	// Find user by verification token with expiry check
	var userID string
	err := s.db.QueryRowContext(ctx, `
		SELECT id FROM commodore.users
		WHERE verification_token = $1 AND verified = false AND token_expires_at > NOW()
	`, tokenHash).Scan(&userID)

	if err == sql.ErrNoRows {
		return &pb.VerifyEmailResponse{
			Success: false,
			Message: "invalid or expired verification token",
		}, nil
	}
	if err != nil {
		return nil, status.Errorf(codes.Internal, "database error: %v", err)
	}

	// Mark as verified and clear token
	_, err = s.db.ExecContext(ctx, `
		UPDATE commodore.users
		SET verified = true, verification_token = NULL, token_expires_at = NULL, updated_at = NOW()
		WHERE id = $1
	`, userID)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to verify email: %v", err)
	}

	return &pb.VerifyEmailResponse{
		Success: true,
		Message: "email verified successfully",
	}, nil
}

// ResendVerification resends the email verification link
func (s *CommodoreServer) ResendVerification(ctx context.Context, req *pb.ResendVerificationRequest) (*pb.ResendVerificationResponse, error) {
	email := req.GetEmail()
	if email == "" {
		return nil, status.Error(codes.InvalidArgument, "email required")
	}

	// Optional Turnstile verification (if configured)
	if s.turnstileValidator != nil && req.GetTurnstileToken() != "" {
		clientIP := ""
		if md, ok := metadata.FromIncomingContext(ctx); ok {
			if ips := md.Get("x-client-ip"); len(ips) > 0 {
				clientIP = ips[0]
			} else if ips := md.Get("x-forwarded-for"); len(ips) > 0 {
				clientIP = strings.Split(ips[0], ",")[0]
			}
		}

		turnstileResp, err := s.turnstileValidator.Verify(ctx, req.GetTurnstileToken(), clientIP)
		if err != nil {
			s.logger.WithError(err).Warn("Turnstile verification request failed")
		} else if !turnstileResp.Success {
			return nil, status.Error(codes.InvalidArgument, "bot verification failed")
		}
	}

	// Find user by email
	var userID string
	var isVerified bool
	var tokenExpiresAt sql.NullTime
	err := s.db.QueryRowContext(ctx, `
		SELECT id, verified, token_expires_at FROM commodore.users WHERE email = $1
	`, email).Scan(&userID, &isVerified, &tokenExpiresAt)

	if err == sql.ErrNoRows {
		// Don't reveal if email exists - return success anyway
		return &pb.ResendVerificationResponse{
			Success: true,
			Message: "if an account exists with that email and is unverified, a new verification link will be sent",
		}, nil
	}
	if err != nil {
		return nil, status.Errorf(codes.Internal, "database error: %v", err)
	}

	// Already verified
	if isVerified {
		return &pb.ResendVerificationResponse{
			Success: false,
			Message: "email is already verified",
		}, nil
	}

	// Rate limiting: check if token was generated within last 5 minutes
	if tokenExpiresAt.Valid {
		// Token expiry is 24h from creation, so creation time is expiry - 24h
		tokenCreatedAt := tokenExpiresAt.Time.Add(-24 * time.Hour)
		if time.Since(tokenCreatedAt) < 5*time.Minute {
			return &pb.ResendVerificationResponse{
				Success: false,
				Message: "please wait a few minutes before requesting another verification email",
			}, nil
		}
	}

	// Generate new verification token
	verificationToken := generateSecureToken(32)
	tokenHash := hashToken(verificationToken)
	tokenExpiry := time.Now().Add(24 * time.Hour)

	// Update user with new token
	_, err = s.db.ExecContext(ctx, `
		UPDATE commodore.users
		SET verification_token = $1, token_expires_at = $2, updated_at = NOW()
		WHERE id = $3
	`, tokenHash, tokenExpiry, userID)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to generate verification token: %v", err)
	}

	// Send verification email
	if err := s.sendVerificationEmail(email, verificationToken); err != nil {
		s.logger.WithFields(logging.Fields{
			"user_id": userID,
			"email":   email,
			"error":   err,
		}).Error("Failed to send verification email")
		return &pb.ResendVerificationResponse{
			Success: false,
			Message: "failed to send verification email, please try again later",
		}, nil
	}

	s.logger.WithFields(logging.Fields{
		"user_id": userID,
		"email":   email,
	}).Info("Verification email resent")

	return &pb.ResendVerificationResponse{
		Success: true,
		Message: "verification email sent",
	}, nil
}

// ForgotPassword initiates the password reset flow
func (s *CommodoreServer) ForgotPassword(ctx context.Context, req *pb.ForgotPasswordRequest) (*pb.ForgotPasswordResponse, error) {
	email := req.GetEmail()
	if email == "" {
		return nil, status.Error(codes.InvalidArgument, "email required")
	}

	// Check if user exists
	var userID string
	err := s.db.QueryRowContext(ctx, `SELECT id FROM commodore.users WHERE email = $1`, email).Scan(&userID)
	if err == sql.ErrNoRows {
		// Don't reveal whether email exists - always return success
		return &pb.ForgotPasswordResponse{
			Success: true,
			Message: "if an account exists with that email, a reset link will be sent",
		}, nil
	}
	if err != nil {
		return nil, status.Errorf(codes.Internal, "database error: %v", err)
	}

	// Generate reset token and hash for storage (uses HMAC if PASSWORD_RESET_SECRET is configured)
	resetToken := generateSecureToken(32)
	resetTokenHash := s.hashTokenWithSecret(resetToken)
	expiresAt := time.Now().Add(1 * time.Hour)

	// Store hashed reset token
	_, err = s.db.ExecContext(ctx, `
		UPDATE commodore.users
		SET reset_token = $1, reset_token_expires = $2, updated_at = NOW()
		WHERE id = $3
	`, resetTokenHash, expiresAt, userID)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to create reset token: %v", err)
	}

	// Send password reset email
	if err := s.sendPasswordResetEmail(email, resetToken); err != nil {
		s.logger.WithFields(logging.Fields{
			"user_id": userID,
			"email":   email,
			"error":   err,
		}).Error("Failed to send password reset email")
		// Don't fail - user may retry
	} else {
		s.logger.WithFields(logging.Fields{
			"user_id": userID,
			"email":   email,
		}).Info("Password reset email sent")
	}

	return &pb.ForgotPasswordResponse{
		Success: true,
		Message: "if an account exists with that email, a reset link will be sent",
	}, nil
}

// ResetPassword resets a user's password with a valid token
func (s *CommodoreServer) ResetPassword(ctx context.Context, req *pb.ResetPasswordRequest) (*pb.ResetPasswordResponse, error) {
	token := req.GetToken()
	password := req.GetPassword()

	if token == "" || password == "" {
		return nil, status.Error(codes.InvalidArgument, "token and password required")
	}

	// Hash token for lookup (uses HMAC if PASSWORD_RESET_SECRET is configured)
	tokenHash := s.hashTokenWithSecret(token)

	// Find user by reset token
	var userID string
	err := s.db.QueryRowContext(ctx, `
		SELECT id FROM commodore.users
		WHERE reset_token = $1 AND reset_token_expires > NOW()
	`, tokenHash).Scan(&userID)

	if err == sql.ErrNoRows {
		return &pb.ResetPasswordResponse{
			Success: false,
			Message: "invalid or expired reset token",
		}, nil
	}
	if err != nil {
		return nil, status.Errorf(codes.Internal, "database error: %v", err)
	}

	// Hash new password
	hashedPassword, err := auth.HashPassword(password)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to hash password: %v", err)
	}

	// Update password and clear reset token
	_, err = s.db.ExecContext(ctx, `
		UPDATE commodore.users
		SET password_hash = $1, reset_token = NULL, reset_token_expires = NULL, updated_at = NOW()
		WHERE id = $2
	`, hashedPassword, userID)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to update password: %v", err)
	}

	return &pb.ResetPasswordResponse{
		Success: true,
		Message: "password reset successfully",
	}, nil
}

// UpdateMe updates the current user's profile
func (s *CommodoreServer) UpdateMe(ctx context.Context, req *pb.UpdateMeRequest) (*pb.User, error) {
	userID, tenantID, err := extractUserContext(ctx)
	if err != nil {
		return nil, err
	}

	// Build dynamic update query
	updates := []string{}
	args := []interface{}{}
	argCount := 1

	if req.FirstName != nil {
		updates = append(updates, fmt.Sprintf("first_name = $%d", argCount))
		args = append(args, *req.FirstName)
		argCount++
	}
	if req.LastName != nil {
		updates = append(updates, fmt.Sprintf("last_name = $%d", argCount))
		args = append(args, *req.LastName)
		argCount++
	}
	if req.PhoneNumber != nil {
		updates = append(updates, fmt.Sprintf("phone_number = $%d", argCount))
		args = append(args, *req.PhoneNumber)
		argCount++
	}

	if len(updates) == 0 {
		return nil, status.Error(codes.InvalidArgument, "no fields to update")
	}

	updates = append(updates, "updated_at = NOW()")
	query := fmt.Sprintf("UPDATE commodore.users SET %s WHERE id = $%d AND tenant_id = $%d",
		strings.Join(updates, ", "), argCount, argCount+1)
	args = append(args, userID, tenantID)

	_, err = s.db.ExecContext(ctx, query, args...)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to update profile: %v", err)
	}

	// Return updated user
	return s.GetMe(ctx, &pb.GetMeRequest{})
}

// UpdateNewsletter updates the user's newsletter subscription preference
func (s *CommodoreServer) UpdateNewsletter(ctx context.Context, req *pb.UpdateNewsletterRequest) (*pb.UpdateNewsletterResponse, error) {
	userID, tenantID, err := extractUserContext(ctx)
	if err != nil {
		return nil, err
	}

	_, err = s.db.ExecContext(ctx, `
		UPDATE commodore.users
		SET newsletter_subscribed = $1, updated_at = NOW()
		WHERE id = $2 AND tenant_id = $3
	`, req.GetSubscribed(), userID, tenantID)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to update newsletter preference: %v", err)
	}

	return &pb.UpdateNewsletterResponse{
		Success: true,
		Message: "newsletter preference updated",
	}, nil
}

// ============================================================================
// STREAM SERVICE (Gateway → Commodore for stream CRUD)
// ============================================================================

// CreateStream creates a new stream for the authenticated user
func (s *CommodoreServer) CreateStream(ctx context.Context, req *pb.CreateStreamRequest) (*pb.CreateStreamResponse, error) {
	userID, tenantID, err := extractUserContext(ctx)
	if err != nil {
		return nil, err
	}

	title := req.GetTitle()
	if title == "" {
		title = "Untitled Stream"
	}

	// Use stored procedure to create stream
	var streamID, streamKey, playbackID, internalName string
	err = s.db.QueryRowContext(ctx, `
		SELECT stream_id, stream_key, playback_id, internal_name
		FROM commodore.create_user_stream($1, $2, $3)
	`, tenantID, userID, title).Scan(&streamID, &streamKey, &playbackID, &internalName)

	if err != nil {
		s.logger.WithError(err).Error("Failed to create stream")
		return nil, status.Errorf(codes.Internal, "failed to create stream: %v", err)
	}

	// Update description if provided
	if req.GetDescription() != "" {
		_, err = s.db.ExecContext(ctx, `
			UPDATE commodore.streams SET description = $1 WHERE id = $2
		`, req.GetDescription(), streamID)
		if err != nil {
			s.logger.WithError(err).Warn("Failed to update stream description")
		}
	}

	// Update recording setting if requested
	if req.GetIsRecording() {
		_, err = s.db.ExecContext(ctx, `
			UPDATE commodore.streams SET is_recording_enabled = true WHERE id = $1
		`, streamID)
		if err != nil {
			s.logger.WithError(err).Warn("Failed to enable recording")
		}
	}

	return &pb.CreateStreamResponse{
		Id:          internalName,
		StreamKey:   streamKey,
		PlaybackId:  playbackID,
		Title:       title,
		Description: req.GetDescription(),
		Status:      "offline",
	}, nil
}

// GetStream retrieves a specific stream
func (s *CommodoreServer) GetStream(ctx context.Context, req *pb.GetStreamRequest) (*pb.Stream, error) {
	userID, tenantID, err := extractUserContext(ctx)
	if err != nil {
		return nil, err
	}

	streamID := req.GetStreamId()
	if streamID == "" {
		return nil, status.Error(codes.InvalidArgument, "stream_id required")
	}

	return s.queryStream(ctx, streamID, userID, tenantID)
}

// ListStreams returns all streams for the authenticated user with keyset pagination
func (s *CommodoreServer) ListStreams(ctx context.Context, req *pb.ListStreamsRequest) (*pb.ListStreamsResponse, error) {
	userID, tenantID, err := extractUserContext(ctx)
	if err != nil {
		return nil, err
	}

	// Parse bidirectional pagination
	params, err := pagination.Parse(req.GetPagination())
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid pagination: %v", err)
	}

	// Get total count
	var total int32
	err = s.db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM commodore.streams WHERE user_id = $1 AND tenant_id = $2
	`, userID, tenantID).Scan(&total)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "database error: %v", err)
	}

	// Build keyset pagination query
	builder := &pagination.KeysetBuilder{
		TimestampColumn: "created_at",
		IDColumn:        "internal_name",
	}

	// Base query
	query := `
		SELECT internal_name, stream_key, playback_id, title, description,
		       is_recording_enabled, created_at, updated_at
		FROM commodore.streams
		WHERE user_id = $1 AND tenant_id = $2`
	args := []interface{}{userID, tenantID}
	argIdx := 3

	// Add keyset condition if cursor provided
	if condition, cursorArgs := builder.Condition(params, argIdx); condition != "" {
		query += " AND " + condition
		args = append(args, cursorArgs...)
		argIdx += len(cursorArgs)
	}

	// Add ORDER BY and LIMIT (fetch limit+1 to detect hasMore)
	query += " " + builder.OrderBy(params)
	query += fmt.Sprintf(" LIMIT %d", params.Limit+1)

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "database error: %v", err)
	}
	defer rows.Close()

	var streams []*pb.Stream
	for rows.Next() {
		stream, err := scanStream(rows)
		if err != nil {
			s.logger.WithError(err).Warn("Error scanning stream")
			continue
		}
		streams = append(streams, stream)
	}

	// Detect hasMore and trim results
	hasMore := len(streams) > params.Limit
	if hasMore {
		streams = streams[:params.Limit]
	}

	// Reverse results if backward pagination
	if params.Direction == pagination.Backward && len(streams) > 0 {
		for i, j := 0, len(streams)-1; i < j; i, j = i+1, j-1 {
			streams[i], streams[j] = streams[j], streams[i]
		}
	}

	// Build cursors from results
	var startCursor, endCursor string
	if len(streams) > 0 {
		first := streams[0]
		last := streams[len(streams)-1]
		startCursor = pagination.EncodeCursor(first.CreatedAt.AsTime(), first.InternalName)
		endCursor = pagination.EncodeCursor(last.CreatedAt.AsTime(), last.InternalName)
	}

	// Build response with proper hasNextPage/hasPreviousPage
	resp := &pb.ListStreamsResponse{
		Streams: streams,
		Pagination: &pb.CursorPaginationResponse{
			TotalCount: total,
		},
	}
	if startCursor != "" {
		resp.Pagination.StartCursor = &startCursor
	}
	if endCursor != "" {
		resp.Pagination.EndCursor = &endCursor
	}
	if params.Direction == pagination.Forward {
		resp.Pagination.HasNextPage = hasMore
		resp.Pagination.HasPreviousPage = params.Cursor != nil
	} else {
		resp.Pagination.HasPreviousPage = hasMore
		resp.Pagination.HasNextPage = params.Cursor != nil
	}

	return resp, nil
}

// UpdateStream updates a stream's properties
func (s *CommodoreServer) UpdateStream(ctx context.Context, req *pb.UpdateStreamRequest) (*pb.Stream, error) {
	userID, tenantID, err := extractUserContext(ctx)
	if err != nil {
		return nil, err
	}

	streamID := req.GetStreamId()
	if streamID == "" {
		return nil, status.Error(codes.InvalidArgument, "stream_id required")
	}

	// Verify ownership
	var exists bool
	err = s.db.QueryRowContext(ctx, `
		SELECT EXISTS(SELECT 1 FROM commodore.streams WHERE internal_name = $1 AND user_id = $2 AND tenant_id = $3)
	`, streamID, userID, tenantID).Scan(&exists)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "database error: %v", err)
	}
	if !exists {
		return nil, status.Error(codes.NotFound, "stream not found")
	}

	// Build update query dynamically
	var updates []string
	var args []interface{}
	argIdx := 1

	if req.Name != nil {
		updates = append(updates, fmt.Sprintf("title = $%d", argIdx))
		args = append(args, *req.Name)
		argIdx++
	}
	if req.Description != nil {
		updates = append(updates, fmt.Sprintf("description = $%d", argIdx))
		args = append(args, *req.Description)
		argIdx++
	}
	if req.Record != nil {
		updates = append(updates, fmt.Sprintf("is_recording_enabled = $%d", argIdx))
		args = append(args, *req.Record)
		argIdx++
	}

	if len(updates) > 0 {
		updates = append(updates, "updated_at = NOW()")
		query := fmt.Sprintf("UPDATE commodore.streams SET %s WHERE internal_name = $%d",
			strings.Join(updates, ", "), argIdx)
		args = append(args, streamID)

		_, err = s.db.ExecContext(ctx, query, args...)
		if err != nil {
			return nil, status.Errorf(codes.Internal, "failed to update stream: %v", err)
		}
	}

	// Handle dynamic DVR start/stop when recording config changes while stream is live
	if req.Record != nil && s.foghornClient != nil {
		newRecordingEnabled := *req.Record
		// Capture variables for goroutine
		internalName := streamID
		tid := tenantID

		go func() {
			// Check if stream is currently live
			contentType := "live"
			meta, err := s.foghornClient.GetStreamMeta(context.Background(), &pb.StreamMetaRequest{
				InternalName: internalName,
				ContentType:  &contentType,
			})
			if err != nil || meta.GetMetaSummary() == nil || !meta.GetMetaSummary().GetIsLive() {
				// Stream not live - config will take effect on next stream start
				return
			}

			// Stream is live - handle recording toggle
			if newRecordingEnabled {
				// Start DVR recording
				_, err := s.foghornClient.StartDVR(context.Background(), &pb.StartDVRRequest{
					TenantId:     tid,
					InternalName: internalName,
				})
				if err != nil {
					s.logger.WithError(err).WithField("internal_name", internalName).
						Error("Failed to start DVR after config change")
				} else {
					s.logger.WithField("internal_name", internalName).
						Info("Started DVR recording after config enabled while live")
				}
			} else {
				// Stop DVR - find active recording first
				recordings, err := s.foghornClient.ListDVRRecordings(context.Background(), tid, &internalName, nil)
				if err != nil {
					s.logger.WithError(err).Error("Failed to list DVR recordings")
					return
				}
				for _, rec := range recordings.GetDvrRecordings() {
					status := rec.GetStatus()
					if status == "recording" || status == "requested" || status == "starting" {
						_, err := s.foghornClient.StopDVR(context.Background(), rec.GetDvrHash(), &tid)
						if err != nil {
							s.logger.WithError(err).WithField("dvr_hash", rec.GetDvrHash()).
								Error("Failed to stop DVR after config change")
						} else {
							s.logger.WithField("internal_name", internalName).
								Info("Stopped DVR recording after config disabled while live")
						}
						break
					}
				}
			}
		}()
	}

	return s.queryStream(ctx, streamID, userID, tenantID)
}

// DeleteStream deletes a stream
func (s *CommodoreServer) DeleteStream(ctx context.Context, req *pb.DeleteStreamRequest) (*pb.DeleteStreamResponse, error) {
	userID, tenantID, err := extractUserContext(ctx)
	if err != nil {
		return nil, err
	}

	streamID := req.GetStreamId()
	if streamID == "" {
		return nil, status.Error(codes.InvalidArgument, "stream_id required")
	}

	// Get stream details before deletion
	var streamUUID, title string
	err = s.db.QueryRowContext(ctx, `
		SELECT id, title FROM commodore.streams
		WHERE internal_name = $1 AND user_id = $2 AND tenant_id = $3
	`, streamID, userID, tenantID).Scan(&streamUUID, &title)

	if err == sql.ErrNoRows {
		return nil, status.Error(codes.NotFound, "stream not found")
	}
	if err != nil {
		return nil, status.Errorf(codes.Internal, "database error: %v", err)
	}

	// Begin transaction
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to begin transaction: %v", err)
	}
	defer tx.Rollback()

	// Delete related stream_keys (use UUID, not internal_name)
	_, err = tx.ExecContext(ctx, `DELETE FROM commodore.stream_keys WHERE stream_id = $1`, streamUUID)
	if err != nil {
		s.logger.WithError(err).Warn("Failed to delete stream keys")
	}

	// Delete related clips via Foghorn gRPC (best-effort, don't fail stream deletion)
	if s.foghornClient != nil {
		clipsResp, err := s.foghornClient.GetClips(ctx, tenantID, &streamID, nil)
		if err != nil {
			s.logger.WithError(err).Warn("Failed to list clips for stream deletion cleanup")
		} else if clipsResp != nil {
			for _, clip := range clipsResp.Clips {
				if _, delErr := s.foghornClient.DeleteClip(ctx, clip.ClipHash, &tenantID); delErr != nil {
					s.logger.WithError(delErr).WithField("clip_hash", clip.ClipHash).Warn("Failed to delete clip during stream cleanup")
				}
			}
		}
	}

	// Delete the stream
	_, err = tx.ExecContext(ctx, `DELETE FROM commodore.streams WHERE id = $1`, streamUUID)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to delete stream: %v", err)
	}

	if err := tx.Commit(); err != nil {
		return nil, status.Errorf(codes.Internal, "failed to commit: %v", err)
	}

	return &pb.DeleteStreamResponse{
		Message:     "Stream deleted successfully",
		StreamId:    streamID,
		StreamTitle: title,
		DeletedAt:   timestamppb.Now(),
	}, nil
}

// RefreshStreamKey generates a new stream key
func (s *CommodoreServer) RefreshStreamKey(ctx context.Context, req *pb.RefreshStreamKeyRequest) (*pb.RefreshStreamKeyResponse, error) {
	userID, tenantID, err := extractUserContext(ctx)
	if err != nil {
		return nil, err
	}

	streamID := req.GetStreamId()
	if streamID == "" {
		return nil, status.Error(codes.InvalidArgument, "stream_id required")
	}

	// Generate new stream key
	newStreamKey := generateStreamKey()

	// Update the stream
	result, err := s.db.ExecContext(ctx, `
		UPDATE commodore.streams
		SET stream_key = $1, updated_at = NOW()
		WHERE internal_name = $2 AND user_id = $3 AND tenant_id = $4
	`, newStreamKey, streamID, userID, tenantID)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to refresh stream key: %v", err)
	}

	rows, _ := result.RowsAffected()
	if rows == 0 {
		return nil, status.Error(codes.NotFound, "stream not found")
	}

	// Get playback ID
	var playbackID string
	s.db.QueryRowContext(ctx, `SELECT playback_id FROM commodore.streams WHERE internal_name = $1`, streamID).Scan(&playbackID)

	return &pb.RefreshStreamKeyResponse{
		Message:           "Stream key refreshed successfully",
		StreamId:          streamID,
		StreamKey:         newStreamKey,
		PlaybackId:        playbackID,
		OldKeyInvalidated: true,
	}, nil
}

// ============================================================================
// STREAM KEY SERVICE (Gateway → Commodore for multi-key management)
// ============================================================================

// CreateStreamKey creates a new stream key for a stream
func (s *CommodoreServer) CreateStreamKey(ctx context.Context, req *pb.CreateStreamKeyRequest) (*pb.StreamKeyResponse, error) {
	userID, tenantID, err := extractUserContext(ctx)
	if err != nil {
		return nil, err
	}

	streamID := req.GetStreamId()
	if streamID == "" {
		return nil, status.Error(codes.InvalidArgument, "stream_id required")
	}

	// Verify stream ownership
	var streamUUID string
	err = s.db.QueryRowContext(ctx, `
		SELECT id FROM commodore.streams WHERE internal_name = $1 AND user_id = $2 AND tenant_id = $3
	`, streamID, userID, tenantID).Scan(&streamUUID)
	if err == sql.ErrNoRows {
		return nil, status.Error(codes.NotFound, "stream not found")
	}
	if err != nil {
		return nil, status.Errorf(codes.Internal, "database error: %v", err)
	}

	// Generate new key
	keyID := uuid.New().String()
	keyValue := generateStreamKey()
	keyName := req.GetKeyName()
	if keyName == "" {
		keyName = "Key " + time.Now().Format("2006-01-02 15:04")
	}

	_, err = s.db.ExecContext(ctx, `
		INSERT INTO commodore.stream_keys (id, tenant_id, user_id, stream_id, key_value, key_name, is_active)
		VALUES ($1, $2, $3, $4, $5, $6, true)
	`, keyID, tenantID, userID, streamUUID, keyValue, keyName)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to create stream key: %v", err)
	}

	return &pb.StreamKeyResponse{
		StreamKey: &pb.StreamKey{
			Id:        keyID,
			TenantId:  tenantID,
			UserId:    userID,
			StreamId:  streamID,
			KeyValue:  keyValue,
			KeyName:   keyName,
			IsActive:  true,
			CreatedAt: timestamppb.Now(),
			UpdatedAt: timestamppb.Now(),
		},
		Message: "Stream key created successfully",
	}, nil
}

// ListStreamKeys lists all keys for a stream
func (s *CommodoreServer) ListStreamKeys(ctx context.Context, req *pb.ListStreamKeysRequest) (*pb.ListStreamKeysResponse, error) {
	userID, tenantID, err := extractUserContext(ctx)
	if err != nil {
		return nil, err
	}

	streamID := req.GetStreamId()
	if streamID == "" {
		return nil, status.Error(codes.InvalidArgument, "stream_id required")
	}

	// Resolve internal_name to UUID
	var streamUUID string
	err = s.db.QueryRowContext(ctx, `
		SELECT id FROM commodore.streams WHERE internal_name = $1 AND user_id = $2 AND tenant_id = $3
	`, streamID, userID, tenantID).Scan(&streamUUID)
	if err == sql.ErrNoRows {
		return nil, status.Error(codes.NotFound, "stream not found")
	}
	if err != nil {
		return nil, status.Errorf(codes.Internal, "database error: %v", err)
	}

	// Parse bidirectional pagination
	params, err := pagination.Parse(req.GetPagination())
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid pagination: %v", err)
	}

	builder := &pagination.KeysetBuilder{
		TimestampColumn: "created_at",
		IDColumn:        "id",
	}

	// Get total count
	var total int32
	err = s.db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM commodore.stream_keys WHERE stream_id = $1
	`, streamUUID).Scan(&total)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "database error: %v", err)
	}

	// Build query with keyset pagination
	query := `
		SELECT id, tenant_id, user_id, stream_id, key_value, key_name, is_active, last_used_at, created_at, updated_at
		FROM commodore.stream_keys
		WHERE stream_id = $1`
	args := []interface{}{streamUUID}
	argIdx := 2

	// Add keyset condition if cursor provided
	if condition, cursorArgs := builder.Condition(params, argIdx); condition != "" {
		query += " AND " + condition
		args = append(args, cursorArgs...)
	}

	// Add ORDER BY and LIMIT
	query += " " + builder.OrderBy(params)
	query += fmt.Sprintf(" LIMIT %d", params.Limit+1)

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "database error: %v", err)
	}
	defer rows.Close()

	var keys []*pb.StreamKey
	for rows.Next() {
		var key pb.StreamKey
		var lastUsedAt sql.NullTime
		var createdAt, updatedAt time.Time

		err := rows.Scan(&key.Id, &key.TenantId, &key.UserId, &key.StreamId, &key.KeyValue, &key.KeyName,
			&key.IsActive, &lastUsedAt, &createdAt, &updatedAt)
		if err != nil {
			continue
		}

		key.CreatedAt = timestamppb.New(createdAt)
		key.UpdatedAt = timestamppb.New(updatedAt)
		if lastUsedAt.Valid {
			key.LastUsedAt = timestamppb.New(lastUsedAt.Time)
		}
		keys = append(keys, &key)
	}

	// Detect hasMore and trim results
	hasMore := len(keys) > params.Limit
	if hasMore {
		keys = keys[:params.Limit]
	}

	// Reverse results if backward pagination
	if params.Direction == pagination.Backward && len(keys) > 0 {
		for i, j := 0, len(keys)-1; i < j; i, j = i+1, j-1 {
			keys[i], keys[j] = keys[j], keys[i]
		}
	}

	// Build cursors from results
	var startCursor, endCursor string
	if len(keys) > 0 {
		first := keys[0]
		last := keys[len(keys)-1]
		startCursor = pagination.EncodeCursor(first.CreatedAt.AsTime(), first.Id)
		endCursor = pagination.EncodeCursor(last.CreatedAt.AsTime(), last.Id)
	}

	// Build response with proper hasNextPage/hasPreviousPage
	resp := &pb.ListStreamKeysResponse{
		StreamKeys: keys,
		Pagination: &pb.CursorPaginationResponse{
			TotalCount: total,
		},
	}
	if startCursor != "" {
		resp.Pagination.StartCursor = &startCursor
	}
	if endCursor != "" {
		resp.Pagination.EndCursor = &endCursor
	}
	if params.Direction == pagination.Forward {
		resp.Pagination.HasNextPage = hasMore
		resp.Pagination.HasPreviousPage = params.Cursor != nil
	} else {
		resp.Pagination.HasPreviousPage = hasMore
		resp.Pagination.HasNextPage = params.Cursor != nil
	}

	return resp, nil
}

// DeactivateStreamKey deactivates a stream key
func (s *CommodoreServer) DeactivateStreamKey(ctx context.Context, req *pb.DeactivateStreamKeyRequest) (*emptypb.Empty, error) {
	userID, tenantID, err := extractUserContext(ctx)
	if err != nil {
		return nil, err
	}

	// Resolve internal_name to UUID
	var streamUUID string
	err = s.db.QueryRowContext(ctx, `
		SELECT id FROM commodore.streams WHERE internal_name = $1 AND user_id = $2 AND tenant_id = $3
	`, req.GetStreamId(), userID, tenantID).Scan(&streamUUID)
	if err == sql.ErrNoRows {
		return nil, status.Error(codes.NotFound, "stream not found")
	}
	if err != nil {
		return nil, status.Errorf(codes.Internal, "database error: %v", err)
	}

	result, err := s.db.ExecContext(ctx, `
		UPDATE commodore.stream_keys SET is_active = false, updated_at = NOW()
		WHERE id = $1 AND stream_id = $2
	`, req.GetKeyId(), streamUUID)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to deactivate key: %v", err)
	}

	rows, _ := result.RowsAffected()
	if rows == 0 {
		return nil, status.Error(codes.NotFound, "stream key not found")
	}

	return &emptypb.Empty{}, nil
}

// ============================================================================
// DEVELOPER SERVICE (Gateway → Commodore for API token management)
// ============================================================================

// CreateAPIToken creates a new API token
func (s *CommodoreServer) CreateAPIToken(ctx context.Context, req *pb.CreateAPITokenRequest) (*pb.CreateAPITokenResponse, error) {
	userID, tenantID, err := extractUserContext(ctx)
	if err != nil {
		return nil, err
	}

	tokenID := uuid.New().String()
	tokenValue := "fw_" + generateSecureToken(32)
	tokenName := req.GetTokenName()
	if tokenName == "" {
		tokenName = "API Token " + time.Now().Format("2006-01-02")
	}

	permissions := req.GetPermissions()
	if len(permissions) == 0 {
		permissions = []string{"read"}
	}

	var expiresAt sql.NullTime
	if req.GetExpiresAt() != nil {
		expiresAt = sql.NullTime{Time: req.GetExpiresAt().AsTime(), Valid: true}
	}

	_, err = s.db.ExecContext(ctx, `
		INSERT INTO commodore.api_tokens (id, tenant_id, user_id, token_value, token_name, permissions, is_active, expires_at)
		VALUES ($1, $2, $3, $4, $5, $6, true, $7)
	`, tokenID, tenantID, userID, tokenValue, tokenName, pq.Array(permissions), expiresAt)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to create API token: %v", err)
	}

	resp := &pb.CreateAPITokenResponse{
		Id:          tokenID,
		TokenValue:  tokenValue,
		TokenName:   tokenName,
		Permissions: permissions,
		CreatedAt:   timestamppb.Now(),
		Message:     "API token created successfully",
	}
	if expiresAt.Valid {
		resp.ExpiresAt = timestamppb.New(expiresAt.Time)
	}

	return resp, nil
}

// ListAPITokens lists all API tokens for the user
func (s *CommodoreServer) ListAPITokens(ctx context.Context, req *pb.ListAPITokensRequest) (*pb.ListAPITokensResponse, error) {
	userID, tenantID, err := extractUserContext(ctx)
	if err != nil {
		return nil, err
	}

	// Parse bidirectional pagination
	params, err := pagination.Parse(req.GetPagination())
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid pagination: %v", err)
	}

	builder := &pagination.KeysetBuilder{
		TimestampColumn: "created_at",
		IDColumn:        "id",
	}

	// Get total count
	var total int32
	err = s.db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM commodore.api_tokens WHERE user_id = $1 AND tenant_id = $2
	`, userID, tenantID).Scan(&total)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "database error: %v", err)
	}

	// Build query with keyset pagination
	query := `
		SELECT id, token_name, permissions,
		       CASE WHEN is_active AND (expires_at IS NULL OR expires_at > NOW()) THEN 'active' ELSE 'inactive' END as status,
		       last_used_at, expires_at, created_at
		FROM commodore.api_tokens
		WHERE user_id = $1 AND tenant_id = $2`
	args := []interface{}{userID, tenantID}
	argIdx := 3

	// Add keyset condition if cursor provided
	if condition, cursorArgs := builder.Condition(params, argIdx); condition != "" {
		query += " AND " + condition
		args = append(args, cursorArgs...)
	}

	// Add ORDER BY and LIMIT
	query += " " + builder.OrderBy(params)
	query += fmt.Sprintf(" LIMIT %d", params.Limit+1)

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "database error: %v", err)
	}
	defer rows.Close()

	var tokens []*pb.APITokenInfo
	for rows.Next() {
		var token pb.APITokenInfo
		var permissions []string
		var lastUsedAt, expiresAt sql.NullTime
		var createdAt time.Time

		err := rows.Scan(&token.Id, &token.TokenName, pq.Array(&permissions), &token.Status,
			&lastUsedAt, &expiresAt, &createdAt)
		if err != nil {
			continue
		}

		token.Permissions = permissions
		token.CreatedAt = timestamppb.New(createdAt)
		if lastUsedAt.Valid {
			token.LastUsedAt = timestamppb.New(lastUsedAt.Time)
		}
		if expiresAt.Valid {
			token.ExpiresAt = timestamppb.New(expiresAt.Time)
		}
		tokens = append(tokens, &token)
	}

	// Detect hasMore and trim results
	hasMore := len(tokens) > params.Limit
	if hasMore {
		tokens = tokens[:params.Limit]
	}

	// Reverse results if backward pagination
	if params.Direction == pagination.Backward && len(tokens) > 0 {
		for i, j := 0, len(tokens)-1; i < j; i, j = i+1, j-1 {
			tokens[i], tokens[j] = tokens[j], tokens[i]
		}
	}

	// Build cursors from results
	var startCursor, endCursor string
	if len(tokens) > 0 {
		first := tokens[0]
		last := tokens[len(tokens)-1]
		startCursor = pagination.EncodeCursor(first.CreatedAt.AsTime(), first.Id)
		endCursor = pagination.EncodeCursor(last.CreatedAt.AsTime(), last.Id)
	}

	// Build response with proper hasNextPage/hasPreviousPage
	resp := &pb.ListAPITokensResponse{
		Tokens: tokens,
		Pagination: &pb.CursorPaginationResponse{
			TotalCount: total,
		},
	}
	if startCursor != "" {
		resp.Pagination.StartCursor = &startCursor
	}
	if endCursor != "" {
		resp.Pagination.EndCursor = &endCursor
	}
	if params.Direction == pagination.Forward {
		resp.Pagination.HasNextPage = hasMore
		resp.Pagination.HasPreviousPage = params.Cursor != nil
	} else {
		resp.Pagination.HasPreviousPage = hasMore
		resp.Pagination.HasNextPage = params.Cursor != nil
	}

	return resp, nil
}

// RevokeAPIToken revokes an API token
func (s *CommodoreServer) RevokeAPIToken(ctx context.Context, req *pb.RevokeAPITokenRequest) (*pb.RevokeAPITokenResponse, error) {
	userID, tenantID, err := extractUserContext(ctx)
	if err != nil {
		return nil, err
	}

	// Get token info before revoking
	var tokenName string
	err = s.db.QueryRowContext(ctx, `
		SELECT token_name FROM commodore.api_tokens WHERE id = $1 AND user_id = $2 AND tenant_id = $3
	`, req.GetTokenId(), userID, tenantID).Scan(&tokenName)
	if err == sql.ErrNoRows {
		return nil, status.Error(codes.NotFound, "token not found")
	}
	if err != nil {
		return nil, status.Errorf(codes.Internal, "database error: %v", err)
	}

	// Revoke the token
	_, err = s.db.ExecContext(ctx, `
		UPDATE commodore.api_tokens SET is_active = false, updated_at = NOW() WHERE id = $1
	`, req.GetTokenId())
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to revoke token: %v", err)
	}

	return &pb.RevokeAPITokenResponse{
		Message:   "Token revoked successfully",
		TokenId:   req.GetTokenId(),
		TokenName: tokenName,
		RevokedAt: timestamppb.Now(),
	}, nil
}

// ============================================================================
// HELPER FUNCTIONS
// ============================================================================

func (s *CommodoreServer) queryStream(ctx context.Context, streamID, userID, tenantID string) (*pb.Stream, error) {
	var stream pb.Stream
	var description sql.NullString
	var createdAt, updatedAt time.Time

	// Query config only - operational state (status, started_at, ended_at) comes from Periscope Data Plane
	err := s.db.QueryRowContext(ctx, `
		SELECT internal_name, stream_key, playback_id, title, description,
		       is_recording_enabled, created_at, updated_at
		FROM commodore.streams
		WHERE internal_name = $1 AND user_id = $2 AND tenant_id = $3
	`, streamID, userID, tenantID).Scan(&stream.InternalName, &stream.StreamKey, &stream.PlaybackId,
		&stream.Title, &description, &stream.IsRecordingEnabled, &createdAt, &updatedAt)

	if err == sql.ErrNoRows {
		return nil, status.Error(codes.NotFound, "stream not found")
	}
	if err != nil {
		return nil, status.Errorf(codes.Internal, "database error: %v", err)
	}

	if description.Valid {
		stream.Description = description.String
	}
	stream.IsRecording = stream.IsRecordingEnabled
	// Note: IsLive, Status, StartedAt, EndedAt are now set by Gateway from Periscope (Data Plane)
	stream.CreatedAt = timestamppb.New(createdAt)
	stream.UpdatedAt = timestamppb.New(updatedAt)

	return &stream, nil
}

// scanStream scans config-only stream data; operational state comes from Periscope Data Plane
func scanStream(rows *sql.Rows) (*pb.Stream, error) {
	var stream pb.Stream
	var description sql.NullString
	var createdAt, updatedAt time.Time

	err := rows.Scan(&stream.InternalName, &stream.StreamKey, &stream.PlaybackId,
		&stream.Title, &description, &stream.IsRecordingEnabled, &createdAt, &updatedAt)
	if err != nil {
		return nil, err
	}

	if description.Valid {
		stream.Description = description.String
	}
	stream.IsRecording = stream.IsRecordingEnabled
	// Note: IsLive, Status, StartedAt, EndedAt are now set by Gateway from Periscope (Data Plane)
	stream.CreatedAt = timestamppb.New(createdAt)
	stream.UpdatedAt = timestamppb.New(updatedAt)

	return &stream, nil
}

func extractUserContext(ctx context.Context) (userID, tenantID string, err error) {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return "", "", status.Error(codes.Unauthenticated, "missing metadata")
	}

	userIDs := md.Get("x-user-id")
	tenantIDs := md.Get("x-tenant-id")

	if len(userIDs) == 0 || len(tenantIDs) == 0 {
		return "", "", status.Error(codes.Unauthenticated, "missing user context")
	}

	return userIDs[0], tenantIDs[0], nil
}

func generateDVRHash() string {
	return time.Now().Format("20060102150405") + generateSecureToken(4)
}

func generateClipHash() string {
	return time.Now().Format("20060102150405") + generateSecureToken(8)
}

func generateVodHash() string {
	return time.Now().Format("20060102150405") + generateSecureToken(8)
}

func generateStreamKey() string {
	return "sk_" + generateSecureToken(16)
}

func generateSecureToken(n int) string {
	b := make([]byte, n)
	rand.Read(b)
	return hex.EncodeToString(b)
}

func getDefaultPermissions(role string) []string {
	switch role {
	case "owner", "admin":
		return []string{"read", "write", "admin"}
	case "member":
		return []string{"read", "write"}
	default:
		return []string{"read"}
	}
}

func encodeOffsetToCursor(offset int32) string {
	return fmt.Sprintf("offset:%d", offset)
}

func decodeCursorToOffset(cursor string) int32 {
	var offset int32
	fmt.Sscanf(cursor, "offset:%d", &offset)
	return offset
}

// ============================================================================
// CLIP SERVICE (Commodore → Foghorn proxy)
// ============================================================================

// CreateClip registers clip in business registry and orchestrates creation via Foghorn
func (s *CommodoreServer) CreateClip(ctx context.Context, req *pb.CreateClipRequest) (*pb.CreateClipResponse, error) {
	if s.foghornClient == nil {
		return nil, status.Error(codes.Unavailable, "Foghorn client not configured")
	}

	// Get user and tenant context from metadata
	userID, tenantID, err := extractUserContext(ctx)
	if err != nil {
		return nil, err
	}

	// Look up stream_id from internal_name
	var streamID string
	_ = s.db.QueryRowContext(ctx, `
		SELECT id::text FROM commodore.streams WHERE internal_name = $1 AND tenant_id = $2
	`, req.InternalName, tenantID).Scan(&streamID)

	// Generate clip hash (Commodore is authoritative)
	clipHash := generateClipHash()
	clipID := uuid.New().String()

	// Resolve timing for storage
	var startTime, duration int64
	if req.StartUnix != nil {
		startTime = *req.StartUnix * 1000 // Convert to ms
	}
	if req.DurationSec != nil {
		duration = *req.DurationSec * 1000 // Convert to ms
	} else if req.StartUnix != nil && req.StopUnix != nil {
		duration = (*req.StopUnix - *req.StartUnix) * 1000
	}

	// Resolve retention
	var retentionUntil *time.Time
	if req.ExpiresAt != nil {
		t := time.Unix(*req.ExpiresAt, 0)
		retentionUntil = &t
	} else {
		t := time.Now().Add(30 * 24 * time.Hour) // Default 30 days
		retentionUntil = &t
	}

	// Store requested params as JSON for audit
	requestedParams := map[string]interface{}{}
	if req.StartUnix != nil {
		requestedParams["start_unix"] = *req.StartUnix
	}
	if req.StopUnix != nil {
		requestedParams["stop_unix"] = *req.StopUnix
	}
	if req.DurationSec != nil {
		requestedParams["duration_sec"] = *req.DurationSec
	}
	paramsJSON, _ := json.Marshal(requestedParams)

	// Register in commodore.clips (business registry)
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO commodore.clips (
			id, tenant_id, user_id, stream_id, clip_hash,
			title, description, start_time, duration, clip_mode, requested_params,
			retention_until, created_at, updated_at
		) VALUES ($1, $2, $3, NULLIF($4, '')::uuid, $5, $6, $7, $8, $9, $10, $11, $12, NOW(), NOW())
	`, clipID, tenantID, userID, streamID, clipHash,
		req.Title, req.Description, startTime, duration, req.Mode.String(), string(paramsJSON),
		retentionUntil)

	if err != nil {
		s.logger.WithFields(logging.Fields{
			"tenant_id":     tenantID,
			"internal_name": req.InternalName,
			"error":         err,
		}).Error("Failed to register clip in business registry")
		return nil, status.Errorf(codes.Internal, "failed to register clip: %v", err)
	}

	s.logger.WithFields(logging.Fields{
		"tenant_id":     tenantID,
		"clip_hash":     clipHash,
		"clip_id":       clipID,
		"internal_name": req.InternalName,
	}).Info("Registered clip in business registry")

	// Build Foghorn request with pre-generated hash
	foghornReq := &pb.CreateClipRequest{
		TenantId:     tenantID,
		InternalName: req.InternalName,
		ClipHash:     &clipHash, // Pass the hash we generated
	}
	if req.Format != "" {
		foghornReq.Format = req.Format
	}
	foghornReq.StartUnix = req.StartUnix
	foghornReq.StopUnix = req.StopUnix
	foghornReq.StartMs = req.StartMs
	foghornReq.StopMs = req.StopMs
	foghornReq.DurationSec = req.DurationSec
	foghornReq.ExpiresAt = func() *int64 { t := retentionUntil.Unix(); return &t }()

	// Call Foghorn for artifact lifecycle management
	resp, err := s.foghornClient.CreateClip(ctx, foghornReq)
	if err != nil {
		s.logger.WithError(err).WithField("clip_hash", clipHash).Error("Failed to create clip artifact via Foghorn")
		// Don't rollback business registry - Foghorn can retry later
		return nil, status.Errorf(codes.Internal, "failed to create clip artifact: %v", err)
	}

	return resp, nil
}

// GetClips returns clips from Commodore business registry
// Lifecycle data (status, size, storage) comes from Periscope via GraphQL field resolvers
func (s *CommodoreServer) GetClips(ctx context.Context, req *pb.GetClipsRequest) (*pb.GetClipsResponse, error) {
	_, tenantID, err := extractUserContext(ctx)
	if err != nil {
		return nil, err
	}

	params, err := pagination.Parse(req.GetPagination())
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid pagination: %v", err)
	}

	// Build WHERE clause with optional stream filter
	whereClause := "c.tenant_id = $1"
	args := []interface{}{tenantID}
	argIdx := 2

	if internalName := req.GetInternalName(); internalName != "" {
		whereClause += fmt.Sprintf(" AND st.internal_name = $%d", argIdx)
		args = append(args, internalName)
		argIdx++
	}

	// Get total count
	var total int32
	countQuery := fmt.Sprintf(`
		SELECT COUNT(*) FROM commodore.clips c
		LEFT JOIN commodore.streams st ON c.stream_id = st.id
		WHERE %s`, whereClause)
	if err := s.db.QueryRowContext(ctx, countQuery, args...).Scan(&total); err != nil {
		return nil, status.Errorf(codes.Internal, "database error: %v", err)
	}

	// Build keyset pagination query
	builder := &pagination.KeysetBuilder{
		TimestampColumn: "c.created_at",
		IDColumn:        "c.clip_hash",
	}

	// Base query - join with streams to get internal_name (stream_name)
	query := fmt.Sprintf(`
		SELECT c.id, c.clip_hash, COALESCE(st.internal_name, ''), c.title, c.description,
		       c.start_time, c.duration, c.clip_mode, c.requested_params,
		       c.retention_until, c.created_at, c.updated_at
		FROM commodore.clips c
		LEFT JOIN commodore.streams st ON c.stream_id = st.id
		WHERE %s`, whereClause)

	// Add keyset condition if cursor provided
	if condition, cursorArgs := builder.Condition(params, argIdx); condition != "" {
		query += " AND " + condition
		args = append(args, cursorArgs...)
	}

	// Add ORDER BY and LIMIT
	query += " " + builder.OrderBy(params)
	query += fmt.Sprintf(" LIMIT %d", params.Limit+1)

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "database error: %v", err)
	}
	defer rows.Close()

	var clips []*pb.ClipInfo
	for rows.Next() {
		var (
			id, clipHash, streamName      string
			title, description            sql.NullString
			startTime, duration           int64
			clipMode                      sql.NullString
			requestedParams               sql.NullString
			retentionUntil                sql.NullTime
			createdAt, updatedAt          time.Time
		)
		if err := rows.Scan(&id, &clipHash, &streamName, &title, &description,
			&startTime, &duration, &clipMode, &requestedParams,
			&retentionUntil, &createdAt, &updatedAt); err != nil {
			s.logger.WithError(err).Warn("Error scanning clip")
			continue
		}

		clip := &pb.ClipInfo{
			Id:         id,
			ClipHash:   clipHash,
			StreamName: streamName,
			StartTime:  startTime / 1000, // Convert ms to seconds
			Duration:   duration / 1000,  // Convert ms to seconds
			Status:     "registry",       // Indicates business registry data, lifecycle from Foghorn
			CreatedAt:  timestamppb.New(createdAt),
			UpdatedAt:  timestamppb.New(updatedAt),
		}
		if title.Valid {
			clip.Title = title.String
		}
		if description.Valid {
			clip.Description = description.String
		}
		if clipMode.Valid {
			clip.ClipMode = &clipMode.String
		}
		if requestedParams.Valid {
			clip.RequestedParams = &requestedParams.String
		}
		if retentionUntil.Valid {
			expiresAt := timestamppb.New(retentionUntil.Time)
			clip.ExpiresAt = expiresAt
		}

		clips = append(clips, clip)
	}

	// Detect hasMore and trim results
	hasMore := len(clips) > params.Limit
	if hasMore {
		clips = clips[:params.Limit]
	}

	// Reverse results if backward pagination
	if params.Direction == pagination.Backward && len(clips) > 0 {
		for i, j := 0, len(clips)-1; i < j; i, j = i+1, j-1 {
			clips[i], clips[j] = clips[j], clips[i]
		}
	}

	// Build cursors from results
	var startCursor, endCursor string
	if len(clips) > 0 {
		first := clips[0]
		last := clips[len(clips)-1]
		startCursor = pagination.EncodeCursor(first.CreatedAt.AsTime(), first.ClipHash)
		endCursor = pagination.EncodeCursor(last.CreatedAt.AsTime(), last.ClipHash)
	}

	// Build response
	resp := &pb.GetClipsResponse{
		Clips: clips,
		Pagination: &pb.CursorPaginationResponse{
			TotalCount: total,
		},
	}
	if startCursor != "" {
		resp.Pagination.StartCursor = &startCursor
	}
	if endCursor != "" {
		resp.Pagination.EndCursor = &endCursor
	}
	if params.Direction == pagination.Forward {
		resp.Pagination.HasNextPage = hasMore
		resp.Pagination.HasPreviousPage = params.Cursor != nil
	} else {
		resp.Pagination.HasPreviousPage = hasMore
		resp.Pagination.HasNextPage = params.Cursor != nil
	}

	return resp, nil
}

// GetClip proxies single clip fetch to Foghorn
func (s *CommodoreServer) GetClip(ctx context.Context, req *pb.GetClipRequest) (*pb.ClipInfo, error) {
	if s.foghornClient == nil {
		return nil, status.Error(codes.Unavailable, "Foghorn client not configured")
	}

	_, tenantID, err := extractUserContext(ctx)
	if err != nil {
		return nil, err
	}

	resp, err := s.foghornClient.GetClip(ctx, req.GetClipHash(), &tenantID)
	if err != nil {
		s.logger.WithError(err).Error("Failed to get clip from Foghorn")
		return nil, status.Errorf(codes.Internal, "failed to get clip: %v", err)
	}

	return resp, nil
}

// GetClipURLs proxies clip URL generation to Foghorn
func (s *CommodoreServer) GetClipURLs(ctx context.Context, req *pb.GetClipURLsRequest) (*pb.ClipViewingURLs, error) {
	if s.foghornClient == nil {
		return nil, status.Error(codes.Unavailable, "Foghorn client not configured")
	}

	_, tenantID, err := extractUserContext(ctx)
	if err != nil {
		return nil, err
	}

	resp, err := s.foghornClient.GetClipURLs(ctx, req.ClipHash, &tenantID)
	if err != nil {
		s.logger.WithError(err).Error("Failed to get clip URLs from Foghorn")
		return nil, status.Errorf(codes.Internal, "failed to get clip URLs: %v", err)
	}

	return resp, nil
}

// DeleteClip proxies clip deletion to Foghorn
func (s *CommodoreServer) DeleteClip(ctx context.Context, req *pb.DeleteClipRequest) (*pb.DeleteClipResponse, error) {
	if s.foghornClient == nil {
		return nil, status.Error(codes.Unavailable, "Foghorn client not configured")
	}

	_, tenantID, err := extractUserContext(ctx)
	if err != nil {
		return nil, err
	}

	resp, err := s.foghornClient.DeleteClip(ctx, req.ClipHash, &tenantID)
	if err != nil {
		s.logger.WithError(err).Error("Failed to delete clip via Foghorn")
		return nil, status.Errorf(codes.Internal, "failed to delete clip: %v", err)
	}

	return resp, nil
}

// ============================================================================
// DVR SERVICE (Commodore → Foghorn proxy)
// ============================================================================

// StopDVR proxies DVR stop to Foghorn
func (s *CommodoreServer) StopDVR(ctx context.Context, req *pb.StopDVRRequest) (*pb.StopDVRResponse, error) {
	if s.foghornClient == nil {
		return nil, status.Error(codes.Unavailable, "Foghorn client not configured")
	}

	_, tenantID, err := extractUserContext(ctx)
	if err != nil {
		return nil, err
	}

	resp, err := s.foghornClient.StopDVR(ctx, req.DvrHash, &tenantID)
	if err != nil {
		s.logger.WithError(err).Error("Failed to stop DVR via Foghorn")
		return nil, status.Errorf(codes.Internal, "failed to stop DVR: %v", err)
	}

	return resp, nil
}

// DeleteDVR proxies DVR deletion to Foghorn
func (s *CommodoreServer) DeleteDVR(ctx context.Context, req *pb.DeleteDVRRequest) (*pb.DeleteDVRResponse, error) {
	if s.foghornClient == nil {
		return nil, status.Error(codes.Unavailable, "Foghorn client not configured")
	}

	_, tenantID, err := extractUserContext(ctx)
	if err != nil {
		return nil, err
	}

	resp, err := s.foghornClient.DeleteDVR(ctx, req.DvrHash, &tenantID)
	if err != nil {
		s.logger.WithError(err).Error("Failed to delete DVR via Foghorn")
		return nil, status.Errorf(codes.Internal, "failed to delete DVR: %v", err)
	}

	return resp, nil
}

// ListDVRRequests returns DVR recordings from Commodore business registry
// Lifecycle data (status, size, storage) comes from Periscope via GraphQL field resolvers
func (s *CommodoreServer) ListDVRRequests(ctx context.Context, req *pb.ListDVRRecordingsRequest) (*pb.ListDVRRecordingsResponse, error) {
	_, tenantID, err := extractUserContext(ctx)
	if err != nil {
		return nil, err
	}

	params, err := pagination.Parse(req.GetPagination())
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid pagination: %v", err)
	}

	// Build WHERE clause with optional stream filter
	whereClause := "d.tenant_id = $1"
	args := []interface{}{tenantID}
	argIdx := 2

	if internalName := req.GetInternalName(); internalName != "" {
		whereClause += fmt.Sprintf(" AND d.internal_name = $%d", argIdx)
		args = append(args, internalName)
		argIdx++
	}

	// Get total count
	var total int32
	countQuery := fmt.Sprintf("SELECT COUNT(*) FROM commodore.dvr_recordings d WHERE %s", whereClause)
	if err := s.db.QueryRowContext(ctx, countQuery, args...).Scan(&total); err != nil {
		return nil, status.Errorf(codes.Internal, "database error: %v", err)
	}

	// Build keyset pagination query
	builder := &pagination.KeysetBuilder{
		TimestampColumn: "d.created_at",
		IDColumn:        "d.dvr_hash",
	}

	// Base query - join with streams to get title
	query := fmt.Sprintf(`
		SELECT d.id, d.dvr_hash, d.internal_name, COALESCE(st.title, d.internal_name),
		       d.retention_until, d.created_at, d.updated_at
		FROM commodore.dvr_recordings d
		LEFT JOIN commodore.streams st ON d.stream_id = st.id
		WHERE %s`, whereClause)

	// Add keyset condition if cursor provided
	if condition, cursorArgs := builder.Condition(params, argIdx); condition != "" {
		query += " AND " + condition
		args = append(args, cursorArgs...)
	}

	// Add ORDER BY and LIMIT
	query += " " + builder.OrderBy(params)
	query += fmt.Sprintf(" LIMIT %d", params.Limit+1)

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "database error: %v", err)
	}
	defer rows.Close()

	var recordings []*pb.DVRInfo
	for rows.Next() {
		var (
			id, dvrHash, internalName, title string
			retentionUntil                   sql.NullTime
			createdAt, updatedAt             time.Time
		)
		if err := rows.Scan(&id, &dvrHash, &internalName, &title,
			&retentionUntil, &createdAt, &updatedAt); err != nil {
			s.logger.WithError(err).Warn("Error scanning DVR recording")
			continue
		}

		recording := &pb.DVRInfo{
			Id:           &id,
			DvrHash:      dvrHash,
			InternalName: internalName,
			Title:        &title,
			CreatedAt:    timestamppb.New(createdAt),
			UpdatedAt:    timestamppb.New(updatedAt),
		}
		if retentionUntil.Valid {
			expiresAt := timestamppb.New(retentionUntil.Time)
			recording.ExpiresAt = expiresAt
		}

		recordings = append(recordings, recording)
	}

	// Detect hasMore and trim results
	hasMore := len(recordings) > params.Limit
	if hasMore {
		recordings = recordings[:params.Limit]
	}

	// Reverse results if backward pagination
	if params.Direction == pagination.Backward && len(recordings) > 0 {
		for i, j := 0, len(recordings)-1; i < j; i, j = i+1, j-1 {
			recordings[i], recordings[j] = recordings[j], recordings[i]
		}
	}

	// Build cursors from results
	var startCursor, endCursor string
	if len(recordings) > 0 {
		first := recordings[0]
		last := recordings[len(recordings)-1]
		startCursor = pagination.EncodeCursor(first.CreatedAt.AsTime(), first.DvrHash)
		endCursor = pagination.EncodeCursor(last.CreatedAt.AsTime(), last.DvrHash)
	}

	// Build response
	resp := &pb.ListDVRRecordingsResponse{
		DvrRecordings: recordings,
		Pagination: &pb.CursorPaginationResponse{
			TotalCount: total,
		},
	}
	if startCursor != "" {
		resp.Pagination.StartCursor = &startCursor
	}
	if endCursor != "" {
		resp.Pagination.EndCursor = &endCursor
	}
	if params.Direction == pagination.Forward {
		resp.Pagination.HasNextPage = hasMore
		resp.Pagination.HasPreviousPage = params.Cursor != nil
	} else {
		resp.Pagination.HasPreviousPage = hasMore
		resp.Pagination.HasNextPage = params.Cursor != nil
	}

	return resp, nil
}

// GetDVRStatus proxies DVR status check to Foghorn
func (s *CommodoreServer) GetDVRStatus(ctx context.Context, req *pb.GetDVRStatusRequest) (*pb.DVRInfo, error) {
	if s.foghornClient == nil {
		return nil, status.Error(codes.Unavailable, "Foghorn client not configured")
	}

	resp, err := s.foghornClient.GetDVRStatus(ctx, req.DvrHash)
	if err != nil {
		s.logger.WithError(err).Error("Failed to get DVR status from Foghorn")
		return nil, status.Errorf(codes.Internal, "failed to get DVR status: %v", err)
	}

	return resp, nil
}

// ============================================================================
// VIEWER SERVICE (Commodore → Foghorn proxy)
// ============================================================================

// ResolveViewerEndpoint proxies viewer endpoint resolution to Foghorn
// and enriches the response with stream metadata from Commodore's database
func (s *CommodoreServer) ResolveViewerEndpoint(ctx context.Context, req *pb.ViewerEndpointRequest) (*pb.ViewerEndpointResponse, error) {
	if s.foghornClient == nil {
		return nil, status.Error(codes.Unavailable, "Foghorn client not configured")
	}

	resp, err := s.foghornClient.ResolveViewerEndpoint(ctx, req.ContentType, req.ContentId, req.ViewerIp)
	if err != nil {
		s.logger.WithError(err).Error("Failed to resolve viewer endpoint from Foghorn")
		return nil, status.Errorf(codes.Internal, "failed to resolve viewer endpoint: %v", err)
	}

	// Enrich metadata with stream info from Commodore's database
	// For live streams, Foghorn doesn't have title/description - we do
	if req.ContentType == "live" && resp.Metadata != nil {
		var title, description sql.NullString
		err := s.db.QueryRowContext(ctx, `
			SELECT title, description FROM commodore.streams WHERE internal_name = $1
		`, req.ContentId).Scan(&title, &description)

		if err == nil {
			if title.Valid && title.String != "" {
				resp.Metadata.Title = &title.String
			}
			if description.Valid && description.String != "" {
				resp.Metadata.Description = &description.String
			}
		}
		// Silently ignore errors - enrichment is best-effort, don't fail the request
	}

	return resp, nil
}

// GetStreamMeta proxies stream metadata fetch to Foghorn
func (s *CommodoreServer) GetStreamMeta(ctx context.Context, req *pb.StreamMetaRequest) (*pb.StreamMetaResponse, error) {
	if s.foghornClient == nil {
		return nil, status.Error(codes.Unavailable, "Foghorn client not configured")
	}

	resp, err := s.foghornClient.GetStreamMeta(ctx, req)
	if err != nil {
		s.logger.WithError(err).Error("Failed to get stream meta from Foghorn")
		return nil, status.Errorf(codes.Internal, "failed to get stream meta: %v", err)
	}

	return resp, nil
}

// ResolveIngestEndpoint proxies ingest endpoint resolution to Foghorn
// and enriches the response with stream metadata from Commodore's database
func (s *CommodoreServer) ResolveIngestEndpoint(ctx context.Context, req *pb.IngestEndpointRequest) (*pb.IngestEndpointResponse, error) {
	if s.foghornClient == nil {
		return nil, status.Error(codes.Unavailable, "Foghorn client not configured")
	}

	resp, err := s.foghornClient.ResolveIngestEndpoint(ctx, req.StreamKey, req.ViewerIp)
	if err != nil {
		s.logger.WithError(err).Error("Failed to resolve ingest endpoint from Foghorn")
		return nil, status.Errorf(codes.Internal, "failed to resolve ingest endpoint: %v", err)
	}

	// Enrich metadata with stream info from Commodore's database if we have internal name
	if resp.Metadata != nil && resp.Metadata.StreamId != "" {
		var title sql.NullString
		err := s.db.QueryRowContext(ctx, `
			SELECT title FROM commodore.streams WHERE internal_name = $1
		`, resp.Metadata.StreamId).Scan(&title)

		if err == nil && title.Valid && title.String != "" {
			resp.Metadata.StreamId = title.String // Use title as display name if available
		}
		// Silently ignore errors - enrichment is best-effort
	}

	return resp, nil
}

// ============================================================================
// SERVER SETUP
// ============================================================================

// NewGRPCServer creates a new gRPC server for Commodore with all services registered
func NewGRPCServer(cfg CommodoreServerConfig) *grpc.Server {
	// Chain auth interceptor with logging interceptor
	authInterceptor := middleware.GRPCAuthInterceptor(middleware.GRPCAuthConfig{
		ServiceToken: cfg.ServiceToken,
		JWTSecret:    cfg.JWTSecret,
		Logger:       cfg.Logger,
		SkipMethods: []string{
			"/grpc.health.v1.Health/Check",
			"/grpc.health.v1.Health/Watch",
		},
	})

	opts := []grpc.ServerOption{
		grpc.ChainUnaryInterceptor(authInterceptor, unaryInterceptor(cfg.Logger)),
	}

	server := grpc.NewServer(opts...)
	commodoreServer := NewCommodoreServer(cfg)

	// Register all services
	pb.RegisterInternalServiceServer(server, commodoreServer)
	pb.RegisterUserServiceServer(server, commodoreServer)
	pb.RegisterStreamServiceServer(server, commodoreServer)
	pb.RegisterStreamKeyServiceServer(server, commodoreServer)
	pb.RegisterDeveloperServiceServer(server, commodoreServer)
	// ClipService, DVRService, ViewerService, and VodService proxy to Foghorn via gRPC
	pb.RegisterClipServiceServer(server, commodoreServer)
	pb.RegisterDVRServiceServer(server, commodoreServer)
	pb.RegisterViewerServiceServer(server, commodoreServer)
	pb.RegisterVodServiceServer(server, commodoreServer)

	// Register gRPC health checking service
	hs := health.NewServer()
	grpc_health_v1.RegisterHealthServer(server, hs)

	return server
}

// unaryInterceptor logs gRPC requests
func unaryInterceptor(logger logging.Logger) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req interface{}, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (interface{}, error) {
		start := time.Now()
		resp, err := handler(ctx, req)
		logger.WithFields(logging.Fields{
			"method":   info.FullMethod,
			"duration": time.Since(start),
			"error":    err,
		}).Debug("gRPC request processed")
		return resp, err
	}
}

// ============================================================================
// HELPER FUNCTIONS
// ============================================================================

// hashToken creates a SHA-256 hash of a token for secure storage (fallback when no secret configured)
func hashToken(token string) string {
	hash := sha256.Sum256([]byte(token))
	return hex.EncodeToString(hash[:])
}

// hashTokenWithSecret creates an HMAC-SHA256 hash of a token using the configured secret
// Falls back to plain SHA-256 if no secret is configured
func (s *CommodoreServer) hashTokenWithSecret(token string) string {
	if len(s.passwordResetSecret) > 0 {
		h := hmac.New(sha256.New, s.passwordResetSecret)
		h.Write([]byte(token))
		return hex.EncodeToString(h.Sum(nil))
	}
	// Fallback to plain hash if no secret configured
	return hashToken(token)
}

// sendVerificationEmail sends an email verification link
func (s *CommodoreServer) sendVerificationEmail(email, token string) error {
	smtpHost := os.Getenv("SMTP_HOST")
	smtpPort := os.Getenv("SMTP_PORT")
	smtpUser := os.Getenv("SMTP_USER")
	smtpPass := os.Getenv("SMTP_PASS")

	if smtpHost == "" {
		s.logger.Warn("SMTP not configured, skipping verification email")
		return nil
	}

	if smtpPort == "" {
		smtpPort = "587"
	}

	fromEmail := os.Getenv("FROM_EMAIL")
	if fromEmail == "" {
		fromEmail = "noreply@frameworks.network"
	}

	baseURL := os.Getenv("WEBAPP_PUBLIC_URL")
	if baseURL == "" {
		baseURL = "http://localhost:18090/app"
	}
	verifyURL := fmt.Sprintf("%s/verify-email?token=%s", baseURL, url.QueryEscape(token))

	subject := "Verify your FrameWorks account"
	body := fmt.Sprintf(`
<!DOCTYPE html><html><body>
  <p>Welcome to FrameWorks!</p>
  <p>Please <a href="%s">click here to verify your email address</a>.</p>
  <p>This link expires in 24 hours.</p>
  <p>If you did not create an account, you can ignore this email.</p>
</body></html>`, verifyURL)

	auth := smtp.PlainAuth("", smtpUser, smtpPass, smtpHost)
	msg := []byte(fmt.Sprintf("To: %s\r\nSubject: %s\r\nMIME-Version: 1.0\r\nContent-Type: text/html; charset=UTF-8\r\n\r\n%s", email, subject, body))
	return smtp.SendMail(smtpHost+":"+smtpPort, auth, fromEmail, []string{email}, msg)
}

// sendPasswordResetEmail sends a password reset link
func (s *CommodoreServer) sendPasswordResetEmail(email, token string) error {
	smtpHost := os.Getenv("SMTP_HOST")
	smtpPort := os.Getenv("SMTP_PORT")
	smtpUser := os.Getenv("SMTP_USER")
	smtpPass := os.Getenv("SMTP_PASS")

	if smtpHost == "" {
		s.logger.Warn("SMTP not configured, skipping password reset email")
		return nil
	}

	if smtpPort == "" {
		smtpPort = "587"
	}

	fromEmail := os.Getenv("FROM_EMAIL")
	if fromEmail == "" {
		fromEmail = "noreply@frameworks.network"
	}

	baseURL := os.Getenv("WEBAPP_PUBLIC_URL")
	if baseURL == "" {
		baseURL = "http://localhost:18090/app"
	}
	resetURL := fmt.Sprintf("%s/reset-password?token=%s", baseURL, url.QueryEscape(token))

	subject := "Reset your FrameWorks password"
	body := fmt.Sprintf(`
<!DOCTYPE html><html><body>
  <p>We received a request to reset your password.</p>
  <p><a href="%s">Click here to reset your password</a> (valid for 1 hour).</p>
  <p>If you did not request this, you can safely ignore this email.</p>
</body></html>`, resetURL)

	auth := smtp.PlainAuth("", smtpUser, smtpPass, smtpHost)
	msg := []byte(fmt.Sprintf("To: %s\r\nSubject: %s\r\nMIME-Version: 1.0\r\nContent-Type: text/html; charset=UTF-8\r\n\r\n%s", email, subject, body))
	return smtp.SendMail(smtpHost+":"+smtpPort, auth, fromEmail, []string{email}, msg)
}

// generateRandomString generates a random alphanumeric string
func generateRandomString(length int) string {
	const charset = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789"
	b := make([]byte, length)
	rand.Read(b)
	for i := range b {
		b[i] = charset[int(b[i])%len(charset)]
	}
	return string(b)
}

// ============================================================================
// VOD SERVICE (Gateway → Commodore → Foghorn proxy)
// User-initiated video uploads (distinct from clips/DVR which are stream-derived)
// ============================================================================

// CreateVodUpload registers VOD in business registry and initiates multipart upload via Foghorn
func (s *CommodoreServer) CreateVodUpload(ctx context.Context, req *pb.CreateVodUploadRequest) (*pb.CreateVodUploadResponse, error) {
	if s.foghornClient == nil {
		return nil, status.Error(codes.Unavailable, "Foghorn client not configured")
	}

	// Get user and tenant context from metadata
	userID, tenantID, err := extractUserContext(ctx)
	if err != nil {
		return nil, err
	}

	// Generate VOD hash (Commodore is authoritative)
	vodHash := generateVodHash()
	vodID := uuid.New().String()

	// Resolve retention (default 90 days for VOD)
	retentionUntil := time.Now().Add(90 * 24 * time.Hour)

	// Register in commodore.vod_assets (business registry)
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO commodore.vod_assets (
			id, tenant_id, user_id, vod_hash,
			title, description, filename, content_type, size_bytes,
			retention_until, created_at, updated_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, NOW(), NOW())
	`, vodID, tenantID, userID, vodHash,
		req.GetTitle(), req.GetDescription(), req.Filename, req.GetContentType(), req.SizeBytes,
		retentionUntil)

	if err != nil {
		s.logger.WithFields(logging.Fields{
			"tenant_id": tenantID,
			"filename":  req.Filename,
			"error":     err,
		}).Error("Failed to register VOD asset in business registry")
		return nil, status.Errorf(codes.Internal, "failed to register VOD asset: %v", err)
	}

	s.logger.WithFields(logging.Fields{
		"tenant_id": tenantID,
		"vod_hash":  vodHash,
		"vod_id":    vodID,
		"filename":  req.Filename,
	}).Info("Registered VOD asset in business registry")

	// Build Foghorn request with pre-generated hash
	foghornReq := &pb.CreateVodUploadRequest{
		TenantId:    tenantID,
		UserId:      userID,
		Filename:    req.Filename,
		SizeBytes:   req.SizeBytes,
		ContentType: req.ContentType,
		Title:       req.Title,
		Description: req.Description,
		VodHash:     &vodHash, // Pass the hash we generated
	}

	// Call Foghorn for S3 multipart upload setup
	resp, err := s.foghornClient.CreateVodUpload(ctx, foghornReq)
	if err != nil {
		s.logger.WithError(err).WithField("vod_hash", vodHash).Error("Failed to create VOD upload via Foghorn")
		// Don't rollback business registry - can be cleaned up later
		return nil, status.Errorf(codes.Internal, "failed to initiate VOD upload: %v", err)
	}

	return resp, nil
}

// CompleteVodUpload finalizes multipart upload via Foghorn
func (s *CommodoreServer) CompleteVodUpload(ctx context.Context, req *pb.CompleteVodUploadRequest) (*pb.CompleteVodUploadResponse, error) {
	if s.foghornClient == nil {
		return nil, status.Error(codes.Unavailable, "Foghorn client not configured")
	}

	// Get tenant context from metadata (for logging/verification)
	_, tenantID, err := extractUserContext(ctx)
	if err != nil {
		return nil, err
	}

	// Forward to Foghorn (it manages S3 multipart completion and lifecycle state)
	foghornReq := &pb.CompleteVodUploadRequest{
		TenantId: tenantID,
		UploadId: req.UploadId,
		Parts:    req.Parts,
	}

	resp, err := s.foghornClient.CompleteVodUpload(ctx, foghornReq)
	if err != nil {
		s.logger.WithError(err).WithField("upload_id", req.UploadId).Error("Failed to complete VOD upload via Foghorn")
		return nil, status.Errorf(codes.Internal, "failed to complete VOD upload: %v", err)
	}

	s.logger.WithFields(logging.Fields{
		"tenant_id":     tenantID,
		"upload_id":     req.UploadId,
		"artifact_hash": resp.GetAsset().GetArtifactHash(),
	}).Info("Completed VOD upload")

	return resp, nil
}

// AbortVodUpload cancels multipart upload via Foghorn
func (s *CommodoreServer) AbortVodUpload(ctx context.Context, req *pb.AbortVodUploadRequest) (*pb.AbortVodUploadResponse, error) {
	if s.foghornClient == nil {
		return nil, status.Error(codes.Unavailable, "Foghorn client not configured")
	}

	// Get tenant context from metadata
	_, tenantID, err := extractUserContext(ctx)
	if err != nil {
		return nil, err
	}

	// Forward to Foghorn (it manages S3 multipart abort and lifecycle state)
	resp, err := s.foghornClient.AbortVodUpload(ctx, tenantID, req.UploadId)
	if err != nil {
		s.logger.WithError(err).WithField("upload_id", req.UploadId).Error("Failed to abort VOD upload via Foghorn")
		return nil, status.Errorf(codes.Internal, "failed to abort VOD upload: %v", err)
	}

	// TODO: Clean up orphaned business registry entry (or let retention job handle it)

	s.logger.WithFields(logging.Fields{
		"tenant_id": tenantID,
		"upload_id": req.UploadId,
	}).Info("Aborted VOD upload")

	return resp, nil
}

// GetVodAsset returns VOD business metadata from Commodore registry
// Lifecycle data (status, size, storage) comes from Periscope via Gateway's ArtifactLifecycleLoader
func (s *CommodoreServer) GetVodAsset(ctx context.Context, req *pb.GetVodAssetRequest) (*pb.VodAssetInfo, error) {
	// Get tenant context from metadata
	_, tenantID, err := extractUserContext(ctx)
	if err != nil {
		return nil, err
	}

	// Query business metadata from Commodore registry ONLY - no Foghorn call
	var (
		id                       string
		vodHash                  string
		title, description       sql.NullString
		filename                 string
		contentType              sql.NullString
		sizeBytes                sql.NullInt64
		retentionUntil           sql.NullTime
		createdAt, updatedAt     time.Time
	)
	err = s.db.QueryRowContext(ctx, `
		SELECT id, vod_hash, title, description, filename, content_type,
		       size_bytes, retention_until, created_at, updated_at
		FROM commodore.vod_assets
		WHERE vod_hash = $1 AND tenant_id = $2
	`, req.ArtifactHash, tenantID).Scan(
		&id, &vodHash, &title, &description, &filename, &contentType,
		&sizeBytes, &retentionUntil, &createdAt, &updatedAt,
	)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, status.Error(codes.NotFound, "VOD asset not found")
		}
		s.logger.WithError(err).WithField("artifact_hash", req.ArtifactHash).Error("Failed to get VOD asset")
		return nil, status.Errorf(codes.Internal, "database error: %v", err)
	}

	// Build response with business metadata only
	// Status/storageLocation will be enriched by Gateway via Periscope
	asset := &pb.VodAssetInfo{
		Id:           id,
		ArtifactHash: vodHash,
		Filename:     filename,
		Status:       pb.VodStatus_VOD_STATUS_UPLOADING, // Default - Gateway enriches from Periscope
		CreatedAt:    timestamppb.New(createdAt),
		UpdatedAt:    timestamppb.New(updatedAt),
	}
	if title.Valid {
		asset.Title = title.String
	}
	if description.Valid {
		asset.Description = description.String
	}
	if sizeBytes.Valid {
		size := sizeBytes.Int64
		asset.SizeBytes = &size
	}
	if retentionUntil.Valid {
		asset.ExpiresAt = timestamppb.New(retentionUntil.Time)
	}

	return asset, nil
}

// ListVodAssets returns VOD assets from Commodore business registry with pagination
// Lifecycle data (status, size, storage) comes from Periscope via Gateway's ArtifactLifecycleLoader
func (s *CommodoreServer) ListVodAssets(ctx context.Context, req *pb.ListVodAssetsRequest) (*pb.ListVodAssetsResponse, error) {
	// Get tenant context from metadata
	_, tenantID, err := extractUserContext(ctx)
	if err != nil {
		return nil, err
	}

	params, err := pagination.Parse(req.GetPagination())
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid pagination: %v", err)
	}

	// Get total count
	var total int32
	countQuery := `SELECT COUNT(*) FROM commodore.vod_assets WHERE tenant_id = $1`
	if err := s.db.QueryRowContext(ctx, countQuery, tenantID).Scan(&total); err != nil {
		return nil, status.Errorf(codes.Internal, "database error: %v", err)
	}

	// Build keyset pagination query
	builder := &pagination.KeysetBuilder{
		TimestampColumn: "created_at",
		IDColumn:        "vod_hash",
	}

	// Base query
	query := `
		SELECT id, vod_hash, title, description, filename, content_type,
		       size_bytes, retention_until, created_at, updated_at
		FROM commodore.vod_assets
		WHERE tenant_id = $1`
	args := []interface{}{tenantID}
	argIdx := 2

	// Add keyset condition if cursor provided
	if condition, cursorArgs := builder.Condition(params, argIdx); condition != "" {
		query += " AND " + condition
		args = append(args, cursorArgs...)
	}

	// Add ORDER BY and LIMIT
	query += " " + builder.OrderBy(params)
	query += fmt.Sprintf(" LIMIT %d", params.Limit+1)

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "database error: %v", err)
	}
	defer rows.Close()

	var assets []*pb.VodAssetInfo
	for rows.Next() {
		var (
			id                       string
			vodHash                  string
			title, description       sql.NullString
			filename                 string
			contentType              sql.NullString
			sizeBytes                sql.NullInt64
			retentionUntil           sql.NullTime
			createdAt, updatedAt     time.Time
		)
		if err := rows.Scan(&id, &vodHash, &title, &description, &filename, &contentType,
			&sizeBytes, &retentionUntil, &createdAt, &updatedAt); err != nil {
			s.logger.WithError(err).Warn("Error scanning VOD asset")
			continue
		}

		// Build asset with business metadata only
		// Status/storageLocation will be enriched by Gateway via Periscope
		asset := &pb.VodAssetInfo{
			Id:           id,
			ArtifactHash: vodHash,
			Filename:     filename,
			Status:       pb.VodStatus_VOD_STATUS_UPLOADING, // Default - Gateway enriches from Periscope
			CreatedAt:    timestamppb.New(createdAt),
			UpdatedAt:    timestamppb.New(updatedAt),
		}
		if title.Valid {
			asset.Title = title.String
		}
		if description.Valid {
			asset.Description = description.String
		}
		if sizeBytes.Valid {
			size := sizeBytes.Int64
			asset.SizeBytes = &size
		}
		if retentionUntil.Valid {
			asset.ExpiresAt = timestamppb.New(retentionUntil.Time)
		}

		assets = append(assets, asset)
	}

	// Detect hasMore and trim results
	hasMore := len(assets) > params.Limit
	if hasMore {
		assets = assets[:params.Limit]
	}

	// Reverse results if backward pagination
	if params.Direction == pagination.Backward && len(assets) > 0 {
		for i, j := 0, len(assets)-1; i < j; i, j = i+1, j-1 {
			assets[i], assets[j] = assets[j], assets[i]
		}
	}

	// Build cursors from results
	var startCursor, endCursor string
	if len(assets) > 0 {
		first := assets[0]
		last := assets[len(assets)-1]
		startCursor = pagination.EncodeCursor(first.CreatedAt.AsTime(), first.ArtifactHash)
		endCursor = pagination.EncodeCursor(last.CreatedAt.AsTime(), last.ArtifactHash)
	}

	// Build response
	resp := &pb.ListVodAssetsResponse{
		Assets: assets,
		Pagination: &pb.CursorPaginationResponse{
			TotalCount: total,
		},
	}
	if startCursor != "" {
		resp.Pagination.StartCursor = &startCursor
	}
	if endCursor != "" {
		resp.Pagination.EndCursor = &endCursor
	}
	if params.Direction == pagination.Forward {
		resp.Pagination.HasNextPage = hasMore
		resp.Pagination.HasPreviousPage = params.Cursor != nil
	} else {
		resp.Pagination.HasPreviousPage = hasMore
		resp.Pagination.HasNextPage = params.Cursor != nil
	}

	return resp, nil
}

// DeleteVodAsset deletes a VOD asset via Foghorn
func (s *CommodoreServer) DeleteVodAsset(ctx context.Context, req *pb.DeleteVodAssetRequest) (*pb.DeleteVodAssetResponse, error) {
	if s.foghornClient == nil {
		return nil, status.Error(codes.Unavailable, "Foghorn client not configured")
	}

	// Get tenant context from metadata
	_, tenantID, err := extractUserContext(ctx)
	if err != nil {
		return nil, err
	}

	// Forward to Foghorn (it handles S3 deletion and lifecycle state)
	resp, err := s.foghornClient.DeleteVodAsset(ctx, tenantID, req.ArtifactHash)
	if err != nil {
		s.logger.WithError(err).WithField("artifact_hash", req.ArtifactHash).Error("Failed to delete VOD asset via Foghorn")
		return nil, status.Errorf(codes.Internal, "failed to delete VOD asset: %v", err)
	}

	// Delete from business registry
	_, delErr := s.db.ExecContext(ctx, `
		DELETE FROM commodore.vod_assets
		WHERE vod_hash = $1 AND tenant_id = $2
	`, req.ArtifactHash, tenantID)
	if delErr != nil {
		s.logger.WithError(delErr).WithField("artifact_hash", req.ArtifactHash).Warn("Failed to delete VOD from business registry (will be cleaned up by retention job)")
	}

	s.logger.WithFields(logging.Fields{
		"tenant_id":     tenantID,
		"artifact_hash": req.ArtifactHash,
	}).Info("Deleted VOD asset")

	return resp, nil
}
