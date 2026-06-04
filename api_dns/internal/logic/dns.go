package logic

import (
	"context"
	"fmt"
	"maps"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"frameworks/api_dns/internal/provider/bunny"
	"frameworks/api_dns/internal/provider/cloudflare"
	pkgdns "github.com/Livepeer-FrameWorks/monorepo/pkg/dns"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/proto"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/servicedefs"
)

// MonitorConfig holds Cloudflare health monitor settings
type MonitorConfig struct {
	Interval int // Health check interval in seconds
	Timeout  int // Health check timeout in seconds
	Retries  int // Number of retries before marking unhealthy
}

// CertChecker tests whether a cluster has a valid wildcard TLS certificate.
// Used to gate granular edge service subdomains. Without a wildcard cert,
// edge nodes can't terminate TLS for service-specific domains.
type CertChecker interface {
	HasClusterWildcardCert(ctx context.Context, clusterSlug, rootDomain string) bool
}

type DNSManager struct {
	cfClient      cloudflareClient
	bunnyClient   bunnyClient
	qmClient      quartermasterClient
	logger        logging.Logger
	domain        string // Root domain e.g. frameworks.network
	proxy         map[string]bool
	recordTTL     int
	lbTTL         int
	staleAge      time.Duration
	monitorConfig MonitorConfig
	servicePorts  map[string]int    // Service type -> HTTP health check port
	healthPaths   map[string]string // Service type -> health check path
	certChecker   CertChecker       // optional; nil = no cert gating

	bunnyCacheMu             sync.Mutex
	bunnyZoneCache           map[string]*bunny.Zone
	bunnyDelegationCheckedAt map[string]time.Time
	cloudflareCleanupAt      map[string]time.Time
}

type cloudflareClient interface {
	ListLoadBalancers() ([]cloudflare.LoadBalancer, error)
	DeleteLoadBalancer(loadBalancerID string) error
	ListDNSRecords(recordType, name string) ([]cloudflare.DNSRecord, error)
	UpdateDNSRecord(recordID string, record cloudflare.DNSRecord) (*cloudflare.DNSRecord, error)
	DeleteDNSRecord(recordID string) error
	CreateDNSRecord(record cloudflare.DNSRecord) (*cloudflare.DNSRecord, error)
	CreateARecord(name, content string, proxied bool, ttl int) (*cloudflare.DNSRecord, error)
	GetPool(poolID string) (*cloudflare.Pool, error)
	DeletePool(poolID string) error
	RemoveOriginFromPool(poolID, originIP string) (*cloudflare.Pool, error)
	AddOriginToPool(poolID string, origin cloudflare.Origin) (*cloudflare.Pool, error)
	CreateLoadBalancer(lb cloudflare.LoadBalancer) (*cloudflare.LoadBalancer, error)
	GetLoadBalancer(lbID string) (*cloudflare.LoadBalancer, error)
	UpdateLoadBalancer(lbID string, lb cloudflare.LoadBalancer) (*cloudflare.LoadBalancer, error)
	ListMonitors() ([]cloudflare.Monitor, error)
	CreateMonitor(monitor cloudflare.Monitor) (*cloudflare.Monitor, error)
	ListPools() ([]cloudflare.Pool, error)
	UpdatePool(poolID string, pool cloudflare.Pool) (*cloudflare.Pool, error)
	CreatePool(pool cloudflare.Pool) (*cloudflare.Pool, error)
}

type bunnyClient interface {
	EnsureZone(ctx context.Context, domain string) (*bunny.Zone, bool, error)
	FindZone(ctx context.Context, domain string) (*bunny.Zone, bool, error)
	ListRecords(ctx context.Context, zoneID int64) ([]bunny.Record, error)
	ReconcileRecordSet(ctx context.Context, zoneID int64, name string, recordType int, desired []bunny.Record) error
}

type dnsNode struct {
	NodeID     string
	ClusterID  string
	ExternalIP string
	Region     string
	Latitude   *float64
	Longitude  *float64
}

type desiredPool struct {
	Name        string
	ServiceType string
	Nodes       []dnsNode
	Latitude    *float64
	Longitude   *float64
}

const (
	rootIngressPoolServiceType = "public-ingress"
	dnsProviderHousekeepingTTL = 10 * time.Minute
)

type quartermasterClient interface {
	ListHealthyNodesForDNS(ctx context.Context, staleThresholdSeconds int, serviceType string) (*proto.ListHealthyNodesForDNSResponse, error)
	ListHealthyNodesForDNSForCluster(ctx context.Context, staleThresholdSeconds int, serviceType, clusterID string) (*proto.ListHealthyNodesForDNSResponse, error)
	GetCluster(ctx context.Context, clusterID string) (*proto.ClusterResponse, error)
	ListClusters(ctx context.Context, pagination *proto.CursorPaginationRequest) (*proto.ListClustersResponse, error)
}

// NewDNSManager creates a new DNSManager
func NewDNSManager(cf cloudflareClient, qm quartermasterClient, logger logging.Logger, rootDomain string, recordTTL int, lbTTL int, staleAge time.Duration, monitorConfig MonitorConfig) *DNSManager {
	return &DNSManager{
		cfClient:                 cf,
		qmClient:                 qm,
		logger:                   logger,
		domain:                   rootDomain,
		proxy:                    loadProxyServices(),
		recordTTL:                recordTTL,
		lbTTL:                    lbTTL,
		staleAge:                 staleAge,
		monitorConfig:            monitorConfig,
		servicePorts:             defaultServicePorts(),
		healthPaths:              defaultServiceHealthPaths(),
		bunnyZoneCache:           map[string]*bunny.Zone{},
		bunnyDelegationCheckedAt: map[string]time.Time{},
		cloudflareCleanupAt:      map[string]time.Time{},
	}
}

// SetCertChecker configures the optional certificate checker for gating
// granular edge service subdomains. When set, SyncServiceByCluster skips
// creating DNS records for edge-egress/ingest/storage/processing when the
// cluster lacks a valid wildcard cert.
func (m *DNSManager) SetCertChecker(checker CertChecker) {
	m.certChecker = checker
}

func (m *DNSManager) SetBunnyClient(client bunnyClient) {
	m.bunnyClient = client
}

func (m *DNSManager) EnsureBunnyClusterZone(ctx context.Context, clusterSlug string) error {
	if m.bunnyClient == nil {
		return nil
	}
	clusterSlug = strings.TrimSpace(clusterSlug)
	if clusterSlug == "" {
		return fmt.Errorf("cluster slug is required")
	}
	zoneDomain := fmt.Sprintf("%s.%s", clusterSlug, m.domain)
	_, _, err := m.ensureBunnyZoneDelegation(ctx, zoneDomain, logging.Fields{"cluster": clusterSlug})
	return err
}

// EnsureBunnyZone creates the Bunny zone at "{label}.{root}" if it
// does not already exist and (re)publishes the Cloudflare NS delegation
// records pointing at Bunny's nameservers. This is the generic helper
// for new zones added by the DNS namespace model (per-service global
// zones like foghorn.frameworks.network, the shared cdn.frameworks.network
// for tenant aliases, etc.).
//
// Calling this is idempotent and safe to invoke before every cert
// issuance against a zone; it short-circuits when both the Bunny
// zone and the NS records are already in place.
func (m *DNSManager) EnsureBunnyZone(ctx context.Context, label string) error {
	if m.bunnyClient == nil {
		return nil
	}
	label = strings.TrimSpace(label)
	if label == "" {
		return fmt.Errorf("zone label is required")
	}
	zoneDomain := fmt.Sprintf("%s.%s", label, m.domain)
	_, _, err := m.ensureBunnyZoneDelegation(ctx, zoneDomain, logging.Fields{"zone_label": label})
	return err
}

// isGranularEdgeService returns true for service types that require a wildcard
// cert on the edge for TLS termination.
func isGranularEdgeService(serviceType string) bool {
	switch serviceType {
	case "edge-egress", "edge-ingest", "edge-storage", "edge-processing":
		return true
	}
	return false
}

// defaultServicePorts returns the default HTTP health check port for each service type,
// sourced from the canonical servicedefs registry where possible.
func defaultServicePorts() map[string]int {
	ports := map[string]int{
		"edge":            18008,
		"edge-egress":     18008,
		"edge-ingest":     18008,
		"edge-storage":    18008,
		"edge-processing": 18008,
	}
	for _, name := range pkgdns.ManagedServiceTypes() {
		if _, exists := ports[name]; exists {
			continue
		}
		if svc, ok := servicedefs.Lookup(name); ok {
			ports[name] = svc.DefaultPort
		}
	}
	return ports
}

// defaultServiceHealthPaths returns the health check path for each service type.
func defaultServiceHealthPaths() map[string]string {
	paths := make(map[string]string)
	for _, e := range []string{"edge", "edge-egress", "edge-ingest", "edge-storage", "edge-processing"} {
		paths[e] = "/health"
	}
	for _, name := range pkgdns.ManagedServiceTypes() {
		if _, exists := paths[name]; exists {
			continue
		}
		if svc, ok := servicedefs.Lookup(name); ok && svc.HealthPath != "" {
			paths[name] = svc.HealthPath
		}
	}
	return paths
}

func loadProxyServices() map[string]bool {
	env := strings.TrimSpace(os.Getenv("NAVIGATOR_PROXY_SERVICES"))
	if env == "" {
		return map[string]bool{
			"bridge":    true,
			"chartroom": true,
			"chatwoot":  true,
			"foredeck":  true,
			"grafana":   true,
			"listmonk":  true,
			"logbook":   true,
			"metabase":  true,
			"steward":   true,
		}
	}

	proxy := make(map[string]bool)
	for _, svc := range strings.Split(env, ",") {
		name := strings.TrimSpace(svc)
		if name == "" {
			continue
		}
		proxy[name] = true
	}
	delete(proxy, "livepeer-gateway")
	return proxy
}

func (m *DNSManager) shouldProxy(serviceType string) bool {
	return m.proxy[serviceType]
}

// SanitizeLabel normalizes a string for use as a DNS label (lowercase, hyphens only).
func SanitizeLabel(raw string) string {
	return pkgdns.SanitizeLabel(raw)
}

// ClusterSlug returns a DNS-safe slug for a cluster, preferring cluster_id over cluster_name.
func ClusterSlug(cluster *proto.InfrastructureCluster) string {
	if cluster == nil {
		return "default"
	}
	return pkgdns.ClusterSlug(cluster.GetClusterId(), cluster.GetClusterName())
}

func (m *DNSManager) clusterSlug(cluster *proto.InfrastructureCluster) string {
	return ClusterSlug(cluster)
}

func edgeNodeRecordName(nodeID, rootDomain string) string {
	nodeLabel := SanitizeLabel(nodeID)
	if strings.HasPrefix(nodeLabel, "edge-") {
		return fmt.Sprintf("%s.%s", nodeLabel, rootDomain)
	}
	return fmt.Sprintf("edge-%s.%s", nodeLabel, rootDomain)
}

func (m *DNSManager) SyncServiceByCluster(ctx context.Context, serviceType string) (map[string]string, error) {
	partialErrors := map[string]string{}

	clustersResp, err := m.qmClient.ListClusters(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to list clusters: %w", err)
	}

	nodesResp, err := m.qmClient.ListHealthyNodesForDNS(ctx, int(m.staleAge.Seconds()), serviceType)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch nodes from Quartermaster: %w", err)
	}

	nodesByCluster := make(map[string][]*proto.InfrastructureNode)
	for _, node := range nodesResp.GetNodes() {
		clusterID := node.GetClusterId()
		if clusterID == "" {
			continue
		}
		nodesByCluster[clusterID] = append(nodesByCluster[clusterID], node)
	}

	sort.Slice(clustersResp.Clusters, func(i, j int) bool {
		return clustersResp.Clusters[i].GetClusterId() < clustersResp.Clusters[j].GetClusterId()
	})

	provider := pkgdns.ProviderForServiceType(serviceType)
	if provider == pkgdns.ProviderBunny && m.bunnyClient == nil {
		m.logger.WithField("service_type", serviceType).Warn("Bunny DNS is not configured; using Cloudflare cluster-scoped fallback")
	}

	for _, cluster := range clustersResp.Clusters {
		nodes := dnsNodesFromProto(nodesByCluster[cluster.GetClusterId()])
		m.syncOneCluster(ctx, serviceType, provider, cluster, nodes, false, partialErrors)
	}

	if provider == pkgdns.ProviderBunny {
		if cleanupErrors, err := m.clearUnsupportedRootServiceDNS(ctx, serviceType); err != nil {
			partialErrors[serviceType+":root-cleanup"] = err.Error()
		} else {
			maps.Copy(partialErrors, cleanupErrors)
		}
	}

	if len(partialErrors) == 0 {
		return nil, nil
	}
	return partialErrors, nil
}

// SyncServiceForCluster reconciles DNS for a single cluster's edge service
// type. Called by Navigator's SyncDNS RPC when Quartermaster targets a
// specific (cluster, service_type) wakeup. Authoritative: an empty healthy
// node set actively clears the cluster service record, unlike the polling
// path which preserves it (transient QM-failure tolerance).
func (m *DNSManager) SyncServiceForCluster(ctx context.Context, serviceType, clusterID string) (map[string]string, error) {
	partialErrors := map[string]string{}

	clusterResp, err := m.qmClient.GetCluster(ctx, clusterID)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch cluster from Quartermaster: %w", err)
	}
	target := clusterResp.GetCluster()
	if target == nil {
		return nil, fmt.Errorf("cluster %q not found", clusterID)
	}

	nodesResp, err := m.qmClient.ListHealthyNodesForDNSForCluster(ctx, int(m.staleAge.Seconds()), serviceType, clusterID)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch nodes from Quartermaster: %w", err)
	}

	nodes := dnsNodesFromProto(nodesResp.GetNodes())

	provider := pkgdns.ProviderForServiceType(serviceType)
	if provider == pkgdns.ProviderBunny && m.bunnyClient == nil {
		m.logger.WithField("service_type", serviceType).Warn("Bunny DNS is not configured; using Cloudflare cluster-scoped fallback")
	}

	m.syncOneCluster(ctx, serviceType, provider, target, nodes, true, partialErrors)
	if provider == pkgdns.ProviderBunny && target.GetIsPlatformOfficial() && hasGlobalRootLabel(serviceType) {
		rootPartial, rootErr := m.syncBunnyRootService(ctx, serviceType, true)
		if rootErr != nil {
			partialErrors[serviceType+":global-root"] = rootErr.Error()
		} else {
			maps.Copy(partialErrors, rootPartial)
		}
	}

	if len(partialErrors) == 0 {
		return nil, nil
	}
	return partialErrors, nil
}

func hasGlobalRootLabel(serviceType string) bool {
	label, ok := pkgdns.PublicSubdomain(serviceType)
	return ok && strings.TrimSpace(label) != ""
}

// syncOneCluster reconciles DNS for a single cluster's service type (edge or
// pool-assigned). authoritative controls empty-set handling:
//
//   - false (polling caller): preserve the cluster service record on empty
//     healthy set. Transient QM unavailability or first-deploy races would
//     otherwise blow DNS away.
//   - true (targeted Navigator wakeup from QM): the empty set IS the
//     authoritative signal. Clear the cluster service record actively.
//
// Per-node edge-<node_id> records (edge-egress only) are always reconciled.
func (m *DNSManager) syncOneCluster(
	ctx context.Context,
	serviceType string,
	provider pkgdns.Provider,
	cluster *proto.InfrastructureCluster,
	nodes []dnsNode,
	authoritative bool,
	partialErrors map[string]string,
) {
	clusterSlug := m.clusterSlug(cluster)
	rootDomain := fmt.Sprintf("%s.%s", clusterSlug, m.domain)
	useBunny := provider == pkgdns.ProviderBunny && m.bunnyClient != nil
	svcFQDN := m.clusterServiceFQDN(serviceType, rootDomain)

	if useBunny && !pkgdns.UsesBunnyClusterDNS(strings.TrimSpace(cluster.GetClusterType())) {
		if err := m.clearBunnyClusterService(ctx, svcFQDN, serviceType, rootDomain); err != nil {
			partialErrors[svcFQDN] = err.Error()
		}
		return
	}

	if !cluster.GetIsActive() {
		if useBunny {
			if err := m.clearBunnyClusterService(ctx, svcFQDN, serviceType, rootDomain); err != nil {
				partialErrors[svcFQDN] = err.Error()
			}
		} else if _, err := m.clearDNSConfig(ctx, svcFQDN); err != nil {
			partialErrors[svcFQDN] = err.Error()
		} else {
			m.logger.WithFields(logging.Fields{
				"service_type": serviceType,
				"cluster":      clusterSlug,
				"fqdn":         svcFQDN,
			}).Info("Cleared DNS for inactive cluster")
		}
		if serviceType == "edge-egress" {
			maps.Copy(partialErrors, m.clearEdgeNodeRecords(rootDomain))
		}
		return
	}

	// Authoritative empty-clear runs BEFORE the cert gate: a node leaving the
	// healthy set must propagate to DNS even if cluster cert state has
	// regressed since the last create. The cert gate only protects record
	// creation, not removal.
	if len(nodes) == 0 && authoritative {
		m.logger.WithFields(logging.Fields{
			"service_type": serviceType,
			"cluster":      clusterSlug,
			"fqdn":         svcFQDN,
		}).Info("Authoritative empty set; clearing cluster service record")
		if useBunny {
			if err := m.clearBunnyClusterService(ctx, svcFQDN, serviceType, rootDomain); err != nil {
				partialErrors[svcFQDN] = err.Error()
			}
		} else if _, err := m.clearDNSConfig(ctx, svcFQDN); err != nil {
			partialErrors[svcFQDN] = err.Error()
		}
		if serviceType == "edge-egress" {
			if provider == pkgdns.ProviderBunny && useBunny {
				maps.Copy(partialErrors, m.syncBunnyEdgeNodeRecords(ctx, rootDomain, nil))
			} else {
				maps.Copy(partialErrors, m.clearEdgeNodeRecords(rootDomain))
			}
		}
		return
	}

	if m.certChecker != nil && isGranularEdgeService(serviceType) {
		if !m.certChecker.HasClusterWildcardCert(ctx, clusterSlug, m.domain) {
			m.logger.WithFields(logging.Fields{
				"service_type": serviceType,
				"cluster":      clusterSlug,
			}).Debug("Skipping granular edge subdomain — no wildcard cert for cluster")
			return
		}
	}

	if len(nodes) == 0 {
		m.logger.WithFields(logging.Fields{
			"service_type": serviceType,
			"cluster":      clusterSlug,
			"fqdn":         svcFQDN,
		}).Warn("No healthy nodes for cluster; preserving existing DNS")
	} else {
		var svcPartial map[string]string
		var syncErr error
		if useBunny {
			svcPartial, syncErr = m.syncBunnyClusterService(ctx, svcFQDN, serviceType, rootDomain, nodes)
		} else {
			svcPartial, syncErr = m.syncClusterService(ctx, svcFQDN, serviceType, nodes)
		}
		if syncErr != nil {
			partialErrors[svcFQDN] = syncErr.Error()
		} else {
			maps.Copy(partialErrors, svcPartial)
		}
	}

	if serviceType != "edge-egress" {
		return
	}

	if provider == pkgdns.ProviderBunny {
		if useBunny {
			maps.Copy(partialErrors, m.syncBunnyEdgeNodeRecords(ctx, rootDomain, nodes))
		} else {
			maps.Copy(partialErrors, m.clearEdgeNodeRecords(rootDomain))
		}
		return
	}

	desiredNodeRecords := map[string]string{}
	for _, node := range nodes {
		if node.ExternalIP == "" {
			continue
		}
		fqdn := edgeNodeRecordName(node.NodeID, rootDomain)
		desiredNodeRecords[fqdn] = node.ExternalIP
		if err := m.applySingleNodeConfig(ctx, fqdn, node.ExternalIP, false); err != nil {
			partialErrors[fqdn] = err.Error()
		}
	}

	aRecords, listErr := m.cfClient.ListDNSRecords("A", "")
	if listErr != nil {
		partialErrors[fmt.Sprintf("edge-nodes.%s", rootDomain)] = listErr.Error()
		return
	}
	prefix := "edge-"
	suffix := "." + rootDomain
	for _, rec := range aRecords {
		if !isEdgeNodeRecord(rec.Name, prefix, suffix) {
			continue
		}
		if _, keep := desiredNodeRecords[rec.Name]; keep {
			continue
		}
		if err := m.cfClient.DeleteDNSRecord(rec.ID); err != nil {
			partialErrors[rec.Name] = err.Error()
		}
	}
}

func edgeNodeRecordLabel(nodeID string) string {
	return pkgdns.EdgeNodeLabel(nodeID)
}

func (m *DNSManager) syncBunnyEdgeNodeRecords(ctx context.Context, zoneDomain string, nodes []dnsNode) map[string]string {
	partialErrors := map[string]string{}
	zone, ok, err := m.bunnyClient.FindZone(ctx, zoneDomain)
	if err != nil {
		return map[string]string{fmt.Sprintf("edge-nodes.%s", zoneDomain): err.Error()}
	}
	if !ok || zone == nil {
		return map[string]string{fmt.Sprintf("edge-nodes.%s", zoneDomain): "Bunny zone not found"}
	}

	desiredLabels := map[string]struct{}{}
	for _, node := range nodes {
		if node.ExternalIP == "" {
			continue
		}
		label := edgeNodeRecordLabel(node.NodeID)
		fqdn := bunnyRecordFQDN(label, zoneDomain)
		desiredLabels[label] = struct{}{}
		records := []bunny.Record{{
			Type:             bunny.RecordTypeA,
			Name:             label,
			Value:            node.ExternalIP,
			TTL:              m.recordTTL,
			Weight:           100,
			MonitorType:      bunny.MonitorTypeNone,
			SmartRoutingType: bunny.SmartRoutingNone,
			Comment:          fmt.Sprintf("Managed by Navigator for %s", fqdn),
		}}
		if reconcileErr := m.bunnyClient.ReconcileRecordSet(ctx, zone.ID, label, bunny.RecordTypeA, records); reconcileErr != nil {
			partialErrors[fqdn] = reconcileErr.Error()
		}
	}

	current, listErr := m.bunnyClient.ListRecords(ctx, zone.ID)
	if listErr != nil {
		partialErrors[fmt.Sprintf("edge-nodes.%s", zoneDomain)] = listErr.Error()
		return partialErrors
	}
	for _, record := range current {
		if record.Type != bunny.RecordTypeA || !isEdgeNodeRecordLabel(record.Name) {
			continue
		}
		label := SanitizeLabel(record.Name)
		if _, keep := desiredLabels[label]; keep {
			continue
		}
		if reconcileErr := m.bunnyClient.ReconcileRecordSet(ctx, zone.ID, label, bunny.RecordTypeA, nil); reconcileErr != nil {
			partialErrors[bunnyRecordFQDN(label, zoneDomain)] = reconcileErr.Error()
		}
	}

	if len(partialErrors) == 0 {
		return nil
	}
	return partialErrors
}

// PhysicalInstanceEndpoint is one running service instance addressed under the
// infra namespace (<service>.<node>.infra.<root>) by its node external IP.
type PhysicalInstanceEndpoint struct {
	NodeID     string
	ExternalIP string
	FQDN       string // public_instance_host synthesized by Quartermaster
}

// SyncPhysicalInstanceEndpoints publishes and prunes the per-instance infra A
// records for one service type into the infra.<root> Bunny zone. Endpoints must
// be pre-gated by the caller on a DESIRED physical ingress site existing for the
// FQDN (desired-state, not proof nginx is serving the cert yet). When
// allowPrune is false (e.g. the caller could not confirm the full desired set
// because an upstream lookup errored) the stale-record sweep is skipped so a
// transient error never deletes a healthy record.
func (m *DNSManager) SyncPhysicalInstanceEndpoints(ctx context.Context, serviceType string, endpoints []PhysicalInstanceEndpoint, allowPrune bool) (map[string]string, error) {
	if m.bunnyClient == nil {
		return nil, nil
	}
	zoneDomain := pkgdns.InfraZoneFQDN(m.domain)
	if zoneDomain == "" {
		return nil, fmt.Errorf("infra zone domain unresolved for root %q", m.domain)
	}
	if err := m.EnsureBunnyZone(ctx, pkgdns.InfraZoneLabel); err != nil {
		return nil, fmt.Errorf("ensure infra zone: %w", err)
	}
	zone, ok, err := m.bunnyClient.FindZone(ctx, zoneDomain)
	if err != nil {
		return nil, fmt.Errorf("find infra zone %s: %w", zoneDomain, err)
	}
	if !ok || zone == nil {
		return map[string]string{zoneDomain: "Bunny infra zone not found"}, nil
	}

	zoneSuffix := "." + zoneDomain
	servicePrefix := pkgdns.SanitizeLabel(serviceType) + "."
	partialErrors := map[string]string{}
	desired := map[string]struct{}{}

	for _, ep := range endpoints {
		fqdn := strings.TrimSuffix(strings.ToLower(strings.TrimSpace(ep.FQDN)), ".")
		if fqdn == "" || ep.ExternalIP == "" || !strings.HasSuffix(fqdn, zoneSuffix) {
			continue
		}
		recordName := strings.TrimSuffix(fqdn, zoneSuffix)
		if recordName == "" {
			continue
		}
		desired[recordName] = struct{}{}
		records := []bunny.Record{{
			Type:             bunny.RecordTypeA,
			Name:             recordName,
			Value:            ep.ExternalIP,
			TTL:              m.recordTTL,
			Weight:           100,
			MonitorType:      bunny.MonitorTypeNone,
			SmartRoutingType: bunny.SmartRoutingNone,
			Comment:          fmt.Sprintf("Managed by Navigator for %s", fqdn),
		}}
		if reconcileErr := m.bunnyClient.ReconcileRecordSet(ctx, zone.ID, recordName, bunny.RecordTypeA, records); reconcileErr != nil {
			partialErrors[fqdn] = reconcileErr.Error()
		}
	}

	// Prune this service type's stale infra A records (records whose name
	// starts with "<service>." but are no longer desired). Scoped to the
	// service prefix so it never touches another service's endpoints. Skipped
	// when the caller could not confirm the full desired set this cycle.
	if allowPrune {
		current, listErr := m.bunnyClient.ListRecords(ctx, zone.ID)
		if listErr != nil {
			partialErrors[zoneDomain] = listErr.Error()
			return partialErrors, fmt.Errorf("list infra zone records: %w", listErr)
		}
		for _, record := range current {
			if record.Type != bunny.RecordTypeA {
				continue
			}
			name := strings.ToLower(strings.TrimSpace(record.Name))
			if !strings.HasPrefix(name, servicePrefix) {
				continue
			}
			if _, keep := desired[name]; keep {
				continue
			}
			if reconcileErr := m.bunnyClient.ReconcileRecordSet(ctx, zone.ID, name, bunny.RecordTypeA, nil); reconcileErr != nil {
				partialErrors[bunnyRecordFQDN(name, zoneDomain)] = reconcileErr.Error()
			}
		}
	}

	if len(partialErrors) == 0 {
		return nil, nil
	}
	return partialErrors, nil
}

func (m *DNSManager) clearEdgeNodeRecords(rootDomain string) map[string]string {
	partialErrors := map[string]string{}
	aRecords, listErr := m.cfClient.ListDNSRecords("A", "")
	if listErr != nil {
		partialErrors[fmt.Sprintf("edge-nodes.%s", rootDomain)] = listErr.Error()
		return partialErrors
	}
	prefix := "edge-"
	suffix := "." + rootDomain
	for _, rec := range aRecords {
		if !isEdgeNodeRecord(rec.Name, prefix, suffix) {
			continue
		}
		if err := m.cfClient.DeleteDNSRecord(rec.ID); err != nil {
			partialErrors[rec.Name] = err.Error()
		}
	}
	if len(partialErrors) == 0 {
		return nil
	}
	return partialErrors
}

func (m *DNSManager) clearUnsupportedRootServiceDNS(ctx context.Context, serviceType string) (map[string]string, error) {
	subdomain, ok := pkgdns.PublicSubdomain(serviceType)
	if !ok || subdomain == "" {
		return nil, nil
	}
	return m.clearDNSConfig(ctx, subdomain+"."+m.domain)
}

func (m *DNSManager) clearBunnyClusterService(ctx context.Context, fqdn, serviceType, zoneDomain string) error {
	zone, ok, err := m.bunnyClient.FindZone(ctx, zoneDomain)
	if err != nil {
		return fmt.Errorf("failed to find Bunny zone %s: %w", zoneDomain, err)
	}
	if ok {
		recordNames, nameOK := bunnyRecordNames(serviceType)
		if !nameOK {
			return fmt.Errorf("unknown Bunny service type: %s", serviceType)
		}
		for _, recordName := range recordNames {
			if err := m.bunnyClient.ReconcileRecordSet(ctx, zone.ID, recordName, bunny.RecordTypeA, nil); err != nil {
				return err
			}
		}
	}
	if _, err := m.clearDNSConfig(ctx, fqdn); err != nil {
		return err
	}
	if serviceType == "foghorn" {
		if _, err := m.clearDNSConfig(ctx, zoneDomain); err != nil {
			return err
		}
	}
	return nil
}

// SyncBunnyRootService publishes the global root entrypoint smart record
// set for a Bunny-managed service. Example: serviceType="foghorn",
// rootDomain="frameworks.network" → publishes the smart-routed A record
// set at the apex of zone "foghorn.frameworks.network".
//
// Membership rule: only nodes in platform_official clusters are included.
// Third-party / tenant-private edges are NEVER members of the global root
// entrypoint; they appear under their own cluster scope and (for paid
// tenants) under the tenant alias zone.
//
// Caller must have already delegated the per-service zone via
// EnsureBunnyZone(serviceType). No-ops cleanly if Bunny is not configured.
func (m *DNSManager) SyncBunnyRootService(ctx context.Context, serviceType string) (map[string]string, error) {
	return m.syncBunnyRootService(ctx, serviceType, false)
}

func (m *DNSManager) syncBunnyRootService(ctx context.Context, serviceType string, authoritative bool) (map[string]string, error) {
	if m.bunnyClient == nil {
		return nil, nil
	}
	if pkgdns.ProviderForServiceType(serviceType) != pkgdns.ProviderBunny {
		return nil, fmt.Errorf("%s is not a Bunny-managed service", serviceType)
	}
	label, ok := pkgdns.PublicSubdomain(serviceType)
	if !ok || label == "" {
		return nil, fmt.Errorf("service %s has no public subdomain", serviceType)
	}
	zoneDomain := label + "." + m.domain

	zone, _, err := m.ensureBunnyZoneDelegation(ctx, zoneDomain, logging.Fields{
		"service_type": serviceType,
		"scope":        "global_root",
	})
	if err != nil {
		return nil, err
	}

	clustersResp, err := m.qmClient.ListClusters(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("list clusters: %w", err)
	}
	platformClusters := map[string]struct{}{}
	for _, c := range clustersResp.GetClusters() {
		if !c.GetIsActive() {
			continue
		}
		if c.GetIsPlatformOfficial() {
			platformClusters[c.GetClusterId()] = struct{}{}
		}
	}

	nodesResp, err := m.qmClient.ListHealthyNodesForDNS(ctx, int(m.staleAge.Seconds()), serviceType)
	if err != nil {
		return nil, fmt.Errorf("list healthy nodes: %w", err)
	}
	var filtered []*proto.InfrastructureNode
	for _, n := range nodesResp.GetNodes() {
		if _, ok := platformClusters[n.GetClusterId()]; ok {
			filtered = append(filtered, n)
		}
	}
	nodes := dnsNodesFromProto(filtered)
	if len(nodes) == 0 {
		if authoritative {
			if err := m.bunnyClient.ReconcileRecordSet(ctx, zone.ID, "", bunny.RecordTypeA, nil); err != nil {
				return nil, fmt.Errorf("clear root record set: %w", err)
			}
			m.logger.WithFields(logging.Fields{
				"service_type": serviceType,
				"zone":         zoneDomain,
				"scope":        "global_root",
			}).Info("Authoritative empty set; cleared global root Bunny DNS")
			return nil, nil
		}
		m.logger.WithFields(logging.Fields{
			"service_type": serviceType,
			"zone":         zoneDomain,
			"scope":        "global_root",
		}).Warn("No platform-official healthy nodes for global root service; preserving existing Bunny DNS")
		return nil, nil
	}

	// Global root records live at the apex of {label}.{root}; record name
	// is empty (apex).
	records := m.bunnyRecordsForNodes(nodes, "", zoneDomain)
	if err := m.bunnyClient.ReconcileRecordSet(ctx, zone.ID, "", bunny.RecordTypeA, records); err != nil {
		return nil, fmt.Errorf("reconcile root record set: %w", err)
	}

	return nil, nil
}

func (m *DNSManager) syncBunnyClusterService(ctx context.Context, fqdn, serviceType, zoneDomain string, nodes []dnsNode) (map[string]string, error) {
	zone, _, err := m.ensureBunnyZoneDelegation(ctx, zoneDomain, logging.Fields{
		"service_type": serviceType,
		"fqdn":         fqdn,
	})
	if err != nil {
		return nil, err
	}

	recordNames, ok := bunnyRecordNames(serviceType)
	if !ok {
		return nil, fmt.Errorf("unknown Bunny service type: %s", serviceType)
	}

	for _, recordName := range recordNames {
		recordFQDN := bunnyRecordFQDN(recordName, zoneDomain)
		records := m.bunnyRecordsForNodes(nodes, recordName, recordFQDN)
		if reconcileErr := m.bunnyClient.ReconcileRecordSet(ctx, zone.ID, recordName, bunny.RecordTypeA, records); reconcileErr != nil {
			return nil, reconcileErr
		}
	}

	cleanupErrors := map[string]string{}
	if m.shouldRunCloudflareCleanup(fqdn) {
		var err error
		cleanupErrors, err = m.clearDNSConfig(ctx, fqdn)
		if err != nil {
			m.logger.WithError(err).WithField("fqdn", fqdn).Warn("Failed to clean old Cloudflare config after Bunny sync")
			return map[string]string{fqdn + ":cloudflare-cleanup": err.Error()}, nil
		}
		m.markCloudflareCleanup(fqdn)
	}
	if serviceType == "foghorn" {
		if m.shouldRunCloudflareCleanup(zoneDomain) {
			apexCleanup, cleanupErr := m.clearDNSConfig(ctx, zoneDomain)
			if cleanupErr != nil {
				m.logger.WithError(cleanupErr).WithField("fqdn", zoneDomain).Warn("Failed to clean old Cloudflare config after Bunny apex sync")
				cleanupErrors[zoneDomain+":cloudflare-cleanup"] = cleanupErr.Error()
				return cleanupErrors, nil
			}
			m.markCloudflareCleanup(zoneDomain)
			maps.Copy(cleanupErrors, apexCleanup)
		}
	}
	return cleanupErrors, nil
}

func (m *DNSManager) bunnyRecordsForNodes(nodes []dnsNode, recordName, fqdn string) []bunny.Record {
	records := make([]bunny.Record, 0, len(nodes))
	for _, node := range nodes {
		record := bunny.Record{
			Type:             bunny.RecordTypeA,
			Name:             recordName,
			Value:            node.ExternalIP,
			TTL:              m.recordTTL,
			Weight:           100,
			MonitorType:      bunny.MonitorTypeNone,
			SmartRoutingType: bunny.SmartRoutingNone,
			Comment:          fmt.Sprintf("Managed by Navigator for %s", fqdn),
		}
		if node.Latitude != nil && node.Longitude != nil {
			record.SmartRoutingType = bunny.SmartRoutingGeolocation
			record.GeolocationLatitude = node.Latitude
			record.GeolocationLongitude = node.Longitude
		}
		records = append(records, record)
	}
	return records
}

func (m *DNSManager) ensureBunnyZoneDelegation(ctx context.Context, zoneDomain string, logFields logging.Fields) (*bunny.Zone, bool, error) {
	cacheKey := bunnyZoneCacheKey(zoneDomain)
	now := time.Now()
	m.bunnyCacheMu.Lock()
	m.ensureProviderCacheMapsLocked()
	if zone := m.bunnyZoneCache[cacheKey]; zone != nil && now.Sub(m.bunnyDelegationCheckedAt[cacheKey]) < dnsProviderHousekeepingTTL {
		zoneCopy := *zone
		m.bunnyCacheMu.Unlock()
		return &zoneCopy, false, nil
	}
	m.bunnyCacheMu.Unlock()

	zone, created, err := m.bunnyClient.EnsureZone(ctx, zoneDomain)
	if err != nil {
		return nil, false, fmt.Errorf("failed to ensure Bunny zone %s: %w", zoneDomain, err)
	}
	if created {
		fields := logging.Fields{"zone": zoneDomain}
		maps.Copy(fields, logFields)
		m.logger.WithFields(fields).Info("Created Bunny DNS zone")
	}

	if err := m.ensureBunnyDelegation(zoneDomain, zone.Nameservers()); err != nil {
		return nil, created, fmt.Errorf("failed to ensure Bunny delegation for %s: %w", zoneDomain, err)
	}
	m.bunnyCacheMu.Lock()
	m.ensureProviderCacheMapsLocked()
	zoneCopy := *zone
	m.bunnyZoneCache[cacheKey] = &zoneCopy
	m.bunnyDelegationCheckedAt[cacheKey] = time.Now()
	m.bunnyCacheMu.Unlock()
	return zone, created, nil
}

func bunnyZoneCacheKey(zoneDomain string) string {
	return strings.TrimSuffix(strings.ToLower(strings.TrimSpace(zoneDomain)), ".")
}

func (m *DNSManager) shouldRunCloudflareCleanup(fqdn string) bool {
	key := strings.ToLower(strings.TrimSpace(fqdn))
	m.bunnyCacheMu.Lock()
	defer m.bunnyCacheMu.Unlock()
	m.ensureProviderCacheMapsLocked()
	last := m.cloudflareCleanupAt[key]
	return last.IsZero() || time.Since(last) >= dnsProviderHousekeepingTTL
}

func (m *DNSManager) markCloudflareCleanup(fqdn string) {
	key := strings.ToLower(strings.TrimSpace(fqdn))
	m.bunnyCacheMu.Lock()
	m.ensureProviderCacheMapsLocked()
	m.cloudflareCleanupAt[key] = time.Now()
	m.bunnyCacheMu.Unlock()
}

func (m *DNSManager) ensureProviderCacheMapsLocked() {
	if m.bunnyZoneCache == nil {
		m.bunnyZoneCache = map[string]*bunny.Zone{}
	}
	if m.bunnyDelegationCheckedAt == nil {
		m.bunnyDelegationCheckedAt = map[string]time.Time{}
	}
	if m.cloudflareCleanupAt == nil {
		m.cloudflareCleanupAt = map[string]time.Time{}
	}
}

func bunnyRecordName(serviceType string) (string, bool) {
	return pkgdns.PublicSubdomain(serviceType)
}

func bunnyRecordNames(serviceType string) ([]string, bool) {
	recordName, ok := bunnyRecordName(serviceType)
	if !ok {
		return nil, false
	}
	if serviceType == "foghorn" {
		return []string{recordName, ""}, true
	}
	return []string{recordName}, true
}

func bunnyRecordFQDN(recordName, zoneDomain string) string {
	if recordName == "" {
		return zoneDomain
	}
	return recordName + "." + zoneDomain
}

func (m *DNSManager) ensureBunnyDelegation(zoneDomain string, nameservers []string) error {
	if len(nameservers) == 0 {
		return fmt.Errorf("bunny zone has no nameservers")
	}

	desired := map[string]bool{}
	for _, ns := range nameservers {
		ns = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(ns)), ".")
		if ns != "" {
			desired[ns] = true
		}
	}
	if len(desired) == 0 {
		return fmt.Errorf("bunny zone has no valid nameservers")
	}

	records, err := m.cfClient.ListDNSRecords("NS", zoneDomain)
	if err != nil {
		return err
	}

	existing := map[string]cloudflare.DNSRecord{}
	for _, record := range records {
		content := strings.TrimSuffix(strings.ToLower(strings.TrimSpace(record.Content)), ".")
		if desired[content] {
			existing[content] = record
			continue
		}
		if err := m.cfClient.DeleteDNSRecord(record.ID); err != nil {
			return err
		}
	}

	for ns := range desired {
		if _, ok := existing[ns]; ok {
			continue
		}
		if _, err := m.cfClient.CreateDNSRecord(cloudflare.DNSRecord{
			Type:    "NS",
			Name:    zoneDomain,
			Content: ns,
			TTL:     300,
			Proxied: false,
		}); err != nil {
			return err
		}
	}
	return nil
}

func isEdgeNodeRecord(recordName, prefix, suffix string) bool {
	if !strings.HasPrefix(recordName, prefix) || !strings.HasSuffix(recordName, suffix) {
		return false
	}

	label := strings.TrimSuffix(recordName, suffix)
	if strings.Contains(label, ".") {
		return false
	}

	if label == "edge-egress" {
		return false
	}

	return true
}

func isEdgeNodeRecordLabel(recordName string) bool {
	label := SanitizeLabel(recordName)
	return strings.HasPrefix(label, "edge-") && label != "edge-egress" && !strings.Contains(label, ".")
}

func (m *DNSManager) clusterServiceFQDN(serviceType, rootDomain string) string {
	if fqdn, ok := pkgdns.ServiceFQDN(serviceType, rootDomain); ok {
		return fqdn
	}
	return fmt.Sprintf("%s.%s", serviceType, rootDomain)
}

// syncClusterService applies DNS for a cluster-scoped service using pre-fetched IPs.
func (m *DNSManager) syncClusterService(ctx context.Context, fqdn, serviceType string, nodes []dnsNode) (map[string]string, error) {
	if len(nodes) == 1 {
		if err := m.applySingleNodeConfig(ctx, fqdn, nodes[0].ExternalIP, m.shouldProxy(serviceType)); err != nil {
			return nil, err
		}
		return m.cleanupManagedPools(fqdn, "", nil), nil
	}
	poolName := sanitizePoolName(fqdn)
	pool := desiredPool{
		Name:        poolName,
		ServiceType: serviceType,
		Nodes:       nodes,
	}
	pool.Latitude, pool.Longitude = centroid(nodes)
	return m.applyLoadBalancerPools(ctx, fqdn, serviceType, []desiredPool{pool}, m.shouldProxy(serviceType), "")
}

// SyncService synchronizes DNS records for a specific service type (e.g. "edge", "bridge")
// It implements the "Smart Record" logic:
// - 1 healthy node -> A record (Direct IP)
// - >1 healthy nodes -> Cloudflare load balancer with managed pools
func (m *DNSManager) SyncService(ctx context.Context, serviceType, rootDomain string) (map[string]string, error) {
	log := m.logger.WithField("service", serviceType)
	log.Info("Starting DNS sync")

	// 1. Fetch Inventory from Quartermaster via gRPC
	nodesResp, err := m.qmClient.ListHealthyNodesForDNS(ctx, int(m.staleAge.Seconds()), serviceType)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch nodes from Quartermaster: %w", err)
	}

	// 2. Filter for Nodes with External IPs
	activeNodes := dnsNodesFromProto(nodesResp.Nodes)
	log.WithField("count", len(activeNodes)).Info("Found active nodes")

	domain := m.domain
	if rootDomain != "" {
		domain = rootDomain
	}
	fqdn, ok := pkgdns.RootServiceFQDN(serviceType, domain)
	if !ok {
		return nil, fmt.Errorf("unknown service type for DNS sync: %s", serviceType)
	}

	// 4. Apply "Smart Record" Logic
	if len(activeNodes) == 0 {
		// Fail-open: preserve existing DNS records during empty inventory windows
		// (transient QM failures, first-deploy race). Records will be updated
		// when positive inventory data arrives.
		log.Warn("No active nodes found, preserving existing DNS records")
		return nil, nil
	}

	if len(activeNodes) == 1 {
		// === Single Node: Direct A Record ===
		log.Info("Single node detected, using A record")
		if err := m.applySingleNodeConfig(ctx, fqdn, activeNodes[0].ExternalIP, m.shouldProxy(serviceType)); err != nil {
			return nil, err
		}
		return m.cleanupManagedPools(fqdn, serviceType, nil), nil
	}

	// === Multi Node: Load Balancer Pool ===
	log.Info("Multiple nodes detected, using Load Balancer")
	return m.applyRootLoadBalancerConfig(ctx, fqdn, serviceType, domain, activeNodes, m.shouldProxy(serviceType))
}

// applySingleNodeConfig ensures an A record exists and cleans up any LB config
func (m *DNSManager) applySingleNodeConfig(ctx context.Context, fqdn, ip string, proxied bool) error {
	// Cleanup LB/Pools if they exist to avoid conflicts
	// Check if LB exists for this hostname
	lbs, err := m.cfClient.ListLoadBalancers()
	if err != nil {
		m.logger.WithError(err).Warn("Failed to list LBs during cleanup check")
	} else {
		for _, lb := range lbs {
			// Cloudflare LBs are matched by hostname (subdomain.domain.com)
			if lb.Name == fqdn {
				m.logger.WithField("lb_id", lb.ID).Info("Deleting conflicting Load Balancer for Single Node mode")
				if deleteErr := m.cfClient.DeleteLoadBalancer(lb.ID); deleteErr != nil {
					m.logger.WithError(deleteErr).Error("Failed to delete conflicting LB")
				}
			}
		}
	}

	// 2. Create/Update A record
	// Note: The cloudflare provider needs to support "Upsert" logic or we implement it here
	// Check if record exists
	records, err := m.cfClient.ListDNSRecords("A", fqdn)
	if err != nil {
		return fmt.Errorf("failed to list existing A records: %w", err)
	}

	if len(records) > 0 {
		// Update existing
		record := records[0]
		if record.Content != ip || record.Proxied != proxied || record.TTL != m.recordTTL {
			m.logger.WithFields(logging.Fields{"fqdn": fqdn, "old_ip": record.Content, "new_ip": ip}).Info("Updating A record")
			record.Content = ip
			record.Proxied = proxied
			record.TTL = m.recordTTL
			if _, err := m.cfClient.UpdateDNSRecord(record.ID, record); err != nil {
				return fmt.Errorf("failed to update A record: %w", err)
			}
		}
		for _, extra := range records[1:] {
			m.logger.WithField("record_id", extra.ID).Info("Deleting extra A record for Single Node mode")
			if err := m.cfClient.DeleteDNSRecord(extra.ID); err != nil {
				m.logger.WithError(err).WithField("record_id", extra.ID).Warn("Failed to delete extra A record")
			}
		}
	} else {
		// Create new
		m.logger.WithFields(logging.Fields{"fqdn": fqdn, "ip": ip}).Info("Creating A record")
		if _, err := m.cfClient.CreateARecord(fqdn, ip, proxied, m.recordTTL); err != nil {
			return fmt.Errorf("failed to create A record: %w", err)
		}
	}

	return nil
}

func dnsNodesFromProto(nodes []*proto.InfrastructureNode) []dnsNode {
	out := make([]dnsNode, 0, len(nodes))
	seen := map[string]struct{}{}
	for _, node := range nodes {
		if node == nil || node.ExternalIp == nil || strings.TrimSpace(*node.ExternalIp) == "" {
			continue
		}
		ip := strings.TrimSpace(*node.ExternalIp)
		if _, ok := seen[ip]; ok {
			continue
		}
		seen[ip] = struct{}{}
		out = append(out, dnsNode{
			NodeID:     node.GetNodeId(),
			ClusterID:  node.GetClusterId(),
			ExternalIP: ip,
			Region:     node.GetRegion(),
			Latitude:   node.Latitude,
			Longitude:  node.Longitude,
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].ClusterID != out[j].ClusterID {
			return out[i].ClusterID < out[j].ClusterID
		}
		if out[i].NodeID != out[j].NodeID {
			return out[i].NodeID < out[j].NodeID
		}
		return out[i].ExternalIP < out[j].ExternalIP
	})
	return out
}

func dnsNodesFromIPs(ips []string) []dnsNode {
	out := make([]dnsNode, 0, len(ips))
	seen := map[string]struct{}{}
	for _, raw := range ips {
		ip := strings.TrimSpace(raw)
		if ip == "" {
			continue
		}
		if _, ok := seen[ip]; ok {
			continue
		}
		seen[ip] = struct{}{}
		out = append(out, dnsNode{ExternalIP: ip})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ExternalIP < out[j].ExternalIP })
	return out
}

func centroid(nodes []dnsNode) (*float64, *float64) {
	var latSum, lonSum float64
	count := 0
	for _, node := range nodes {
		if node.Latitude == nil || node.Longitude == nil {
			continue
		}
		latSum += *node.Latitude
		lonSum += *node.Longitude
		count++
	}
	if count == 0 {
		return nil, nil
	}
	lat := latSum / float64(count)
	lon := lonSum / float64(count)
	return &lat, &lon
}

func sanitizePoolName(name string) string {
	return strings.ReplaceAll(name, ".", "-")
}

func originName(node dnsNode) string {
	if node.NodeID != "" {
		return SanitizeLabel(node.NodeID)
	}
	return strings.NewReplacer(".", "-", ":", "-").Replace(node.ExternalIP)
}

func originsForNodes(nodes []dnsNode) []cloudflare.Origin {
	origins := make([]cloudflare.Origin, 0, len(nodes))
	for _, node := range nodes {
		origins = append(origins, cloudflare.Origin{
			Name:    originName(node),
			Address: node.ExternalIP,
			Enabled: true,
			Weight:  1.0,
		})
	}
	return origins
}

func sameStringSet(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func (m *DNSManager) applyRootLoadBalancerConfig(ctx context.Context, fqdn, serviceType, rootDomain string, nodes []dnsNode, proxied bool) (map[string]string, error) {
	pools := sharedRootIngressPools(rootDomain, nodes)
	return m.applyLoadBalancerPools(ctx, fqdn, serviceType, pools, proxied, serviceType)
}

func sharedRootIngressPools(rootDomain string, nodes []dnsNode) []desiredPool {
	nodesByIP := map[string]dnsNode{}
	for _, node := range nodes {
		ip := strings.TrimSpace(node.ExternalIP)
		if ip == "" {
			continue
		}
		existing, ok := nodesByIP[ip]
		if !ok {
			nodesByIP[ip] = node
			continue
		}
		if existing.Latitude == nil && node.Latitude != nil {
			existing.Latitude = node.Latitude
		}
		if existing.Longitude == nil && node.Longitude != nil {
			existing.Longitude = node.Longitude
		}
		nodesByIP[ip] = existing
	}

	ips := make([]string, 0, len(nodesByIP))
	for ip := range nodesByIP {
		ips = append(ips, ip)
	}
	sort.Strings(ips)

	pools := make([]desiredPool, 0, len(ips))
	for _, ip := range ips {
		node := nodesByIP[ip]
		pool := desiredPool{
			Name:        sharedRootIngressPoolName(rootDomain, ip),
			ServiceType: rootIngressPoolServiceType,
			Nodes:       []dnsNode{node},
			Latitude:    node.Latitude,
			Longitude:   node.Longitude,
		}
		pools = append(pools, pool)
	}
	return pools
}

func sharedRootIngressPoolName(rootDomain, ip string) string {
	originLabel := strings.NewReplacer(".", "-", ":", "-").Replace(strings.TrimSpace(ip))
	return sanitizePoolName(fmt.Sprintf("%s-ingress-%s", rootDomain, originLabel))
}

// applyLoadBalancerConfig adapts an explicit IP slice into a single managed
// Cloudflare pool for callers that already resolved service inventory.
func (m *DNSManager) applyLoadBalancerConfig(ctx context.Context, fqdn, poolName, serviceType string, ips []string, proxied bool) (map[string]string, error) {
	nodes := dnsNodesFromIPs(ips)
	pool := desiredPool{
		Name:        poolName,
		ServiceType: serviceType,
		Nodes:       nodes,
	}
	pool.Latitude, pool.Longitude = centroid(nodes)
	return m.applyLoadBalancerPools(ctx, fqdn, serviceType, []desiredPool{pool}, proxied, poolName)
}

// applyLoadBalancerPools ensures Cloudflare pools exist and points the LB at them.
func (m *DNSManager) applyLoadBalancerPools(ctx context.Context, fqdn, serviceType string, desiredPools []desiredPool, proxied bool, legacyPoolName string) (map[string]string, error) {
	partialErrors := make(map[string]string)

	desiredPoolIDs := make([]string, 0, len(desiredPools))
	desiredPoolNames := map[string]bool{}
	for _, pool := range desiredPools {
		desiredPoolNames[pool.Name] = true
	}

	preCreateCleanupErrors := m.cleanupManagedPools(fqdn, legacyPoolName, desiredPoolNames)
	maps.Copy(partialErrors, preCreateCleanupErrors)

	for _, pool := range desiredPools {
		poolID, err := m.ensureDesiredPool(pool)
		if err != nil {
			return nil, err
		}
		desiredPoolIDs = append(desiredPoolIDs, poolID)
	}
	sort.Strings(desiredPoolIDs)

	// Ensure LB Object exists for this hostname.
	// Check if LB exists for this hostname
	lbs, err := m.cfClient.ListLoadBalancers()
	if err != nil {
		return nil, fmt.Errorf("failed to list LBs: %w", err)
	}

	var lbID string
	var previousPoolIDs []string
	for _, lb := range lbs {
		// CreateLoadBalancer stores the FQDN in Name, so compare against that field here.
		if lb.Name == fqdn {
			lbID = lb.ID
			previousPoolIDs = poolIDsFromLoadBalancer(lb)
			break
		}
	}

	if lbID == "" {
		// Create LB
		m.logger.WithField("fqdn", fqdn).Info("Creating Load Balancer")
		fallbackPool := ""
		if len(desiredPoolIDs) > 0 {
			fallbackPool = desiredPoolIDs[0]
		}
		lb := cloudflare.LoadBalancer{
			Name:           fqdn, // This acts as the hostname
			Description:    fmt.Sprintf("Auto-managed by Navigator for %s", serviceType),
			TTL:            m.lbTTL,
			FallbackPool:   fallbackPool,
			DefaultPools:   desiredPoolIDs,
			Proxied:        proxied,
			Enabled:        true,
			SteeringPolicy: steeringPolicyForPools(desiredPools),
		}
		_, err = m.cfClient.CreateLoadBalancer(lb)
		if err != nil {
			return nil, fmt.Errorf("failed to create LB: %w", err)
		}
	} else {
		currentLB, getLBErr := m.cfClient.GetLoadBalancer(lbID)
		if getLBErr != nil {
			return nil, fmt.Errorf("failed to get LB details: %w", getLBErr)
		}
		previousPoolIDs = poolIDsFromLoadBalancer(*currentLB)

		fallbackPool := ""
		if len(desiredPoolIDs) > 0 {
			fallbackPool = desiredPoolIDs[0]
		}
		steeringPolicy := steeringPolicyForPools(desiredPools)
		needsUpdate := currentLB.FallbackPool != fallbackPool ||
			!sameStringSet(currentLB.DefaultPools, desiredPoolIDs) ||
			currentLB.SteeringPolicy != steeringPolicy ||
			len(currentLB.RegionPools) > 0 ||
			len(currentLB.CountryPools) > 0 ||
			len(currentLB.PopPools) > 0
		if needsUpdate || currentLB.TTL != m.lbTTL || currentLB.Proxied != proxied {
			currentLB.FallbackPool = fallbackPool
			currentLB.DefaultPools = desiredPoolIDs
			currentLB.RegionPools = nil
			currentLB.CountryPools = nil
			currentLB.PopPools = nil
			currentLB.TTL = m.lbTTL
			currentLB.Proxied = proxied
			currentLB.SteeringPolicy = steeringPolicy
			if _, updateLBErr := m.cfClient.UpdateLoadBalancer(lbID, *currentLB); updateLBErr != nil {
				return nil, fmt.Errorf("failed to update LB: %w", updateLBErr)
			}
		}
	}

	// Also ensure A records are gone (cleanup Single Node config)
	aRecords, listAErr := m.cfClient.ListDNSRecords("A", fqdn)
	if listAErr == nil {
		for _, rec := range aRecords {
			m.logger.WithField("record_id", rec.ID).Info("Deleting conflicting A record for LB mode")
			if delAErr := m.cfClient.DeleteDNSRecord(rec.ID); delAErr != nil {
				m.logger.WithError(delAErr).WithField("record_id", rec.ID).Warn("Failed to delete conflicting A record")
				partialErrors[fmt.Sprintf("%s:%s", fqdn, rec.ID)] = delAErr.Error()
			}
		}
	}

	// Also clean up any conflicting CNAME records
	cnameRecords, listCNAMEErr := m.cfClient.ListDNSRecords("CNAME", fqdn)
	if listCNAMEErr == nil {
		for _, rec := range cnameRecords {
			m.logger.WithField("record_id", rec.ID).Info("Deleting conflicting CNAME record for LB mode")
			if delCNAMEErr := m.cfClient.DeleteDNSRecord(rec.ID); delCNAMEErr != nil {
				m.logger.WithError(delCNAMEErr).WithField("record_id", rec.ID).Warn("Failed to delete conflicting CNAME record")
				partialErrors[fmt.Sprintf("%s:cname:%s", fqdn, rec.ID)] = delCNAMEErr.Error()
			}
		}
	}

	stalePoolErrors := m.cleanupManagedPools(fqdn, legacyPoolName, desiredPoolNames)
	maps.Copy(partialErrors, stalePoolErrors)
	stalePoolIDErrors := m.cleanupPoolIDs(fqdn, previousPoolIDs, desiredPoolIDs)
	maps.Copy(partialErrors, stalePoolIDErrors)

	if len(partialErrors) == 0 {
		return nil, nil
	}
	return partialErrors, nil
}

func poolIDsFromLoadBalancer(lb cloudflare.LoadBalancer) []string {
	seen := map[string]struct{}{}
	add := func(poolID string) {
		poolID = strings.TrimSpace(poolID)
		if poolID != "" {
			seen[poolID] = struct{}{}
		}
	}
	add(lb.FallbackPool)
	for _, poolID := range lb.DefaultPools {
		add(poolID)
	}
	for _, poolIDs := range lb.RegionPools {
		for _, poolID := range poolIDs {
			add(poolID)
		}
	}
	for _, poolIDs := range lb.CountryPools {
		for _, poolID := range poolIDs {
			add(poolID)
		}
	}
	for _, poolIDs := range lb.PopPools {
		for _, poolID := range poolIDs {
			add(poolID)
		}
	}

	out := make([]string, 0, len(seen))
	for poolID := range seen {
		out = append(out, poolID)
	}
	sort.Strings(out)
	return out
}

// ClearServiceDNS is the explicit decommission path for removing all DNS
// configuration for a service FQDN. Call this when a service is intentionally
// drained; the periodic sync paths preserve existing records on empty
// inventory to avoid accidental deletions during transient outages.
func (m *DNSManager) ClearServiceDNS(ctx context.Context, fqdn string) (map[string]string, error) {
	m.logger.WithField("fqdn", fqdn).Info("Explicit DNS teardown requested")
	return m.clearDNSConfig(ctx, fqdn)
}

// clearDNSConfig removes all DNS configuration for a given FQDN (LB, A, CNAME records)
func (m *DNSManager) clearDNSConfig(_ context.Context, fqdn string) (map[string]string, error) {
	if m.cfClient == nil {
		return nil, fmt.Errorf("cloudflare client is required for DNS cleanup")
	}

	lbs, err := m.cfClient.ListLoadBalancers()
	if err != nil {
		return nil, fmt.Errorf("failed to list LBs: %w", err)
	}

	for _, lb := range lbs {
		if lb.Name == fqdn {
			m.logger.WithField("lb_id", lb.ID).Info("Deleting Load Balancer for empty node set")
			if err := m.cfClient.DeleteLoadBalancer(lb.ID); err != nil {
				return nil, fmt.Errorf("failed to delete LB: %w", err)
			}
		}
	}

	for _, recordType := range []string{"A", "CNAME"} {
		records, err := m.cfClient.ListDNSRecords(recordType, fqdn)
		if err != nil {
			return nil, fmt.Errorf("failed to list %s records: %w", recordType, err)
		}
		for _, rec := range records {
			m.logger.WithField("record_id", rec.ID).Info("Deleting DNS record for empty node set")
			if err := m.cfClient.DeleteDNSRecord(rec.ID); err != nil {
				return nil, fmt.Errorf("failed to delete DNS record: %w", err)
			}
		}
	}

	partialErrors := m.cleanupManagedPools(fqdn, "", nil)
	if len(partialErrors) == 0 {
		return nil, nil
	}
	return partialErrors, nil
}

// ensureMonitor finds or creates a health check monitor for a service type
func (m *DNSManager) ensureMonitor(serviceType string) (string, error) {
	monitors, err := m.cfClient.ListMonitors()
	if err != nil {
		return "", fmt.Errorf("failed to list monitors: %w", err)
	}

	monitorName := fmt.Sprintf("nav-%s-health", serviceType)
	for _, mon := range monitors {
		if mon.Description == monitorName {
			return mon.ID, nil
		}
	}

	// Determine port for this service type
	port := m.servicePorts[serviceType]
	if port == 0 {
		port = 80 // Default fallback
	}

	// Create new monitor
	path := m.healthPaths[serviceType]
	if path == "" {
		path = "/health"
	}
	monitorType := "http"
	expectedCodes := "200"
	followRedirects := false
	if serviceType == rootIngressPoolServiceType {
		path = "/"
		port = 80
		expectedCodes = "2xx"
		followRedirects = true
	}
	m.logger.WithFields(logging.Fields{"name": monitorName, "port": port}).Info("Creating health check monitor")
	monitor := cloudflare.Monitor{
		Type:            monitorType,
		Description:     monitorName,
		Method:          "GET",
		Path:            path,
		Port:            port,
		Timeout:         m.monitorConfig.Timeout,
		Interval:        m.monitorConfig.Interval,
		Retries:         m.monitorConfig.Retries,
		ExpectedCodes:   expectedCodes,
		FollowRedirects: followRedirects,
	}
	created, err := m.cfClient.CreateMonitor(monitor)
	if err != nil {
		return "", fmt.Errorf("failed to create monitor: %w", err)
	}
	return created.ID, nil
}

func steeringPolicyForPools(pools []desiredPool) string {
	withLocation := 0
	for _, pool := range pools {
		if pool.Latitude != nil && pool.Longitude != nil {
			withLocation++
		}
	}
	if withLocation >= 2 {
		return "proximity"
	}
	return "off"
}

func (m *DNSManager) cleanupManagedPools(fqdn, legacyPoolName string, desiredNames map[string]bool) map[string]string {
	partialErrors := map[string]string{}
	pools, err := m.cfClient.ListPools()
	if err != nil {
		partialErrors[fmt.Sprintf("%s:pools", fqdn)] = err.Error()
		return partialErrors
	}

	prefix := sanitizePoolName(fqdn) + "-"
	fqdnPoolName := sanitizePoolName(fqdn)
	for _, pool := range pools {
		if desiredNames != nil && desiredNames[pool.Name] {
			continue
		}
		managed := strings.HasPrefix(pool.Name, prefix) || pool.Name == fqdnPoolName
		if legacyPoolName != "" && pool.Name == legacyPoolName {
			managed = true
		}
		if !managed {
			continue
		}
		if err := m.cfClient.DeletePool(pool.ID); err != nil {
			partialErrors[fmt.Sprintf("%s:pool:%s", fqdn, pool.Name)] = err.Error()
		}
	}
	if len(partialErrors) == 0 {
		return nil
	}
	return partialErrors
}

func (m *DNSManager) cleanupPoolIDs(fqdn string, previousPoolIDs, desiredPoolIDs []string) map[string]string {
	desired := map[string]bool{}
	for _, poolID := range desiredPoolIDs {
		desired[poolID] = true
	}

	partialErrors := map[string]string{}
	for _, poolID := range previousPoolIDs {
		if desired[poolID] {
			continue
		}
		if err := m.cfClient.DeletePool(poolID); err != nil {
			partialErrors[fmt.Sprintf("%s:pool:%s", fqdn, poolID)] = err.Error()
		}
	}
	if len(partialErrors) == 0 {
		return nil
	}
	return partialErrors
}

// ensurePool finds a pool by name or creates it, attaching a health monitor.
func (m *DNSManager) ensurePool(name, serviceType string, ips []string) (string, error) {
	nodes := dnsNodesFromIPs(ips)
	pool := desiredPool{
		Name:        name,
		ServiceType: serviceType,
		Nodes:       nodes,
	}
	pool.Latitude, pool.Longitude = centroid(nodes)
	return m.ensureDesiredPool(pool)
}

func (m *DNSManager) ensureDesiredPool(desired desiredPool) (string, error) {
	// First, ensure we have a monitor for this service type
	monitorID, err := m.ensureMonitor(desired.ServiceType)
	if err != nil {
		m.logger.WithError(err).Warn("Failed to ensure monitor, pool will have no health checks")
		monitorID = "" // Continue without monitor
	}

	pools, err := m.cfClient.ListPools()
	if err != nil {
		return "", fmt.Errorf("failed to list pools: %w", err)
	}

	for _, p := range pools {
		if p.Name == desired.Name {
			p.Description = "Managed by Navigator"
			p.Enabled = true
			p.MinimumOrigins = 1
			p.Monitor = monitorID
			p.Origins = originsForNodes(desired.Nodes)
			p.Latitude = desired.Latitude
			p.Longitude = desired.Longitude
			p.OriginSteering = &cloudflare.OriginSteering{Policy: "random"}
			if _, updateErr := m.cfClient.UpdatePool(p.ID, p); updateErr != nil {
				return "", fmt.Errorf("failed to update pool: %w", updateErr)
			}
			return p.ID, nil
		}
	}

	// Not found, create
	m.logger.WithField("name", desired.Name).Info("Creating new Load Balancer Pool")

	newPool := cloudflare.Pool{
		Name:           desired.Name,
		Description:    "Managed by Navigator",
		Enabled:        true,
		MinimumOrigins: 1,
		Origins:        originsForNodes(desired.Nodes),
		Monitor:        monitorID,
		Latitude:       desired.Latitude,
		Longitude:      desired.Longitude,
		OriginSteering: &cloudflare.OriginSteering{Policy: "random"},
	}
	created, err := m.cfClient.CreatePool(newPool)
	if err != nil {
		return "", fmt.Errorf("failed to create pool: %w", err)
	}
	return created.ID, nil
}
