package control

import (
	"context"
	"strings"
	"testing"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
)

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
