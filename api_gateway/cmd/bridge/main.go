package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"frameworks/api_gateway/graph"
	"frameworks/api_gateway/graph/generated"
	"frameworks/api_gateway/internal/clients"
	"frameworks/api_gateway/internal/handlers"
	"frameworks/api_gateway/internal/middleware"
	pkgauth "frameworks/pkg/auth"
	"frameworks/pkg/config"
	"frameworks/pkg/logging"

	"github.com/99designs/gqlgen/graphql/handler"
	"github.com/99designs/gqlgen/graphql/handler/transport"
	"github.com/99designs/gqlgen/graphql/playground"
	"github.com/gin-gonic/gin"
)

func main() {
	// Initialize logging
	logger := logging.NewLogger()
	logger.Info("Starting Bridge GraphQL Gateway")

	// Load configuration
	config.LoadEnv(logger)

	// Initialize service clients
	serviceToken := config.GetEnv("SERVICE_TOKEN", "")
	serviceClients := clients.NewServiceClients(clients.Config{
		ServiceToken: serviceToken,
		Logger:       logger,
	})

	// Initialize auth proxy
	commodoreURL := config.GetEnv("COMMODORE_URL", "http://localhost:18001")
	authProxy := handlers.NewAuthProxy(commodoreURL, logger)

	// Initialize GraphQL resolver and server
	resolver := graph.NewResolver(serviceClients, logger)

	// Create GraphQL server with WebSocket support for subscriptions
	gqlHandler := handler.New(generated.NewExecutableSchema(generated.Config{Resolvers: resolver}))

	// Add transport options
	gqlHandler.AddTransport(transport.POST{})
	gqlHandler.AddTransport(transport.GET{})
	gqlHandler.AddTransport(transport.Websocket{
		KeepAlivePingInterval: 10 * time.Second,
	})

	// Get port from environment
	port := getPort()

	// Set Gin mode
	if config.GetEnv("GIN_MODE", "debug") == "release" {
		gin.SetMode(gin.ReleaseMode)
	}

	// Create router
	router := gin.New()
	router.Use(gin.Logger())
	router.Use(gin.Recovery())

	// Health check endpoint (no auth required)
	router.GET("/health", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{
			"status":  "healthy",
			"service": "bridge",
			"version": config.GetEnv("VERSION", "development"),
		})
	})

	// Public API routes (no auth required)
	{
		router.GET("/status", func(c *gin.Context) {
			c.JSON(http.StatusOK, gin.H{
				"service": "bridge",
				"status":  "ready",
				"message": "GraphQL Gateway - Ready",
			})
		})
	}

	// Auth endpoints (proxied to Commodore)
	auth := router.Group("/auth")
	{
		auth.POST("/login", authProxy.ProxyToCommodore("/login"))
		auth.POST("/register", authProxy.ProxyToCommodore("/register"))
		auth.POST("/logout", authProxy.ProxyToCommodore("/logout"))
		auth.GET("/verify/:token", authProxy.ProxyToCommodore("/verify/:token"))
		auth.POST("/refresh", authProxy.ProxyToCommodore("/refresh"))
		auth.POST("/forgot-password", authProxy.ProxyToCommodore("/forgot-password"))
		auth.POST("/reset-password", authProxy.ProxyToCommodore("/reset-password"))
	}

	// GraphQL endpoint with optional auth (some queries are public)
	graphqlGroup := router.Group("/graphql")
	jwtSecret := config.GetEnv("JWT_SECRET", "default-secret-key-change-in-production")
	graphqlGroup.Use(pkgauth.JWTAuthMiddleware([]byte(jwtSecret))) // Using pkg/auth
	graphqlGroup.Use(middleware.GraphQLContextMiddleware())        // Bridge user context to GraphQL
	{
		// GraphQL endpoint
		graphqlGroup.POST("/", gin.WrapH(gqlHandler))

		// GraphQL playground in development
		if config.GetEnv("GIN_MODE", "debug") != "release" {
			graphqlGroup.GET("/", gin.WrapH(playground.Handler("GraphQL Playground", "/graphql/")))
		}
	}

	// Create HTTP server
	srv := &http.Server{
		Addr:    fmt.Sprintf(":%d", port),
		Handler: router,
	}

	// Start server in a goroutine
	go func() {
		logger.Info(fmt.Sprintf("Bridge GraphQL Gateway listening on port %d", port))
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logger.Fatal(fmt.Sprintf("Failed to start server: %v", err))
		}
	}()

	// Graceful shutdown
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	logger.Info("Shutting down Bridge GraphQL Gateway...")

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Shutdown the resolver first to clean up WebSocket connections
	if err := resolver.Shutdown(); err != nil {
		logger.Error(fmt.Sprintf("Error shutting down resolver: %v", err))
	}

	if err := srv.Shutdown(ctx); err != nil {
		logger.Fatal(fmt.Sprintf("Server forced to shutdown: %v", err))
	}

	logger.Info("Bridge GraphQL Gateway stopped")
}

func getPort() int {
	portStr := os.Getenv("BRIDGE_PORT")
	if portStr == "" {
		portStr = os.Getenv("PORT")
	}
	if portStr == "" {
		portStr = "18000"
	}

	port, err := strconv.Atoi(portStr)
	if err != nil {
		log.Printf("Invalid port %s, using default 18000", portStr)
		return 18000
	}

	return port
}
