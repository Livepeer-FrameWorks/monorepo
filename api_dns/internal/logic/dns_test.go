package logic

import (
	"context"
	"errors"
	"strings"
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
	nodeType         string
	staleAge         int
	response         *proto.ListHealthyNodesForDNSResponse
	err              error
	callCount        int
	clustersResponse *proto.ListClustersResponse
	clustersErr      error
}

func (f *fakeQuartermasterClient) ListHealthyNodesForDNS(ctx context.Context, nodeType string, staleThresholdSeconds int) (*proto.ListHealthyNodesForDNSResponse, error) {
	f.callCount++
	f.nodeType = nodeType
	f.staleAge = staleThresholdSeconds
	return f.response, f.err
}

func (f *fakeQuartermasterClient) ListClusters(ctx context.Context, pagination *proto.CursorPaginationRequest) (*proto.ListClustersResponse, error) {
	if f.clustersResponse == nil {
		return &proto.ListClustersResponse{}, f.clustersErr
	}
	return f.clustersResponse, f.clustersErr
}

func TestSyncService_UsesStaleAgeSeconds(t *testing.T) {
	qm := &fakeQuartermasterClient{err: errors.New("quartermaster unavailable")}
	cf := &fakeCloudflareClient{}
	logger := logrus.New()
	manager := NewDNSManager(cf, qm, logger, "example.com", 60, 60, 5*time.Minute, MonitorConfig{})

	_, err := manager.SyncService(context.Background(), "edge-egress", "")
	if err == nil {
		t.Fatal("expected error from Quartermaster")
	}
	if qm.nodeType != "edge-egress" {
		t.Fatalf("expected node type edge-egress, got %s", qm.nodeType)
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

	_, err := manager.SyncService(context.Background(), "edge-egress", "")
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
	if warnEntry.Data["service"] != "edge-egress" {
		t.Fatalf("expected service field edge-egress, got %v", warnEntry.Data["service"])
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

	partialErrors, err := manager.applyLoadBalancerConfig(context.Background(), "edge-egress.example.com", "edge-egress", "edge-egress", []string{"2.2.2.2", "3.3.3.3"}, false)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if len(partialErrors) != 2 {
		t.Fatalf("expected 2 partial errors, got %d", len(partialErrors))
	}
	if _, ok := partialErrors["edge-egress.example.com:1.1.1.1"]; !ok {
		t.Fatalf("expected partial error for stale origin removal")
	}
	if _, ok := partialErrors["edge-egress.example.com:3.3.3.3"]; !ok {
		t.Fatalf("expected partial error for origin add failure")
	}
}

func TestApplyLoadBalancerConfig_ListLoadBalancersError(t *testing.T) {
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
		listLoadBalancers: func() ([]cloudflare.LoadBalancer, error) {
			return nil, errors.New("list lb failed")
		},
	}

	manager := newTestManager(cf)
	_, err := manager.applyLoadBalancerConfig(context.Background(), "edge-egress.example.com", "edge-egress", "edge-egress", []string{"2.2.2.2"}, false)
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "failed to list LBs") {
		t.Fatalf("expected wrapped error, got: %v", err)
	}
}

func TestApplyLoadBalancerConfig_UpdateLoadBalancerError(t *testing.T) {
	cf := &fakeCloudflareClient{
		listMonitors: func() ([]cloudflare.Monitor, error) {
			return nil, nil
		},
		createMonitor: func(monitor cloudflare.Monitor) (*cloudflare.Monitor, error) {
			monitor.ID = "monitor"
			return &monitor, nil
		},
		listPools: func() ([]cloudflare.Pool, error) {
			return []cloudflare.Pool{{ID: "pool", Name: "edge-egress"}}, nil
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
		listLoadBalancers: func() ([]cloudflare.LoadBalancer, error) {
			return []cloudflare.LoadBalancer{{ID: "lb1", Name: "edge-egress.example.com"}}, nil
		},
		getLoadBalancer: func(lbID string) (*cloudflare.LoadBalancer, error) {
			return &cloudflare.LoadBalancer{
				ID:           lbID,
				Name:         "edge-egress.example.com",
				FallbackPool: "wrong-pool",
				DefaultPools: []string{"wrong-pool"},
				TTL:          30,
				Proxied:      false,
				Enabled:      true,
			}, nil
		},
		updateLoadBalancer: func(lbID string, lb cloudflare.LoadBalancer) (*cloudflare.LoadBalancer, error) {
			return nil, errors.New("update failed")
		},
		listDNSRecords: func(recordType, name string) ([]cloudflare.DNSRecord, error) {
			return nil, nil
		},
	}

	manager := newTestManager(cf)
	_, err := manager.applyLoadBalancerConfig(context.Background(), "edge-egress.example.com", "edge-egress", "edge-egress", []string{"2.2.2.2"}, false)
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "failed to update LB") {
		t.Fatalf("expected wrapped error, got: %v", err)
	}
}

func newTestManager(cf *fakeCloudflareClient) *DNSManager {
	logger := logrus.New()
	logger.SetLevel(logrus.DebugLevel)
	return &DNSManager{
		cfClient:  cf,
		qmClient:  &fakeQuartermasterClient{},
		logger:    logger,
		domain:    "example.com",
		proxy:     map[string]bool{"chartroom": true},
		recordTTL: 60,
		lbTTL:     60,
		staleAge:  5 * time.Minute,
		monitorConfig: MonitorConfig{
			Interval: 60,
			Timeout:  5,
			Retries:  2,
		},
		servicePorts: defaultServicePorts(),
	}
}

func TestApplySingleNodeConfig_CreateARecord(t *testing.T) {
	var created bool
	cf := &fakeCloudflareClient{
		listDNSRecords: func(recordType, name string) ([]cloudflare.DNSRecord, error) {
			return nil, nil
		},
		createARecord: func(name, content string, proxied bool, ttl int) (*cloudflare.DNSRecord, error) {
			created = true
			if name != "edge-egress.example.com" {
				t.Fatalf("expected name edge-egress.example.com, got %s", name)
			}
			if content != "1.2.3.4" {
				t.Fatalf("expected content 1.2.3.4, got %s", content)
			}
			if proxied {
				t.Fatal("expected proxied=false")
			}
			if ttl != 60 {
				t.Fatalf("expected ttl 60, got %d", ttl)
			}
			return &cloudflare.DNSRecord{ID: "rec1", Name: name, Content: content}, nil
		},
	}
	m := newTestManager(cf)

	err := m.applySingleNodeConfig(context.Background(), "edge-egress.example.com", "1.2.3.4", false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !created {
		t.Fatal("expected A record to be created")
	}
}

func TestApplySingleNodeConfig_CreateARecordError(t *testing.T) {
	cf := &fakeCloudflareClient{
		listDNSRecords: func(recordType, name string) ([]cloudflare.DNSRecord, error) {
			return nil, nil
		},
		createARecord: func(name, content string, proxied bool, ttl int) (*cloudflare.DNSRecord, error) {
			return nil, errors.New("cf api down")
		},
	}
	m := newTestManager(cf)

	err := m.applySingleNodeConfig(context.Background(), "edge-egress.example.com", "1.2.3.4", false)
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "failed to create A record") {
		t.Fatalf("expected wrapped error, got: %v", err)
	}
}

func TestApplySingleNodeConfig_UpdateExistingRecord(t *testing.T) {
	var updated bool
	cf := &fakeCloudflareClient{
		listDNSRecords: func(recordType, name string) ([]cloudflare.DNSRecord, error) {
			return []cloudflare.DNSRecord{
				{ID: "rec1", Content: "9.9.9.9", Proxied: false, TTL: 60},
			}, nil
		},
		updateDNSRecord: func(recordID string, record cloudflare.DNSRecord) (*cloudflare.DNSRecord, error) {
			updated = true
			if recordID != "rec1" {
				t.Fatalf("expected record ID rec1, got %s", recordID)
			}
			if record.Content != "1.2.3.4" {
				t.Fatalf("expected new IP 1.2.3.4, got %s", record.Content)
			}
			return &record, nil
		},
	}
	m := newTestManager(cf)

	err := m.applySingleNodeConfig(context.Background(), "edge-egress.example.com", "1.2.3.4", false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !updated {
		t.Fatal("expected record to be updated")
	}
}

func TestApplySingleNodeConfig_NoUpdateWhenUnchanged(t *testing.T) {
	var updated bool
	cf := &fakeCloudflareClient{
		listDNSRecords: func(recordType, name string) ([]cloudflare.DNSRecord, error) {
			return []cloudflare.DNSRecord{
				{ID: "rec1", Content: "1.2.3.4", Proxied: false, TTL: 60},
			}, nil
		},
		updateDNSRecord: func(recordID string, record cloudflare.DNSRecord) (*cloudflare.DNSRecord, error) {
			updated = true
			return &record, nil
		},
	}
	m := newTestManager(cf)

	err := m.applySingleNodeConfig(context.Background(), "edge-egress.example.com", "1.2.3.4", false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if updated {
		t.Fatal("expected no update when record unchanged")
	}
}

func TestApplySingleNodeConfig_DeletesExtraRecords(t *testing.T) {
	var deleted []string
	cf := &fakeCloudflareClient{
		listDNSRecords: func(recordType, name string) ([]cloudflare.DNSRecord, error) {
			return []cloudflare.DNSRecord{
				{ID: "rec1", Content: "1.2.3.4", Proxied: false, TTL: 60},
				{ID: "rec2", Content: "5.5.5.5", Proxied: false, TTL: 60},
				{ID: "rec3", Content: "6.6.6.6", Proxied: false, TTL: 60},
			}, nil
		},
		deleteDNSRecord: func(recordID string) error {
			deleted = append(deleted, recordID)
			return nil
		},
	}
	m := newTestManager(cf)

	err := m.applySingleNodeConfig(context.Background(), "edge-egress.example.com", "1.2.3.4", false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(deleted) != 2 {
		t.Fatalf("expected 2 extra records deleted, got %d", len(deleted))
	}
	if deleted[0] != "rec2" || deleted[1] != "rec3" {
		t.Fatalf("expected rec2,rec3 deleted, got %v", deleted)
	}
}

func TestApplySingleNodeConfig_ListRecordsError(t *testing.T) {
	cf := &fakeCloudflareClient{
		listDNSRecords: func(recordType, name string) ([]cloudflare.DNSRecord, error) {
			return nil, errors.New("list failed")
		},
	}
	m := newTestManager(cf)

	err := m.applySingleNodeConfig(context.Background(), "edge-egress.example.com", "1.2.3.4", false)
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "failed to list existing A records") {
		t.Fatalf("expected wrapped error, got: %v", err)
	}
}

func TestApplySingleNodeConfig_CleansUpConflictingLB(t *testing.T) {
	var lbDeleted bool
	cf := &fakeCloudflareClient{
		listLoadBalancers: func() ([]cloudflare.LoadBalancer, error) {
			return []cloudflare.LoadBalancer{
				{ID: "lb1", Name: "edge-egress.example.com"},
				{ID: "lb2", Name: "other.example.com"},
			}, nil
		},
		deleteLoadBalancer: func(id string) error {
			if id == "lb1" {
				lbDeleted = true
			}
			return nil
		},
		listDNSRecords: func(recordType, name string) ([]cloudflare.DNSRecord, error) {
			return nil, nil
		},
		createARecord: func(name, content string, proxied bool, ttl int) (*cloudflare.DNSRecord, error) {
			return &cloudflare.DNSRecord{}, nil
		},
	}
	m := newTestManager(cf)

	err := m.applySingleNodeConfig(context.Background(), "edge-egress.example.com", "1.2.3.4", false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !lbDeleted {
		t.Fatal("expected conflicting LB to be deleted")
	}
}

func TestClearDNSConfig_DeletesMatchingRecordsAndLBs(t *testing.T) {
	var lbDeleted, aDeleted, cnameDeleted bool
	cf := &fakeCloudflareClient{
		listLoadBalancers: func() ([]cloudflare.LoadBalancer, error) {
			return []cloudflare.LoadBalancer{
				{ID: "lb1", Name: "edge-egress.example.com"},
			}, nil
		},
		deleteLoadBalancer: func(id string) error {
			lbDeleted = true
			return nil
		},
		listDNSRecords: func(recordType, name string) ([]cloudflare.DNSRecord, error) {
			switch recordType {
			case "A":
				return []cloudflare.DNSRecord{{ID: "a1"}}, nil
			case "CNAME":
				return []cloudflare.DNSRecord{{ID: "cname1"}}, nil
			}
			return nil, nil
		},
		deleteDNSRecord: func(id string) error {
			if id == "a1" {
				aDeleted = true
			}
			if id == "cname1" {
				cnameDeleted = true
			}
			return nil
		},
	}
	m := newTestManager(cf)

	_, err := m.clearDNSConfig(context.Background(), "edge-egress.example.com")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !lbDeleted {
		t.Fatal("expected LB deleted")
	}
	if !aDeleted {
		t.Fatal("expected A record deleted")
	}
	if !cnameDeleted {
		t.Fatal("expected CNAME record deleted")
	}
}

func TestClearDNSConfig_NoRecords(t *testing.T) {
	cf := &fakeCloudflareClient{
		listLoadBalancers: func() ([]cloudflare.LoadBalancer, error) {
			return nil, nil
		},
		listDNSRecords: func(recordType, name string) ([]cloudflare.DNSRecord, error) {
			return nil, nil
		},
	}
	m := newTestManager(cf)

	partialErrors, err := m.clearDNSConfig(context.Background(), "edge-egress.example.com")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if partialErrors != nil {
		t.Fatalf("expected nil partial errors, got %v", partialErrors)
	}
}

func TestClearDNSConfig_ListLBError(t *testing.T) {
	cf := &fakeCloudflareClient{
		listLoadBalancers: func() ([]cloudflare.LoadBalancer, error) {
			return nil, errors.New("api down")
		},
	}
	m := newTestManager(cf)

	_, err := m.clearDNSConfig(context.Background(), "edge-egress.example.com")
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "failed to list LBs") {
		t.Fatalf("expected wrapped error, got: %v", err)
	}
}

func TestClearDNSConfig_DeleteLBError(t *testing.T) {
	cf := &fakeCloudflareClient{
		listLoadBalancers: func() ([]cloudflare.LoadBalancer, error) {
			return []cloudflare.LoadBalancer{{ID: "lb1", Name: "edge-egress.example.com"}}, nil
		},
		deleteLoadBalancer: func(id string) error {
			return errors.New("delete failed")
		},
	}
	m := newTestManager(cf)

	_, err := m.clearDNSConfig(context.Background(), "edge-egress.example.com")
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "failed to delete LB") {
		t.Fatalf("expected wrapped error, got: %v", err)
	}
}

func TestClearDNSConfig_DeleteRecordError(t *testing.T) {
	cf := &fakeCloudflareClient{
		listLoadBalancers: func() ([]cloudflare.LoadBalancer, error) {
			return nil, nil
		},
		listDNSRecords: func(recordType, name string) ([]cloudflare.DNSRecord, error) {
			if recordType == "A" {
				return []cloudflare.DNSRecord{{ID: "a1"}}, nil
			}
			return nil, nil
		},
		deleteDNSRecord: func(id string) error {
			return errors.New("delete record failed")
		},
	}
	m := newTestManager(cf)

	_, err := m.clearDNSConfig(context.Background(), "edge-egress.example.com")
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "failed to delete DNS record") {
		t.Fatalf("expected wrapped error, got: %v", err)
	}
}

func TestEnsureMonitor_ReusesExisting(t *testing.T) {
	cf := &fakeCloudflareClient{
		listMonitors: func() ([]cloudflare.Monitor, error) {
			return []cloudflare.Monitor{
				{ID: "mon-abc", Description: "nav-edge-egress-health"},
				{ID: "mon-xyz", Description: "nav-gateway-health"},
			}, nil
		},
	}
	m := newTestManager(cf)

	id, err := m.ensureMonitor("edge-egress")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if id != "mon-abc" {
		t.Fatalf("expected mon-abc, got %s", id)
	}
}

func TestEnsureMonitor_CreatesNew(t *testing.T) {
	var created cloudflare.Monitor
	cf := &fakeCloudflareClient{
		listMonitors: func() ([]cloudflare.Monitor, error) {
			return nil, nil
		},
		createMonitor: func(monitor cloudflare.Monitor) (*cloudflare.Monitor, error) {
			created = monitor
			monitor.ID = "new-mon"
			return &monitor, nil
		},
	}
	m := newTestManager(cf)

	id, err := m.ensureMonitor("edge-egress")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if id != "new-mon" {
		t.Fatalf("expected new-mon, got %s", id)
	}
	if created.Description != "nav-edge-egress-health" {
		t.Fatalf("expected description nav-edge-health, got %s", created.Description)
	}
	if created.Port != 18008 {
		t.Fatalf("expected port 18008, got %d", created.Port)
	}
	if created.Path != "/health" {
		t.Fatalf("expected path /health, got %s", created.Path)
	}
}

func TestEnsureMonitor_ListError(t *testing.T) {
	cf := &fakeCloudflareClient{
		listMonitors: func() ([]cloudflare.Monitor, error) {
			return nil, errors.New("api error")
		},
	}
	m := newTestManager(cf)

	_, err := m.ensureMonitor("edge-egress")
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "failed to list monitors") {
		t.Fatalf("expected wrapped error, got: %v", err)
	}
}

func TestEnsureMonitor_CreateError(t *testing.T) {
	cf := &fakeCloudflareClient{
		listMonitors: func() ([]cloudflare.Monitor, error) {
			return nil, nil
		},
		createMonitor: func(monitor cloudflare.Monitor) (*cloudflare.Monitor, error) {
			return nil, errors.New("create failed")
		},
	}
	m := newTestManager(cf)

	_, err := m.ensureMonitor("edge-egress")
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "failed to create monitor") {
		t.Fatalf("expected wrapped error, got: %v", err)
	}
}

func TestEnsureMonitor_DefaultPort(t *testing.T) {
	var created cloudflare.Monitor
	cf := &fakeCloudflareClient{
		listMonitors: func() ([]cloudflare.Monitor, error) {
			return nil, nil
		},
		createMonitor: func(monitor cloudflare.Monitor) (*cloudflare.Monitor, error) {
			created = monitor
			monitor.ID = "mon"
			return &monitor, nil
		},
	}
	m := newTestManager(cf)

	// "unknown-svc" has no entry in servicePorts, should fall back to 80
	_, err := m.ensureMonitor("unknown-svc")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if created.Port != 80 {
		t.Fatalf("expected default port 80, got %d", created.Port)
	}
}

func TestEnsurePool_ReusesExisting(t *testing.T) {
	cf := &fakeCloudflareClient{
		listMonitors: func() ([]cloudflare.Monitor, error) {
			return []cloudflare.Monitor{{ID: "mon1", Description: "nav-edge-egress-health"}}, nil
		},
		listPools: func() ([]cloudflare.Pool, error) {
			return []cloudflare.Pool{
				{ID: "pool1", Name: "edge-egress", Monitor: "mon1"},
			}, nil
		},
	}
	m := newTestManager(cf)

	id, err := m.ensurePool("edge-egress", "edge-egress", []string{"1.1.1.1"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if id != "pool1" {
		t.Fatalf("expected pool1, got %s", id)
	}
}

func TestEnsurePool_AttachesMonitorToExisting(t *testing.T) {
	var poolUpdated bool
	cf := &fakeCloudflareClient{
		listMonitors: func() ([]cloudflare.Monitor, error) {
			return []cloudflare.Monitor{{ID: "mon-new", Description: "nav-edge-egress-health"}}, nil
		},
		listPools: func() ([]cloudflare.Pool, error) {
			return []cloudflare.Pool{
				{ID: "pool1", Name: "edge-egress", Monitor: "mon-old"},
			}, nil
		},
		updatePool: func(poolID string, pool cloudflare.Pool) (*cloudflare.Pool, error) {
			poolUpdated = true
			if pool.Monitor != "mon-new" {
				t.Fatalf("expected monitor mon-new, got %s", pool.Monitor)
			}
			return &pool, nil
		},
	}
	m := newTestManager(cf)

	id, err := m.ensurePool("edge-egress", "edge-egress", []string{"1.1.1.1"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if id != "pool1" {
		t.Fatalf("expected pool1, got %s", id)
	}
	if !poolUpdated {
		t.Fatal("expected pool to be updated with new monitor")
	}
}

func TestEnsurePool_CreatesNew(t *testing.T) {
	var created cloudflare.Pool
	cf := &fakeCloudflareClient{
		listMonitors: func() ([]cloudflare.Monitor, error) {
			return nil, nil
		},
		createMonitor: func(monitor cloudflare.Monitor) (*cloudflare.Monitor, error) {
			monitor.ID = "mon1"
			return &monitor, nil
		},
		listPools: func() ([]cloudflare.Pool, error) {
			return nil, nil
		},
		createPool: func(pool cloudflare.Pool) (*cloudflare.Pool, error) {
			created = pool
			pool.ID = "new-pool"
			return &pool, nil
		},
	}
	m := newTestManager(cf)

	id, err := m.ensurePool("edge-egress", "edge-egress", []string{"1.1.1.1", "2.2.2.2"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if id != "new-pool" {
		t.Fatalf("expected new-pool, got %s", id)
	}
	if created.Name != "edge-egress" {
		t.Fatalf("expected name edge, got %s", created.Name)
	}
	if len(created.Origins) != 2 {
		t.Fatalf("expected 2 origins, got %d", len(created.Origins))
	}
	if created.Monitor != "mon1" {
		t.Fatalf("expected monitor mon1, got %s", created.Monitor)
	}
}

func TestEnsurePool_ListPoolsError(t *testing.T) {
	cf := &fakeCloudflareClient{
		listMonitors: func() ([]cloudflare.Monitor, error) {
			return nil, nil
		},
		createMonitor: func(monitor cloudflare.Monitor) (*cloudflare.Monitor, error) {
			monitor.ID = "mon1"
			return &monitor, nil
		},
		listPools: func() ([]cloudflare.Pool, error) {
			return nil, errors.New("list failed")
		},
	}
	m := newTestManager(cf)

	_, err := m.ensurePool("edge-egress", "edge-egress", []string{"1.1.1.1"})
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "failed to list pools") {
		t.Fatalf("expected wrapped error, got: %v", err)
	}
}

func TestEnsurePool_CreatePoolError(t *testing.T) {
	cf := &fakeCloudflareClient{
		listMonitors: func() ([]cloudflare.Monitor, error) {
			return nil, nil
		},
		createMonitor: func(monitor cloudflare.Monitor) (*cloudflare.Monitor, error) {
			monitor.ID = "mon1"
			return &monitor, nil
		},
		listPools: func() ([]cloudflare.Pool, error) {
			return nil, nil
		},
		createPool: func(pool cloudflare.Pool) (*cloudflare.Pool, error) {
			return nil, errors.New("create failed")
		},
	}
	m := newTestManager(cf)

	_, err := m.ensurePool("edge-egress", "edge-egress", []string{"1.1.1.1"})
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "failed to create pool") {
		t.Fatalf("expected wrapped error, got: %v", err)
	}
}

func TestEnsurePool_ContinuesWithoutMonitor(t *testing.T) {
	cf := &fakeCloudflareClient{
		listMonitors: func() ([]cloudflare.Monitor, error) {
			return nil, errors.New("monitor api broken")
		},
		listPools: func() ([]cloudflare.Pool, error) {
			return nil, nil
		},
		createPool: func(pool cloudflare.Pool) (*cloudflare.Pool, error) {
			if pool.Monitor != "" {
				t.Fatalf("expected empty monitor when ensureMonitor fails, got %s", pool.Monitor)
			}
			pool.ID = "pool1"
			return &pool, nil
		},
	}
	m := newTestManager(cf)

	id, err := m.ensurePool("edge-egress", "edge-egress", []string{"1.1.1.1"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if id != "pool1" {
		t.Fatalf("expected pool1, got %s", id)
	}
}

func TestSyncService_UnknownServiceType(t *testing.T) {
	ip := "1.2.3.4"
	qm := &fakeQuartermasterClient{
		response: &proto.ListHealthyNodesForDNSResponse{
			Nodes: []*proto.InfrastructureNode{{ExternalIp: &ip}},
		},
	}
	m := newTestManager(&fakeCloudflareClient{})
	m.qmClient = qm

	_, err := m.SyncService(context.Background(), "nonexistent", "")
	if err == nil {
		t.Fatal("expected error for unknown service type")
	}
	if !strings.Contains(err.Error(), "unknown service type") {
		t.Fatalf("expected unknown service type error, got: %v", err)
	}
}

func TestSyncService_SingleNode(t *testing.T) {
	ip := "10.0.0.1"
	var createdFQDN string
	qm := &fakeQuartermasterClient{
		response: &proto.ListHealthyNodesForDNSResponse{
			Nodes: []*proto.InfrastructureNode{{ExternalIp: &ip}},
		},
	}
	cf := &fakeCloudflareClient{
		listDNSRecords: func(recordType, name string) ([]cloudflare.DNSRecord, error) {
			return nil, nil
		},
		createARecord: func(name, content string, proxied bool, ttl int) (*cloudflare.DNSRecord, error) {
			createdFQDN = name
			return &cloudflare.DNSRecord{}, nil
		},
	}
	m := newTestManager(cf)
	m.qmClient = qm

	_, err := m.SyncService(context.Background(), "edge-egress", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if createdFQDN != "edge-egress.example.com" {
		t.Fatalf("expected edge-egress.example.com, got %s", createdFQDN)
	}
}

func TestSyncService_CustomRootDomain(t *testing.T) {
	ip := "10.0.0.1"
	var createdFQDN string
	qm := &fakeQuartermasterClient{
		response: &proto.ListHealthyNodesForDNSResponse{
			Nodes: []*proto.InfrastructureNode{{ExternalIp: &ip}},
		},
	}
	cf := &fakeCloudflareClient{
		listDNSRecords: func(recordType, name string) ([]cloudflare.DNSRecord, error) {
			return nil, nil
		},
		createARecord: func(name, content string, proxied bool, ttl int) (*cloudflare.DNSRecord, error) {
			createdFQDN = name
			return &cloudflare.DNSRecord{}, nil
		},
	}
	m := newTestManager(cf)
	m.qmClient = qm

	_, err := m.SyncService(context.Background(), "edge-egress", "custom.net")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if createdFQDN != "edge-egress.custom.net" {
		t.Fatalf("expected edge-egress.custom.net, got %s", createdFQDN)
	}
}

func TestSyncService_WebsiteUsesRootDomain(t *testing.T) {
	ip := "10.0.0.1"
	var createdFQDN string
	qm := &fakeQuartermasterClient{
		response: &proto.ListHealthyNodesForDNSResponse{
			Nodes: []*proto.InfrastructureNode{{ExternalIp: &ip}},
		},
	}
	cf := &fakeCloudflareClient{
		listDNSRecords: func(recordType, name string) ([]cloudflare.DNSRecord, error) {
			return nil, nil
		},
		createARecord: func(name, content string, proxied bool, ttl int) (*cloudflare.DNSRecord, error) {
			createdFQDN = name
			return &cloudflare.DNSRecord{}, nil
		},
	}
	m := newTestManager(cf)
	m.qmClient = qm

	_, err := m.SyncService(context.Background(), "website", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// website subdomain is "@", so fqdn should be just the domain
	if createdFQDN != "example.com" {
		t.Fatalf("expected example.com (root domain), got %s", createdFQDN)
	}
}

func TestSyncService_SubdomainMapping(t *testing.T) {
	tests := []struct {
		serviceType     string
		expectedSubpart string
	}{
		{"edge", "edge.example.com"},
		{"edge-egress", "edge-egress.example.com"},
		{"edge-ingest", "edge-ingest.example.com"},
		{"foghorn", "foghorn.example.com"},
		{"gateway", "bridge.example.com"},
		{"bridge", "bridge.example.com"},
		{"chartroom", "chartroom.example.com"},
		{"website", "example.com"},
		{"logbook", "logbook.example.com"},
		{"steward", "steward.example.com"},
	}

	for _, tt := range tests {
		t.Run(tt.serviceType, func(t *testing.T) {
			ip := "10.0.0.1"
			var createdFQDN string
			qm := &fakeQuartermasterClient{
				response: &proto.ListHealthyNodesForDNSResponse{
					Nodes: []*proto.InfrastructureNode{{ExternalIp: &ip}},
				},
			}
			cf := &fakeCloudflareClient{
				listDNSRecords: func(recordType, name string) ([]cloudflare.DNSRecord, error) {
					return nil, nil
				},
				createARecord: func(name, content string, proxied bool, ttl int) (*cloudflare.DNSRecord, error) {
					createdFQDN = name
					return &cloudflare.DNSRecord{}, nil
				},
			}
			m := newTestManager(cf)
			m.qmClient = qm

			_, err := m.SyncService(context.Background(), tt.serviceType, "")
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if createdFQDN != tt.expectedSubpart {
				t.Fatalf("expected %s, got %s", tt.expectedSubpart, createdFQDN)
			}
		})
	}
}

func TestSyncService_ProxiedServices(t *testing.T) {
	ip := "10.0.0.1"
	var proxiedValue bool
	qm := &fakeQuartermasterClient{
		response: &proto.ListHealthyNodesForDNSResponse{
			Nodes: []*proto.InfrastructureNode{{ExternalIp: &ip}},
		},
	}
	cf := &fakeCloudflareClient{
		listDNSRecords: func(recordType, name string) ([]cloudflare.DNSRecord, error) {
			return nil, nil
		},
		createARecord: func(name, content string, proxied bool, ttl int) (*cloudflare.DNSRecord, error) {
			proxiedValue = proxied
			return &cloudflare.DNSRecord{}, nil
		},
	}
	m := newTestManager(cf)
	m.qmClient = qm

	// "chartroom" is in proxy map, should be proxied
	_, err := m.SyncService(context.Background(), "chartroom", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !proxiedValue {
		t.Fatal("expected chartroom to be proxied")
	}

	// "edge-egress" is NOT in proxy map, should not be proxied
	proxiedValue = true // reset
	_, err = m.SyncService(context.Background(), "edge-egress", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if proxiedValue {
		t.Fatal("expected edge-egress to not be proxied")
	}
}

func TestLoadProxyServices_Default(t *testing.T) {
	// Unset the env var to test default behavior
	t.Setenv("NAVIGATOR_PROXY_SERVICES", "")
	proxy := loadProxyServices()
	if !proxy["chartroom"] || !proxy["website"] || !proxy["logbook"] {
		t.Fatalf("expected chartroom, website, logbook in defaults, got %v", proxy)
	}
	if proxy["edge-egress"] {
		t.Fatal("edge-egress should not be proxied by default")
	}
}

func TestLoadProxyServices_Custom(t *testing.T) {
	t.Setenv("NAVIGATOR_PROXY_SERVICES", "edge-egress, gateway, ")
	proxy := loadProxyServices()
	if !proxy["edge-egress"] || !proxy["gateway"] {
		t.Fatalf("expected edge-egress, gateway from env, got %v", proxy)
	}
	if proxy["chartroom"] {
		t.Fatal("chartroom should not be in custom proxy list")
	}
}

var _ logging.Logger = (*logrus.Logger)(nil)

func strPtr(s string) *string { return &s }

func TestClusterServiceFQDN(t *testing.T) {
	m := newTestManager(&fakeCloudflareClient{})

	tests := []struct {
		serviceType string
		rootDomain  string
		expected    string
	}{
		{"edge", "c1.example.com", "edge.c1.example.com"},
		{"edge-egress", "c1.example.com", "edge-egress.c1.example.com"},
		{"edge-ingest", "c1.example.com", "edge-ingest.c1.example.com"},
		{"foghorn", "c1.example.com", "foghorn.c1.example.com"},
		{"gateway", "c1.example.com", "bridge.c1.example.com"},
		{"bridge", "c1.example.com", "bridge.c1.example.com"},
		{"chartroom", "c1.example.com", "chartroom.c1.example.com"},
		{"website", "c1.example.com", "c1.example.com"},
		{"logbook", "c1.example.com", "logbook.c1.example.com"},
		{"steward", "c1.example.com", "steward.c1.example.com"},
	}

	for _, tt := range tests {
		t.Run(tt.serviceType, func(t *testing.T) {
			got := m.clusterServiceFQDN(tt.serviceType, tt.rootDomain)
			if got != tt.expected {
				t.Fatalf("clusterServiceFQDN(%q, %q) = %q, want %q",
					tt.serviceType, tt.rootDomain, got, tt.expected)
			}
		})
	}
}

func TestSyncServiceByCluster_EdgeEgressCreatesNodeRecords(t *testing.T) {
	ip := "10.0.0.1"
	var createdRecords []string

	qm := &fakeQuartermasterClient{
		clustersResponse: &proto.ListClustersResponse{
			Clusters: []*proto.InfrastructureCluster{
				{ClusterId: "cluster-abc", ClusterName: "test-cluster", IsActive: true},
			},
		},
		response: &proto.ListHealthyNodesForDNSResponse{
			Nodes: []*proto.InfrastructureNode{
				{NodeId: "node-1", ClusterId: "cluster-abc", ExternalIp: strPtr(ip)},
			},
		},
	}

	cf := &fakeCloudflareClient{
		createARecord: func(name, content string, proxied bool, ttl int) (*cloudflare.DNSRecord, error) {
			createdRecords = append(createdRecords, name)
			return &cloudflare.DNSRecord{Name: name, Content: content}, nil
		},
	}

	m := &DNSManager{
		cfClient:      cf,
		qmClient:      qm,
		logger:        logrus.New(),
		domain:        "example.com",
		proxy:         map[string]bool{},
		recordTTL:     60,
		lbTTL:         60,
		staleAge:      5 * time.Minute,
		monitorConfig: MonitorConfig{Interval: 60, Timeout: 5, Retries: 2},
		servicePorts:  map[string]int{"edge-egress": 18008},
	}

	partialErrors, err := m.SyncServiceByCluster(context.Background(), "edge-egress")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(partialErrors) > 0 {
		t.Fatalf("unexpected partial errors: %v", partialErrors)
	}

	hasNodeRecord := false
	for _, r := range createdRecords {
		if strings.HasPrefix(r, "edge-node-") {
			hasNodeRecord = true
		}
	}
	if !hasNodeRecord {
		t.Fatalf("expected edge node A record, got records: %v", createdRecords)
	}
}

func TestSyncServiceByCluster_NonEdgeSkipsNodeRecords(t *testing.T) {
	ip := "10.0.0.1"
	var createdRecords []string

	qm := &fakeQuartermasterClient{
		clustersResponse: &proto.ListClustersResponse{
			Clusters: []*proto.InfrastructureCluster{
				{ClusterId: "cluster-abc", ClusterName: "test", IsActive: true},
			},
		},
		response: &proto.ListHealthyNodesForDNSResponse{
			Nodes: []*proto.InfrastructureNode{
				{NodeId: "node-1", ClusterId: "cluster-abc", ExternalIp: strPtr(ip)},
			},
		},
	}

	cf := &fakeCloudflareClient{
		createARecord: func(name, content string, proxied bool, ttl int) (*cloudflare.DNSRecord, error) {
			createdRecords = append(createdRecords, name)
			return &cloudflare.DNSRecord{Name: name, Content: content}, nil
		},
	}

	m := &DNSManager{
		cfClient:      cf,
		qmClient:      qm,
		logger:        logrus.New(),
		domain:        "example.com",
		proxy:         map[string]bool{},
		recordTTL:     60,
		lbTTL:         60,
		staleAge:      5 * time.Minute,
		monitorConfig: MonitorConfig{Interval: 60, Timeout: 5, Retries: 2},
		servicePorts:  map[string]int{"foghorn": 18008},
	}

	_, err := m.SyncServiceByCluster(context.Background(), "foghorn")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	for _, r := range createdRecords {
		if strings.HasPrefix(r, "edge-") {
			t.Fatalf("foghorn should not create edge node records, but created: %s", r)
		}
	}
}

func TestSyncServiceByCluster_UsesClusterScopedPoolNames(t *testing.T) {
	ip1 := "10.0.0.1"
	ip2 := "10.0.0.2"
	ip3 := "10.0.1.1"
	ip4 := "10.0.1.2"

	qm := &fakeQuartermasterClient{
		clustersResponse: &proto.ListClustersResponse{
			Clusters: []*proto.InfrastructureCluster{
				{ClusterId: "cluster-1", ClusterName: "one", IsActive: true},
				{ClusterId: "cluster-2", ClusterName: "two", IsActive: true},
			},
		},
		response: &proto.ListHealthyNodesForDNSResponse{
			Nodes: []*proto.InfrastructureNode{
				{NodeId: "n1", ClusterId: "cluster-1", ExternalIp: strPtr(ip1)},
				{NodeId: "n2", ClusterId: "cluster-1", ExternalIp: strPtr(ip2)},
				{NodeId: "n3", ClusterId: "cluster-2", ExternalIp: strPtr(ip3)},
				{NodeId: "n4", ClusterId: "cluster-2", ExternalIp: strPtr(ip4)},
			},
		},
	}

	createdPools := []string{}
	var monitorPorts []int
	cf := &fakeCloudflareClient{
		listMonitors: func() ([]cloudflare.Monitor, error) {
			return nil, nil
		},
		createMonitor: func(monitor cloudflare.Monitor) (*cloudflare.Monitor, error) {
			monitorPorts = append(monitorPorts, monitor.Port)
			monitor.ID = "monitor"
			return &monitor, nil
		},
		listPools: func() ([]cloudflare.Pool, error) {
			return nil, nil
		},
		createPool: func(pool cloudflare.Pool) (*cloudflare.Pool, error) {
			pool.ID = pool.Name
			createdPools = append(createdPools, pool.Name)
			return &pool, nil
		},
		getPool: func(poolID string) (*cloudflare.Pool, error) {
			return &cloudflare.Pool{ID: poolID}, nil
		},
		listLoadBalancers: func() ([]cloudflare.LoadBalancer, error) {
			return nil, nil
		},
	}

	m := newTestManager(cf)
	m.qmClient = qm

	partialErrors, err := m.SyncServiceByCluster(context.Background(), "foghorn")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(partialErrors) > 0 {
		t.Fatalf("unexpected partial errors: %v", partialErrors)
	}

	if len(createdPools) != 2 {
		t.Fatalf("expected 2 pools, got %d (%v)", len(createdPools), createdPools)
	}

	for _, name := range createdPools {
		if strings.Contains(name, ".") {
			t.Fatalf("expected sanitized cluster-scoped pool name without dots, got %q", name)
		}
		if !strings.Contains(name, "example-com") {
			t.Fatalf("expected pool name to include cluster fqdn scope, got %q", name)
		}
	}

	if createdPools[0] == createdPools[1] {
		t.Fatalf("expected unique pool names per cluster, got %v", createdPools)
	}

	for _, port := range monitorPorts {
		if port != 18008 {
			t.Fatalf("expected health check monitor on port 18008 (foghorn), got %d", port)
		}
	}
}
