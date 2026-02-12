package control

import (
	"strings"
	"testing"

	"frameworks/pkg/logging"
)

func TestStartGRPCServer_NoTLSSource_FailsClosedByDefault(t *testing.T) {
	t.Setenv("GRPC_TLS_CERT_PATH", "")
	t.Setenv("GRPC_TLS_KEY_PATH", "")
	t.Setenv("FOGHORN_ALLOW_INSECURE_CONTROL_GRPC", "")

	prevNavigator := navigatorClient
	navigatorClient = nil
	t.Cleanup(func() { navigatorClient = prevNavigator })

	_, err := StartGRPCServer(GRPCServerConfig{Addr: "127.0.0.1:0", Logger: logging.NewLogger()})
	if err == nil {
		t.Fatal("expected StartGRPCServer to fail without TLS source")
	}
	if !strings.Contains(err.Error(), "insecure control gRPC is disabled") {
		t.Fatalf("expected insecure-disabled error, got: %v", err)
	}
}

func TestStartGRPCServer_NoTLSSource_AllowsExplicitInsecureMode(t *testing.T) {
	t.Setenv("GRPC_TLS_CERT_PATH", "")
	t.Setenv("GRPC_TLS_KEY_PATH", "")
	t.Setenv("FOGHORN_ALLOW_INSECURE_CONTROL_GRPC", "true")

	prevNavigator := navigatorClient
	navigatorClient = nil
	t.Cleanup(func() { navigatorClient = prevNavigator })

	srv, err := StartGRPCServer(GRPCServerConfig{Addr: "127.0.0.1:0", Logger: logging.NewLogger()})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	t.Cleanup(srv.Stop)
}
