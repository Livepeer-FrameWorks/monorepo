// Package mcp implements a Model Context Protocol server for the FrameWorks platform.
// It enables autonomous AI agents to discover, self-assess, and use platform features.
package mcp

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"frameworks/api_gateway/internal/clients"
	"frameworks/api_gateway/internal/mcp/preflight"
	"frameworks/api_gateway/internal/mcp/prompts"
	"frameworks/api_gateway/internal/mcp/resources"
	"frameworks/api_gateway/internal/mcp/tools"
	"frameworks/api_gateway/internal/middleware"
	"frameworks/api_gateway/internal/resolvers"
	"frameworks/pkg/ctxkeys"
	"frameworks/pkg/logging"
	"frameworks/pkg/version"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Server wraps the MCP server with FrameWorks-specific functionality.
type Server struct {
	mcpServer      *mcp.Server
	serviceClients *clients.ServiceClients
	resolver       *resolvers.Resolver
	logger         logging.Logger
	jwtSecret      []byte
	preflightCheck *preflight.Checker
	rateLimiter    *middleware.RateLimiter
	tenantCache    *middleware.TenantCache
	usageTracker   *middleware.UsageTracker
	trustedProxies *middleware.TrustedProxies
}

// Config holds configuration for the MCP server.
type Config struct {
	ServiceClients *clients.ServiceClients
	Resolver       *resolvers.Resolver
	Logger         logging.Logger
	JWTSecret      []byte
	ServiceToken   string
	RateLimiter    *middleware.RateLimiter
	TenantCache    *middleware.TenantCache
	UsageTracker   *middleware.UsageTracker
	TrustedProxies *middleware.TrustedProxies
}

// NewServer creates a new MCP server with all resources, tools, and prompts registered.
func NewServer(cfg Config) *Server {
	// Create the MCP server
	mcpServer := mcp.NewServer(&mcp.Implementation{
		Name:    "frameworks",
		Version: version.Version,
	}, nil)

	s := &Server{
		mcpServer:      mcpServer,
		serviceClients: cfg.ServiceClients,
		resolver:       cfg.Resolver,
		logger:         cfg.Logger,
		jwtSecret:      cfg.JWTSecret,
		preflightCheck: preflight.NewChecker(cfg.ServiceClients, cfg.Logger),
		rateLimiter:    cfg.RateLimiter,
		tenantCache:    cfg.TenantCache,
		usageTracker:   cfg.UsageTracker,
		trustedProxies: cfg.TrustedProxies,
	}

	// Register resources
	s.registerResources()

	// Register tools
	s.registerTools()

	// Register prompts
	s.registerPrompts()

	// Register access controls (auth + x402 + rate limiting + usage)
	s.registerAccessMiddleware()

	return s
}

// registerResources registers all MCP resources.
func (s *Server) registerResources() {
	// Account status - agent self-awareness (critical)
	resources.RegisterAccountResources(s.mcpServer, s.serviceClients, s.resolver, s.logger)

	// Billing resources
	resources.RegisterBillingResources(s.mcpServer, s.serviceClients, s.resolver, s.logger)

	// Stream resources
	resources.RegisterStreamResources(s.mcpServer, s.serviceClients, s.resolver, s.logger)

	// Analytics resources
	resources.RegisterAnalyticsResources(s.mcpServer, s.serviceClients, s.resolver, s.logger)

	// Node resources
	resources.RegisterNodeResources(s.mcpServer, s.serviceClients, s.resolver, s.logger)

	// VOD resources
	resources.RegisterVODResources(s.mcpServer, s.serviceClients, s.resolver, s.logger)

	// Knowledge resources (video streaming expertise)
	resources.RegisterKnowledgeResources(s.mcpServer, s.serviceClients, s.resolver, s.logger)

	// Support resources (conversation history)
	resources.RegisterSupportResources(s.mcpServer, s.serviceClients, s.resolver, s.logger)

	// API schema resources (catalog of curated examples)
	resources.RegisterAPISchemaResources(s.mcpServer, s.serviceClients, s.resolver, s.logger)
}

// registerTools registers all MCP tools.
func (s *Server) registerTools() {
	// Account tools (always allowed)
	tools.RegisterAccountTools(s.mcpServer, s.serviceClients, s.resolver, s.preflightCheck, s.logger)

	// Payment tools (x402 auth and balance - works without prior auth)
	tools.RegisterPaymentTools(s.mcpServer, s.serviceClients, s.resolver, s.preflightCheck, s.logger)

	// Billing tools (require billing details)
	tools.RegisterBillingTools(s.mcpServer, s.serviceClients, s.resolver, s.preflightCheck, s.logger)

	// Stream tools (require billing + balance)
	tools.RegisterStreamTools(s.mcpServer, s.serviceClients, s.resolver, s.preflightCheck, s.logger)

	// Clip tools (require billing + balance)
	tools.RegisterClipTools(s.mcpServer, s.serviceClients, s.resolver, s.preflightCheck, s.logger)

	// DVR tools (require billing + balance)
	tools.RegisterDVRTools(s.mcpServer, s.serviceClients, s.resolver, s.preflightCheck, s.logger)

	// Playback tools (free)
	tools.RegisterPlaybackTools(s.mcpServer, s.serviceClients, s.resolver, s.preflightCheck, s.logger)

	// VOD tools (require billing + balance for upload)
	tools.RegisterVODTools(s.mcpServer, s.serviceClients, s.resolver, s.preflightCheck, s.logger)

	// QoE diagnostic tools (for video consultant)
	tools.RegisterQoETools(s.mcpServer, s.serviceClients, s.resolver, s.preflightCheck, s.logger)

	// Support tools (search history)
	tools.RegisterSupportTools(s.mcpServer, s.serviceClients, s.resolver, s.preflightCheck, s.logger)

	// API integration assistant tools (schema introspection, query generation)
	tools.RegisterAPIAssistantTools(s.mcpServer, s.serviceClients, s.resolver, s.preflightCheck, s.logger)
}

// registerPrompts registers all MCP prompts.
func (s *Server) registerPrompts() {
	prompts.RegisterPrompts(s.mcpServer, s.serviceClients, s.preflightCheck, s.logger)
}

// HTTPHandler returns an HTTP handler for the MCP server.
// It handles authentication and creates per-request servers with the appropriate context.
func (s *Server) HTTPHandler() http.Handler {
	baseHandler := mcp.NewStreamableHTTPHandler(
		func(r *http.Request) *mcp.Server {
			// Check if we need to send on-connect notification
			s.sendOnConnectNotification(r.Context())

			// The SDK manages sessions internally - we just return our configured server
			// Authentication context is passed via the request context
			return s.mcpServer
		},
		&mcp.StreamableHTTPOptions{
			Stateless:      false, // Maintain session state
			JSONResponse:   false, // Use SSE format
			SessionTimeout: 0,     // No timeout
		},
	)

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := s.extractAuthContext(r)
		r = r.WithContext(ctx)

		if ctxkeys.GetAuthType(ctx) == "x402" {
			if token := ctxkeys.GetJWTToken(ctx); token != "" {
				w.Header().Set("X-Access-Token", token)
			}
			if expiresAt, ok := ctxkeys.GetJWTExpiresAt(ctx); ok && !expiresAt.IsZero() {
				w.Header().Set("X-Access-Token-Expires-At", expiresAt.UTC().Format(time.RFC3339))
			}
		}

		baseHandler.ServeHTTP(w, r)
	})
}

// extractAuthContext extracts authentication from the HTTP request and returns a context with user info.
// Supports multiple auth methods:
// 1. JWT/API tokens via middleware.AuthenticateRequest
// 2. Wallet signature via X-Wallet-* headers
func (s *Server) extractAuthContext(r *http.Request) context.Context {
	ctx := r.Context()

	authResult, err := middleware.AuthenticateRequest(ctx, r, s.serviceClients, s.jwtSecret, middleware.AuthOptions{
		AllowCookies: false,
		AllowWallet:  true,
		AllowX402:    false,
	}, s.logger)
	if err != nil {
		s.logger.WithError(err).Warn("MCP auth failed")
	} else if authResult != nil {
		ctx = middleware.ApplyAuthToContext(ctx, authResult)
	}

	clientIP := middleware.ClientIPFromRequestWithTrust(r, s.trustedProxies)
	if clientIP != "" {
		ctx = context.WithValue(ctx, ctxkeys.KeyClientIP, clientIP)
	}

	var xPayment string
	if r != nil {
		if r.URL != nil {
			ctx = context.WithValue(ctx, ctxkeys.KeyRequestPath, r.URL.Path)
		}
		xPayment = middleware.GetX402PaymentHeader(r)
		if xPayment != "" {
			ctx = context.WithValue(ctx, ctxkeys.KeyXPayment, xPayment)
		}
	}

	return ctx
}

// sendOnConnectNotification checks account status and sends a notification if setup is needed.
func (s *Server) sendOnConnectNotification(ctx context.Context) {
	tenantID := ctxkeys.GetTenantID(ctx)
	if tenantID == "" {
		return // No tenant context, skip notification
	}

	// Check account status
	blockers, err := s.preflightCheck.GetBlockers(ctx)
	if err != nil {
		s.logger.WithError(err).Warn("Failed to check account status for notification")
		return
	}

	if len(blockers) > 0 {
		// Account needs setup - would send notification via MCP
		// Note: The SDK handles notifications via the session, we log for now
		s.logger.WithField("tenant_id", tenantID).
			WithField("blockers", len(blockers)).
			Info("MCP client connected with account setup required")
	}
}

// registerAccessMiddleware enforces auth, x402 settlement, rate limits, and usage tracking.
func (s *Server) registerAccessMiddleware() {
	if s.rateLimiter == nil || s.tenantCache == nil {
		if s.logger != nil {
			s.logger.Warn("MCP access middleware disabled (missing rate limiter or tenant cache)")
		}
		return
	}

	s.mcpServer.AddReceivingMiddleware(func(next mcp.MethodHandler) mcp.MethodHandler {
		return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
			if strings.HasPrefix(method, "notifications/") {
				return next(ctx, method, req)
			}

			start := time.Now()
			opName := mcpOperationName(method, req.GetParams())

			clientIP := ctxkeys.GetClientIP(ctx)
			xPayment := ctxkeys.GetXPayment(ctx)
			if extra := req.GetExtra(); extra != nil && extra.Header != nil {
				if headerPayment := middleware.GetX402PaymentHeaderFromHeaders(extra.Header); headerPayment != "" {
					xPayment = headerPayment
				}
			}

			resourcePath := mcpOperationResourcePath(opName, req.GetParams())
			tenantID := ctxkeys.GetTenantID(ctx)
			contentID := ""
			if opName == "mcp:tools/call:resolve_playback_endpoint" {
				contentID = extractPlaybackContentID(req.GetParams())
				if contentID != "" {
					if ownerTenantID := s.resolvePlaybackOwnerTenant(ctx, contentID); ownerTenantID != "" {
						tenantID = ownerTenantID
						ctx = context.WithValue(ctx, ctxkeys.KeyTenantID, ownerTenantID)
						resourcePath = "viewer://" + contentID
					}
				}
			} else if tenantID == "" && xPayment != "" {
				ctx = s.applyX402Auth(ctx, xPayment, clientIP)
				tenantID = ctxkeys.GetTenantID(ctx)
			}

			publicAllowlisted := isPublicMCPOperation(opName)
			decision := middleware.EvaluateAccess(ctx, middleware.AccessRequest{
				TenantID:          tenantID,
				ClientIP:          clientIP,
				Path:              resourcePath,
				OperationName:     opName,
				XPayment:          xPayment,
				PublicAllowlisted: publicAllowlisted,
				X402Processed:     ctxkeys.IsX402Processed(ctx),
				X402AuthOnly:      ctxkeys.IsX402AuthOnly(ctx),
			}, s.rateLimiter, s.tenantCache.GetLimitsFunc(), s.tenantCache, s.serviceClients.Purser, s.serviceClients.Purser, s.serviceClients.Commodore, s.logger)

			if !decision.Allowed {
				return nil, accessDecisionError(decision)
			}

			result, err := next(ctx, method, req)
			if s.usageTracker != nil {
				durationMs := uint64(time.Since(start).Milliseconds())
				authType := deriveAuthType(ctx)
				userID := ctxkeys.GetUserID(ctx)
				tenantID := ctxkeys.GetTenantID(ctx)
				if tenantID == "" {
					tenantID = "anonymous"
				}
				errorCount := uint32(0)
				if err != nil {
					errorCount = 1
				} else if toolResult, ok := result.(*mcp.CallToolResult); ok && toolResult != nil && toolResult.IsError {
					errorCount = 1
				}
				s.usageTracker.Record(start, tenantID, authType, "mcp", opName, userID, getContextTokenHash(ctx), durationMs, 0, errorCount)
			}

			return result, err
		}
	})
}

func accessDecisionError(decision middleware.AccessDecision) error {
	code := int64(jsonrpc.CodeInternalError)
	message := "request denied"
	switch decision.Status {
	case http.StatusPaymentRequired:
		code = -32002
		message = "payment required"
	case http.StatusTooManyRequests:
		code = -32029
		message = "rate limit exceeded"
	case http.StatusUnauthorized:
		code = -32001
		message = "unauthorized"
	}

	payload := map[string]any{}
	for key, value := range decision.Body {
		payload[key] = value
	}
	if len(decision.Headers) > 0 {
		payload["headers"] = decision.Headers
	}

	data, err := json.Marshal(payload)
	if err != nil {
		return &jsonrpc.Error{Code: code, Message: message}
	}
	return &jsonrpc.Error{Code: code, Message: message, Data: data}
}

func mcpOperationName(method string, params mcp.Params) string {
	switch method {
	case "tools/call":
		if callParams, ok := params.(*mcp.CallToolParamsRaw); ok && callParams != nil && callParams.Name != "" {
			return "mcp:tools/call:" + callParams.Name
		}
		return "mcp:tools/call"
	case "resources/read":
		if readParams, ok := params.(*mcp.ReadResourceParams); ok && readParams != nil && readParams.URI != "" {
			return "mcp:resources/read:" + readParams.URI
		}
		return "mcp:resources/read"
	case "prompts/get":
		return "mcp:prompts/get"
	case "prompts/list":
		return "mcp:prompts/list"
	case "tools/list":
		return "mcp:tools/list"
	case "resources/list":
		return "mcp:resources/list"
	case "resources/templates/list":
		return "mcp:resources/templates/list"
	case "initialize":
		return "mcp:initialize"
	}

	if method != "" {
		return "mcp:" + method
	}
	return "mcp:unknown"
}

func mcpOperationResourcePath(opName string, params mcp.Params) string {
	if opName == "" {
		return "graphql://operation"
	}

	if strings.HasPrefix(opName, "mcp:tools/call:") {
		toolName := strings.TrimPrefix(opName, "mcp:tools/call:")
		if resource := mcpToolResource(toolName, params); resource != "" {
			return resource
		}
		if gqlOp := mcpToolGraphQLOp(toolName); gqlOp != "" {
			return "graphql://" + gqlOp
		}
	}

	return "graphql://" + strings.TrimPrefix(opName, "mcp:")
}

func mcpToolGraphQLOp(toolName string) string {
	switch toolName {
	case "create_stream":
		return "createStream"
	case "update_stream":
		return "updateStream"
	case "delete_stream":
		return "deleteStream"
	case "refresh_stream_key":
		return "refreshStreamKey"
	case "create_clip":
		return "createClip"
	case "delete_clip":
		return "deleteClip"
	case "start_dvr":
		return "startDVR"
	case "stop_dvr":
		return "stopDVR"
	case "create_vod_upload":
		return "createVodUpload"
	case "complete_vod_upload":
		return "completeVodUpload"
	case "abort_vod_upload":
		return "abortVodUpload"
	case "delete_vod_asset":
		return "deleteVodAsset"
	default:
		return ""
	}
}

func mcpToolResource(toolName string, params mcp.Params) string {
	switch toolName {
	case "resolve_playback_endpoint":
		if contentID := extractPlaybackContentID(params); contentID != "" {
			return "viewer://" + contentID
		}
	case "update_stream", "delete_stream", "refresh_stream_key", "create_clip", "start_dvr":
		if streamID := getMcpArgString(params, "stream_id", "streamId", "streamID"); streamID != "" {
			return "stream://" + streamID
		}
	case "delete_clip":
		if clipHash := getMcpArgString(params, "clip_hash", "clipHash"); clipHash != "" {
			return "clip://" + clipHash
		}
	case "stop_dvr":
		if dvrHash := getMcpArgString(params, "dvr_hash", "dvrHash"); dvrHash != "" {
			return "dvr://" + dvrHash
		}
	case "delete_vod_asset":
		if artifact := getMcpArgString(params, "artifact_hash", "artifactHash", "id"); artifact != "" {
			return "vod://" + artifact
		}
	}

	return ""
}

func getMcpArgString(params mcp.Params, keys ...string) string {
	callParams, ok := params.(*mcp.CallToolParamsRaw)
	if !ok || callParams == nil || len(callParams.Arguments) == 0 {
		return ""
	}
	var payload map[string]any
	if err := json.Unmarshal(callParams.Arguments, &payload); err != nil {
		return ""
	}
	for _, key := range keys {
		if value, ok := payload[key]; ok {
			if str, ok := value.(string); ok && str != "" {
				return str
			}
		}
	}
	return ""
}

func isPublicMCPOperation(opName string) bool {
	switch opName {
	case "mcp:tools/list",
		"mcp:resources/list",
		"mcp:resources/templates/list",
		"mcp:prompts/list",
		"mcp:prompts/get",
		"mcp:initialize",
		"mcp:tools/call:get_payment_options",
		"mcp:tools/call:submit_payment",
		"mcp:tools/call:resolve_playback_endpoint",
		"mcp:resources/read:account://status",
		"mcp:resources/read:billing://pricing":
		return true
	default:
		return false
	}
}

func deriveAuthType(ctx context.Context) string {
	if v := ctxkeys.GetAuthType(ctx); v != "" {
		return v
	}
	if ctxkeys.GetJWTToken(ctx) != "" {
		return "jwt"
	}
	if ctxkeys.GetAPIToken(ctx) != "" {
		return "api_token"
	}
	if ctxkeys.GetWalletAddress(ctx) != "" {
		return "wallet"
	}
	return "anonymous"
}

func getContextTokenHash(ctx context.Context) uint64 {
	if v := ctx.Value(ctxkeys.KeyAPITokenHash); v != nil {
		switch t := v.(type) {
		case uint64:
			return t
		case uint32:
			return uint64(t)
		case int64:
			if t > 0 {
				return uint64(t)
			}
		case int:
			if t > 0 {
				return uint64(t)
			}
		}
	}
	return 0
}

func extractPlaybackContentID(params mcp.Params) string {
	callParams, ok := params.(*mcp.CallToolParamsRaw)
	if !ok || callParams == nil {
		return ""
	}
	if callParams.Name != "resolve_playback_endpoint" {
		return ""
	}
	if len(callParams.Arguments) == 0 {
		return ""
	}

	var payload map[string]any
	if err := json.Unmarshal(callParams.Arguments, &payload); err != nil {
		return ""
	}
	for _, key := range []string{"content_id", "contentId", "contentID"} {
		if value, ok := payload[key]; ok {
			if s, ok := value.(string); ok && s != "" {
				return s
			}
		}
	}
	return ""
}

func (s *Server) resolvePlaybackOwnerTenant(ctx context.Context, contentID string) string {
	if contentID == "" || s.serviceClients == nil || s.serviceClients.Commodore == nil {
		return ""
	}
	if resp, err := s.serviceClients.Commodore.ResolveArtifactPlaybackID(ctx, contentID); err == nil && resp.Found && resp.TenantId != "" {
		return resp.TenantId
	}
	if resp, err := s.serviceClients.Commodore.ResolvePlaybackID(ctx, contentID); err == nil && resp.TenantId != "" {
		return resp.TenantId
	}
	return ""
}

func (s *Server) applyX402Auth(ctx context.Context, xPayment, clientIP string) context.Context {
	if xPayment == "" || s.serviceClients == nil || s.serviceClients.Commodore == nil {
		return ctx
	}

	payload, err := middleware.ParseX402PaymentHeader(xPayment)
	if err != nil {
		if s.logger != nil {
			s.logger.WithError(err).Warn("Invalid X-PAYMENT header")
		}
		return ctx
	}

	resp, err := s.serviceClients.Commodore.WalletLoginWithX402(ctx, payload, clientIP, "")
	if err != nil {
		if s.logger != nil {
			s.logger.WithError(err).Warn("X-PAYMENT login failed")
		}
		return ctx
	}
	if resp == nil || resp.Auth == nil || resp.Auth.User == nil {
		return ctx
	}

	email := ""
	if resp.Auth.User.Email != nil {
		email = *resp.Auth.User.Email
	}
	expiresAt := (*time.Time)(nil)
	if resp.Auth.ExpiresAt != nil {
		value := resp.Auth.ExpiresAt.AsTime()
		expiresAt = &value
	}

	walletAddress := resp.PayerAddress
	if walletAddress == "" && payload.Payload != nil && payload.Payload.Authorization != nil {
		walletAddress = payload.Payload.Authorization.From
	}

	authResult := &middleware.AuthResult{
		UserID:        resp.Auth.User.Id,
		TenantID:      resp.Auth.User.TenantId,
		Email:         email,
		Role:          resp.Auth.User.Role,
		AuthType:      "x402",
		JWTToken:      resp.Auth.Token,
		WalletAddress: walletAddress,
		ExpiresAt:     expiresAt,
		X402Processed: true,
		X402AuthOnly:  resp.IsAuthOnly,
	}

	return middleware.ApplyAuthToContext(ctx, authResult)
}
