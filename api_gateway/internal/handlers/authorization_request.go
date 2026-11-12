package handlers

import (
	"context"
	"net/http"
	"strings"
	"time"

	authutil "github.com/Livepeer-FrameWorks/monorepo/pkg/auth"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	"github.com/gin-gonic/gin"
)

type authorizationInput struct {
	ClientID            string `json:"client_id" binding:"required"`
	RedirectURI         string `json:"redirect_uri" binding:"required"`
	CodeChallenge       string `json:"code_challenge" binding:"required"`
	CodeChallengeMethod string `json:"code_challenge_method" binding:"required"`
	Scope               string `json:"scope"`
	State               string `json:"state"`
	RequestContext      string `json:"request_context"`
}

func (r authorizationInput) matches(p authutil.NativeAuthorizationRequest) bool {
	scope := r.Scope
	if scope == "" {
		scope = "account"
	}
	return r.ClientID == p.ClientID && r.RedirectURI == p.RedirectURI && r.CodeChallenge == p.CodeChallenge && r.CodeChallengeMethod == p.CodeChallengeMethod && scope == p.Scope && r.State == p.State
}

func (h *AuthHandlers) AuthorizeStart() gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 8192)
		var input authorizationInput
		if err := c.ShouldBindJSON(&input); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid authorization request"})
			return
		}
		key := h.runtimeSettings().JWTSecret
		if len(key) == 0 {
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": "authorization service temporarily unavailable"})
			return
		}
		info := ctxkeys.GetAuthClientMetadata(c.Request.Context())
		now := time.Now()
		token, err := authutil.SignNativeAuthorizationContext(authutil.NativeAuthorizationRequest{ClientID: input.ClientID, RedirectURI: input.RedirectURI, CodeChallenge: input.CodeChallenge, CodeChallengeMethod: input.CodeChallengeMethod, Scope: input.Scope, State: input.State, DeviceLabel: info.DeviceLabel, ClientVersion: info.Version, IP: info.IP, UserAgent: info.UserAgent}, key, now)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid authorization request"})
			return
		}
		c.Header("Cache-Control", "no-store")
		c.JSON(http.StatusOK, gin.H{"request_context": token, "expires_at": now.Add(authutil.NativeAuthorizationContextTTL).UTC().Truncate(time.Second)})
	}
}

func (h *AuthHandlers) AuthorizeLookup() gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 16384)
		var input struct {
			RequestContext string `json:"request_context" binding:"required"`
		}
		if err := c.ShouldBindJSON(&input); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid authorization request"})
			return
		}
		key := h.runtimeSettings().JWTSecret
		if len(key) == 0 {
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": "authorization service temporarily unavailable"})
			return
		}
		request, err := authutil.VerifyNativeAuthorizationContext(input.RequestContext, key, time.Now())
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid or expired authorization request"})
			return
		}
		location := ""
		if data := h.sessionGeoIP.Lookup(request.IP); data != nil {
			location = strings.Trim(strings.Join([]string{data.City, data.CountryName}, ", "), ", ")
		}
		c.Header("Cache-Control", "no-store")
		c.JSON(http.StatusOK, gin.H{"client_id": request.ClientID, "redirect_uri": request.RedirectURI, "code_challenge": request.CodeChallenge, "code_challenge_method": request.CodeChallengeMethod, "scope": request.Scope, "state": request.State, "device_label": request.DeviceLabel, "client_version": request.ClientVersion, "request_ip": request.IP, "user_agent": request.UserAgent, "location": location, "requested_at": time.Unix(request.RequestedAt, 0).UTC(), "expires_at": time.Unix(request.ExpiresAt, 0).UTC()})
	}
}

// Identity remains the verified browser's; the signed context supplies only
// the native request's display metadata and exact PKCE binding.
func (h *AuthHandlers) authorizationContext(c *gin.Context, input authorizationInput) (context.Context, bool) {
	ctx := c.Request.Context()
	if input.RequestContext == "" {
		return ctx, true
	}
	key := h.runtimeSettings().JWTSecret
	if len(key) == 0 {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "authorization service temporarily unavailable"})
		return nil, false
	}
	request, err := authutil.VerifyNativeAuthorizationContext(input.RequestContext, key, time.Now())
	if err != nil || !input.matches(request) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid or expired authorization request"})
		return nil, false
	}
	ctx = ctxkeys.WithAuthClientMetadata(ctx, ctxkeys.AuthClientMetadata{IP: request.IP, DeviceLabel: request.DeviceLabel, Version: request.ClientVersion, UserAgent: request.UserAgent})
	ctx = context.WithValue(ctx, ctxkeys.KeyClientIP, request.IP)
	return ctx, true
}
