package control

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"net"
	"sync"
	"testing"
	"time"

	"frameworks/api_balancing/internal/appconfig"
	foghornclient "github.com/Livepeer-FrameWorks/monorepo/pkg/clients/foghorn"
	navclient "github.com/Livepeer-FrameWorks/monorepo/pkg/clients/navigator"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	commonpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/common"
	dnspb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/dns"
	foghornpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/foghorn"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
	quartermasterpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/quartermaster"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/status"
)

func isolatePublicTLSState(t *testing.T) {
	t.Helper()
	oldCert, oldNavigator := serverCert.certs.Load(), navigatorClient
	oldCell, oldServed := localClusterID, servedClusters.Load()
	oldGet, oldList, oldValidator := getClusterFn, listTLSBundlesFn, validateBootstrapTokenFn
	oldRequests := certRefreshRequests
	certRefreshRequests = make(chan struct{}, 1)
	localClusterID = "media-us"
	servedClusters.Store(&sync.Map{})
	getClusterFn = func(_ context.Context, id string) (*quartermasterpb.InfrastructureCluster, error) {
		return &quartermasterpb.InfrastructureCluster{ClusterId: id}, nil
	}
	listTLSBundlesFn = func(context.Context, string, *commonpb.CursorPaginationRequest) (*quartermasterpb.ListTLSBundlesResponse, error) {
		return &quartermasterpb.ListTLSBundlesResponse{}, nil
	}
	t.Cleanup(func() {
		serverCert.certs.Store(oldCert)
		navigatorClient, localClusterID = oldNavigator, oldCell
		servedClusters.Store(oldServed)
		getClusterFn, listTLSBundlesFn, validateBootstrapTokenFn = oldGet, oldList, oldValidator
		certRefreshRequests = oldRequests
	})
}

func TestPublicCertRefreshCancellationStopsAuthorityReads(t *testing.T) {
	isolatePublicTLSState(t)
	useFoghornConfig(t, &appconfig.Foghorn{PlatformRootDomain: "example.test"})
	if err := serverCert.StoreBundles([]*ipcpb.TLSCertBundle{testTLSBundle(t, "cluster:media-us", "*.media-us.example.test")}); err != nil {
		t.Fatal(err)
	}
	navigatorClient = &navclient.Client{}
	started := make(chan struct{})
	listTLSBundlesFn = func(ctx context.Context, _ string, _ *commonpb.CursorPaginationRequest) (*quartermasterpb.ListTLSBundlesResponse, error) {
		close(started)
		<-ctx.Done()
		return nil, ctx.Err()
	}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() { StartCertRefreshLoop(ctx, time.Hour, logging.NewLogger()); close(done) }()
	t.Cleanup(func() { cancel(); <-done })
	requestCertRefresh()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("public certificate refresh did not start")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(300 * time.Millisecond):
		t.Fatal("public certificate worker ignored shutdown while reading authority")
	}
}

type bootstrapBundleFixture struct {
	dnspb.UnimplementedNavigatorServiceServer
	mu          sync.Mutex
	bundles     map[string]*ipcpb.TLSCertBundle
	unavailable bool
}

func (f *bootstrapBundleFixture) GetTLSBundle(_ context.Context, req *dnspb.GetTLSBundleRequest) (*dnspb.GetTLSBundleResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.unavailable {
		return nil, status.Error(codes.Unavailable, "issuer fixture unavailable")
	}
	bundle := f.bundles[req.GetBundleId()]
	if bundle == nil {
		return &dnspb.GetTLSBundleResponse{Found: false}, nil
	}
	return &dnspb.GetTLSBundleResponse{Found: true, CertPem: bundle.CertPem, KeyPem: bundle.KeyPem, Domains: []string{bundle.Domain}, ExpiresAt: bundle.ExpiresAt}, nil
}

// The issuer and inventory are fixtures; both gRPC transports, the external
// Foghorn builder, its refresh worker and the bootstrap client are production code.
func TestPublicBootstrapTLSConvergesWithoutRestart(t *testing.T) {
	isolatePublicTLSState(t)
	useFoghornConfig(t, &appconfig.Foghorn{PlatformRootDomain: "example.test"})
	cell := testTLSBundle(t, "cluster:media-us", "*.media-us.example.test")
	private := testTLSBundle(t, "cluster:customer-private", "*.customer-private.example.test")
	navigator := testTLSBundle(t, "navigator", "navigator.example.test")
	fixture := &bootstrapBundleFixture{bundles: map[string]*ipcpb.TLSCertBundle{cell.BundleId: cell}}
	certificate, err := tls.X509KeyPair([]byte(navigator.CertPem), []byte(navigator.KeyPem))
	if err != nil {
		t.Fatal(err)
	}
	listener, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	issuer := grpc.NewServer(grpc.Creds(credentials.NewTLS(&tls.Config{Certificates: []tls.Certificate{certificate}, MinVersion: tls.VersionTLS12})))
	dnspb.RegisterNavigatorServiceServer(issuer, fixture)
	go func() { _ = issuer.Serve(listener) }()
	t.Cleanup(issuer.Stop)
	navigatorClient, err = navclient.NewClient(navclient.Config{Addr: listener.Addr().String(), CACertPEM: navigator.CertPem, ServerName: "navigator.example.test", Logger: logging.NewLogger()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = navigatorClient.Close() })
	validateBootstrapTokenFn = func(context.Context, string) (*quartermasterpb.ValidateBootstrapTokenResponse, error) {
		return &quartermasterpb.ValidateBootstrapTokenResponse{Valid: true, Kind: "edge_node", ClusterId: "customer-private", BootstrapControlCellId: "media-us"}, nil
	}
	external, err := BuildExternalGRPCServer(t.Context(), GRPCServerConfig{Logger: logging.NewLogger()})
	if err != nil {
		t.Fatal(err)
	}
	public, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = external.Serve(public) }()
	t.Cleanup(external.Stop)
	client, err := foghornclient.NewGRPCClient(foghornclient.GRPCConfig{GRPCAddr: public.Addr().String(), UseTLS: true, CACertPEM: cell.CertPem, ServerName: "foghorn.media-us.example.test", Bootstrap: true, Timeout: time.Second, Logger: logging.NewLogger()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	req := &foghornpb.PreRegisterEdgeRequest{EnrollmentToken: "fixture-bound-token", PreferredNodeId: "stable-node"}
	if response, callErr := client.PreRegisterEdge(t.Context(), req); status.Code(callErr) != codes.Unavailable || response != nil {
		t.Fatalf("private identity was reported ready before issuance: response=%v err=%v", response, callErr)
	}
	roots := x509.NewCertPool()
	roots.AppendCertsFromPEM([]byte(private.CertPem))
	dialPrivate := func() error {
		ctx, cancel := context.WithTimeout(t.Context(), 500*time.Millisecond)
		defer cancel()
		conn, dialErr := (&tls.Dialer{Config: &tls.Config{RootCAs: roots, ServerName: "foghorn.customer-private.example.test", MinVersion: tls.VersionTLS12}}).DialContext(ctx, "tcp", public.Addr().String())
		if dialErr == nil {
			_ = conn.Close()
		}
		return dialErr
	}
	if handshakeErr := dialPrivate(); handshakeErr == nil {
		t.Fatal("unissued private SNI accepted a TLS handshake")
	}
	fixture.mu.Lock()
	fixture.bundles[private.BundleId] = private
	fixture.mu.Unlock()
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() { StartCertRefreshLoop(ctx, time.Hour, logging.NewLogger()); close(done) }()
	t.Cleanup(func() { cancel(); <-done })
	requestCertRefresh()
	deadline := time.Now().Add(3 * time.Second)
	for {
		if handshakeErr := dialPrivate(); handshakeErr == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("issued private certificate did not converge without restart")
		}
		time.Sleep(20 * time.Millisecond)
	}
	response, err := client.PreRegisterEdge(t.Context(), req)
	if err != nil || response.GetClusterId() != "customer-private" || response.GetNodeId() != "stable-node" || response.GetFoghornGrpcAddr() != "foghorn.customer-private.example.test:18029" {
		t.Fatalf("bootstrap lost persistent private identity: response=%v err=%v", response, err)
	}
	fixture.mu.Lock()
	fixture.unavailable = true
	fixture.mu.Unlock()
	refreshPublicTLSBundles(logging.NewLogger())
	if handshakeErr := dialPrivate(); handshakeErr != nil {
		t.Fatalf("transient issuer failure discarded last valid public certificate: %v", handshakeErr)
	}
	if _, lookupErr := serverCert.GetCertificate(&tls.ClientHelloInfo{ServerName: "foghorn.internal"}); lookupErr == nil {
		t.Fatal("mesh identity leaked into public listener")
	}
}
