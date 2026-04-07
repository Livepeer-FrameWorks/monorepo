package control

import (
	"context"
	"strings"
	"testing"

	"frameworks/pkg/logging"
)

func TestStartGRPCServer_NoTLSSource_FailsClosedByDefault(t *testing.T) {
	t.Setenv("GRPC_TLS_CERT_PATH", "")
	t.Setenv("GRPC_TLS_KEY_PATH", "")
	t.Setenv("GRPC_ALLOW_INSECURE", "")

	prevNavigator := navigatorClient
	navigatorClient = nil
	t.Cleanup(func() { navigatorClient = prevNavigator })

	_, err := StartGRPCServer(context.Background(), GRPCServerConfig{Addr: "127.0.0.1:0", Logger: logging.NewLogger()})
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
	t.Setenv("GRPC_ALLOW_INSECURE", "true")

	prevNavigator := navigatorClient
	navigatorClient = nil
	t.Cleanup(func() { navigatorClient = prevNavigator })

	srv, err := StartGRPCServer(context.Background(), GRPCServerConfig{Addr: "127.0.0.1:0", Logger: logging.NewLogger()})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	t.Cleanup(srv.Stop)
}
