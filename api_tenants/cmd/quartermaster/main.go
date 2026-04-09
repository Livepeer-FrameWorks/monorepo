package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"strconv"
	"strings"
	"time"

	qmgrpc "frameworks/api_tenants/internal/grpc"
	"frameworks/api_tenants/internal/handlers"
	decklogclient "frameworks/pkg/clients/decklog"
	"frameworks/pkg/clients/navigator"
	purserclient "frameworks/pkg/clients/purser"
	qmclient "frameworks/pkg/clients/quartermaster"
	"frameworks/pkg/config"
	"frameworks/pkg/database"
	"frameworks/pkg/geoip"
	"frameworks/pkg/logging"
	"frameworks/pkg/monitoring"
	pb "frameworks/pkg/proto"
	"frameworks/pkg/qmbootstrap"
	"frameworks/pkg/server"
	"frameworks/pkg/version"
	"github.com/gin-gonic/gin"
)

func main() {
	// Setup logger
	logger := logging.NewLoggerWithService("quartermaster")

	// Load environment variables
	config.LoadEnv(logger)

	logger.Info("Starting Quartermaster (Tenant Management API)")

	dbURL := config.RequireEnv("DATABASE_URL")
	serviceToken := config.RequireEnv("SERVICE_TOKEN")
	jwtSecret := config.RequireEnv("JWT_SECRET")
	quartermasterGRPCAddr := config.GetEnv("QUARTERMASTER_GRPC_ADDR", "quartermaster:19002")
	navigatorGRPCAddr := config.GetEnv("NAVIGATOR_GRPC_ADDR", "") // Optional: enables DNS features.

	// Connect to database
	dbConfig := database.DefaultConfig()
	dbConfig.URL = dbURL
	db := database.MustConnect(dbConfig, logger)
	defer func() { _ = db.Close() }()

	// Setup monitoring
	healthChecker := monitoring.NewHealthChecker("quartermaster", version.Version)
	metricsCollector := monitoring.NewMetricsCollector("quartermaster", version.Version, version.GitCommit)

	// Add health checks
	healthChecker.AddCheck("database", monitoring.DatabaseHealthCheck(db))
	healthChecker.AddCheck("config", monitoring.ConfigurationHealthCheck(map[string]string{
		"DATABASE_URL":  dbURL,
		"SERVICE_TOKEN": serviceToken,
	}))

	// Create gRPC server metrics
	serverMetrics := &qmgrpc.ServerMetrics{
		TenantOperations:  metricsCollector.NewCounter("grpc_tenant_operations_total", "gRPC tenant operations", []string{"operation", "status"}),
		ClusterOperations: metricsCollector.NewCounter("grpc_cluster_operations_total", "gRPC cluster operations", []string{"operation", "status"}),
		NodeOperations:    metricsCollector.NewCounter("grpc_node_operations_total", "gRPC node operations", []string{"operation", "status"}),
		ServiceOperations: metricsCollector.NewCounter("grpc_service_operations_total", "gRPC service registry operations", []string{"operation", "status"}),
		GRPCRequests:      metricsCollector.NewCounter("grpc_requests_total", "Total gRPC requests", []string{"method", "status"}),
		GRPCDuration:      metricsCollector.NewHistogram("grpc_request_duration_seconds", "gRPC request duration", []string{"method"}, nil),
	}

	// Initialize Navigator client
	var navigatorClient *navigator.Client
	var err error

	if navigatorGRPCAddr != "" {
		navigatorClient, err = navigator.NewClient(navigator.Config{
			Addr:          navigatorGRPCAddr,
			Timeout:       5 * time.Second,
			Logger:        logger,
			ServiceToken:  serviceToken,
			AllowInsecure: config.GetEnvBool("GRPC_ALLOW_INSECURE", true),
			CACertFile:    config.GetEnv("GRPC_TLS_CA_PATH", ""),
			ServerName:    config.GetEnv("GRPC_TLS_SERVER_NAME", ""),
		})
		if err != nil {
			logger.WithError(err).Error("Failed to create Navigator client - DNS features will be disabled")
		} else {
			defer func() { _ = navigatorClient.Close() }() // Ensure the client connection is closed
		}
	} else {
		logger.Info("NAVIGATOR_GRPC_ADDR not set - DNS features will be disabled")
	}

	// Create Decklog gRPC client for service events
	decklogGRPCAddr := config.GetEnv("DECKLOG_GRPC_ADDR", "decklog:18006")
	decklogClient, err := decklogclient.NewBatchedClient(decklogclient.BatchedClientConfig{
		Target:        decklogGRPCAddr,
		AllowInsecure: config.GetEnvBool("GRPC_ALLOW_INSECURE", true),
		CACertFile:    config.GetEnv("GRPC_TLS_CA_PATH", ""),
		ServerName:    config.GetEnv("GRPC_TLS_SERVER_NAME", ""),
		Timeout:       5 * time.Second,
		Source:        "quartermaster",
		ServiceToken:  serviceToken,
	}, logger)
	if err != nil {
		logger.WithError(err).Warn("Failed to create Decklog gRPC client - service events will be disabled")
		decklogClient = nil
	} else {
		defer func() { _ = decklogClient.Close() }()
		logger.WithField("addr", decklogGRPCAddr).Info("Connected to Decklog gRPC")
	}

	// Create Purser gRPC client for billing status lookups (cross-service via gRPC, not DB)
	purserGRPCAddr := config.GetEnv("PURSER_GRPC_ADDR", "purser:19003")
	var purserClient *purserclient.GRPCClient
	purserClient, err = purserclient.NewGRPCClient(purserclient.GRPCConfig{
		GRPCAddr:      purserGRPCAddr,
		Timeout:       5 * time.Second,
		Logger:        logger,
		ServiceToken:  serviceToken,
		AllowInsecure: config.GetEnvBool("GRPC_ALLOW_INSECURE", true),
		CACertFile:    config.GetEnv("GRPC_TLS_CA_PATH", ""),
		ServerName:    config.GetEnv("GRPC_TLS_SERVER_NAME", ""),
	})
	if err != nil {
		logger.WithError(err).Warn("Failed to create Purser gRPC client - billing status lookups will use defaults")
		purserClient = nil
	} else {
		defer func() { _ = purserClient.Close() }()
		logger.WithField("addr", purserGRPCAddr).Info("Connected to Purser gRPC")
	}

	// Initialize handlers (for health poller)
	handlers.Init(db, logger)

	// Setup router with unified monitoring
	router := server.SetupServiceRouter(logger, "quartermaster", healthChecker, metricsCollector)

	// NOTE: All API routes removed - now handled via gRPC only.
	// Gateway -> Quartermaster gRPC for all tenant, cluster, node, service operations.
	router.GET("/internal/ingress-sites", func(c *gin.Context) {
		if !requirePrivateInternalRequest(c) {
			return
		}
		authz := strings.TrimSpace(c.GetHeader("Authorization"))
		if authz != "Bearer "+serviceToken {
			c.JSON(http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
			return
		}

		nodeID := strings.TrimSpace(c.Query("node_id"))
		if nodeID == "" {
			c.JSON(http.StatusBadRequest, map[string]string{"error": "node_id is required"})
			return
		}

		rows, err := db.QueryContext(c.Request.Context(), `
			SELECT site_id, cluster_id, node_id, domains, tls_bundle_id, kind, upstream, COALESCE(metadata, '{}'::jsonb)
			FROM quartermaster.ingress_sites
			WHERE node_id = $1
			ORDER BY site_id
		`, nodeID)
		if err != nil {
			c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		defer rows.Close()

		type ingressSite struct {
			SiteID      string                 `json:"site_id"`
			ClusterID   string                 `json:"cluster_id"`
			NodeID      string                 `json:"node_id"`
			Domains     []string               `json:"domains"`
			TLSBundleID string                 `json:"tls_bundle_id"`
			Kind        string                 `json:"kind"`
			Upstream    string                 `json:"upstream"`
			Metadata    map[string]interface{} `json:"metadata"`
		}

		var sites []ingressSite
		for rows.Next() {
			var site ingressSite
			var domainsJSON, metadataJSON []byte
			if err := rows.Scan(&site.SiteID, &site.ClusterID, &site.NodeID, &domainsJSON, &site.TLSBundleID, &site.Kind, &site.Upstream, &metadataJSON); err != nil {
				c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
				return
			}
			if unmarshalErr := json.Unmarshal(domainsJSON, &site.Domains); unmarshalErr != nil {
				site.Domains = nil
			}
			if unmarshalErr := json.Unmarshal(metadataJSON, &site.Metadata); unmarshalErr != nil {
				site.Metadata = nil
			}
			sites = append(sites, site)
		}

		c.JSON(http.StatusOK, map[string]interface{}{"sites": sites})
	})

	// Start health poller before serving
	handlers.StartHealthPoller()

	// Start gRPC server in a goroutine
	grpcPort := config.GetEnv("GRPC_PORT", "19002")
	go func() {
		grpcAddr := fmt.Sprintf(":%s", grpcPort)
		lis, err := net.Listen("tcp", grpcAddr)
		if err != nil {
			logger.WithError(err).Fatal("Failed to listen on gRPC port")
		}

		geoipReader := geoip.GetSharedReader()
		if geoipReader != nil {
			logger.WithField("provider", geoipReader.GetProvider()).Info("GeoIP reader loaded")
		}

		grpcServer := qmgrpc.NewGRPCServer(qmgrpc.GRPCServerConfig{
			DB:              db,
			Logger:          logger,
			ServiceToken:    serviceToken,
			JWTSecret:       []byte(jwtSecret),
			NavigatorClient: navigatorClient,
			DecklogClient:   decklogClient,
			PurserClient:    purserClient,
			GeoIPReader:     geoipReader,
			Metrics:         serverMetrics,
			CertFile:        config.GetEnv("GRPC_TLS_CERT_PATH", ""),
			KeyFile:         config.GetEnv("GRPC_TLS_KEY_PATH", ""),
			AllowInsecure:   config.GetEnvBool("GRPC_ALLOW_INSECURE", true),
		})
		logger.WithField("addr", grpcAddr).Info("Starting gRPC server")

		if err := grpcServer.Serve(lis); err != nil {
			logger.WithError(err).Fatal("gRPC server failed")
		}
	}()

	// Start HTTP server with graceful shutdown
	serverConfig := server.DefaultConfig("quartermaster", "18002")
	serverConfig.TLSCertFile = config.GetEnv("QUARTERMASTER_HTTP_TLS_CERT_FILE", "")
	serverConfig.TLSKeyFile = config.GetEnv("QUARTERMASTER_HTTP_TLS_KEY_FILE", "")

	// Best-effort self-registration in Quartermaster (idempotent, using gRPC)
	// Must be launched BEFORE server.Start() which blocks
	go func() {
		qc, err := qmclient.NewGRPCClient(qmclient.GRPCConfig{
			GRPCAddr:      quartermasterGRPCAddr,
			Timeout:       10 * time.Second,
			Logger:        logger,
			ServiceToken:  serviceToken,
			AllowInsecure: config.GetEnvBool("GRPC_ALLOW_INSECURE", true),
			CACertFile:    config.GetEnv("GRPC_TLS_CA_PATH", ""),
			ServerName:    config.GetEnv("GRPC_TLS_SERVER_NAME", ""),
		})
		if err != nil {
			logger.WithError(err).Warn("Failed to create Quartermaster gRPC client for self-registration")
			return
		}
		defer func() { _ = qc.Close() }()
		healthEndpoint := "/health"
		httpPort, _ := strconv.Atoi(serverConfig.Port)
		if httpPort <= 0 || httpPort > 65535 {
			logger.Warn("Quartermaster bootstrap skipped: invalid port")
			return
		}
		advertiseHost := config.GetEnv("QUARTERMASTER_HOST", "quartermaster")
		clusterID := config.GetEnv("CLUSTER_ID", "")
		req := &pb.BootstrapServiceRequest{
			Type:           "quartermaster",
			Version:        version.Version,
			Protocol:       "http",
			HealthEndpoint: &healthEndpoint,
			Port:           int32(httpPort),
			AdvertiseHost:  &advertiseHost,
			ClusterId: func() *string {
				if clusterID != "" {
					return &clusterID
				}
				return nil
			}(),
		}
		if nodeID := config.GetEnv("NODE_ID", ""); nodeID != "" {
			req.NodeId = &nodeID
		}
		if _, err := qmbootstrap.BootstrapServiceWithRetry(context.Background(), qc, req, logger, qmbootstrap.DefaultRetryConfig("quartermaster")); err != nil {
			logger.WithError(err).Warn("Quartermaster bootstrap (quartermaster) failed")
		} else {
			logger.Info("Quartermaster bootstrap (quartermaster) ok")
		}
	}()

	if err := server.Start(serverConfig, router, logger); err != nil {
		logger.WithError(err).Fatal("Server startup failed")
	}
}

func requirePrivateInternalRequest(c *gin.Context) bool {
	host := c.Request.RemoteAddr
	if splitHost, _, err := net.SplitHostPort(host); err == nil && splitHost != "" {
		host = splitHost
	}
	if isPrivateClientIP(host) {
		return true
	}
	c.JSON(http.StatusForbidden, map[string]string{"error": "private network access required"})
	return false
}

func isPrivateClientIP(raw string) bool {
	addr, err := netip.ParseAddr(strings.TrimSpace(raw))
	if err != nil {
		return false
	}
	return addr.IsLoopback() || addr.IsPrivate() || addr.IsLinkLocalUnicast()
}
