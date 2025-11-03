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
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"frameworks/pkg/logging"

	jwt "github.com/golang-jwt/jwt/v5"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/lib/pq"

	commodoreapi "frameworks/pkg/api/commodore"
	fapi "frameworks/pkg/api/foghorn"
	purserapi "frameworks/pkg/api/purser"
	qmapi "frameworks/pkg/api/quartermaster"
	"frameworks/pkg/auth"
	"frameworks/pkg/cache"
	"frameworks/pkg/clients"
	foghorn "frameworks/pkg/clients/foghorn"
	purserclient "frameworks/pkg/clients/purser"
	qmclient "frameworks/pkg/clients/quartermaster"
	"frameworks/pkg/config"
	"frameworks/pkg/models"
)

// HandlerMetrics holds the metrics for handler operations
type HandlerMetrics struct {
	AuthOperations   *prometheus.CounterVec
	AuthDuration     *prometheus.HistogramVec
	ActiveSessions   *prometheus.GaugeVec
	LoginAttempts    *prometheus.CounterVec
	RegistrationFlow *prometheus.CounterVec
	StreamOperations *prometheus.CounterVec
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

type ClipRequest struct {
	ID         string
	ClipHash   string
	FoghornURL string
	TenantID   string
	Status     string
	CreatedAt  time.Time
}

type ActiveRequestTracker struct {
	mutex    sync.RWMutex
	requests map[string]*ClipRequest
}

func NewActiveRequestTracker() *ActiveRequestTracker {
	return &ActiveRequestTracker{
		requests: make(map[string]*ClipRequest),
	}
}

func (t *ActiveRequestTracker) Add(req *ClipRequest) {
	t.mutex.Lock()
	defer t.mutex.Unlock()
	t.requests[req.ID] = req
}

func (t *ActiveRequestTracker) GetByTenant(tenantID string) []*ClipRequest {
	t.mutex.RLock()
	defer t.mutex.RUnlock()
	var result []*ClipRequest
	for _, req := range t.requests {
		if req.TenantID == tenantID {
			result = append(result, req)
		}
	}
	return result
}

func (t *ActiveRequestTracker) Remove(id string) {
	t.mutex.Lock()
	defer t.mutex.Unlock()
	delete(t.requests, id)
}

var activeRequests = NewActiveRequestTracker()

func Init(database *sql.DB, log logging.Logger, r Router, m *HandlerMetrics) {
	db = database
	logger = log
	router = r
	metrics = m

	quartermasterURL = config.RequireEnv("QUARTERMASTER_URL")
	purserURL = config.RequireEnv("PURSER_URL")
	serviceToken = config.RequireEnv("SERVICE_TOKEN")

	// Quartermaster cache
	qmTTLSecs := getEnvInt("QUARTERMASTER_CACHE_TTL_SECONDS", 60)
	qmSWRSecs := getEnvInt("QUARTERMASTER_CACHE_SWR_SECONDS", 30)
	qmNegSecs := getEnvInt("QUARTERMASTER_CACHE_NEG_TTL_SECONDS", 10)
	qmMax := getEnvInt("QUARTERMASTER_CACHE_MAX", 10000)
	qmCache := cache.New(cache.Options{TTL: time.Duration(qmTTLSecs) * time.Second, StaleWhileRevalidate: time.Duration(qmSWRSecs) * time.Second, NegativeTTL: time.Duration(qmNegSecs) * time.Second, MaxEntries: qmMax}, cache.MetricsHooks{})

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
		Cache: qmCache,
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

func getEnvInt(key string, def int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
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

		c.JSON(http.StatusOK, commodoreapi.RegisterResponse{
			Success: true,
			Message: "Registration successful. Please check your email to verify your account.",
		})
		return
	}

	if err := verifyTurnstileToken(req.TurnstileToken, clientIP); err != nil {
		logger.WithFields(logging.Fields{
			"ip":    clientIP,
			"email": req.Email,
			"error": err.Error(),
		}).Warn("Turnstile validation failed")
		if metrics != nil {
			metrics.AuthOperations.WithLabelValues("register", "error").Inc()
		}

		c.JSON(http.StatusBadRequest, commodoreapi.ErrorResponse{Error: "Turnstile verification failed"})
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
	err = db.QueryRow("SELECT id FROM commodore.users WHERE tenant_id = $1 AND email = $2", tenantID, req.Email).Scan(&existingID)
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
	err = db.QueryRow("SELECT COUNT(*) FROM commodore.users WHERE tenant_id = $1", tenantID).Scan(&userCount)
	if err == nil && userCount == 0 {
		role = "owner"
	}

	_, err = db.Exec(`
		INSERT INTO commodore.users (id, tenant_id, email, password_hash, role, permissions, verified, verification_token, token_expires_at) 
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

	// Validate Turnstile token if configured
	clientIP := c.ClientIP()
	if err := verifyTurnstileToken(req.TurnstileToken, clientIP); err != nil {
		logger.WithFields(logging.Fields{
			"ip":    clientIP,
			"email": req.Email,
			"error": err,
		}).Warn("Login attempt failed Turnstile verification")
		c.JSON(http.StatusBadRequest, commodoreapi.ErrorResponse{Error: "Bot verification failed"})
		return
	}

	// Find user by email (shared tenancy)
	var user models.User
	err := db.QueryRow(`
		SELECT id, tenant_id, email, password_hash, role, verified, is_active, created_at
		FROM commodore.users WHERE email = $1
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
	_, err = db.Exec("UPDATE commodore.users SET last_login_at = NOW() WHERE id = $1", user.ID)
	if err != nil {
		logger.WithFields(logging.Fields{
			"error":   err,
			"user_id": user.ID,
		}).Warn("Failed to update last login timestamp")
	}

	// Generate JWT token
	jwtSecret := []byte(config.RequireEnv("JWT_SECRET"))
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
		SELECT id, email, role, created_at, is_active 
		FROM commodore.users WHERE id = $1
	`, userID).Scan(&user.ID, &user.Email, &user.Role, &user.CreatedAt, &user.IsActive)

	if err != nil {
		c.JSON(http.StatusNotFound, commodoreapi.ErrorResponse{Error: "User not found"})
		return
	}

	// Get user's streams
	tenantID := c.GetString("tenant_id")
	rows, err := db.Query(`
		SELECT internal_name, stream_key, playback_id, title, status 
		FROM commodore.streams WHERE user_id = $1 AND tenant_id = $2 ORDER BY created_at DESC
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
            FROM commodore.streams 
            WHERE tenant_id = $1
            ORDER BY created_at DESC
        `
		args = []interface{}{tenantID}
	} else {
		// Regular users see only their own streams
		query = `
            SELECT stream_key, playback_id, internal_name, title, description,
                   is_recording_enabled, is_public, status, start_time, end_time, created_at, updated_at
            FROM commodore.streams 
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

		// Keep GraphQL 'record' field in sync
		stream.IsRecording = stream.IsRecordingEnabled

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
        FROM commodore.streams WHERE internal_name = $1 AND user_id = $2 AND tenant_id = $3
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
	stream.IsRecording = stream.IsRecordingEnabled

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
		SELECT EXISTS(SELECT 1 FROM commodore.streams WHERE internal_name = $1 AND user_id = $2 AND tenant_id = $3)
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
	var metrics commodoreapi.StreamMetrics

	err = db.QueryRow(`
		SELECT viewers, status, bitrate, resolution, max_viewers, updated_at
		FROM commodore.streams WHERE internal_name = $1 AND user_id = $2 AND tenant_id = $3
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
	var recordingConfigJSON []byte
	err := db.QueryRow(`
		SELECT s.id, s.user_id, s.tenant_id, s.internal_name, u.is_active, s.recording_config
		FROM commodore.streams s
		JOIN commodore.users u ON s.user_id = u.id
		WHERE LOWER(s.stream_key) = LOWER($1)
	`, streamKey).Scan(&streamID, &userID, &tenantID, &internalName, &isActive, &recordingConfigJSON)

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

	// Parse recording configuration
	var recordingConfig *commodoreapi.RecordingConfig
	if len(recordingConfigJSON) > 0 {
		var config commodoreapi.RecordingConfig
		if err := json.Unmarshal(recordingConfigJSON, &config); err == nil {
			recordingConfig = &config
		} else {
			logger.WithFields(logging.Fields{
				"stream_key": streamKey,
				"error":      err,
			}).Warn("Failed to parse recording config JSON")
		}
	}

	// Update last used timestamp for the stream key (also case-insensitive)
	_, err = db.Exec(`
		UPDATE commodore.stream_keys SET last_used_at = NOW() 
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
		Recording:    recordingConfig,
	})
}

// CreateClip creates a clip from a stream with request tracking
func CreateClip(c *gin.Context) {
	tenantID := c.GetString("tenant_id")
	if tenantID == "" {
		c.JSON(http.StatusUnauthorized, commodoreapi.ErrorResponse{Error: "Authentication required"})
		return
	}

	var req commodoreapi.ClipCreateRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, commodoreapi.ErrorResponse{Error: err.Error()})
		return
	}

	foghornList := os.Getenv("FOGHORN_URLS")
	if foghornList == "" {
		foghornList = os.Getenv("FOGHORN_URL")
	}
	if foghornList == "" {
		c.JSON(http.StatusServiceUnavailable, commodoreapi.ErrorResponse{Error: "FOGHORN_URLS or FOGHORN_URL not configured"})
		return
	}
	urls := strings.Split(foghornList, ",")

	// Typed request
	fReq := fapi.CreateClipRequest{
		TenantID:     tenantID,
		InternalName: req.InternalName,
		Format:       req.Format,
		Title:        req.Title,
		StartUnix:    req.StartUnix,
		StopUnix:     req.StopUnix,
		StartMS:      req.StartMS,
		StopMS:       req.StopMS,
		DurationSec:  req.DurationSec,
	}

	// Try each Foghorn until one succeeds
	var lastErr error
	for _, raw := range urls {
		base := strings.TrimSpace(raw)
		if base == "" {
			continue
		}
		if !strings.HasPrefix(base, "http://") && !strings.HasPrefix(base, "https://") {
			base = "http://" + base
		}

		cli := foghorn.NewClient(foghorn.Config{BaseURL: base, ServiceToken: serviceToken, Timeout: 30 * time.Second, Logger: logger})
		res, err := cli.CreateClip(c.Request.Context(), &fReq)
		if err == nil {
			// Success! Track this request for real-time status updates
			if res.ClipHash != "" {
				requestID := uuid.New().String()
				clipRequest := &ClipRequest{
					ID:         requestID,
					ClipHash:   res.ClipHash,
					FoghornURL: base,
					TenantID:   tenantID,
					Status:     "processing",
					CreatedAt:  time.Now(),
				}
				activeRequests.Add(clipRequest)

				logger.WithFields(logging.Fields{
					"request_id":  requestID,
					"clip_hash":   res.ClipHash,
					"tenant_id":   tenantID,
					"foghorn_url": base,
				}).Info("Tracking active clip request")
			}

			c.JSON(http.StatusOK, res)
			return
		}
		lastErr = err
	}

	c.JSON(http.StatusBadGateway, commodoreapi.ErrorResponse{Error: fmt.Sprintf("all foghorns failed: %v", lastErr)})
}

// GetClips lists clips for the authenticated user
func GetClips(c *gin.Context) {
	tenantID := c.GetString("tenant_id")
	if tenantID == "" {
		c.JSON(http.StatusUnauthorized, commodoreapi.ErrorResponse{Error: "Authentication required"})
		return
	}

	// Parse pagination parameters
	page := 1
	limit := 20
	if pageStr := c.Query("page"); pageStr != "" {
		if p, err := strconv.Atoi(pageStr); err == nil && p > 0 {
			page = p
		}
	}
	if limitStr := c.Query("limit"); limitStr != "" {
		if l, err := strconv.Atoi(limitStr); err == nil && l > 0 && l <= 100 {
			limit = l
		}
	}

	// Get list of Foghorn URLs
	foghornList := os.Getenv("FOGHORN_URLS")
	if foghornList == "" {
		foghornList = os.Getenv("FOGHORN_URL")
	}
	if foghornList == "" {
		c.JSON(http.StatusServiceUnavailable, commodoreapi.ErrorResponse{Error: "FOGHORN_URLS not configured"})
		return
	}
	urls := strings.Split(foghornList, ",")

	// Query all Foghorns and merge results
	var allClips []commodoreapi.ClipFullResponse
	var totalCount int

	// First, check for active requests and query their specific Foghorns for real-time status
	activeReqs := activeRequests.GetByTenant(tenantID)
	for _, req := range activeReqs {
		if req.FoghornURL != "" {
			cli := foghorn.NewClient(foghorn.Config{BaseURL: req.FoghornURL, ServiceToken: serviceToken, Timeout: 10 * time.Second, Logger: logger})
			clipRes, err := cli.GetClip(c.Request.Context(), req.ClipHash, tenantID)
			if err == nil {
				// Convert to Commodore format
				commodoreClip := commodoreapi.ClipFullResponse{
					ID:          clipRes.ID,
					ClipHash:    clipRes.ClipHash,
					StreamName:  clipRes.StreamName,
					Title:       clipRes.Title,
					StartTime:   clipRes.StartTime,
					Duration:    clipRes.Duration,
					NodeID:      clipRes.NodeID,
					StoragePath: clipRes.StoragePath,
					SizeBytes:   clipRes.SizeBytes,
					Status:      clipRes.Status,
					AccessCount: clipRes.AccessCount,
					CreatedAt:   clipRes.CreatedAt,
				}
				allClips = append(allClips, commodoreClip)

				// If clip is now ready or failed, remove from active tracking
				if clipRes.Status == "ready" || clipRes.Status == "failed" {
					activeRequests.Remove(req.ID)
				}
			}
		}
	}

	// Then query all Foghorns for remaining clips
	for _, rawURL := range urls {
		base := strings.TrimSpace(rawURL)
		if base == "" {
			continue
		}
		if !strings.HasPrefix(base, "http://") && !strings.HasPrefix(base, "https://") {
			base = "http://" + base
		}

		cli := foghorn.NewClient(foghorn.Config{BaseURL: base, ServiceToken: serviceToken, Timeout: 30 * time.Second, Logger: logger})
		res, err := cli.GetClips(c.Request.Context(), tenantID, page, limit)
		if err != nil {
			logger.WithError(err).WithField("foghorn_url", base).Warn("Failed to query clips from Foghorn")
			continue
		}

		// Convert Foghorn ClipInfo to Commodore ClipFullResponse, avoiding duplicates
		for _, clip := range res.Clips {
			// Check if we already have this clip from active requests
			isDuplicate := false
			for _, existing := range allClips {
				if existing.ClipHash == clip.ClipHash {
					isDuplicate = true
					break
				}
			}

			if !isDuplicate {
				commodoreClip := commodoreapi.ClipFullResponse{
					ID:          clip.ID,
					ClipHash:    clip.ClipHash,
					StreamName:  clip.StreamName,
					Title:       clip.Title,
					StartTime:   clip.StartTime,
					Duration:    clip.Duration,
					NodeID:      clip.NodeID,
					StoragePath: clip.StoragePath,
					SizeBytes:   clip.SizeBytes,
					Status:      clip.Status,
					AccessCount: clip.AccessCount,
					CreatedAt:   clip.CreatedAt,
				}
				allClips = append(allClips, commodoreClip)
			}
		}
		totalCount += res.Total
	}

	// Sort by creation time (newest first)
	sort.Slice(allClips, func(i, j int) bool {
		return allClips[i].CreatedAt.After(allClips[j].CreatedAt)
	})

	// Apply pagination to merged results
	start := (page - 1) * limit
	end := start + limit
	if end > len(allClips) {
		end = len(allClips)
	}
	if start > len(allClips) {
		start = len(allClips)
		allClips = []commodoreapi.ClipFullResponse{}
	} else {
		allClips = allClips[start:end]
	}

	c.JSON(http.StatusOK, commodoreapi.ClipsListResponse{
		Clips: allClips,
		Total: totalCount,
		Page:  page,
		Limit: limit,
	})
}

// GetClip retrieves a specific clip by ID
func GetClip(c *gin.Context) {
	tenantID := c.GetString("tenant_id")
	clipID := c.Param("id")

	if tenantID == "" {
		c.JSON(http.StatusUnauthorized, commodoreapi.ErrorResponse{Error: "Authentication required"})
		return
	}
	if clipID == "" {
		c.JSON(http.StatusBadRequest, commodoreapi.ErrorResponse{Error: "clip ID is required"})
		return
	}

	// First, we need to find the clip_hash for this ID by querying all Foghorns
	foghornList := os.Getenv("FOGHORN_URLS")
	if foghornList == "" {
		foghornList = os.Getenv("FOGHORN_URL")
	}
	if foghornList == "" {
		c.JSON(http.StatusServiceUnavailable, commodoreapi.ErrorResponse{Error: "FOGHORN_URLS not configured"})
		return
	}
	urls := strings.Split(foghornList, ",")

	// Query each Foghorn to find the clip
	for _, rawURL := range urls {
		base := strings.TrimSpace(rawURL)
		if base == "" {
			continue
		}
		if !strings.HasPrefix(base, "http://") && !strings.HasPrefix(base, "https://") {
			base = "http://" + base
		}

		cli := foghorn.NewClient(foghorn.Config{BaseURL: base, ServiceToken: serviceToken, Timeout: 30 * time.Second, Logger: logger})

		// Get all clips and search for matching ID
		res, err := cli.GetClips(c.Request.Context(), tenantID, 1, 1000) // Large page to find the clip
		if err != nil {
			continue
		}

		for _, clip := range res.Clips {
			if clip.ID == clipID {
				// Found it! Convert to Commodore format
				commodoreClip := commodoreapi.ClipFullResponse{
					ID:          clip.ID,
					ClipHash:    clip.ClipHash,
					StreamName:  clip.StreamName,
					Title:       clip.Title,
					StartTime:   clip.StartTime,
					Duration:    clip.Duration,
					NodeID:      clip.NodeID,
					StoragePath: clip.StoragePath,
					SizeBytes:   clip.SizeBytes,
					Status:      clip.Status,
					AccessCount: clip.AccessCount,
					CreatedAt:   clip.CreatedAt,
				}
				c.JSON(http.StatusOK, commodoreClip)
				return
			}
		}
	}

	c.JSON(http.StatusNotFound, commodoreapi.ErrorResponse{Error: "Clip not found"})
}

// GetClipURLs generates viewing URLs for a clip
func GetClipURLs(c *gin.Context) {
	tenantID := c.GetString("tenant_id")
	clipID := c.Param("id")

	if tenantID == "" {
		c.JSON(http.StatusUnauthorized, commodoreapi.ErrorResponse{Error: "Authentication required"})
		return
	}
	if clipID == "" {
		c.JSON(http.StatusBadRequest, commodoreapi.ErrorResponse{Error: "clip ID is required"})
		return
	}

	// Find the clip and get its hash
	foghornList := os.Getenv("FOGHORN_URLS")
	if foghornList == "" {
		foghornList = os.Getenv("FOGHORN_URL")
	}
	if foghornList == "" {
		c.JSON(http.StatusServiceUnavailable, commodoreapi.ErrorResponse{Error: "FOGHORN_URLS not configured"})
		return
	}
	urls := strings.Split(foghornList, ",")

	// Query each Foghorn to find the clip
	for _, rawURL := range urls {
		base := strings.TrimSpace(rawURL)
		if base == "" {
			continue
		}
		if !strings.HasPrefix(base, "http://") && !strings.HasPrefix(base, "https://") {
			base = "http://" + base
		}

		cli := foghorn.NewClient(foghorn.Config{BaseURL: base, ServiceToken: serviceToken, Timeout: 30 * time.Second, Logger: logger})

		// Get all clips and search for matching ID
		res, err := cli.GetClips(c.Request.Context(), tenantID, 1, 1000)
		if err != nil {
			continue
		}

		for _, clip := range res.Clips {
			if clip.ID == clipID {
				// Found it! Get node information for viewing URLs
				nodeInfo, err := cli.GetClipNode(c.Request.Context(), clip.ClipHash, tenantID)
				if err != nil {
					logger.WithError(err).Error("Failed to get clip node info")
					c.JSON(http.StatusServiceUnavailable, commodoreapi.ErrorResponse{Error: "Failed to get viewing URLs"})
					return
				}

				// Generate URLs using the node's outputs
				urls := make(map[string]string)
				vodStreamName := fmt.Sprintf("vod+%s", clip.ClipHash)

				baseURL := nodeInfo.BaseURL
				if baseURL == "" {
					baseURL = "https://unknown-node"
				}

				// Common protocols for VOD
				protocols := []string{"HLS", "DASH", "webrtc", "progressive"}
				for _, protocol := range protocols {
					if protocolData, exists := nodeInfo.Outputs[protocol]; exists {
						if protocolMap, ok := protocolData.(map[string]interface{}); ok {
							if urlPattern, exists := protocolMap["url"]; exists {
								if urlStr, ok := urlPattern.(string); ok {
									// Replace wildcard with VOD stream name
									finalURL := strings.ReplaceAll(urlStr, "$", vodStreamName)
									if !strings.HasPrefix(finalURL, "http") {
										finalURL = baseURL + finalURL
									}
									urls[strings.ToLower(protocol)] = finalURL
								}
							}
						}
					}
				}

				c.JSON(http.StatusOK, commodoreapi.ClipViewingURLs{
					URLs: urls,
				})
				return
			}
		}
	}

	c.JSON(http.StatusNotFound, commodoreapi.ErrorResponse{Error: "Clip not found"})
}

// DeleteClip soft-deletes a clip
func DeleteClip(c *gin.Context) {
	tenantID := c.GetString("tenant_id")
	clipID := c.Param("id")

	if tenantID == "" {
		c.JSON(http.StatusUnauthorized, commodoreapi.ErrorResponse{Error: "Authentication required"})
		return
	}
	if clipID == "" {
		c.JSON(http.StatusBadRequest, commodoreapi.ErrorResponse{Error: "clip ID is required"})
		return
	}

	// Find the clip and get its hash
	foghornList := os.Getenv("FOGHORN_URLS")
	if foghornList == "" {
		foghornList = os.Getenv("FOGHORN_URL")
	}
	if foghornList == "" {
		c.JSON(http.StatusServiceUnavailable, commodoreapi.ErrorResponse{Error: "FOGHORN_URLS not configured"})
		return
	}
	urls := strings.Split(foghornList, ",")

	// Query each Foghorn to find and delete the clip
	for _, rawURL := range urls {
		base := strings.TrimSpace(rawURL)
		if base == "" {
			continue
		}
		if !strings.HasPrefix(base, "http://") && !strings.HasPrefix(base, "https://") {
			base = "http://" + base
		}

		cli := foghorn.NewClient(foghorn.Config{BaseURL: base, ServiceToken: serviceToken, Timeout: 30 * time.Second, Logger: logger})

		// Get all clips and search for matching ID
		res, err := cli.GetClips(c.Request.Context(), tenantID, 1, 1000)
		if err != nil {
			continue
		}

		for _, clip := range res.Clips {
			if clip.ID == clipID {
				// Found it! Delete using clip hash
				if err := cli.DeleteClip(c.Request.Context(), clip.ClipHash, tenantID); err != nil {
					logger.WithError(err).Error("Failed to delete clip")
					c.JSON(http.StatusInternalServerError, commodoreapi.ErrorResponse{Error: "Failed to delete clip"})
					return
				}

				c.JSON(http.StatusOK, commodoreapi.SuccessResponse{
					Message: "Clip deleted successfully",
				})
				return
			}
		}
	}

	c.JSON(http.StatusNotFound, commodoreapi.ErrorResponse{Error: "Clip not found"})
}

// StartDVR starts a DVR recording for a stream by proxying to Foghorn
func StartDVR(c *gin.Context) {
	tenantID := c.GetString("tenant_id")
	if tenantID == "" {
		c.JSON(http.StatusUnauthorized, commodoreapi.ErrorResponse{Error: "Authentication required"})
		return
	}

	var req commodoreapi.StreamClipRequest
	if err := c.ShouldBindJSON(&req); err != nil || req.InternalName == "" {
		c.JSON(http.StatusBadRequest, commodoreapi.ErrorResponse{Error: "internal_name is required"})
		return
	}

	foghornList := os.Getenv("FOGHORN_URLS")
	if foghornList == "" {
		foghornList = os.Getenv("FOGHORN_URL")
	}
	if foghornList == "" {
		c.JSON(http.StatusServiceUnavailable, commodoreapi.ErrorResponse{Error: "FOGHORN_URLS not configured"})
		return
	}
	urls := strings.Split(foghornList, ",")

	var lastErr error
	for _, rawURL := range urls {
		base := strings.TrimSpace(rawURL)
		if base == "" {
			continue
		}
		if !strings.HasPrefix(base, "http://") && !strings.HasPrefix(base, "https://") {
			base = "http://" + base
		}
		cli := foghorn.NewClient(foghorn.Config{BaseURL: base, ServiceToken: serviceToken, Timeout: 20 * time.Second, Logger: logger})

		ctx, cancel := context.WithTimeout(c.Request.Context(), 10*time.Second)
		defer cancel()
		res, err := cli.StartDVRRecording(ctx, &fapi.StartDVRRequest{TenantID: tenantID, InternalName: req.InternalName, StreamID: req.StreamID})
		if err == nil {
			c.JSON(http.StatusOK, res)
			return
		}
		lastErr = err
	}

	c.JSON(http.StatusBadGateway, commodoreapi.ErrorResponse{Error: fmt.Sprintf("all foghorns failed: %v", lastErr)})
}

// StopDVR stops an active DVR recording
func StopDVR(c *gin.Context) {
	tenantID := c.GetString("tenant_id")
	if tenantID == "" {
		c.JSON(http.StatusUnauthorized, commodoreapi.ErrorResponse{Error: "Authentication required"})
		return
	}
	var req commodoreapi.DVRClipRequest
	if err := c.ShouldBindJSON(&req); err != nil || req.DVRHash == "" {
		c.JSON(http.StatusBadRequest, commodoreapi.ErrorResponse{Error: "dvr_hash is required"})
		return
	}

	foghornList := os.Getenv("FOGHORN_URLS")
	if foghornList == "" {
		foghornList = os.Getenv("FOGHORN_URL")
	}
	if foghornList == "" {
		c.JSON(http.StatusServiceUnavailable, commodoreapi.ErrorResponse{Error: "FOGHORN_URLS not configured"})
		return
	}
	urls := strings.Split(foghornList, ",")

	var lastErr error
	for _, rawURL := range urls {
		base := strings.TrimSpace(rawURL)
		if base == "" {
			continue
		}
		if !strings.HasPrefix(base, "http://") && !strings.HasPrefix(base, "https://") {
			base = "http://" + base
		}
		cli := foghorn.NewClient(foghorn.Config{BaseURL: base, ServiceToken: serviceToken, Timeout: 15 * time.Second, Logger: logger})
		ctx, cancel := context.WithTimeout(c.Request.Context(), 8*time.Second)
		defer cancel()
		if err := cli.StopDVRRecording(ctx, req.DVRHash, tenantID); err == nil {
			c.JSON(http.StatusOK, commodoreapi.SuccessResponse{Message: "stopping"})
			return
		} else {
			lastErr = err
		}
	}

	c.JSON(http.StatusBadGateway, commodoreapi.ErrorResponse{Error: fmt.Sprintf("all foghorns failed: %v", lastErr)})
}

// GetDVRStatus retrieves DVR status for a given hash
func GetDVRStatus(c *gin.Context) {
	tenantID := c.GetString("tenant_id")
	if tenantID == "" {
		c.JSON(http.StatusUnauthorized, commodoreapi.ErrorResponse{Error: "Authentication required"})
		return
	}
	dvrHash := c.Param("dvr_hash")
	if dvrHash == "" {
		c.JSON(http.StatusBadRequest, commodoreapi.ErrorResponse{Error: "dvr_hash required"})
		return
	}

	foghornList := os.Getenv("FOGHORN_URLS")
	if foghornList == "" {
		foghornList = os.Getenv("FOGHORN_URL")
	}
	if foghornList == "" {
		c.JSON(http.StatusServiceUnavailable, commodoreapi.ErrorResponse{Error: "FOGHORN_URLS not configured"})
		return
	}
	urls := strings.Split(foghornList, ",")

	for _, rawURL := range urls {
		base := strings.TrimSpace(rawURL)
		if base == "" {
			continue
		}
		if !strings.HasPrefix(base, "http://") && !strings.HasPrefix(base, "https://") {
			base = "http://" + base
		}
		cli := foghorn.NewClient(foghorn.Config{BaseURL: base, ServiceToken: serviceToken, Timeout: 10 * time.Second, Logger: logger})
		ctx, cancel := context.WithTimeout(c.Request.Context(), 6*time.Second)
		defer cancel()
		if info, err := cli.GetDVRStatus(ctx, dvrHash, tenantID); err == nil {
			c.JSON(http.StatusOK, info)
			return
		}
	}

	c.JSON(http.StatusNotFound, commodoreapi.ErrorResponse{Error: "DVR recording not found"})
}

// ListDVRRequests lists DVR recordings for a tenant (optionally filtered)
func ListDVRRequests(c *gin.Context) {
	tenantID := c.GetString("tenant_id")
	if tenantID == "" {
		c.JSON(http.StatusUnauthorized, commodoreapi.ErrorResponse{Error: "Authentication required"})
		return
	}
	internalName := c.Query("internal_name")
	status := c.Query("status")
	page, _ := strconv.Atoi(c.Query("page"))
	limit, _ := strconv.Atoi(c.Query("limit"))
	if page <= 0 {
		page = 1
	}
	if limit <= 0 || limit > 100 {
		limit = 20
	}

	foghornList := os.Getenv("FOGHORN_URLS")
	if foghornList == "" {
		foghornList = os.Getenv("FOGHORN_URL")
	}
	if foghornList == "" {
		c.JSON(http.StatusServiceUnavailable, commodoreapi.ErrorResponse{Error: "FOGHORN_URLS not configured"})
		return
	}
	urls := strings.Split(foghornList, ",")

	var merged []fapi.DVRInfo
	total := 0
	for _, rawURL := range urls {
		base := strings.TrimSpace(rawURL)
		if base == "" {
			continue
		}
		if !strings.HasPrefix(base, "http://") && !strings.HasPrefix(base, "https://") {
			base = "http://" + base
		}
		cli := foghorn.NewClient(foghorn.Config{BaseURL: base, ServiceToken: serviceToken, Timeout: 15 * time.Second, Logger: logger})
		ctx, cancel := context.WithTimeout(c.Request.Context(), 8*time.Second)
		defer cancel()
		res, err := cli.ListDVRRecordings(ctx, tenantID, internalName, status, page, limit)
		if err != nil {
			continue
		}
		merged = append(merged, res.DVRRecordings...)
		total += res.Total
	}

	c.JSON(http.StatusOK, gin.H{
		"dvr_recordings": merged,
		"total":          total,
		"page":           page,
		"limit":          limit,
	})
}

// GetRecordingConfig returns recording_config for a stream
func GetRecordingConfig(c *gin.Context) {
	internalName := c.Param("id") // Now expects internal_name as :id parameter
	if internalName == "" {
		c.JSON(http.StatusBadRequest, commodoreapi.ErrorResponse{Error: "stream ID required"})
		return
	}
	var cfgJSON []byte
	if err := db.QueryRow(`SELECT recording_config FROM commodore.streams WHERE internal_name = $1`, internalName).Scan(&cfgJSON); err != nil {
		if err == sql.ErrNoRows {
			c.JSON(http.StatusNotFound, commodoreapi.ErrorResponse{Error: "stream not found"})
			return
		}
		logger.WithError(err).Error("Failed to fetch recording_config")
		c.JSON(http.StatusInternalServerError, commodoreapi.ErrorResponse{Error: "database error"})
		return
	}
	if len(cfgJSON) == 0 {
		c.JSON(http.StatusOK, commodoreapi.RecordingConfig{Enabled: false, RetentionDays: 30, Format: "ts", SegmentDuration: 6})
		return
	}
	var cfg commodoreapi.RecordingConfig
	if err := json.Unmarshal(cfgJSON, &cfg); err != nil {
		c.JSON(http.StatusOK, commodoreapi.RecordingConfig{Enabled: false, RetentionDays: 30, Format: "ts", SegmentDuration: 6})
		return
	}
	c.JSON(http.StatusOK, cfg)
}

// UpdateRecordingConfig updates recording_config for a stream
func UpdateRecordingConfig(c *gin.Context) {
	tenantID := c.GetString("tenant_id")
	if tenantID == "" {
		c.JSON(http.StatusUnauthorized, commodoreapi.ErrorResponse{Error: "Authentication required"})
		return
	}
	internalName := c.Param("id") // Now expects internal_name as :id parameter
	if internalName == "" {
		c.JSON(http.StatusBadRequest, commodoreapi.ErrorResponse{Error: "stream ID required"})
		return
	}
	var cfg commodoreapi.RecordingConfig
	if err := c.ShouldBindJSON(&cfg); err != nil {
		c.JSON(http.StatusBadRequest, commodoreapi.ErrorResponse{Error: "invalid body"})
		return
	}
	b, _ := json.Marshal(cfg)
	// Ensure tenant ownership when updating
	res, err := db.Exec(`UPDATE commodore.streams SET recording_config = $1::jsonb, updated_at = NOW() WHERE internal_name = $2 AND tenant_id = $3`, string(b), internalName, tenantID)
	if err != nil {
		logger.WithError(err).Error("Failed to update recording_config")
		c.JSON(http.StatusInternalServerError, commodoreapi.ErrorResponse{Error: "database error"})
		return
	}
	if rows, _ := res.RowsAffected(); rows == 0 {
		c.JSON(http.StatusNotFound, commodoreapi.ErrorResponse{Error: "stream not found"})
		return
	}
	c.JSON(http.StatusOK, cfg)
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
		FROM commodore.stream_keys 
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
	tenantID := c.GetString("tenant_id")

	var req commodoreapi.CreateStreamRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, commodoreapi.ErrorResponse{Error: err.Error()})
		return
	}

	var streamID, streamKey, playbackID, internalName string
	err := db.QueryRow(`
		SELECT stream_id, stream_key, playback_id, internal_name 
		FROM commodore.create_user_stream($1, $2, $3)
	`, tenantID, userID, req.Title).Scan(&streamID, &streamKey, &playbackID, &internalName)

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
			UPDATE commodore.streams SET description = $1 WHERE id = $2
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
	tenantID := c.GetString("tenant_id")
	streamID := c.Param("id")

	// Verify user owns the stream and get stream details
	var streamUUID, streamKey, internalName, title string
	err := db.QueryRow(`
		SELECT id, stream_key, internal_name, title 
		FROM commodore.streams 
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
	_, err = tx.Exec(`DELETE FROM commodore.stream_keys WHERE stream_id = $1`, streamID)
	if err != nil {
		logger.WithFields(logging.Fields{
			"user_id":   userID,
			"stream_id": streamID,
			"error":     err,
		}).Error("Failed to delete stream keys")
		c.JSON(http.StatusInternalServerError, commodoreapi.ErrorResponse{Error: "Failed to cleanup stream keys"})
		return
	}

	// Delete related clips via Foghorn API (no cross-service DB access)
	foghornList := os.Getenv("FOGHORN_URLS")
	if foghornList == "" {
		foghornList = os.Getenv("FOGHORN_URL")
	}
	if foghornList != "" {
		urls := strings.Split(foghornList, ",")
		for _, rawURL := range urls {
			base := strings.TrimSpace(rawURL)
			if base == "" {
				continue
			}
			if !strings.HasPrefix(base, "http://") && !strings.HasPrefix(base, "https://") {
				base = "http://" + base
			}
			cli := foghorn.NewClient(foghorn.Config{BaseURL: base, ServiceToken: serviceToken, Timeout: 30 * time.Second, Logger: logger})
			res, err := cli.GetClips(c.Request.Context(), tenantID, 1, 1000)
			if err != nil {
				logger.WithError(err).WithField("foghorn_url", base).Warn("Failed to list clips for deletion")
				continue
			}
			for _, clip := range res.Clips {
				if clip.StreamName == internalName {
					_ = cli.DeleteClip(c.Request.Context(), clip.ClipHash, tenantID)
				}
			}
		}
	}

	// Delete the stream itself
	_, err = tx.Exec(`DELETE FROM commodore.streams WHERE id = $1`, streamID)
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
		SELECT id, email, created_at, is_active 
		FROM commodore.users ORDER BY created_at DESC
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
        FROM commodore.streams ORDER BY created_at DESC
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
		stream.IsRecording = stream.IsRecordingEnabled

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
		UPDATE commodore.streams SET status = 'terminated', end_time = NOW() WHERE internal_name = $1
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
		FROM commodore.streams 
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
	err = db.QueryRow(`SELECT 'sk_' || commodore.generate_random_string(28)`).Scan(&newStreamKey)
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
		UPDATE commodore.streams 
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
		UPDATE commodore.stream_keys 
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
		INSERT INTO commodore.stream_keys (stream_id, key_value, key_name, is_active)
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
	var eventData commodoreapi.StreamEventRequest

	if err := c.ShouldBindJSON(&eventData); err != nil {
		c.JSON(http.StatusBadRequest, commodoreapi.ErrorResponse{Error: "Invalid payload"})
		return
	}

	// Update stream status based on whether we have stream_id or internal_name
	if eventData.StreamID != "" {
		_, err := db.Exec(`
			UPDATE commodore.streams 
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
			UPDATE commodore.streams 
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
			UPDATE commodore.stream_keys SET last_used_at = NOW()
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
	var req commodoreapi.StreamStatusRequest

	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, commodoreapi.ErrorResponse{Error: "Invalid payload"})
		return
	}

	// Validate required fields
	if req.InternalName == "" || req.NodeID == "" {
		c.JSON(http.StatusBadRequest, commodoreapi.ErrorResponse{Error: "Missing required fields"})
		return
	}

	// Note: Stream status is now tracked in Data Plane (Periscope)
	// Control Plane only manages configuration, not live status
	logger.WithFields(logging.Fields{
		"internal_name": req.InternalName,
		"status":        req.Status,
		"node_id":       req.NodeID,
		"buffer_state":  req.BufferState,
	}).Info("Stream lifecycle event (status now tracked in Data Plane)")

	c.JSON(http.StatusOK, commodoreapi.SuccessResponse{Success: true, Message: "Event logged"})
}

// HandleRecordingStatus handles recording status updates from Helmsman
func HandleRecordingStatus(c *gin.Context) {
	var eventData commodoreapi.RecordingStatusRequest

	if err := c.ShouldBindJSON(&eventData); err != nil {
		c.JSON(http.StatusBadRequest, commodoreapi.ErrorResponse{Error: "Invalid payload"})
		return
	}

	if eventData.InternalName == "" {
		c.JSON(http.StatusBadRequest, commodoreapi.ErrorResponse{Error: "internal_name required"})
		return
	}

	// Prefer explicit tenant header if present; otherwise update by unique internal_name
	tenantID := c.GetHeader("X-Tenant-ID")
	var err error
	if tenantID != "" {
		_, err = db.Exec(`
            UPDATE commodore.streams SET is_recording_enabled = $1, updated_at = NOW()
            WHERE tenant_id = $2 AND internal_name = $3
        `, eventData.IsRecording, tenantID, eventData.InternalName)
	} else {
		_, err = db.Exec(`
            UPDATE commodore.streams SET is_recording_enabled = $1, updated_at = NOW()
            WHERE internal_name = $2
        `, eventData.IsRecording, eventData.InternalName)
	}
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
		SELECT internal_name, tenant_id, status FROM commodore.streams WHERE LOWER(playback_id) = LOWER($1)
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
	var req commodoreapi.PushStatusRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, commodoreapi.ErrorResponse{Error: "Invalid payload"})
		return
	}

	// Validate required fields
	if req.InternalName == "" || req.NodeID == "" {
		c.JSON(http.StatusBadRequest, commodoreapi.ErrorResponse{Error: "Missing required fields"})
		return
	}

	logger.WithFields(logging.Fields{
		"node_id":       req.NodeID,
		"internal_name": req.InternalName,
		"push_target":   req.PushTarget,
	}).Info("Push status event")

	// Log push status event processed
	logger.WithFields(logging.Fields{
		"internal_name": req.InternalName,
		"push_target":   req.PushTarget,
		"push_id":       req.PushID,
	}).Info("Push status event processed")

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
	err := db.QueryRow(`SELECT 'fwk_' || commodore.generate_random_string(40)`).Scan(&tokenValue)
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
		INSERT INTO commodore.api_tokens (id, tenant_id, user_id, token_value, token_name, permissions, is_active, expires_at, created_at)
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
		SELECT id, token_name, permissions, is_active, last_used_at, expires_at, created_at
		FROM commodore.api_tokens 
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
		SELECT token_name FROM commodore.api_tokens 
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
		UPDATE commodore.api_tokens 
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
		// Fallback to path param style /verify/:token
		token = c.Param("token")
	}
	if token == "" {
		c.JSON(http.StatusBadRequest, commodoreapi.ErrorResponse{Error: "Verification token required"})
		return
	}

	// Find user by verification token and check expiry
	var userID, tenantID, email string
	var tokenExpiry time.Time
	err := db.QueryRow(`
		SELECT id, tenant_id, email, token_expires_at 
		FROM commodore.users 
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
		UPDATE commodore.users 
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

// ForgotPassword initiates a password reset by sending a signed reset link to the user's email
func ForgotPassword(c *gin.Context) {
	type reqBody struct {
		Email string `json:"email"`
	}
	var req reqBody
	if err := c.ShouldBindJSON(&req); err != nil || req.Email == "" {
		c.JSON(http.StatusOK, gin.H{"message": "If that email exists, a reset link has been sent."})
		return
	}

	// Find user
	var userID, email string
	err := db.QueryRow(`SELECT id, email FROM commodore.users WHERE LOWER(email)=LOWER($1) AND is_active=true`, req.Email).Scan(&userID, &email)
	if err == sql.ErrNoRows || err != nil {
		// Do not leak existence
		c.JSON(http.StatusOK, gin.H{"message": "If that email exists, a reset link has been sent."})
		return
	}

	// Create signed reset token (JWT) with short expiry
	resetToken, err := generatePasswordResetToken(userID, email)
	if err != nil {
		logger.WithError(err).Warn("Failed to generate reset token")
		c.JSON(http.StatusOK, gin.H{"message": "If that email exists, a reset link has been sent."})
		return
	}

	// Send email (best effort)
	if err := sendPasswordResetEmail(email, resetToken, "FrameWorks"); err != nil {
		logger.WithError(err).Warn("Failed to send reset email")
	}

	c.JSON(http.StatusOK, gin.H{"message": "If that email exists, a reset link has been sent."})
}

// ResetPassword validates the reset token and updates the user's password
func ResetPassword(c *gin.Context) {
	type reqBody struct {
		Token    string `json:"token"`
		Password string `json:"password"`
	}
	var req reqBody
	if err := c.ShouldBindJSON(&req); err != nil || req.Token == "" || len(req.Password) < 6 {
		c.JSON(http.StatusBadRequest, commodoreapi.ErrorResponse{Error: "Invalid request"})
		return
	}

	userID, email, err := validatePasswordResetToken(req.Token)
	if err != nil {
		c.JSON(http.StatusBadRequest, commodoreapi.ErrorResponse{Error: "Invalid or expired token"})
		return
	}

	// Ensure user still exists and is active
	var exists bool
	if err := db.QueryRow(`SELECT EXISTS(SELECT 1 FROM commodore.users WHERE id=$1 AND email=$2 AND is_active=true)`, userID, email).Scan(&exists); err != nil || !exists {
		c.JSON(http.StatusBadRequest, commodoreapi.ErrorResponse{Error: "Invalid token"})
		return
	}

	// Update password hash
	hash, err := auth.HashPassword(req.Password)
	if err != nil {
		c.JSON(http.StatusInternalServerError, commodoreapi.ErrorResponse{Error: "Failed to update password"})
		return
	}
	if _, err := db.Exec(`UPDATE commodore.users SET password_hash=$1, updated_at=NOW() WHERE id=$2`, hash, userID); err != nil {
		logger.WithError(err).Error("Failed to update user password")
		c.JSON(http.StatusInternalServerError, commodoreapi.ErrorResponse{Error: "Failed to update password"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "Password has been reset. You can now log in."})
}

// Logout is a stateless no-op for JWT sessions, provided for API parity
func Logout(c *gin.Context) {
	c.Status(http.StatusNoContent)
}

// generatePasswordResetToken creates a short-lived signed token for password reset
func generatePasswordResetToken(userID, email string) (string, error) {
	secret := []byte(config.RequireEnv("JWT_SECRET"))
	claims := jwt.RegisteredClaims{
		Subject:   userID,
		ExpiresAt: jwt.NewNumericDate(time.Now().Add(1 * time.Hour)),
		IssuedAt:  jwt.NewNumericDate(time.Now()),
		Audience:  []string{"password_reset"},
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	token.Header["typ"] = "JWT"
	// Include email in the token via header to avoid custom claims struct
	token.Header["eml"] = email
	return token.SignedString(secret)
}

// validatePasswordResetToken validates the reset token and returns userID and email
func validatePasswordResetToken(tokenString string) (string, string, error) {
	secret := []byte(config.RequireEnv("JWT_SECRET"))
	parsed, err := jwt.ParseWithClaims(tokenString, &jwt.RegisteredClaims{}, func(token *jwt.Token) (interface{}, error) { return secret, nil })
	if err != nil || !parsed.Valid {
		return "", "", fmt.Errorf("invalid token")
	}
	claims, ok := parsed.Claims.(*jwt.RegisteredClaims)
	if !ok {
		return "", "", fmt.Errorf("invalid claims")
	}
	if claims.ExpiresAt == nil || time.Now().After(claims.ExpiresAt.Time) {
		return "", "", fmt.Errorf("expired")
	}
	userID := claims.Subject
	email, _ := parsed.Header["eml"].(string)
	return userID, email, nil
}

// sendPasswordResetEmail sends a password reset email
func sendPasswordResetEmail(email, token, tenantName string) error {
	smtpHost := os.Getenv("SMTP_HOST")
	smtpPort := os.Getenv("SMTP_PORT")
	smtpUser := os.Getenv("SMTP_USER")
	smtpPass := os.Getenv("SMTP_PASS")
	fromEmail := os.Getenv("FROM_EMAIL")
	if smtpHost == "" {
		return fmt.Errorf("SMTP not configured")
	}
	if smtpPort == "" {
		smtpPort = "587"
	}
	if fromEmail == "" {
		fromEmail = "noreply@frameworks.network"
	}

	baseURL := os.Getenv("BASE_URL")
	if baseURL == "" {
		baseURL = "http://localhost:3000"
	}
	resetURL := fmt.Sprintf("%s/reset-password?token=%s", baseURL, url.QueryEscape(token))

	subject := fmt.Sprintf("Reset your %s password", tenantName)
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
func createUserStreamInTenant(tenantID, userID, title string) (*commodoreapi.CreateStreamResult, error) {
	// Generate unique identifiers
	streamID := uuid.New().String()
	streamKey := "sk_" + generateRandomString(28)
	playbackID := generateRandomString(16)
	internalName := streamID

	// Insert the stream with tenant context
	_, err := db.Exec(`
		INSERT INTO commodore.streams (id, tenant_id, user_id, stream_key, playback_id, internal_name, title)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
	`, streamID, tenantID, userID, streamKey, playbackID, internalName, title)

	if err != nil {
		return nil, err
	}

	// Also create an entry in stream_keys for backward compatibility
	_, err = db.Exec(`
		INSERT INTO commodore.stream_keys (tenant_id, stream_id, key_value, key_name, is_active)
		VALUES ($1, $2, $3, 'Primary Key', TRUE)
	`, tenantID, streamID, streamKey)

	if err != nil {
		return nil, err
	}

	return &commodoreapi.CreateStreamResult{
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
	if os.Getenv("TURNSTILE_AUTH_SECRET_KEY") != "" {
		return nil
	}

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
	if req.Behavior.FormShownAt == 0 || req.Behavior.SubmittedAt == 0 {
		errors = append(errors, "Missing behavioral data")
	} else {
		// Check timing
		timeSpent := req.Behavior.SubmittedAt - req.Behavior.FormShownAt

		// Too fast (less than 3 seconds)
		if timeSpent < 3000 {
			errors = append(errors, "Form submitted too quickly")
		}

		// Too slow (more than 30 minutes)
		if timeSpent > 30*60*1000 {
			errors = append(errors, "Form session expired")
		}

		// Check for human interactions
		if !req.Behavior.Mouse && !req.Behavior.Typed {
			errors = append(errors, "No human interaction detected")
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

func verifyTurnstileToken(token, clientIP string) error {
	secret := os.Getenv("TURNSTILE_AUTH_SECRET_KEY")
	if secret == "" {
		return nil
	}

	if token == "" {
		return fmt.Errorf("missing turnstile token")
	}

	data := url.Values{}
	data.Set("secret", secret)
	data.Set("response", token)
	if clientIP != "" {
		data.Set("remoteip", clientIP)
	}

	req, err := http.NewRequest(http.MethodPost, "https://challenges.cloudflare.com/turnstile/v0/siteverify", strings.NewReader(data.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	var result struct {
		Success    bool     `json:"success"`
		ErrorCodes []string `json:"error-codes"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return err
	}

	if !result.Success {
		return fmt.Errorf("%v", result.ErrorCodes)
	}

	return nil
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
	features := extractFeaturesFromTier(*tierInfo.Tier, nil)                     // CustomFeatures handled in function
	limits := extractLimitsFromTier(*tierInfo.Tier, nil, tierInfo.ClusterAccess) // CustomAllocations handled in function

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
		IsRecordingEnabled:  tier.Features.Recording,
		IsAnalyticsEnabled:  tier.Features.Analytics,
		IsAPIEnabled:        tier.Features.APIAccess,
		IsWhiteLabelEnabled: tier.Features.CustomBranding,
	}

	// Override with custom features if provided
	if customFeatures != nil {
		features.IsRecordingEnabled = getBoolFromJSONB(customFeatures, "recording_enabled", features.IsRecordingEnabled)
		features.IsAnalyticsEnabled = getBoolFromJSONB(customFeatures, "analytics_enabled", features.IsAnalyticsEnabled)
		features.IsAPIEnabled = getBoolFromJSONB(customFeatures, "api_enabled", features.IsAPIEnabled)
		features.IsWhiteLabelEnabled = getBoolFromJSONB(customFeatures, "white_label_enabled", features.IsWhiteLabelEnabled)
	}

	return features
}

// extractLimitsFromTier converts tier allocations to TenantLimits
func extractLimitsFromTier(tier models.BillingTier, customAllocations models.JSONB, clusterAccess []models.TenantClusterAccess) models.TenantLimits {
	limits := models.TenantLimits{
		MaxStreams:     getAllocationInt(tier.ComputeAllocation, 5),
		MaxStorageGB:   getAllocationInt(tier.StorageAllocation, 10),
		MaxBandwidthGB: getAllocationInt(tier.BandwidthAllocation, 100),
		MaxUsers:       5, // Default user limit
	}

	// Override with custom allocations if provided
	if customAllocations != nil {
		limits.MaxStreams = getIntFromJSONB(customAllocations, "max_streams", limits.MaxStreams)
		limits.MaxStorageGB = getIntFromJSONB(customAllocations, "max_storage_gb", limits.MaxStorageGB)
		limits.MaxBandwidthGB = getIntFromJSONB(customAllocations, "max_bandwidth_gb", limits.MaxBandwidthGB)
		limits.MaxUsers = getIntFromJSONB(customAllocations, "max_users", limits.MaxUsers)
	}

	return limits
}

// Helper functions for extracting values from AllocationDetails
func getAllocationInt(allocation models.AllocationDetails, defaultVal int) int {
	if allocation.Limit.IsUnlimited {
		return -1 // Use -1 to represent unlimited
	}

	if allocation.Limit.GetInt() == 0 {
		return defaultVal
	}

	return allocation.Limit.GetInt()
}

// Helper functions for type-safe JSONB access
func getBoolFromJSONB(jsonb models.JSONB, key string, defaultVal bool) bool {
	if jsonb == nil {
		return defaultVal
	}
	if val, exists := jsonb[key]; exists {
		if b, ok := val.(bool); ok {
			return b
		}
	}
	return defaultVal
}

func getIntFromJSONB(jsonb models.JSONB, key string, defaultVal int) int {
	if jsonb == nil {
		return defaultVal
	}
	if val, exists := jsonb[key]; exists {
		if f, ok := val.(float64); ok {
			return int(f)
		}
		if i, ok := val.(int); ok {
			return i
		}
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

	var tenantID, userID string
	var recordingConfigJSON []byte
	err := db.QueryRow(`SELECT tenant_id, user_id, recording_config FROM commodore.streams WHERE internal_name = $1`, internalName).Scan(&tenantID, &userID, &recordingConfigJSON)
	if err == sql.ErrNoRows {
		c.JSON(http.StatusNotFound, commodoreapi.ErrorResponse{Error: "Not found"})
		return
	}
	if err != nil {
		logger.WithError(err).Error("Failed to resolve internal_name")
		c.JSON(http.StatusInternalServerError, commodoreapi.ErrorResponse{Error: "Database error"})
		return
	}

	// Parse recording config if present
	var recordingConfig *commodoreapi.RecordingConfig
	if len(recordingConfigJSON) > 0 {
		var config commodoreapi.RecordingConfig
		if err := json.Unmarshal(recordingConfigJSON, &config); err == nil {
			recordingConfig = &config
		}
	}

	c.JSON(http.StatusOK, commodoreapi.InternalNameResponse{
		InternalName: internalName,
		TenantID:     tenantID,
		UserID:       userID,
		Recording:    recordingConfig,
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

// ============================================================================
// STREAM KEYS MANAGEMENT
// ============================================================================

// GetStreamKeys returns all keys for a specific stream (tenant-scoped)
func GetStreamKeys(c *gin.Context) {
	defer func() {
		if metrics != nil {
			metrics.StreamOperations.WithLabelValues("get_stream_keys", "completed").Inc()
		}
	}()

	tenantID := c.GetString("tenant_id")
	userID := c.GetString("user_id")
	if tenantID == "" || userID == "" {
		c.JSON(http.StatusUnauthorized, commodoreapi.ErrorResponse{Error: "Authentication required"})
		return
	}

	streamID := c.Param("id")
	if streamID == "" {
		c.JSON(http.StatusBadRequest, commodoreapi.ErrorResponse{Error: "Stream ID required"})
		return
	}

	// Verify stream ownership
	var streamExists bool
	err := db.QueryRow(`
		SELECT EXISTS(SELECT 1 FROM commodore.streams WHERE id = $1 AND tenant_id = $2 AND user_id = $3)
	`, streamID, tenantID, userID).Scan(&streamExists)
	if err != nil {
		logger.WithError(err).Error("Failed to verify stream ownership")
		c.JSON(http.StatusInternalServerError, commodoreapi.ErrorResponse{Error: "Failed to verify stream ownership"})
		return
	}

	if !streamExists {
		c.JSON(http.StatusNotFound, commodoreapi.ErrorResponse{Error: "Stream not found"})
		return
	}

	// Get all keys for the stream
	rows, err := db.Query(`
		SELECT id, tenant_id, user_id, stream_id, key_value, key_name, is_active, last_used_at, created_at, updated_at
		FROM commodore.stream_keys
		WHERE tenant_id = $1 AND stream_id = $2
		ORDER BY created_at DESC
	`, tenantID, streamID)
	if err != nil {
		logger.WithError(err).Error("Failed to fetch stream keys")
		c.JSON(http.StatusInternalServerError, commodoreapi.ErrorResponse{Error: "Failed to fetch stream keys"})
		return
	}
	defer rows.Close()

	var keys []commodoreapi.StreamKey
	for rows.Next() {
		var key commodoreapi.StreamKey
		var lastUsedAt sql.NullTime

		err := rows.Scan(
			&key.ID,
			&key.TenantID,
			&key.UserID,
			&key.StreamID,
			&key.KeyValue,
			&key.KeyName,
			&key.IsActive,
			&lastUsedAt,
			&key.CreatedAt,
			&key.UpdatedAt,
		)
		if err != nil {
			logger.WithError(err).Error("Failed to scan stream key")
			continue
		}

		if lastUsedAt.Valid {
			key.LastUsedAt = &lastUsedAt.Time
		}

		keys = append(keys, key)
	}

	c.JSON(http.StatusOK, commodoreapi.StreamKeysResponse{
		StreamKeys: keys,
		Count:      len(keys),
	})
}

// CreateStreamKey creates a new key for a stream
func CreateStreamKey(c *gin.Context) {
	defer func() {
		if metrics != nil {
			metrics.StreamOperations.WithLabelValues("create_stream_key", "completed").Inc()
		}
	}()

	tenantID := c.GetString("tenant_id")
	userID := c.GetString("user_id")
	if tenantID == "" || userID == "" {
		c.JSON(http.StatusUnauthorized, commodoreapi.ErrorResponse{Error: "Authentication required"})
		return
	}

	streamID := c.Param("id")
	if streamID == "" {
		c.JSON(http.StatusBadRequest, commodoreapi.ErrorResponse{Error: "Stream ID required"})
		return
	}

	var req commodoreapi.CreateStreamKeyRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, commodoreapi.ErrorResponse{Error: "Invalid request format"})
		return
	}

	// Verify stream ownership
	var streamExists bool
	err := db.QueryRow(`
		SELECT EXISTS(SELECT 1 FROM commodore.streams WHERE id = $1 AND tenant_id = $2 AND user_id = $3)
	`, streamID, tenantID, userID).Scan(&streamExists)
	if err != nil {
		logger.WithError(err).Error("Failed to verify stream ownership")
		c.JSON(http.StatusInternalServerError, commodoreapi.ErrorResponse{Error: "Failed to verify stream ownership"})
		return
	}

	if !streamExists {
		c.JSON(http.StatusNotFound, commodoreapi.ErrorResponse{Error: "Stream not found"})
		return
	}

	// Generate new key
	keyBytes := make([]byte, 16)
	if _, err := rand.Read(keyBytes); err != nil {
		logger.WithError(err).Error("Failed to generate stream key")
		c.JSON(http.StatusInternalServerError, commodoreapi.ErrorResponse{Error: "Failed to generate stream key"})
		return
	}
	keyValue := "sk_" + hex.EncodeToString(keyBytes)

	// Insert new key
	keyID := uuid.New().String()
	keyName := req.KeyName
	if keyName == "" {
		keyName = "Key " + time.Now().Format("2006-01-02 15:04")
	}

	_, err = db.Exec(`
		INSERT INTO commodore.stream_keys (id, tenant_id, stream_id, key_value, key_name, is_active, created_at)
		VALUES ($1, $2, $3, $4, $5, TRUE, NOW())
	`, keyID, tenantID, streamID, keyValue, keyName)
	if err != nil {
		logger.WithError(err).Error("Failed to create stream key")
		c.JSON(http.StatusInternalServerError, commodoreapi.ErrorResponse{Error: "Failed to create stream key"})
		return
	}

	key := commodoreapi.StreamKey{
		ID:        keyID,
		KeyValue:  keyValue,
		KeyName:   keyName,
		IsActive:  true,
		CreatedAt: time.Now(),
	}

	c.JSON(http.StatusCreated, commodoreapi.StreamKeyResponse{
		StreamKey: key,
		Message:   "Stream key created successfully",
	})
}

// DeactivateStreamKey deactivates a stream key
func DeactivateStreamKey(c *gin.Context) {
	tenantID := c.GetString("tenant_id")
	userID := c.GetString("user_id")
	if tenantID == "" || userID == "" {
		c.JSON(http.StatusUnauthorized, commodoreapi.ErrorResponse{Error: "Authentication required"})
		return
	}

	streamID := c.Param("id")
	keyID := c.Param("key_id")
	if streamID == "" || keyID == "" {
		c.JSON(http.StatusBadRequest, commodoreapi.ErrorResponse{Error: "Stream ID and Key ID required"})
		return
	}

	// Verify ownership and deactivate key
	result, err := db.Exec(`
		UPDATE commodore.stream_keys SET is_active = FALSE
		WHERE id = $1 AND stream_id = $2 AND tenant_id = $3 
		AND EXISTS(SELECT 1 FROM commodore.streams WHERE id = $4 AND tenant_id = $5 AND user_id = $6)
	`, keyID, streamID, tenantID, streamID, tenantID, userID)
	if err != nil {
		logger.WithError(err).Error("Failed to deactivate stream key")
		c.JSON(http.StatusInternalServerError, commodoreapi.ErrorResponse{Error: "Failed to deactivate stream key"})
		return
	}

	rowsAffected, _ := result.RowsAffected()
	if rowsAffected == 0 {
		c.JSON(http.StatusNotFound, commodoreapi.ErrorResponse{Error: "Stream key not found"})
		return
	}

	c.JSON(http.StatusOK, commodoreapi.SuccessResponse{Success: true})
}

// ============================================================================
// RECORDINGS MANAGEMENT
// ============================================================================

// GetRecordings returns all recordings for a tenant, optionally filtered by stream
func GetRecordings(c *gin.Context) {
	_ = time.Now()
	defer func() {
		if metrics != nil {
			metrics.StreamOperations.WithLabelValues("get_recordings", "completed").Inc()
		}
	}()

	tenantID := c.GetString("tenant_id")
	userID := c.GetString("user_id")
	if tenantID == "" || userID == "" {
		c.JSON(http.StatusUnauthorized, commodoreapi.ErrorResponse{Error: "Authentication required"})
		return
	}

	streamID := c.Query("stream_id")

	var query string
	var args []interface{}

	if streamID != "" {
		// Get recordings for specific stream
		query = `
			SELECT r.id, r.stream_id, r.title, r.duration, 
				   r.file_size_bytes, r.file_path, r.thumbnail_url, 
				   r.status, r.created_at, r.updated_at
			FROM commodore.recordings r
			JOIN commodore.streams s ON r.stream_id = s.id
			WHERE r.tenant_id = $1 AND s.user_id = $2 AND r.stream_id = $3
			ORDER BY r.created_at DESC
		`
		args = []interface{}{tenantID, userID, streamID}
	} else {
		// Get all recordings for user
		query = `
			SELECT r.id, r.stream_id, r.title, r.duration, 
				   r.file_size_bytes, r.file_path, r.thumbnail_url, 
				   r.status, r.created_at, r.updated_at
			FROM commodore.recordings r
			JOIN commodore.streams s ON r.stream_id = s.id
			WHERE r.tenant_id = $1 AND s.user_id = $2
			ORDER BY r.created_at DESC
		`
		args = []interface{}{tenantID, userID}
	}

	rows, err := db.Query(query, args...)
	if err != nil {
		logger.WithError(err).Error("Failed to fetch recordings")
		c.JSON(http.StatusInternalServerError, commodoreapi.ErrorResponse{Error: "Failed to fetch recordings"})
		return
	}
	defer rows.Close()

	var recordings []commodoreapi.Recording
	for rows.Next() {
		var recording commodoreapi.Recording
		var fileSizeBytes sql.NullInt64
		var filePath, thumbnailURL sql.NullString
		var duration sql.NullInt32

		err := rows.Scan(
			&recording.ID,
			&recording.StreamID,
			&recording.Filename,
			&duration,
			&fileSizeBytes,
			&filePath,
			&thumbnailURL,
			&recording.Status,
			&recording.CreatedAt,
			&recording.UpdatedAt,
		)
		if err != nil {
			logger.WithError(err).Error("Failed to scan recording")
			continue
		}

		if duration.Valid {
			durationInt := int(duration.Int32)
			recording.Duration = &durationInt
		}
		if fileSizeBytes.Valid {
			recording.FileSize = &fileSizeBytes.Int64
		}
		if filePath.Valid {
			recording.PlaybackID = &filePath.String
		}
		if thumbnailURL.Valid {
			recording.ThumbnailURL = &thumbnailURL.String
		}

		recordings = append(recordings, recording)
	}

	c.JSON(http.StatusOK, commodoreapi.RecordingsResponse{
		Recordings: recordings,
		Count:      len(recordings),
	})
}

// ResolveViewerEndpoint resolves viewer endpoints through Foghorn
func ResolveViewerEndpoint(c *gin.Context) {
	var req commodoreapi.ViewerEndpointRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, commodoreapi.ErrorResponse{Error: "Invalid request payload"})
		return
	}

	// Validate required fields
	if req.ContentType == "" || req.ContentID == "" {
		c.JSON(http.StatusBadRequest, commodoreapi.ErrorResponse{Error: "content_type and content_id are required"})
		return
	}

	// Get viewer IP from request if not provided
	if req.ViewerIP == "" {
		req.ViewerIP = c.ClientIP()
	}

	// Get Foghorn URLs from environment (following existing pattern)
	foghornList := os.Getenv("FOGHORN_URLS")
	if foghornList == "" {
		logger.Error("FOGHORN_URLS environment variable not set")
		c.JSON(http.StatusInternalServerError, commodoreapi.ErrorResponse{Error: "Service configuration error"})
		return
	}

	serviceToken := config.RequireEnv("SERVICE_TOKEN")
	if serviceToken == "" {
		logger.Error("SERVICE_TOKEN environment variable not set")
		c.JSON(http.StatusInternalServerError, commodoreapi.ErrorResponse{Error: "Service configuration error"})
		return
	}

	urls := strings.Split(foghornList, ",")
	var lastErr error

	// Try each Foghorn instance (following existing pattern from CreateClip)
	for _, rawURL := range urls {
		rawURL = strings.TrimSpace(rawURL)
		if rawURL == "" {
			continue
		}

		base, err := url.Parse(rawURL)
		if err != nil {
			logger.WithError(err).WithField("url", rawURL).Error("Invalid Foghorn URL")
			lastErr = err
			continue
		}

		// Create Foghorn client for this instance
		cli := foghorn.NewClient(foghorn.Config{
			BaseURL:      base.String(),
			ServiceToken: serviceToken,
			Timeout:      30 * time.Second,
			Logger:       logger,
		})

		// Convert to Foghorn request format
		foghornReq := &fapi.ViewerEndpointRequest{
			ContentType: req.ContentType,
			ContentID:   req.ContentID,
			ViewerIP:    req.ViewerIP,
		}

		// Make the request
		res, err := cli.ResolveViewerEndpoint(c.Request.Context(), foghornReq)
		if err != nil {
			logger.WithError(err).WithField("foghorn_url", base.String()).Error("Foghorn viewer endpoint resolution failed")
			lastErr = err
			continue
		}

		// Return the response directly (it's already the correct type via type alias)
		c.JSON(http.StatusOK, res)
		return
	}

	// All Foghorn instances failed
	logger.WithError(lastErr).Error("All Foghorn instances failed for viewer endpoint resolution")
	c.JSON(http.StatusServiceUnavailable, commodoreapi.ErrorResponse{Error: "Service temporarily unavailable"})
}

// StreamMetaByKey resolves a stream_key to internal_name and proxies meta request to Foghorn
func StreamMetaByKey(c *gin.Context) {
	streamKey := c.Param("stream_key")
	if streamKey == "" {
		c.JSON(http.StatusBadRequest, commodoreapi.ErrorResponse{Error: "stream_key is required"})
		return
	}

	// Resolve tenant and stream info
	var tenantID, internalName string
	err := db.QueryRow(`
        SELECT s.tenant_id, s.internal_name
        FROM commodore.streams s
        JOIN commodore.stream_keys k ON k.stream_id = s.id AND k.is_active = true
        WHERE LOWER(k.key_value) = LOWER($1)
    `, streamKey).Scan(&tenantID, &internalName)
	if err == sql.ErrNoRows {
		c.JSON(http.StatusNotFound, commodoreapi.ErrorResponse{Error: "stream key not found"})
		return
	}
	if err != nil {
		logger.WithError(err).Error("Failed to resolve stream key")
		c.JSON(http.StatusInternalServerError, commodoreapi.ErrorResponse{Error: "database error"})
		return
	}

	// Determine Foghorn URL (cluster aware): reuse existing env fan-out pattern
	foghornList := os.Getenv("FOGHORN_URLS")
	if foghornList == "" {
		foghornList = os.Getenv("FOGHORN_URL")
	}
	if foghornList == "" {
		c.JSON(http.StatusServiceUnavailable, commodoreapi.ErrorResponse{Error: "FOGHORN_URLS not configured"})
		return
	}
	urls := strings.Split(foghornList, ",")

	serviceToken := config.RequireEnv("SERVICE_TOKEN")

	// Optional target params and includeRaw flag
	targetBaseURL := c.Query("target_base_url")
	targetNodeID := c.Query("target_node_id")
	includeRaw := false
	if v := c.Query("include_raw"); v != "" {
		if v == "1" || strings.ToLower(v) == "true" || strings.ToLower(v) == "yes" {
			includeRaw = true
		}
	}

	// Try each Foghorn until success
	var lastErr error
	for _, raw := range urls {
		base := strings.TrimSpace(raw)
		if base == "" {
			continue
		}
		if !strings.HasPrefix(base, "http://") && !strings.HasPrefix(base, "https://") {
			base = "http://" + base
		}
		cli := foghorn.NewClient(foghorn.Config{BaseURL: base, ServiceToken: serviceToken, Timeout: 8 * time.Second, Logger: logger})
		ctx, cancel := context.WithTimeout(c.Request.Context(), 6*time.Second)
		defer cancel()
		res, err := cli.GetStreamMeta(ctx, internalName, includeRaw, targetBaseURL, targetNodeID)
		if err == nil {
			c.JSON(http.StatusOK, res)
			return
		}
		lastErr = err
	}

	c.JSON(http.StatusBadGateway, commodoreapi.ErrorResponse{Error: fmt.Sprintf("all foghorns failed: %v", lastErr)})
}
