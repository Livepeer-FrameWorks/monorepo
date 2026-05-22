package control

import (
	"context"
	"strings"
	"testing"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	pb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto"
	"google.golang.org/grpc"
)

type testInternalOnlyService interface {
	mustEmbedTestInternalOnlyService()
}

type testInternalOnlyServer struct{}

func (*testInternalOnlyServer) mustEmbedTestInternalOnlyService() {}

var testInternalOnlyServiceDesc = grpc.ServiceDesc{
	ServiceName: "test.InternalOnly",
	HandlerType: (*testInternalOnlyService)(nil),
}

func TestStartGRPCServers_NoTLSSource_FailsClosedByDefault(t *testing.T) {
	t.Setenv("GRPC_TLS_CERT_PATH", "")
	t.Setenv("GRPC_TLS_KEY_PATH", "")
	t.Setenv("GRPC_ALLOW_INSECURE", "")

	prevNavigator := navigatorClient
	navigatorClient = nil
	t.Cleanup(func() { navigatorClient = prevNavigator })

	_, err := StartGRPCServers(context.Background(), GRPCServerConfig{
		InternalBindAddr: "127.0.0.1:0",
		ExternalBindAddr: "127.0.0.1:0",
		Logger:           logging.NewLogger(),
	})
	if err == nil {
		t.Fatal("expected StartGRPCServers to fail without TLS source")
	}
	if !strings.Contains(err.Error(), "internal gRPC listener requires") {
		t.Fatalf("expected insecure-disabled error, got: %v", err)
	}
}

func TestStartGRPCServers_NoTLSSource_AllowsExplicitInsecureMode(t *testing.T) {
	t.Setenv("GRPC_TLS_CERT_PATH", "")
	t.Setenv("GRPC_TLS_KEY_PATH", "")
	t.Setenv("GRPC_ALLOW_INSECURE", "true")

	prevNavigator := navigatorClient
	navigatorClient = nil
	t.Cleanup(func() { navigatorClient = prevNavigator })

	servers, err := StartGRPCServers(context.Background(), GRPCServerConfig{
		InternalBindAddr: "127.0.0.1:0",
		ExternalBindAddr: "127.0.0.1:0",
		Logger:           logging.NewLogger(),
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	t.Cleanup(func() {
		servers.Internal.Stop()
		servers.External.Stop()
	})
}

func TestStartGRPCServers_ServiceSurfaceSplit(t *testing.T) {
	t.Setenv("GRPC_TLS_CERT_PATH", "")
	t.Setenv("GRPC_TLS_KEY_PATH", "")
	t.Setenv("GRPC_ALLOW_INSECURE", "true")

	prevNavigator := navigatorClient
	navigatorClient = nil
	t.Cleanup(func() { navigatorClient = prevNavigator })

	servers, err := StartGRPCServers(context.Background(), GRPCServerConfig{
		InternalBindAddr: "127.0.0.1:0",
		ExternalBindAddr: "127.0.0.1:0",
		Logger:           logging.NewLogger(),
		InternalRegistrars: []ServiceRegistrar{func(srv *grpc.Server) {
			srv.RegisterService(&testInternalOnlyServiceDesc, &testInternalOnlyServer{})
		}},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	t.Cleanup(func() {
		servers.Internal.Stop()
		servers.External.Stop()
	})

	internal := servers.Internal.GetServiceInfo()
	if _, ok := internal["test.InternalOnly"]; !ok {
		t.Fatal("expected internal listener to expose internal registrars")
	}
	if _, ok := internal[pb.HelmsmanControl_ServiceDesc.ServiceName]; ok {
		t.Fatal("internal listener must not expose HelmsmanControl")
	}
	if _, ok := internal["foghorn.EdgeProvisioningService"]; ok {
		t.Fatal("internal listener must not expose EdgeProvisioning")
	}

	external := servers.External.GetServiceInfo()
	if _, ok := external[pb.HelmsmanControl_ServiceDesc.ServiceName]; !ok {
		t.Fatal("expected external listener to expose HelmsmanControl")
	}
	if _, ok := external["foghorn.EdgeProvisioningService"]; !ok {
		t.Fatal("expected external listener to expose EdgeProvisioning")
	}
	if _, ok := external["test.InternalOnly"]; ok {
		t.Fatal("external listener must not expose internal registrars")
	}
}
