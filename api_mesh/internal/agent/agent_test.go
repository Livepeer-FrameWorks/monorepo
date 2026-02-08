package agent

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"

	"frameworks/api_mesh/internal/wireguard"
	"frameworks/pkg/logging"
	pb "frameworks/pkg/proto"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type fakeMeshClient struct {
	syncResponses      []meshSyncResult
	bootstrapResponses []meshBootstrapResult
	syncRequests       []*pb.InfrastructureSyncRequest
	bootstrapRequests  []*pb.BootstrapInfrastructureNodeRequest
}

type meshSyncResult struct {
	resp *pb.InfrastructureSyncResponse
	err  error
}

type meshBootstrapResult struct {
	resp *pb.BootstrapInfrastructureNodeResponse
	err  error
}

func (f *fakeMeshClient) SyncMesh(_ context.Context, req *pb.InfrastructureSyncRequest) (*pb.InfrastructureSyncResponse, error) {
	f.syncRequests = append(f.syncRequests, req)
	if len(f.syncResponses) == 0 {
		return nil, status.Error(codes.Internal, "no sync response")
	}
	result := f.syncResponses[0]
	f.syncResponses = f.syncResponses[1:]
	return result.resp, result.err
}

func (f *fakeMeshClient) BootstrapInfrastructureNode(_ context.Context, req *pb.BootstrapInfrastructureNodeRequest) (*pb.BootstrapInfrastructureNodeResponse, error) {
	f.bootstrapRequests = append(f.bootstrapRequests, req)
	if len(f.bootstrapResponses) == 0 {
		return nil, status.Error(codes.Internal, "no bootstrap response")
	}
	result := f.bootstrapResponses[0]
	f.bootstrapResponses = f.bootstrapResponses[1:]
	return result.resp, result.err
}

type fakeWireguard struct {
	mu       sync.Mutex
	applyErr error
	pubKey   string
	privKey  string
	applied  []wireguard.Config
	sequence *[]string
}

func (f *fakeWireguard) Init() error {
	return nil
}

func (f *fakeWireguard) Apply(cfg wireguard.Config) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.applyErr != nil {
		return f.applyErr
	}
	if f.sequence != nil {
		*f.sequence = append(*f.sequence, "apply")
	}
	f.applied = append(f.applied, cfg)
	return nil
}

func (f *fakeWireguard) Close() error {
	return nil
}

func (f *fakeWireguard) GetPublicKey() (string, error) {
	return f.pubKey, nil
}

func (f *fakeWireguard) GetPrivateKey() (string, error) {
	return f.privKey, nil
}

type fakeDNS struct {
	mu        sync.Mutex
	updates   []map[string][]string
	updateErr error
	sequence  *[]string
}

func (f *fakeDNS) Start() {}

func (f *fakeDNS) Stop() {}

func (f *fakeDNS) UpdateRecords(records map[string][]string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.sequence != nil {
		*f.sequence = append(*f.sequence, "dns")
	}
	copied := make(map[string][]string, len(records))
	for key, values := range records {
		valueCopy := make([]string, len(values))
		copy(valueCopy, values)
		copied[key] = valueCopy
	}
	f.updates = append(f.updates, copied)
	return f.updateErr
}

func newTestMetrics() *Metrics {
	return &Metrics{
		SyncOperations: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "test_sync_operations_total",
				Help: "mesh sync operations",
			},
			[]string{"status"},
		),
		PeersConnected: prometheus.NewGaugeVec(
			prometheus.GaugeOpts{
				Name: "test_peers_connected",
				Help: "connected peers",
			},
			[]string{},
		),
	}
}

func newTestAgent(t *testing.T, client *fakeMeshClient, wg *fakeWireguard, dns *fakeDNS) *Agent {
	t.Helper()
	return &Agent{
		logger:       logging.NewLogger(),
		client:       client,
		wgManager:    wg,
		dnsServer:    dns,
		nodeID:       "node-1",
		nodeName:     "node-1",
		listenPort:   51820,
		syncTimeout:  2 * time.Second,
		syncInterval: 2 * time.Second,
		stopChan:     make(chan struct{}),
	}
}

func TestAgentSyncBootstrapJoinFlow(t *testing.T) {
	logger := logging.NewLogger()
	nodeIDPath := filepath.Join(t.TempDir(), "node_id")
	if err := os.WriteFile(nodeIDPath, []byte("node-old"), 0600); err != nil {
		t.Fatalf("write node id: %v", err)
	}

	mesh := &fakeMeshClient{
		syncResponses: []meshSyncResult{
			{err: status.Error(codes.NotFound, "missing")},
			{resp: &pb.InfrastructureSyncResponse{
				WireguardIp:   "10.0.0.2",
				WireguardPort: 51820,
				Peers: []*pb.InfrastructurePeer{
					{
						NodeName:   "node-a",
						PublicKey:  "pub-a",
						Endpoint:   "1.2.3.4:51820",
						AllowedIps: []string{"10.0.0.3/32"},
						KeepAlive:  25,
					},
				},
				ServiceEndpoints: map[string]*pb.ServiceEndpoints{
					"api": {Ips: []string{"10.0.0.9"}},
				},
			}},
		},
		bootstrapResponses: []meshBootstrapResult{
			{resp: &pb.BootstrapInfrastructureNodeResponse{NodeId: "node-new", ClusterId: "cluster-1"}},
		},
	}

	wg := &fakeWireguard{pubKey: "pub-self", privKey: "priv-self"}
	dnsService := &fakeDNS{}

	agent, err := New(Config{
		EnrollmentToken:  "token-123",
		NodeIDPath:       nodeIDPath,
		NodeName:         "privateer-1",
		NodeType:         "edge",
		Logger:           logger,
		MeshClient:       mesh,
		WireGuardManager: wg,
		DNSService:       dnsService,
	})
	if err != nil {
		t.Fatalf("new agent: %v", err)
	}

	agent.sync()

	if len(mesh.bootstrapRequests) != 1 {
		t.Fatalf("expected bootstrap request, got %d", len(mesh.bootstrapRequests))
	}
	if got := mesh.bootstrapRequests[0].GetNodeId(); got != "node-old" {
		t.Fatalf("expected bootstrap node_id node-old, got %q", got)
	}
	if got := agent.nodeID; got != "node-new" {
		t.Fatalf("expected node id updated to node-new, got %s", got)
	}
	if len(mesh.syncRequests) != 2 {
		t.Fatalf("expected two sync requests, got %d", len(mesh.syncRequests))
	}
	if mesh.syncRequests[1].NodeId != "node-new" {
		t.Fatalf("expected retry sync to use updated node id, got %s", mesh.syncRequests[1].NodeId)
	}
	if len(wg.applied) != 1 {
		t.Fatalf("expected wireguard apply once, got %d", len(wg.applied))
	}
	if wg.applied[0].Address != "10.0.0.2/32" {
		t.Fatalf("unexpected wireguard address %s", wg.applied[0].Address)
	}
	if len(dnsService.updates) != 1 {
		t.Fatalf("expected dns update once, got %d", len(dnsService.updates))
	}
	if got := dnsService.updates[0]["node-a"][0]; got != "10.0.0.3" {
		t.Fatalf("expected dns record for node-a, got %s", got)
	}
	if got := dnsService.updates[0]["api"][0]; got != "10.0.0.9" {
		t.Fatalf("expected dns record for api, got %s", got)
	}
}

func TestAgentSyncBootstrapInvalidToken(t *testing.T) {
	logger := logging.NewLogger()
	nodeIDPath := filepath.Join(t.TempDir(), "node_id")
	if err := os.WriteFile(nodeIDPath, []byte("node-x"), 0600); err != nil {
		t.Fatalf("write node id: %v", err)
	}

	mesh := &fakeMeshClient{
		syncResponses: []meshSyncResult{
			{err: status.Error(codes.NotFound, "missing")},
		},
		bootstrapResponses: []meshBootstrapResult{
			{err: status.Error(codes.PermissionDenied, "invalid token")},
		},
	}

	wg := &fakeWireguard{pubKey: "pub-self", privKey: "priv-self"}
	dnsService := &fakeDNS{}

	agent, err := New(Config{
		EnrollmentToken:  "bad-token",
		NodeIDPath:       nodeIDPath,
		NodeName:         "privateer-2",
		Logger:           logger,
		MeshClient:       mesh,
		WireGuardManager: wg,
		DNSService:       dnsService,
	})
	if err != nil {
		t.Fatalf("new agent: %v", err)
	}

	agent.sync()

	if len(mesh.syncRequests) != 1 {
		t.Fatalf("expected one sync request, got %d", len(mesh.syncRequests))
	}
	if len(mesh.bootstrapRequests) != 1 {
		t.Fatalf("expected one bootstrap request, got %d", len(mesh.bootstrapRequests))
	}
	if len(wg.applied) != 0 {
		t.Fatalf("expected no wireguard apply, got %d", len(wg.applied))
	}
}

func TestAgentSyncRevokedWithoutToken(t *testing.T) {
	logger := logging.NewLogger()
	nodeIDPath := filepath.Join(t.TempDir(), "node_id")
	if err := os.WriteFile(nodeIDPath, []byte("node-y"), 0600); err != nil {
		t.Fatalf("write node id: %v", err)
	}

	mesh := &fakeMeshClient{
		syncResponses: []meshSyncResult{
			{err: status.Error(codes.NotFound, "revoked")},
		},
	}

	wg := &fakeWireguard{pubKey: "pub-self", privKey: "priv-self"}
	dnsService := &fakeDNS{}

	agent, err := New(Config{
		NodeIDPath:       nodeIDPath,
		NodeName:         "privateer-3",
		Logger:           logger,
		MeshClient:       mesh,
		WireGuardManager: wg,
		DNSService:       dnsService,
	})
	if err != nil {
		t.Fatalf("new agent: %v", err)
	}

	agent.sync()

	if len(mesh.bootstrapRequests) != 0 {
		t.Fatalf("expected no bootstrap request, got %d", len(mesh.bootstrapRequests))
	}
	if len(wg.applied) != 0 {
		t.Fatalf("expected no wireguard apply, got %d", len(wg.applied))
	}
	if got := agent.consecutiveFails.Load(); got != 1 {
		t.Fatalf("expected consecutiveFails 1, got %d", got)
	}
}

func TestLoadOrGenerateNodeID(t *testing.T) {
	logger := logging.NewLogger()
	root := t.TempDir()

	t.Run("load existing", func(t *testing.T) {
		nodeIDPath := filepath.Join(root, "existing")
		if err := os.WriteFile(nodeIDPath, []byte("node-existing"), 0600); err != nil {
			t.Fatalf("write node id: %v", err)
		}

		got := loadOrGenerateNodeID(nodeIDPath, logger)
		if got != "node-existing" {
			t.Fatalf("expected node-existing, got %s", got)
		}
	})

	t.Run("generate new", func(t *testing.T) {
		nodeIDPath := filepath.Join(root, "generated", "node_id")
		got := loadOrGenerateNodeID(nodeIDPath, logger)
		if got == "" {
			t.Fatal("expected generated node id")
		}
		data, err := os.ReadFile(nodeIDPath)
		if err != nil {
			t.Fatalf("read node id: %v", err)
		}
		if string(data) != got {
			t.Fatalf("expected node id persisted, got %s", string(data))
		}
	})
}

func TestAgentSyncRetryDoesNotApplyOnFailure(t *testing.T) {
	logger := logging.NewLogger()
	nodeIDPath := filepath.Join(t.TempDir(), "node_id")
	if err := os.WriteFile(nodeIDPath, []byte("node-retry"), 0600); err != nil {
		t.Fatalf("write node id: %v", err)
	}

	mesh := &fakeMeshClient{
		syncResponses: []meshSyncResult{
			{err: status.Error(codes.NotFound, "missing")},
			{err: status.Error(codes.Unavailable, "temporary")},
		},
		bootstrapResponses: []meshBootstrapResult{
			{resp: &pb.BootstrapInfrastructureNodeResponse{NodeId: "node-retry", ClusterId: "cluster-1"}},
		},
	}

	wg := &fakeWireguard{pubKey: "pub-self", privKey: "priv-self"}
	dnsService := &fakeDNS{}

	agent, err := New(Config{
		EnrollmentToken:  "token-456",
		NodeIDPath:       nodeIDPath,
		NodeName:         "privateer-4",
		Logger:           logger,
		MeshClient:       mesh,
		WireGuardManager: wg,
		DNSService:       dnsService,
	})
	if err != nil {
		t.Fatalf("new agent: %v", err)
	}

	agent.sync()

	if len(wg.applied) != 0 {
		t.Fatalf("expected no wireguard apply on retry failure, got %d", len(wg.applied))
	}
	if got := agent.consecutiveFails.Load(); got != 1 {
		t.Fatalf("expected consecutiveFails 1, got %d", got)
	}
}

func TestSyncAppliesConfigBeforeDNSUpdate(t *testing.T) {
	resp := &pb.InfrastructureSyncResponse{
		WireguardIp:   "10.200.0.5",
		WireguardPort: 51820,
		Peers: []*pb.InfrastructurePeer{
			{
				PublicKey:  "peer-1",
				Endpoint:   "10.0.0.1:51820",
				AllowedIps: []string{"10.0.0.2/32"},
				KeepAlive:  25,
				NodeName:   "edge-1",
			},
		},
		ServiceEndpoints: map[string]*pb.ServiceEndpoints{
			"router": {Ips: []string{"10.0.0.10"}},
		},
	}

	client := &fakeMeshClient{syncResponses: []meshSyncResult{{resp: resp}}}
	sequence := make([]string, 0, 2)
	wg := &fakeWireguard{
		pubKey:   "public-key",
		privKey:  "private-key",
		sequence: &sequence,
	}
	dns := &fakeDNS{sequence: &sequence}
	agent := newTestAgent(t, client, wg, dns)

	agent.sync()

	if !reflect.DeepEqual(sequence, []string{"apply", "dns"}) {
		t.Fatalf("expected apply then dns sequence, got %v", sequence)
	}
	if len(wg.applied) != 1 {
		t.Fatalf("expected one apply call, got %d", len(wg.applied))
	}
	if len(dns.updates) != 1 {
		t.Fatalf("expected one dns update, got %d", len(dns.updates))
	}

	dnsRecords := dns.updates[0]
	expectedIP := "10.0.0.2"
	if got := dnsRecords["edge-1"]; len(got) != 1 || got[0] != expectedIP {
		t.Fatalf("expected dns record for edge-1 to be %s, got %v", expectedIP, got)
	}
}

func TestSyncRollsBackWireGuardOnDNSFailure(t *testing.T) {
	initialResp := &pb.InfrastructureSyncResponse{
		WireguardIp:   "10.200.0.5",
		WireguardPort: 51820,
		Peers: []*pb.InfrastructurePeer{
			{
				PublicKey:  "peer-1",
				Endpoint:   "10.0.0.1:51820",
				AllowedIps: []string{"10.0.0.2/32"},
				KeepAlive:  25,
				NodeName:   "edge-1",
			},
		},
	}
	rotatedResp := &pb.InfrastructureSyncResponse{
		WireguardIp:   "10.200.0.6",
		WireguardPort: 51821,
		Peers: []*pb.InfrastructurePeer{
			{
				PublicKey:  "peer-2",
				Endpoint:   "10.0.0.2:51820",
				AllowedIps: []string{"10.0.0.3/32"},
				KeepAlive:  25,
				NodeName:   "edge-2",
			},
		},
	}

	client := &fakeMeshClient{syncResponses: []meshSyncResult{
		{resp: initialResp},
		{resp: rotatedResp},
	}}
	wg := &fakeWireguard{
		pubKey:  "public-key",
		privKey: "private-key",
	}
	dns := &fakeDNS{}
	agent := newTestAgent(t, client, wg, dns)

	agent.sync()

	dns.updateErr = errors.New("dns update failed")
	agent.sync()

	if len(wg.applied) != 3 {
		t.Fatalf("expected three apply calls (initial, rotated, rollback), got %d", len(wg.applied))
	}

	rolledBack := wg.applied[2]
	expected := wg.applied[0]
	if !reflect.DeepEqual(rolledBack, expected) {
		t.Fatalf("expected rollback to apply previous config, got %+v vs %+v", rolledBack, expected)
	}

	lastCfg := agent.getLastAppliedConfig()
	if lastCfg == nil || !reflect.DeepEqual(*lastCfg, expected) {
		t.Fatalf("expected last applied config to remain initial config, got %+v", lastCfg)
	}

	if agent.consecutiveFails.Load() != 1 {
		t.Fatalf("expected consecutive failure count to be 1, got %d", agent.consecutiveFails.Load())
	}
}

func TestAgentSyncTimeoutRecordsFailure(t *testing.T) {
	client := &fakeMeshClient{
		syncResponses: []meshSyncResult{
			{err: context.DeadlineExceeded},
		},
	}
	wg := &fakeWireguard{pubKey: "pub", privKey: "priv"}
	dns := &fakeDNS{}
	metrics := newTestMetrics()
	agent := newTestAgent(t, client, wg, dns)
	agent.metrics = metrics

	agent.sync()

	if got := testutil.ToFloat64(metrics.SyncOperations.WithLabelValues("failed")); got != 1 {
		t.Fatalf("expected failed sync counter to be 1, got %v", got)
	}
	if agent.consecutiveFails.Load() != 1 {
		t.Fatalf("expected consecutive failures to be 1, got %d", agent.consecutiveFails.Load())
	}
	if agent.lastSyncSuccess.Load() != 0 {
		t.Fatalf("expected last sync success to be 0, got %d", agent.lastSyncSuccess.Load())
	}
}

func TestAgentSyncReconcilesStaleState(t *testing.T) {
	client := &fakeMeshClient{
		syncResponses: []meshSyncResult{
			{resp: &pb.InfrastructureSyncResponse{
				WireguardIp:   "10.0.0.10",
				WireguardPort: 51820,
				Peers: []*pb.InfrastructurePeer{
					{
						PublicKey:  "peer-key",
						Endpoint:   "1.2.3.4:51820",
						AllowedIps: []string{"10.0.0.2/32"},
						KeepAlive:  25,
						NodeName:   "peer-one",
					},
				},
				ServiceEndpoints: map[string]*pb.ServiceEndpoints{
					"metrics": {Ips: []string{"10.0.0.5"}},
				},
			}},
			{resp: &pb.InfrastructureSyncResponse{
				WireguardIp:   "10.0.0.10",
				WireguardPort: 51820,
			}},
		},
	}
	wg := &fakeWireguard{pubKey: "pub", privKey: "priv"}
	dns := &fakeDNS{}
	metrics := newTestMetrics()
	agent := newTestAgent(t, client, wg, dns)
	agent.metrics = metrics

	agent.sync()
	agent.sync()

	if len(dns.updates) != 2 {
		t.Fatalf("expected dns records updated twice, got %d", len(dns.updates))
	}
	if len(dns.updates[1]) != 0 {
		t.Fatalf("expected second dns update to clear records, got %v", dns.updates[1])
	}
	if got := testutil.ToFloat64(metrics.PeersConnected.WithLabelValues()); got != 0 {
		t.Fatalf("expected peers connected to be 0 after reconciliation, got %v", got)
	}
	if len(wg.applied) != 2 {
		t.Fatalf("expected wireguard apply to be called twice, got %d", len(wg.applied))
	}
	if len(wg.applied[1].Peers) != 0 {
		t.Fatalf("expected wireguard config peers to be empty after reconciliation, got %v", wg.applied[1].Peers)
	}
}

func TestAgentSyncApplyFailurePropagates(t *testing.T) {
	client := &fakeMeshClient{
		syncResponses: []meshSyncResult{
			{resp: &pb.InfrastructureSyncResponse{
				WireguardIp:   "10.0.0.10",
				WireguardPort: 51820,
			}},
		},
	}
	wg := &fakeWireguard{pubKey: "pub", privKey: "priv", applyErr: errors.New("apply failed")}
	dns := &fakeDNS{}
	metrics := newTestMetrics()
	agent := newTestAgent(t, client, wg, dns)
	agent.metrics = metrics

	agent.sync()

	if got := testutil.ToFloat64(metrics.SyncOperations.WithLabelValues("failed")); got != 1 {
		t.Fatalf("expected failed sync counter to be 1, got %v", got)
	}
}
