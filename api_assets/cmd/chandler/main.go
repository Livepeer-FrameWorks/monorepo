package main

import (
	"context"
	"strconv"
	"time"

	"frameworks/api_assets/internal/cache"
	"frameworks/api_assets/internal/handlers"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/config"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/monitoring"
	pb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/qmbootstrap"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/server"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/version"

	qmclient "github.com/Livepeer-FrameWorks/monorepo/pkg/clients/quartermaster"
)

func main() {
	if version.HandleCLI() {
		return
	}

	logger := logging.NewLoggerWithService("chandler")
	config.LoadEnv(logger)

	serviceToken := config.GetEnv("SERVICE_TOKEN", "")
	qmAddr := config.GetEnv("QUARTERMASTER_GRPC_ADDR",
		config.GetEnv("QUARTERMASTER_HOST", "quartermaster")+":"+config.GetEnv("QUARTERMASTER_GRPC_PORT", "19002"))
	clusterID := config.GetEnv("CLUSTER_ID", "")

	// Bucket/endpoint/region default to env; per-cluster lookup below overrides
	// when CLUSTER_ID is set and Quartermaster is reachable. Credentials stay
	// env-sourced regardless — they are infrastructure secrets, not cluster-
	// row data.
	s3Cfg := handlers.S3Config{
		Bucket:       config.GetEnv("STORAGE_S3_BUCKET", ""),
		Prefix:       config.GetEnv("STORAGE_S3_PREFIX", ""),
		Region:       config.GetEnv("STORAGE_S3_REGION", "us-east-1"),
		Endpoint:     config.GetEnv("STORAGE_S3_ENDPOINT", ""),
		AccessKey:    config.GetEnv("STORAGE_S3_ACCESS_KEY", ""),
		SecretKey:    config.GetEnv("STORAGE_S3_SECRET_KEY", ""),
		ServiceToken: serviceToken,
	}

	if clusterID != "" {
		applyClusterS3FromQuartermaster(logger, qmAddr, serviceToken, clusterID, &s3Cfg)
	}
	if s3Cfg.Bucket == "" {
		logger.Warn("S3 bucket not configured (no cluster row, no env) — asset requests will return 503 until configured")
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

// applyClusterS3FromQuartermaster overrides bucket/endpoint/region with the
// values stored on the local cluster's infrastructure_clusters row. Best-
// effort: if Quartermaster is unreachable or returns no row, the existing
// env-defaulted s3Cfg stands. Credentials are never sourced from Quartermaster
// — they are infrastructure secrets and stay env-only.
func applyClusterS3FromQuartermaster(logger logging.Logger, qmAddr, serviceToken, clusterID string, s3Cfg *handlers.S3Config) {
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
		logger.WithError(err).Warn("Quartermaster gRPC client unavailable; falling back to env S3 config")
		return
	}
	defer func() { _ = qc.Close() }()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	resp, err := qc.GetCluster(ctx, clusterID)
	switch {
	case err != nil:
		logger.WithError(err).WithField("cluster_id", clusterID).
			Warn("Quartermaster GetCluster failed; falling back to env S3 config")
		return
	case resp == nil || resp.GetCluster() == nil:
		logger.WithField("cluster_id", clusterID).
			Warn("Quartermaster returned no cluster row; falling back to env S3 config")
		return
	}
	cluster := resp.GetCluster()
	if v := cluster.GetS3Bucket(); v != "" {
		s3Cfg.Bucket = v
	}
	if v := cluster.GetS3Endpoint(); v != "" {
		s3Cfg.Endpoint = v
	}
	if v := cluster.GetS3Region(); v != "" {
		s3Cfg.Region = v
	}
}
