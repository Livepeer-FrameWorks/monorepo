package logic

import (
	"context"
	"strings"
	"testing"

	"frameworks/api_dns/internal/provider/cloudflare"
	pkgdns "github.com/Livepeer-FrameWorks/monorepo/pkg/dns"
	quartermasterpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/quartermaster"
)

// Cloudflare-served services sit behind each host's reverse proxy and their
// service ports are not public (Bridge binds loopback and the mesh address), so
// a multi-origin load balancer is health-checked through the ingress, never on
// the raw service port at the origin's external IP.
func TestSyncService_CloudflareServicesProbeTheIngressNotTheServicePort(t *testing.T) {
	ip1, ip2 := "10.0.0.1", "10.0.1.1"
	for _, serviceType := range pkgdns.CloudflareManagedServiceTypes() {
		t.Run(serviceType, func(t *testing.T) {
			qm := &fakeQuartermasterClient{response: &quartermasterpb.ListHealthyNodesForDNSResponse{
				Nodes: []*quartermasterpb.InfrastructureNode{
					{NodeId: "regional-eu-1", ExternalIp: strPtr(ip1)},
					{NodeId: "regional-us-1", ExternalIp: strPtr(ip2)},
				},
			}}
			var monitors []cloudflare.Monitor
			pools := map[string]cloudflare.Pool{}
			cf := &fakeCloudflareClient{
				listMonitors: func() ([]cloudflare.Monitor, error) { return monitors, nil },
				createMonitor: func(mon cloudflare.Monitor) (*cloudflare.Monitor, error) {
					mon.ID = mon.Description
					monitors = append(monitors, mon)
					return &mon, nil
				},
				listPools: func() ([]cloudflare.Pool, error) {
					out := make([]cloudflare.Pool, 0, len(pools))
					for _, p := range pools {
						out = append(out, p)
					}
					return out, nil
				},
				createPool: func(p cloudflare.Pool) (*cloudflare.Pool, error) {
					p.ID = p.Name
					pools[p.Name] = p
					return &p, nil
				},
				listLoadBalancers: func() ([]cloudflare.LoadBalancer, error) { return nil, nil },
				createLoadBalancer: func(lb cloudflare.LoadBalancer) (*cloudflare.LoadBalancer, error) {
					lb.ID = lb.Name
					return &lb, nil
				},
				listDNSRecords: func(string, string) ([]cloudflare.DNSRecord, error) { return nil, nil },
			}
			m := newTestManager(cf)
			m.qmClient = qm
			m.proxy = loadProxyServices("")
			m.servicePorts = defaultServicePorts()
			m.healthPaths = defaultServiceHealthPaths()

			if _, err := m.SyncService(context.Background(), serviceType, ""); err != nil {
				t.Fatalf("sync %s: %v", serviceType, err)
			}
			if len(pools) == 0 {
				t.Fatalf("sync %s created no load-balancer pools", serviceType)
			}
			byID := map[string]cloudflare.Monitor{}
			for _, mon := range monitors {
				byID[mon.ID] = mon
			}
			for _, pool := range pools {
				mon, ok := byID[pool.Monitor]
				if !ok {
					t.Fatalf("pool %s has no monitor", pool.Name)
				}
				if mon.Type != "http" || mon.Port != 80 || mon.Path != "/health" {
					t.Fatalf("pool %s monitor = %s :%d %s, want the ingress probe http :80 /health", pool.Name, mon.Type, mon.Port, mon.Path)
				}
			}
		})
	}
}

// Without Bunny, a media cluster's service records fall back to Cloudflare
// load balancers. Foghorn, Chandler, vmauth (telemetry) and the Livepeer
// gateway listen only on loopback and the WireGuard address, so those pools
// are probed through the ingress too; only edges are probed on the node.
func TestEnsureMonitor_ClusterServicesProbeTheIngress(t *testing.T) {
	for _, serviceType := range pkgdns.BunnyManagedServiceTypes() {
		var created cloudflare.Monitor
		cf := &fakeCloudflareClient{
			listMonitors: func() ([]cloudflare.Monitor, error) { return nil, nil },
			createMonitor: func(mon cloudflare.Monitor) (*cloudflare.Monitor, error) {
				created = mon
				mon.ID = mon.Description
				return &mon, nil
			},
		}
		m := newTestManager(cf)
		if _, err := m.ensureMonitor(serviceType); err != nil {
			t.Fatalf("ensureMonitor %s: %v", serviceType, err)
		}
		edge := strings.HasPrefix(serviceType, "edge")
		if !edge && (created.Port != 80 || created.Path != "/health") {
			t.Fatalf("%s monitor = :%d %s, want the ingress probe :80 /health", serviceType, created.Port, created.Path)
		}
		if edge && created.Port != 18008 {
			t.Fatalf("%s monitor port = %d, want the edge probe on 18008", serviceType, created.Port)
		}
	}
}
