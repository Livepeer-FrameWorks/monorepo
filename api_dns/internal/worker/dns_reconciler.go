package worker

import (
	"context"
	"strings"
	"time"

	"frameworks/api_dns/internal/logic"
	"frameworks/pkg/logging"
	"frameworks/pkg/proto"
)

type DNSReconciler struct {
	dnsManager   *logic.DNSManager
	certManager  *logic.CertManager
	qmClient     quartermasterClient
	logger       logging.Logger
	interval     time.Duration
	rootDomain   string
	acmeEmail    string
	serviceTypes []string
}

type quartermasterClient interface {
	ListClusters(ctx context.Context, pagination *proto.CursorPaginationRequest) (*proto.ListClustersResponse, error)
}

func NewDNSReconciler(dnsManager *logic.DNSManager, certManager *logic.CertManager, qmClient quartermasterClient, logger logging.Logger, interval time.Duration, rootDomain, acmeEmail string, serviceTypes []string) *DNSReconciler {
	return &DNSReconciler{
		dnsManager:   dnsManager,
		certManager:  certManager,
		qmClient:     qmClient,
		logger:       logger,
		interval:     interval,
		rootDomain:   rootDomain,
		acmeEmail:    acmeEmail,
		serviceTypes: serviceTypes,
	}
}

func (r *DNSReconciler) Start(ctx context.Context) {
	r.logger.Info("Starting DNS reconciliation worker")
	ticker := time.NewTicker(r.interval)
	defer ticker.Stop()

	r.reconcile(ctx)

	for {
		select {
		case <-ctx.Done():
			r.logger.Info("Stopping DNS reconciliation worker")
			return
		case <-ticker.C:
			r.reconcile(ctx)
		}
	}
}

func (r *DNSReconciler) reconcile(ctx context.Context) {
	for _, serviceType := range r.serviceTypes {
		partialErrors, err := r.dnsManager.SyncService(ctx, serviceType, "")
		if err != nil {
			r.logger.WithError(err).WithField("service_type", serviceType).Error("DNS reconciliation failed")
		}
		if len(partialErrors) > 0 {
			r.logger.WithField("service_type", serviceType).WithField("partial_errors", partialErrors).Warn("DNS reconciliation completed with partial errors")
		}

		if serviceType == "edge" || serviceType == "edge-egress" || serviceType == "edge-ingest" || serviceType == "foghorn" {
			clusterPartialErrors, clusterErr := r.dnsManager.SyncServiceByCluster(ctx, serviceType)
			if clusterErr != nil {
				r.logger.WithError(clusterErr).WithField("service_type", serviceType).Error("Cluster DNS reconciliation failed")
			}
			if len(clusterPartialErrors) > 0 {
				r.logger.WithField("service_type", serviceType).WithField("partial_errors", clusterPartialErrors).Warn("Cluster DNS reconciliation completed with partial errors")
			}
		}
	}

	r.ensureClusterWildcardCerts(ctx)
}

func (r *DNSReconciler) ensureClusterWildcardCerts(ctx context.Context) {
	if r.certManager == nil || r.qmClient == nil || strings.TrimSpace(r.acmeEmail) == "" {
		return
	}

	clustersResp, err := r.qmClient.ListClusters(ctx, nil)
	if err != nil {
		r.logger.WithError(err).Warn("Failed to list clusters for wildcard certificate issuance")
		return
	}

	for _, cluster := range clustersResp.GetClusters() {
		if !cluster.GetIsActive() {
			continue
		}
		clusterSlug := logic.ClusterSlug(cluster)
		if clusterSlug == "" || clusterSlug == "default" {
			continue
		}
		cert, certErr := r.certManager.EnsureClusterWildcardCertificate(ctx, clusterSlug, r.rootDomain, r.acmeEmail)
		if certErr != nil {
			r.logger.WithError(certErr).WithField("cluster", cluster.GetClusterId()).Warn("Failed to ensure cluster wildcard certificate")
			continue
		}
		r.logger.WithFields(logging.Fields{
			"cluster":    cluster.GetClusterId(),
			"domain":     cert.Domain,
			"expires_at": cert.ExpiresAt,
		}).Debug("Ensured cluster wildcard certificate")
	}
}
