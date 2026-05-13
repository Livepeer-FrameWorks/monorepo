package worker

import (
	"context"
	"strings"
	"time"

	"frameworks/api_dns/internal/logic"
	pkgdns "github.com/Livepeer-FrameWorks/monorepo/pkg/dns"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/proto"
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
	ListTLSBundles(ctx context.Context, clusterID string, pagination *proto.CursorPaginationRequest) (*proto.ListTLSBundlesResponse, error)
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
		switch pkgdns.ProviderForServiceType(serviceType) {
		case pkgdns.ProviderBunny:
			if pkgdns.IsClusterScopedServiceType(serviceType) {
				clusterPartialErrors, clusterErr := r.dnsManager.SyncServiceByCluster(ctx, serviceType)
				if clusterErr != nil {
					r.logger.WithError(clusterErr).WithField("service_type", serviceType).Error("Cluster DNS reconciliation failed")
				}
				if len(clusterPartialErrors) > 0 {
					r.logger.WithField("service_type", serviceType).WithField("partial_errors", clusterPartialErrors).Warn("Cluster DNS reconciliation completed with partial errors")
				}
			}
			// Global root entrypoint publish: code-owned media labels
			// get smart record sets at {label}.{root} populated from
			// platform_official cluster nodes.
			if isGlobalServiceZone(serviceType) {
				if _, err := r.dnsManager.SyncBunnyRootService(ctx, serviceType); err != nil {
					r.logger.WithError(err).WithField("service_type", serviceType).Warn("Global root DNS reconciliation failed")
				}
			}
		case pkgdns.ProviderCloudflare:
			partialErrors, err := r.dnsManager.SyncService(ctx, serviceType, "")
			if err != nil {
				r.logger.WithError(err).WithField("service_type", serviceType).Error("DNS reconciliation failed")
			}
			if len(partialErrors) > 0 {
				r.logger.WithField("service_type", serviceType).WithField("partial_errors", partialErrors).Warn("DNS reconciliation completed with partial errors")
			}
		default:
			r.logger.WithField("service_type", serviceType).Debug("Skipping service with no public DNS provider")
		}
	}

	r.ensureClusterWildcardCerts(ctx)
	r.ensureGlobalPlatformCerts(ctx)
	r.ensureTLSBundles(ctx)
	r.processPendingTenantAliases(ctx)
}

// processPendingTenantAliases runs the per-tick worker pass for tenant
// alias intent rows. Each cycle, Navigator looks at tenant_aliases for
// rows in cert_issuing or cert_failed status, runs ACME issuance, and
// transitions to cert_issued or back to cert_failed with the error.
func (r *DNSReconciler) processPendingTenantAliases(ctx context.Context) {
	if r.certManager == nil || strings.TrimSpace(r.acmeEmail) == "" || strings.TrimSpace(r.rootDomain) == "" {
		return
	}
	if err := r.dnsManager.EnsureBunnyZone(ctx, logic.TenantAliasZoneLabel); err != nil {
		r.logger.WithError(err).WithField("zone_label", logic.TenantAliasZoneLabel).Warn("Failed to ensure tenant alias Bunny zone")
		return
	}
	processed, err := r.certManager.ProcessPendingTenantAliases(ctx, r.rootDomain, r.acmeEmail)
	if err != nil {
		r.logger.WithError(err).Warn("Tenant alias intent worker failed")
		return
	}
	if processed > 0 {
		r.logger.WithField("count", processed).Debug("Processed tenant alias intents")
	}
}

// ensureGlobalPlatformCerts issues two multi-SAN platform certs covering
// the 8 per-service global zones under root:
//   - pool-assigned multi-SAN: foghorn/chandler/livepeer.{root}
//   - platform-edge multi-SAN:  edge*.{root}
//
// Foghorn fetches these from Navigator and distributes them via
// ConfigSeed only to platform-operated nodes (pool nodes / platform_official
// edges respectively). The DNS smart record sets that resolve these
// hostnames are managed separately — see ensureTLSBundles for the
// bundle pattern, and pkg/dns for the per-service zone reconciliation.
func (r *DNSReconciler) ensureGlobalPlatformCerts(ctx context.Context) {
	if r.certManager == nil || strings.TrimSpace(r.acmeEmail) == "" || strings.TrimSpace(r.rootDomain) == "" {
		return
	}

	for _, label := range logic.GlobalServiceZoneLabels() {
		if err := r.dnsManager.EnsureBunnyZone(ctx, label); err != nil {
			r.logger.WithError(err).WithField("zone_label", label).Warn("Failed to ensure global service Bunny zone")
			return
		}
	}

	if _, err := r.certManager.EnsurePoolAssignedGlobalCertificate(ctx, r.rootDomain, r.acmeEmail); err != nil {
		r.logger.WithError(err).Warn("Failed to ensure pool-assigned global multi-SAN certificate")
	}

	if _, err := r.certManager.EnsurePlatformEdgeGlobalCertificate(ctx, r.rootDomain, r.acmeEmail); err != nil {
		r.logger.WithError(err).Warn("Failed to ensure platform-edge global multi-SAN certificate")
	}
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
		if !usesBunnyClusterDNS(cluster) {
			continue
		}
		clusterSlug := logic.ClusterSlug(cluster)
		if clusterSlug == "" || clusterSlug == "default" {
			continue
		}
		if zoneErr := r.dnsManager.EnsureBunnyClusterZone(ctx, clusterSlug); zoneErr != nil {
			r.logger.WithError(zoneErr).WithField("cluster", cluster.GetClusterId()).Warn("Failed to ensure media cluster DNS zone")
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

func usesBunnyClusterDNS(cluster *proto.InfrastructureCluster) bool {
	return pkgdns.UsesBunnyClusterDNS(strings.TrimSpace(cluster.GetClusterType()))
}

// isGlobalServiceZone reports whether a serviceType's public subdomain is
// one of the code-owned Bunny global media labels.
func isGlobalServiceZone(serviceType string) bool {
	label, ok := pkgdns.PublicSubdomain(serviceType)
	if !ok || label == "" {
		return false
	}
	for _, configured := range logic.GlobalServiceZoneLabels() {
		if configured == label {
			return true
		}
	}
	return false
}

func (r *DNSReconciler) ensureTLSBundles(ctx context.Context) {
	if r.certManager == nil || r.qmClient == nil {
		return
	}

	resp, err := r.qmClient.ListTLSBundles(ctx, "", nil)
	if err != nil {
		r.logger.WithError(err).Warn("Failed to list tls bundles for reconciliation")
		return
	}

	for _, bundle := range resp.GetBundles() {
		if strings.TrimSpace(bundle.GetBundleId()) == "" || len(bundle.GetDomains()) == 0 {
			continue
		}
		email := strings.TrimSpace(bundle.GetEmail())
		if email == "" {
			email = r.acmeEmail
		}
		result, ensureErr := r.certManager.EnsureTLSBundle(ctx, bundle.GetBundleId(), bundle.GetDomains(), email)
		if ensureErr != nil {
			r.logger.WithError(ensureErr).WithField("bundle_id", bundle.GetBundleId()).Warn("Failed to ensure tls bundle")
			continue
		}
		r.logger.WithFields(logging.Fields{
			"bundle_id":  result.BundleID,
			"domains":    result.Domains,
			"expires_at": result.ExpiresAt,
			"cluster_id": bundle.GetClusterId(),
		}).Debug("Ensured tls bundle")
	}
}
