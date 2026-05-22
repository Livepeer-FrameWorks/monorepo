package handlers

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strings"

	"frameworks/api_gateway/internal/attribution"
	gatewayerrors "frameworks/api_gateway/internal/errors"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/clients/commodore"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/config"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	pb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto"

	"github.com/gin-gonic/gin"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
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

// handleBotCheckError writes a distinct HTTP 403 + BOT_CHECK_FAILED response
// when Commodore signalled a bot-verification failure, and returns true to
// short-circuit the caller. Returns false for any other error so the caller
// can continue with its normal error handling.
func handleBotCheckError(c *gin.Context, err error) bool {
	st, ok := status.FromError(err)
	if !ok || st.Code() != codes.PermissionDenied || !strings.Contains(st.Message(), "bot verification") {
		return false
	}
	c.JSON(http.StatusForbidden, gin.H{
		"error":      "browser verification required",
		"error_code": "BOT_CHECK_FAILED",
	})
	return true
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
		cookieDomain: config.GetCookieDomain(),
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
			if isAuthServiceUnavailable(err) {
				c.JSON(http.StatusServiceUnavailable, gin.H{"error": "authentication service temporarily unavailable"})
				return
			}
			if handleBotCheckError(c, err) {
				return
			}
			errMsg := gatewayerrors.SanitizeGRPCError(err, "invalid credentials", loginAllowedErrors)
			c.JSON(http.StatusUnauthorized, gin.H{"error": errMsg})
			return
		}

		// Set all auth tokens as HttpOnly cookies.
		isDev := config.IsDevelopment()
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
			if isAuthServiceUnavailable(err) {
				c.JSON(http.StatusServiceUnavailable, gin.H{"error": "authentication service temporarily unavailable"})
				return
			}
			errMsg := gatewayerrors.SanitizeGRPCError(err, "wallet authentication failed", walletLoginAllowedErrors)
			c.JSON(http.StatusUnauthorized, gin.H{"error": errMsg})
			return
		}

		// Set all auth tokens as HttpOnly cookies (same as Login).
		isDev := config.IsDevelopment()
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
			if handleBotCheckError(c, err) {
				return
			}
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

		// Clear all auth cookies (must match domain and Secure flag to actually clear).
		isDev := config.IsDevelopment()
		secure := !isDev
		c.SetCookie(accessTokenCookie, "", -1, "/", h.cookieDomain, secure, true)
		c.SetCookie(refreshTokenCookie, "", -1, "/", h.cookieDomain, secure, true)
		c.SetCookie(tenantIDCookie, "", -1, "/", h.cookieDomain, secure, true)

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
			if isAuthServiceUnavailable(err) {
				c.JSON(http.StatusServiceUnavailable, gin.H{"error": "authentication service temporarily unavailable"})
				return
			}
			// Clear invalid cookies (must match Secure flag to actually clear).
			isDev := config.IsDevelopment()
			secure := !isDev
			c.SetCookie(refreshTokenCookie, "", -1, "/", h.cookieDomain, secure, true)
			c.SetCookie(accessTokenCookie, "", -1, "/", h.cookieDomain, secure, true)
			c.SetCookie(tenantIDCookie, "", -1, "/", h.cookieDomain, secure, true)
			c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid or expired refresh token"})
			return
		}

		// Set all auth tokens as HttpOnly cookies.
		isDev := config.IsDevelopment()
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

// WebappURL exposes the configured webapp origin/base path to native clients
// so browser handoff uses the same public URL as hosted login.
func (h *AuthHandlers) WebappURL() gin.HandlerFunc {
	return func(c *gin.Context) {
		webappURL := strings.TrimRight(strings.TrimSpace(config.GetEnv("WEBAPP_PUBLIC_URL", "")), "/")
		if webappURL == "" {
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": "webapp public URL is not configured"})
			return
		}
		parsed, err := url.Parse(webappURL)
		if err != nil || parsed.Scheme == "" || parsed.Host == "" {
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": "webapp public URL is invalid"})
			return
		}
		c.JSON(http.StatusOK, gin.H{"webapp_url": webappURL})
	}
}

// AuthorizeComplete handles POST /auth/authorize/complete — the webapp
// /authorize page calls this after the signed-in user clicks "Approve". The
// user_id/tenant_id forwarded to Commodore come from the verified JWT
// session (gateway's authInterceptor propagates them via gRPC metadata);
// the client cannot supply them.
func (h *AuthHandlers) AuthorizeComplete() gin.HandlerFunc {
	return func(c *gin.Context) {
		var req struct {
			ClientID            string `json:"client_id" binding:"required"`
			RedirectURI         string `json:"redirect_uri" binding:"required"`
			CodeChallenge       string `json:"code_challenge" binding:"required"`
			CodeChallengeMethod string `json:"code_challenge_method" binding:"required"`
			Scope               string `json:"scope"`
			State               string `json:"state"`
		}
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request: " + err.Error()})
			return
		}

		resp, err := h.commodore.CompleteAuthorization(c.Request.Context(), &pb.CompleteAuthorizationRequest{
			ClientId:            req.ClientID,
			RedirectUri:         req.RedirectURI,
			CodeChallenge:       req.CodeChallenge,
			CodeChallengeMethod: req.CodeChallengeMethod,
			Scope:               req.Scope,
			State:               req.State,
		})
		if err != nil {
			h.logger.WithError(err).Debug("CompleteAuthorization failed")
			if isAuthServiceUnavailable(err) {
				c.JSON(http.StatusServiceUnavailable, gin.H{"error": "authentication service temporarily unavailable"})
				return
			}
			st, ok := status.FromError(err)
			if ok && st.Code() == codes.InvalidArgument {
				c.JSON(http.StatusBadRequest, gin.H{"error": st.Message()})
				return
			}
			if ok && st.Code() == codes.PermissionDenied {
				c.JSON(http.StatusForbidden, gin.H{"error": st.Message()})
				return
			}
			c.JSON(http.StatusInternalServerError, gin.H{"error": "authorization failed"})
			return
		}

		c.JSON(http.StatusOK, gin.H{
			"code":       resp.Code,
			"expires_at": resp.ExpiresAt.AsTime(),
		})
	}
}

// OAuthToken handles POST /auth/oauth/token — the native client's loopback
// receiver calls this with the PKCE authorization code and verifier to obtain
// a real session. Tokens are returned in the body (not cookies) because the
// native client stores them in OS Keychain/credential store.
func (h *AuthHandlers) OAuthToken() gin.HandlerFunc {
	return func(c *gin.Context) {
		var req struct {
			Code         string `json:"code" binding:"required"`
			CodeVerifier string `json:"code_verifier" binding:"required"`
			ClientID     string `json:"client_id" binding:"required"`
			RedirectURI  string `json:"redirect_uri" binding:"required"`
		}
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request: " + err.Error()})
			return
		}

		resp, err := h.commodore.ExchangeAuthorizationCode(c.Request.Context(), &pb.ExchangeAuthorizationCodeRequest{
			Code:         req.Code,
			CodeVerifier: req.CodeVerifier,
			ClientId:     req.ClientID,
			RedirectUri:  req.RedirectURI,
		})
		if err != nil {
			h.logger.WithError(err).Debug("ExchangeAuthorizationCode failed")
			if isAuthServiceUnavailable(err) {
				c.JSON(http.StatusServiceUnavailable, gin.H{"error": "authentication service temporarily unavailable"})
				return
			}
			st, _ := status.FromError(err)
			switch st.Code() {
			case codes.Unauthenticated, codes.PermissionDenied:
				c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid_grant"})
			case codes.AlreadyExists:
				c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_grant", "error_description": "authorization code already used"})
			default:
				c.JSON(http.StatusInternalServerError, gin.H{"error": "token exchange failed"})
			}
			return
		}

		c.JSON(http.StatusOK, gin.H{
			"access_token":  resp.Token,
			"refresh_token": resp.RefreshToken,
			"token_type":    "Bearer",
			"expires_at":    resp.ExpiresAt.AsTime(),
			"user":          userToJSON(resp.User),
		})
	}
}

// DeviceStart handles POST /auth/device/start — CLI begins a device-code
// grant. No authentication required.
func (h *AuthHandlers) DeviceStart() gin.HandlerFunc {
	return func(c *gin.Context) {
		var req struct {
			ClientID string `json:"client_id" binding:"required"`
			Scope    string `json:"scope"`
		}
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request: " + err.Error()})
			return
		}

		resp, err := h.commodore.StartDeviceAuthorization(c.Request.Context(), &pb.StartDeviceAuthorizationRequest{
			ClientId: req.ClientID,
			Scope:    req.Scope,
		})
		if err != nil {
			h.logger.WithError(err).Debug("StartDeviceAuthorization failed")
			if isAuthServiceUnavailable(err) {
				c.JSON(http.StatusServiceUnavailable, gin.H{"error": "authentication service temporarily unavailable"})
				return
			}
			st, _ := status.FromError(err)
			if st.Code() == codes.PermissionDenied || st.Code() == codes.InvalidArgument {
				c.JSON(http.StatusBadRequest, gin.H{"error": st.Message()})
				return
			}
			c.JSON(http.StatusInternalServerError, gin.H{"error": "device authorization failed"})
			return
		}

		c.JSON(http.StatusOK, gin.H{
			"device_code":               resp.DeviceCode,
			"user_code":                 resp.UserCode,
			"verification_uri":          resp.VerificationUri,
			"verification_uri_complete": resp.VerificationUriComplete,
			"expires_in":                resp.ExpiresInSeconds,
			"interval":                  resp.IntervalSeconds,
		})
	}
}

// DevicePoll handles POST /auth/device/poll — CLI polls for completion. On
// approval, returns access + refresh tokens in body. On pending markers,
// returns HTTP 400/429 with the RFC 8628 §3.5 error code.
func (h *AuthHandlers) DevicePoll() gin.HandlerFunc {
	return func(c *gin.Context) {
		var req struct {
			DeviceCode string `json:"device_code" binding:"required"`
			ClientID   string `json:"client_id" binding:"required"`
		}
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request: " + err.Error()})
			return
		}

		resp, err := h.commodore.PollDeviceAuthorization(c.Request.Context(), &pb.PollDeviceAuthorizationRequest{
			DeviceCode: req.DeviceCode,
			ClientId:   req.ClientID,
		})
		if err != nil {
			if isAuthServiceUnavailable(err) {
				c.JSON(http.StatusServiceUnavailable, gin.H{"error": "authentication service temporarily unavailable"})
				return
			}
			st, _ := status.FromError(err)
			marker := st.Message()
			switch marker {
			case "AUTHORIZATION_PENDING":
				c.JSON(http.StatusBadRequest, gin.H{"error": "authorization_pending", "error_code": marker})
			case "SLOW_DOWN":
				c.JSON(http.StatusTooManyRequests, gin.H{"error": "slow_down", "error_code": marker})
			case "ACCESS_DENIED":
				c.JSON(http.StatusForbidden, gin.H{"error": "access_denied", "error_code": marker})
			case "EXPIRED_TOKEN":
				c.JSON(http.StatusGone, gin.H{"error": "expired_token", "error_code": marker})
			default:
				h.logger.WithError(err).Debug("PollDeviceAuthorization failed")
				c.JSON(http.StatusInternalServerError, gin.H{"error": "device poll failed"})
			}
			return
		}

		c.JSON(http.StatusOK, gin.H{
			"access_token":  resp.Token,
			"refresh_token": resp.RefreshToken,
			"token_type":    "Bearer",
			"expires_at":    resp.ExpiresAt.AsTime(),
			"user":          userToJSON(resp.User),
		})
	}
}

// DeviceLookup handles POST /auth/device/lookup — the webapp /device page
// calls this before approval so the user can see which client requested the
// session.
func (h *AuthHandlers) DeviceLookup() gin.HandlerFunc {
	return func(c *gin.Context) {
		var req struct {
			UserCode string `json:"user_code" binding:"required"`
		}
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request: " + err.Error()})
			return
		}

		resp, err := h.commodore.LookupDeviceAuthorization(c.Request.Context(), &pb.LookupDeviceAuthorizationRequest{
			UserCode: req.UserCode,
		})
		if err != nil {
			h.logger.WithError(err).Debug("LookupDeviceAuthorization failed")
			if isAuthServiceUnavailable(err) {
				c.JSON(http.StatusServiceUnavailable, gin.H{"error": "authentication service temporarily unavailable"})
				return
			}
			st, _ := status.FromError(err)
			switch st.Code() {
			case codes.NotFound:
				c.JSON(http.StatusNotFound, gin.H{"error": st.Message()})
			case codes.InvalidArgument:
				c.JSON(http.StatusBadRequest, gin.H{"error": st.Message()})
			case codes.FailedPrecondition:
				c.JSON(http.StatusConflict, gin.H{"error": st.Message()})
			default:
				c.JSON(http.StatusInternalServerError, gin.H{"error": "device lookup failed"})
			}
			return
		}

		var expiresAt any
		if resp.ExpiresAt != nil {
			expiresAt = resp.ExpiresAt.AsTime()
		}
		c.JSON(http.StatusOK, gin.H{
			"client_id":  resp.ClientId,
			"scope":      resp.Scope,
			"expires_at": expiresAt,
		})
	}
}

// DeviceApprove handles POST /auth/device/approve — the webapp /device page
// calls this after the signed-in user confirms the displayed user_code.
// user_id/tenant_id come from the verified JWT session.
func (h *AuthHandlers) DeviceApprove() gin.HandlerFunc {
	return func(c *gin.Context) {
		var req struct {
			UserCode string `json:"user_code" binding:"required"`
		}
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request: " + err.Error()})
			return
		}

		resp, err := h.commodore.ApproveDeviceAuthorization(c.Request.Context(), &pb.ApproveDeviceAuthorizationRequest{
			UserCode: req.UserCode,
		})
		if err != nil {
			h.logger.WithError(err).Debug("ApproveDeviceAuthorization failed")
			if isAuthServiceUnavailable(err) {
				c.JSON(http.StatusServiceUnavailable, gin.H{"error": "authentication service temporarily unavailable"})
				return
			}
			st, _ := status.FromError(err)
			switch st.Code() {
			case codes.NotFound:
				c.JSON(http.StatusNotFound, gin.H{"error": st.Message()})
			case codes.InvalidArgument:
				c.JSON(http.StatusBadRequest, gin.H{"error": st.Message()})
			case codes.FailedPrecondition:
				c.JSON(http.StatusConflict, gin.H{"error": st.Message()})
			default:
				c.JSON(http.StatusInternalServerError, gin.H{"error": "device approval failed"})
			}
			return
		}

		c.JSON(http.StatusOK, gin.H{
			"success":   resp.Success,
			"client_id": resp.ClientId,
		})
	}
}

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
			if isAuthServiceUnavailable(err) {
				c.JSON(http.StatusServiceUnavailable, gin.H{"error": "authentication service temporarily unavailable"})
				return
			}
			c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
			return
		}

		c.JSON(http.StatusOK, userToJSON(user))
	}
}

func isAuthServiceUnavailable(err error) bool {
	if err == nil {
		return false
	}
	st, ok := status.FromError(err)
	if !ok {
		return false
	}
	switch st.Code() {
	case codes.Unavailable, codes.DeadlineExceeded:
		return true
	default:
		return false
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
func userToJSON(u *pb.User) map[string]any {
	if u == nil {
		return nil
	}
	result := map[string]any{
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
