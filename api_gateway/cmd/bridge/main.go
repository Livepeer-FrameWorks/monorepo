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
	pkgauth "frameworks/pkg/auth"
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
	serviceToken := config.GetEnv("SERVICE_TOKEN", "")
	serviceClients := clients.NewServiceClients(clients.Config{
		ServiceToken: serviceToken,
		Logger:       logger,
	})

	// Initialize auth proxy
	commodoreURL := config.GetEnv("COMMODORE_URL", "http://localhost:18001")
	authProxy := handlers.NewAuthProxy(commodoreURL, logger)

	// Setup monitoring
	healthChecker := monitoring.NewHealthChecker("bridge", version.Version)
	metricsCollector := monitoring.NewMetricsCollector("bridge", version.Version, version.GitCommit)

	// Add health checks
	healthChecker.AddCheck("config", monitoring.ConfigurationHealthCheck(map[string]string{
		"JWT_SECRET":        config.GetEnv("JWT_SECRET", ""),
		"SERVICE_TOKEN":     config.GetEnv("SERVICE_TOKEN", ""),
		"COMMODORE_URL":     config.GetEnv("COMMODORE_URL", ""),
		"PERISCOPE_URL":     config.GetEnv("PERISCOPE_URL", ""),
		"PURSER_URL":        config.GetEnv("PURSER_URL", ""),
		"QUARTERMASTER_URL": config.GetEnv("QUARTERMASTER_URL", ""),
		"SIGNALMAN_URL":     config.GetEnv("SIGNALMAN_URL", ""),
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

					// Validate JWT token
					jwtSecret := config.GetEnv("JWT_SECRET", "default-secret-key-change-in-production")
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

	// GraphQL endpoint with optional auth (some queries are public)
	graphqlGroup := app.Group("/graphql")
	jwtSecret := config.GetEnv("JWT_SECRET", "default-secret-key-change-in-production")
	graphqlGroup.Use(middleware.DemoMode(logger))                  // Demo mode detection (must be before auth)
	graphqlGroup.Use(pkgauth.JWTAuthMiddleware([]byte(jwtSecret))) // Standard auth with WebSocket support
	graphqlGroup.Use(middleware.GraphQLContextMiddleware())        // Bridge user context to GraphQL
	graphqlGroup.Use(middleware.GraphQLAttachLoaders(serviceClients))
	{
		// GraphQL endpoint
		graphqlGroup.POST("/", gin.WrapH(gqlHandler))

		// GraphQL playground in development
		if config.GetEnv("GIN_MODE", "debug") != "release" {
			// Use a separate route for playground to avoid conflicts
			app.GET("/graphql/playground", gin.WrapH(playground.Handler("GraphQL Playground", "/graphql/")))
		}
	}

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
}
