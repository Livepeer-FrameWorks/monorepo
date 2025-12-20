package main

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"time"

	"frameworks/api_dns/internal/logic"
	"frameworks/api_dns/internal/provider/cloudflare"
	"frameworks/api_dns/internal/store"
	"frameworks/api_dns/internal/worker"
	"frameworks/pkg/clients/quartermaster"
	"frameworks/pkg/config"
	"frameworks/pkg/database"
	"frameworks/pkg/logging"
	"frameworks/pkg/middleware"
	"frameworks/pkg/monitoring"
	pb "frameworks/pkg/proto"
	"frameworks/pkg/server"
	"frameworks/pkg/version"

	"github.com/gin-gonic/gin"
	"github.com/prometheus/client_golang/prometheus"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/health"
	"google.golang.org/grpc/health/grpc_health_v1"
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
	DNSManager  *logic.DNSManager
	CertManager *logic.CertManager
	Logger      logging.Logger
	Metrics     *ServerMetrics
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

	// Initialize Store
	certStore := store.NewStore(db)

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
		GRPCAddr:     qmGRPCAddr,
		Timeout:      10 * time.Second,
		Logger:       logger,
		ServiceToken: serviceToken,
	})
	if err != nil {
		logger.WithError(err).Fatal("Failed to create Quartermaster gRPC client")
	}
	defer qmClient.Close()

	// === Logic Initialization ===
	rootDomain := config.RequireEnv("NAVIGATOR_ROOT_DOMAIN")

	dnsManager := logic.NewDNSManager(cfClient, qmClient, logger, rootDomain)
	certManager := logic.NewCertManager(certStore)

	// === Background Workers ===
	renewalWorker := worker.NewRenewalWorker(certStore, certManager, logger)
	go renewalWorker.Start(context.Background())

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
		DNSManager:  dnsManager,
		CertManager: certManager,
		Logger:      logger,
		Metrics:     serverMetrics,
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

		grpcServer := grpc.NewServer(
			grpc.Creds(insecure.NewCredentials()),
			grpc.UnaryInterceptor(authInterceptor),
		)
		pb.RegisterNavigatorServiceServer(grpcServer, navigatorServer)

		// gRPC health service so external probes can use gRPC health checks
		hs := health.NewServer()
		hs.SetServingStatus("", grpc_health_v1.HealthCheckResponse_SERVING)
		hs.SetServingStatus(pb.NavigatorService_ServiceDesc.ServiceName, grpc_health_v1.HealthCheckResponse_SERVING)
		grpc_health_v1.RegisterHealthServer(grpcServer, hs)

		logger.WithField("port", grpcPort).Info("Navigator gRPC server starting...")
		if err := grpcServer.Serve(lis); err != nil {
			logger.WithError(err).Fatal("Navigator gRPC server failed")
		}
	}()

	// === HTTP Server ===
	serverConfig := server.DefaultConfig("navigator", config.RequireEnv("NAVIGATOR_PORT"))

	app := server.SetupServiceRouter(logger, "navigator", healthChecker, metricsCollector)
	app.GET("/status", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"status": "running", "version": version.Version})
	})

	if err := server.Start(serverConfig, app, logger); err != nil {
		logger.WithError(err).Fatal("Navigator HTTP server failed")
	}
}

// SyncDNS implements the gRPC SyncDNS method
func (s *NavigatorServer) SyncDNS(ctx context.Context, req *pb.SyncDNSRequest) (*pb.SyncDNSResponse, error) {
	s.Logger.WithField("service_type", req.GetServiceType()).Info("Received SyncDNS request")

	if err := s.DNSManager.SyncService(ctx, req.GetServiceType()); err != nil {
		s.Logger.WithError(err).Error("DNS sync failed")
		return &pb.SyncDNSResponse{
			Success: false,
			Message: fmt.Sprintf("Sync failed: %v", err),
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
