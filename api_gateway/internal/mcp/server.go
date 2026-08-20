// Package mcp implements a Model Context Protocol server for the FrameWorks platform.
// It enables autonomous AI agents to discover, self-assess, and use platform features.
package mcp

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"frameworks/api_gateway/internal/clients"
	"frameworks/api_gateway/internal/mcp/mcperrors"
	"frameworks/api_gateway/internal/mcp/preflight"
	"frameworks/api_gateway/internal/mcp/prompts"
	"frameworks/api_gateway/internal/mcp/resources"
	"frameworks/api_gateway/internal/mcp/tools"
	"frameworks/api_gateway/internal/middleware"
	"frameworks/api_gateway/internal/resolvers"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/accesspolicy"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	purserpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/purser"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/tenants"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/version"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const (
	maxMCPRequestBytes = 256 << 10
	maxMCPResultBytes  = 64 << 10
	mcpSessionTimeout  = 30 * time.Minute
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
	skipperClient  tools.SkipperCaller
	originAllowed  func(string) bool
}

type skipperToolAvailability interface {
	ToolsAvailable(ctx context.Context) bool
}

// Config holds configuration for the MCP server.
type Config struct {
	ServiceClients *clients.ServiceClients
	Resolver       *resolvers.Resolver
	Logger         logging.Logger
	JWTSecret      []byte
	RateLimiter    *middleware.RateLimiter
	TenantCache    *middleware.TenantCache
	UsageTracker   *middleware.UsageTracker
	TrustedProxies *middleware.TrustedProxies
	SkipperClient  tools.SkipperCaller
	OriginAllowed  func(string) bool
}

// NewServer creates a new MCP server with all resources, tools, and prompts registered.
// Returns error if required dependencies (RateLimiter, TenantCache) are missing.
func NewServer(cfg Config) (*Server, error) {
	if cfg.RateLimiter == nil {
		return nil, fmt.Errorf("MCP server requires RateLimiter for access control")
	}
	if cfg.TenantCache == nil {
		return nil, fmt.Errorf("MCP server requires TenantCache for billing checks")
	}

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
		skipperClient:  cfg.SkipperClient,
		originAllowed:  cfg.OriginAllowed,
	}

	// Register resources
	s.registerResources()

	// Register tools
	s.registerTools()

	// Register prompts
	s.registerPrompts()

	// Register access controls (auth + x402 + rate limiting + usage)
	s.registerAccessMiddleware()

	return s, nil
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

	// Cluster resources (marketplace, subscriptions)
	resources.RegisterClusterResources(s.mcpServer, s.serviceClients, s.resolver, s.logger)

	// VOD resources
	resources.RegisterVODResources(s.mcpServer, s.serviceClients, s.logger)

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

	// Wallet bootstrap and linked-wallet management (authentication, never payment)
	tools.RegisterWalletTools(s.mcpServer, s.serviceClients, s.logger)

	// Payment tools (authenticated tenant top-up and balance)
	tools.RegisterPaymentTools(s.mcpServer, s.serviceClients, s.resolver, s.preflightCheck, s.logger)

	// Billing tools (require billing details)
	tools.RegisterBillingTools(s.mcpServer, s.serviceClients, s.resolver, s.preflightCheck, s.logger)

	// Stream tools (require billing + balance)
	tools.RegisterStreamTools(s.mcpServer, s.serviceClients, s.resolver, s.preflightCheck, s.logger)

	// Clip tools (require billing + balance)
	tools.RegisterClipTools(s.mcpServer, s.serviceClients, s.resolver, s.preflightCheck, s.logger)

	// DVR tools (require billing + balance)
	tools.RegisterDVRTools(s.mcpServer, s.serviceClients, s.resolver, s.preflightCheck, s.logger)

	// Media retention tools (cost-affecting; storage policy + per-asset overrides)
	tools.RegisterRetentionTools(s.mcpServer, s.serviceClients, s.resolver, s.preflightCheck, s.logger)

	// Playback access tester (sensitive; accepts JWTs, may fire webhooks)
	tools.RegisterPlaybackAccessTestTools(s.mcpServer, s.serviceClients, s.resolver, s.preflightCheck, s.logger)

	// Playback auth management (sensitive; create_signing_key returns one-shot private material)
	tools.RegisterPlaybackAuthTools(s.mcpServer, s.serviceClients, s.resolver, s.logger)

	// Multistream push targets (cost-affecting)
	tools.RegisterMultistreamTools(s.mcpServer, s.serviceClients, s.resolver, s.preflightCheck, s.logger)

	// Cluster invite + subscription request flows
	tools.RegisterClusterSubscriptionTools(s.mcpServer, s.serviceClients, s.resolver, s.preflightCheck, s.logger)

	// Playback tools (free)
	tools.RegisterPlaybackTools(s.mcpServer, s.serviceClients, s.resolver, s.preflightCheck, s.logger)

	// VOD tools (require billing + balance for upload)
	tools.RegisterVODTools(s.mcpServer, s.serviceClients, s.resolver, s.preflightCheck, s.logger)

	// Infrastructure tools (marketplace, cluster lifecycle)
	tools.RegisterInfrastructureTools(s.mcpServer, s.serviceClients, s.resolver, s.preflightCheck, s.logger)

	// QoE diagnostic tools (for video consultant)
	tools.RegisterQoETools(s.mcpServer, s.serviceClients, s.resolver, s.preflightCheck, s.logger)

	// Support tools (search history)
	tools.RegisterSupportTools(s.mcpServer, s.serviceClients, s.resolver, s.preflightCheck, s.logger)

	// API integration assistant tools (schema introspection, query generation)
	tools.RegisterAPIAssistantTools(s.mcpServer, s.serviceClients, s.resolver, s.preflightCheck, s.logger)

	// Skipper proxy tools (knowledge search, web search — forwarded to Skipper spoke)
	tools.RegisterSkipperTools(s.mcpServer, s.skipperClient, s.logger)
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
			SessionTimeout: mcpSessionTimeout,
		},
	)

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !validMCPOrigin(r, s.originAllowed) {
			http.Error(w, "MCP origin not allowed", http.StatusForbidden)
			return
		}
		if r.Body != nil {
			r.Body = http.MaxBytesReader(w, r.Body, maxMCPRequestBytes)
		}
		ctx := s.extractAuthContext(r)
		r = r.WithContext(ctx)

		if authType := ctxkeys.GetAuthType(ctx); authType == "wallet" {
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

func validMCPOrigin(r *http.Request, allowed func(string) bool) bool {
	if r == nil {
		return false
	}
	origin := strings.TrimSpace(r.Header.Get("Origin"))
	if origin == "" {
		return true
	}
	parsed, err := url.Parse(origin)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" || parsed.User != nil || parsed.Path != "" || parsed.RawQuery != "" || parsed.Fragment != "" {
		return false
	}
	if allowed != nil {
		return allowed(origin)
	}
	return strings.EqualFold(parsed.Host, r.Host)
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
		// Account needs setup. Session-level notifications are handled by the SDK,
		// so the server only logs the condition here.
		s.logger.WithField("tenant_id", tenantID).
			WithField("blockers", len(blockers)).
			Info("MCP client connected with account setup required")
	}
}

// registerAccessMiddleware enforces auth, x402 settlement, rate limits, and usage tracking.
// Dependencies (rateLimiter, tenantCache) are validated by NewServer.
func (s *Server) registerAccessMiddleware() {
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
			// rateLimitTenantID decouples the rate-limit bucket from the billing
			// tenant when they differ (playback resolves to the stream OWNER for
			// billing, but must be throttled on the CALLER's identity).
			var rateLimitTenantID *string
			if opName == "mcp:tools/call:resolve_playback_endpoint" {
				if raw := extractPlaybackContentID(req.GetParams()); raw != "" {
					// Normalize once (Relay/global IDs → canonical playback_id) and share
					// it with the handler via ctx. Owner attribution and viewer:// x402
					// key off the canonical playback_id, not the raw (possibly Relay) input.
					playbackID, ownerTenantID, nErr := tools.NormalizePlaybackContent(ctx, raw, s.serviceClients)
					if nErr == nil && playbackID != "" {
						ctx = context.WithValue(ctx, ctxkeys.KeyPlaybackContentID, playbackID)
						resourcePath = "viewer://" + playbackID
						if ownerTenantID != "" {
							// Billing switches to the content owner; the rate-limit bucket
							// stays the caller's so an anonymous viewer can't exhaust the
							// owner's tenant bucket (see mcpAccessIdentity).
							tenantID, rateLimitTenantID = mcpAccessIdentity(tenantID, ownerTenantID)
							ctx = context.WithValue(ctx, ctxkeys.KeyTenantID, tenantID)
						}
					}
				}
			}

			publicAllowlisted := isPublicMCPOperation(opName)

			// Only true protocol-metadata operations (initialize, list/get) bypass
			// access control entirely — they carry no billable work and predate auth.
			// Public *tool/resource* calls (payment, playback, marketplace) still flow
			// through EvaluateAccess so the per-IP public rate limit applies to them,
			// same as the public GraphQL surface.
			if tenantID == "" && isPublicMCPMetadataOperation(opName) {
				result, err := next(ctx, method, req)
				if err == nil {
					result = filterMCPDiscovery(ctx, method, result, s.skipperClient)
				}
				return result, err
			}

			if method == "tools/call" {
				toolName := strings.TrimPrefix(opName, "mcp:tools/call:")
				if err := authorizeMCPTool(ctx, toolName); err != nil {
					mcpToolDenialsTotal.WithLabelValues(toolName, "scope").Inc()
					return nil, err
				}
			}
			if method == "resources/read" {
				if err := authorizeMCPResource(ctx, mcpReadResourceURI(req.GetParams())); err != nil {
					return nil, err
				}
			}
			if xPayment != "" && method == "tools/call" && isSideEffectingMCPCall(opName) {
				toolName := strings.TrimPrefix(opName, "mcp:tools/call:")
				strategy, registered := accesspolicy.MCPX402MutationStrategy(toolName)
				if registered && strategy != accesspolicy.X402OwnerIdempotency {
					return nil, &jsonrpc.Error{
						Code: -32015, Message: "x402 direct mutation execution unsupported",
						Data: json.RawMessage(`{"code":"X402_MUTATION_DIRECT_EXECUTION_UNSUPPORTED","message":"use submit_payment to top up, then retry without the payment header"}`),
					}
				}
			}

			decision := middleware.EvaluateAccess(ctx, middleware.AccessRequest{
				TenantID:          tenantID,
				RateLimitTenantID: rateLimitTenantID,
				ClientIP:          clientIP,
				Path:              resourcePath,
				OperationName:     opName,
				XPayment:          xPayment,
				PublicAllowlisted: publicAllowlisted,
			}, s.rateLimiter, s.tenantCache.GetLimitsFunc(), s.tenantCache, s.serviceClients.Purser, s.serviceClients.Purser, s.serviceClients.Commodore, s.logger)

			if !decision.Allowed {
				if method == "tools/call" {
					mcpToolDenialsTotal.WithLabelValues(strings.TrimPrefix(opName, "mcp:tools/call:"), accessDenialReason(decision.Status)).Inc()
				}
				s.logger.WithField("operation", opName).
					WithField("tenant_id", tenantID).
					WithField("auth_type", deriveAuthType(ctx)).
					WithField("status", decision.Status).
					WithField("duration_ms", time.Since(start).Milliseconds()).
					Warn("MCP request denied")
				return nil, accessDecisionError(decision)
			}

			var result mcp.Result
			var err error
			if decision.X402Settled && method == "tools/call" && isSideEffectingMCPCall(opName) {
				result, err = s.executePaidMCPMutation(ctx, method, opName, tenantID, xPayment, req, next)
			} else {
				result, err = next(ctx, method, req)
			}
			if err == nil {
				result = filterMCPDiscovery(ctx, method, result, s.skipperClient)
			}
			if err == nil && method == "tools/call" {
				result = enforceMCPResultLimit(strings.TrimPrefix(opName, "mcp:tools/call:"), result)
			}
			if err == nil && method == "resources/read" && mcpEncodedResultBytes(result) > maxMCPResultBytes {
				resourcePolicy, _ := resourcePolicyForURI(mcpReadResourceURI(req.GetParams()))
				mcpResourceDenialsTotal.WithLabelValues(resourcePolicy.Scope, "result_too_large").Inc()
				result = nil
				err = mcpResultTooLargeError()
			}
			if method == "tools/call" {
				toolName := strings.TrimPrefix(opName, "mcp:tools/call:")
				policy, _ := tools.ToolPolicyForName(toolName)
				outcome := "success"
				if err != nil {
					outcome = "protocol_error"
				} else if toolResult, ok := result.(*mcp.CallToolResult); ok && toolResult != nil && toolResult.IsError {
					outcome = "tool_error"
				}
				mcpToolCallsTotal.WithLabelValues(toolName, string(policy.Risk), outcome).Inc()
				mcpToolDurationSeconds.WithLabelValues(toolName, string(policy.Risk)).Observe(time.Since(start).Seconds())
				mcpToolResultBytes.WithLabelValues(toolName).Observe(float64(mcpEncodedResultBytes(result)))
				entry := s.logger.WithField("operation", opName).
					WithField("tenant_id", ctxkeys.GetTenantID(ctx)).
					WithField("user_id", ctxkeys.GetUserID(ctx)).
					WithField("auth_type", deriveAuthType(ctx)).
					WithField("duration_ms", time.Since(start).Milliseconds())
				if err != nil {
					entry.WithError(err).Warn("MCP tool call failed")
				} else if toolResult, ok := result.(*mcp.CallToolResult); ok && toolResult != nil && toolResult.IsError {
					entry.WithField("result_bytes", len(mcpTextResult(toolResult))).Warn("MCP tool call returned tool error")
				} else {
					entry.WithField("result_bytes", mcpResultTextBytes(result)).Info("MCP tool call completed")
				}
			}
			if s.usageTracker != nil {
				durationMs := uint64(time.Since(start).Milliseconds())
				authType := deriveAuthType(ctx)
				userID := ctxkeys.GetUserID(ctx)
				tenantID := ctxkeys.GetTenantID(ctx)
				if tenantID == "" {
					tenantID = tenants.AnonymousTenantID.String()
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

type durableMCPMutationResult struct {
	Texts             []string        `json:"texts,omitempty"`
	StructuredContent json.RawMessage `json:"structured_content,omitempty"`
	IsError           bool            `json:"is_error,omitempty"`
	HandlerError      string          `json:"handler_error,omitempty"`
}

func isSideEffectingMCPCall(opName string) bool {
	tool := strings.TrimPrefix(opName, "mcp:tools/call:")
	for _, prefix := range []string{
		"get_", "list_", "browse_", "search_", "diagnose_", "introspect_",
		"generate_", "test_", "resolve_", "ask_", "check_",
	} {
		if strings.HasPrefix(tool, prefix) {
			return false
		}
	}
	return tool != "" && tool != opName && tool != "submit_payment"
}

func mcpMutationIdempotencyKey(req mcp.Request) string {
	if extra := req.GetExtra(); extra != nil && extra.Header != nil {
		if value := strings.TrimSpace(extra.Header.Get("Idempotency-Key")); value != "" {
			return value
		}
	}
	if params := req.GetParams(); params != nil {
		meta := params.GetMeta()
		for _, name := range []string{"idempotencyKey", "idempotency_key", "idempotency-key"} {
			if value, ok := meta[name].(string); ok && strings.TrimSpace(value) != "" {
				return strings.TrimSpace(value)
			}
		}
	}
	return ""
}

func mcpMutationFingerprint(method, operation string, params mcp.Params) (string, error) {
	payload, err := json.Marshal(params)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(append([]byte(method+"\x00"+operation+"\x00"), payload...))
	return fmt.Sprintf("%x", digest[:]), nil
}

func encodeDurableMCPResult(result mcp.Result, handlerErr error) ([]byte, error) {
	envelope := durableMCPMutationResult{}
	if handlerErr != nil {
		envelope.HandlerError = handlerErr.Error()
	}
	if toolResult, ok := result.(*mcp.CallToolResult); ok && toolResult != nil {
		envelope.IsError = toolResult.IsError
		for _, content := range toolResult.Content {
			if text, ok := content.(*mcp.TextContent); ok {
				envelope.Texts = append(envelope.Texts, text.Text)
			}
		}
		if toolResult.StructuredContent != nil {
			structured, err := json.Marshal(toolResult.StructuredContent)
			if err != nil {
				return nil, err
			}
			envelope.StructuredContent = structured
		}
	}
	return json.Marshal(envelope)
}

func decodeDurableMCPResult(payload []byte) (mcp.Result, error) {
	var envelope durableMCPMutationResult
	if err := json.Unmarshal(payload, &envelope); err != nil {
		return nil, fmt.Errorf("decode stored MCP mutation result: %w", err)
	}
	if envelope.HandlerError != "" {
		return nil, errors.New(envelope.HandlerError)
	}
	result := &mcp.CallToolResult{IsError: envelope.IsError}
	for _, value := range envelope.Texts {
		result.Content = append(result.Content, &mcp.TextContent{Text: value})
	}
	if len(envelope.StructuredContent) > 0 {
		var structured any
		if err := json.Unmarshal(envelope.StructuredContent, &structured); err != nil {
			return nil, fmt.Errorf("decode stored MCP structured result: %w", err)
		}
		result.StructuredContent = structured
	}
	return result, nil
}

func (s *Server) executePaidMCPMutation(ctx context.Context, method, operation, tenantID, payment string, req mcp.Request, next mcp.MethodHandler) (mcp.Result, error) {
	key := mcpMutationIdempotencyKey(req)
	if len(key) < 8 || len(key) > 255 {
		return nil, &jsonrpc.Error{
			Code: -32010, Message: "idempotency key required",
			Data: json.RawMessage(`{"code":"IDEMPOTENCY_KEY_REQUIRED","message":"paid MCP mutations require Idempotency-Key or _meta.idempotencyKey containing 8-255 characters"}`),
		}
	}
	payload, err := middleware.ParseX402PaymentHeader(payment)
	if err != nil || payload.GetQuoteId() == "" {
		return nil, &jsonrpc.Error{Code: -32011, Message: "paid mutation quote missing"}
	}
	fingerprint, err := mcpMutationFingerprint(method, operation, req.GetParams())
	if err != nil {
		return nil, err
	}
	claimCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	claim, err := s.serviceClients.Purser.ClaimX402MutationResult(claimCtx, &purserpb.ClaimX402MutationResultRequest{
		TenantId: tenantID, QuoteId: payload.GetQuoteId(), IdempotencyKey: key,
		RequestFingerprint: fingerprint, Protocol: "mcp", Operation: operation,
	})
	cancel()
	if err != nil {
		return nil, &jsonrpc.Error{Code: -32012, Message: "paid mutation idempotency rejected", Data: json.RawMessage(fmt.Sprintf("%q", err.Error()))}
	}
	switch claim.GetState() {
	case "completed":
		return decodeDurableMCPResult(claim.GetResult())
	case "in_progress":
		return nil, &jsonrpc.Error{
			Code: -32013, Message: "paid mutation in progress",
			Data: json.RawMessage(`{"code":"PAID_MUTATION_IN_PROGRESS","retry_after_seconds":2}`),
		}
	case "operator_review":
		return nil, &jsonrpc.Error{
			Code: -32016, Message: "paid mutation requires operator review",
			Data: json.RawMessage(`{"code":"PAID_MUTATION_OPERATOR_REVIEW","message":"do not execute the mutation again"}`),
		}
	case "claimed":
		result, handlerErr := next(ctx, method, req)
		encoded, encodeErr := encodeDurableMCPResult(result, handlerErr)
		if encodeErr != nil {
			return result, handlerErr
		}
		if len(encoded) > maxMCPResultBytes {
			encoded = []byte(`{"texts":["The original mutation completed but its response exceeded the durable replay limit; inspect the resource state."],"is_error":true}`)
		}
		completeErr := completePaidMCPMutationResult(s.serviceClients.Purser, &purserpb.CompleteX402MutationResultRequest{
			TenantId: tenantID, QuoteId: payload.GetQuoteId(), IdempotencyKey: key,
			RequestFingerprint: fingerprint, Result: encoded,
			ContentType: "application/json", StatusCode: http.StatusOK,
		})
		if completeErr != nil && s.logger != nil {
			s.logger.WithError(completeErr).WithField("operation", operation).Error("Failed to persist paid MCP mutation result after retries; claim will enter operator review")
		}
		return result, handlerErr
	default:
		return nil, &jsonrpc.Error{Code: -32014, Message: "invalid paid mutation claim state"}
	}
}

type mutationResultCompleter interface {
	CompleteX402MutationResult(context.Context, *purserpb.CompleteX402MutationResultRequest) (*purserpb.CompleteX402MutationResultResponse, error)
}

func completePaidMCPMutationResult(store mutationResultCompleter, req *purserpb.CompleteX402MutationResultRequest) error {
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		completeCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		_, lastErr = store.CompleteX402MutationResult(completeCtx, req)
		cancel()
		if lastErr == nil {
			return nil
		}
	}
	return lastErr
}

func mcpResultTextBytes(result mcp.Result) int {
	if toolResult, ok := result.(*mcp.CallToolResult); ok && toolResult != nil {
		return len(mcpTextResult(toolResult))
	}
	return 0
}

func enforceMCPResultLimit(toolName string, result mcp.Result) mcp.Result {
	if result == nil {
		return result
	}
	encoded, err := json.Marshal(result)
	if err != nil || len(encoded) <= maxMCPResultBytes {
		return result
	}
	mcpToolDenialsTotal.WithLabelValues(toolName, "result_too_large").Inc()
	payload := map[string]any{
		"code":      "RESULT_TOO_LARGE",
		"message":   "The result exceeded the MCP response limit. Narrow the query or request a smaller page.",
		"max_bytes": maxMCPResultBytes,
	}
	text, err := json.Marshal(payload)
	if err != nil {
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: `{"code":"RESULT_TOO_LARGE"}`}}, IsError: true}
	}
	return &mcp.CallToolResult{
		Content:           []mcp.Content{&mcp.TextContent{Text: string(text)}},
		StructuredContent: payload,
		IsError:           true,
	}
}

func mcpResultTooLargeError() error {
	payload := map[string]any{
		"code":      "RESULT_TOO_LARGE",
		"message":   "The result exceeded the MCP response limit. Narrow the request or use the direct API.",
		"max_bytes": maxMCPResultBytes,
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return &jsonrpc.Error{Code: -32016, Message: "result too large"}
	}
	return &jsonrpc.Error{Code: -32016, Message: "result too large", Data: data}
}

func mcpEncodedResultBytes(result mcp.Result) int {
	encoded, err := json.Marshal(result)
	if err != nil {
		return 0
	}
	return len(encoded)
}

func accessDenialReason(status int) string {
	switch status {
	case http.StatusUnauthorized:
		return "authentication"
	case http.StatusPaymentRequired:
		return "payment"
	case http.StatusTooManyRequests:
		return "rate_limit"
	default:
		return "access"
	}
}

func mcpTextResult(result *mcp.CallToolResult) string {
	if result == nil {
		return ""
	}
	var parts []string
	for _, content := range result.Content {
		if text, ok := content.(*mcp.TextContent); ok {
			parts = append(parts, text.Text)
		}
	}
	return strings.Join(parts, "\n")
}

func filterSkipperTools(ctx context.Context, result mcp.Result, skipper tools.SkipperCaller) mcp.Result {
	if result == nil || skipper == nil {
		return result
	}
	listResult, ok := result.(*mcp.ListToolsResult)
	if !ok || listResult == nil {
		return result
	}
	availability, ok := skipper.(skipperToolAvailability)
	if !ok || availability.ToolsAvailable(ctx) {
		return result
	}
	filtered := make([]*mcp.Tool, 0, len(listResult.Tools))
	for _, tool := range listResult.Tools {
		if tool == nil {
			continue
		}
		if tool.Name == "search_knowledge" || tool.Name == "search_web" || tool.Name == "ask_consultant" {
			continue
		}
		filtered = append(filtered, tool)
	}
	listResult.Tools = filtered
	return listResult
}

func filterMCPDiscovery(ctx context.Context, method string, result mcp.Result, skipper tools.SkipperCaller) mcp.Result {
	switch method {
	case "tools/list":
		result = filterToolsByPolicy(ctx, result)
		return filterSkipperTools(ctx, result, skipper)
	case "resources/list":
		return filterResourcesByPolicy(ctx, result)
	case "resources/templates/list":
		return filterResourceTemplatesByPolicy(ctx, result)
	default:
		return result
	}
}

func mcpReadResourceURI(params mcp.Params) string {
	if read, ok := params.(*mcp.ReadResourceParams); ok && read != nil {
		return read.URI
	}
	return ""
}

func filterToolsByPolicy(ctx context.Context, result mcp.Result) mcp.Result {
	listResult, ok := result.(*mcp.ListToolsResult)
	if !ok || listResult == nil {
		return result
	}
	filtered := make([]*mcp.Tool, 0, len(listResult.Tools))
	for _, tool := range listResult.Tools {
		if tool == nil {
			continue
		}
		policy, exists := tools.ToolPolicyForName(tool.Name)
		if !exists {
			continue
		}
		if canUseMCPTool(ctx, policy) {
			filtered = append(filtered, tool)
		}
	}
	listResult.Tools = filtered
	return listResult
}

func authorizeMCPTool(ctx context.Context, toolName string) error {
	policy, ok := tools.ToolPolicyForName(toolName)
	if !ok {
		return &jsonrpc.Error{Code: jsonrpc.CodeInternalError, Message: "tool security policy missing"}
	}
	if policy.Public && ctxkeys.GetAuthType(ctx) != "api_token" {
		return nil
	}
	for _, scope := range requiredMCPScopes(ctx, policy) {
		if err := middleware.RequirePermission(ctx, scope); err != nil {
			data, marshalErr := json.Marshal(map[string]any{"code": "INSUFFICIENT_SCOPE", "required_scope": scope})
			if marshalErr != nil {
				return &jsonrpc.Error{Code: -32003, Message: "insufficient permissions"}
			}
			return &jsonrpc.Error{Code: -32003, Message: "insufficient permissions", Data: data}
		}
	}
	return nil
}

func canUseMCPTool(ctx context.Context, policy tools.ToolPolicy) bool {
	if policy.Public && ctxkeys.GetAuthType(ctx) != "api_token" {
		return true
	}
	for _, scope := range requiredMCPScopes(ctx, policy) {
		if !middleware.HasPermission(ctx, scope) {
			return false
		}
	}
	return true
}

func requiredMCPScopes(ctx context.Context, policy tools.ToolPolicy) []string {
	scopes := []string{policy.Scope}
	if policy.Risk == tools.ToolRiskHigh && ctxkeys.GetAuthType(ctx) == "api_token" {
		scopes = append(scopes, "mcp:high-risk")
	}
	return scopes
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
		message = "not authenticated"
	}

	payload := map[string]any{}
	if decision.Status == http.StatusUnauthorized {
		payload["resource_metadata"] = mcperrors.ResourceMetadataURL
	}
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
	if isPublicMCPMetadataOperation(opName) {
		return true
	}
	switch opName {
	case "mcp:tools/call:resolve_playback_endpoint",
		"mcp:tools/call:browse_marketplace",
		"mcp:tools/call:request_wallet_challenge",
		"mcp:resources/read:account://status",
		"mcp:resources/read:billing://pricing",
		"mcp:resources/read:clusters://marketplace":
		return true
	default:
		return false
	}
}

// isPublicMCPMetadataOperation is the subset of public operations that carry no
// billable work and may skip access control entirely (protocol discovery). Public
// tool/resource *calls* are deliberately excluded so they are rate-limited.
func isPublicMCPMetadataOperation(opName string) bool {
	switch opName {
	case "mcp:tools/list",
		"mcp:resources/list",
		"mcp:resources/templates/list",
		"mcp:prompts/list",
		"mcp:prompts/get",
		"mcp:initialize":
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

// mcpAccessIdentity decides the billing tenant and the caller's rate-limit identity
// for a playback resolve that maps to a stream owner. Billing switches to the owner
// (so a delinquent owner's streams don't play), while the rate-limit bucket stays
// the caller's — an empty caller means anonymous → per-IP bucket — so an anonymous
// viewer cannot exhaust the owner's tenant bucket. When no owner is resolved the
// caller remains the billing tenant with no decoupling (nil).
func mcpAccessIdentity(callerTenantID, ownerTenantID string) (billingTenantID string, rateLimitTenantID *string) {
	if ownerTenantID == "" {
		return callerTenantID, nil
	}
	caller := callerTenantID
	return ownerTenantID, &caller
}
