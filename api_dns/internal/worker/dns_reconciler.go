package worker

import (
	"context"
	"strings"
	"time"

	"frameworks/api_dns/internal/logic"
	pkgdns "github.com/Livepeer-FrameWorks/monorepo/pkg/dns"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	commonpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/common"
	quartermasterpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/quartermaster"
)

type DNSReconciler struct {
	dnsManager         *logic.DNSManager
	certManager        *logic.CertManager
	qmClient           quartermasterClient
	logger             logging.Logger
	interval           time.Duration
	rootDomain         string
	acmeEmail          string
	serviceTypes       []string
	healthStaleSeconds int
}

type quartermasterClient interface {
	ListClusters(ctx context.Context, pagination *commonpb.CursorPaginationRequest) (*quartermasterpb.ListClustersResponse, error)
	ListTLSBundles(ctx context.Context, clusterID string, pagination *commonpb.CursorPaginationRequest) (*quartermasterpb.ListTLSBundlesResponse, error)
	ListServiceInstancesByType(ctx context.Context, serviceType, clusterID string, staleThresholdSeconds int32) (*quartermasterpb.ListServiceInstancesByTypeResponse, error)
	ListIngressSites(ctx context.Context, clusterID, nodeID string, pagination *commonpb.CursorPaginationRequest) (*quartermasterpb.ListIngressSitesResponse, error)
}

func NewDNSReconciler(dnsManager *logic.DNSManager, certManager *logic.CertManager, qmClient quartermasterClient, logger logging.Logger, interval time.Duration, rootDomain, acmeEmail string, serviceTypes []string, healthStaleSeconds int) *DNSReconciler {
	return &DNSReconciler{
		dnsManager:         dnsManager,
		certManager:        certManager,
		qmClient:           qmClient,
		logger:             logger,
		interval:           interval,
		rootDomain:         rootDomain,
		acmeEmail:          acmeEmail,
		serviceTypes:       serviceTypes,
		healthStaleSeconds: healthStaleSeconds,
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
	r.ensureInfraZone(ctx)
	r.ensureTLSBundles(ctx)
	r.syncPhysicalInstanceEndpoints(ctx)
	r.processPendingTenantAliases(ctx)
	r.processPendingCustomDomains(ctx)
}

// ensureInfraZone delegates the infra Bunny zone before ensureTLSBundles issues
// the physical endpoint certs, so first-run DNS-01 for
// <service>.<node>.infra.<root> has a zone to place challenge records in instead
// of failing until the next tick.
func (r *DNSReconciler) ensureInfraZone(ctx context.Context) {
	if r.dnsManager == nil || len(pkgdns.PhysicalEndpointServiceTypes()) == 0 {
		return
	}
	if err := r.dnsManager.EnsureBunnyZone(ctx, pkgdns.InfraZoneLabel); err != nil {
		r.logger.WithError(err).Warn("Failed to ensure infra Bunny zone")
	}
}

// syncPhysicalInstanceEndpoints publishes per-instance infra A records
// (<service>.<node>.infra.<root>) for the services that need explicit instance
// addressing. Each endpoint is gated on a DESIRED physical ingress site existing
// (kind='physical'). That is the provisioning-complete signal, not proof the
// record is published or that the real cert has been synced to the node, so a
// residual DNS+cert-apply window remains. For VOD/chapter that window is covered
// by the fail-closed rendition validator → local-AV fallback; live STREAM_PROCESS
// has no such backstop and transiently degrades to source-only within it.
func (r *DNSReconciler) syncPhysicalInstanceEndpoints(ctx context.Context) {
	if r.dnsManager == nil || r.qmClient == nil {
		return
	}
	for _, serviceType := range pkgdns.PhysicalEndpointServiceTypes() {
		// Periodic backstop: operational details are logged inside; the next tick
		// retries, so just note it and move on.
		if _, err := r.syncPhysicalInstanceEndpointsForType(ctx, serviceType); err != nil {
			r.logger.WithError(err).WithField("service_type", serviceType).Debug("Periodic infra DNS sync had errors; will retry next tick")
		}
	}
}

// SyncPhysicalInstanceEndpointsForType refreshes the per-instance infra A records
// for one physical-endpoint service type, on demand. It is the event-driven entry
// (Navigator.SyncDNS) so a node/service change refreshes infra DNS immediately
// instead of waiting for the next reconcile tick — every box runs several services,
// so node-keyed records must not lag a poll interval. Ensuring the infra zone first
// keeps a fresh node's first publish from failing before the periodic loop runs.
// No-op (nil, nil) for non-physical types. Returns partial errors and a hard error
// so the event-driven SyncDNS caller can report failure instead of silently
// succeeding while infra records did not refresh.
func (r *DNSReconciler) SyncPhysicalInstanceEndpointsForType(ctx context.Context, serviceType string) (map[string]string, error) {
	if r.dnsManager == nil || r.qmClient == nil || !pkgdns.IsPhysicalEndpointServiceType(serviceType) {
		return nil, nil
	}
	r.ensureInfraZone(ctx)
	return r.syncPhysicalInstanceEndpointsForType(ctx, serviceType)
}

func (r *DNSReconciler) syncPhysicalInstanceEndpointsForType(ctx context.Context, serviceType string) (map[string]string, error) {
	resp, err := r.qmClient.ListServiceInstancesByType(ctx, serviceType, "", int32(r.healthStaleSeconds))
	if err != nil {
		r.logger.WithError(err).WithField("service_type", serviceType).Warn("Failed to list physical service instances for infra DNS")
		return nil, err
	}
	endpoints := make([]logic.PhysicalInstanceEndpoint, 0, len(resp.GetInstances()))
	// A transient ingress-gate lookup error must not let a healthy record be
	// pruned as "no longer desired": suppress pruning for this whole cycle
	// so a QM blip cannot delete valid physical A records.
	gateErrored := false
	for _, inst := range resp.GetInstances() {
		fqdn := strings.TrimSpace(inst.GetPublicInstanceHost())
		ip := strings.TrimSpace(inst.GetExternalIp())
		if fqdn == "" || ip == "" {
			continue
		}
		provisioned, gateErr := r.hasPhysicalIngress(ctx, inst.GetClusterId(), inst.GetNodeId(), fqdn)
		if gateErr != nil {
			gateErrored = true
			r.logger.WithError(gateErr).WithFields(logging.Fields{
				"service_type": serviceType,
				"node_id":      inst.GetNodeId(),
			}).Warn("Ingress gate lookup failed; preserving existing infra records this cycle")
			continue
		}
		if !provisioned {
			r.logger.WithFields(logging.Fields{
				"service_type": serviceType,
				"node_id":      inst.GetNodeId(),
				"fqdn":         fqdn,
			}).Debug("Skipping infra DNS for node without provisioned physical ingress")
			continue
		}
		endpoints = append(endpoints, logic.PhysicalInstanceEndpoint{
			NodeID:     inst.GetNodeId(),
			ExternalIP: ip,
			FQDN:       fqdn,
		})
	}
	partialErrors, syncErr := r.dnsManager.SyncPhysicalInstanceEndpoints(ctx, serviceType, endpoints, !gateErrored)
	if syncErr != nil {
		r.logger.WithError(syncErr).WithField("service_type", serviceType).Warn("Infra DNS reconciliation failed")
	}
	if len(partialErrors) > 0 {
		r.logger.WithField("service_type", serviceType).WithField("partial_errors", partialErrors).Warn("Infra DNS reconciliation completed with partial errors")
	}
	return partialErrors, syncErr
}

// hasPhysicalIngress reports whether the node already has a DESIRED physical
// ingress site whose domains include fqdn — the gate that keeps Navigator from
// publishing a physical A record before the node is even provisioned for it
// (it does not prove nginx is serving the real cert yet). The error return is
// distinct from a clean "not provisioned" so the caller can tell a transient
// lookup failure (preserve records) from a real absence (prune).
func (r *DNSReconciler) hasPhysicalIngress(ctx context.Context, clusterID, nodeID, fqdn string) (bool, error) {
	if strings.TrimSpace(nodeID) == "" {
		return false, nil
	}
	want := strings.ToLower(strings.TrimSpace(fqdn))
	// Walk every page: ListIngressSites is cursor-paginated, and a node with
	// enough sites would otherwise hide the physical site past page one and look
	// unprovisioned — which would prune a valid physical A record.
	var after *string
	for {
		page := &commonpb.CursorPaginationRequest{First: 100, After: after}
		resp, err := r.qmClient.ListIngressSites(ctx, clusterID, nodeID, page)
		if err != nil {
			return false, err
		}
		for _, site := range resp.GetSites() {
			if site.GetKind() != "physical" {
				continue
			}
			for _, d := range site.GetDomains() {
				if strings.ToLower(strings.TrimSpace(d)) == want {
					return true, nil
				}
			}
		}
		pg := resp.GetPagination()
		if pg == nil || !pg.GetHasNextPage() || pg.GetEndCursor() == "" {
			return false, nil
		}
		cursor := pg.GetEndCursor()
		after = &cursor
	}
}

// processPendingCustomDomains drives the customer-owned (BYO) domain
// lifecycle: DNS verification → ACME issuance → cert_issued → teardown.
// Verification needs the tenant's alias subdomain (the custom domain
// CNAMEs into the tenant alias zone), so we fetch it on demand.
func (r *DNSReconciler) processPendingCustomDomains(ctx context.Context) {
	if r.certManager == nil || strings.TrimSpace(r.acmeEmail) == "" || strings.TrimSpace(r.rootDomain) == "" {
		return
	}
	if err := r.dnsManager.EnsureBunnyZone(ctx, logic.AcmeDNSZoneLabel); err != nil {
		r.logger.WithError(err).WithField("zone_label", logic.AcmeDNSZoneLabel).Warn("Failed to ensure acme-dns Bunny zone")
		return
	}
	lookup := func(ctx context.Context, tenantID string) (string, error) {
		alias, err := r.certManager.GetTenantAlias(ctx, tenantID)
		if err != nil {
			return "", err
		}
		return alias.Subdomain, nil
	}
	processed, err := r.certManager.ProcessPendingCustomDomains(ctx, r.rootDomain, r.acmeEmail, lookup)
	if err != nil {
		r.logger.WithError(err).Warn("Custom-domain worker failed")
		return
	}
	if processed > 0 {
		r.logger.WithField("count", processed).Debug("Processed custom-domain transitions")
	}
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
		bundle, certErr := r.certManager.EnsureClusterWildcardCertificate(ctx, clusterSlug, r.rootDomain, r.acmeEmail)
		if certErr != nil {
			r.logger.WithError(certErr).WithField("cluster", cluster.GetClusterId()).Warn("Failed to ensure cluster TLS bundle")
			continue
		}
		r.logger.WithFields(logging.Fields{
			"cluster":    cluster.GetClusterId(),
			"bundle_id":  bundle.BundleID,
			"domains":    bundle.Domains,
			"expires_at": bundle.ExpiresAt,
		}).Debug("Ensured cluster TLS bundle")
	}
}

func usesBunnyClusterDNS(cluster *quartermasterpb.InfrastructureCluster) bool {
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
