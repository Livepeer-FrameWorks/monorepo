package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"os"
	"strconv"
	"strings"
	"time"

	"frameworks/api_dns/internal/logic"
	"frameworks/api_dns/internal/provider/bunny"
	"frameworks/api_dns/internal/provider/cloudflare"
	"frameworks/api_dns/internal/store"
	"frameworks/api_dns/internal/worker"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/clients/quartermaster"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/config"
	fieldcrypt "github.com/Livepeer-FrameWorks/monorepo/pkg/crypto"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/database"
	pkgdns "github.com/Livepeer-FrameWorks/monorepo/pkg/dns"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/grpcutil"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/middleware"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/monitoring"
	pb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/qmbootstrap"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/server"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/version"

	"errors"
	"github.com/gin-gonic/gin"
	"github.com/prometheus/client_golang/prometheus"
	"google.golang.org/grpc"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/health"
	"google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/reflection"
	"google.golang.org/grpc/status"
)

// ServerMetrics holds Prometheus metrics for the gRPC server
type ServerMetrics struct {
	DNSOperations  *prometheus.CounterVec
	CertOperations *prometheus.CounterVec
	GRPCRequests   *prometheus.CounterVec
	GRPCDuration   *prometheus.HistogramVec
}

// NavigatorServer holds dependencies for the gRPC and HTTP server
type NavigatorServer struct {
	pb.UnimplementedNavigatorServiceServer
	DNSManager        *logic.DNSManager
	CertManager       *logic.CertManager
	InternalCAManager *logic.InternalCAManager
	AliasPublisher    *worker.AliasApplyStateWorker
	Quartermaster     *quartermaster.GRPCClient
	Logger            logging.Logger
	Metrics           *ServerMetrics
	// RootDomain is the operator base domain (e.g. "frameworks.network").
	// Custom-domain RPCs use it to build the canonical CNAME instructions
	// returned to the dashboard.
	RootDomain string
}

func main() {
	if version.HandleCLI() {
		return
	}

	// Setup logger
	logger := logging.NewLoggerWithService("navigator")

	// Load environment variables
	config.LoadEnv(logger)

	logger.Info("Starting Navigator (Public DNS Manager and Certificate Authority)")

	// Service token for service-to-service authentication
	serviceToken := config.RequireEnv("SERVICE_TOKEN")

	dbURL := config.RequireEnv("DATABASE_URL")

	// === Database Connection ===
	dbConfig := database.DefaultConfig()
	dbConfig.URL = dbURL
	db := database.MustConnect(dbConfig, logger)
	defer db.Close()

	encKey := config.RequireEnv("FIELD_ENCRYPTION_KEY")
	keyEncryptor, err := fieldcrypt.DeriveFieldEncryptor([]byte(encKey), "navigator-private-keys")
	if err != nil {
		logger.WithError(err).Fatal("Failed to derive field encryption key")
	}

	// Initialize Store
	certStore := store.NewStore(db, keyEncryptor)

	// === Configuration Loading ===
	// Cloudflare config
	cfConfig, err := cloudflare.LoadConfig()
	if err != nil {
		logger.WithError(err).Fatal("Failed to load Cloudflare configuration")
	}
	configureCloudflareACMETokenAlias()
	cfClient := cloudflare.NewClientFromConfig(cfConfig)
	bunnyClient := bunny.NewClientFromConfig(bunny.LoadConfig())
	if bunnyClient == nil {
		logger.WithField("services", pkgdns.BunnyManagedServiceTypes()).Warn("BUNNY_API_KEY not configured; media cluster DNS will use explicit Cloudflare fallback")
	}

	// Quartermaster gRPC client
	qmGRPCAddr := config.GetEnv("QUARTERMASTER_GRPC_ADDR", "quartermaster:19002")
	qmClient, err := quartermaster.NewGRPCClient(quartermaster.GRPCConfig{
		GRPCAddr:      qmGRPCAddr,
		Timeout:       10 * time.Second,
		Logger:        logger,
		ServiceToken:  serviceToken,
		AllowInsecure: config.GetEnvBool("GRPC_ALLOW_INSECURE", false),
		CACertFile:    config.GetEnv("GRPC_TLS_CA_PATH", ""),
		ServerName:    config.GetServiceGRPCTLSServerName("quartermaster"),
	})
	if err != nil {
		logger.WithError(err).Fatal("Failed to create Quartermaster gRPC client")
	}
	defer qmClient.Close()

	// === Logic Initialization ===
	rootDomain := config.RequireEnv("BRAND_DOMAIN")
	acmeEmail := config.RequireEnv("ACME_EMAIL")

	recordTTL := config.GetEnvInt("NAVIGATOR_DNS_TTL_A_RECORD", 60)
	lbTTL := config.GetEnvInt("NAVIGATOR_DNS_TTL_LB", 60)
	staleSeconds := config.GetEnvInt("NAVIGATOR_DNS_HEALTH_STALE_SECONDS", 300)
	monitorConfig := logic.MonitorConfig{
		Interval: config.GetEnvInt("NAVIGATOR_CF_MONITOR_INTERVAL", 60),
		Timeout:  config.GetEnvInt("NAVIGATOR_CF_MONITOR_TIMEOUT", 5),
		Retries:  config.GetEnvInt("NAVIGATOR_CF_MONITOR_RETRIES", 2),
	}
	dnsManager := logic.NewDNSManager(cfClient, qmClient, logger, rootDomain, recordTTL, lbTTL, time.Duration(staleSeconds)*time.Second, monitorConfig)
	dnsManager.SetBunnyClient(bunnyClient)
	certManager := logic.NewCertManager(certStore)
	if bunnyClient != nil {
		certManager.UseBunnyForClusterZones(rootDomain)
	}
	internalCAManager := logic.NewInternalCAManager(certStore, qmClient, logger, rootDomain)
	dnsManager.SetCertChecker(certManager)
	if err := internalCAManager.EnsureCA(context.Background()); err != nil {
		logger.WithError(err).Fatal("Failed to initialize internal CA")
	}

	// === Background Workers ===
	renewalWorker := worker.NewRenewalWorker(certStore, certManager, logger, acmeEmail)
	go renewalWorker.Start(context.Background())
	reconcileIntervalSeconds := config.GetEnvInt("NAVIGATOR_DNS_RECONCILE_INTERVAL_SECONDS", 60)
	reconciler := worker.NewDNSReconciler(dnsManager, certManager, qmClient, logger, time.Duration(reconcileIntervalSeconds)*time.Second, rootDomain, acmeEmail, pkgdns.ManagedServiceTypes())
	go reconciler.Start(context.Background())

	// Tenant alias worker reconciles DNS from Navigator's durable
	// per-edge ACK state. Foghorn reports ACKs through Navigator gRPC.
	tenantZoneLabel := logic.TenantAliasZoneLabel
	aliasWorkerIntervalSeconds := config.GetEnvInt("NAVIGATOR_ALIAS_APPLY_STATE_INTERVAL_SECONDS", 15)
	aliasWorker := worker.NewAliasApplyStateWorker(
		certStore,
		bunnyClient,
		quartermasterEdgeResolver{qm: qmClient},
		logger,
		time.Duration(aliasWorkerIntervalSeconds)*time.Second,
		rootDomain,
		tenantZoneLabel,
	)
	go aliasWorker.Start(context.Background())

	// Setup monitoring
	healthChecker := monitoring.NewHealthChecker("navigator", version.Version)
	metricsCollector := monitoring.NewMetricsCollector("navigator", version.Version, version.GitCommit)

	// Create gRPC server metrics
	serverMetrics := &ServerMetrics{
		DNSOperations:  metricsCollector.NewCounter("dns_operations_total", "DNS operations", []string{"operation", "status"}),
		CertOperations: metricsCollector.NewCounter("cert_operations_total", "Certificate operations", []string{"operation", "status"}),
		GRPCRequests:   metricsCollector.NewCounter("grpc_requests_total", "Total gRPC requests", []string{"method", "status"}),
		GRPCDuration:   metricsCollector.NewHistogram("grpc_request_duration_seconds", "gRPC request duration", []string{"method"}, nil),
	}

	// === Server Setup ===
	navigatorServer := &NavigatorServer{
		DNSManager:        dnsManager,
		CertManager:       certManager,
		InternalCAManager: internalCAManager,
		AliasPublisher:    aliasWorker,
		Quartermaster:     qmClient,
		Logger:            logger,
		Metrics:           serverMetrics,
		RootDomain:        rootDomain,
	}

	healthChecker.AddCheck("database", monitoring.DatabaseHealthCheck(db))
	healthChecker.AddCheck("cloudflare_connectivity", func() monitoring.CheckResult {
		if cfClient == nil {
			return monitoring.CheckResult{
				Status:  "unhealthy",
				Message: "cloudflare client not initialized",
			}
		}
		return monitoring.CheckResult{
			Status:  "healthy",
			Message: "cloudflare client ready",
		}
	})

	// === gRPC Server ===
	go func() {
		grpcPort := config.RequireEnv("NAVIGATOR_GRPC_PORT")

		lis, err := net.Listen("tcp", ":"+grpcPort)
		if err != nil {
			logger.WithError(err).Fatal("Failed to listen for gRPC")
		}

		// Auth interceptor for service-to-service calls
		authInterceptor := middleware.GRPCAuthInterceptor(middleware.GRPCAuthConfig{
			ServiceToken: serviceToken,
			Logger:       logger,
			SkipMethods: []string{
				"/grpc.health.v1.Health/Check",
				"/grpc.health.v1.Health/Watch",
			},
		})

		grpcCertFile := strings.TrimSpace(config.GetEnv("GRPC_TLS_CERT_PATH", ""))
		grpcKeyFile := strings.TrimSpace(config.GetEnv("GRPC_TLS_KEY_PATH", ""))
		tlsCfg := grpcutil.ServerTLSConfig{
			CertFile:      grpcCertFile,
			KeyFile:       grpcKeyFile,
			AllowInsecure: grpcCertFile == "" && grpcKeyFile == "",
		}
		if caErr := internalCAManager.EnsureLocalServerCertificate(context.Background(), "navigator", grpcCertFile, grpcKeyFile); caErr != nil {
			logger.WithError(caErr).Fatal("Failed to stage Navigator bootstrap gRPC certificate")
		}
		waitCtx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		if waitErr := grpcutil.WaitForServerTLSFiles(waitCtx, tlsCfg, logger); waitErr != nil {
			logger.WithError(waitErr).Fatal("Timed out waiting for Navigator gRPC TLS files")
		}
		grpcTLSOpt, err := grpcutil.ServerTLS(tlsCfg, logger)
		if err != nil {
			logger.WithError(err).Fatal("Failed to configure Navigator gRPC TLS")
		}
		if grpcTLSOpt == nil {
			logger.Warn("Navigator gRPC is running without TLS; private keys require a private network path.")
		}

		// GRPCMetricsInterceptor sits outermost so Unauthenticated /
		// PermissionDenied rejections from authInterceptor / private-peer
		// still show up in navigator_grpc_requests_total.
		serverOpts := []grpc.ServerOption{
			grpc.ChainUnaryInterceptor(
				middleware.GRPCMetricsInterceptor(serverMetrics.GRPCRequests, serverMetrics.GRPCDuration),
				grpcutil.SanitizeUnaryServerInterceptor(),
				authInterceptor,
				requirePrivatePeerUnaryInterceptor(),
			),
		}
		if grpcTLSOpt != nil {
			serverOpts = append(serverOpts, grpcTLSOpt)
		}
		grpcServer := grpc.NewServer(serverOpts...)
		pb.RegisterNavigatorServiceServer(grpcServer, navigatorServer)

		// gRPC health service so external probes can use gRPC health checks
		hs := health.NewServer()
		hs.SetServingStatus("", grpc_health_v1.HealthCheckResponse_SERVING)
		hs.SetServingStatus(pb.NavigatorService_ServiceDesc.ServiceName, grpc_health_v1.HealthCheckResponse_SERVING)
		grpc_health_v1.RegisterHealthServer(grpcServer, hs)
		reflection.Register(grpcServer)

		logger.WithField("port", grpcPort).Info("Navigator gRPC server starting...")
		if err := grpcServer.Serve(lis); err != nil {
			logger.WithError(err).Fatal("Navigator gRPC server failed")
		}
	}()

	// === HTTP Server ===
	serverConfig := server.DefaultConfig("navigator", config.RequireEnv("NAVIGATOR_PORT"))
	serverConfig.TLSCertFile = config.GetEnv("NAVIGATOR_HTTP_TLS_CERT_FILE", "")
	serverConfig.TLSKeyFile = config.GetEnv("NAVIGATOR_HTTP_TLS_KEY_FILE", "")

	app := server.SetupServiceRouter(logger, "navigator", healthChecker, metricsCollector)
	app.GET("/status", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"status": "running", "version": version.Version})
	})
	app.GET("/internal/tls-bundles/:bundleID", func(c *gin.Context) {
		if !requirePrivateInternalRequest(c) {
			return
		}
		authz := strings.TrimSpace(c.GetHeader("Authorization"))
		if authz != "Bearer "+serviceToken {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
			return
		}

		bundleID := strings.TrimSpace(c.Param("bundleID"))
		if bundleID == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "bundle_id is required"})
			return
		}

		bundle, err := certManager.GetTLSBundle(c.Request.Context(), bundleID)
		if err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
			return
		}

		hash := sha256.Sum256([]byte(bundle.CertPEM + bundle.KeyPEM))
		c.JSON(http.StatusOK, gin.H{
			"bundle_id":  bundle.BundleID,
			"domains":    bundle.Domains,
			"cert_pem":   bundle.CertPEM,
			"key_pem":    bundle.KeyPEM,
			"expires_at": bundle.ExpiresAt.Unix(),
			"version":    hex.EncodeToString(hash[:]),
		})
	})

	// Best-effort service registration in Quartermaster
	go func() {
		grpcPortStr := config.GetEnv("NAVIGATOR_GRPC_PORT", "19004")
		grpcPortInt, err := strconv.Atoi(grpcPortStr)
		if err != nil || grpcPortInt <= 0 || grpcPortInt > 65535 {
			logger.Warn("Quartermaster bootstrap skipped: invalid port")
			return
		}
		advertiseHost := config.GetEnv("NAVIGATOR_HOST", "navigator")
		clusterID := config.GetEnv("CLUSTER_ID", "")
		req := &pb.BootstrapServiceRequest{
			Type:          "navigator",
			Version:       version.Version,
			Protocol:      "grpc",
			Port:          int32(grpcPortInt),
			AdvertiseHost: &advertiseHost,
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
		if _, err := qmbootstrap.BootstrapServiceWithRetry(context.Background(), qmClient, req, logger, qmbootstrap.DefaultRetryConfig("navigator")); err != nil {
			logger.WithError(err).Warn("Quartermaster bootstrap (navigator) failed")
		} else {
			logger.Info("Quartermaster bootstrap (navigator) ok")
		}
	}()

	server.RegisterEnvFileReload("navigator", logger)
	if err := server.Start(serverConfig, app, logger); err != nil {
		logger.WithError(err).Fatal("Navigator HTTP server failed")
	}
}

func configureCloudflareACMETokenAlias() {
	if os.Getenv("CLOUDFLARE_DNS_API_TOKEN") != "" || os.Getenv("CLOUDFLARE_API_TOKEN") == "" {
		return
	}
	_ = os.Setenv("CLOUDFLARE_DNS_API_TOKEN", os.Getenv("CLOUDFLARE_API_TOKEN"))
}

func requirePrivatePeerUnaryInterceptor() grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req interface{}, _ *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (interface{}, error) {
		p, ok := peer.FromContext(ctx)
		if !ok || p.Addr == nil {
			return nil, status.Error(codes.PermissionDenied, "navigator gRPC requires a private network peer")
		}

		host := p.Addr.String()
		if splitHost, _, err := net.SplitHostPort(host); err == nil && splitHost != "" {
			host = splitHost
		}
		if !isPrivateClientIP(host) {
			return nil, status.Error(codes.PermissionDenied, "navigator gRPC requires a private network peer")
		}

		return handler(ctx, req)
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
	c.JSON(http.StatusForbidden, gin.H{"error": "private network access required"})
	return false
}

func isPrivateClientIP(raw string) bool {
	addr, err := netip.ParseAddr(strings.TrimSpace(raw))
	if err != nil {
		return false
	}
	return addr.IsLoopback() || addr.IsPrivate() || addr.IsLinkLocalUnicast()
}

// SyncDNS implements the gRPC SyncDNS method
func (s *NavigatorServer) SyncDNS(ctx context.Context, req *pb.SyncDNSRequest) (*pb.SyncDNSResponse, error) {
	s.Logger.WithField("service_type", req.GetServiceType()).Info("Received SyncDNS request")

	partialErrors, err := s.DNSManager.SyncService(ctx, req.GetServiceType(), req.GetRootDomain())
	if err != nil {
		s.Logger.WithError(err).Error("DNS sync failed")
		return &pb.SyncDNSResponse{
			Success: false,
			Message: fmt.Sprintf("Sync failed: %v", err),
			Errors:  partialErrors,
		}, nil
	}

	if len(partialErrors) > 0 {
		return &pb.SyncDNSResponse{
			Success: false,
			Message: "Sync completed with errors",
			Errors:  partialErrors,
		}, nil
	}

	return &pb.SyncDNSResponse{
		Success: true,
		Message: "DNS sync completed successfully",
	}, nil
}

// IssueCertificate implements the gRPC IssueCertificate method
func (s *NavigatorServer) IssueCertificate(ctx context.Context, req *pb.IssueCertificateRequest) (*pb.IssueCertificateResponse, error) {
	// Extract optional tenant_id from request
	tenantID := ""
	if req.TenantId != nil {
		tenantID = *req.TenantId
	}

	log := s.Logger.WithField("domain", req.GetDomain())
	if tenantID != "" {
		log = log.WithField("tenant_id", tenantID)
	}
	log.Info("Received IssueCertificate request")

	certPEM, keyPEM, expiresAt, err := s.CertManager.IssueCertificate(ctx, tenantID, req.GetDomain(), req.GetEmail())
	if err != nil {
		log.WithError(err).Error("Certificate issuance failed")
		return &pb.IssueCertificateResponse{
			Success: false,
			Error:   err.Error(),
		}, nil
	}

	return &pb.IssueCertificateResponse{
		Success:   true,
		Message:   "Certificate issued successfully",
		TenantId:  req.TenantId,
		Domain:    req.GetDomain(),
		CertPem:   certPEM,
		KeyPem:    keyPEM,
		ExpiresAt: expiresAt.Unix(),
	}, nil
}

// GetCertificate implements the gRPC GetCertificate method
func (s *NavigatorServer) GetCertificate(ctx context.Context, req *pb.GetCertificateRequest) (*pb.GetCertificateResponse, error) {
	// Extract optional tenant_id from request
	tenantID := ""
	if req.TenantId != nil {
		tenantID = *req.TenantId
	}

	log := s.Logger.WithField("domain", req.GetDomain())
	if tenantID != "" {
		log = log.WithField("tenant_id", tenantID)
	}
	log.Info("Received GetCertificate request")

	cert, err := s.CertManager.GetCertificate(ctx, tenantID, req.GetDomain())
	if err != nil {
		log.WithError(err).Info("Certificate not found")
		return &pb.GetCertificateResponse{
			Found: false,
			Error: err.Error(),
		}, nil
	}

	// Return tenant_id if set
	var respTenantID *string
	if cert.TenantID.Valid {
		respTenantID = &cert.TenantID.String
	}

	return &pb.GetCertificateResponse{
		Found:     true,
		TenantId:  respTenantID,
		Domain:    cert.Domain,
		CertPem:   cert.CertPEM,
		KeyPem:    cert.KeyPEM,
		ExpiresAt: cert.ExpiresAt.Unix(),
	}, nil
}

func (s *NavigatorServer) GetTLSBundle(ctx context.Context, req *pb.GetTLSBundleRequest) (*pb.GetTLSBundleResponse, error) {
	log := s.Logger.WithField("bundle_id", req.GetBundleId())
	log.Info("Received GetTLSBundle request")

	bundle, err := s.CertManager.GetTLSBundle(ctx, req.GetBundleId())
	if err != nil {
		log.WithError(err).Info("TLS bundle not found")
		return &pb.GetTLSBundleResponse{
			Found: false,
			Error: err.Error(),
		}, nil
	}

	hash := sha256.Sum256([]byte(bundle.CertPEM + bundle.KeyPEM))
	return &pb.GetTLSBundleResponse{
		Found:     true,
		BundleId:  bundle.BundleID,
		Domains:   bundle.Domains,
		CertPem:   bundle.CertPEM,
		KeyPem:    bundle.KeyPEM,
		ExpiresAt: bundle.ExpiresAt.Unix(),
		Version:   hex.EncodeToString(hash[:]),
	}, nil
}

func (s *NavigatorServer) GetCABundle(ctx context.Context, _ *pb.GetCABundleRequest) (*pb.GetCABundleResponse, error) {
	caPEM, err := s.InternalCAManager.GetCABundle(ctx)
	if err != nil {
		s.Logger.WithError(err).Error("Failed to get internal CA bundle")
		return &pb.GetCABundleResponse{
			Found: false,
			Error: err.Error(),
		}, nil
	}

	return &pb.GetCABundleResponse{
		Found: true,
		CaPem: caPEM,
	}, nil
}

// EnsureTenantAlias implements the gRPC EnsureTenantAlias method.
// Idempotent: persists alias intent and queues async ACME work.
func (s *NavigatorServer) EnsureTenantAlias(ctx context.Context, req *pb.EnsureTenantAliasRequest) (*pb.EnsureTenantAliasResponse, error) {
	tenantID := req.GetTenantId()
	subdomain := req.GetSubdomain()
	log := s.Logger.WithField("tenant_id", tenantID).WithField("subdomain", subdomain)
	log.Info("Received EnsureTenantAlias request")

	alias, err := s.CertManager.EnsureTenantAlias(ctx, tenantID, subdomain)
	if err != nil {
		log.WithError(err).Warn("Failed to persist tenant alias intent")
		return &pb.EnsureTenantAliasResponse{Error: err.Error()}, nil
	}
	return &pb.EnsureTenantAliasResponse{
		Accepted: true,
		Status:   alias.Status,
	}, nil
}

// RemoveTenantAlias implements the gRPC RemoveTenantAlias method.
// Idempotent: marks alias for teardown; worker cleans up DNS + state.
func (s *NavigatorServer) RemoveTenantAlias(ctx context.Context, req *pb.RemoveTenantAliasRequest) (*pb.RemoveTenantAliasResponse, error) {
	if err := s.CertManager.RemoveTenantAlias(ctx, req.GetTenantId()); err != nil {
		s.Logger.WithError(err).WithField("tenant_id", req.GetTenantId()).Warn("Failed to mark tenant alias for teardown")
		return &pb.RemoveTenantAliasResponse{}, nil
	}
	return &pb.RemoveTenantAliasResponse{Accepted: true}, nil
}

// GetTenantAliasStatus implements the gRPC GetTenantAliasStatus method.
// Returns found=false for tenants without an alias intent (the
// not-found case is treated as a normal "no row" response, not an
// error; callers like the webapp check Found to decide what to show).
func (s *NavigatorServer) GetTenantAliasStatus(ctx context.Context, req *pb.GetTenantAliasStatusRequest) (*pb.GetTenantAliasStatusResponse, error) {
	alias, err := s.CertManager.GetTenantAlias(ctx, req.GetTenantId())
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return &pb.GetTenantAliasStatusResponse{Found: false}, nil
		}
		s.Logger.WithError(err).WithField("tenant_id", req.GetTenantId()).Warn("GetTenantAliasStatus lookup failed")
		return nil, status.Errorf(codes.Internal, "lookup failed: %v", err)
	}
	resp := &pb.GetTenantAliasStatusResponse{
		Found:     true,
		TenantId:  alias.TenantID,
		Subdomain: alias.Subdomain,
		Status:    alias.Status,
	}
	dnsReady, readyErr := s.CertManager.TenantAliasDNSReady(ctx, req.GetTenantId())
	if readyErr != nil {
		s.Logger.WithError(readyErr).WithField("tenant_id", req.GetTenantId()).Debug("Tenant alias DNS readiness lookup failed")
	}
	resp.DnsReady = dnsReady
	if alias.CertIssuedAt.Valid {
		resp.CertIssuedAt = alias.CertIssuedAt.Time.Unix()
	}
	if alias.LastError.Valid {
		resp.LastError = alias.LastError.String
	}
	return resp, nil
}

// ReportConfigSeedApplyResult persists edge cert readiness ACKs observed
// by Foghorn, then reconciles affected tenant DNS immediately.
func (s *NavigatorServer) ReportConfigSeedApplyResult(ctx context.Context, req *pb.ReportConfigSeedApplyResultRequest) (*pb.ReportConfigSeedApplyResultResponse, error) {
	appliedAt := time.Unix(req.GetAppliedAt(), 0).UTC()
	appliedBundleIDs, failedBundleIDs := s.filterTenantBundlesForCluster(ctx, req.GetClusterId(), req.GetAppliedBundleIds(), req.GetFailedBundleIds())
	affected, err := s.CertManager.RecordConfigSeedApplyResult(ctx,
		req.GetNodeId(),
		req.GetClusterId(),
		req.GetSeedVersion(),
		appliedBundleIDs,
		failedBundleIDs,
		appliedAt,
	)
	if err != nil {
		s.Logger.WithError(err).WithField("node_id", req.GetNodeId()).Warn("Failed to record ConfigSeed apply result")
		return nil, status.Errorf(codes.Internal, "record apply result: %v", err)
	}
	if s.AliasPublisher != nil {
		for _, tenantID := range affected {
			if pubErr := s.AliasPublisher.PublishTenantAlias(ctx, tenantID); pubErr != nil {
				s.Logger.WithError(pubErr).WithField("tenant_id", tenantID).Warn("Failed to publish tenant alias after apply ACK")
			}
		}
	}
	return &pb.ReportConfigSeedApplyResultResponse{
		Accepted:          true,
		AffectedTenantIds: affected,
	}, nil
}

func (s *NavigatorServer) filterTenantBundlesForCluster(ctx context.Context, clusterID string, applied, failed []string) ([]string, []string) {
	if s.Quartermaster == nil || strings.TrimSpace(clusterID) == "" {
		return filterNonTenantBundles(applied), filterNonTenantBundles(failed)
	}
	lookupCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	resp, err := s.Quartermaster.ListAliasedTenantsForCluster(lookupCtx, clusterID)
	if err != nil {
		s.Logger.WithError(err).WithField("cluster_id", clusterID).Debug("Skipping tenant ACKs because active tenant lookup failed")
		return filterNonTenantBundles(applied), filterNonTenantBundles(failed)
	}
	allowed := map[string]struct{}{}
	for _, ref := range resp.GetTenants() {
		allowed[ref.GetTenantId()] = struct{}{}
	}
	return filterBundlesForAllowedTenants(applied, allowed), filterBundlesForAllowedTenants(failed, allowed)
}

func filterBundlesForAllowedTenants(bundleIDs []string, allowed map[string]struct{}) []string {
	out := make([]string, 0, len(bundleIDs))
	for _, bundleID := range bundleIDs {
		tenantID, ok := strings.CutPrefix(bundleID, "tenant:")
		if ok {
			if _, allowedTenant := allowed[tenantID]; !allowedTenant {
				continue
			}
		}
		out = append(out, bundleID)
	}
	return out
}

func filterNonTenantBundles(bundleIDs []string) []string {
	out := make([]string, 0, len(bundleIDs))
	for _, bundleID := range bundleIDs {
		if strings.HasPrefix(bundleID, "tenant:") {
			continue
		}
		out = append(out, bundleID)
	}
	return out
}

// RemoveTenantAliasCluster drops one cluster's edges from a tenant's DNS
// eligibility before future ConfigSeeds omit that tenant cert.
func (s *NavigatorServer) RemoveTenantAliasCluster(ctx context.Context, req *pb.RemoveTenantAliasClusterRequest) (*pb.RemoveTenantAliasClusterResponse, error) {
	if err := s.CertManager.RemoveTenantAliasCluster(ctx, req.GetTenantId(), req.GetClusterId()); err != nil {
		s.Logger.WithError(err).WithFields(logging.Fields{
			"tenant_id":  req.GetTenantId(),
			"cluster_id": req.GetClusterId(),
		}).Warn("Failed to remove tenant alias cluster state")
		return nil, status.Errorf(codes.Internal, "remove tenant alias cluster: %v", err)
	}
	if s.AliasPublisher != nil {
		if err := s.AliasPublisher.PublishTenantAlias(ctx, req.GetTenantId()); err != nil {
			s.Logger.WithError(err).WithField("tenant_id", req.GetTenantId()).Warn("Failed to republish tenant alias after cluster removal")
		}
	}
	return &pb.RemoveTenantAliasClusterResponse{Accepted: true}, nil
}

// EnsureCustomDomain persists tenant custom-domain intent and queues async
// verification + ACME issuance. Returns the CNAMEs the customer must set
// (stable across the lifecycle so the dashboard can render them
// idempotently).
func (s *NavigatorServer) EnsureCustomDomain(ctx context.Context, req *pb.EnsureCustomDomainRequest) (*pb.EnsureCustomDomainResponse, error) {
	tenantID := req.GetTenantId()
	domain := req.GetDomain()
	log := s.Logger.WithFields(logging.Fields{"tenant_id": tenantID, "domain": domain})
	log.Info("Received EnsureCustomDomain request")

	row, err := s.CertManager.EnsureCustomDomain(ctx, tenantID, domain)
	if err != nil {
		log.WithError(err).Warn("Failed to persist custom domain intent")
		return &pb.EnsureCustomDomainResponse{Error: err.Error()}, nil
	}
	alias, aliasErr := s.CertManager.GetTenantAlias(ctx, tenantID)
	if aliasErr != nil && !errors.Is(aliasErr, store.ErrNotFound) {
		log.WithError(aliasErr).Warn("Failed to look up tenant alias for custom domain")
		return nil, status.Errorf(codes.Internal, "tenant alias lookup: %v", aliasErr)
	}
	if alias == nil || alias.Subdomain == "" {
		return &pb.EnsureCustomDomainResponse{
			Accepted: false,
			Status:   row.Status,
			Error:    "tenant alias not provisioned; configure the paid tenant alias first",
		}, nil
	}
	traffic := alias.Subdomain + "." + logic.TenantAliasZoneLabel + "." + s.RootDomain + "."
	acme := row.AcmeDNSSubdomain + "." + logic.AcmeDNSZoneLabel + "." + s.RootDomain + "."
	return &pb.EnsureCustomDomainResponse{
		Accepted:                   true,
		Status:                     row.Status,
		RequiredTrafficCname:       traffic,
		RequiredAcmeChallengeCname: acme,
	}, nil
}

// RemoveCustomDomain signals teardown.
func (s *NavigatorServer) RemoveCustomDomain(ctx context.Context, req *pb.RemoveCustomDomainRequest) (*pb.RemoveCustomDomainResponse, error) {
	if err := s.CertManager.RemoveCustomDomain(ctx, req.GetTenantId(), req.GetDomain()); err != nil {
		s.Logger.WithError(err).WithFields(logging.Fields{
			"tenant_id": req.GetTenantId(),
			"domain":    req.GetDomain(),
		}).Warn("Failed to mark custom domain for teardown")
		return nil, status.Errorf(codes.Internal, "remove custom domain: %v", err)
	}
	return &pb.RemoveCustomDomainResponse{Accepted: true}, nil
}

// GetCustomDomainStatus returns lifecycle state for a single (tenant_id,
// domain) pair plus the canonical CNAMEs to display in the dashboard.
func (s *NavigatorServer) GetCustomDomainStatus(ctx context.Context, req *pb.GetCustomDomainStatusRequest) (*pb.GetCustomDomainStatusResponse, error) {
	row, err := s.CertManager.GetTenantCustomDomain(ctx, req.GetTenantId(), req.GetDomain())
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return &pb.GetCustomDomainStatusResponse{Found: false}, nil
		}
		s.Logger.WithError(err).WithFields(logging.Fields{
			"tenant_id": req.GetTenantId(),
			"domain":    req.GetDomain(),
		}).Warn("GetCustomDomainStatus lookup failed")
		return nil, status.Errorf(codes.Internal, "lookup failed: %v", err)
	}
	resp := &pb.GetCustomDomainStatusResponse{
		Found:    true,
		TenantId: row.TenantID,
		Domain:   row.Domain,
		Status:   row.Status,
	}
	if alias, aliasErr := s.CertManager.GetTenantAlias(ctx, req.GetTenantId()); aliasErr == nil && alias != nil && alias.Subdomain != "" {
		resp.RequiredTrafficCname = alias.Subdomain + "." + logic.TenantAliasZoneLabel + "." + s.RootDomain + "."
	}
	resp.RequiredAcmeChallengeCname = row.AcmeDNSSubdomain + "." + logic.AcmeDNSZoneLabel + "." + s.RootDomain + "."
	if row.LastVerifiedAt.Valid {
		resp.LastVerifiedAt = row.LastVerifiedAt.Time.Unix()
	}
	if row.CertIssuedAt.Valid {
		resp.CertIssuedAt = row.CertIssuedAt.Time.Unix()
	}
	if row.CertExpiresAt.Valid {
		resp.CertExpiresAt = row.CertExpiresAt.Time.Unix()
	}
	if row.LastError.Valid {
		resp.LastError = row.LastError.String
	}
	return resp, nil
}

func (s *NavigatorServer) IssueInternalCert(ctx context.Context, req *pb.IssueInternalCertRequest) (*pb.IssueInternalCertResponse, error) {
	log := s.Logger.WithFields(logging.Fields{
		"node_id":      req.GetNodeId(),
		"service_type": req.GetServiceType(),
	})
	cert, err := s.InternalCAManager.IssueInternalCert(ctx, req.GetNodeId(), req.GetServiceType(), req.GetIssueToken())
	if err != nil {
		log.WithError(err).Error("Failed to issue internal certificate")
		return &pb.IssueInternalCertResponse{
			Success:     false,
			NodeId:      req.GetNodeId(),
			ServiceType: req.GetServiceType(),
			Error:       err.Error(),
		}, nil
	}

	return &pb.IssueInternalCertResponse{
		Success:     true,
		NodeId:      cert.NodeID,
		ServiceType: cert.ServiceType,
		CertPem:     cert.CertPEM,
		KeyPem:      cert.KeyPEM,
		ExpiresAt:   cert.ExpiresAt.Unix(),
	}, nil
}

// quartermasterEdgeResolver implements worker.EdgeAddressResolver by
// asking Quartermaster for a node's external IPv4. The alias apply-state
// worker uses this to populate Bunny smart record sets with the actual
// public IPs of edges that have ACKed the tenant's TLS bundle.
type quartermasterEdgeResolver struct {
	qm *quartermaster.GRPCClient
}

func (r quartermasterEdgeResolver) ResolveEdgeAddresses(ctx context.Context, nodeID string) ([]string, []string, error) {
	if r.qm == nil || strings.TrimSpace(nodeID) == "" {
		return nil, nil, nil
	}
	resp, err := r.qm.GetNode(ctx, nodeID)
	if err != nil {
		return nil, nil, err
	}
	node := resp.GetNode()
	if node == nil {
		return nil, nil, nil
	}
	var ipv4 []string
	if v := strings.TrimSpace(node.GetExternalIp()); v != "" {
		ipv4 = []string{v}
	}
	return ipv4, nil, nil
}

func (r quartermasterEdgeResolver) TenantActiveInCluster(ctx context.Context, tenantID, clusterID string) (bool, error) {
	if r.qm == nil || strings.TrimSpace(tenantID) == "" || strings.TrimSpace(clusterID) == "" {
		return false, nil
	}
	resp, err := r.qm.ListAliasedTenantsForCluster(ctx, clusterID)
	if err != nil {
		return false, err
	}
	for _, ref := range resp.GetTenants() {
		if ref.GetTenantId() == tenantID {
			return true, nil
		}
	}
	return false, nil
}

// ClusterControlCellHealthy returns true when the cluster's control cell
// (the Foghorn cell that owns ConfigSeed + tenant-alias-bundle delivery
// for it) reports healthy. Tenant alias DNS only publishes edges whose
// owning cell can actually push config to them; a degraded or offline
// cell drops out of the membership set until it recovers.
func (r quartermasterEdgeResolver) ClusterControlCellHealthy(ctx context.Context, clusterID string) (bool, error) {
	if r.qm == nil || strings.TrimSpace(clusterID) == "" {
		return false, nil
	}
	resp, err := r.qm.GetCluster(ctx, clusterID)
	if err != nil {
		return false, err
	}
	cluster := resp.GetCluster()
	if cluster == nil {
		return false, nil
	}
	controlCell := strings.TrimSpace(cluster.GetControlCellId())
	if controlCell == "" {
		// Empty control_cell_id means the cluster controls itself
		// (platform-official) or hasn't been assigned to a regional
		// cell yet; either way fall back to the cluster's own health.
		return clusterHealthy(cluster.GetHealthStatus()), nil
	}
	if controlCell == cluster.GetClusterId() {
		return clusterHealthy(cluster.GetHealthStatus()), nil
	}
	cellResp, err := r.qm.GetCluster(ctx, controlCell)
	if err != nil {
		return false, err
	}
	cell := cellResp.GetCluster()
	if cell == nil {
		return false, nil
	}
	return clusterHealthy(cell.GetHealthStatus()), nil
}

// clusterHealthy maps cluster.health_status to a binary "DNS membership
// may include this cluster's edges" decision.
func clusterHealthy(status string) bool {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "degraded", "offline", "unhealthy":
		return false
	}
	return true
}
