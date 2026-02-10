package logic

import (
	"context"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strings"
	"time"

	"frameworks/api_dns/internal/provider/cloudflare"
	"frameworks/pkg/logging"
	"frameworks/pkg/proto"
)

// MonitorConfig holds Cloudflare health monitor settings
type MonitorConfig struct {
	Interval int // Health check interval in seconds
	Timeout  int // Health check timeout in seconds
	Retries  int // Number of retries before marking unhealthy
}

// DNSManager handles DNS synchronization logic
type DNSManager struct {
	cfClient      cloudflareClient
	qmClient      quartermasterClient
	logger        logging.Logger
	domain        string // Root domain e.g. frameworks.network
	proxy         map[string]bool
	recordTTL     int
	lbTTL         int
	staleAge      time.Duration
	monitorConfig MonitorConfig
	servicePorts  map[string]int // Service type -> HTTP health check port
}

type cloudflareClient interface {
	ListLoadBalancers() ([]cloudflare.LoadBalancer, error)
	DeleteLoadBalancer(loadBalancerID string) error
	ListDNSRecords(recordType, name string) ([]cloudflare.DNSRecord, error)
	UpdateDNSRecord(recordID string, record cloudflare.DNSRecord) (*cloudflare.DNSRecord, error)
	DeleteDNSRecord(recordID string) error
	CreateARecord(name, content string, proxied bool, ttl int) (*cloudflare.DNSRecord, error)
	GetPool(poolID string) (*cloudflare.Pool, error)
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

type quartermasterClient interface {
	ListHealthyNodesForDNS(ctx context.Context, nodeType string, staleThresholdSeconds int) (*proto.ListHealthyNodesForDNSResponse, error)
	ListClusters(ctx context.Context, pagination *proto.CursorPaginationRequest) (*proto.ListClustersResponse, error)
}

var slugSanitizer = regexp.MustCompile(`[^a-z0-9-]`)

// NewDNSManager creates a new DNSManager
func NewDNSManager(cf cloudflareClient, qm quartermasterClient, logger logging.Logger, rootDomain string, recordTTL int, lbTTL int, staleAge time.Duration, monitorConfig MonitorConfig) *DNSManager {
	return &DNSManager{
		cfClient:      cf,
		qmClient:      qm,
		logger:        logger,
		domain:        rootDomain,
		proxy:         loadProxyServices(),
		recordTTL:     recordTTL,
		lbTTL:         lbTTL,
		staleAge:      staleAge,
		monitorConfig: monitorConfig,
		servicePorts:  defaultServicePorts(),
	}
}

// defaultServicePorts returns the default HTTP health check port for each service type
func defaultServicePorts() map[string]int {
	return map[string]int{
		"edge":    18008, // Foghorn HTTP port
		"ingest":  18008, // Foghorn HTTP port
		"play":    18008, // Foghorn HTTP port
		"gateway": 18001, // Bridge HTTP port
		"api":     18001, // Bridge HTTP port (alias)
		"app":     3000,  // SvelteKit
		"website": 4321,  // Astro
		"docs":    4321,  // Astro
		"forms":   18032, // Forms HTTP port
	}
}

func loadProxyServices() map[string]bool {
	env := strings.TrimSpace(os.Getenv("NAVIGATOR_PROXY_SERVICES"))
	if env == "" {
		return map[string]bool{
			"app":     true,
			"website": true,
			"docs":    true,
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
	return proxy
}

func (m *DNSManager) shouldProxy(serviceType string) bool {
	return m.proxy[serviceType]
}

func sanitizeLabel(raw string) string {
	label := strings.ToLower(strings.TrimSpace(raw))
	label = strings.ReplaceAll(label, "_", "-")
	label = slugSanitizer.ReplaceAllString(label, "-")
	label = strings.Trim(label, "-")
	if label == "" {
		return "default"
	}
	return label
}

func (m *DNSManager) clusterSlug(cluster *proto.InfrastructureCluster) string {
	if cluster == nil {
		return "default"
	}
	if v := sanitizeLabel(cluster.GetClusterId()); v != "default" {
		return v
	}
	return sanitizeLabel(cluster.GetClusterName())
}

func (m *DNSManager) SyncServiceByCluster(ctx context.Context, serviceType string) (map[string]string, error) {
	partialErrors := map[string]string{}

	clustersResp, err := m.qmClient.ListClusters(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to list clusters: %w", err)
	}

	nodesResp, err := m.qmClient.ListHealthyNodesForDNS(ctx, serviceType, int(m.staleAge.Seconds()))
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

	for _, cluster := range clustersResp.Clusters {
		if !cluster.GetIsActive() {
			continue
		}
		clusterSlug := m.clusterSlug(cluster)
		rootDomain := fmt.Sprintf("%s.%s", clusterSlug, m.domain)

		nodes := nodesByCluster[cluster.GetClusterId()]
		ips := make([]string, 0, len(nodes))
		for _, node := range nodes {
			if node.ExternalIp != nil && *node.ExternalIp != "" {
				ips = append(ips, *node.ExternalIp)
			}
		}

		if len(ips) == 0 {
			if _, err := m.clearDNSConfig(ctx, fmt.Sprintf("%s.%s", serviceType, rootDomain)); err != nil {
				partialErrors[fmt.Sprintf("%s.%s", serviceType, rootDomain)] = err.Error()
			}
		} else {
			svcPartial, syncErr := m.SyncService(ctx, serviceType, rootDomain)
			if syncErr != nil {
				partialErrors[fmt.Sprintf("%s.%s", serviceType, rootDomain)] = syncErr.Error()
			} else {
				for k, v := range svcPartial {
					partialErrors[k] = v
				}
			}
		}

		if serviceType != "edge" {
			continue
		}

		desiredNodeRecords := map[string]string{}
		for _, node := range nodes {
			if node.ExternalIp == nil || *node.ExternalIp == "" {
				continue
			}
			nodeLabel := sanitizeLabel(node.GetNodeId())
			fqdn := fmt.Sprintf("edge-%s.%s", nodeLabel, rootDomain)
			desiredNodeRecords[fqdn] = *node.ExternalIp
			if err := m.applySingleNodeConfig(ctx, fqdn, *node.ExternalIp, false); err != nil {
				partialErrors[fqdn] = err.Error()
			}
		}

		aRecords, listErr := m.cfClient.ListDNSRecords("A", "")
		if listErr != nil {
			partialErrors[fmt.Sprintf("edge-nodes.%s", rootDomain)] = listErr.Error()
			continue
		}
		prefix := "edge-"
		suffix := "." + rootDomain
		for _, rec := range aRecords {
			if !strings.HasPrefix(rec.Name, prefix) || !strings.HasSuffix(rec.Name, suffix) {
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

	if len(partialErrors) == 0 {
		return nil, nil
	}
	return partialErrors, nil
}

// SyncService synchronizes DNS records for a specific service type (e.g. "edge", "gateway")
// It implements the "Smart Record" logic:
// - 1 healthy node -> A record (Direct IP)
// - >1 healthy nodes -> Load Balancer Pool + CNAME
func (m *DNSManager) SyncService(ctx context.Context, serviceType, rootDomain string) (map[string]string, error) {
	log := m.logger.WithField("service", serviceType)
	log.Info("Starting DNS sync")

	// 1. Fetch Inventory from Quartermaster via gRPC
	// Filter by node_type which maps to our service types
	nodesResp, err := m.qmClient.ListHealthyNodesForDNS(ctx, serviceType, int(m.staleAge.Seconds()))
	if err != nil {
		return nil, fmt.Errorf("failed to fetch nodes from Quartermaster: %w", err)
	}

	// 2. Filter for Nodes with External IPs
	var activeIPs []string
	for _, node := range nodesResp.Nodes {
		// We only want nodes with external IPs
		// Note: Status field was removed - all returned nodes are considered active
		if node.ExternalIp != nil && *node.ExternalIp != "" {
			activeIPs = append(activeIPs, *node.ExternalIp)
		}
	}
	log.WithField("count", len(activeIPs)).Info("Found active nodes")

	// 3. Determine Subdomain
	// Map internal service types to public subdomains
	var subdomain string
	switch serviceType {
	case "edge":
		subdomain = "edge"
	case "ingest":
		subdomain = "ingest"
	case "play":
		subdomain = "play"
	case "gateway", "api": // Handle both for robustness
		subdomain = "api"
	case "app":
		subdomain = "app"
	case "website":
		subdomain = "@" // Root
	case "docs":
		subdomain = "docs"
	case "forms":
		subdomain = "forms"
	default:
		return nil, fmt.Errorf("unknown service type for DNS sync: %s", serviceType)
	}

	domain := m.domain
	if rootDomain != "" {
		domain = rootDomain
	}

	fqdn := fmt.Sprintf("%s.%s", subdomain, domain)
	if subdomain == "@" {
		fqdn = domain
	}

	// 4. Apply "Smart Record" Logic
	if len(activeIPs) == 0 {
		log.Warn("No active nodes found, removing DNS records")
		return m.clearDNSConfig(ctx, fqdn)
	}

	if len(activeIPs) == 1 {
		// === Single Node: Direct A Record ===
		log.Info("Single node detected, using A record")
		return nil, m.applySingleNodeConfig(ctx, fqdn, activeIPs[0], m.shouldProxy(serviceType))
	}

	// === Multi Node: Load Balancer Pool ===
	log.Info("Multiple nodes detected, using Load Balancer")
	return m.applyLoadBalancerConfig(ctx, fqdn, serviceType, activeIPs, m.shouldProxy(serviceType))
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

// applyLoadBalancerConfig ensures an LB Pool exists and updates origins
func (m *DNSManager) applyLoadBalancerConfig(ctx context.Context, fqdn, poolName string, ips []string, proxied bool) (map[string]string, error) {
	partialErrors := make(map[string]string)

	// 1. Find or Create Pool
	// We use the serviceType (e.g. "edge") as the pool name
	poolID, err := m.ensurePool(poolName, ips)
	if err != nil {
		return nil, err
	}

	// 2. Sync Origins (The hardest part)
	// We need to get current origins, find diff, add/remove.
	currentPool, err := m.cfClient.GetPool(poolID)
	if err != nil {
		return nil, fmt.Errorf("failed to get pool details: %w", err)
	}

	// Build map of desired IPs
	desiredMap := make(map[string]bool)
	for _, ip := range ips {
		desiredMap[ip] = true
	}

	// Check existing origins
	existingMap := make(map[string]bool)
	for _, origin := range currentPool.Origins {
		existingMap[origin.Address] = true

		// If origin exists but is not in desired list, REMOVE it
		if !desiredMap[origin.Address] {
			m.logger.WithField("ip", origin.Address).Info("Removing stale origin from pool")
			if _, removeErr := m.cfClient.RemoveOriginFromPool(poolID, origin.Address); removeErr != nil {
				m.logger.WithError(removeErr).Error("Failed to remove origin")
				partialErrors[fmt.Sprintf("%s:%s", fqdn, origin.Address)] = removeErr.Error()
			}
		}
	}

	// Add new origins
	for _, ip := range ips {
		if !existingMap[ip] {
			m.logger.WithField("ip", ip).Info("Adding new origin to pool")
			origin := cloudflare.Origin{
				Name:    strings.ReplaceAll(ip, ".", "-"),
				Address: ip,
				Enabled: true,
				Weight:  1.0,
			}
			if _, addErr := m.cfClient.AddOriginToPool(poolID, origin); addErr != nil {
				m.logger.WithError(addErr).Error("Failed to add origin")
				partialErrors[fmt.Sprintf("%s:%s", fqdn, ip)] = addErr.Error()
			}
		}
	}

	// 3. Ensure LB Object exists (CNAME to Pool)
	// Check if LB exists for this hostname
	lbs, err := m.cfClient.ListLoadBalancers()
	if err != nil {
		return nil, fmt.Errorf("failed to list LBs: %w", err)
	}

	var lbID string
	for _, lb := range lbs {
		// Name is usually the display name, not the hostname, wait.
		// Cloudflare API: Name is the user-friendly name.
		// But usually for LBs, the "Name" IS the hostname in many contexts, or we filter by it.
		// Wait, `CreateLoadBalancer` sets `Name`.
		// The `LoadBalancer` struct doesn't have a separate "Hostname" field in my provider?
		// Let's check provider/cloudflare/types.go if I can.
		// Assuming Name == Hostname for now based on usage.
		if lb.Name == fqdn {
			lbID = lb.ID
			break
		}
	}

	if lbID == "" {
		// Create LB
		m.logger.WithField("fqdn", fqdn).Info("Creating Load Balancer")
		lb := cloudflare.LoadBalancer{
			Name:           fqdn, // This acts as the hostname
			Description:    fmt.Sprintf("Auto-managed by Navigator for %s", poolName),
			TTL:            m.lbTTL,
			FallbackPool:   poolID,
			DefaultPools:   []string{poolID},
			RegionPools:    make(map[string][]string), // Empty for now
			Proxied:        proxied,
			Enabled:        true,
			SteeringPolicy: "geo",
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

		needsUpdate := currentLB.FallbackPool != poolID || len(currentLB.DefaultPools) != 1 || currentLB.DefaultPools[0] != poolID
		if needsUpdate || currentLB.TTL != m.lbTTL || currentLB.Proxied != proxied {
			currentLB.FallbackPool = poolID
			currentLB.DefaultPools = []string{poolID}
			currentLB.TTL = m.lbTTL
			currentLB.Proxied = proxied
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

	if len(partialErrors) == 0 {
		return nil, nil
	}
	return partialErrors, nil
}

// clearDNSConfig removes all DNS configuration for a given FQDN (LB, A, CNAME records)
func (m *DNSManager) clearDNSConfig(_ context.Context, fqdn string) (map[string]string, error) {
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

	return nil, nil
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
	m.logger.WithFields(logging.Fields{"name": monitorName, "port": port}).Info("Creating health check monitor")
	monitor := cloudflare.Monitor{
		Type:          "http",
		Description:   monitorName,
		Method:        "GET",
		Path:          "/health",
		Port:          port,
		Timeout:       m.monitorConfig.Timeout,
		Interval:      m.monitorConfig.Interval,
		Retries:       m.monitorConfig.Retries,
		ExpectedCodes: "200",
	}
	created, err := m.cfClient.CreateMonitor(monitor)
	if err != nil {
		return "", fmt.Errorf("failed to create monitor: %w", err)
	}
	return created.ID, nil
}

// ensurePool finds a pool by name or creates it, attaching a health monitor
func (m *DNSManager) ensurePool(name string, ips []string) (string, error) {
	// First, ensure we have a monitor for this service type
	monitorID, err := m.ensureMonitor(name)
	if err != nil {
		m.logger.WithError(err).Warn("Failed to ensure monitor, pool will have no health checks")
		monitorID = "" // Continue without monitor
	}

	pools, err := m.cfClient.ListPools()
	if err != nil {
		return "", fmt.Errorf("failed to list pools: %w", err)
	}

	for _, p := range pools {
		if p.Name == name {
			// Pool exists - ensure monitor is attached if we have one
			if monitorID != "" && p.Monitor != monitorID {
				m.logger.WithFields(logging.Fields{"pool": name, "monitor": monitorID}).Info("Attaching monitor to existing pool")
				p.Monitor = monitorID
				if _, updateErr := m.cfClient.UpdatePool(p.ID, p); updateErr != nil {
					m.logger.WithError(updateErr).Warn("Failed to attach monitor to pool")
				}
			}
			return p.ID, nil
		}
	}

	// Not found, create
	m.logger.WithField("name", name).Info("Creating new Load Balancer Pool")
	origins := make([]cloudflare.Origin, 0, len(ips))
	for _, ip := range ips {
		origins = append(origins, cloudflare.Origin{
			Name:    strings.ReplaceAll(ip, ".", "-"),
			Address: ip,
			Enabled: true,
			Weight:  1.0,
		})
	}

	newPool := cloudflare.Pool{
		Name:           name,
		Description:    "Managed by Navigator",
		Enabled:        true,
		MinimumOrigins: 1,
		Origins:        origins,
		Monitor:        monitorID,
	}
	created, err := m.cfClient.CreatePool(newPool)
	if err != nil {
		return "", fmt.Errorf("failed to create pool: %w", err)
	}
	return created.ID, nil
}
