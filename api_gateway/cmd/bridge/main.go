package main

import (
	"context"
	"net/http"
	"strings"
	"time"

	"frameworks/api_gateway/graph"
	"frameworks/api_gateway/graph/generated"
	"frameworks/api_gateway/internal/clients"
	"frameworks/api_gateway/internal/handlers"
	"frameworks/api_gateway/internal/middleware"
	"frameworks/api_gateway/internal/resolvers"
	qmapi "frameworks/pkg/api/quartermaster"
	pkgauth "frameworks/pkg/auth"
	qmclient "frameworks/pkg/clients/quartermaster"
	"frameworks/pkg/config"
	"frameworks/pkg/logging"
	"frameworks/pkg/monitoring"
	"frameworks/pkg/server"
	"frameworks/pkg/version"

	"github.com/99designs/gqlgen/graphql/handler"
	"github.com/99designs/gqlgen/graphql/handler/extension"
	"github.com/99designs/gqlgen/graphql/handler/transport"
	"github.com/99designs/gqlgen/graphql/playground"
	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
)

func main() {
	// Setup logger
	logger := logging.NewLoggerWithService("bridge")

	// Load environment variables
	config.LoadEnv(logger)

	logger.Info("Starting Bridge GraphQL Gateway")

	// Initialize service clients
	serviceToken := config.RequireEnv("SERVICE_TOKEN")
	commodoreURL := config.RequireEnv("COMMODORE_URL")
	quartermasterURL := config.RequireEnv("QUARTERMASTER_URL")
	purserURL := config.RequireEnv("PURSER_URL")
	periscopeQueryURL := config.RequireEnv("PERISCOPE_QUERY_URL")
	signalmanWSURL := config.RequireEnv("SIGNALMAN_WS_URL")
	jwtSecret := config.RequireEnv("JWT_SECRET")
	serviceClients := clients.NewServiceClients(clients.Config{
		ServiceToken: serviceToken,
		Logger:       logger,
	})

	// Initialize auth proxy
	authProxy := handlers.NewAuthProxy(commodoreURL, logger)

	// Setup monitoring
	healthChecker := monitoring.NewHealthChecker("bridge", version.Version)
	metricsCollector := monitoring.NewMetricsCollector("bridge", version.Version, version.GitCommit)

	// Add health checks
	healthChecker.AddCheck("config", monitoring.ConfigurationHealthCheck(map[string]string{
		"JWT_SECRET":          jwtSecret,
		"SERVICE_TOKEN":       serviceToken,
		"COMMODORE_URL":       commodoreURL,
		"PERISCOPE_QUERY_URL": periscopeQueryURL,
		"PURSER_URL":          purserURL,
		"QUARTERMASTER_URL":   quartermasterURL,
		"SIGNALMAN_WS_URL":    signalmanWSURL,
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
	resolver := graph.NewResolver(serviceClients, logger, graphqlMetrics)

	// Create GraphQL server with WebSocket support for subscriptions
	gqlHandler := handler.New(generated.NewExecutableSchema(generated.Config{Resolvers: resolver}))

	// Enable introspection for developer API explorer
	gqlHandler.Use(extension.Introspection{})

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
			// Get authorization from WebSocket connection params using built-in method
			authHeader := initPayload.Authorization()

			if authHeader != "" {
				// Parse JWT token from Authorization header
				parts := strings.Split(authHeader, " ")
				if len(parts) == 2 && parts[0] == "Bearer" {
					token := parts[1]

					claims, err := pkgauth.ValidateJWT(token, []byte(jwtSecret))
					if err == nil {
						// Add user context to WebSocket connection
						ctx = context.WithValue(ctx, "user_id", claims.UserID)
						ctx = context.WithValue(ctx, "tenant_id", claims.TenantID)
						ctx = context.WithValue(ctx, "email", claims.Email)
						ctx = context.WithValue(ctx, "role", claims.Role)
						ctx = context.WithValue(ctx, "jwt_token", token)

						// Create user context
						user := &middleware.UserContext{
							UserID:   claims.UserID,
							TenantID: claims.TenantID,
							Email:    claims.Email,
							Role:     claims.Role,
						}
						ctx = context.WithValue(ctx, "user", user)
					}
				}
			}

			// Return the context and initPayload (can be modified if needed)
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
	}

	// Auth endpoints (proxied to Commodore)
	auth := app.Group("/auth")
	{
		auth.POST("/login", authProxy.ProxyToCommodore("/login"))
		auth.POST("/register", authProxy.ProxyToCommodore("/register"))
		auth.POST("/logout", authProxy.ProxyToCommodore("/logout"))
		auth.GET("/me", authProxy.ProxyToCommodore("/me"))
		auth.GET("/verify/:token", authProxy.ProxyToCommodore("/verify/:token"))
		auth.POST("/refresh", authProxy.ProxyToCommodore("/refresh"))
		auth.POST("/forgot-password", authProxy.ProxyToCommodore("/forgot-password"))
		auth.POST("/reset-password", authProxy.ProxyToCommodore("/reset-password"))
	}

	// GraphQL endpoint (single route group)
	graphqlGroup := app.Group("/graphql")
	graphqlGroup.Use(middleware.DemoMode(logger))                   // Demo mode detection (must be before auth)
	graphqlGroup.Use(middleware.PublicOrJWTAuth([]byte(jwtSecret))) // Allowlist public queries or require auth
	graphqlGroup.Use(middleware.GraphQLContextMiddleware())         // Bridge user context to GraphQL
	graphqlGroup.Use(middleware.GraphQLAttachLoaders(serviceClients))
	{
		graphqlGroup.POST("/", gin.WrapH(gqlHandler))
		if config.GetEnv("GIN_MODE", "debug") != "release" {
			app.GET("/graphql/playground", gin.WrapH(playground.Handler("GraphQL Playground", "/graphql/")))
		}
	}

	// No separate public route; PublicOrJWTAuth handles allowlisted unauthenticated queries

	// Dedicated WebSocket endpoint for GraphQL subscriptions (no auth middleware)
	// Authentication is handled in the WebSocket InitFunc via connection params
	app.GET("/graphql/ws", gin.WrapH(gqlHandler))

	// Use standard server startup with graceful shutdown
	serverConfig := server.DefaultConfig("bridge", "18000")

	// Start server with standard graceful shutdown handling
	if err := server.Start(serverConfig, app, logger); err != nil {
		logger.Fatal("Failed to start server: " + err.Error())
	}

	// Shutdown the resolver to clean up WebSocket connections
	if err := resolver.Shutdown(); err != nil {
		logger.Error("Error shutting down resolver: " + err.Error())
	}

	// Best-effort service registration in Quartermaster
	go func() {
		qc := qmclient.NewClient(qmclient.Config{BaseURL: quartermasterURL, ServiceToken: serviceToken, Logger: logger})
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if _, err := qc.BootstrapService(ctx, &qmapi.BootstrapServiceRequest{Type: "gateway", Version: version.Version, Protocol: "http", HealthEndpoint: func() *string { s := "/health"; return &s }(), Port: 18000}); err != nil {
			logger.WithError(err).Warn("Quartermaster bootstrap (gateway) failed")
		} else {
			logger.Info("Quartermaster bootstrap (gateway) ok")
		}
	}()
}
