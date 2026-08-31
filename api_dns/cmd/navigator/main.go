package main

import (
	"context"
	"errors"
	"fmt"
	"math"
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
	dnspb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/dns"
	quartermasterpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/quartermaster"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/qmbootstrap"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/server"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/version"

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

// ServerMetrics holds Prometheus metrics for the gRPC server. Per-method
// counts + duration are captured by GRPCMetricsInterceptor; separate
// dns_/cert_operations_total would only rename the same axis.
type ServerMetrics struct {
	GRPCRequests *prometheus.CounterVec
	GRPCDuration *prometheus.HistogramVec
}

type tenantAckAuthority interface {
	ListAliasedTenantsForCluster(context.Context, string) (*quartermasterpb.ListAliasedTenantsForClusterResponse, error)
}

// NavigatorServer holds dependencies for the gRPC and HTTP server
type NavigatorServer struct {
	dnspb.UnimplementedNavigatorServiceServer
	DNSManager        *logic.DNSManager
	CertManager       *logic.CertManager
	InternalCAManager *logic.InternalCAManager
	AliasPublisher    *worker.AliasApplyStateWorker
	Quartermaster     tenantAckAuthority
	TenantClusters    tenantClusterAuthority
	// Reconciler also owns the per-instance physical infra DNS sync; SyncDNS calls
	// it so a node/service change refreshes <service>.<node>.infra.<root> at once
	// instead of waiting for the periodic reconcile tick.
	Reconciler *worker.DNSReconciler
	Logger     logging.Logger
	Metrics    *ServerMetrics
	// RootDomain is the operator base domain (e.g. "frameworks.network").
	// Custom-domain RPCs use it to build the canonical CNAME instructions
	// returned to the dashboard.
	RootDomain string
}

type tenantClusterAuthority interface {
	TenantAliasClusterAuthorityState(ctx context.Context, tenantID, clusterID string) (string, error)
	EnsureTenantAliasCluster(ctx context.Context, tenantID, clusterID string, sequence int64) (bool, error)
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
	dbConfig.ServiceName = "navigator"
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
	renewalWorker := worker.NewRenewalWorker(certStore, certManager, logger, rootDomain, acmeEmail)
	go renewalWorker.Start(context.Background())
	reconcileIntervalSeconds := config.GetEnvInt("NAVIGATOR_DNS_RECONCILE_INTERVAL_SECONDS", 60)
	reconciler := worker.NewDNSReconciler(dnsManager, certManager, qmClient, logger, time.Duration(reconcileIntervalSeconds)*time.Second, rootDomain, acmeEmail, pkgdns.ManagedServiceTypes(), staleSeconds)
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
		staleSeconds,
	)
	go aliasWorker.Start(context.Background())

	// Setup monitoring
	healthChecker := monitoring.NewHealthChecker("navigator", version.Version)
	metricsCollector := monitoring.NewMetricsCollector("navigator", version.Version, version.GitCommit)

	// Create gRPC server metrics
	serverMetrics := &ServerMetrics{
		GRPCRequests: metricsCollector.NewCounter("grpc_requests_total", "Total gRPC requests", []string{"method", "status"}),
		GRPCDuration: metricsCollector.NewHistogram("grpc_request_duration_seconds", "gRPC request duration", []string{"method"}, nil),
	}

	// === Server Setup ===
	navigatorServer := &NavigatorServer{
		DNSManager:        dnsManager,
		CertManager:       certManager,
		InternalCAManager: internalCAManager,
		AliasPublisher:    aliasWorker,
		Quartermaster:     qmClient,
		TenantClusters:    certManager,
		Reconciler:        reconciler,
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
		dnspb.RegisterNavigatorServiceServer(grpcServer, navigatorServer)

		// gRPC health service so external probes can use gRPC health checks
		hs := health.NewServer()
		hs.SetServingStatus("", grpc_health_v1.HealthCheckResponse_SERVING)
		hs.SetServingStatus(dnspb.NavigatorService_ServiceDesc.ServiceName, grpc_health_v1.HealthCheckResponse_SERVING)
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

		c.JSON(http.StatusOK, gin.H{
			"bundle_id":  bundle.BundleID,
			"domains":    bundle.Domains,
			"cert_pem":   bundle.CertPEM,
			"key_pem":    bundle.KeyPEM,
			"expires_at": bundle.ExpiresAt.Unix(),
			"version":    bundle.Version,
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
		req := &quartermasterpb.BootstrapServiceRequest{
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
	return func(ctx context.Context, req any, _ *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
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

// SyncDNS implements the gRPC SyncDNS method. Cluster-scoped requests publish
// one cluster's service record and, for platform-official Bunny clusters, also
// refresh the matching root entrypoint. Unscoped Bunny requests refresh only
// the root entrypoint; unscoped Cloudflare requests use the root service sync
// path.
func (s *NavigatorServer) SyncDNS(ctx context.Context, req *dnspb.SyncDNSRequest) (*dnspb.SyncDNSResponse, error) {
	log := s.Logger.WithField("service_type", req.GetServiceType())
	if req.ClusterId != nil {
		log = log.WithField("cluster_id", req.GetClusterId())
	}
	log.Info("Received SyncDNS request")

	var (
		partialErrors map[string]string
		err           error
	)
	isPhysical := s.Reconciler != nil && pkgdns.IsPhysicalEndpointServiceType(req.GetServiceType())
	switch {
	case req.ClusterId != nil && req.GetClusterId() != "":
		// Cluster-scoped: pooled record for the served media cluster. For a physical
		// type this is the right pooled half (livepeer.<media-cluster>); the physical
		// block below adds the node-keyed records.
		partialErrors, err = s.DNSManager.SyncServiceForCluster(ctx, req.GetServiceType(), req.GetClusterId())
	case isPhysical:
		// Physical-only wake (no served cluster — e.g. an unassigned gateway, or a
		// node lifecycle event): refresh ONLY the node-keyed infra records below, with
		// no unrelated pooled/root logical DNS work or its failure/timeout surface.
	case pkgdns.ProviderForServiceType(req.GetServiceType()) == pkgdns.ProviderBunny:
		partialErrors, err = s.DNSManager.SyncBunnyRootService(ctx, req.GetServiceType())
	default:
		partialErrors, err = s.DNSManager.SyncService(ctx, req.GetServiceType(), req.GetRootDomain())
	}

	// A physical-endpoint service (e.g. livepeer-gateway) also has per-node infra A
	// records keyed on the node, not the cluster pool. Refresh them on the same event
	// so a node/service change publishes immediately rather than lagging the reconcile
	// interval. Merge its errors so the caller cannot read success while the infra
	// records actually failed to refresh.
	if isPhysical {
		physErrors, physErr := s.Reconciler.SyncPhysicalInstanceEndpointsForType(ctx, req.GetServiceType())
		if physErr != nil && err == nil {
			err = physErr
		}
		for k, v := range physErrors {
			if partialErrors == nil {
				partialErrors = map[string]string{}
			}
			partialErrors[k] = v
		}
	}
	if err != nil {
		log.WithError(err).Error("DNS sync failed")
		return &dnspb.SyncDNSResponse{
			Success: false,
			Message: fmt.Sprintf("Sync failed: %v", err),
			Errors:  partialErrors,
		}, nil
	}

	if len(partialErrors) > 0 {
		return &dnspb.SyncDNSResponse{
			Success: false,
			Message: "Sync completed with errors",
			Errors:  partialErrors,
		}, nil
	}

	return &dnspb.SyncDNSResponse{
		Success: true,
		Message: "DNS sync completed successfully",
	}, nil
}

// IssueCertificate implements the gRPC IssueCertificate method
func (s *NavigatorServer) IssueCertificate(ctx context.Context, req *dnspb.IssueCertificateRequest) (*dnspb.IssueCertificateResponse, error) {
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
		return &dnspb.IssueCertificateResponse{
			Success: false,
			Error:   err.Error(),
		}, nil
	}

	return &dnspb.IssueCertificateResponse{
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
func (s *NavigatorServer) GetCertificate(ctx context.Context, req *dnspb.GetCertificateRequest) (*dnspb.GetCertificateResponse, error) {
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
		absent, msg := lookupAbsence(err)
		if absent {
			log.Info("Certificate not found")
		} else {
			log.WithError(err).Warn("Certificate lookup failed")
		}
		return &dnspb.GetCertificateResponse{
			Found: false,
			Error: msg,
		}, nil
	}

	// Return tenant_id if set
	var respTenantID *string
	if cert.TenantID.Valid {
		respTenantID = &cert.TenantID.String
	}

	return &dnspb.GetCertificateResponse{
		Found:     true,
		TenantId:  respTenantID,
		Domain:    cert.Domain,
		CertPem:   cert.CertPEM,
		KeyPem:    cert.KeyPEM,
		ExpiresAt: cert.ExpiresAt.Unix(),
	}, nil
}

func (s *NavigatorServer) GetTLSBundle(ctx context.Context, req *dnspb.GetTLSBundleRequest) (*dnspb.GetTLSBundleResponse, error) {
	log := s.Logger.WithField("bundle_id", req.GetBundleId())
	log.Info("Received GetTLSBundle request")

	bundle, err := s.CertManager.GetTLSBundle(ctx, req.GetBundleId())
	if err != nil {
		// Only a genuine not-found is authoritative absence (empty Error).
		// A store failure keeps its message so Foghorn preserves last-good
		// material instead of treating the bundle as removed.
		absent, msg := lookupAbsence(err)
		if absent {
			log.Info("TLS bundle not found")
		} else {
			log.WithError(err).Warn("TLS bundle lookup failed")
		}
		return &dnspb.GetTLSBundleResponse{
			Found: false,
			Error: msg,
		}, nil
	}

	return &dnspb.GetTLSBundleResponse{
		Found:     true,
		BundleId:  bundle.BundleID,
		Domains:   bundle.Domains,
		CertPem:   bundle.CertPEM,
		KeyPem:    bundle.KeyPEM,
		ExpiresAt: bundle.ExpiresAt.Unix(),
		Version:   bundle.Version,
	}, nil
}

func (s *NavigatorServer) GetCABundle(ctx context.Context, _ *dnspb.GetCABundleRequest) (*dnspb.GetCABundleResponse, error) {
	caPEM, err := s.InternalCAManager.GetCABundle(ctx)
	if err != nil {
		absent, msg := lookupAbsence(err)
		if absent {
			s.Logger.Info("Internal CA bundle not provisioned yet")
		} else {
			s.Logger.WithError(err).Error("Failed to get internal CA bundle")
		}
		return &dnspb.GetCABundleResponse{
			Found: false,
			Error: msg,
		}, nil
	}

	return &dnspb.GetCABundleResponse{
		Found: true,
		CaPem: caPEM,
	}, nil
}

// EnsureTenantAlias implements the gRPC EnsureTenantAlias method.
// Idempotent: persists alias intent and queues async ACME work.
func (s *NavigatorServer) EnsureTenantAlias(ctx context.Context, req *dnspb.EnsureTenantAliasRequest) (*dnspb.EnsureTenantAliasResponse, error) {
	tenantID := req.GetTenantId()
	subdomain := req.GetSubdomain()
	log := s.Logger.WithField("tenant_id", tenantID).WithField("subdomain", subdomain)
	log.Info("Received EnsureTenantAlias request")

	alias, err := s.CertManager.EnsureTenantAlias(ctx, tenantID, subdomain)
	if err != nil {
		log.WithError(err).Warn("Failed to persist tenant alias intent")
		return &dnspb.EnsureTenantAliasResponse{Error: err.Error()}, nil
	}
	return &dnspb.EnsureTenantAliasResponse{
		Accepted: true,
		Status:   alias.Status,
	}, nil
}

// RemoveTenantAlias implements the gRPC RemoveTenantAlias method.
// Idempotent: marks alias for teardown; worker cleans up DNS + state.
func (s *NavigatorServer) RemoveTenantAlias(ctx context.Context, req *dnspb.RemoveTenantAliasRequest) (*dnspb.RemoveTenantAliasResponse, error) {
	if err := s.CertManager.RemoveTenantAlias(ctx, req.GetTenantId()); err != nil {
		s.Logger.WithError(err).WithField("tenant_id", req.GetTenantId()).Warn("Failed to mark tenant alias for teardown")
		return &dnspb.RemoveTenantAliasResponse{}, nil
	}
	return &dnspb.RemoveTenantAliasResponse{Accepted: true}, nil
}

// GetTenantAliasStatus implements the gRPC GetTenantAliasStatus method.
// Returns found=false for tenants without an alias intent (the
// not-found case is treated as a normal "no row" response, not an
// error; callers like the webapp check Found to decide what to show).
func (s *NavigatorServer) GetTenantAliasStatus(ctx context.Context, req *dnspb.GetTenantAliasStatusRequest) (*dnspb.GetTenantAliasStatusResponse, error) {
	alias, err := s.CertManager.GetTenantAlias(ctx, req.GetTenantId())
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return &dnspb.GetTenantAliasStatusResponse{Found: false}, nil
		}
		s.Logger.WithError(err).WithField("tenant_id", req.GetTenantId()).Warn("GetTenantAliasStatus lookup failed")
		return nil, status.Errorf(codes.Internal, "lookup failed: %v", err)
	}
	resp := &dnspb.GetTenantAliasStatusResponse{
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
	retirements, retErr := s.CertManager.ListTenantAliasRetirementLabels(ctx, req.GetTenantId())
	if retErr != nil {
		s.Logger.WithError(retErr).WithField("tenant_id", req.GetTenantId()).Debug("Tenant alias retirement lookup failed")
	}
	resp.PendingRetirements = retirements
	authorizedClusters, clusterErr := s.CertManager.ListTenantAliasAuthorizedClusters(ctx, req.GetTenantId())
	if clusterErr != nil {
		s.Logger.WithError(clusterErr).WithField("tenant_id", req.GetTenantId()).Debug("Tenant alias cluster-authority lookup failed")
	} else {
		resp.AuthorizedClusterIds = authorizedClusters
	}
	return resp, nil
}

// RemoveTenantAliasSubdomain retires one specific label's Bunny records
// without disturbing the tenant's active alias. Idempotent; the alias
// worker performs the actual cleanup asynchronously.
func (s *NavigatorServer) RemoveTenantAliasSubdomain(ctx context.Context, req *dnspb.RemoveTenantAliasSubdomainRequest) (*dnspb.RemoveTenantAliasSubdomainResponse, error) {
	if err := s.CertManager.RemoveTenantAliasSubdomain(ctx, req.GetTenantId(), req.GetSubdomain()); err != nil {
		s.Logger.WithError(err).WithFields(logging.Fields{
			"tenant_id": req.GetTenantId(),
			"subdomain": req.GetSubdomain(),
		}).Warn("Failed to enqueue tenant alias subdomain retirement")
		return &dnspb.RemoveTenantAliasSubdomainResponse{Error: err.Error()}, nil
	}
	return &dnspb.RemoveTenantAliasSubdomainResponse{Accepted: true}, nil
}

// ReportConfigSeedApplyResult persists edge cert readiness ACKs observed
// by Foghorn, then reconciles affected tenant DNS immediately.
func (s *NavigatorServer) ReportConfigSeedApplyResult(ctx context.Context, req *dnspb.ReportConfigSeedApplyResultRequest) (*dnspb.ReportConfigSeedApplyResultResponse, error) {
	appliedAt := time.Unix(req.GetAppliedAt(), 0).UTC()
	filter := s.filterTenantBundlesForCluster(ctx, req.GetClusterId(), req.GetAppliedBundleIds(), req.GetFailedBundleIds())
	if filter.err != nil && !errors.Is(filter.err, errMissingClusterIdentity) {
		// The classifier returns only retryable/unresolved counts on error;
		// authoritative denials are never double-counted as failures. A
		// missing cluster identity is terminal (quarantined at the sender),
		// so its bundles are not "deferred by an authority failure" either.
		logic.ObserveConfigSeedApplyAckAuthorityErrors(filter.deferredApplied, filter.deferredFailed)
	}
	if filter.filteredApplied != 0 || filter.filteredFailed != 0 {
		logic.ObserveConfigSeedApplyAckFiltered(filter.filteredApplied, filter.filteredFailed)
		s.Logger.WithFields(logging.Fields{
			"cluster_id":       req.GetClusterId(),
			"filtered_applied": filter.filteredApplied,
			"filtered_failed":  filter.filteredFailed,
		}).Info("Filtered ConfigSeed tenant apply results outside cluster authority")
	}
	if filter.err != nil && len(filter.applied) == 0 && len(filter.failed) == 0 {
		return nil, status.Errorf(tenantAckAuthorityErrorCode(filter.err), "validate tenant apply ACK authority: %v", filter.err)
	}
	affected, discarded, err := s.CertManager.RecordConfigSeedApplyResult(ctx,
		req.GetNodeId(),
		req.GetClusterId(),
		req.GetSeedVersion(),
		req.GetDeliverySequence(),
		filter.applied,
		filter.failed,
		req.GetBundleVersions(),
		appliedAt,
	)
	if err != nil {
		s.Logger.WithError(err).WithField("node_id", req.GetNodeId()).Warn("Failed to record ConfigSeed apply result")
		return nil, status.Errorf(codes.Internal, "record apply result: %v", err)
	}
	if discarded.Stale > 0 {
		s.Logger.WithFields(logging.Fields{
			"node_id": req.GetNodeId(), "seed_version": req.GetSeedVersion(),
			"delivery_sequence": req.GetDeliverySequence(), "discarded": discarded.Stale,
		}).Info("Discarded stale ConfigSeed tenant apply results")
	}
	if discarded.Revoked > 0 {
		s.Logger.WithFields(logging.Fields{
			"node_id": req.GetNodeId(), "seed_version": req.GetSeedVersion(),
			"delivery_sequence": req.GetDeliverySequence(), "discarded": discarded.Revoked,
		}).Info("Discarded ConfigSeed tenant apply results for revoked cluster authority")
	}
	if discarded.MissingParent > 0 {
		s.Logger.WithFields(logging.Fields{
			"node_id": req.GetNodeId(), "seed_version": req.GetSeedVersion(),
			"delivery_sequence": req.GetDeliverySequence(), "discarded": discarded.MissingParent,
		}).Info("Classified ConfigSeed tenant apply results without alias authority")
	}
	if s.AliasPublisher != nil {
		// On a fully validated delivery, republish every authorized tenant in
		// the request — not only rows that advanced. A replayed delivery whose
		// entries are all fenced as duplicates still repairs a publish that
		// failed on the earlier partial pass.
		republish := affected
		if filter.err == nil {
			republish = tenantIDsFromBundles(filter.applied, filter.failed)
		}
		for _, tenantID := range republish {
			if pubErr := s.AliasPublisher.PublishTenantAlias(ctx, tenantID); pubErr != nil {
				s.Logger.WithError(pubErr).WithField("tenant_id", tenantID).Warn("Failed to publish tenant alias after apply ACK")
			}
		}
	}
	if filter.err != nil {
		// Locally authorized and non-tenant results are durable now. Returning
		// a retryable code retains the whole Foghorn outbox delivery so only
		// the unresolved first-admission entries remain pending on its next
		// replay; a malformed request instead quarantines at the sender.
		return nil, status.Errorf(tenantAckAuthorityErrorCode(filter.err), "validate tenant apply ACK authority: %v", filter.err)
	}
	return &dnspb.ReportConfigSeedApplyResultResponse{
		Accepted:          true,
		AffectedTenantIds: affected,
	}, nil
}

type tenantBundleFilterResult struct {
	applied, failed                 []string
	filteredApplied, filteredFailed int
	deferredApplied, deferredFailed int
	err                             error
}

func (s *NavigatorServer) filterTenantBundlesForCluster(ctx context.Context, clusterID string, applied, failed []string) tenantBundleFilterResult {
	if !containsTenantBundle(applied) && !containsTenantBundle(failed) {
		return tenantBundleFilterResult{applied: applied, failed: failed}
	}
	clusterID = strings.TrimSpace(clusterID)
	if clusterID == "" {
		return tenantBundleFilterResult{
			deferredApplied: countTenantBundles(applied),
			deferredFailed:  countTenantBundles(failed),
			err:             errMissingClusterIdentity,
		}
	}
	allowed := map[string]struct{}{}
	unknown := map[string]struct{}{}
	deferred := map[string]struct{}{}
	classified := map[string]struct{}{}
	bundleIDs := append(append([]string(nil), applied...), failed...)
	for index, bundleID := range bundleIDs {
		tenantID, tenantBundle := strings.CutPrefix(bundleID, "tenant:")
		if !tenantBundle || tenantID == "" {
			continue
		}
		if _, alreadyClassified := classified[tenantID]; alreadyClassified {
			continue
		}
		classified[tenantID] = struct{}{}
		authorityState := ""
		if s.TenantClusters != nil {
			var authorityErr error
			authorityState, authorityErr = s.TenantClusters.TenantAliasClusterAuthorityState(ctx, tenantID, clusterID)
			if authorityErr != nil {
				deferred[tenantID] = struct{}{}
				for _, remainingID := range bundleIDs[index+1:] {
					remainingTenant, ok := strings.CutPrefix(remainingID, "tenant:")
					if !ok || remainingTenant == "" {
						continue
					}
					// A malformed batch can repeat a tenant already classified
					// active; deferring it again would drive the terminal
					// filtered count negative.
					if _, alreadyAllowed := allowed[remainingTenant]; alreadyAllowed {
						continue
					}
					deferred[remainingTenant] = struct{}{}
				}
				// Tenants classified unknown before the failure were never
				// denied either; defer them too so they are not misreported
				// as terminally filtered.
				for unknownTenant := range unknown {
					deferred[unknownTenant] = struct{}{}
				}
				return filterTenantBundlesForAllowed(applied, failed, allowed, deferred, fmt.Errorf("read local tenant cluster authority: %w", authorityErr))
			}
		}
		switch authorityState {
		case "active":
			allowed[tenantID] = struct{}{}
		case "":
			unknown[tenantID] = struct{}{}
		}
	}
	if len(unknown) > 0 {
		if s.Quartermaster == nil {
			return filterTenantBundlesForAllowed(applied, failed, allowed, unknown, errors.New("quartermaster client is unavailable for first tenant-cluster admission"))
		}
		lookupCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
		defer cancel()
		resp, err := s.Quartermaster.ListAliasedTenantsForCluster(lookupCtx, clusterID)
		if err != nil {
			s.Logger.WithError(err).WithField("cluster_id", clusterID).Warn("Tenant apply ACK authority lookup failed")
			return filterTenantBundlesForAllowed(applied, failed, allowed, unknown, fmt.Errorf("list aliased tenants for cluster: %w", err))
		}
		for _, ref := range resp.GetTenants() {
			if _, needsAdmission := unknown[ref.GetTenantId()]; needsAdmission {
				admitted := true
				if s.TenantClusters != nil {
					var grantErr error
					admitted, grantErr = s.TenantClusters.EnsureTenantAliasCluster(ctx, ref.GetTenantId(), clusterID, 0)
					if grantErr != nil {
						for tenantID := range unknown {
							if _, admittedAlready := allowed[tenantID]; !admittedAlready {
								deferred[tenantID] = struct{}{}
							}
						}
						return filterTenantBundlesForAllowed(applied, failed, allowed, deferred, fmt.Errorf("persist legacy tenant cluster admission: %w", grantErr))
					}
				}
				if admitted {
					allowed[ref.GetTenantId()] = struct{}{}
				}
			}
		}
	}
	return filterTenantBundlesForAllowed(applied, failed, allowed, nil, nil)
}

func filterTenantBundlesForAllowed(applied, failed []string, allowed, deferred map[string]struct{}, authorityErr error) tenantBundleFilterResult {
	filteredApplied := filterBundlesForAllowedTenants(applied, allowed)
	filteredFailed := filterBundlesForAllowedTenants(failed, allowed)
	deferredApplied := countTenantBundlesForTenants(applied, deferred)
	deferredFailed := countTenantBundlesForTenants(failed, deferred)
	return tenantBundleFilterResult{
		applied: filteredApplied,
		failed:  filteredFailed,
		// Clamped: a malformed batch that repeats a tenant across the kept and
		// deferred sets must degrade to zero, never feed a negative delta into
		// a Prometheus counter.
		filteredApplied: max(0, countTenantBundles(applied)-countTenantBundles(filteredApplied)-deferredApplied),
		filteredFailed:  max(0, countTenantBundles(failed)-countTenantBundles(filteredFailed)-deferredFailed),
		deferredApplied: deferredApplied,
		deferredFailed:  deferredFailed,
		err:             authorityErr,
	}
}

func countTenantBundlesForTenants(bundleIDs []string, tenants map[string]struct{}) int {
	count := 0
	for _, bundleID := range bundleIDs {
		tenantID, ok := strings.CutPrefix(bundleID, "tenant:")
		if !ok {
			continue
		}
		if _, included := tenants[tenantID]; included {
			count++
		}
	}
	return count
}

// errMissingClusterIdentity is a permanent request defect: retrying the same
// delivery can never succeed, so the RPC surfaces it as InvalidArgument and
// Foghorn quarantines the outbox row instead of retrying forever.
var errMissingClusterIdentity = errors.New("cluster identity is missing")

// tenantAckAuthorityErrorCode maps an authority-validation failure to its gRPC
// code: permanent request defects are InvalidArgument (terminal for Foghorn's
// delivery outbox), everything else stays retryable Unavailable.
func tenantAckAuthorityErrorCode(err error) codes.Code {
	if errors.Is(err, errMissingClusterIdentity) {
		return codes.InvalidArgument
	}
	return codes.Unavailable
}

// lookupAbsence classifies a store read error for the Get* RPCs. Only a
// genuine not-found is authoritative absence (Found=false with an empty
// Error); every other failure keeps its message so consumers preserve
// last-good local state instead of treating the row as removed.
func lookupAbsence(err error) (absent bool, msg string) {
	if errors.Is(err, store.ErrNotFound) {
		return true, ""
	}
	return false, err.Error()
}

// tenantIDsFromBundles returns the deduplicated tenant ids carried by the
// given tenant: bundle id lists, in first-seen order.
func tenantIDsFromBundles(bundleLists ...[]string) []string {
	seen := map[string]struct{}{}
	out := []string{}
	for _, list := range bundleLists {
		for _, bundleID := range list {
			tenantID, ok := strings.CutPrefix(bundleID, "tenant:")
			if !ok || tenantID == "" {
				continue
			}
			if _, dup := seen[tenantID]; dup {
				continue
			}
			seen[tenantID] = struct{}{}
			out = append(out, tenantID)
		}
	}
	return out
}

func containsTenantBundle(bundleIDs []string) bool {
	for _, bundleID := range bundleIDs {
		if strings.HasPrefix(bundleID, "tenant:") {
			return true
		}
	}
	return false
}

func countTenantBundles(bundleIDs []string) int {
	count := 0
	for _, bundleID := range bundleIDs {
		if strings.HasPrefix(bundleID, "tenant:") {
			count++
		}
	}
	return count
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

// RemoveTenantAliasCluster drops one cluster's edges from a tenant's DNS
// eligibility before future ConfigSeeds omit that tenant cert.
func (s *NavigatorServer) RemoveTenantAliasCluster(ctx context.Context, req *dnspb.RemoveTenantAliasClusterRequest) (*dnspb.RemoveTenantAliasClusterResponse, error) {
	if req.GetAuthoritySequence() > math.MaxInt64 {
		return nil, status.Error(codes.InvalidArgument, "authority_sequence exceeds signed database range")
	}
	if _, err := s.CertManager.RemoveTenantAliasCluster(ctx, req.GetTenantId(), req.GetClusterId(), int64(req.GetAuthoritySequence())); err != nil {
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
	return &dnspb.RemoveTenantAliasClusterResponse{Accepted: true}, nil
}

// EnsureTenantAliasCluster installs Quartermaster's ordered positive
// tenant/cluster authority. ACKs can only update readiness beneath this row.
func (s *NavigatorServer) EnsureTenantAliasCluster(ctx context.Context, req *dnspb.EnsureTenantAliasClusterRequest) (*dnspb.EnsureTenantAliasClusterResponse, error) {
	if req.GetAuthoritySequence() > math.MaxInt64 {
		return nil, status.Error(codes.InvalidArgument, "authority_sequence exceeds signed database range")
	}
	if _, err := s.CertManager.EnsureTenantAliasCluster(ctx, req.GetTenantId(), req.GetClusterId(), int64(req.GetAuthoritySequence())); err != nil {
		s.Logger.WithError(err).WithFields(logging.Fields{
			"tenant_id":  req.GetTenantId(),
			"cluster_id": req.GetClusterId(),
		}).Warn("Failed to ensure tenant alias cluster authority")
		return nil, status.Errorf(codes.Internal, "ensure tenant alias cluster: %v", err)
	}
	return &dnspb.EnsureTenantAliasClusterResponse{Accepted: true}, nil
}

// EnsureCustomDomain persists tenant custom-domain intent and queues async
// verification + ACME issuance. Returns the CNAMEs the customer must set
// (stable across the lifecycle so the dashboard can render them
// idempotently).
func (s *NavigatorServer) EnsureCustomDomain(ctx context.Context, req *dnspb.EnsureCustomDomainRequest) (*dnspb.EnsureCustomDomainResponse, error) {
	tenantID := req.GetTenantId()
	domain := req.GetDomain()
	log := s.Logger.WithFields(logging.Fields{"tenant_id": tenantID, "domain": domain})
	log.Info("Received EnsureCustomDomain request")

	row, err := s.CertManager.EnsureCustomDomain(ctx, tenantID, domain)
	if err != nil {
		log.WithError(err).Warn("Failed to persist custom domain intent")
		return &dnspb.EnsureCustomDomainResponse{Error: err.Error()}, nil
	}
	alias, aliasErr := s.CertManager.GetTenantAlias(ctx, tenantID)
	if aliasErr != nil && !errors.Is(aliasErr, store.ErrNotFound) {
		log.WithError(aliasErr).Warn("Failed to look up tenant alias for custom domain")
		return nil, status.Errorf(codes.Internal, "tenant alias lookup: %v", aliasErr)
	}
	if alias == nil || alias.Subdomain == "" {
		return &dnspb.EnsureCustomDomainResponse{
			Accepted: false,
			Status:   row.Status,
			Error:    "tenant alias not provisioned; configure the paid tenant alias first",
		}, nil
	}
	traffic := alias.Subdomain + "." + logic.TenantAliasZoneLabel + "." + s.RootDomain + "."
	acme := row.AcmeDNSSubdomain + "." + logic.AcmeDNSZoneLabel + "." + s.RootDomain + "."
	return &dnspb.EnsureCustomDomainResponse{
		Accepted:                   true,
		Status:                     row.Status,
		RequiredTrafficCname:       traffic,
		RequiredAcmeChallengeCname: acme,
	}, nil
}

// RemoveCustomDomain signals teardown.
func (s *NavigatorServer) RemoveCustomDomain(ctx context.Context, req *dnspb.RemoveCustomDomainRequest) (*dnspb.RemoveCustomDomainResponse, error) {
	if err := s.CertManager.RemoveCustomDomain(ctx, req.GetTenantId(), req.GetDomain()); err != nil {
		s.Logger.WithError(err).WithFields(logging.Fields{
			"tenant_id": req.GetTenantId(),
			"domain":    req.GetDomain(),
		}).Warn("Failed to mark custom domain for teardown")
		return nil, status.Errorf(codes.Internal, "remove custom domain: %v", err)
	}
	return &dnspb.RemoveCustomDomainResponse{Accepted: true}, nil
}

// GetCustomDomainStatus returns lifecycle state for a single (tenant_id,
// domain) pair plus the canonical CNAMEs to display in the dashboard.
func (s *NavigatorServer) GetCustomDomainStatus(ctx context.Context, req *dnspb.GetCustomDomainStatusRequest) (*dnspb.GetCustomDomainStatusResponse, error) {
	row, err := s.CertManager.GetTenantCustomDomain(ctx, req.GetTenantId(), req.GetDomain())
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return &dnspb.GetCustomDomainStatusResponse{Found: false}, nil
		}
		s.Logger.WithError(err).WithFields(logging.Fields{
			"tenant_id": req.GetTenantId(),
			"domain":    req.GetDomain(),
		}).Warn("GetCustomDomainStatus lookup failed")
		return nil, status.Errorf(codes.Internal, "lookup failed: %v", err)
	}
	resp := &dnspb.GetCustomDomainStatusResponse{
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

func (s *NavigatorServer) IssueInternalCert(ctx context.Context, req *dnspb.IssueInternalCertRequest) (*dnspb.IssueInternalCertResponse, error) {
	log := s.Logger.WithFields(logging.Fields{
		"node_id":      req.GetNodeId(),
		"service_type": req.GetServiceType(),
	})
	cert, err := s.InternalCAManager.IssueInternalCert(ctx, req.GetNodeId(), req.GetServiceType(), req.GetIssueToken())
	if err != nil {
		log.WithError(err).Error("Failed to issue internal certificate")
		return &dnspb.IssueInternalCertResponse{
			Success:     false,
			NodeId:      req.GetNodeId(),
			ServiceType: req.GetServiceType(),
			Error:       err.Error(),
		}, nil
	}

	return &dnspb.IssueInternalCertResponse{
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

func (r quartermasterEdgeResolver) ResolveServiceAddressesForClusters(ctx context.Context, serviceType string, clusterIDs []string, staleThresholdSeconds int) ([]worker.ServiceAddress, error) {
	if r.qm == nil || strings.TrimSpace(serviceType) == "" {
		return nil, nil
	}
	seen := map[string]struct{}{}
	out := []worker.ServiceAddress{}
	for _, clusterID := range clusterIDs {
		clusterID = strings.TrimSpace(clusterID)
		if clusterID == "" {
			continue
		}
		resp, err := r.qm.ListHealthyNodesForDNSForCluster(ctx, staleThresholdSeconds, serviceType, clusterID)
		if err != nil {
			return nil, err
		}
		for _, node := range resp.GetNodes() {
			ip := strings.TrimSpace(node.GetExternalIp())
			if ip == "" {
				continue
			}
			key := node.GetNodeId() + "\x00" + ip
			if _, dup := seen[key]; dup {
				continue
			}
			seen[key] = struct{}{}
			out = append(out, worker.ServiceAddress{NodeID: node.GetNodeId(), IP: ip})
		}
	}
	return out, nil
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
