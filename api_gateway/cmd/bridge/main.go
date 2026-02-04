package main

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"frameworks/api_gateway/graph"
	"frameworks/api_gateway/graph/generated"
	"frameworks/api_gateway/internal/clients"
	gatewayerrors "frameworks/api_gateway/internal/errors"
	"frameworks/api_gateway/internal/handlers"
	mcpserver "frameworks/api_gateway/internal/mcp"
	"frameworks/api_gateway/internal/middleware"
	"frameworks/api_gateway/internal/resolvers"
	"frameworks/api_gateway/internal/webhooks"
	pkgauth "frameworks/pkg/auth"
	"frameworks/pkg/config"
	"frameworks/pkg/ctxkeys"
	"frameworks/pkg/logging"
	"frameworks/pkg/monitoring"
	pb "frameworks/pkg/proto"
	"frameworks/pkg/server"
	"frameworks/pkg/tenants"
	"frameworks/pkg/version"

	"github.com/99designs/gqlgen/graphql"
	"github.com/99designs/gqlgen/graphql/handler"
	"github.com/99designs/gqlgen/graphql/handler/extension"
	"github.com/99designs/gqlgen/graphql/handler/transport"
	"github.com/99designs/gqlgen/graphql/playground"
	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
	"github.com/vektah/gqlparser/v2/ast"
)

func main() {
	// Setup logger
	logger := logging.NewLoggerWithService("bridge")

	// Load environment variables
	config.LoadEnv(logger)

	logger.Info("Starting Bridge GraphQL Gateway")

	skillMarkdown, skillJSON := loadSkillFiles(logger)

	// Initialize service clients (all gRPC-based)
	serviceToken := config.RequireEnv("SERVICE_TOKEN")
	jwtSecret := config.RequireEnv("JWT_SECRET")
	serviceClients, err := clients.NewServiceClients(clients.Config{
		ServiceToken: serviceToken,
		Logger:       logger,
	})
	if err != nil {
		logger.WithError(err).Fatal("Failed to initialize service clients")
	}

	// Initialize auth handlers (gRPC-based)
	authHandlers := handlers.NewAuthHandlers(serviceClients.Commodore, logger)

	// Initialize rate limiter with tenant cache (fetches limits from Quartermaster)
	rateLimiter := middleware.NewRateLimiter(middleware.RateLimitConfig{
		Logger: logger,
	})
	defer rateLimiter.Stop()

	tenantCache := middleware.NewTenantCache(serviceClients.Quartermaster, logger)

	usageHashSecret := config.GetEnv("USAGE_HASH_SECRET", "")
	middleware.InitHasher(usageHashSecret)
	if usageHashSecret == "" {
		logger.Warn("USAGE_HASH_SECRET not set; using legacy FNV-1a hashing for usage identifiers")
	}

	// Initialize usage tracker for API request analytics
	usageTracker := middleware.NewUsageTracker(middleware.UsageTrackerConfig{
		Decklog:    serviceClients.Decklog,
		Logger:     logger,
		SourceNode: config.GetEnv("HOSTNAME", "bridge"),
	})
	defer usageTracker.Stop()

	trustedProxies, invalidProxies := middleware.ParseTrustedProxies(config.GetEnv("TRUSTED_PROXY_CIDRS", ""))
	if len(invalidProxies) > 0 {
		logger.WithField("invalid_entries", strings.Join(invalidProxies, ", ")).
			Warn("Ignoring invalid trusted proxy entries")
	}

	// Setup monitoring
	healthChecker := monitoring.NewHealthChecker("bridge", version.Version)
	metricsCollector := monitoring.NewMetricsCollector("bridge", version.Version, version.GitCommit)

	// Add health checks (all internal services are now gRPC)
	healthChecker.AddCheck("config", monitoring.ConfigurationHealthCheck(map[string]string{
		"JWT_SECRET":    jwtSecret,
		"SERVICE_TOKEN": serviceToken,
	}))

	// Create custom GraphQL metrics
	graphqlMetrics := &resolvers.GraphQLMetrics{
		Operations:           metricsCollector.NewCounter("graphql_operations_total", "Total GraphQL operations", []string{"operation", "status"}),
		Duration:             metricsCollector.NewHistogram("graphql_operation_duration_seconds", "GraphQL operation duration", []string{"operation"}, nil),
		WebSocketConnections: metricsCollector.NewGauge("websocket_connections_active", "Active WebSocket connections", []string{"tenant_id"}),
		WebSocketMessages:    metricsCollector.NewCounter("websocket_messages_total", "WebSocket messages", []string{"direction", "type"}),
		SubscriptionsActive:  metricsCollector.NewGauge("subscription_active_count", "Active GraphQL subscriptions", []string{"operation"}),
	}

	// Initialize GraphQL resolver and server
	resolver := graph.NewResolver(serviceClients, logger, graphqlMetrics, serviceToken)

	// Setup complexity functions for pagination-aware query cost calculation
	var complexity generated.ComplexityRoot
	graph.SetupComplexity(&complexity)

	// Create GraphQL server with WebSocket support for subscriptions
	gqlHandler := handler.New(generated.NewExecutableSchema(generated.Config{
		Resolvers:  resolver,
		Complexity: complexity,
	}))
	gqlHandler.SetErrorPresenter(gatewayerrors.ErrorPresenter(logger))

	// Enable introspection for developer API explorer
	gqlHandler.Use(extension.Introspection{})

	// Add query complexity limit to prevent expensive queries
	// Default 1000 matches Shopify's per-query limit with pagination-aware complexity
	complexityLimit := config.GetEnvInt("GRAPHQL_COMPLEXITY_LIMIT", 1000)
	if complexityLimit > 0 {
		gqlHandler.Use(extension.FixedComplexityLimit(complexityLimit))
		logger.WithField("limit", complexityLimit).Info("GraphQL complexity limit enabled")
	}

	// Add query depth limit to prevent deeply nested queries
	maxDepth := config.GetEnvInt("GRAPHQL_MAX_DEPTH", 10)
	if maxDepth > 0 {
		gqlHandler.AroundOperations(func(ctx context.Context, next graphql.OperationHandler) graphql.ResponseHandler {
			if !graphql.HasOperationContext(ctx) {
				return next(ctx)
			}
			opCtx := graphql.GetOperationContext(ctx)
			if opCtx.Doc == nil {
				return next(ctx)
			}
			depth := calculateQueryDepth(opCtx.Doc.Operations, opCtx.Doc.Fragments)
			if depth > maxDepth {
				return func(ctx context.Context) *graphql.Response {
					return graphql.ErrorResponse(ctx, "query exceeds maximum depth of %d (got %d)", maxDepth, depth)
				}
			}
			return next(ctx)
		})
		logger.WithField("max_depth", maxDepth).Info("GraphQL depth limit enabled")
	}

	gqlHandler.AroundResponses(func(ctx context.Context, next graphql.ResponseHandler) *graphql.Response {
		resp := next(ctx)
		if resp != nil {
			if ginCtx, ok := ctx.Value(ctxkeys.KeyGinContext).(*gin.Context); ok && ginCtx != nil {
				ginCtx.Set(string(ctxkeys.KeyGraphQLErrorCount), len(resp.Errors))
				if graphql.HasOperationContext(ctx) {
					if opCtx := graphql.GetOperationContext(ctx); opCtx.Operation != nil {
						ginCtx.Set(string(ctxkeys.KeyGraphQLOperationType), string(opCtx.Operation.Operation))
						ginCtx.Set(string(ctxkeys.KeyGraphQLOperationName), opCtx.Operation.Name)
					}
				}
				if stats := extension.GetComplexityStats(ctx); stats != nil {
					ginCtx.Set(string(ctxkeys.KeyGraphQLComplexity), stats.Complexity)
				}
			}
		}
		return resp
	})

	gqlHandler.AroundOperations(func(ctx context.Context, next graphql.OperationHandler) graphql.ResponseHandler {
		if !graphql.HasOperationContext(ctx) {
			return next(ctx)
		}
		opCtx := graphql.GetOperationContext(ctx)
		if opCtx.Operation != nil && opCtx.Operation.Operation == ast.Subscription {
			start := time.Now()
			tenantID, authType, userID, tokenHash := extractUsageContext(ctx)
			opName := opCtx.Operation.Name
			opType := string(opCtx.Operation.Operation)
			complexity := uint32(0)
			if stats := extension.GetComplexityStats(ctx); stats != nil {
				complexity = uint32(stats.Complexity)
			}
			go func(subCtx context.Context) {
				<-subCtx.Done()
				durationMs := time.Since(start).Milliseconds()
				usageTracker.Record(start, tenantID, authType, opType, opName, userID, tokenHash, uint64(durationMs), complexity, 0)
			}(ctx)
		}
		return next(ctx)
	})

	// Add transport options
	gqlHandler.AddTransport(transport.POST{})
	gqlHandler.AddTransport(transport.GET{})
	gqlHandler.AddTransport(transport.Websocket{
		KeepAlivePingInterval: 10 * time.Second,
		Upgrader: websocket.Upgrader{
			CheckOrigin: func(r *http.Request) bool {
				// Allow all origins in development - in production, restrict to specific domains
				return true
			},
		},
		InitFunc: func(ctx context.Context, initPayload transport.InitPayload) (context.Context, *transport.InitPayload, error) {
			// Try to get token from connectionParams first, then fall back to cookie in context
			var token string

			// 1. Try connectionParams.Authorization (for clients that can pass tokens)
			authHeader := initPayload.Authorization()
			if authHeader != "" {
				parts := strings.Split(authHeader, " ")
				if len(parts) == 2 && parts[0] == "Bearer" {
					token = parts[1]
				}
			}

			// 2. Fall back to cookie token passed via Gin context
			if token == "" {
				if cookieToken, ok := ctx.Value(ctxkeys.KeyWSCookieToken).(string); ok && cookieToken != "" {
					token = cookieToken
				}
			}

			// 3. Wallet auth via original HTTP request headers
			if token == "" {
				if req, ok := ctx.Value(ctxkeys.KeyHTTPRequest).(*http.Request); ok && req != nil {
					walletAddress := req.Header.Get("X-Wallet-Address")
					if walletAddress != "" {
						signature := req.Header.Get("X-Wallet-Signature")
						message := req.Header.Get("X-Wallet-Message")
						if signature != "" && message != "" {
							resp, walletErr := serviceClients.Commodore.WalletLogin(ctx, walletAddress, message, signature)
							if walletErr == nil && resp != nil && resp.User != nil {
								email := ""
								if resp.User.Email != nil {
									email = *resp.User.Email
								}
								ctx = context.WithValue(ctx, ctxkeys.KeyUserID, resp.User.Id)
								ctx = context.WithValue(ctx, ctxkeys.KeyTenantID, resp.User.TenantId)
								ctx = context.WithValue(ctx, ctxkeys.KeyEmail, email)
								ctx = context.WithValue(ctx, ctxkeys.KeyRole, resp.User.Role)
								ctx = context.WithValue(ctx, ctxkeys.KeyAuthType, "wallet")
								ctx = context.WithValue(ctx, ctxkeys.KeyWalletAddr, walletAddress)
								if resp.Token != "" {
									ctx = context.WithValue(ctx, ctxkeys.KeyJWTToken, resp.Token)
								}

								user := &middleware.UserContext{
									UserID:   resp.User.Id,
									TenantID: resp.User.TenantId,
									Email:    email,
									Role:     resp.User.Role,
								}
								ctx = context.WithValue(ctx, ctxkeys.KeyUser, user)
								return ctx, &initPayload, nil
							}
						}
					}
				}
			}

			if token != "" {
				// Try JWT validation
				claims, claimsErr := pkgauth.ValidateJWT(token, []byte(jwtSecret))
				if claimsErr == nil {
					ctx = context.WithValue(ctx, ctxkeys.KeyUserID, claims.UserID)
					ctx = context.WithValue(ctx, ctxkeys.KeyTenantID, claims.TenantID)
					ctx = context.WithValue(ctx, ctxkeys.KeyEmail, claims.Email)
					ctx = context.WithValue(ctx, ctxkeys.KeyRole, claims.Role)
					ctx = context.WithValue(ctx, ctxkeys.KeyJWTToken, token)
					ctx = context.WithValue(ctx, ctxkeys.KeyAuthType, "jwt")

					user := &middleware.UserContext{
						UserID:   claims.UserID,
						TenantID: claims.TenantID,
						Email:    claims.Email,
						Role:     claims.Role,
					}
					ctx = context.WithValue(ctx, ctxkeys.KeyUser, user)
				} else {
					// Try API Token via Commodore
					resp, apiErr := serviceClients.Commodore.ValidateAPIToken(ctx, token)
					if apiErr == nil && resp.Valid {
						ctx = context.WithValue(ctx, ctxkeys.KeyUserID, resp.UserId)
						ctx = context.WithValue(ctx, ctxkeys.KeyTenantID, resp.TenantId)
						ctx = context.WithValue(ctx, ctxkeys.KeyEmail, resp.Email)
						ctx = context.WithValue(ctx, ctxkeys.KeyRole, resp.Role)
						ctx = context.WithValue(ctx, ctxkeys.KeyAuthType, "api_token")
						ctx = context.WithValue(ctx, ctxkeys.KeyAPIToken, token)
						if resp.TokenId != "" {
							ctx = context.WithValue(ctx, ctxkeys.KeyAPITokenHash, middleware.HashIdentifier(resp.TokenId))
						} else {
							ctx = context.WithValue(ctx, ctxkeys.KeyAPITokenHash, middleware.HashIdentifier(token))
						}
						if len(resp.Permissions) > 0 {
							ctx = context.WithValue(ctx, ctxkeys.KeyPermissions, resp.Permissions)
						}

						user := &middleware.UserContext{
							UserID:      resp.UserId,
							TenantID:    resp.TenantId,
							Email:       resp.Email,
							Role:        resp.Role,
							Permissions: resp.Permissions,
						}
						ctx = context.WithValue(ctx, ctxkeys.KeyUser, user)
					}
				}
			}

			return ctx, &initPayload, nil
		},
	})

	// Setup router with unified monitoring
	app := server.SetupServiceRouter(logger, "bridge", healthChecker, metricsCollector)

	// Public API routes (no auth required)
	{
		app.GET("/status", func(c *gin.Context) {
			c.JSON(http.StatusOK, gin.H{
				"service": "bridge",
				"status":  "ready",
				"message": "GraphQL Gateway - Ready",
			})
		})

		app.GET("/skill.md", func(c *gin.Context) {
			if skillMarkdown == nil {
				c.JSON(http.StatusNotFound, gin.H{"error": "skill.md not available"})
				return
			}
			c.Header("Access-Control-Allow-Origin", "*")
			c.Data(http.StatusOK, "text/markdown; charset=utf-8", skillMarkdown)
		})

		app.GET("/skill.json", func(c *gin.Context) {
			if skillJSON == nil {
				c.JSON(http.StatusNotFound, gin.H{"error": "skill.json not available"})
				return
			}
			c.Header("Access-Control-Allow-Origin", "*")
			c.Data(http.StatusOK, "application/json; charset=utf-8", skillJSON)
		})

		// MCP discovery endpoint (SEP-1649)
		app.GET("/.well-known/mcp.json", func(c *gin.Context) {
			c.Header("Access-Control-Allow-Origin", "*")
			docsURL := strings.TrimSpace(os.Getenv("DOCS_PUBLIC_URL"))
			docsURLValue := ""
			if docsURL != "" {
				docsURLValue = docsURL + "/streamers/mcp"
			}
			c.JSON(http.StatusOK, gin.H{
				"name":         "frameworks",
				"version":      version.Version,
				"title":        config.GetEnv("BRAND_NAME", "FrameWorks"),
				"description":  "Sovereign SaaS for live video",
				"mcp_endpoint": "/mcp",
				"transports":   []string{"streamable-http"},
				"auth": gin.H{
					"schemes":  []string{"bearer", "wallet"},
					"required": true,
				},
				"capabilities": gin.H{
					"tools":     true,
					"resources": true,
					"prompts":   true,
				},
				"docs_url": docsURLValue,
			})
		})
	}

	// Auth endpoints (gRPC to Commodore)
	auth := app.Group("/auth")
	{
		// Public auth endpoints (no auth required)
		auth.POST("/login", authHandlers.Login())
		auth.POST("/wallet-login", authHandlers.WalletLogin())
		auth.POST("/register", authHandlers.Register())
		auth.GET("/verify/:token", authHandlers.VerifyEmail())
		auth.POST("/resend-verification", authHandlers.ResendVerification())
		auth.POST("/refresh", authHandlers.RefreshToken())
		auth.POST("/forgot-password", authHandlers.ForgotPassword())
		auth.POST("/reset-password", authHandlers.ResetPassword())

		// Protected auth endpoints (require JWT from cookie or header)
		authProtected := auth.Group("", middleware.RequireJWTAuth([]byte(jwtSecret)))
		authProtected.POST("/logout", authHandlers.Logout())
		authProtected.GET("/me", authHandlers.GetMe())
		authProtected.PATCH("/me", authHandlers.UpdateMe())
		authProtected.GET("/me/newsletter", authHandlers.GetNewsletterStatus())
		authProtected.POST("/me/newsletter", authHandlers.UpdateNewsletter())
	}

	// Webhook routing - external payment provider webhooks forwarded to internal services via gRPC.
	// No auth middleware - signature verification happens in the target service.
	// Route pattern: /webhooks/{service}/{provider}
	webhookRouter := webhooks.NewRouter(logger)
	webhookRouter.RegisterService("billing", serviceClients.Purser) // Stripe, Mollie webhooks
	{
		app.POST("/webhooks/:service/:provider", webhookRouter.Handle)
		app.GET("/webhooks/health", webhookRouter.HandleHealth)
		logger.Info("Webhook router enabled at /webhooks/:service/:provider")
	}

	// GraphQL endpoint (single route group)
	graphqlGroup := app.Group("/graphql")
	graphqlGroup.Use(middleware.PublicOrJWTAuth([]byte(jwtSecret), serviceClients)) // Allowlist public queries or require auth
	graphqlGroup.Use(middleware.DemoModePostAuth(logger))                           // Demo mode detection (after auth)
	graphqlGroup.Use(middleware.ViewerX402Middleware(serviceClients, logger))       // Resolve viewer x402 before GraphQL executes

	// IMPORTANT: WebSocket upgrades may authenticate in the GraphQL WS InitFunc (connectionParams),
	// so rate limiting must not run before that auth has a chance to set tenant context.
	graphqlHTTP := graphqlGroup.Group("/")
	graphqlHTTP.Use(middleware.RateLimitMiddlewareWithX402(rateLimiter, tenantCache.GetLimitsFunc(), tenantCache, serviceClients.Purser, serviceClients.Purser, serviceClients.Commodore))
	graphqlHTTP.Use(middleware.GraphQLContextMiddleware())
	graphqlHTTP.Use(middleware.GraphQLAttachLoaders(serviceClients))
	graphqlHTTP.Use(middleware.UsageTrackerMiddleware(usageTracker))

	{
		graphqlHTTP.POST("/", gin.WrapH(gqlHandler))
		graphqlGroup.GET("/ws", func(c *gin.Context) {
			ctx := c.Request.Context()
			if cookieToken, cookieErr := c.Cookie("access_token"); cookieErr == nil && cookieToken != "" {
				ctx = context.WithValue(ctx, ctxkeys.KeyWSCookieToken, cookieToken)
			}
			ctx = context.WithValue(ctx, ctxkeys.KeyHTTPRequest, c.Request)
			c.Request = c.Request.WithContext(ctx)
			gqlHandler.ServeHTTP(c.Writer, c.Request)
		})
		// Enable playground based on explicit config or GIN_MODE (default: enabled in non-release mode)
		playgroundEnabled := config.GetEnvBool("GRAPHQL_PLAYGROUND_ENABLED", config.GetEnv("GIN_MODE", "debug") != "release")
		if playgroundEnabled {
			app.GET("/graphql/playground", gin.WrapH(playground.Handler("GraphQL Playground", "/graphql/")))
			logger.Info("GraphQL Playground enabled at /graphql/playground")
		}
	}

	// No separate public route; PublicOrJWTAuth handles allowlisted unauthenticated queries

	// MCP (Model Context Protocol) endpoint for AI agent access
	// Auth is handled inside the MCP server via request headers
	mcpServer, err := mcpserver.NewServer(mcpserver.Config{
		ServiceClients: serviceClients,
		Resolver:       resolver.Resolver,
		Logger:         logger,
		JWTSecret:      []byte(jwtSecret),
		ServiceToken:   serviceToken,
		RateLimiter:    rateLimiter,
		TenantCache:    tenantCache,
		UsageTracker:   usageTracker,
		TrustedProxies: trustedProxies,
	})
	if err != nil {
		logger.WithError(err).Fatal("Failed to initialize MCP server")
	}
	app.Any("/mcp", gin.WrapH(mcpServer.HTTPHandler()))
	app.Any("/mcp/*path", gin.WrapH(mcpServer.HTTPHandler()))
	logger.Info("MCP endpoint enabled at /mcp")

	// Use standard server startup with graceful shutdown
	serverConfig := server.DefaultConfig("bridge", "18000")

	// Best-effort service registration in Quartermaster (gRPC, before server starts)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		port, _ := strconv.Atoi(serverConfig.Port)
		healthEndpoint := "/health"
		advertiseHost := config.GetEnv("BRIDGE_HOST", "bridge")
		clusterID := config.GetEnv("CLUSTER_ID", "")
		if resp, err := serviceClients.Quartermaster.BootstrapService(ctx, &pb.BootstrapServiceRequest{
			Type:           "gateway",
			Version:        version.Version,
			Protocol:       "http",
			HealthEndpoint: &healthEndpoint,
			Port:           int32(port),
			AdvertiseHost:  &advertiseHost,
			ClusterId: func() *string {
				if clusterID != "" {
					return &clusterID
				}
				return nil
			}(),
		}); err != nil {
			logger.WithError(err).Warn("Quartermaster bootstrap (gateway) failed")
		} else {
			if resp != nil && resp.GetOwnerTenantId() != "" {
				usageTracker.SetServiceTenantID(resp.GetOwnerTenantId())
				logger.WithField("tenant_id", resp.GetOwnerTenantId()).Info("Usage tracker tenant owner set")
			}
			logger.Info("Quartermaster bootstrap (gateway) ok")
		}
	}()

	// Start server with standard graceful shutdown handling
	if err := server.Start(serverConfig, app, logger); err != nil {
		logger.Fatal("Failed to start server: " + err.Error())
	}

	// Shutdown the resolver to clean up WebSocket connections
	if err := resolver.Shutdown(); err != nil {
		logger.Error("Error shutting down resolver: " + err.Error())
	}
}

func extractUsageContext(ctx context.Context) (tenantID, authType, userID string, tokenHash uint64) {
	tenantID = ctxkeys.GetTenantID(ctx)
	if tenantID == "" {
		tenantID = tenants.AnonymousTenantID.String()
	}
	authType = ctxkeys.GetAuthType(ctx)
	if authType == "" {
		authType = "anonymous"
	}
	userID = ctxkeys.GetUserID(ctx)
	if v := ctx.Value(ctxkeys.KeyAPITokenHash); v != nil {
		switch t := v.(type) {
		case uint64:
			tokenHash = t
		case uint32:
			tokenHash = uint64(t)
		case int64:
			if t > 0 {
				tokenHash = uint64(t)
			}
		case int:
			if t > 0 {
				tokenHash = uint64(t)
			}
		}
	}
	return tenantID, authType, userID, tokenHash
}

func loadSkillFiles(logger logging.Logger) ([]byte, []byte) {
	candidates := []string{}
	if envDir := strings.TrimSpace(os.Getenv("SKILL_FILES_DIR")); envDir != "" {
		candidates = append(candidates, envDir)
	}
	candidates = append(candidates, "/app", "docs/skills")

	var markdownPath string
	var jsonPath string
	for _, candidate := range candidates {
		mdPath := filepath.Join(candidate, "skill.md")
		jsPath := filepath.Join(candidate, "skill.json")
		if _, err := os.Stat(mdPath); err == nil {
			if _, err := os.Stat(jsPath); err == nil {
				markdownPath = mdPath
				jsonPath = jsPath
				break
			}
		}
	}

	if markdownPath == "" || jsonPath == "" {
		logger.WithField("candidates", strings.Join(candidates, ", ")).Warn("skill files not found in any candidate directory")
		return nil, nil
	}

	markdown, err := os.ReadFile(markdownPath)
	if err != nil {
		logger.WithError(err).WithField("path", markdownPath).Warn("skill.md not found")
	}

	jsonData, err := os.ReadFile(jsonPath)
	if err != nil {
		logger.WithError(err).WithField("path", jsonPath).Warn("skill.json not found")
	}

	return markdown, jsonData
}

// calculateQueryDepth walks the GraphQL AST and returns the maximum selection depth.
// Depth is counted from field selections (not from operation root).
func calculateQueryDepth(operations ast.OperationList, fragments ast.FragmentDefinitionList) int {
	maxDepth := 0
	fragmentIndex := map[string]*ast.FragmentDefinition{}
	for _, fragment := range fragments {
		fragmentIndex[fragment.Name] = fragment
	}
	for _, op := range operations {
		if d := selectionSetDepth(op.SelectionSet, fragmentIndex, map[string]bool{}); d > maxDepth {
			maxDepth = d
		}
	}
	return maxDepth
}

func selectionSetDepth(set ast.SelectionSet, fragments map[string]*ast.FragmentDefinition, visited map[string]bool) int {
	maxDepth := 0
	for _, sel := range set {
		var childDepth int
		switch s := sel.(type) {
		case *ast.Field:
			if s.SelectionSet != nil {
				childDepth = 1 + selectionSetDepth(s.SelectionSet, fragments, visited)
			} else {
				childDepth = 1
			}
		case *ast.InlineFragment:
			childDepth = selectionSetDepth(s.SelectionSet, fragments, visited)
		case *ast.FragmentSpread:
			if visited[s.Name] {
				childDepth = 0
				break
			}
			fragment, ok := fragments[s.Name]
			if !ok {
				childDepth = 0
				break
			}
			visited[s.Name] = true
			childDepth = selectionSetDepth(fragment.SelectionSet, fragments, visited)
			delete(visited, s.Name)
		}
		if childDepth > maxDepth {
			maxDepth = childDepth
		}
	}
	return maxDepth
}
