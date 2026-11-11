package cmd

import (
	"context"
	"net"
	"path/filepath"
	"strconv"
	"sync"
	"testing"

	fwcfg "frameworks/cli/internal/config"
	"frameworks/cli/internal/controlplane"
	"frameworks/cli/pkg/inventory"
	fhpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/foghorn"
	controlpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/foghorn_control"
	qmpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/quartermaster"
	"google.golang.org/grpc"
)

type privateControllerFixture struct {
	qmpb.UnimplementedClusterServiceServer
	mu   sync.Mutex
	cell string
}

func (f *privateControllerFixture) GetCluster(_ context.Context, request *qmpb.GetClusterRequest) (*qmpb.ClusterResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return &qmpb.ClusterResponse{Cluster: &qmpb.InfrastructureCluster{ClusterId: request.ClusterId, ControlCellId: f.cell}}, nil
}

type privateNodeHealthFixture struct {
	fhpb.UnimplementedNodeControlServiceServer
	cell string
}

func (f privateNodeHealthFixture) GetNodeHealth(_ context.Context, request *controlpb.GetNodeHealthRequest) (*controlpb.GetNodeHealthResponse, error) {
	return &controlpb.GetNodeHealthResponse{NodeId: request.NodeId, ClusterId: "private-cluster", TenantId: "private-owner", ProvisionedConfigCliVersion: f.cell}, nil
}

func TestPrivateNodeHealthFollowsCanonicalController(t *testing.T) {
	start := func(register func(*grpc.Server)) int {
		listener, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		s := grpc.NewServer()
		register(s)
		t.Cleanup(s.Stop)
		go func() { _ = s.Serve(listener) }()
		return listener.Addr().(*net.TCPAddr).Port
	}
	owner := &privateControllerFixture{cell: "media-us"}
	qmPort := start(func(s *grpc.Server) { qmpb.RegisterClusterServiceServer(s, owner) })
	manifest := restartFoghornCellManifest("eu", "us")
	manifest.Hosts["qm"] = inventory.Host{WireguardIP: "127.0.0.1"}
	manifest.Services["quartermaster"] = inventory.ServiceConfig{Enabled: true, Host: "qm", GRPCPort: qmPort}
	for _, cell := range []string{"eu", "us"} {
		port := start(func(s *grpc.Server) {
			fhpb.RegisterNodeControlServiceServer(s, privateNodeHealthFixture{cell: "media-" + cell})
		})
		host := manifest.Hosts["media-"+cell+"-1"]
		host.WireguardIP = "127.0.0.1"
		manifest.Hosts["media-"+cell+"-1"] = host
		svc := manifest.Services["foghorn-"+cell]
		svc.GRPCPort = port
		manifest.Services["foghorn-"+cell] = svc
	}
	ctxCfg := fwcfg.Context{Name: "platform", Persona: fwcfg.PersonaPlatform, AccessMode: fwcfg.AccessModeMesh, Auth: fwcfg.Auth{ServiceToken: "fixture-service"}}
	resolver := controlplane.NewResolverWithManifest(ctxCfg, manifest, filepath.Join(t.TempDir(), "cluster.yaml"), "")
	t.Cleanup(resolver.Close)
	for i, cell := range []string{"media-us", "media-eu"} {
		t.Run(strconv.Itoa(i)+cell, func(t *testing.T) {
			owner.mu.Lock()
			owner.cell = cell
			owner.mu.Unlock()
			client, err := clusterNodesFoghornClient(t.Context(), resolver, ctxCfg, "private-cluster")
			if err != nil {
				t.Fatalf("private controller lookup: %v", err)
			}
			t.Cleanup(func() { _ = client.Close() })
			health, _, err := client.GetNodeHealth(t.Context(), &controlpb.GetNodeHealthRequest{NodeId: "private-node"})
			if err != nil || health.GetProvisionedConfigCliVersion() != cell || health.GetNodeId() != "private-node" || health.GetTenantId() != "private-owner" {
				t.Fatalf("private identity/controller mismatch: health=%v error=%v", health, err)
			}
		})
	}
}
