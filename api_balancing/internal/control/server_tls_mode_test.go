package control

import (
	"context"
	"strings"
	"testing"

	"frameworks/api_balancing/internal/appconfig"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
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

func useInsecureControlGRPC(t *testing.T) {
	t.Helper()
	insecureSettings := &appconfig.Foghorn{}
	insecureSettings.AllowInsecure = true
	useFoghornConfig(t, insecureSettings)

	prevNavigator := navigatorClient
	navigatorClient = nil
	t.Cleanup(func() { navigatorClient = prevNavigator })
}

func TestBuildInternalGRPCServer_NoTLSSource_FailsClosedByDefault(t *testing.T) {
	useFoghornConfig(t, &appconfig.Foghorn{})

	prevNavigator := navigatorClient
	navigatorClient = nil
	t.Cleanup(func() { navigatorClient = prevNavigator })

	srv, err := BuildInternalGRPCServer(context.Background(), GRPCServerConfig{
		InternalBindAddr: "127.0.0.1:0",
		Logger:           logging.NewLogger(),
	})
	if err == nil {
		srv.Stop()
		t.Fatal("expected the internal build to fail without a TLS source")
	}
	if !strings.Contains(err.Error(), "internal gRPC listener requires") {
		t.Fatalf("expected insecure-disabled error, got: %v", err)
	}
}

func TestBuildControlGRPCServers_NoTLSSource_AllowsExplicitInsecureMode(t *testing.T) {
	useInsecureControlGRPC(t)
	cfg := GRPCServerConfig{InternalBindAddr: "127.0.0.1:0", ExternalBindAddr: "127.0.0.1:0", Logger: logging.NewLogger()}

	internal, err := BuildInternalGRPCServer(context.Background(), cfg)
	if err != nil {
		t.Fatalf("internal build: %v", err)
	}
	t.Cleanup(internal.Stop)
	external, err := BuildExternalGRPCServer(context.Background(), cfg)
	if err != nil {
		t.Fatalf("external build: %v", err)
	}
	t.Cleanup(external.Stop)
}

func TestBuildControlGRPCServers_ServiceSurfaceSplit(t *testing.T) {
	useInsecureControlGRPC(t)
	cfg := GRPCServerConfig{
		InternalBindAddr: "127.0.0.1:0",
		ExternalBindAddr: "127.0.0.1:0",
		Logger:           logging.NewLogger(),
		InternalRegistrars: []ServiceRegistrar{func(srv *grpc.Server) {
			srv.RegisterService(&testInternalOnlyServiceDesc, &testInternalOnlyServer{})
		}},
	}

	internalSrv, err := BuildInternalGRPCServer(context.Background(), cfg)
	if err != nil {
		t.Fatalf("internal build: %v", err)
	}
	t.Cleanup(internalSrv.Stop)
	externalSrv, err := BuildExternalGRPCServer(context.Background(), cfg)
	if err != nil {
		t.Fatalf("external build: %v", err)
	}
	t.Cleanup(externalSrv.Stop)

	internal := internalSrv.GetServiceInfo()
	if _, ok := internal["test.InternalOnly"]; !ok {
		t.Fatal("expected internal listener to expose internal registrars")
	}
	if _, ok := internal[ipcpb.HelmsmanControl_ServiceDesc.ServiceName]; ok {
		t.Fatal("internal listener must not expose HelmsmanControl")
	}
	if _, ok := internal["foghorn.EdgeProvisioningService"]; ok {
		t.Fatal("internal listener must not expose EdgeProvisioning")
	}

	external := externalSrv.GetServiceInfo()
	if _, ok := external[ipcpb.HelmsmanControl_ServiceDesc.ServiceName]; !ok {
		t.Fatal("expected external listener to expose HelmsmanControl")
	}
	if _, ok := external["foghorn.EdgeProvisioningService"]; !ok {
		t.Fatal("expected external listener to expose EdgeProvisioning")
	}
	if _, ok := external["test.InternalOnly"]; ok {
		t.Fatal("external listener must not expose internal registrars")
	}
}
