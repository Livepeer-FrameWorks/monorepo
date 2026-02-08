package logic

import (
	"context"
	"errors"
	"testing"
	"time"

	"frameworks/api_dns/internal/provider/cloudflare"
	"frameworks/pkg/logging"
	"frameworks/pkg/proto"

	"github.com/sirupsen/logrus"
	logrustest "github.com/sirupsen/logrus/hooks/test"
)

type fakeCloudflareClient struct {
	listLoadBalancers    func() ([]cloudflare.LoadBalancer, error)
	deleteLoadBalancer   func(loadBalancerID string) error
	listDNSRecords       func(recordType, name string) ([]cloudflare.DNSRecord, error)
	updateDNSRecord      func(recordID string, record cloudflare.DNSRecord) (*cloudflare.DNSRecord, error)
	deleteDNSRecord      func(recordID string) error
	createARecord        func(name, content string, proxied bool, ttl int) (*cloudflare.DNSRecord, error)
	getPool              func(poolID string) (*cloudflare.Pool, error)
	removeOriginFromPool func(poolID, originIP string) (*cloudflare.Pool, error)
	addOriginToPool      func(poolID string, origin cloudflare.Origin) (*cloudflare.Pool, error)
	createLoadBalancer   func(lb cloudflare.LoadBalancer) (*cloudflare.LoadBalancer, error)
	getLoadBalancer      func(lbID string) (*cloudflare.LoadBalancer, error)
	updateLoadBalancer   func(lbID string, lb cloudflare.LoadBalancer) (*cloudflare.LoadBalancer, error)
	listMonitors         func() ([]cloudflare.Monitor, error)
	createMonitor        func(monitor cloudflare.Monitor) (*cloudflare.Monitor, error)
	listPools            func() ([]cloudflare.Pool, error)
	updatePool           func(poolID string, pool cloudflare.Pool) (*cloudflare.Pool, error)
	createPool           func(pool cloudflare.Pool) (*cloudflare.Pool, error)
}

func (f *fakeCloudflareClient) ListLoadBalancers() ([]cloudflare.LoadBalancer, error) {
	if f.listLoadBalancers != nil {
		return f.listLoadBalancers()
	}
	return nil, nil
}

func (f *fakeCloudflareClient) DeleteLoadBalancer(loadBalancerID string) error {
	if f.deleteLoadBalancer != nil {
		return f.deleteLoadBalancer(loadBalancerID)
	}
	return nil
}

func (f *fakeCloudflareClient) ListDNSRecords(recordType, name string) ([]cloudflare.DNSRecord, error) {
	if f.listDNSRecords != nil {
		return f.listDNSRecords(recordType, name)
	}
	return nil, nil
}

func (f *fakeCloudflareClient) UpdateDNSRecord(recordID string, record cloudflare.DNSRecord) (*cloudflare.DNSRecord, error) {
	if f.updateDNSRecord != nil {
		return f.updateDNSRecord(recordID, record)
	}
	return &record, nil
}

func (f *fakeCloudflareClient) DeleteDNSRecord(recordID string) error {
	if f.deleteDNSRecord != nil {
		return f.deleteDNSRecord(recordID)
	}
	return nil
}

func (f *fakeCloudflareClient) CreateARecord(name, content string, proxied bool, ttl int) (*cloudflare.DNSRecord, error) {
	if f.createARecord != nil {
		return f.createARecord(name, content, proxied, ttl)
	}
	return &cloudflare.DNSRecord{Name: name, Content: content}, nil
}

func (f *fakeCloudflareClient) GetPool(poolID string) (*cloudflare.Pool, error) {
	if f.getPool != nil {
		return f.getPool(poolID)
	}
	return nil, errors.New("pool not found")
}

func (f *fakeCloudflareClient) RemoveOriginFromPool(poolID, originIP string) (*cloudflare.Pool, error) {
	if f.removeOriginFromPool != nil {
		return f.removeOriginFromPool(poolID, originIP)
	}
	return nil, nil
}

func (f *fakeCloudflareClient) AddOriginToPool(poolID string, origin cloudflare.Origin) (*cloudflare.Pool, error) {
	if f.addOriginToPool != nil {
		return f.addOriginToPool(poolID, origin)
	}
	return nil, nil
}

func (f *fakeCloudflareClient) CreateLoadBalancer(lb cloudflare.LoadBalancer) (*cloudflare.LoadBalancer, error) {
	if f.createLoadBalancer != nil {
		return f.createLoadBalancer(lb)
	}
	return &lb, nil
}

func (f *fakeCloudflareClient) GetLoadBalancer(lbID string) (*cloudflare.LoadBalancer, error) {
	if f.getLoadBalancer != nil {
		return f.getLoadBalancer(lbID)
	}
	return nil, errors.New("lb not found")
}

func (f *fakeCloudflareClient) UpdateLoadBalancer(lbID string, lb cloudflare.LoadBalancer) (*cloudflare.LoadBalancer, error) {
	if f.updateLoadBalancer != nil {
		return f.updateLoadBalancer(lbID, lb)
	}
	return &lb, nil
}

func (f *fakeCloudflareClient) ListMonitors() ([]cloudflare.Monitor, error) {
	if f.listMonitors != nil {
		return f.listMonitors()
	}
	return nil, nil
}

func (f *fakeCloudflareClient) CreateMonitor(monitor cloudflare.Monitor) (*cloudflare.Monitor, error) {
	if f.createMonitor != nil {
		return f.createMonitor(monitor)
	}
	monitor.ID = "monitor"
	return &monitor, nil
}

func (f *fakeCloudflareClient) ListPools() ([]cloudflare.Pool, error) {
	if f.listPools != nil {
		return f.listPools()
	}
	return nil, nil
}

func (f *fakeCloudflareClient) UpdatePool(poolID string, pool cloudflare.Pool) (*cloudflare.Pool, error) {
	if f.updatePool != nil {
		return f.updatePool(poolID, pool)
	}
	return &pool, nil
}

func (f *fakeCloudflareClient) CreatePool(pool cloudflare.Pool) (*cloudflare.Pool, error) {
	if f.createPool != nil {
		return f.createPool(pool)
	}
	pool.ID = "pool"
	return &pool, nil
}

type fakeQuartermasterClient struct {
	nodeType  string
	staleAge  int
	response  *proto.ListHealthyNodesForDNSResponse
	err       error
	callCount int
}

func (f *fakeQuartermasterClient) ListHealthyNodesForDNS(ctx context.Context, nodeType string, staleThresholdSeconds int) (*proto.ListHealthyNodesForDNSResponse, error) {
	f.callCount++
	f.nodeType = nodeType
	f.staleAge = staleThresholdSeconds
	return f.response, f.err
}

func TestSyncService_UsesStaleAgeSeconds(t *testing.T) {
	qm := &fakeQuartermasterClient{err: errors.New("quartermaster unavailable")}
	cf := &fakeCloudflareClient{}
	logger := logrus.New()
	manager := NewDNSManager(cf, qm, logger, "example.com", 60, 60, 5*time.Minute, MonitorConfig{})

	_, err := manager.SyncService(context.Background(), "edge", "")
	if err == nil {
		t.Fatal("expected error from Quartermaster")
	}
	if qm.nodeType != "edge" {
		t.Fatalf("expected node type edge, got %s", qm.nodeType)
	}
	if qm.staleAge != 300 {
		t.Fatalf("expected stale age 300 seconds, got %d", qm.staleAge)
	}
}

func TestSyncService_NoActiveNodesLogsWarning(t *testing.T) {
	qm := &fakeQuartermasterClient{
		response: &proto.ListHealthyNodesForDNSResponse{},
	}
	cf := &fakeCloudflareClient{
		listLoadBalancers: func() ([]cloudflare.LoadBalancer, error) {
			return nil, nil
		},
		listDNSRecords: func(recordType, name string) ([]cloudflare.DNSRecord, error) {
			return nil, nil
		},
	}
	logger := logrus.New()
	logger.SetLevel(logrus.DebugLevel)
	hook := logrustest.NewLocal(logger)
	manager := NewDNSManager(cf, qm, logger, "example.com", 60, 60, 5*time.Minute, MonitorConfig{})

	_, err := manager.SyncService(context.Background(), "edge", "")
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	var warnEntry *logrus.Entry
	for _, entry := range hook.AllEntries() {
		if entry.Message == "No active nodes found, removing DNS records" {
			warnEntry = entry
			break
		}
	}
	if warnEntry == nil {
		t.Fatal("expected warning log for empty node set")
	}
	if warnEntry.Level != logrus.WarnLevel {
		t.Fatalf("expected warn level log, got %s", warnEntry.Level.String())
	}
	if warnEntry.Data["service"] != "edge" {
		t.Fatalf("expected service field edge, got %v", warnEntry.Data["service"])
	}
}

func TestApplyLoadBalancerConfig_ReturnsPartialErrors(t *testing.T) {
	cf := &fakeCloudflareClient{
		listMonitors: func() ([]cloudflare.Monitor, error) {
			return nil, nil
		},
		createMonitor: func(monitor cloudflare.Monitor) (*cloudflare.Monitor, error) {
			monitor.ID = "monitor"
			return &monitor, nil
		},
		listPools: func() ([]cloudflare.Pool, error) {
			return nil, nil
		},
		createPool: func(pool cloudflare.Pool) (*cloudflare.Pool, error) {
			pool.ID = "pool"
			return &pool, nil
		},
		getPool: func(poolID string) (*cloudflare.Pool, error) {
			return &cloudflare.Pool{
				ID: poolID,
				Origins: []cloudflare.Origin{
					{Name: "one", Address: "1.1.1.1"},
					{Name: "two", Address: "2.2.2.2"},
				},
			}, nil
		},
		removeOriginFromPool: func(poolID, originIP string) (*cloudflare.Pool, error) {
			if originIP == "1.1.1.1" {
				return nil, errors.New("remove failed")
			}
			return nil, nil
		},
		addOriginToPool: func(poolID string, origin cloudflare.Origin) (*cloudflare.Pool, error) {
			if origin.Address == "3.3.3.3" {
				return nil, errors.New("add failed")
			}
			return nil, nil
		},
		listLoadBalancers: func() ([]cloudflare.LoadBalancer, error) {
			return nil, nil
		},
		createLoadBalancer: func(lb cloudflare.LoadBalancer) (*cloudflare.LoadBalancer, error) {
			lb.ID = "lb"
			return &lb, nil
		},
		listDNSRecords: func(recordType, name string) ([]cloudflare.DNSRecord, error) {
			return nil, nil
		},
	}

	logger := logrus.New()
	manager := NewDNSManager(cf, &fakeQuartermasterClient{}, logger, "example.com", 60, 60, 5*time.Minute, MonitorConfig{})

	partialErrors, err := manager.applyLoadBalancerConfig(context.Background(), "edge.example.com", "edge", []string{"2.2.2.2", "3.3.3.3"}, false)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if len(partialErrors) != 2 {
		t.Fatalf("expected 2 partial errors, got %d", len(partialErrors))
	}
	if _, ok := partialErrors["edge.example.com:1.1.1.1"]; !ok {
		t.Fatalf("expected partial error for stale origin removal")
	}
	if _, ok := partialErrors["edge.example.com:3.3.3.3"]; !ok {
		t.Fatalf("expected partial error for origin add failure")
	}
}

var _ logging.Logger = (*logrus.Logger)(nil)
