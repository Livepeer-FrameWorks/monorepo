package main

import (
	"context"
	"strconv"
	"time"

	"frameworks/api_assets/internal/cache"
	"frameworks/api_assets/internal/handlers"
	"frameworks/pkg/config"
	"frameworks/pkg/logging"
	"frameworks/pkg/monitoring"
	pb "frameworks/pkg/proto"
	"frameworks/pkg/qmbootstrap"
	"frameworks/pkg/server"
	"frameworks/pkg/version"

	qmclient "frameworks/pkg/clients/quartermaster"
)

func main() {
	if version.HandleCLI() {
		return
	}

	logger := logging.NewLoggerWithService("chandler")
	config.LoadEnv(logger)

	s3Bucket := config.GetEnv("STORAGE_S3_BUCKET", "")
	if s3Bucket == "" {
		logger.Warn("STORAGE_S3_BUCKET not set — asset requests will return 503 until configured")
	}
	s3Cfg := handlers.S3Config{
		Bucket:       s3Bucket,
		Prefix:       config.GetEnv("STORAGE_S3_PREFIX", ""),
		Region:       config.GetEnv("STORAGE_S3_REGION", "us-east-1"),
		Endpoint:     config.GetEnv("STORAGE_S3_ENDPOINT", ""),
		AccessKey:    config.GetEnv("STORAGE_S3_ACCESS_KEY", ""),
		SecretKey:    config.GetEnv("STORAGE_S3_SECRET_KEY", ""),
		ServiceToken: config.GetEnv("SERVICE_TOKEN", ""),
	}

	maxCacheBytes := int64(config.GetEnvInt("CACHE_MAX_BYTES", 50*1024*1024)) // 50MB default
	cacheTTL := time.Duration(config.GetEnvInt("CACHE_TTL_SECONDS", 30)) * time.Second
	lru := cache.NewLRU(maxCacheBytes, cacheTTL)

	healthChecker := monitoring.NewHealthChecker("chandler", version.Version)
	metricsCollector := monitoring.NewMetricsCollector("chandler", version.Version, version.GitCommit)

	cacheHits := metricsCollector.NewCounter("cache_hits_total", "Cache hit count", nil)
	cacheMisses := metricsCollector.NewCounter("cache_misses_total", "Cache miss count", nil)
	s3Errors := metricsCollector.NewCounter("s3_errors_total", "S3 fetch error count", nil)

	assetHandler, err := handlers.NewAssetHandler(s3Cfg, lru, logger, cacheHits.WithLabelValues(), cacheMisses.WithLabelValues(), s3Errors.WithLabelValues())
	if err != nil {
		logger.WithError(err).Fatal("Failed to create asset handler")
	}

	router := server.SetupServiceRouter(logger, "chandler", healthChecker, metricsCollector)
	assetHandler.RegisterRoutes(router)

	serverConfig := server.DefaultConfig("chandler", "18020")

	// Quartermaster bootstrap
	go func() {
		serviceToken := config.GetEnv("SERVICE_TOKEN", "")
		qmAddr := config.GetEnv("QUARTERMASTER_GRPC_ADDR",
			config.GetEnv("QUARTERMASTER_HOST", "quartermaster")+":"+config.GetEnv("QUARTERMASTER_GRPC_PORT", "19002"))

		qc, err := qmclient.NewGRPCClient(qmclient.GRPCConfig{
			GRPCAddr:      qmAddr,
			Timeout:       10 * time.Second,
			Logger:        logger,
			ServiceToken:  serviceToken,
			AllowInsecure: config.GetEnvBool("GRPC_ALLOW_INSECURE", false),
			CACertFile:    config.GetEnv("GRPC_TLS_CA_PATH", ""),
			ServerName:    config.GetEnv("GRPC_TLS_SERVER_NAME", ""),
		})
		if err != nil {
			logger.WithError(err).Warn("Failed to create Quartermaster gRPC client")
			return
		}
		defer func() { _ = qc.Close() }()

		healthEndpoint := "/health"
		httpPort, _ := strconv.Atoi(serverConfig.Port)
		if httpPort <= 0 || httpPort > 65535 {
			logger.Warn("Quartermaster bootstrap skipped: invalid port")
			return
		}
		advertiseHost := config.GetEnv("CHANDLER_HOST", "chandler")
		clusterID := config.GetEnv("CLUSTER_ID", "")

		req := &pb.BootstrapServiceRequest{
			Type:           "chandler",
			Version:        version.Version,
			Protocol:       "http",
			HealthEndpoint: &healthEndpoint,
			Port:           int32(httpPort),
			AdvertiseHost:  &advertiseHost,
		}
		if clusterID != "" {
			req.ClusterId = &clusterID
		}
		if nodeID := config.GetEnv("NODE_ID", ""); nodeID != "" {
			req.NodeId = &nodeID
		}

		if _, err := qmbootstrap.BootstrapServiceWithRetry(
			context.Background(),
			qc,
			req,
			logger,
			qmbootstrap.DefaultRetryConfig("chandler"),
		); err != nil {
			logger.WithError(err).Warn("Quartermaster bootstrap failed")
		} else {
			logger.Info("Quartermaster bootstrap ok")
		}
	}()

	if err := server.Start(serverConfig, router, logger); err != nil {
		logger.WithError(err).Fatal("Server startup failed")
	}
}
