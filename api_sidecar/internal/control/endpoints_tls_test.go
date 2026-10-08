package control

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"frameworks/api_sidecar/internal/appconfig/appconfigtest"
	sidecarcfg "frameworks/api_sidecar/internal/config"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
)

// startTLSFakeFoghorn serves the control stream over TLS with a certificate
// for name, the way a cell's Foghorn serves its public name, and returns the
// listen address and the CA file that verifies it.
func startTLSFakeFoghorn(t *testing.T, name string) (*fakeFoghorn, string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: name},
		DNSNames:              []string{name},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IsCA:                  true,
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	caFile := filepath.Join(t.TempDir(), "ca.crt")
	if err := os.WriteFile(caFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600); err != nil {
		t.Fatal(err)
	}
	cert := tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
	lis := listenLoopback(t)
	f := &fakeFoghorn{
		addr:       lis.Addr().String(),
		server:     grpc.NewServer(grpc.Creds(credentials.NewTLS(&tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{cert}}))),
		registered: make(chan time.Time, 16),
	}
	ipcpb.RegisterHelmsmanControlServer(f.server, f)
	go func() { _ = f.server.Serve(lis) }()
	t.Cleanup(f.server.Stop)
	return f, caFile
}

// runTLSControlDialer runs the real control client with TLS trust in caFile
// against addrs, resolving every name to ips, until the test ends.
func runTLSControlDialer(t *testing.T, caFile, serverName string, addrs, ips []string, lastDial string, servers ...*fakeFoghorn) {
	t.Helper()
	resetControlState(t)
	stateDir := t.TempDir()
	appconfigtest.Setenv(t, "HELMSMAN_STATE_DIR", stateDir)
	prevConfig := currentConfig
	currentConfig = &sidecarcfg.HelmsmanConfig{
		NodeID:            "edge-tls-name-test",
		StateDir:          stateDir,
		StorageLocalPath:  t.TempDir(),
		GRPCTLSCAPath:     caFile,
		GRPCTLSServerName: serverName,
	}
	logger := logging.NewLogger()
	stop := make(chan struct{})
	done := make(chan struct{})
	dialer := &controlDialer{
		addrs:    addrs,
		lookup:   func(context.Context, string) ([]string, error) { return ips, nil },
		connect:  func(endpoint controlEndpoint) error { return runClient(endpoint, logger) },
		logger:   logger,
		stop:     stop,
		lastDial: lastDial,
	}
	go func() {
		defer close(done)
		dialer.run()
	}()
	t.Cleanup(func() {
		close(stop)
		for _, f := range servers {
			f.server.Stop()
		}
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			t.Error("control dialer did not stop")
		}
		currentConfig = prevConfig
	})
}

func fakeFoghornPort(t *testing.T, f *fakeFoghorn) string {
	t.Helper()
	_, port, err := net.SplitHostPort(f.addr)
	if err != nil {
		t.Fatal(err)
	}
	return port
}

// Production edges name their cell's Foghorn by DNS name over TLS. The client
// dials each address the name resolves to, so the connection's authority must
// stay the certificate's name: a dial that sets the authority to name:port
// while the TLS credentials carry the bare name is refused by gRPC before it
// leaves the edge.
func TestControlClientConnectsToTLSFoghornByName(t *testing.T) {
	const name = "foghorn.media-test.frameworks.network"
	foghorn, caFile := startTLSFakeFoghorn(t, name)
	addr := net.JoinHostPort(name, fakeFoghornPort(t, foghorn))
	runTLSControlDialer(t, caFile, "", []string{addr}, []string{"127.0.0.1"}, "", foghorn)

	awaitRegistration(t, foghorn, 10*time.Second)
}

// The cell failover over TLS: the name resolves to an instance that refuses
// and one that serves, and the client reaches the serving one under the name.
func TestControlClientFailsOverBetweenTLSInstancesOfAName(t *testing.T) {
	const name = "foghorn.media-test.frameworks.network"
	foghorn, caFile := startTLSFakeFoghorn(t, name)
	port := fakeFoghornPort(t, foghorn)
	addr := net.JoinHostPort(name, port)
	// Nothing listens on 127.0.0.2 at this port. Starting after the serving
	// instance makes the refusing one the first dial.
	runTLSControlDialer(t, caFile, "", []string{addr}, []string{"127.0.0.1", "127.0.0.2"}, net.JoinHostPort("127.0.0.1", port), foghorn)

	awaitRegistration(t, foghorn, 10*time.Second)
}

// FOGHORN_GRPC_TLS_SERVER_NAME names the certificate when the configured name
// differs from it; the connection verifies against the override.
func TestControlClientUsesTLSServerNameOverrideWithAHostname(t *testing.T) {
	const name = "foghorn.media-test.frameworks.network"
	foghorn, caFile := startTLSFakeFoghorn(t, name)
	addr := net.JoinHostPort("control.media-test.frameworks.network", fakeFoghornPort(t, foghorn))
	runTLSControlDialer(t, caFile, name, []string{addr}, []string{"127.0.0.1"}, "", foghorn)

	awaitRegistration(t, foghorn, 10*time.Second)
}

// TLS still checks the name: a Foghorn whose certificate does not name the
// configured host is never registered with.
func TestControlClientRefusesTLSFoghornWithAnotherName(t *testing.T) {
	foghorn, caFile := startTLSFakeFoghorn(t, "foghorn.other.frameworks.network")
	addr := net.JoinHostPort("foghorn.media-test.frameworks.network", fakeFoghornPort(t, foghorn))
	runTLSControlDialer(t, caFile, "", []string{addr}, []string{"127.0.0.1"}, "", foghorn)

	select {
	case <-foghorn.registered:
		t.Fatal("registered with a Foghorn whose certificate names another host")
	case <-time.After(3 * time.Second):
	}
}
