package main

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	"frameworks/api_assets/internal/cache"
	"frameworks/api_assets/internal/handlers"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/config"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/monitoring"
	quartermasterpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/quartermaster"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/qmbootstrap"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/server"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/servicedefs"
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

	s3Cfg := handlers.S3Config{
		Bucket:       config.GetEnv("STORAGE_S3_BUCKET", ""),
		Prefix:       config.GetEnv("STORAGE_S3_PREFIX", ""),
		Region:       config.GetEnv("STORAGE_S3_REGION", "us-east-1"),
		Endpoint:     config.GetEnv("STORAGE_S3_ENDPOINT", ""),
		AccessKey:    config.GetEnv("STORAGE_S3_ACCESS_KEY", ""),
		SecretKey:    config.GetEnv("STORAGE_S3_SECRET_KEY", ""),
		ServiceToken: serviceToken,
	}

	loadAuthority := func() error { return applyClusterS3FromQuartermaster(logger, qmAddr, serviceToken, clusterID, &s3Cfg) }
	if err := resolveChandlerS3Serving(clusterID, config.GetEnvBool("CHANDLER_DEV_ALLOW_ENV_S3", false), loadAuthority, &s3Cfg, logger); err != nil {
		// Only a genuine authority-lookup failure returns an error; Chandler cannot obtain its authority, so fail closed.
		// A restart re-attempts once Quartermaster is reachable.
		logger.WithError(err).WithField("cluster_id", clusterID).Fatal("Cluster S3 lookup failed — cannot resolve the authoritative storage descriptor")
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
			ServerName:    config.GetServiceGRPCTLSServerName("quartermaster"),
		})
		if err != nil {
			logger.WithError(err).Warn("Failed to create Quartermaster gRPC client")
			return
		}
		defer func() { _ = qc.Close() }()

		// Advertise READINESS, not liveness: Quartermaster must consider this
		// instance serviceable only when it can read its immutable backend. /health
		// is up before the store is proven; /ready (servicedefs ReadyPath) is the
		// store-backed probe. Keep the two distinct so a bad descriptor deregisters
		// serving without flapping process liveness.
		healthEndpoint := "/ready"
		if def, ok := servicedefs.Lookup("chandler"); ok {
			healthEndpoint = def.ReadinessPath()
		}
		httpPort, _ := strconv.Atoi(serverConfig.Port)
		if httpPort <= 0 || httpPort > 65535 {
			logger.Warn("Quartermaster bootstrap skipped: invalid port")
			return
		}
		advertiseHost := config.GetEnv("CHANDLER_HOST", "chandler")
		clusterID := config.GetEnv("CLUSTER_ID", "")

		req := &quartermasterpb.BootstrapServiceRequest{
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

	server.RegisterEnvFileReload("chandler", logger)
	if err := server.Start(serverConfig, router, logger); err != nil {
		logger.WithError(err).Fatal("Server startup failed")
	}
}

// applyClusterS3FromQuartermaster loads the local cluster's storage placement.
// Credentials remain env-only infrastructure secrets.
func applyClusterS3FromQuartermaster(logger logging.Logger, qmAddr, serviceToken, clusterID string, s3Cfg *handlers.S3Config) error {
	qc, err := qmclient.NewGRPCClient(qmclient.GRPCConfig{
		GRPCAddr:      qmAddr,
		Timeout:       10 * time.Second,
		Logger:        logger,
		ServiceToken:  serviceToken,
		AllowInsecure: config.GetEnvBool("GRPC_ALLOW_INSECURE", false),
		CACertFile:    config.GetEnv("GRPC_TLS_CA_PATH", ""),
		ServerName:    config.GetServiceGRPCTLSServerName("quartermaster"),
	})
	if err != nil {
		return fmt.Errorf("quartermaster client: %w", err)
	}
	defer func() { _ = qc.Close() }()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	resp, err := qc.GetCluster(ctx, clusterID)
	switch {
	case err != nil:
		return fmt.Errorf("get cluster: %w", err)
	case resp == nil || resp.GetCluster() == nil:
		return fmt.Errorf("cluster row not found")
	}
	cluster := resp.GetCluster()
	if cluster.GetS3Bucket() == "" {
		return errClusterNoS3Descriptor
	}
	// INCOMPLETE-DESCRIPTOR FENCE (mirrors Foghorn's first-boot guard): a row with a bucket but an unset
	// (NULL) s3_prefix is NOT a complete immutable descriptor — a COALESCE'd empty prefix is indistinguishable
	// from a known-empty one, so adopting it could make Chandler address the bucket ROOT while Foghorn writes
	// under a real, non-empty prefix. Fail closed (treat as no-descriptor → serve 503) until the prefix is
	// established by desired-state bootstrap, so Chandler never serves the wrong keyspace
	// during an incomplete/mixed Quartermaster rollout.
	if !cluster.GetS3PrefixPresent() {
		return errClusterNoS3Descriptor
	}
	// Quartermaster owns the full immutable descriptor tuple. Adopt bucket,
	// endpoint, and prefix VERBATIM — including legitimately empty endpoint/prefix
	// — so Chandler addresses EXACTLY the keyspace Foghorn established against the
	// same row; conditionally retaining env values for these would let the two
	// diverge. Region applies the shared empty→us-east-1 default, matching
	// Foghorn's effective descriptor. Credentials remain env-only secrets.
	s3Cfg.Bucket = cluster.GetS3Bucket()
	s3Cfg.Endpoint = cluster.GetS3Endpoint()
	s3Cfg.Prefix = cluster.GetS3Prefix()
	if r := cluster.GetS3Region(); r != "" {
		s3Cfg.Region = r
	} else {
		s3Cfg.Region = "us-east-1"
	}
	return nil
}

// errClusterNoS3Descriptor signals that the cluster row exists but declares no
// S3 backend — S3 is not configured for this cell (distinct from a lookup
// failure). The caller treats it as "disabled", not fatal.
var errClusterNoS3Descriptor = errors.New("cluster row declares no s3 descriptor")

// resolveChandlerS3Serving decides Chandler's final S3 serving descriptor and writes it into s3Cfg in place (S3
// disabled = empty bucket). Chandler is intrinsically CELL-SCOPED, so:
//   - With a CLUSTER_ID: loadAuthority() fetches the authoritative Quartermaster descriptor. A missing or incomplete
//     descriptor disables S3 (serve 503 until established); an authority-lookup failure returns an error (the caller fails
//     closed / restarts). There is NO env fallback — serving an env backend the authority never validated is forbidden.
//   - Without a CLUSTER_ID but with an env bucket: FAIL CLOSED (disable S3) — a production deploy always renders
//     CLUSTER_ID, so this is a malformed/manual config and serving that unauthorized keyspace is refused. An explicit
//     dev opt-in (allowEnvS3, from CHANDLER_DEV_ALLOW_ENV_S3) keeps a cluster-less local bring-up working.
//
// Extracted from main() so this authority/fail-closed decision is unit-testable without a live Quartermaster.
func resolveChandlerS3Serving(clusterID string, allowEnvS3 bool, loadAuthority func() error, s3Cfg *handlers.S3Config, logger logging.Logger) error {
	if clusterID != "" {
		switch err := loadAuthority(); {
		case err == nil:
			// Loaded the authoritative descriptor from Quartermaster.
		case errors.Is(err, errClusterNoS3Descriptor):
			s3Cfg.Bucket, s3Cfg.Endpoint, s3Cfg.Prefix = "", "", ""
			logger.WithField("cluster_id", clusterID).Info("Cluster row declares no complete S3 descriptor — S3 disabled for this cell")
		default:
			return fmt.Errorf("cluster S3 lookup failed: %w", err)
		}
		return nil
	}
	if s3Cfg.Bucket != "" {
		if allowEnvS3 {
			logger.Warn("CHANDLER_DEV_ALLOW_ENV_S3 set: serving the env S3 descriptor WITHOUT a CLUSTER_ID (dev only; bypasses the Quartermaster authority)")
			return nil
		}
		s3Cfg.Bucket, s3Cfg.Endpoint, s3Cfg.Prefix = "", "", ""
		logger.Error("CLUSTER_ID is empty but an env S3 bucket is set — refusing to serve an unauthorized keyspace; S3 disabled (503). Set CLUSTER_ID, or CHANDLER_DEV_ALLOW_ENV_S3=true for a cluster-less dev bring-up.")
	}
	return nil
}
