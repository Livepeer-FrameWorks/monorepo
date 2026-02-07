package handlers

import (
	"encoding/json"
	"net/http"
	"os"
	"strings"

	"frameworks/api_gateway/internal/attribution"
	gatewayerrors "frameworks/api_gateway/internal/errors"
	"frameworks/pkg/clients/commodore"
	"frameworks/pkg/logging"
	pb "frameworks/pkg/proto"

	"github.com/gin-gonic/gin"
)

const (
	refreshTokenCookie = "refresh_token"
	accessTokenCookie  = "access_token"
	tenantIDCookie     = "tenant_id"
	refreshTokenMaxAge = 30 * 24 * 60 * 60 // 30 days in seconds
	accessTokenMaxAge  = 15 * 60           // 15 minutes in seconds (matches JWT expiry)
)

var (
	loginAllowedErrors         = []string{"not verified", "deactivated"}
	walletLoginAllowedErrors   = []string{"signature", "expired"}
	registerAllowedErrors      = []string{"already exists", "user limit", "bot verification"}
	verifyEmailAllowedErrors   = []string{"invalid or expired", "already verified"}
	resetPasswordAllowedErrors = []string{"invalid or expired", "password too weak"}
)

// behaviorJSON represents client-side behavioral signals sent as JSON
type behaviorJSON struct {
	FormShownAt int64 `json:"formShownAt"`
	SubmittedAt int64 `json:"submittedAt"`
	Mouse       bool  `json:"mouse"`
	Typed       bool  `json:"typed"`
}

// parseBehavior converts JSON behavior string to proto BehaviorData
func parseBehavior(behaviorStr string) *pb.BehaviorData {
	if behaviorStr == "" {
		return nil
	}
	var b behaviorJSON
	if err := json.Unmarshal([]byte(behaviorStr), &b); err != nil {
		return nil
	}
	return &pb.BehaviorData{
		FormShownAt: b.FormShownAt,
		SubmittedAt: b.SubmittedAt,
		Mouse:       b.Mouse,
		Typed:       b.Typed,
	}
}

// AuthHandlers handles authentication requests using gRPC client
type AuthHandlers struct {
	commodore    *commodore.GRPCClient
	logger       logging.Logger
	cookieDomain string
}

// NewAuthHandlers creates a new auth handlers instance.
// COOKIE_DOMAIN controls the Domain attribute on auth cookies.
// Leave empty for single-domain deployments (default).
// Set to ".example.com" for cross-subdomain cookie sharing (e.g. docs site).
func NewAuthHandlers(commodoreClient *commodore.GRPCClient, logger logging.Logger) *AuthHandlers {
	return &AuthHandlers{
		commodore:    commodoreClient,
		logger:       logger,
		cookieDomain: os.Getenv("COOKIE_DOMAIN"),
	}
}

// Login handles user login
func (h *AuthHandlers) Login() gin.HandlerFunc {
	return func(c *gin.Context) {
		var req struct {
			Email          string `json:"email" binding:"required,email"`
			Password       string `json:"password" binding:"required"`
			TurnstileToken string `json:"turnstile_token"`
			PhoneNumber    string `json:"phone_number"`
			HumanCheck     string `json:"human_check"`
			Behavior       string `json:"behavior"`
		}
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request: " + err.Error()})
			return
		}

		resp, err := h.commodore.Login(c.Request.Context(), &pb.LoginRequest{
			Email:          req.Email,
			Password:       req.Password,
			TurnstileToken: req.TurnstileToken,
			PhoneNumber:    req.PhoneNumber,
			HumanCheck:     req.HumanCheck,
			Behavior:       parseBehavior(req.Behavior),
		})
		if err != nil {
			h.logger.WithError(err).Debug("Login failed")
			errMsg := gatewayerrors.SanitizeGRPCError(err, "invalid credentials", loginAllowedErrors)
			c.JSON(http.StatusUnauthorized, gin.H{"error": errMsg})
			return
		}

		// Set all auth tokens as HttpOnly cookies
		// Check multiple env vars for dev mode detection
		isDev := os.Getenv("ENV") == "development" ||
			os.Getenv("BUILD_ENV") == "development" ||
			os.Getenv("GO_ENV") == "development"
		secure := !isDev
		sameSite := http.SameSiteLaxMode

		// Access token - short-lived, httpOnly
		if resp.Token != "" {
			c.SetSameSite(sameSite)
			c.SetCookie(accessTokenCookie, resp.Token, accessTokenMaxAge, "/", h.cookieDomain, secure, true)
		}

		// Refresh token - long-lived, httpOnly
		if resp.RefreshToken != "" {
			c.SetSameSite(sameSite)
			c.SetCookie(refreshTokenCookie, resp.RefreshToken, refreshTokenMaxAge, "/", h.cookieDomain, secure, true)
		}

		// Tenant ID - needed for multi-tenant isolation, httpOnly
		if resp.User != nil && resp.User.TenantId != "" {
			c.SetSameSite(sameSite)
			c.SetCookie(tenantIDCookie, resp.User.TenantId, accessTokenMaxAge, "/", h.cookieDomain, secure, true)
		}

		// Return user data (no tokens in body for security)
		c.JSON(http.StatusOK, gin.H{
			"user":       userToJSON(resp.User),
			"expires_at": resp.ExpiresAt.AsTime(),
		})
	}
}

// WalletLogin handles wallet-based authentication
func (h *AuthHandlers) WalletLogin() gin.HandlerFunc {
	return func(c *gin.Context) {
		var req struct {
			Address      string `json:"address" binding:"required"`
			Message      string `json:"message" binding:"required"`
			Signature    string `json:"signature" binding:"required"`
			UTMSource    string `json:"utm_source"`
			UTMMedium    string `json:"utm_medium"`
			UTMCampaign  string `json:"utm_campaign"`
			UTMContent   string `json:"utm_content"`
			UTMTerm      string `json:"utm_term"`
			ReferralCode string `json:"referral_code"`
			LandingPage  string `json:"landing_page"`
		}
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request: " + err.Error()})
			return
		}

		utmSource := req.UTMSource
		if utmSource == "" {
			utmSource = c.Query("utm_source")
		}
		utmMedium := req.UTMMedium
		if utmMedium == "" {
			utmMedium = c.Query("utm_medium")
		}
		utmCampaign := req.UTMCampaign
		if utmCampaign == "" {
			utmCampaign = c.Query("utm_campaign")
		}
		utmContent := req.UTMContent
		if utmContent == "" {
			utmContent = c.Query("utm_content")
		}
		utmTerm := req.UTMTerm
		if utmTerm == "" {
			utmTerm = c.Query("utm_term")
		}
		referralCode := req.ReferralCode
		if referralCode == "" {
			referralCode = c.Query("referral_code")
		}
		if referralCode == "" {
			referralCode = c.Query("ref")
		}

		resp, err := h.commodore.WalletLogin(c.Request.Context(), req.Address, req.Message, req.Signature, attribution.Enrich(c.Request, &pb.SignupAttribution{
			SignupChannel: "wallet",
			SignupMethod:  "wallet_ethereum",
			UtmSource:     utmSource,
			UtmMedium:     utmMedium,
			UtmCampaign:   utmCampaign,
			UtmContent:    utmContent,
			UtmTerm:       utmTerm,
			ReferralCode:  referralCode,
			LandingPage:   req.LandingPage,
		}))
		if err != nil {
			h.logger.WithError(err).Debug("Wallet login failed")
			errMsg := gatewayerrors.SanitizeGRPCError(err, "wallet authentication failed", walletLoginAllowedErrors)
			c.JSON(http.StatusUnauthorized, gin.H{"error": errMsg})
			return
		}

		// Set all auth tokens as HttpOnly cookies (same as Login)
		isDev := os.Getenv("ENV") == "development" ||
			os.Getenv("BUILD_ENV") == "development" ||
			os.Getenv("GO_ENV") == "development"
		secure := !isDev
		sameSite := http.SameSiteLaxMode

		// Access token - short-lived, httpOnly
		if resp.Token != "" {
			c.SetSameSite(sameSite)
			c.SetCookie(accessTokenCookie, resp.Token, accessTokenMaxAge, "/", h.cookieDomain, secure, true)
		}

		// Refresh token - long-lived, httpOnly
		if resp.RefreshToken != "" {
			c.SetSameSite(sameSite)
			c.SetCookie(refreshTokenCookie, resp.RefreshToken, refreshTokenMaxAge, "/", h.cookieDomain, secure, true)
		}

		// Tenant ID - needed for multi-tenant isolation, httpOnly
		if resp.User != nil && resp.User.TenantId != "" {
			c.SetSameSite(sameSite)
			c.SetCookie(tenantIDCookie, resp.User.TenantId, accessTokenMaxAge, "/", h.cookieDomain, secure, true)
		}

		// Return user data (no tokens in body for security)
		c.JSON(http.StatusOK, gin.H{
			"user":       userToJSON(resp.User),
			"expires_at": resp.ExpiresAt.AsTime(),
		})
	}
}

// Register handles user registration
func (h *AuthHandlers) Register() gin.HandlerFunc {
	return func(c *gin.Context) {
		var req struct {
			Email          string `json:"email" binding:"required,email"`
			Password       string `json:"password" binding:"required,min=8"`
			FirstName      string `json:"first_name"` // Optional - can be added later in settings
			LastName       string `json:"last_name"`  // Optional - can be added later in settings
			PhoneNumber    string `json:"phone_number"`
			TurnstileToken string `json:"turnstile_token"`
			HumanCheck     string `json:"human_check"`
			Behavior       string `json:"behavior"`
			UTMSource      string `json:"utm_source"`
			UTMMedium      string `json:"utm_medium"`
			UTMCampaign    string `json:"utm_campaign"`
			UTMContent     string `json:"utm_content"`
			UTMTerm        string `json:"utm_term"`
			ReferralCode   string `json:"referral_code"`
			LandingPage    string `json:"landing_page"`
		}
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request: " + err.Error()})
			return
		}

		utmSource := req.UTMSource
		if utmSource == "" {
			utmSource = c.Query("utm_source")
		}
		utmMedium := req.UTMMedium
		if utmMedium == "" {
			utmMedium = c.Query("utm_medium")
		}
		utmCampaign := req.UTMCampaign
		if utmCampaign == "" {
			utmCampaign = c.Query("utm_campaign")
		}
		utmContent := req.UTMContent
		if utmContent == "" {
			utmContent = c.Query("utm_content")
		}
		utmTerm := req.UTMTerm
		if utmTerm == "" {
			utmTerm = c.Query("utm_term")
		}
		referralCode := req.ReferralCode
		if referralCode == "" {
			referralCode = c.Query("referral_code")
		}
		if referralCode == "" {
			referralCode = c.Query("ref")
		}
		resp, err := h.commodore.Register(c.Request.Context(), &pb.RegisterRequest{
			Email:          req.Email,
			Password:       req.Password,
			FirstName:      req.FirstName,
			LastName:       req.LastName,
			PhoneNumber:    req.PhoneNumber,
			TurnstileToken: req.TurnstileToken,
			HumanCheck:     req.HumanCheck,
			Behavior:       parseBehavior(req.Behavior),
			Attribution: attribution.Enrich(c.Request, &pb.SignupAttribution{
				SignupChannel: "web",
				SignupMethod:  "email_password",
				UtmSource:     utmSource,
				UtmMedium:     utmMedium,
				UtmCampaign:   utmCampaign,
				UtmContent:    utmContent,
				UtmTerm:       utmTerm,
				ReferralCode:  referralCode,
				LandingPage:   req.LandingPage,
			}),
		})
		if err != nil {
			h.logger.WithError(err).Error("Registration failed")
			c.JSON(http.StatusInternalServerError, gin.H{"error": "registration failed"})
			return
		}

		if !resp.Success {
			message := gatewayerrors.SanitizeMessage(resp.Message, "registration failed", registerAllowedErrors)
			c.JSON(http.StatusBadRequest, gin.H{"error": message})
			return
		}

		c.JSON(http.StatusCreated, gin.H{
			"success": resp.Success,
			"message": resp.Message,
		})
	}
}

// Logout handles user logout
func (h *AuthHandlers) Logout() gin.HandlerFunc {
	return func(c *gin.Context) {
		// Extract token from Authorization header
		authHeader := c.GetHeader("Authorization")
		token := strings.TrimPrefix(authHeader, "Bearer ")

		resp, err := h.commodore.Logout(c.Request.Context(), token)
		if err != nil {
			h.logger.WithError(err).Error("Logout failed")
			c.JSON(http.StatusInternalServerError, gin.H{"error": "logout failed"})
			return
		}

		// Clear all auth cookies (must match domain to actually clear)
		c.SetCookie(accessTokenCookie, "", -1, "/", h.cookieDomain, false, true)
		c.SetCookie(refreshTokenCookie, "", -1, "/", h.cookieDomain, false, true)
		c.SetCookie(tenantIDCookie, "", -1, "/", h.cookieDomain, false, true)

		c.JSON(http.StatusOK, gin.H{
			"success": resp.Success,
			"message": resp.Message,
		})
	}
}

// RefreshToken handles token refresh
func (h *AuthHandlers) RefreshToken() gin.HandlerFunc {
	return func(c *gin.Context) {
		// Read refresh token from HttpOnly cookie
		refreshToken, err := c.Cookie(refreshTokenCookie)
		if err != nil || refreshToken == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "refresh token not found"})
			return
		}

		resp, err := h.commodore.RefreshToken(c.Request.Context(), refreshToken)
		if err != nil {
			h.logger.WithError(err).Debug("Token refresh failed")
			// Clear invalid cookie
			c.SetCookie(refreshTokenCookie, "", -1, "/", h.cookieDomain, false, true)
			c.SetCookie(accessTokenCookie, "", -1, "/", h.cookieDomain, false, true)
			c.SetCookie(tenantIDCookie, "", -1, "/", h.cookieDomain, false, true)
			c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid or expired refresh token"})
			return
		}

		// Set all auth tokens as HttpOnly cookies
		// Check multiple env vars for dev mode detection
		isDev := os.Getenv("ENV") == "development" ||
			os.Getenv("BUILD_ENV") == "development" ||
			os.Getenv("GO_ENV") == "development"
		secure := !isDev
		sameSite := http.SameSiteLaxMode

		// Access token - short-lived, httpOnly
		if resp.Token != "" {
			c.SetSameSite(sameSite)
			c.SetCookie(accessTokenCookie, resp.Token, accessTokenMaxAge, "/", h.cookieDomain, secure, true)
		}

		// Refresh token - token rotation for security
		if resp.RefreshToken != "" {
			c.SetSameSite(sameSite)
			c.SetCookie(refreshTokenCookie, resp.RefreshToken, refreshTokenMaxAge, "/", h.cookieDomain, secure, true)
		}

		// Tenant ID - refresh in case user switched tenants
		if resp.User != nil && resp.User.TenantId != "" {
			c.SetSameSite(sameSite)
			c.SetCookie(tenantIDCookie, resp.User.TenantId, accessTokenMaxAge, "/", h.cookieDomain, secure, true)
		}

		// Return user data (no tokens in body for security)
		c.JSON(http.StatusOK, gin.H{
			"user":       userToJSON(resp.User),
			"expires_at": resp.ExpiresAt.AsTime(),
		})
	}
}

// VerifyEmail handles email verification
func (h *AuthHandlers) VerifyEmail() gin.HandlerFunc {
	return func(c *gin.Context) {
		// Token can come from path param or query param
		token := c.Param("token")
		if token == "" {
			token = c.Query("token")
		}
		if token == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "verification token required"})
			return
		}

		resp, err := h.commodore.VerifyEmail(c.Request.Context(), token)
		if err != nil {
			h.logger.WithError(err).Error("Email verification failed")
			c.JSON(http.StatusInternalServerError, gin.H{"error": "verification failed"})
			return
		}

		if !resp.Success {
			message := gatewayerrors.SanitizeMessage(resp.Message, "verification failed", verifyEmailAllowedErrors)
			c.JSON(http.StatusBadRequest, gin.H{"error": message})
			return
		}

		c.JSON(http.StatusOK, gin.H{
			"success": resp.Success,
			"message": resp.Message,
		})
	}
}

// ResendVerification handles resending verification email
func (h *AuthHandlers) ResendVerification() gin.HandlerFunc {
	return func(c *gin.Context) {
		var req struct {
			Email          string `json:"email" binding:"required,email"`
			TurnstileToken string `json:"turnstile_token"`
		}
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request: " + err.Error()})
			return
		}

		resp, err := h.commodore.ResendVerification(c.Request.Context(), req.Email, req.TurnstileToken)
		if err != nil {
			h.logger.WithError(err).Error("Resend verification failed")
			// Still return generic success to not reveal if email exists
			c.JSON(http.StatusOK, gin.H{
				"success": true,
				"message": "if an account exists with that email and is unverified, a new verification link will be sent",
			})
			return
		}

		c.JSON(http.StatusOK, gin.H{
			"success": resp.Success,
			"message": resp.Message,
		})
	}
}

// ForgotPassword handles forgot password request
func (h *AuthHandlers) ForgotPassword() gin.HandlerFunc {
	return func(c *gin.Context) {
		var req struct {
			Email string `json:"email" binding:"required,email"`
		}
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request: " + err.Error()})
			return
		}

		resp, err := h.commodore.ForgotPassword(c.Request.Context(), req.Email)
		if err != nil {
			h.logger.WithError(err).Error("Forgot password failed")
			// Still return success to not reveal if email exists
			c.JSON(http.StatusOK, gin.H{
				"success": true,
				"message": "if an account exists with that email, a reset link will be sent",
			})
			return
		}

		c.JSON(http.StatusOK, gin.H{
			"success": resp.Success,
			"message": resp.Message,
		})
	}
}

// ResetPassword handles password reset
func (h *AuthHandlers) ResetPassword() gin.HandlerFunc {
	return func(c *gin.Context) {
		var req struct {
			Token    string `json:"token" binding:"required"`
			Password string `json:"password" binding:"required,min=8"`
		}
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request: " + err.Error()})
			return
		}

		resp, err := h.commodore.ResetPassword(c.Request.Context(), req.Token, req.Password)
		if err != nil {
			h.logger.WithError(err).Error("Password reset failed")
			c.JSON(http.StatusInternalServerError, gin.H{"error": "password reset failed"})
			return
		}

		if !resp.Success {
			message := gatewayerrors.SanitizeMessage(resp.Message, "password reset failed", resetPasswordAllowedErrors)
			c.JSON(http.StatusBadRequest, gin.H{"error": message})
			return
		}

		c.JSON(http.StatusOK, gin.H{
			"success": resp.Success,
			"message": resp.Message,
		})
	}
}

// GetMe handles get current user profile
func (h *AuthHandlers) GetMe() gin.HandlerFunc {
	return func(c *gin.Context) {
		user, err := h.commodore.GetMe(c.Request.Context())
		if err != nil {
			h.logger.WithError(err).Error("Get profile failed")
			c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
			return
		}

		c.JSON(http.StatusOK, userToJSON(user))
	}
}

// UpdateMe handles update current user profile
func (h *AuthHandlers) UpdateMe() gin.HandlerFunc {
	return func(c *gin.Context) {
		var req struct {
			FirstName   *string `json:"first_name"`
			LastName    *string `json:"last_name"`
			PhoneNumber *string `json:"phone_number"`
		}
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request: " + err.Error()})
			return
		}

		pbReq := &pb.UpdateMeRequest{}
		if req.FirstName != nil {
			pbReq.FirstName = req.FirstName
		}
		if req.LastName != nil {
			pbReq.LastName = req.LastName
		}
		if req.PhoneNumber != nil {
			pbReq.PhoneNumber = req.PhoneNumber
		}

		user, err := h.commodore.UpdateMe(c.Request.Context(), pbReq)
		if err != nil {
			h.logger.WithError(err).Error("Update profile failed")
			c.JSON(http.StatusInternalServerError, gin.H{"error": "update failed"})
			return
		}

		c.JSON(http.StatusOK, userToJSON(user))
	}
}

// UpdateNewsletter handles newsletter subscription update
func (h *AuthHandlers) UpdateNewsletter() gin.HandlerFunc {
	return func(c *gin.Context) {
		var req struct {
			Subscribed bool `json:"subscribed"`
		}
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request: " + err.Error()})
			return
		}

		resp, err := h.commodore.UpdateNewsletter(c.Request.Context(), req.Subscribed)
		if err != nil {
			h.logger.WithError(err).Error("Update newsletter failed")
			c.JSON(http.StatusInternalServerError, gin.H{"error": "update failed"})
			return
		}

		c.JSON(http.StatusOK, gin.H{
			"success": resp.Success,
			"message": resp.Message,
		})
	}
}

// GetNewsletterStatus returns the user's current newsletter subscription status
func (h *AuthHandlers) GetNewsletterStatus() gin.HandlerFunc {
	return func(c *gin.Context) {
		subscribed, err := h.commodore.GetNewsletterStatus(c.Request.Context())
		if err != nil {
			h.logger.WithError(err).Error("Get newsletter status failed")
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to get status"})
			return
		}

		c.JSON(http.StatusOK, gin.H{
			"subscribed": subscribed,
		})
	}
}

// userToJSON converts a proto User to a JSON-friendly map
func userToJSON(u *pb.User) map[string]interface{} {
	if u == nil {
		return nil
	}
	result := map[string]interface{}{
		"id":          u.Id,
		"tenant_id":   u.TenantId,
		"email":       u.Email,
		"first_name":  u.FirstName,
		"last_name":   u.LastName,
		"role":        u.Role,
		"permissions": u.Permissions,
		"is_active":   u.IsActive,
		"is_verified": u.IsVerified,
		"created_at":  u.CreatedAt.AsTime(),
		"updated_at":  u.UpdatedAt.AsTime(),
	}
	if u.LastLoginAt != nil {
		result["last_login_at"] = u.LastLoginAt.AsTime()
	}
	return result
}
