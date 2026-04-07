package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"strconv"
	"strings"
	"time"

	"frameworks/api_dns/internal/logic"
	"frameworks/api_dns/internal/provider/cloudflare"
	"frameworks/api_dns/internal/store"
	"frameworks/api_dns/internal/worker"
	"frameworks/pkg/clients/quartermaster"
	"frameworks/pkg/config"
	fieldcrypt "frameworks/pkg/crypto"
	"frameworks/pkg/database"
	pkgdns "frameworks/pkg/dns"
	"frameworks/pkg/grpcutil"
	"frameworks/pkg/logging"
	"frameworks/pkg/middleware"
	"frameworks/pkg/monitoring"
	pb "frameworks/pkg/proto"
	"frameworks/pkg/qmbootstrap"
	"frameworks/pkg/server"
	"frameworks/pkg/version"

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
	Logger            logging.Logger
	Metrics           *ServerMetrics
}

func main() {
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
	cfClient := cloudflare.NewClientFromConfig(cfConfig)

	// Quartermaster gRPC client
	qmGRPCAddr := config.GetEnv("QUARTERMASTER_GRPC_ADDR", "quartermaster:19002")
	qmClient, err := quartermaster.NewGRPCClient(quartermaster.GRPCConfig{
		GRPCAddr:      qmGRPCAddr,
		Timeout:       10 * time.Second,
		Logger:        logger,
		ServiceToken:  serviceToken,
		AllowInsecure: config.GetEnvBool("GRPC_ALLOW_INSECURE", true),
		CACertFile:    config.GetEnv("GRPC_TLS_CA_PATH", ""),
		ServerName:    config.GetEnv("GRPC_TLS_SERVER_NAME", ""),
	})
	if err != nil {
		logger.WithError(err).Fatal("Failed to create Quartermaster gRPC client")
	}
	defer qmClient.Close()

	// === Logic Initialization ===
	rootDomain := config.RequireEnv("BRAND_DOMAIN")

	recordTTL := config.GetEnvInt("NAVIGATOR_DNS_TTL_A_RECORD", 60)
	lbTTL := config.GetEnvInt("NAVIGATOR_DNS_TTL_LB", 60)
	staleSeconds := config.GetEnvInt("NAVIGATOR_DNS_HEALTH_STALE_SECONDS", 300)
	monitorConfig := logic.MonitorConfig{
		Interval: config.GetEnvInt("NAVIGATOR_CF_MONITOR_INTERVAL", 60),
		Timeout:  config.GetEnvInt("NAVIGATOR_CF_MONITOR_TIMEOUT", 5),
		Retries:  config.GetEnvInt("NAVIGATOR_CF_MONITOR_RETRIES", 2),
	}
	dnsManager := logic.NewDNSManager(cfClient, qmClient, logger, rootDomain, recordTTL, lbTTL, time.Duration(staleSeconds)*time.Second, monitorConfig)
	certManager := logic.NewCertManager(certStore)
	internalCAManager := logic.NewInternalCAManager(certStore, qmClient, logger, rootDomain)
	dnsManager.SetCertChecker(certManager)
	if err := internalCAManager.EnsureCA(context.Background()); err != nil {
		logger.WithError(err).Fatal("Failed to initialize internal CA")
	}

	// === Background Workers ===
	renewalWorker := worker.NewRenewalWorker(certStore, certManager, logger)
	go renewalWorker.Start(context.Background())
	reconcileIntervalSeconds := config.GetEnvInt("NAVIGATOR_DNS_RECONCILE_INTERVAL_SECONDS", 60)
	acmeEmail := config.GetEnv("FROM_EMAIL", "info@frameworks.network")
	reconciler := worker.NewDNSReconciler(dnsManager, certManager, qmClient, logger, time.Duration(reconcileIntervalSeconds)*time.Second, rootDomain, acmeEmail, pkgdns.ManagedServiceTypes())
	go reconciler.Start(context.Background())

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
		Logger:            logger,
		Metrics:           serverMetrics,
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
		if err := internalCAManager.EnsureLocalServerCertificate(context.Background(), "navigator", grpcCertFile, grpcKeyFile); err != nil {
			logger.WithError(err).Fatal("Failed to stage Navigator bootstrap gRPC certificate")
		}
		waitCtx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		if err := grpcutil.WaitForServerTLSFiles(waitCtx, tlsCfg, logger); err != nil {
			logger.WithError(err).Fatal("Timed out waiting for Navigator gRPC TLS files")
		}
		grpcTLSOpt, err := grpcutil.ServerTLS(tlsCfg, logger)
		if err != nil {
			logger.WithError(err).Fatal("Failed to configure Navigator gRPC TLS")
		}
		if grpcTLSOpt == nil {
			logger.Warn("Navigator gRPC is running without TLS; private keys require a private network path.")
		}

		serverOpts := []grpc.ServerOption{
			grpc.ChainUnaryInterceptor(
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
		grpcPortInt, _ := strconv.Atoi(config.GetEnv("NAVIGATOR_GRPC_PORT", "19004"))
		if grpcPortInt <= 0 || grpcPortInt > 65535 {
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

	if err := server.Start(serverConfig, app, logger); err != nil {
		logger.WithError(err).Fatal("Navigator HTTP server failed")
	}
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
