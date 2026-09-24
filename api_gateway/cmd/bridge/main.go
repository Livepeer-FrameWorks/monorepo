package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"
	"time"

	"frameworks/api_gateway/graph"
	"frameworks/api_gateway/graph/generated"
	"frameworks/api_gateway/internal/appconfig"
	"frameworks/api_gateway/internal/clients"
	gatewayerrors "frameworks/api_gateway/internal/errors"
	"frameworks/api_gateway/internal/handlers"
	mcpserver "frameworks/api_gateway/internal/mcp"
	"frameworks/api_gateway/internal/middleware"
	"frameworks/api_gateway/internal/resolvers"
	"frameworks/api_gateway/internal/webhooks"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/cache"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/config"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/monitoring"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/qmbootstrap"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/server"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/tenants"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/version"

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
	if version.HandleCLI() {
		return
	}

	// Setup logger
	logger := logging.NewLoggerWithService("bridge")

	// Load environment variables
	config.LoadEnv(logger)

	logger.Info("Starting Bridge GraphQL Gateway")

	configOptions := config.Options{Service: "bridge", Logger: logger}
	cfg, err := config.Load[appconfig.Bridge](configOptions)
	if err != nil {
		logger.WithError(err).Fatal("Invalid configuration")
	}
	cfg.ApplyLogLevel(logger)
	liveConfig := config.NewLive(cfg, configOptions)
	serviceToken := cfg.ServiceToken
	jwtSecret := cfg.JWTSecret

	skills := loadSkillFiles(logger, cfg.SkillFilesDir, cfg.X402GasWalletAddress)

	// Initialize service clients (all gRPC-based)
	serviceClients, err := clients.NewServiceClients(serviceClientsConfig(cfg, logger))
	if err != nil {
		logger.WithError(err).Fatal("Failed to initialize service clients")
	}

	// Initialize auth handlers (gRPC-based)
	authHandlers := handlers.NewAuthHandlers(serviceClients.Commodore, logger, handlers.AuthConfig{
		CookieDomain: cfg.CookieDomain,
		Runtime: func() handlers.AuthRuntime {
			current := liveConfig.Get()
			return handlers.AuthRuntime{
				SecureCookies:   !current.IsDevelopment(),
				WebappPublicURL: current.WebappPublicURL,
			}
		},
	})

	// Initialize rate limiter with tenant cache (fetches limits from Quartermaster)
	rateLimiter := middleware.NewRateLimiter(middleware.RateLimitConfig{
		Logger: logger,
		Settings: func() middleware.AccessSettings {
			current := liveConfig.Get()
			return middleware.AccessSettings{
				PublicLimitPerMinute: current.PublicRateLimitPerMinute,
				PublicBurst:          current.PublicRateLimitBurst,
				DocsPublicURL:        current.DocsPublicURL,
			}
		},
	})
	defer rateLimiter.Stop()

	tenantCache := middleware.NewTenantCache(serviceClients.Quartermaster, logger)

	usageHashSecret := cfg.UsageHashSecret
	middleware.InitHasher(usageHashSecret)
	if usageHashSecret == "" {
		logger.Warn("USAGE_HASH_SECRET not set; using ephemeral random secret (hashes will not survive restarts)")
	}

	// Initialize usage tracker for API request analytics
	usageTracker := middleware.NewUsageTracker(middleware.UsageTrackerConfig{
		Decklog:    serviceClients.Decklog,
		Logger:     logger,
		SourceNode: cfg.SourceNode,
	})
	defer usageTracker.Stop()

	// Read through the live config so a SIGHUP env-file reload takes effect, and
	// so Bridge and Foghorn cannot drift apart on who a caller is after a change.
	trustedProxies := middleware.TrustedProxiesFromEnv(
		"TRUSTED_PROXY_CIDRS",
		func(string) string { return liveConfig.Get().TrustedProxyCIDRs },
		func(invalid []string) {
			logger.WithField("invalid_entries", strings.Join(invalid, ", ")).
				Warn("Ignoring invalid trusted proxy entries")
		},
	)

	originMatcher := middleware.NewOriginMatcher(cfg.AllowedOrigins, !cfg.Release())
	originAllowed := originMatcher.Allowed

	// Setup monitoring
	healthChecker := monitoring.NewHealthChecker("bridge", version.Version)
	metricsCollector := monitoring.NewMetricsCollector("bridge", version.Version, version.GitCommit)

	// Bridge owns no datastore and reaches every service lazily over gRPC, so
	// readiness reports serving until shutdown starts.
	readiness := monitoring.NewReadinessChecker("bridge", version.Version)

	// Create custom GraphQL metrics. signalman_streams_active tracks upstream
	// bridge→Signalman subscription streams per tenant (not browser WS);
	// subscription_active_count tracks served GraphQL subscriptions per
	// operation.
	graphqlMetrics := &resolvers.GraphQLMetrics{
		Operations:          metricsCollector.NewCounter("graphql_operations_total", "Total GraphQL operations", []string{"operation", "status"}),
		Duration:            metricsCollector.NewHistogram("graphql_operation_duration_seconds", "GraphQL operation duration", []string{"operation"}, nil),
		SignalmanStreams:    metricsCollector.NewGauge("signalman_streams_active", "Active bridge→Signalman upstream subscription streams", []string{"tenant_id"}),
		WebSocketMessages:   metricsCollector.NewCounter("websocket_messages_total", "WebSocket messages", []string{"direction", "type"}),
		SubscriptionsActive: metricsCollector.NewGauge("subscription_active_count", "Active GraphQL subscriptions", []string{"operation"}),
		CacheLoadsActive:    metricsCollector.NewGauge("cache_loads_active", "Active shared authority cache loads", []string{"service", "operation"}),
		CacheLoadTimeouts:   metricsCollector.NewCounter("cache_load_timeouts_total", "Shared authority cache loads terminated by their operation deadline", []string{"service", "operation"}),
	}

	// Initialize GraphQL resolver and server
	resolverCfg := resolverConfig(liveConfig)
	resolver := graph.NewResolver(serviceClients, logger, graphqlMetrics, resolverCfg)

	// Setup complexity functions for pagination-aware query cost calculation
	var complexity generated.ComplexityRoot
	graph.SetupComplexity(&complexity)

	// Create GraphQL server with WebSocket support for subscriptions
	gqlHandler := handler.New(generated.NewExecutableSchema(generated.Config{
		Resolvers:  resolver,
		Complexity: complexity,
	}))
	gqlHandler.SetErrorPresenter(gatewayerrors.ErrorPresenter(logger))
	gqlHandler.SetRecoverFunc(func(ctx context.Context, err any) error {
		logger.WithFields(logging.Fields{
			"panic":      err,
			"stacktrace": string(debug.Stack()),
		}).Error("Panic recovered in GraphQL resolver")
		return errors.New("internal error")
	})

	gqlHandler.AroundOperations(func(ctx context.Context, next graphql.OperationHandler) graphql.ResponseHandler {
		return next(resolvers.WithNodeHealthCache(ctx))
	})
	gqlHandler.AroundOperations(func(ctx context.Context, next graphql.OperationHandler) graphql.ResponseHandler {
		return next(resolvers.WithStoragePricingCache(ctx))
	})

	// Enable introspection for developer API explorer
	gqlHandler.Use(extension.Introspection{})

	// Add query complexity limit to prevent expensive queries. Cost is computed with
	// scalar/enum leaves valued at zero, so it reflects object structure and per-row
	// fetch work rather than how many cheap scalars a row projects (see graph package).
	complexityLimit := cfg.GraphQLComplexityLimit
	if complexityLimit > 0 {
		gqlHandler.Use(&graph.ScalarFreeComplexityLimit{
			Func: func(_ context.Context, opCtx *graphql.OperationContext) int {
				if isIntrospectionOperation(opCtx.Operation) {
					return math.MaxInt
				}
				return complexityLimit
			},
		})
		logger.WithField("limit", complexityLimit).Info("GraphQL complexity limit enabled")
	}

	// Add query depth limit to prevent deeply nested queries
	maxDepth := cfg.GraphQLMaxDepth
	if maxDepth > 0 {
		gqlHandler.AroundOperations(func(ctx context.Context, next graphql.OperationHandler) graphql.ResponseHandler {
			if !graphql.HasOperationContext(ctx) {
				return next(ctx)
			}
			opCtx := graphql.GetOperationContext(ctx)
			if opCtx.Doc == nil {
				return next(ctx)
			}
			if isIntrospectionOperation(opCtx.Operation) {
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

	// WebSocket upgrades pass the HTTP auth and rate-limit layers because their
	// connectionParams are processed later. Enforce the same public-operation
	// boundary after InitFunc has resolved identity, then charge the operation.
	gqlHandler.AroundOperations(middleware.GraphQLOperationAuth())
	gqlHandler.AroundOperations(middleware.GraphQLOperationAccess(tenantCache))
	gqlHandler.AroundOperations(middleware.GraphQLOperationRateLimit(rateLimiter, tenantCache.GetLimitsFunc()))

	gqlHandler.AroundResponses(func(ctx context.Context, next graphql.ResponseHandler) *graphql.Response {
		resp := next(ctx)
		if resp != nil {
			if ginCtx, ok := ctx.Value(ctxkeys.KeyGinContext).(*gin.Context); ok && ginCtx != nil {
				ginCtx.Set(string(ctxkeys.KeyGraphQLErrorCount), len(resp.Errors))
				if graphql.HasOperationContext(ctx) {
					if opCtx := graphql.GetOperationContext(ctx); opCtx.Operation != nil {
						ginCtx.Set(string(ctxkeys.KeyGraphQLOperationType), string(opCtx.Operation.Operation))
						ginCtx.Set(string(ctxkeys.KeyGraphQLOperationName), opCtx.Operation.Name)
						ginCtx.Set(string(ctxkeys.KeyGraphQLRootFields), middleware.GraphQLRootFields(ctx))
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
			rootFields := middleware.GraphQLRootFields(ctx)
			complexity := uint32(0)
			if stats := extension.GetComplexityStats(ctx); stats != nil {
				complexity = uint32(stats.Complexity)
			}
			go func(subCtx context.Context) {
				<-subCtx.Done()
				durationMs := time.Since(start).Milliseconds()
				usageTracker.Record(start, tenantID, authType, opType, opName, rootFields, userID, tokenHash, uint64(durationMs), complexity, 0)
			}(ctx)
		}
		return next(ctx)
	})

	gqlHandler.AroundOperations(func(ctx context.Context, next graphql.OperationHandler) graphql.ResponseHandler {
		return recoverOperation(ctx, next, logger)
	})

	// Add transport options
	gqlHandler.AddTransport(transport.POST{})
	gqlHandler.AddTransport(transport.GET{})
	gqlHandler.AddTransport(middleware.GraphQLWebsocketTransport(serviceClients, []byte(jwtSecret), logger, websocket.Upgrader{
		CheckOrigin: originMatcher.CheckWebsocketOrigin,
	}, 10*time.Second))

	// Setup router with unified monitoring
	app := server.NewServiceRouter(server.RouterSpec{
		Service:            "bridge",
		Logger:             logger,
		Health:             healthChecker,
		Ready:              readiness,
		Metrics:            metricsCollector,
		Runtime:            cfg.HTTPRuntime,
		DebugToken:         serviceToken,
		DebugConfig:        func() any { return liveConfig.Get() },
		DebugConfigOptions: configOptions,
	})

	// Public API routes (no auth required)
	{
		server.HandleOptionalTrailingSlash(app, http.MethodGet, "/status", func(c *gin.Context) {
			c.JSON(http.StatusOK, gin.H{
				"service": "bridge",
				"status":  "ready",
				"message": "GraphQL Gateway - Ready",
			})
		})

		server.HandleOptionalTrailingSlash(app, http.MethodGet, "/SKILL.md", func(c *gin.Context) {
			if skills.skillMD == nil {
				c.JSON(http.StatusNotFound, gin.H{"error": "SKILL.md not available"})
				return
			}
			c.Header("Access-Control-Allow-Origin", "*")
			c.Data(http.StatusOK, "text/markdown; charset=utf-8", skills.skillMD)
		})

		server.HandleOptionalTrailingSlash(app, http.MethodGet, "/heartbeat.md", func(c *gin.Context) {
			if skills.heartbeatMD == nil {
				c.JSON(http.StatusNotFound, gin.H{"error": "heartbeat.md not available"})
				return
			}
			c.Header("Access-Control-Allow-Origin", "*")
			c.Data(http.StatusOK, "text/markdown; charset=utf-8", skills.heartbeatMD)
		})

		server.HandleOptionalTrailingSlash(app, http.MethodGet, "/skill.json", func(c *gin.Context) {
			if skills.skillJSON == nil {
				c.JSON(http.StatusNotFound, gin.H{"error": "skill.json not available"})
				return
			}
			c.Header("Access-Control-Allow-Origin", "*")
			c.Data(http.StatusOK, "application/json; charset=utf-8", skills.skillJSON)
		})

		server.HandleOptionalTrailingSlash(app, http.MethodGet, "/llms.txt", func(c *gin.Context) {
			if skills.llmsTxt == nil {
				c.JSON(http.StatusNotFound, gin.H{"error": "llms.txt not available"})
				return
			}
			c.Header("Access-Control-Allow-Origin", "*")
			c.Data(http.StatusOK, "text/plain; charset=utf-8", skills.llmsTxt)
		})

		server.HandleOptionalTrailingSlash(app, http.MethodGet, "/robots.txt", func(c *gin.Context) {
			if skills.robotsTxt == nil {
				c.JSON(http.StatusNotFound, gin.H{"error": "robots.txt not available"})
				return
			}
			c.Data(http.StatusOK, "text/plain; charset=utf-8", skills.robotsTxt)
		})

		server.HandleOptionalTrailingSlash(app, http.MethodGet, "/.well-known/mcp.json", func(c *gin.Context) {
			if skills.mcpJSON == nil {
				c.JSON(http.StatusNotFound, gin.H{"error": "mcp.json not available"})
				return
			}
			c.Header("Access-Control-Allow-Origin", "*")
			c.Data(http.StatusOK, "application/json; charset=utf-8", skills.mcpJSON)
		})

		server.HandleOptionalTrailingSlash(app, http.MethodGet, "/.well-known/security.txt", func(c *gin.Context) {
			if skills.securityTxt == nil {
				c.JSON(http.StatusNotFound, gin.H{"error": "security.txt not available"})
				return
			}
			c.Data(http.StatusOK, "text/plain; charset=utf-8", skills.securityTxt)
		})

		server.HandleOptionalTrailingSlash(app, http.MethodGet, "/.well-known/oauth-protected-resource", func(c *gin.Context) {
			if skills.oauthPRM == nil {
				c.JSON(http.StatusNotFound, gin.H{"error": "oauth-protected-resource not available"})
				return
			}
			c.Header("Access-Control-Allow-Origin", "*")
			c.Data(http.StatusOK, "application/json; charset=utf-8", skills.oauthPRM)
		})

		server.HandleOptionalTrailingSlash(app, http.MethodGet, "/.well-known/did.json", func(c *gin.Context) {
			if skills.didJSON == nil {
				c.JSON(http.StatusNotFound, gin.H{"error": "did.json not available"})
				return
			}
			c.Header("Access-Control-Allow-Origin", "*")
			c.Data(http.StatusOK, "application/json; charset=utf-8", skills.didJSON)
		})
	}

	// Auth endpoints (gRPC to Commodore)
	auth := app.Group("/auth")
	{
		// Public auth endpoints (no auth required)
		server.HandleOptionalTrailingSlash(auth, http.MethodPost, "/login", authHandlers.Login())
		server.HandleOptionalTrailingSlash(auth, http.MethodPost, "/wallet-challenge",
			middleware.PublicOperationRateLimitMiddleware(rateLimiter, trustedProxies, "walletChallenge"),
			authHandlers.WalletChallenge())
		server.HandleOptionalTrailingSlash(auth, http.MethodPost, "/wallet-login",
			middleware.PublicOperationRateLimitMiddleware(rateLimiter, trustedProxies, "walletLogin"),
			authHandlers.WalletLogin())
		server.HandleOptionalTrailingSlash(auth, http.MethodPost, "/register", authHandlers.Register())
		server.HandleOptionalTrailingSlash(auth, http.MethodPost, "/verify",
			middleware.PublicOperationRateLimitMiddlewareWithLimits(rateLimiter, trustedProxies, "verifyEmail", 10, 5),
			authHandlers.VerifyEmail())
		server.HandleOptionalTrailingSlash(auth, http.MethodPost, "/resend-verification",
			middleware.PublicOperationRateLimitMiddlewareWithLimits(rateLimiter, trustedProxies, "resendVerification", 5, 5),
			authHandlers.ResendVerification())
		server.HandleOptionalTrailingSlash(auth, http.MethodPost, "/refresh", authHandlers.RefreshToken())
		server.HandleOptionalTrailingSlash(auth, http.MethodPost, "/forgot-password",
			middleware.PublicOperationRateLimitMiddlewareWithLimits(rateLimiter, trustedProxies, "forgotPassword", 5, 5),
			authHandlers.ForgotPassword())
		server.HandleOptionalTrailingSlash(auth, http.MethodPost, "/reset-password",
			middleware.PublicOperationRateLimitMiddlewareWithLimits(rateLimiter, trustedProxies, "resetPassword", 10, 5),
			authHandlers.ResetPassword())
		server.HandleOptionalTrailingSlash(auth, http.MethodGet, "/webapp-url", authHandlers.WebappURL())
		server.HandleOptionalTrailingSlash(auth, http.MethodGet, "/token/validate", middleware.PublicOrJWTAuth([]byte(jwtSecret), serviceClients), func(c *gin.Context) {
			c.JSON(http.StatusOK, gin.H{
				"valid":     true,
				"auth_type": ctxkeys.GetAuthType(c.Request.Context()),
				"user_id":   ctxkeys.GetUserID(c.Request.Context()),
				"tenant_id": ctxkeys.GetTenantID(c.Request.Context()),
			})
		})

		// Native-client browser-handoff (RFC 8252 + RFC 7636 PKCE) and
		// device-code grant (RFC 8628). Public endpoints called by the tray
		// loopback receiver and the CLI; the matching webapp consent
		// endpoints are protected and live in the authProtected group below.
		server.HandleOptionalTrailingSlash(auth, http.MethodPost, "/oauth/token", authHandlers.OAuthToken())
		server.HandleOptionalTrailingSlash(auth, http.MethodPost, "/device/start", authHandlers.DeviceStart())
		server.HandleOptionalTrailingSlash(auth, http.MethodPost, "/device/poll", authHandlers.DevicePoll())

		// Protected auth endpoints (require JWT from cookie or header)
		authProtected := auth.Group("", middleware.RequireJWTAuth([]byte(jwtSecret)))
		server.HandleOptionalTrailingSlash(authProtected, http.MethodPost, "/logout", authHandlers.Logout())
		server.HandleOptionalTrailingSlash(authProtected, http.MethodPost, "/authorize/complete", authHandlers.AuthorizeComplete())
		server.HandleOptionalTrailingSlash(authProtected, http.MethodPost, "/device/lookup", authHandlers.DeviceLookup())
		server.HandleOptionalTrailingSlash(authProtected, http.MethodPost, "/device/approve", authHandlers.DeviceApprove())
		server.HandleOptionalTrailingSlash(authProtected, http.MethodGet, "/me", authHandlers.GetMe())
		server.HandleOptionalTrailingSlash(authProtected, http.MethodPatch, "/me", authHandlers.UpdateMe())
		server.HandleOptionalTrailingSlash(authProtected, http.MethodGet, "/me/newsletter", authHandlers.GetNewsletterStatus())
		server.HandleOptionalTrailingSlash(authProtected, http.MethodPost, "/me/newsletter", authHandlers.UpdateNewsletter())
	}

	// Public infrastructure-node enrollment endpoint. Takes a bootstrap token
	// (validated server-side by Quartermaster) and a locally-generated
	// WireGuard public key; returns the assigned mesh identity and seed peer
	// set. Keeping this on Bridge means Quartermaster's gRPC listener stays
	// mesh-only — freshly-enrolled nodes reach the control plane through the
	// one externally-reachable Bridge surface.
	{
		infraBootstrap := handlers.NewInfrastructureBootstrapHandler(serviceClients.Quartermaster, logger)
		server.HandleOptionalTrailingSlash(app, http.MethodPost, "/v1/bootstrap/infrastructure-node", infraBootstrap.Handle)
	}

	// Authenticated immutable billing-document downloads. This route is outside
	// rated GraphQL admission so a zero-balance prepaid customer can always
	// retrieve invoices, receipts, and credit notes.
	{
		billingDocuments := handlers.NewBillingDocumentHandlers(serviceClients.Purser)
		billingDocumentRoutes := app.Group("/v1/billing/documents", middleware.RequireJWTAuth([]byte(jwtSecret)))
		server.HandleOptionalTrailingSlash(billingDocumentRoutes, http.MethodGet, "", billingDocuments.List())
		server.HandleOptionalTrailingSlash(billingDocumentRoutes, http.MethodGet, "/:kind/:id", billingDocuments.Download())
	}

	// Public player telemetry beacons. Unauthenticated and outside the GraphQL
	// tenant rate-limiter: the browser sends only content_id + ephemeral
	// trace/session ids, and a shared BeaconIntake derives trusted attribution
	// from Commodore, mints the canonical event_id, rate-limits per IP, and
	// verifies the telemetry token. Boot (one-shot startup) and session
	// (viewer-experienced QoE deltas) beacons share one intake/cache.
	{
		telemetrySecret := []byte(cfg.TelemetryTokenSecret)
		if len(telemetrySecret) == 0 {
			logger.Warn("TELEMETRY_TOKEN_SECRET not set; player telemetry will not include serving-cluster attribution")
		}
		telemetryConfigured := metricsCollector.NewGauge("player_telemetry_token_configured", "Whether the shared player telemetry attribution key is configured", nil)
		if len(telemetrySecret) > 0 {
			telemetryConfigured.WithLabelValues().Set(1)
		} else {
			telemetryConfigured.WithLabelValues().Set(0)
		}
		beaconIntake := handlers.NewBeaconIntake(serviceClients.Commodore, rateLimiter, telemetrySecret, logger)
		beaconIntake.SetMetrics(&handlers.BeaconMetrics{
			Events: metricsCollector.NewCounter(
				"player_telemetry_events_total",
				"Player telemetry intake outcomes",
				[]string{"type", "outcome"},
			),
			CacheLoadsActive: metricsCollector.NewGauge("player_telemetry_attribution_loads_active", "Active shared player-attribution authority loads", nil).WithLabelValues(),
			CacheTimeouts:    metricsCollector.NewCounter("player_telemetry_attribution_load_timeouts_total", "Player-attribution authority loads terminated by their operation deadline", nil).WithLabelValues(),
		})

		bootTelemetry := handlers.NewPlaybackTelemetryHandler(beaconIntake, serviceClients.Decklog)
		server.HandleOptionalTrailingSlash(app, http.MethodPost, "/playback/telemetry/boot", bootTelemetry.Handle)
		server.HandleOptionalTrailingSlash(app, http.MethodOptions, "/playback/telemetry/boot", bootTelemetry.HandleOptions)

		sessionTelemetry := handlers.NewPlaybackSessionHandler(beaconIntake, serviceClients.Decklog)
		server.HandleOptionalTrailingSlash(app, http.MethodPost, "/playback/telemetry/session", sessionTelemetry.Handle)
		server.HandleOptionalTrailingSlash(app, http.MethodOptions, "/playback/telemetry/session", sessionTelemetry.HandleOptions)
	}

	// Webhook routing - external payment provider webhooks forwarded to internal services via gRPC.
	// No auth middleware - signature verification happens in the target service.
	// Route pattern: /webhooks/{service}/{provider}
	webhookRouter := webhooks.NewRouter(logger, cfg.WebhookRateLimitPerMin)
	webhookRouter.RegisterService("billing", serviceClients.Purser) // Stripe, Mollie webhooks
	{
		server.HandleOptionalTrailingSlash(app, http.MethodPost, "/webhooks/:service/:provider", webhookRouter.Handle)
		server.HandleOptionalTrailingSlash(app, http.MethodGet, "/webhooks/health", webhookRouter.HandleHealth)
		logger.Info("Webhook router enabled at /webhooks/:service/:provider")
	}

	// GraphQL endpoint (single route group)
	graphqlGroup := app.Group("/graphql")
	graphqlGroup.Use(middleware.PublicOrJWTAuth([]byte(jwtSecret), serviceClients)) // Allowlist public queries or require auth
	graphqlGroup.Use(middleware.DemoModePostAuth(logger))                           // Demo mode detection (after auth)

	// IMPORTANT: WebSocket upgrades may authenticate in the GraphQL WS InitFunc (connectionParams),
	// so rate limiting must not run before that auth has a chance to set tenant context.
	graphqlHTTPMiddleware := []gin.HandlerFunc{
		middleware.RateLimitMiddlewareWithX402(rateLimiter, tenantCache.GetLimitsFunc(), tenantCache, serviceClients.Purser, serviceClients.Purser, serviceClients.Commodore, trustedProxies),
		middleware.ViewerX402Middleware(serviceClients, logger), // Resolve and settle viewer x402 only after abuse throttling.
		middleware.GraphQLContextMiddleware(serviceToken),
		middleware.GraphQLAttachLoaders(serviceClients),
		middleware.UsageTrackerMiddleware(usageTracker),
	}
	{
		graphqlHandlers := append([]gin.HandlerFunc{}, graphqlHTTPMiddleware...)
		graphqlHandlers = append(graphqlHandlers, gin.WrapH(gqlHandler))
		server.HandleOptionalTrailingSlash(graphqlGroup, http.MethodPost, "", graphqlHandlers...)
		server.HandleOptionalTrailingSlash(graphqlGroup, http.MethodOptions, "", func(c *gin.Context) {})
		server.HandleOptionalTrailingSlash(graphqlGroup, http.MethodGet, "/ws", func(c *gin.Context) {
			ctx := c.Request.Context()
			if cookieToken, cookieErr := c.Cookie("access_token"); cookieErr == nil && cookieToken != "" {
				ctx = context.WithValue(ctx, ctxkeys.KeyWSCookieToken, cookieToken)
			}
			ctx = context.WithValue(ctx, ctxkeys.KeyHTTPRequest, c.Request)
			// Resolve the caller once here, the same way the HTTP middleware
			// does: WS never passes through it, so without this the throttle
			// below and any resolver reading the client IP would fall back to
			// forgeable forwarding headers.
			if clientIP := middleware.TrustedClientIP(c, trustedProxies); clientIP != "" {
				ctx = context.WithValue(ctx, ctxkeys.KeyClientIP, clientIP)
			}
			c.Request = c.Request.WithContext(ctx)
			gqlHandler.ServeHTTP(c.Writer, c.Request)
		})
		// Enable playground based on explicit config or GIN_MODE (default: enabled in non-release mode)
		playgroundEnabled := cfg.PlaygroundEnabled()
		if playgroundEnabled {
			server.HandleOptionalTrailingSlash(app, http.MethodGet, "/graphql/playground", gin.WrapH(playground.Handler("GraphQL Playground", "/graphql/")))
			logger.Info("GraphQL Playground enabled at /graphql/playground")
		}
	}

	// No separate public route; PublicOrJWTAuth handles allowlisted unauthenticated queries

	// Lazy-connect to Skipper spoke MCP for proxying ask_consultant.
	// The actual connection is deferred until the first tool call so bridge can
	// start before skipper without losing tool registrations.
	skipperSpokeURL := cfg.SkipperSpokeURL
	skipperClient := mcpserver.NewLazySkipperClient(mcpserver.SkipperClientConfig{
		SpokeURL:     skipperSpokeURL,
		ServiceToken: serviceToken,
		Logger:       logger,
	}, logger)
	defer func() { _ = skipperClient.Close() }()

	// MCP (Model Context Protocol) endpoint for AI agent access
	// Auth is handled inside the MCP server via request headers
	mcpServer, err := mcpserver.NewServer(mcpserver.Config{
		ServiceClients:    serviceClients,
		Resolver:          resolver.Resolver,
		Logger:            logger,
		JWTSecret:         []byte(jwtSecret),
		RateLimiter:       rateLimiter,
		TenantCache:       tenantCache,
		UsageTracker:      usageTracker,
		TrustedProxies:    trustedProxies,
		SkipperClient:     skipperClient,
		OriginAllowed:     originAllowed,
		GatewayGraphQLURL: cfg.GatewayGraphQLURL(),
	})
	if err != nil {
		logger.WithError(err).Fatal("Failed to initialize MCP server")
	}
	app.Any("/mcp", gin.WrapH(mcpServer.HTTPHandler()))
	app.Any("/mcp/*path", gin.WrapH(mcpServer.HTTPHandler()))
	logger.Info("MCP endpoint enabled at /mcp")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Best-effort service registration in Quartermaster (gRPC)
	go func() {
		req, reqErr := qmbootstrap.NewServiceRequest(qmbootstrap.ServiceRegistration{
			ServiceType:   "bridge",
			Port:          cfg.Port,
			AdvertiseHost: cfg.AdvertiseHost,
			ClusterID:     cfg.ClusterID,
			NodeID:        cfg.NodeID,
		})
		if reqErr != nil {
			logger.WithError(reqErr).Warn("Quartermaster bootstrap skipped")
			return
		}
		resp, bootstrapErr := qmbootstrap.BootstrapServiceWithRetry(ctx, serviceClients.Quartermaster, req, logger, qmbootstrap.DefaultRetryConfig("bridge"))
		if bootstrapErr != nil {
			logger.WithError(bootstrapErr).Warn("Quartermaster bootstrap (bridge) failed")
		} else {
			if resp != nil && resp.GetOwnerTenantId() != "" {
				usageTracker.SetServiceTenantID(resp.GetOwnerTenantId())
				logger.WithField("tenant_id", resp.GetOwnerTenantId()).Info("Usage tracker tenant owner set")
			}
			logger.Info("Quartermaster bootstrap (gateway) ok")
		}
	}()

	server.RegisterEnvFileReload("bridge", logger)
	if runErr := server.Run(ctx, server.RunSpec{
		Service:  "bridge",
		Logger:   logger,
		Ready:    readiness,
		HTTP:     []server.HTTPListener{{Name: "http", Port: cfg.Port, Handler: app}},
		OnReload: []server.ReloadCallback{liveConfig.Reload},
		OnShutdown: []func(context.Context){func(context.Context) {
			// Shutdown the resolver to clean up WebSocket connections
			if shutdownErr := resolver.Shutdown(); shutdownErr != nil {
				logger.WithError(shutdownErr).Error("Error shutting down resolver")
			}
		}},
	}); runErr != nil {
		logger.WithError(runErr).Fatal("Server exited with error")
	}
}

func seconds(n int) time.Duration {
	return time.Duration(n) * time.Second
}

// serviceClientsConfig builds the downstream gRPC client configuration. The
// clients are dialed once at startup.
func serviceClientsConfig(cfg *appconfig.Bridge, logger logging.Logger) clients.Config {
	return clients.Config{
		ServiceToken:  cfg.ServiceToken,
		JWTSecret:     []byte(cfg.JWTSecret),
		Logger:        logger,
		AllowInsecure: cfg.GRPCAllowInsecure,
		CACertFile:    cfg.GRPCTLSCAPath,
		Commodore:     clients.Endpoint{Addr: cfg.CommodoreGRPCAddr, TLSServerName: cfg.CommodoreGRPCTLSServerName},
		Periscope:     clients.Endpoint{Addr: cfg.PeriscopeGRPCAddr, TLSServerName: cfg.PeriscopeGRPCTLSServerName},
		Purser:        clients.Endpoint{Addr: cfg.PurserGRPCAddr, TLSServerName: cfg.PurserGRPCTLSServerName},
		Quartermaster: clients.Endpoint{Addr: cfg.QuartermasterGRPCAddr, TLSServerName: cfg.QuartermasterGRPCTLSServerName},
		Signalman:     clients.Endpoint{Addr: cfg.SignalmanGRPCAddr, TLSServerName: cfg.SignalmanGRPCTLSServerName},
		Decklog:       clients.Endpoint{Addr: cfg.DecklogGRPCAddr, TLSServerName: cfg.DecklogGRPCTLSServerName},
		Navigator:     clients.Endpoint{Addr: cfg.NavigatorGRPCAddr, TLSServerName: cfg.NavigatorGRPCTLSServerName},
		Deckhand:      clients.Endpoint{Addr: cfg.DeckhandGRPCAddr, TLSServerName: cfg.DeckhandGRPCTLSServerName},
		Skipper:       clients.Endpoint{Addr: cfg.SkipperGRPCAddr, TLSServerName: cfg.SkipperGRPCTLSServerName},
		Lookout:       clients.Endpoint{Addr: cfg.LookoutGRPCAddr, TLSServerName: cfg.LookoutGRPCTLSServerName},
		Bosun:         clients.Endpoint{Addr: cfg.BosunGRPCAddr, TLSServerName: cfg.BosunGRPCTLSServerName},
		QuartermasterCache: cache.Options{
			TTL:                  seconds(cfg.QuartermasterCacheTTLSeconds),
			StaleWhileRevalidate: seconds(cfg.QuartermasterCacheSWRSeconds),
			NegativeTTL:          seconds(cfg.QuartermasterCacheNegTTLSeconds),
			MaxEntries:           cfg.QuartermasterCacheMax,
		},
		ClusterID: cfg.ClusterID,
		Region:    cfg.Region,
	}
}

// resolverConfig builds the GraphQL resolver configuration. streamingConfig
// values are read from the live snapshot at use time; everything else,
// including the pooled Signalman dialer settings, is fixed at startup.
func resolverConfig(live *config.Live[appconfig.Bridge]) resolvers.ResolverConfig {
	cfg := live.Get()
	return resolvers.ResolverConfig{
		ServiceToken:              cfg.ServiceToken,
		SignalmanAddr:             cfg.SignalmanGRPCAddr,
		SignalmanAddrs:            cfg.SignalmanGRPCAddrs,
		MaxSubscriptionsPerTenant: cfg.WSMaxSubscriptionsPerTenant,
		SignalmanDial: resolvers.SignalmanDialSettings{
			OpenTimeout:   seconds(cfg.SignalmanConnectTimeoutSecs),
			AllowInsecure: cfg.GRPCAllowInsecure,
			CACertFile:    cfg.GRPCTLSCAPath,
			TLSServerName: cfg.SignalmanGRPCTLSServerName,
		},
		PeriscopeCache: cache.Options{
			TTL:                  seconds(cfg.PeriscopeCacheTTLSeconds),
			StaleWhileRevalidate: seconds(cfg.PeriscopeCacheSWRSeconds),
			NegativeTTL:          seconds(cfg.PeriscopeCacheNegTTLSeconds),
			MaxEntries:           cfg.PeriscopeCacheMax,
		},
		PeriscopeLoadTimeout: seconds(cfg.PeriscopeCacheLoadTimeoutSeconds),
		TelemetrySecret:      []byte(cfg.TelemetryTokenSecret),
		LocalClusterID:       cfg.ClusterID,
		Streaming: func() resolvers.StreamingSettings {
			current := live.Get()
			return resolvers.StreamingSettings{
				SRTPort:    current.StreamingSRTPort,
				RTMPPort:   current.StreamingRTMPPort,
				RootDomain: current.PlatformRootDomain,
			}
		},
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

type skillFiles struct {
	skillMD     []byte
	skillJSON   []byte
	heartbeatMD []byte
	mcpJSON     []byte
	securityTxt []byte
	llmsTxt     []byte
	robotsTxt   []byte
	didJSON     []byte
	oauthPRM    []byte
}

// loadSkillFiles finds the agent discovery files. configuredDir (SKILL_FILES_DIR)
// is checked before the built-in candidates; gasWalletAddress is substituted
// into did.json.
func loadSkillFiles(logger logging.Logger, configuredDir, gasWalletAddress string) skillFiles {
	candidates := []string{}
	if explicitDir := strings.TrimSpace(configuredDir); explicitDir != "" {
		candidates = append(candidates, explicitDir)
	}
	candidates = append(candidates, ".", "/app", "docs/skills", "../docs/skills")

	var dir string
	for _, candidate := range candidates {
		if _, err := os.Stat(filepath.Join(candidate, "SKILL.md")); err == nil {
			if _, err := os.Stat(filepath.Join(candidate, "skill.json")); err == nil {
				dir = candidate
				break
			}
		}
	}

	if dir == "" {
		logger.WithField("candidates", strings.Join(candidates, ", ")).Warn("skill files not found in any candidate directory")
		return skillFiles{}
	}

	readFile := func(name string) []byte {
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			logger.WithError(err).WithField("path", filepath.Join(dir, name)).Warn(name + " not found")
			return nil
		}
		return data
	}

	sf := skillFiles{
		skillMD:     readFile("SKILL.md"),
		skillJSON:   readFile("skill.json"),
		heartbeatMD: readFile("heartbeat.md"),
		mcpJSON:     readFile("mcp.json"),
		securityTxt: readFile("security.txt"),
		llmsTxt:     readFile("llms.txt"),
		robotsTxt:   readFile("robots.txt"),
		didJSON:     readFile("did.json"),
		oauthPRM:    readFile("oauth-protected-resource.json"),
	}

	if sf.didJSON != nil {
		if addr := strings.TrimSpace(gasWalletAddress); addr != "" {
			sf.didJSON = bytes.ReplaceAll(sf.didJSON, []byte("{{X402_GAS_WALLET_ADDRESS}}"), []byte(addr))
		} else {
			var doc map[string]interface{}
			if err := json.Unmarshal(sf.didJSON, &doc); err == nil {
				delete(doc, "verificationMethod")
				if stripped, err := json.MarshalIndent(doc, "", "  "); err == nil {
					sf.didJSON = stripped
				}
			}
		}
	}

	return sf
}

func isIntrospectionOperation(op *ast.OperationDefinition) bool {
	if op == nil || len(op.SelectionSet) == 0 {
		return false
	}
	for _, sel := range op.SelectionSet {
		field, ok := sel.(*ast.Field)
		if !ok || !strings.HasPrefix(field.Name, "__") {
			return false
		}
	}
	return true
}

func recoverOperation(ctx context.Context, next graphql.OperationHandler, logger logging.Logger) (handler graphql.ResponseHandler) {
	defer func() {
		if r := recover(); r != nil {
			logger.WithFields(logging.Fields{
				"panic":      r,
				"stacktrace": string(debug.Stack()),
			}).Error("Panic recovered in GraphQL operation")
			handler = func(ctx context.Context) *graphql.Response {
				return graphql.ErrorResponse(ctx, "internal error")
			}
		}
	}()
	return next(ctx)
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
