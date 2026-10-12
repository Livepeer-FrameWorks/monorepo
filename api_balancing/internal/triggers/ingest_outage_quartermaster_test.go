package triggers

import (
	"context"
	"net"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"frameworks/api_balancing/internal/state"
	qmclient "github.com/Livepeer-FrameWorks/monorepo/pkg/clients/quartermaster"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	quartermasterpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/quartermaster"
	"github.com/sirupsen/logrus"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// hangingQuartermaster accepts connections and never answers Livepeer gateway
// discovery or node lookups: a stopped or partitioned Quartermaster behind a
// connection that stays open.
type hangingQuartermaster struct {
	quartermasterpb.UnimplementedBootstrapServiceServer
	quartermasterpb.UnimplementedNodeServiceServer
	discoveries atomic.Int32
	nodeLookups atomic.Int32
}

func (q *hangingQuartermaster) DiscoverServices(ctx context.Context, _ *quartermasterpb.ServiceDiscoveryRequest) (*quartermasterpb.ServiceDiscoveryResponse, error) {
	q.discoveries.Add(1)
	<-ctx.Done()
	return nil, status.FromContextError(ctx.Err()).Err()
}

func (q *hangingQuartermaster) GetNodeByLogicalName(ctx context.Context, _ *quartermasterpb.GetNodeByLogicalNameRequest) (*quartermasterpb.NodeResponse, error) {
	q.nodeLookups.Add(1)
	<-ctx.Done()
	return nil, status.FromContextError(ctx.Err()).Err()
}

// hangingQuartermasterClient is a real Quartermaster gRPC client, with its
// connection defaults and failsafe interceptor, on an IP-literal address.
func hangingQuartermasterClient(t *testing.T, server *hangingQuartermaster) *qmclient.GRPCClient {
	t.Helper()
	listener, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	grpcServer := grpc.NewServer()
	quartermasterpb.RegisterBootstrapServiceServer(grpcServer, server)
	quartermasterpb.RegisterNodeServiceServer(grpcServer, server)
	go func() { _ = grpcServer.Serve(listener) }()
	client, err := qmclient.NewGRPCClient(qmclient.GRPCConfig{GRPCAddr: listener.Addr().String(), Logger: logging.Logger(logrus.New()),
		AllowInsecure: true, ServiceToken: "test-quartermaster-service-token"})
	if err != nil {
		t.Fatalf("quartermaster client: %v", err)
	}
	t.Cleanup(func() {
		_ = client.Close()
		grpcServer.Stop()
	})
	return client
}

func assertLocalAVFallback(t *testing.T, p *Processor) {
	t.Helper()
	cached, ok := p.streamCache.Peek("process:stream-internal")
	if !ok {
		t.Fatal("admission cached no processing config for the stream")
	}
	processes, _ := cached.(string)
	if !strings.Contains(processes, `"process":"AV"`) || strings.Contains(processes, `"process":"Livepeer"`) {
		t.Fatalf("processing config = %s; want the local MistProcAV fallback", processes)
	}
}

// During a combined Commodore and Quartermaster outage the signed outage owner
// is admitted with the cell's own session mint inside the 3 s budget: Livepeer
// gateway discovery is one bounded attempt for the whole resolution and
// degrades to local MistProcAV.
func TestPushRewrite_QuartermasterAndCommodoreHang_OutageOwnerAdmittedWithinBudget(t *testing.T) {
	for _, tc := range []struct {
		name             string
		answerValidation bool
	}{
		{name: "validation hangs"},
		{name: "claim hangs", answerValidation: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			commodoreServer := &hangingCommodore{answerValidation: tc.answerValidation}
			commodoreClient := hangingCommodoreClient(t, commodoreServer)
			quartermaster := &hangingQuartermaster{}
			quartermasterClient := hangingQuartermasterClient(t, quartermaster)
			elapsed, p := admitOutageOwnerPushRewrite(t, gatewayTemplate, func(p *Processor) {
				p.commodoreClient = commodoreClient
				p.SetQuartermasterClient(quartermasterClient)
				// Origin and official are cluster-a; the process's own cluster
				// is a second candidate, as in a multi-cluster cell.
				p.clusterID = "cluster-local"
			})
			if elapsed > 2500*time.Millisecond {
				t.Fatalf("admission took %s of its 3s budget waiting on Commodore and Quartermaster", elapsed)
			}
			if commodoreServer.calls.Load() == 0 {
				t.Fatal("admission never asked Commodore; the test does not exercise the hanging call")
			}
			if calls := quartermaster.discoveries.Load(); calls != 1 {
				t.Fatalf("gateway discovery asked Quartermaster %d times; want one bounded attempt for the resolution", calls)
			}
			assertLocalAVFallback(t, p)
		})
	}
}

// Once Quartermaster is found unreachable, admissions in the backoff window
// resolve gateways without asking it and fall back at once.
func TestPushRewrite_QuartermasterOutageBackoffSkipsGatewayDiscovery(t *testing.T) {
	quartermaster := &hangingQuartermaster{}
	quartermasterClient := hangingQuartermasterClient(t, quartermaster)
	elapsed, p := admitOutageOwnerPushRewrite(t, gatewayTemplate, func(p *Processor) {
		p.SetQuartermasterClient(quartermasterClient)
		p.clusterID = "cluster-local"
		p.QuartermasterOutageBackoff().RecordUnreachable(status.Error(codes.Unavailable, "quartermaster is down"))
	})
	if calls := quartermaster.discoveries.Load() + quartermaster.nodeLookups.Load(); calls != 0 {
		t.Fatalf("admission asked Quartermaster %d times inside the outage backoff", calls)
	}
	if elapsed > time.Second {
		t.Fatalf("admission inside the Quartermaster outage backoff took %s", elapsed)
	}
	assertLocalAVFallback(t, p)
}

// A node whose attribution is not in node state is looked up in Quartermaster
// with one bounded attempt; a hanging Quartermaster costs about a second of the
// budget, not the trigger's whole wait.
func TestResolveNodeClusterID_HangingQuartermasterIsOneBoundedAttempt(t *testing.T) {
	quartermaster := &hangingQuartermaster{}
	sm := state.ResetDefaultManagerForTests()
	t.Cleanup(sm.Shutdown)
	p := NewProcessor(testLogger(), nil, nil, nil, nil)
	p.SetQuartermasterClient(hangingQuartermasterClient(t, quartermaster))
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	started := time.Now()
	if cluster := p.resolveNodeClusterIDWithContext(ctx, "unattributed-node"); cluster != "" {
		t.Fatalf("hanging Quartermaster attributed the node to %q", cluster)
	}
	if elapsed := time.Since(started); elapsed > 1500*time.Millisecond {
		t.Fatalf("node attribution waited %s on a hanging Quartermaster", elapsed)
	}
	if calls := quartermaster.nodeLookups.Load(); calls != 1 {
		t.Fatalf("node attribution asked Quartermaster %d times; want one attempt", calls)
	}
}

// Discovery that cannot be made answers from the last known gateways whatever
// their age, so a Quartermaster outage keeps Livepeer transcoding on the
// gateways it last assigned.
func TestApplyLivepeerBroadcasters_UnreachableQuartermasterUsesLastKnownGateways(t *testing.T) {
	disc := newFakeGatewayDiscoverer(map[string][]string{"cluster-x": {"gw.x.example.com"}})
	p := newGatewayProcessor(t, disc, "cluster-x")
	if got := p.ApplyLivepeerBroadcasters(gatewayTemplate, []string{"cluster-x"}); !strings.Contains(got, "gw.x.example.com") {
		t.Fatalf("initial resolution = %q", got)
	}
	p.gatewayMu.Lock()
	entry := p.gatewayURLs["cluster-x"]
	entry.resolvedAt = time.Now().Add(-time.Hour)
	p.gatewayURLs["cluster-x"] = entry
	p.gatewayMu.Unlock()
	p.QuartermasterOutageBackoff().RecordUnreachable(status.Error(codes.Unavailable, "quartermaster is down"))

	got := p.ApplyLivepeerBroadcasters(gatewayTemplate, []string{"cluster-x"})
	if !strings.Contains(got, "gw.x.example.com") || strings.Contains(got, `"process":"AV"`) {
		t.Fatalf("resolution during the outage = %q; want the last known gateway", got)
	}
	if calls := disc.callCount("cluster-x"); calls != 1 {
		t.Fatalf("discovery calls = %d; the outage backoff must answer without asking", calls)
	}
}
