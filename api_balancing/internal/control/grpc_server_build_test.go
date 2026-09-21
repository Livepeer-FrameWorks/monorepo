package control

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"frameworks/api_balancing/internal/appconfig"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
)

// server.Run cancels a pending gRPC Build when shutdown starts, so the TLS file
// wait inside the internal build must end with its context instead of holding
// shutdown for the full two-minute wait.
func TestBuildInternalGRPCServerStopsWaitingForTLSWhenContextEnds(t *testing.T) {
	settings := &appconfig.Foghorn{}
	dir := t.TempDir()
	settings.CertPath = filepath.Join(dir, "missing.crt")
	settings.KeyPath = filepath.Join(dir, "missing.key")
	useFoghornConfig(t, settings)

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	start := time.Now()
	srv, err := BuildInternalGRPCServer(ctx, GRPCServerConfig{InternalBindAddr: "127.0.0.1:0", Logger: logging.NewLogger()})
	if err == nil {
		srv.Stop()
		t.Fatal("expected the build to fail while TLS files are missing")
	}
	if elapsed := time.Since(start); elapsed > 10*time.Second {
		t.Fatalf("build kept waiting %s after its context ended", elapsed)
	}
}

func TestBuildExternalGRPCServerRegistersEdgeServicesOnly(t *testing.T) {
	settings := &appconfig.Foghorn{}
	settings.AllowInsecure = true
	useFoghornConfig(t, settings)
	prevNavigator := navigatorClient
	navigatorClient = nil
	t.Cleanup(func() { navigatorClient = prevNavigator })

	srv, err := BuildExternalGRPCServer(context.Background(), GRPCServerConfig{ExternalBindAddr: "127.0.0.1:0", Logger: logging.NewLogger()})
	if err != nil {
		t.Fatalf("build external server: %v", err)
	}
	t.Cleanup(srv.Stop)
	services := srv.GetServiceInfo()
	if _, ok := services[ipcpb.HelmsmanControl_ServiceDesc.ServiceName]; !ok {
		t.Fatalf("external server lacks HelmsmanControl: %v", services)
	}
	if _, ok := services[testInternalOnlyServiceDesc.ServiceName]; ok {
		t.Fatal("external server registered an internal-only service")
	}
}
