package handlers

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	mathrand "math/rand"
	"net/http"
	"net/smtp"
	"os"
	"strings"
	"sync"
	"time"

	"frameworks/pkg/logging"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/lib/pq"

	commodoreapi "frameworks/pkg/api/commodore"
	purserapi "frameworks/pkg/api/purser"
	qmapi "frameworks/pkg/api/quartermaster"
	"frameworks/pkg/auth"
	"frameworks/pkg/clients"
	purserclient "frameworks/pkg/clients/purser"
	qmclient "frameworks/pkg/clients/quartermaster"
	"frameworks/pkg/models"
)

// HandlerMetrics holds the metrics for handler operations
type HandlerMetrics struct {
	AuthOperations   *prometheus.CounterVec
	AuthDuration     *prometheus.HistogramVec
	ActiveSessions   *prometheus.GaugeVec
	StreamOperations *prometheus.CounterVec
	DBQueries        *prometheus.CounterVec
	DBDuration       *prometheus.HistogramVec
	DBConnections    *prometheus.GaugeVec
}

var db *sql.DB
var logger logging.Logger
var router Router
var metrics *HandlerMetrics

// Rate limiting for registration
var registrationAttempts = make(map[string][]time.Time)
var registrationMutex sync.RWMutex

var (
	quartermasterURL    string
	purserURL           string
	serviceToken        string
	quartermasterClient *qmclient.Client
	purserClient        *purserclient.Client
)

func Init(database *sql.DB, log logging.Logger, r Router, m *HandlerMetrics) {
	db = database
	logger = log
	router = r
	metrics = m

	quartermasterURL = os.Getenv("QUARTERMASTER_URL")
	if quartermasterURL == "" {
		quartermasterURL = "http://localhost:18002"
	}

	purserURL = os.Getenv("PURSER_URL")
	if purserURL == "" {
		purserURL = "http://localhost:18003"
	}

	serviceToken = os.Getenv("SERVICE_TOKEN")
	if serviceToken == "" {
		log.Fatal("SERVICE_TOKEN environment variable is required")
	}

	quartermasterClient = qmclient.NewClient(qmclient.Config{
		BaseURL:      quartermasterURL,
		ServiceToken: serviceToken,
		Timeout:      10 * time.Second,
		Logger:       logger,
		CircuitBreakerConfig: &clients.CircuitBreakerConfig{
			FailureThreshold: 5,
			SuccessThreshold: 2,
			Timeout:          30 * time.Second,
		},
	})

	purserClient = purserclient.NewClient(purserclient.Config{
		BaseURL:      purserURL,
		ServiceToken: serviceToken,
		Timeout:      10 * time.Second,
		Logger:       logger,
		CircuitBreakerConfig: &clients.CircuitBreakerConfig{
			FailureThreshold: 5,
			SuccessThreshold: 2,
			Timeout:          30 * time.Second,
		},
	})
}

// Register handles user registration with comprehensive bot protection
func Register(c *gin.Context) {
	start := time.Now()

	// Record metrics
	defer func() {
		duration := time.Since(start).Seconds()
		if metrics != nil {
			metrics.AuthDuration.WithLabelValues("register").Observe(duration)
		}
	}()

	var req models.RegisterRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		if metrics != nil {
			metrics.AuthOperations.WithLabelValues("register", "error").Inc()
		}
		c.JSON(http.StatusBadRequest, commodoreapi.ErrorResponse{Error: err.Error()})
		return
	}

	clientIP := c.ClientIP()

	// Rate limiting - max 5 attempts per IP per 15 minutes
	if !checkRegistrationRateLimit(clientIP) {
		logger.WithFields(logging.Fields{
			"ip":    clientIP,
			"email": req.Email,
		}).Warn("Registration rate limit exceeded")

		// Return fake success to avoid revealing rate limiting to attackers
		c.JSON(http.StatusOK, commodoreapi.RegisterResponse{
			Success: true,
			Message: "Registration successful. Please check your email to verify your account.",
		})
		return
	}

	// Bot protection validation
	botValidationErrors := validateBotProtection(req, clientIP)
	if len(botValidationErrors) > 0 {
		logger.WithFields(logging.Fields{
			"ip":     clientIP,
			"email":  req.Email,
			"errors": botValidationErrors,
		}).Warn("Bot protection validation failed")

		// Return fake success to avoid revealing bot detection to attackers
		c.JSON(http.StatusOK, commodoreapi.RegisterResponse{
			Success: true,
			Message: "Registration successful. Please check your email to verify your account.",
		})
		return
	}

	// For new registrations, create a new tenant through Quartermaster
	tenantID, err := createTenantForRegistration(req.Email)
	if err != nil {
		logger.WithError(err).WithField("email", req.Email).Error("Failed to create tenant for registration")
		c.JSON(http.StatusInternalServerError, commodoreapi.ErrorResponse{Error: "Failed to create tenant"})
		return
	}

	// Check user limit by calling Purser to validate if user registration is allowed
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	limitCheck, err := purserClient.CheckUserLimit(ctx, &purserapi.CheckUserLimitRequest{
		TenantID: tenantID,
		Email:    req.Email,
	})
	if err != nil {
		logger.WithError(err).Error("Failed to check user limit with Purser")
		c.JSON(http.StatusInternalServerError, commodoreapi.ErrorResponse{Error: "Failed to validate user limit"})
		return
	}

	if !limitCheck.Allowed {
		c.JSON(http.StatusForbidden, commodoreapi.ErrorResponse{Error: "Tenant user limit reached"})
		return
	}

	// Check if user already exists in this tenant
	var existingID string
	err = db.QueryRow("SELECT id FROM users WHERE tenant_id = $1 AND email = $2", tenantID, req.Email).Scan(&existingID)
	if err != sql.ErrNoRows {
		c.JSON(http.StatusConflict, commodoreapi.ErrorResponse{Error: "User already exists in this tenant"})
		return
	}

	// Hash password
	hashedPassword, err := auth.HashPassword(req.Password)
	if err != nil {
		c.JSON(http.StatusInternalServerError, commodoreapi.ErrorResponse{Error: "Failed to hash password"})
		return
	}

	// Generate verification token
	verificationToken := generateVerificationToken()
	tokenExpiry := time.Now().Add(24 * time.Hour) // 24 hour expiry

	// Create user with tenant context (verified = false)
	userID := uuid.New().String()
	role := "member" // Default role, first user becomes owner

	// Check if this is the first user (becomes owner)
	var userCount int
	err = db.QueryRow("SELECT COUNT(*) FROM users WHERE tenant_id = $1", tenantID).Scan(&userCount)
	if err == nil && userCount == 0 {
		role = "owner"
	}

	_, err = db.Exec(`
		INSERT INTO users (id, tenant_id, email, password_hash, role, permissions, verified, verification_token, token_expires_at) 
		VALUES ($1, $2, $3, $4, $5, $6, false, $7, $8)
	`, userID, tenantID, req.Email, hashedPassword, role, getDefaultPermissions(role), verificationToken, tokenExpiry)

	if err != nil {
		logger.WithFields(logging.Fields{
			"tenant_id": tenantID,
			"email":     req.Email,
			"error":     err,
		}).Error("Failed to create user")
		c.JSON(http.StatusInternalServerError, commodoreapi.ErrorResponse{Error: "Failed to create user"})
		return
	}

	// Send verification email
	err = sendVerificationEmail(req.Email, verificationToken, "FrameWorks")
	if err != nil {
		logger.WithFields(logging.Fields{
			"user_id":   userID,
			"tenant_id": tenantID,
			"email":     req.Email,
			"error":     err,
		}).Error("Failed to send verification email")
		// Don't fail registration if email fails, just log it
	}

	logger.WithFields(logging.Fields{
		"user_id":   userID,
		"tenant_id": tenantID,
		"email":     req.Email,
		"role":      role,
	}).Info("User registered successfully, verification email sent")

	if metrics != nil {
		metrics.AuthOperations.WithLabelValues("register", "success").Inc()
	}

	c.JSON(http.StatusOK, commodoreapi.RegisterResponse{
		Success: true,
		Message: "Registration successful. Please check your email to verify your account.",
	})
}

// Login handles user authentication with verification requirement
func Login(c *gin.Context) {
	start := time.Now()

	// Record metrics
	defer func() {
		duration := time.Since(start).Seconds()
		if metrics != nil {
			metrics.AuthDuration.WithLabelValues("login").Observe(duration)
		}
	}()

	var req models.LoginRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		if metrics != nil {
			metrics.AuthOperations.WithLabelValues("login", "error").Inc()
		}
		c.JSON(http.StatusBadRequest, commodoreapi.ErrorResponse{Error: err.Error()})
		return
	}

	// Find user by email (shared tenancy)
	var user models.User
	err := db.QueryRow(`
		SELECT id, tenant_id, email, password_hash, role, verified, is_active, COALESCE(created_at, NOW()) as created_at
		FROM users WHERE email = $1
	`, req.Email).Scan(&user.ID, &user.TenantID, &user.Email, &user.PasswordHash, &user.Role, &user.IsVerified, &user.IsActive, &user.CreatedAt)

	if err == sql.ErrNoRows {
		logger.WithFields(logging.Fields{
			"email": req.Email,
		}).Warn("Login attempt with non-existent user")
		c.JSON(http.StatusUnauthorized, commodoreapi.ErrorResponse{Error: "Invalid credentials"})
		return
	} else if err != nil {
		logger.WithFields(logging.Fields{
			"error": err,
			"email": req.Email,
		}).Error("Database error during login")
		c.JSON(http.StatusInternalServerError, commodoreapi.ErrorResponse{Error: "Database error"})
		return
	}

	// Check if user account is active
	if !user.IsActive {
		c.JSON(http.StatusUnauthorized, commodoreapi.ErrorResponse{Error: "Account is deactivated"})
		return
	}

	// Check if email is verified
	if !user.IsVerified {
		logger.WithFields(logging.Fields{
			"user_id":   user.ID,
			"tenant_id": user.TenantID,
			"email":     req.Email,
		}).Warn("Login attempt with unverified email")
		c.JSON(http.StatusUnauthorized, commodoreapi.ErrorResponse{Error: "Please verify your email address before logging in. Check your inbox for the verification email."})
		return
	}

	// Verify password
	if !auth.CheckPassword(req.Password, user.PasswordHash) {
		logger.WithFields(logging.Fields{
			"user_id":   user.ID,
			"tenant_id": user.TenantID,
			"email":     req.Email,
		}).Warn("Login attempt with incorrect password")
		c.JSON(http.StatusUnauthorized, commodoreapi.ErrorResponse{Error: "Invalid credentials"})
		return
	}

	// Update last login timestamp
	_, err = db.Exec("UPDATE users SET last_login = NOW() WHERE id = $1", user.ID)
	if err != nil {
		logger.WithFields(logging.Fields{
			"error":   err,
			"user_id": user.ID,
		}).Warn("Failed to update last login timestamp")
	}

	// Generate JWT token
	jwtSecret := []byte(os.Getenv("JWT_SECRET"))
	token, err := auth.GenerateJWT(user.ID, user.TenantID, user.Email, user.Role, jwtSecret)
	if err != nil {
		logger.WithFields(logging.Fields{
			"error":   err,
			"user_id": user.ID,
		}).Error("Failed to generate JWT token")
		c.JSON(http.StatusInternalServerError, commodoreapi.ErrorResponse{Error: "Failed to generate token"})
		return
	}

	logger.WithFields(logging.Fields{
		"user_id":   user.ID,
		"tenant_id": user.TenantID,
		"email":     req.Email,
		"role":      user.Role,
	}).Info("User logged in successfully")

	// Return token and user info
	c.JSON(http.StatusOK, commodoreapi.AuthResponse{
		Token: token,
		User: models.User{
			ID:          user.ID,
			TenantID:    user.TenantID,
			Email:       user.Email,
			Role:        user.Role,
			Permissions: user.Permissions,
		},
		ExpiresAt: time.Now().Add(24 * time.Hour),
	})
}

// GetMe returns current user info with their streams
func GetMe(c *gin.Context) {
	userID := c.GetString("user_id")

	var user models.User
	err := db.QueryRow(`
		SELECT id, email, role, COALESCE(created_at, NOW()) as created_at, is_active 
		FROM users WHERE id = $1
	`, userID).Scan(&user.ID, &user.Email, &user.Role, &user.CreatedAt, &user.IsActive)

	if err != nil {
		c.JSON(http.StatusNotFound, commodoreapi.ErrorResponse{Error: "User not found"})
		return
	}

	// Get user's streams
	tenantID := c.GetString("tenant_id")
	rows, err := db.Query(`
		SELECT internal_name, stream_key, playback_id, title, status 
		FROM streams WHERE user_id = $1 AND tenant_id = $2 ORDER BY created_at DESC
	`, userID, tenantID)

	if err != nil {
		logger.WithFields(logging.Fields{
			"user_id": userID,
			"error":   err,
		}).Error("Failed to fetch user streams")
		c.JSON(http.StatusOK, commodoreapi.UserWithStreamsResponse{
			User:    user,
			Streams: []commodoreapi.UserStreamInfo{},
		})
		return
	}
	defer rows.Close()

	var streams []commodoreapi.UserStreamInfo
	for rows.Next() {
		var internalName, streamKey, playbackID, title, status string
		err := rows.Scan(&internalName, &streamKey, &playbackID, &title, &status)
		if err != nil {
			logger.WithFields(logging.Fields{
				"user_id": userID,
				"error":   err,
			}).Error("Error scanning stream in user profile")
			continue
		}
		streams = append(streams, commodoreapi.UserStreamInfo{
			ID:         internalName,
			StreamKey:  streamKey,
			PlaybackID: playbackID,
			Title:      title,
			Status:     status,
		})
	}

	c.JSON(http.StatusOK, commodoreapi.UserWithStreamsResponse{
		User:    user,
		Streams: streams,
	})
}

// GetStreams returns user's streams (Control Plane data only)
func GetStreams(c *gin.Context) {
	userID := c.GetString("user_id")
	tenantID := c.GetString("tenant_id")
	role := c.GetString("role")

	var query string
	var args []interface{}

	if role == "service" {
		// Service accounts can see all streams for the tenant
		query = `
			SELECT stream_key, playback_id, internal_name, title, description,
			       is_recording_enabled, is_public, status, start_time, end_time, created_at, updated_at
			FROM streams 
			WHERE tenant_id = $1
			ORDER BY created_at DESC
		`
		args = []interface{}{tenantID}
	} else {
		// Regular users see only their own streams
		query = `
			SELECT stream_key, playback_id, internal_name, title, description,
			       is_recording_enabled, is_public, status, start_time, end_time, created_at, updated_at
			FROM streams 
			WHERE user_id = $1 AND tenant_id = $2
			ORDER BY created_at DESC
		`
		args = []interface{}{userID, tenantID}
	}

	rows, err := db.Query(query, args...)

	if err != nil {
		logger.WithFields(logging.Fields{
			"tenant_id": tenantID,
			"user_id":   userID,
			"role":      role,
			"error":     err,
		}).Error("Database error fetching user streams")
		c.JSON(http.StatusInternalServerError, commodoreapi.ErrorResponse{Error: "Database error"})
		return
	}
	defer rows.Close()

	streams := []models.Stream{}
	for rows.Next() {
		var stream models.Stream
		var startTime, endTime *time.Time
		var description *string // Handle nullable description
		err := rows.Scan(&stream.StreamKey, &stream.PlaybackID,
			&stream.InternalName, &stream.Title, &description,
			&stream.IsRecordingEnabled, &stream.IsPublic, &stream.Status,
			&startTime, &endTime, &stream.CreatedAt, &stream.UpdatedAt)

		if err != nil {
			logger.WithFields(logging.Fields{
				"tenant_id": tenantID,
				"user_id":   userID,
				"error":     err,
			}).Error("Row scan error while fetching streams")
			continue
		}

		// Convert nullable fields
		if description != nil {
			stream.Description = *description
		} else {
			stream.Description = ""
		}

		// Convert nullable timestamps
		if startTime != nil {
			stream.StartTime = startTime
		}
		if endTime != nil {
			stream.EndTime = endTime
		}

		// Set public ID to internal_name
		stream.ID = stream.InternalName

		streams = append(streams, stream)
	}

	c.JSON(http.StatusOK, streams)
}

// GetStream returns a specific stream
func GetStream(c *gin.Context) {
	userID := c.GetString("user_id")
	tenantID := c.GetString("tenant_id")
	streamInternalName := c.Param("id") // Now expects internal_name instead of UUID

	var stream models.Stream
	var startTime, endTime *time.Time
	err := db.QueryRow(`
		SELECT id, title, description, stream_key, playback_id, internal_name, 
		       is_recording_enabled, is_public, status, start_time, end_time, created_at
		FROM streams WHERE internal_name = $1 AND user_id = $2 AND tenant_id = $3
	`, streamInternalName, userID, tenantID).Scan(&stream.ID, &stream.Title, &stream.Description,
		&stream.StreamKey, &stream.PlaybackID, &stream.InternalName,
		&stream.IsRecordingEnabled, &stream.IsPublic, &stream.Status,
		&startTime, &endTime, &stream.CreatedAt)

	if err == sql.ErrNoRows {
		c.JSON(http.StatusNotFound, commodoreapi.ErrorResponse{Error: "Stream not found"})
		return
	}

	if err != nil {
		logger.WithFields(logging.Fields{
			"tenant_id":            tenantID,
			"user_id":              userID,
			"stream_internal_name": streamInternalName,
			"error":                err,
		}).Error("Database error fetching stream")
		c.JSON(http.StatusInternalServerError, commodoreapi.ErrorResponse{Error: "Failed to fetch stream"})
		return
	}

	// Convert nullable timestamps
	if startTime != nil {
		stream.StartTime = startTime
	}
	if endTime != nil {
		stream.EndTime = endTime
	}

	// Set public ID to internal_name
	stream.ID = stream.InternalName

	c.JSON(http.StatusOK, stream)
}

// GetStreamMetrics returns real-time metrics for a specific stream
func GetStreamMetrics(c *gin.Context) {
	userID := c.GetString("user_id")
	tenantID := c.GetString("tenant_id")
	streamInternalName := c.Param("id") // Now expects internal_name instead of UUID

	// Verify user owns the stream
	var streamExists bool
	err := db.QueryRow(`
		SELECT EXISTS(SELECT 1 FROM streams WHERE internal_name = $1 AND user_id = $2 AND tenant_id = $3)
	`, streamInternalName, userID, tenantID).Scan(&streamExists)

	if err != nil {
		c.JSON(http.StatusInternalServerError, commodoreapi.ErrorResponse{Error: "Database error"})
		return
	}

	if !streamExists {
		c.JSON(http.StatusNotFound, commodoreapi.ErrorResponse{Error: "Stream not found"})
		return
	}

	// Get current stream metrics from the database
	var metrics struct {
		Viewers      int       `json:"viewers"`
		Status       string    `json:"status"`
		BandwidthIn  *int64    `json:"bandwidth_in"`
		BandwidthOut *int64    `json:"bandwidth_out"`
		Resolution   *string   `json:"resolution"`
		Bitrate      *string   `json:"bitrate"`
		MaxViewers   *int      `json:"max_viewers"`
		UpdatedAt    time.Time `json:"updated_at"`
	}

	err = db.QueryRow(`
		SELECT viewers, status, bitrate, resolution, max_viewers, updated_at
		FROM streams WHERE internal_name = $1 AND user_id = $2 AND tenant_id = $3
	`, streamInternalName, userID, tenantID).Scan(&metrics.Viewers, &metrics.Status, &metrics.Bitrate,
		&metrics.Resolution, &metrics.MaxViewers, &metrics.UpdatedAt)

	if err != nil {
		logger.WithFields(logging.Fields{
			"tenant_id":            tenantID,
			"user_id":              userID,
			"stream_internal_name": streamInternalName,
			"error":                err,
		}).Error("Failed to fetch stream metrics")
		c.JSON(http.StatusInternalServerError, commodoreapi.ErrorResponse{Error: "Failed to fetch metrics"})
		return
	}

	c.JSON(http.StatusOK, commodoreapi.StreamMetricsResponse{
		Metrics: struct {
			Viewers      int       `json:"viewers"`
			Status       string    `json:"status"`
			BandwidthIn  *int64    `json:"bandwidth_in"`
			BandwidthOut *int64    `json:"bandwidth_out"`
			Resolution   *string   `json:"resolution"`
			Bitrate      *string   `json:"bitrate"`
			MaxViewers   *int      `json:"max_viewers"`
			UpdatedAt    time.Time `json:"updated_at"`
		}{
			Viewers:      metrics.Viewers,
			Status:       metrics.Status,
			BandwidthIn:  metrics.BandwidthIn,
			BandwidthOut: metrics.BandwidthOut,
			Resolution:   metrics.Resolution,
			Bitrate:      metrics.Bitrate,
			MaxViewers:   metrics.MaxViewers,
			UpdatedAt:    metrics.UpdatedAt,
		},
	})
}

// ValidateStreamKey validates a stream key for RTMP ingest (used by MistServer triggers)
func ValidateStreamKey(c *gin.Context) {
	streamKey := c.Param("stream_key")

	var streamID, userID, tenantID, internalName string
	var isActive bool
	err := db.QueryRow(`
		SELECT s.id, s.user_id, s.tenant_id, s.internal_name, u.is_active
		FROM streams s
		JOIN users u ON s.user_id = u.id
		WHERE LOWER(s.stream_key) = LOWER($1)
	`, streamKey).Scan(&streamID, &userID, &tenantID, &internalName, &isActive)

	if err == sql.ErrNoRows {
		c.JSON(http.StatusNotFound, commodoreapi.ValidateStreamKeyResponse{Error: "Invalid stream key"})
		return
	}

	if err != nil {
		logger.WithFields(logging.Fields{
			"stream_key": streamKey,
			"error":      err,
		}).Error("Database error validating stream key")
		c.JSON(http.StatusInternalServerError, commodoreapi.ValidateStreamKeyResponse{Error: "Database error"})
		return
	}

	if !isActive {
		c.JSON(http.StatusForbidden, commodoreapi.ValidateStreamKeyResponse{Error: "User account is inactive"})
		return
	}

	// Update last used timestamp for the stream key (also case-insensitive)
	_, err = db.Exec(`
		UPDATE stream_keys SET last_used_at = NOW() 
		WHERE LOWER(key_value) = LOWER($1) AND is_active = true
	`, streamKey)
	if err != nil {
		logger.WithFields(logging.Fields{
			"tenant_id":  tenantID,
			"stream_key": streamKey,
			"stream_id":  streamID,
			"error":      err,
		}).Error("Failed to update stream key usage")
	}

	c.JSON(http.StatusOK, commodoreapi.ValidateStreamKeyResponse{
		Valid:        true,
		UserID:       userID,
		TenantID:     tenantID,
		InternalName: internalName,
	})
}

// CreateClip creates a clip from a stream (STUB - not implemented)
func CreateClip(c *gin.Context) {
	var req commodoreapi.CreateClipRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, commodoreapi.ErrorResponse{Error: err.Error()})
		return
	}

	c.JSON(http.StatusNotImplemented, commodoreapi.NotImplementedResponse{
		Error:   "Clip creation is not currently implemented",
		Message: "This feature requires Foghorn service discovery in Commodore, which is not yet deployed",
		Status:  "not_implemented",
	})
}

// GetStreamNode returns the appropriate node for a stream
func GetStreamNode(c *gin.Context) {
	streamKey := c.Param("stream_key")
	if streamKey == "" {
		c.JSON(http.StatusBadRequest, commodoreapi.ErrorResponse{Error: "stream_key is required"})
		return
	}

	// Get tenant and stream info
	var tenantID, streamID string
	err := db.QueryRow(`
		SELECT tenant_id, stream_id 
		FROM stream_keys 
		WHERE key_value = $1 AND is_active = true
	`, streamKey).Scan(&tenantID, &streamID)

	if err == sql.ErrNoRows {
		c.JSON(http.StatusNotFound, commodoreapi.ErrorResponse{Error: "Stream key not found"})
		return
	} else if err != nil {
		logger.WithError(err).Error("Failed to query stream key")
		c.JSON(http.StatusInternalServerError, commodoreapi.ErrorResponse{Error: "Failed to query stream key"})
		return
	}

	// Get best cluster for stream
	cluster, err := router.GetBestClusterForStream(commodoreapi.StreamRequest{
		TenantID: tenantID,
		StreamID: streamID,
	})
	if err != nil {
		logger.WithError(err).Error("Failed to get cluster for stream")
		c.JSON(http.StatusInternalServerError, commodoreapi.ErrorResponse{Error: "Failed to get cluster for stream"})
		return
	}

	c.JSON(http.StatusOK, commodoreapi.NodeLookupResponse{
		BaseURL:   cluster.BaseURL,
		ClusterID: cluster.ClusterID,
	})
}

// CreateStream creates a new stream for a user
func CreateStream(c *gin.Context) {
	userID := c.GetString("user_id")

	var req commodoreapi.CreateStreamRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, commodoreapi.ErrorResponse{Error: err.Error()})
		return
	}

	var streamID, streamKey, playbackID, internalName string
	err := db.QueryRow(`
		SELECT stream_id, stream_key, playback_id, internal_name 
		FROM create_user_stream($1, $2)
	`, userID, req.Title).Scan(&streamID, &streamKey, &playbackID, &internalName)

	if err != nil {
		logger.WithFields(logging.Fields{
			"user_id": userID,
			"title":   req.Title,
			"error":   err,
		}).Error("Failed to create stream")
		c.JSON(http.StatusInternalServerError, commodoreapi.ErrorResponse{Error: "Failed to create stream"})
		return
	}

	// Update description if provided
	if req.Description != "" {
		_, err = db.Exec(`
			UPDATE streams SET description = $1 WHERE id = $2
		`, req.Description, streamID)
		if err != nil {
			logger.WithFields(logging.Fields{
				"user_id":     userID,
				"stream_id":   streamID,
				"description": req.Description,
				"error":       err,
			}).Error("Failed to update stream description")
		}
	}

	c.JSON(http.StatusCreated, commodoreapi.CreateStreamResponse{
		ID:          internalName,
		StreamKey:   streamKey,
		PlaybackID:  playbackID,
		Title:       req.Title,
		Description: req.Description,
		Status:      "offline", // Default status for new streams
	})
}

// DeleteStream deletes a stream for a user
func DeleteStream(c *gin.Context) {
	userID := c.GetString("user_id")
	streamID := c.Param("id")

	// Verify user owns the stream and get stream details
	var streamUUID, streamKey, internalName, title string
	err := db.QueryRow(`
		SELECT id, stream_key, internal_name, title 
		FROM streams 
		WHERE internal_name = $1 AND user_id = $2
	`, streamID, userID).Scan(&streamUUID, &streamKey, &internalName, &title)

	if err == sql.ErrNoRows {
		c.JSON(http.StatusNotFound, commodoreapi.ErrorResponse{Error: "Stream not found or not owned by user"})
		return
	}

	if err != nil {
		logger.WithFields(logging.Fields{
			"user_id":   userID,
			"stream_id": streamID,
			"error":     err,
		}).Error("Database error fetching stream for deletion")
		c.JSON(http.StatusInternalServerError, commodoreapi.ErrorResponse{Error: "Database error"})
		return
	}

	// Begin transaction for cleanup
	tx, err := db.Begin()
	if err != nil {
		logger.WithFields(logging.Fields{
			"user_id":   userID,
			"stream_id": streamID,
			"error":     err,
		}).Error("Failed to begin transaction for stream deletion")
		c.JSON(http.StatusInternalServerError, commodoreapi.ErrorResponse{Error: "Database error"})
		return
	}
	defer tx.Rollback()

	// Delete related stream_keys
	_, err = tx.Exec(`DELETE FROM stream_keys WHERE stream_id = $1`, streamID)
	if err != nil {
		logger.WithFields(logging.Fields{
			"user_id":   userID,
			"stream_id": streamID,
			"error":     err,
		}).Error("Failed to delete stream keys")
		c.JSON(http.StatusInternalServerError, commodoreapi.ErrorResponse{Error: "Failed to cleanup stream keys"})
		return
	}

	// Delete related clips
	_, err = tx.Exec(`DELETE FROM clips WHERE stream_id = $1`, streamID)
	if err != nil {
		logger.WithFields(logging.Fields{
			"user_id":   userID,
			"stream_id": streamID,
			"error":     err,
		}).Error("Failed to delete clips")
		c.JSON(http.StatusInternalServerError, commodoreapi.ErrorResponse{Error: "Failed to cleanup clips"})
		return
	}

	// Delete the stream itself
	_, err = tx.Exec(`DELETE FROM streams WHERE id = $1`, streamID)
	if err != nil {
		logger.WithFields(logging.Fields{
			"user_id":   userID,
			"stream_id": streamID,
			"error":     err,
		}).Error("Failed to delete stream")
		c.JSON(http.StatusInternalServerError, commodoreapi.ErrorResponse{Error: "Failed to delete stream"})
		return
	}

	// Commit transaction
	if err = tx.Commit(); err != nil {
		logger.WithFields(logging.Fields{
			"user_id":   userID,
			"stream_id": streamID,
			"error":     err,
		}).Error("Failed to commit stream deletion")
		c.JSON(http.StatusInternalServerError, commodoreapi.ErrorResponse{Error: "Failed to complete stream deletion"})
		return
	}

	logger.WithFields(logging.Fields{
		"user_id":   userID,
		"stream_id": streamID,
		"title":     title,
	}).Info("Stream deleted successfully")

	c.JSON(http.StatusOK, commodoreapi.StreamDeleteResponse{
		Message:     "Stream deleted successfully",
		StreamID:    streamID,
		StreamTitle: title,
		DeletedAt:   time.Now(),
	})
}

// Admin handlers

// GetUsers returns all users (admin only)
func GetUsers(c *gin.Context) {
	rows, err := db.Query(`
		SELECT id, email, COALESCE(created_at, NOW()) as created_at, is_active 
		FROM users ORDER BY created_at DESC
	`)

	if err != nil {
		c.JSON(http.StatusInternalServerError, commodoreapi.ErrorResponse{Error: "Failed to fetch users"})
		return
	}
	defer rows.Close()

	var users []models.User
	for rows.Next() {
		var user models.User
		err := rows.Scan(&user.ID, &user.Email, &user.CreatedAt, &user.IsActive)
		if err != nil {
			logger.WithFields(logging.Fields{
				"function": "GetUsers",
				"error":    err,
			}).Error("Error scanning user in admin users list")
			continue
		}
		users = append(users, user)
	}

	c.JSON(http.StatusOK, users)
}

// GetAllStreams returns all streams (admin only)
func GetAllStreams(c *gin.Context) {
	rows, err := db.Query(`
		SELECT internal_name, stream_key, playback_id, title, description,
		       is_recording_enabled, is_public, status, start_time, end_time, created_at
		FROM streams ORDER BY created_at DESC
	`)

	if err != nil {
		c.JSON(http.StatusInternalServerError, commodoreapi.ErrorResponse{Error: "Failed to fetch streams"})
		return
	}
	defer rows.Close()

	var streams []models.Stream
	for rows.Next() {
		var stream models.Stream
		var startTime, endTime *time.Time
		err := rows.Scan(&stream.InternalName, &stream.StreamKey, &stream.PlaybackID,
			&stream.Title, &stream.Description,
			&stream.IsRecordingEnabled, &stream.IsPublic, &stream.Status,
			&startTime, &endTime, &stream.CreatedAt)
		if err != nil {
			logger.WithFields(logging.Fields{
				"function": "GetAllStreams",
				"error":    err,
			}).Error("Error scanning stream in admin streams list")
			continue
		}

		// Set public ID to internal_name
		stream.ID = stream.InternalName

		// Convert nullable timestamps
		if startTime != nil {
			stream.StartTime = startTime
		}
		if endTime != nil {
			stream.EndTime = endTime
		}

		streams = append(streams, stream)
	}

	c.JSON(http.StatusOK, streams)
}

// TerminateStream terminates a stream (admin only)
func TerminateStream(c *gin.Context) {
	streamInternalName := c.Param("id") // Now expects internal_name instead of UUID

	_, err := db.Exec(`
		UPDATE streams SET status = 'terminated', end_time = NOW() WHERE internal_name = $1
	`, streamInternalName)

	if err != nil {
		c.JSON(http.StatusInternalServerError, commodoreapi.ErrorResponse{Error: "Failed to terminate stream"})
		return
	}

	c.JSON(http.StatusOK, commodoreapi.SuccessResponse{Success: true, Message: "Stream terminated successfully"})
}

// RefreshStreamKey generates a new stream key for an existing stream
func RefreshStreamKey(c *gin.Context) {
	userID := c.GetString("user_id")
	tenantID := c.GetString("tenant_id")
	streamInternalName := c.Param("id") // Now expects internal_name instead of UUID

	// Verify user owns the stream and get stream UUID for key updates
	var currentStreamKey, playbackID, internalName, streamUUID string
	err := db.QueryRow(`
		SELECT stream_key, playback_id, internal_name, id 
		FROM streams 
		WHERE internal_name = $1 AND user_id = $2 AND tenant_id = $3
	`, streamInternalName, userID, tenantID).Scan(&currentStreamKey, &playbackID, &internalName, &streamUUID)

	if err == sql.ErrNoRows {
		c.JSON(http.StatusNotFound, commodoreapi.ErrorResponse{Error: "Stream not found or not owned by user"})
		return
	}

	if err != nil {
		logger.WithFields(logging.Fields{
			"user_id":              userID,
			"stream_internal_name": streamInternalName,
			"error":                err,
		}).Error("Database error fetching stream for key refresh")
		c.JSON(http.StatusInternalServerError, commodoreapi.ErrorResponse{Error: "Database error"})
		return
	}

	// Generate new stream key using database function for consistency
	var newStreamKey string
	err = db.QueryRow(`SELECT 'sk_' || generate_random_string(28)`).Scan(&newStreamKey)
	if err != nil {
		logger.WithFields(logging.Fields{
			"user_id":              userID,
			"stream_internal_name": streamInternalName,
			"error":                err,
		}).Error("Failed to generate new stream key")
		c.JSON(http.StatusInternalServerError, commodoreapi.ErrorResponse{Error: "Failed to generate new stream key"})
		return
	}

	// Update the stream with new key
	_, err = db.Exec(`
		UPDATE streams 
		SET stream_key = $1, updated_at = NOW()
		WHERE id = $2
	`, newStreamKey, streamUUID)

	if err != nil {
		logger.WithFields(logging.Fields{
			"user_id":              userID,
			"stream_internal_name": streamInternalName,
			"error":                err,
		}).Error("Failed to update stream key")
		c.JSON(http.StatusInternalServerError, commodoreapi.ErrorResponse{Error: "Failed to update stream key"})
		return
	}

	// Update the stream_keys table (deactivate old key and add new one)
	_, err = db.Exec(`
		UPDATE stream_keys 
		SET is_active = false 
		WHERE LOWER(key_value) = LOWER($1)
	`, currentStreamKey)
	if err != nil {
		logger.WithFields(logging.Fields{
			"user_id":              userID,
			"stream_internal_name": streamInternalName,
			"error":                err,
		}).Error("Failed to deactivate old stream key")
	}

	// Add new key to stream_keys table
	_, err = db.Exec(`
		INSERT INTO stream_keys (stream_id, key_value, key_name, is_active)
		VALUES ($1, $2, 'Refreshed Key', true)
	`, streamUUID, newStreamKey)
	if err != nil {
		logger.WithFields(logging.Fields{
			"user_id":              userID,
			"stream_internal_name": streamInternalName,
			"error":                err,
		}).Error("Failed to add new stream key")
	}

	c.JSON(http.StatusOK, commodoreapi.RefreshKeyResponse{
		Message:           "Stream key refreshed successfully",
		StreamID:          streamInternalName, // Return internal_name as the public stream ID
		StreamKey:         newStreamKey,
		PlaybackID:        playbackID,
		OldKeyInvalidated: true,
	})
}

// Internal endpoints for Helmsman webhook forwarding

// HandleStreamStart handles stream start events from Helmsman
func HandleStreamStart(c *gin.Context) {
	var eventData struct {
		StreamID     string `json:"stream_id"`
		StreamKey    string `json:"stream_key"`
		InternalName string `json:"internal_name"`
		Hostname     string `json:"hostname"`
		PushURL      string `json:"push_url"`
		EventType    string `json:"event_type"`
		Timestamp    int64  `json:"timestamp"`
		// Cluster metadata
		ClusterID  string `json:"cluster_id"`
		FoghornURI string `json:"foghorn_uri"`
		NodeID     string `json:"node_id"`
		NodeName   string `json:"node_name"`
	}

	if err := c.ShouldBindJSON(&eventData); err != nil {
		c.JSON(http.StatusBadRequest, commodoreapi.ErrorResponse{Error: "Invalid payload"})
		return
	}

	// Update stream status based on whether we have stream_id or internal_name
	if eventData.StreamID != "" {
		_, err := db.Exec(`
			UPDATE streams 
			SET status = 'live', start_time = NOW(), updated_at = NOW()
			WHERE id = $1
		`, eventData.StreamID)
		if err != nil {
			logger.WithFields(logging.Fields{
				"stream_id":     eventData.StreamID,
				"internal_name": eventData.InternalName,
				"cluster_id":    eventData.ClusterID,
				"node_id":       eventData.NodeID,
				"error":         err,
			}).Error("Failed to update stream status by ID")
			c.JSON(http.StatusInternalServerError, commodoreapi.ErrorResponse{Error: "Database error"})
			return
		}
	} else if eventData.InternalName != "" {
		_, err := db.Exec(`
			UPDATE streams 
			SET status = 'live', start_time = NOW(), updated_at = NOW()
			WHERE internal_name = $1
		`, eventData.InternalName)
		if err != nil {
			logger.WithFields(logging.Fields{
				"internal_name": eventData.InternalName,
				"cluster_id":    eventData.ClusterID,
				"node_id":       eventData.NodeID,
				"error":         err,
			}).Error("Failed to update stream status by internal name")
			c.JSON(http.StatusInternalServerError, commodoreapi.ErrorResponse{Error: "Database error"})
			return
		}
	}

	// Update stream key last used timestamp if provided
	if eventData.StreamKey != "" {
		_, err := db.Exec(`
			UPDATE stream_keys SET last_used_at = NOW()
			WHERE LOWER(key_value) = LOWER($1) AND is_active = true
		`, eventData.StreamKey)
		if err != nil {
			logger.WithFields(logging.Fields{
				"stream_key": eventData.StreamKey,
				"error":      err,
			}).Error("Failed to update stream key usage")
		}
	}

	c.JSON(http.StatusOK, commodoreapi.SuccessResponse{Success: true})
}

// HandleStreamStatus handles stream status updates from Helmsman
func HandleStreamStatus(c *gin.Context) {
	var eventData struct {
		InternalName  string `json:"internal_name"`
		Status        string `json:"status"`
		BufferState   string `json:"buffer_state"`
		StreamDetails string `json:"stream_details"`
		EventType     string `json:"event_type"`
		Timestamp     int64  `json:"timestamp"`
	}

	if err := c.ShouldBindJSON(&eventData); err != nil {
		c.JSON(http.StatusBadRequest, commodoreapi.ErrorResponse{Error: "Invalid payload"})
		return
	}

	// Note: Stream status is now tracked in Data Plane (Periscope)
	// Control Plane only manages configuration, not live status
	logger.WithFields(logging.Fields{
		"internal_name": eventData.InternalName,
		"status":        eventData.Status,
		"event_type":    eventData.EventType,
	}).Info("Stream lifecycle event (status now tracked in Data Plane)")

	c.JSON(http.StatusOK, commodoreapi.SuccessResponse{Success: true, Message: "Event logged"})
}

// HandleRecordingStatus handles recording status updates from Helmsman
func HandleRecordingStatus(c *gin.Context) {
	var eventData struct {
		InternalName string `json:"internal_name"`
		IsRecording  bool   `json:"is_recording"`
		EventType    string `json:"event_type"`
		Timestamp    int64  `json:"timestamp"`
	}

	if err := c.ShouldBindJSON(&eventData); err != nil {
		c.JSON(http.StatusBadRequest, commodoreapi.ErrorResponse{Error: "Invalid payload"})
		return
	}

	// Update recording status with tenant awareness
	tenantID := "00000000-0000-0000-0000-000000000001" // Demo tenant for now
	_, err := db.Exec(`
		UPDATE streams SET is_recording_enabled = $1, updated_at = NOW()
		WHERE tenant_id = $2 AND internal_name = $3
	`, eventData.IsRecording, tenantID, eventData.InternalName)
	if err != nil {
		logger.WithFields(logging.Fields{
			"tenant_id":     tenantID,
			"internal_name": eventData.InternalName,
			"is_recording":  eventData.IsRecording,
			"error":         err,
		}).Error("Failed to update recording status")
		c.JSON(http.StatusInternalServerError, commodoreapi.ErrorResponse{Error: "Database error"})
		return
	}

	logger.WithFields(logging.Fields{
		"tenant_id":     tenantID,
		"internal_name": eventData.InternalName,
		"event_type":    eventData.EventType,
		"is_recording":  eventData.IsRecording,
		"timestamp":     eventData.Timestamp,
	}).Info("Stream event processed")

	c.JSON(http.StatusOK, commodoreapi.SuccessResponse{Success: true})
}

// ResolvePlaybackID resolves a playback ID to internal name for MistServer DEFAULT_STREAM trigger
func ResolvePlaybackID(c *gin.Context) {
	playbackID := c.Param("playback_id")

	var internalName, tenantID, status string
	err := db.QueryRow(`
		SELECT internal_name, tenant_id, status FROM streams WHERE LOWER(playback_id) = LOWER($1)
	`, playbackID).Scan(&internalName, &tenantID, &status)

	if err == sql.ErrNoRows {
		c.JSON(http.StatusNotFound, commodoreapi.ErrorResponse{Error: "Playback ID not found"})
		return
	}

	if err != nil {
		logger.WithFields(logging.Fields{
			"playback_id": playbackID,
			"error":       err,
		}).Error("Database error resolving playback ID")
		c.JSON(http.StatusInternalServerError, commodoreapi.ErrorResponse{Error: "Database error"})
		return
	}

	// Only allow viewing of live streams (could be extended for recorded streams later)
	if status != "live" {
		c.JSON(http.StatusNotFound, commodoreapi.ErrorResponse{Error: "Stream not live"})
		return
	}

	c.JSON(http.StatusOK, commodoreapi.ResolvePlaybackIDResponse{
		InternalName: internalName,
		TenantID:     tenantID,
		Status:       status,
		PlaybackID:   playbackID,
	})
}

// HandlePushStatus handles push status updates from Helmsman
func HandlePushStatus(c *gin.Context) {
	var eventData map[string]interface{}
	if err := c.ShouldBindJSON(&eventData); err != nil {
		c.JSON(http.StatusBadRequest, commodoreapi.ErrorResponse{Error: "Invalid payload"})
		return
	}

	// Extract internal name if present
	internalName, _ := eventData["internal_name"].(string)
	nodeID, _ := eventData["node_id"].(string)
	eventType, _ := eventData["event_type"].(string)

	logger.WithFields(logging.Fields{
		"node_id":       nodeID,
		"internal_name": internalName,
		"event_type":    eventType,
	}).Info("Push status event")

	// Log push status event processed
	if internalName != "" {
		logger.WithFields(logging.Fields{
			"internal_name": internalName,
			"event_type":    eventType,
		}).Info("Push status event processed")
	}

	c.JSON(http.StatusOK, commodoreapi.SuccessResponse{Success: true})
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// Developer API Token Management

// CreateAPIToken creates a new API token for the authenticated user
func CreateAPIToken(c *gin.Context) {
	userID := c.GetString("user_id")
	tenantID := c.GetString("tenant_id")

	var req models.CreateAPITokenRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, commodoreapi.ErrorResponse{Error: err.Error()})
		return
	}

	// Generate API token with FrameWorks prefix
	var tokenValue string
	err := db.QueryRow(`SELECT 'fwk_' || generate_random_string(40)`).Scan(&tokenValue)
	if err != nil {
		logger.WithFields(logging.Fields{
			"user_id": userID,
			"error":   err,
		}).Error("Failed to generate API token")
		c.JSON(http.StatusInternalServerError, commodoreapi.ErrorResponse{Error: "Failed to generate token"})
		return
	}

	// Set default permissions if not provided
	permissions := req.Permissions
	if permissions == nil {
		permissions = []string{"read", "write"}
	}

	// Calculate expiration date
	var expiresAt *time.Time
	if req.ExpiresAt != nil {
		expiresAt = req.ExpiresAt
	} else {
		// Default to 30 days if not provided
		expiry := time.Now().AddDate(0, 0, 30)
		expiresAt = &expiry
	}

	// Create token record
	tokenID := uuid.New().String()
	_, err = db.Exec(`
		INSERT INTO api_tokens (id, tenant_id, user_id, token_value, token_name, permissions, is_active, expires_at, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, true, $7, NOW())
	`, tokenID, tenantID, userID, tokenValue, req.TokenName, pq.Array(permissions), expiresAt)

	if err != nil {
		logger.WithFields(logging.Fields{
			"user_id":    userID,
			"token_name": req.TokenName,
			"error":      err,
		}).Error("Failed to create API token")
		c.JSON(http.StatusInternalServerError, commodoreapi.ErrorResponse{Error: "Failed to create token"})
		return
	}

	c.JSON(http.StatusCreated, commodoreapi.CreateAPITokenResponse{
		ID:          tokenID,
		TokenValue:  tokenValue,
		TokenName:   req.TokenName,
		Permissions: permissions,
		ExpiresAt:   expiresAt,
		CreatedAt:   time.Now(),
		Message:     "API token created successfully. Store this token securely - it won't be shown again.",
	})
}

// GetAPITokens returns all API tokens for the authenticated user (without token values)
func GetAPITokens(c *gin.Context) {
	userID := c.GetString("user_id")

	rows, err := db.Query(`
		SELECT id, token_name, permissions, is_active, last_used_at, expires_at, COALESCE(created_at, NOW()) as created_at
		FROM api_tokens 
		WHERE user_id = $1 
		ORDER BY created_at DESC
	`, userID)

	if err != nil {
		logger.WithFields(logging.Fields{
			"user_id": userID,
			"error":   err,
		}).Error("Failed to fetch API tokens")
		c.JSON(http.StatusInternalServerError, commodoreapi.ErrorResponse{Error: "Failed to fetch tokens"})
		return
	}
	defer rows.Close()

	var tokens []commodoreapi.APITokenInfo
	for rows.Next() {
		var id, tokenName string
		var permissions []string
		var isActive bool
		var lastUsedAt, expiresAt *time.Time
		var createdAt time.Time

		err := rows.Scan(&id, &tokenName, &permissions, &isActive, &lastUsedAt, &expiresAt, &createdAt)
		if err != nil {
			logger.WithFields(logging.Fields{
				"user_id": userID,
				"error":   err,
			}).Error("Error scanning API token")
			continue
		}

		// Determine status
		status := "active"
		if !isActive {
			status = "revoked"
		} else if expiresAt != nil && expiresAt.Before(time.Now()) {
			status = "expired"
		}

		tokens = append(tokens, commodoreapi.APITokenInfo{
			ID:          id,
			TokenName:   tokenName,
			Permissions: permissions,
			Status:      status,
			LastUsedAt:  lastUsedAt,
			ExpiresAt:   expiresAt,
			CreatedAt:   createdAt,
		})
	}

	c.JSON(http.StatusOK, commodoreapi.APITokenListResponse{
		Tokens: tokens,
		Count:  len(tokens),
	})
}

// RevokeAPIToken revokes an API token
func RevokeAPIToken(c *gin.Context) {
	userID := c.GetString("user_id")
	tokenID := c.Param("id")

	// Verify user owns the token
	var tokenName string
	err := db.QueryRow(`
		SELECT token_name FROM api_tokens 
		WHERE id = $1 AND user_id = $2 AND is_active = true
	`, tokenID, userID).Scan(&tokenName)

	if err == sql.ErrNoRows {
		c.JSON(http.StatusNotFound, commodoreapi.ErrorResponse{Error: "API token not found or already revoked"})
		return
	}

	if err != nil {
		logger.WithFields(logging.Fields{
			"user_id":  userID,
			"token_id": tokenID,
			"error":    err,
		}).Error("Database error fetching API token")
		c.JSON(http.StatusInternalServerError, commodoreapi.ErrorResponse{Error: "Database error"})
		return
	}

	// Revoke the token
	_, err = db.Exec(`
		UPDATE api_tokens 
		SET is_active = false, updated_at = NOW()
		WHERE id = $1
	`, tokenID)

	if err != nil {
		logger.WithFields(logging.Fields{
			"user_id":    userID,
			"token_id":   tokenID,
			"token_name": tokenName,
			"error":      err,
		}).Error("Failed to revoke API token")
		c.JSON(http.StatusInternalServerError, commodoreapi.ErrorResponse{Error: "Failed to revoke token"})
		return
	}

	logger.WithFields(logging.Fields{
		"user_id":    userID,
		"token_id":   tokenID,
		"token_name": tokenName,
	}).Info("API token revoked successfully")

	c.JSON(http.StatusOK, commodoreapi.RevokeAPITokenResponse{
		Message:   "API token revoked successfully",
		TokenID:   tokenID,
		TokenName: tokenName,
		RevokedAt: time.Now(),
	})
}

// VerifyEmail handles email verification via token
func VerifyEmail(c *gin.Context) {
	token := c.Query("token")
	if token == "" {
		c.JSON(http.StatusBadRequest, commodoreapi.ErrorResponse{Error: "Verification token required"})
		return
	}

	// Find user by verification token and check expiry
	var userID, tenantID, email string
	var tokenExpiry time.Time
	err := db.QueryRow(`
		SELECT id, tenant_id, email, token_expires_at 
		FROM users 
		WHERE verification_token = $1 AND verified = false
	`, token).Scan(&userID, &tenantID, &email, &tokenExpiry)

	if err == sql.ErrNoRows {
		c.JSON(http.StatusNotFound, commodoreapi.ErrorResponse{Error: "Invalid or expired verification token"})
		return
	} else if err != nil {
		logger.WithFields(logging.Fields{
			"error": err,
			"token": token,
		}).Error("Database error during verification")
		c.JSON(http.StatusInternalServerError, commodoreapi.ErrorResponse{Error: "Database error"})
		return
	}

	// Check if token has expired
	if time.Now().After(tokenExpiry) {
		c.JSON(http.StatusBadRequest, commodoreapi.ErrorResponse{Error: "Verification token has expired"})
		return
	}

	// Mark user as verified and clear verification token
	_, err = db.Exec(`
		UPDATE users 
		SET verified = true, verification_token = NULL, token_expires_at = NULL, updated_at = NOW()
		WHERE id = $1
	`, userID)

	if err != nil {
		logger.WithFields(logging.Fields{
			"error":     err,
			"user_id":   userID,
			"tenant_id": tenantID,
		}).Error("Failed to verify user")
		c.JSON(http.StatusInternalServerError, commodoreapi.ErrorResponse{Error: "Failed to verify user"})
		return
	}

	logger.WithFields(logging.Fields{
		"user_id":   userID,
		"tenant_id": tenantID,
		"email":     email,
	}).Info("User email verified successfully")

	c.JSON(http.StatusOK, commodoreapi.EmailVerificationResponse{
		Success: true,
		Message: "Email verified successfully! You can now log in to your account.",
	})
}

// getTenantContext extracts tenant ID from request context
func getTenantContext(c *gin.Context) string {
	// First check X-Tenant-ID header
	if tenantID := c.GetHeader("X-Tenant-ID"); tenantID != "" {
		return tenantID
	}

	// Then try to resolve from domain
	host := c.Request.Host
	var domain string

	// Extract subdomain or domain
	if strings.Contains(host, ".") {
		parts := strings.SplitN(host, ".", 2)
		if len(parts) == 2 && parts[1] == "frameworks.network" {
			domain = parts[0] // Subdomain
		} else {
			domain = host // Custom domain
		}
	}

	if domain == "" {
		return ""
	}

	// Call Quartermaster to resolve tenant
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	resolution, err := quartermasterClient.ResolveTenant(ctx, &qmapi.ResolveTenantRequest{
		Domain: domain,
	})
	if err != nil {
		logger.WithError(err).Error("Failed to resolve tenant")
		return ""
	}

	if resolution.Error != "" {
		return ""
	}

	return resolution.TenantID
}

// getDefaultPermissions returns default permissions for a role
func getDefaultPermissions(role string) []string {
	switch role {
	case "owner":
		return []string{"streams:read", "streams:write", "analytics:read", "users:read", "users:write", "settings:write", "billing:read"}
	case "admin":
		return []string{"streams:read", "streams:write", "analytics:read", "users:read", "users:write"}
	case "member":
		return []string{"streams:read", "streams:write", "analytics:read"}
	case "viewer":
		return []string{"streams:read", "analytics:read"}
	default:
		return []string{"streams:read"}
	}
}

// generateRandomString generates a random string of specified length
func generateRandomString(length int) string {
	const charset = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789"
	b := make([]byte, length)
	for i := range b {
		b[i] = charset[mathrand.Intn(len(charset))]
	}
	return string(b)
}

// createUserStreamInTenant creates a stream for a user in a specific tenant
func createUserStreamInTenant(tenantID, userID, title string) (*struct {
	StreamID     string
	StreamKey    string
	PlaybackID   string
	InternalName string
}, error) {
	// Generate unique identifiers
	streamID := uuid.New().String()
	streamKey := "sk_" + generateRandomString(28)
	playbackID := generateRandomString(16)
	internalName := streamID

	// Insert the stream with tenant context
	_, err := db.Exec(`
		INSERT INTO streams (id, tenant_id, user_id, stream_key, playback_id, internal_name, title)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
	`, streamID, tenantID, userID, streamKey, playbackID, internalName, title)

	if err != nil {
		return nil, err
	}

	// Also create an entry in stream_keys for backward compatibility
	_, err = db.Exec(`
		INSERT INTO stream_keys (tenant_id, stream_id, key_value, key_name, is_active)
		VALUES ($1, $2, $3, 'Primary Key', TRUE)
	`, tenantID, streamID, streamKey)

	if err != nil {
		return nil, err
	}

	return &struct {
		StreamID     string
		StreamKey    string
		PlaybackID   string
		InternalName string
	}{
		StreamID:     streamID,
		StreamKey:    streamKey,
		PlaybackID:   playbackID,
		InternalName: internalName,
	}, nil
}

// Bot protection validation functions

func checkRegistrationRateLimit(clientIP string) bool {
	registrationMutex.Lock()
	defer registrationMutex.Unlock()

	now := time.Now()
	windowStart := now.Add(-15 * time.Minute)

	// Clean old attempts
	if attempts, exists := registrationAttempts[clientIP]; exists {
		var validAttempts []time.Time
		for _, attempt := range attempts {
			if attempt.After(windowStart) {
				validAttempts = append(validAttempts, attempt)
			}
		}
		registrationAttempts[clientIP] = validAttempts
	}

	// Check if under limit (5 per 15 minutes)
	attempts := registrationAttempts[clientIP]
	if len(attempts) >= 5 {
		return false
	}

	// Add current attempt
	registrationAttempts[clientIP] = append(attempts, now)
	return true
}

func validateBotProtection(req models.RegisterRequest, clientIP string) []string {
	var errors []string

	// 1. Honeypot check
	if req.PhoneNumber != "" {
		errors = append(errors, "Honeypot field filled (bot detected)")
	}

	// 2. Human check toggle
	if req.HumanCheck != "human" {
		errors = append(errors, "Human verification not selected")
	}

	// 3. Behavioral analysis
	if req.Behavior == "" {
		errors = append(errors, "Missing behavioral data")
	} else {
		var behaviorData map[string]interface{}
		if err := json.Unmarshal([]byte(req.Behavior), &behaviorData); err != nil {
			errors = append(errors, "Invalid behavioral data format")
		} else {
			// Check timing
			if formShownAt, ok := behaviorData["formShownAt"].(float64); ok {
				if submittedAt, ok := behaviorData["submittedAt"].(float64); ok {
					timeSpent := int64(submittedAt - formShownAt)

					// Too fast (less than 3 seconds)
					if timeSpent < 3000 {
						errors = append(errors, "Form submitted too quickly")
					}

					// Too slow (more than 30 minutes)
					if timeSpent > 30*60*1000 {
						errors = append(errors, "Form session expired")
					}
				}
			}

			// Check for human interactions
			mouse, hasMousee := behaviorData["mouse"].(bool)
			typed, hasTyped := behaviorData["typed"].(bool)

			if (!hasMousee || !mouse) && (!hasTyped || !typed) {
				errors = append(errors, "No human interaction detected")
			}
		}
	}

	// 4. Spam keyword detection
	content := strings.ToLower(req.Email)
	spamKeywords := []string{"crypto", "bitcoin", "investment", "loan", "casino", "viagra", "pharmacy", "dating", "singles"}
	for _, keyword := range spamKeywords {
		if strings.Contains(content, keyword) {
			errors = append(errors, "Potential spam keywords detected")
			break
		}
	}

	return errors
}

func generateVerificationToken() string {
	bytes := make([]byte, 32)
	rand.Read(bytes)
	return hex.EncodeToString(bytes)
}

func sendVerificationEmail(email, token, tenantName string) error {
	// Get SMTP configuration from environment
	smtpHost := os.Getenv("SMTP_HOST")
	smtpPort := os.Getenv("SMTP_PORT")
	smtpUser := os.Getenv("SMTP_USER")
	smtpPass := os.Getenv("SMTP_PASS")
	fromEmail := os.Getenv("FROM_EMAIL")

	if smtpHost == "" {
		logger.Warn("No SMTP configuration found, verification email not sent")
		return fmt.Errorf("SMTP not configured")
	}

	if smtpPort == "" {
		smtpPort = "587"
	}
	if fromEmail == "" {
		fromEmail = "noreply@frameworks.network"
	}

	// Create verification URL
	baseURL := os.Getenv("BASE_URL")
	if baseURL == "" {
		baseURL = "http://localhost:9003"
	}
	verifyURL := fmt.Sprintf("%s/verify-email?token=%s", baseURL, token)

	// Email content
	subject := fmt.Sprintf("Verify your %s account", tenantName)
	body := fmt.Sprintf(`
<!DOCTYPE html>
<html>
<head>
    <title>%s</title>
</head>
<body style="font-family: Arial, sans-serif; line-height: 1.6; color: #333;">
    <div style="max-width: 600px; margin: 0 auto; padding: 20px;">
        <h2 style="color: #2563eb;">Welcome to %s!</h2>
        <p>Thank you for registering with FrameWorks. To complete your registration and verify your email address, please click the button below:</p>
        
        <div style="text-align: center; margin: 30px 0;">
            <a href="%s" style="background-color: #2563eb; color: white; padding: 12px 24px; text-decoration: none; border-radius: 6px; display: inline-block;">
                Verify Your Email Address
            </a>
        </div>
        
        <p>If the button doesn't work, you can also copy and paste this link into your browser:</p>
        <p style="word-break: break-all; color: #666; font-size: 14px;">%s</p>
        
        <p><strong>Important:</strong> This verification link will expire in 24 hours.</p>
        
        <hr style="border: none; border-top: 1px solid #eee; margin: 30px 0;">
        <p style="font-size: 12px; color: #666;">
            If you didn't create an account with us, please ignore this email.
        </p>
    </div>
</body>
</html>
`, subject, tenantName, verifyURL, verifyURL)

	// Send email
	auth := smtp.PlainAuth("", smtpUser, smtpPass, smtpHost)

	msg := []byte(fmt.Sprintf("To: %s\r\nSubject: %s\r\nMIME-Version: 1.0\r\nContent-Type: text/html; charset=UTF-8\r\n\r\n%s",
		email, subject, body))

	err := smtp.SendMail(smtpHost+":"+smtpPort, auth, fromEmail, []string{email}, msg)
	if err != nil {
		return fmt.Errorf("failed to send email: %v", err)
	}

	return nil
}

// GetTenantKafkaConfig returns Kafka configuration for a tenant
func GetTenantKafkaConfig(c *gin.Context) {
	tenantID := c.Param("tenant_id")
	if tenantID == "" {
		c.JSON(http.StatusBadRequest, commodoreapi.ErrorResponse{Error: "tenant_id is required"})
		return
	}

	// Get Kafka config from router
	brokers, topicPrefix, err := router.GetKafkaConfigForTenant(tenantID)
	if err != nil {
		logger.WithError(err).WithField("tenant_id", tenantID).Error("Failed to get Kafka config")
		c.JSON(http.StatusInternalServerError, commodoreapi.ErrorResponse{Error: "Failed to get Kafka config"})
		return
	}

	c.JSON(http.StatusOK, commodoreapi.KafkaConfigResponse{
		Brokers:     brokers,
		TopicPrefix: topicPrefix,
	})
}

// Add these helper functions
func validateTenant(tenantID, userID string) (*models.TenantValidation, error) {
	// First, call Quartermaster to validate basic tenant info
	basicValidation, err := callQuartermasterValidation(tenantID, userID)
	if err != nil {
		return nil, fmt.Errorf("failed to validate tenant: %w", err)
	}

	if !basicValidation.Valid {
		return &models.TenantValidation{
			IsValid: false,
			Message: basicValidation.Error,
		}, nil
	}

	// Then, call Purser to get billing tier information
	tierInfo, err := callPurserTierInfo(tenantID)
	if err != nil {
		// If billing service is unavailable, return basic validation only
		logger.WithError(err).WithField("tenant_id", tenantID).Warn("Failed to get billing info, using basic validation")
		return &models.TenantValidation{
			IsValid:  basicValidation.Valid && basicValidation.IsActive,
			Tenant:   models.Tenant{ID: tenantID, Name: basicValidation.Name, IsActive: basicValidation.IsActive},
			Features: models.TenantFeatures{}, // Default empty features
			Limits:   models.TenantLimits{},   // Default empty limits
		}, nil
	}

	// Convert tier information to features and limits
	features := extractFeaturesFromTier(*tierInfo.Tier, tierInfo.Subscription.CustomFeatures)
	limits := extractLimitsFromTier(*tierInfo.Tier, tierInfo.Subscription.CustomAllocations, tierInfo.ClusterAccess)

	validation := &models.TenantValidation{
		IsValid:  basicValidation.Valid && basicValidation.IsActive && tierInfo.Subscription.Status == "active",
		Tenant:   tierInfo.Tenant,
		Features: features,
		Limits:   limits,
	}

	if !validation.IsValid {
		validation.Message = "Tenant or subscription not active"
	}

	return validation, nil
}

// callQuartermasterValidation calls Quartermaster for basic tenant validation
func callQuartermasterValidation(tenantID, userID string) (*models.ValidateTenantResponse, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	validation, err := quartermasterClient.ValidateTenant(ctx, &qmapi.ValidateTenantRequest{
		TenantID: tenantID,
		UserID:   userID,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to call Quartermaster: %w", err)
	}

	// Convert from API response type to models type
	return (*models.ValidateTenantResponse)(validation), nil
}

// callPurserTierInfo calls Purser to get tenant billing tier information
func callPurserTierInfo(tenantID string) (*models.TenantTierInfo, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	tierInfo, err := purserClient.GetTenantTierInfo(ctx, tenantID)
	if err != nil {
		return nil, fmt.Errorf("failed to call Purser: %w", err)
	}

	// Convert from API response type to models type (they're the same via type alias)
	return (*models.TenantTierInfo)(tierInfo), nil
}

// extractFeaturesFromTier converts tier features to TenantFeatures
func extractFeaturesFromTier(tier models.BillingTier, customFeatures models.JSONB) models.TenantFeatures {
	features := models.TenantFeatures{
		IsRecordingEnabled:  getBoolFromMap(map[string]interface{}(tier.Features), "recording_enabled", false),
		IsAnalyticsEnabled:  getBoolFromMap(map[string]interface{}(tier.Features), "analytics_enabled", true),
		IsAPIEnabled:        getBoolFromMap(map[string]interface{}(tier.Features), "api_enabled", true),
		IsWhiteLabelEnabled: getBoolFromMap(map[string]interface{}(tier.Features), "white_label_enabled", false),
	}

	// Override with custom features if provided
	if customFeatures != nil {
		if val, exists := customFeatures["recording_enabled"]; exists {
			if b, ok := val.(bool); ok {
				features.IsRecordingEnabled = b
			}
		}
		if val, exists := customFeatures["analytics_enabled"]; exists {
			if b, ok := val.(bool); ok {
				features.IsAnalyticsEnabled = b
			}
		}
		if val, exists := customFeatures["api_enabled"]; exists {
			if b, ok := val.(bool); ok {
				features.IsAPIEnabled = b
			}
		}
		if val, exists := customFeatures["white_label_enabled"]; exists {
			if b, ok := val.(bool); ok {
				features.IsWhiteLabelEnabled = b
			}
		}
	}

	return features
}

// extractLimitsFromTier converts tier allocations to TenantLimits
func extractLimitsFromTier(tier models.BillingTier, customAllocations models.JSONB, clusterAccess []models.TenantClusterAccess) models.TenantLimits {
	limits := models.TenantLimits{
		MaxStreams:     getIntFromMap(map[string]interface{}(tier.ComputeAllocation), "max_streams", 5),
		MaxStorageGB:   getIntFromMap(map[string]interface{}(tier.StorageAllocation), "max_storage_gb", 10),
		MaxBandwidthGB: getIntFromMap(map[string]interface{}(tier.BandwidthAllocation), "max_bandwidth_gb", 100),
		MaxUsers:       getIntFromMap(map[string]interface{}(tier.ComputeAllocation), "max_users", 10),
	}

	// Override with custom allocations if provided
	if customAllocations != nil {
		if val, exists := customAllocations["max_streams"]; exists {
			if i, ok := val.(float64); ok {
				limits.MaxStreams = int(i)
			}
		}
		if val, exists := customAllocations["max_storage_gb"]; exists {
			if i, ok := val.(float64); ok {
				limits.MaxStorageGB = int(i)
			}
		}
		if val, exists := customAllocations["max_bandwidth_gb"]; exists {
			if i, ok := val.(float64); ok {
				limits.MaxBandwidthGB = int(i)
			}
		}
		if val, exists := customAllocations["max_users"]; exists {
			if i, ok := val.(float64); ok {
				limits.MaxUsers = int(i)
			}
		}
	}

	return limits
}

// Helper functions for type-safe map access
func getBoolFromMap(m map[string]interface{}, key string, defaultVal bool) bool {
	if val, ok := m[key].(bool); ok {
		return val
	}
	return defaultVal
}

func getIntFromMap(m map[string]interface{}, key string, defaultVal int) int {
	if val, ok := m[key].(float64); ok {
		return int(val)
	}
	if val, ok := m[key].(int); ok {
		return val
	}
	return defaultVal
}

// ResolveInternalName resolves an internal_name to tenant_id for Helmsman service lookups
func ResolveInternalName(c *gin.Context) {
	internalName := c.Param("internal_name")
	if internalName == "" {
		c.JSON(http.StatusBadRequest, commodoreapi.ErrorResponse{Error: "internal_name required"})
		return
	}

	var tenantID string
	err := db.QueryRow(`SELECT tenant_id FROM streams WHERE internal_name = $1`, internalName).Scan(&tenantID)
	if err == sql.ErrNoRows {
		c.JSON(http.StatusNotFound, commodoreapi.ErrorResponse{Error: "Not found"})
		return
	}
	if err != nil {
		logger.WithError(err).Error("Failed to resolve internal_name")
		c.JSON(http.StatusInternalServerError, commodoreapi.ErrorResponse{Error: "Database error"})
		return
	}

	c.JSON(http.StatusOK, commodoreapi.InternalNameResponse{
		InternalName: internalName,
	})
}

// createTenantForRegistration creates a new tenant through Quartermaster for user registration
func createTenantForRegistration(email string) (string, error) {
	// Generate tenant name from email domain or use email prefix
	tenantName := strings.Split(email, "@")[0]

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	createResp, err := quartermasterClient.CreateTenant(ctx, &qmapi.CreateTenantRequest{
		Name:                   tenantName,
		DeploymentModel:        "shared",
		PrimaryDeploymentTier:  "global",
		AllowedDeploymentTiers: []string{"global"},
	})
	if err != nil {
		return "", fmt.Errorf("failed to call Quartermaster: %w", err)
	}

	if createResp.Error != "" {
		return "", fmt.Errorf("Quartermaster error: %s", createResp.Error)
	}

	logger.WithFields(logging.Fields{
		"tenant_id":   createResp.Tenant.ID,
		"tenant_name": tenantName,
		"email":       email,
	}).Info("Created new tenant for user registration")

	return createResp.Tenant.ID, nil
}
