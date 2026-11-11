package graph_test

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"net"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"frameworks/api_gateway/graph"
	"frameworks/api_gateway/graph/generated"
	"frameworks/api_gateway/internal/clients/clientstest"
	"frameworks/api_gateway/internal/resolvers"
	"github.com/99designs/gqlgen/graphql/handler"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/grpcutil"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	foghornpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/foghorn"
	quartermasterpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/quartermaster"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
)

type bootstrapPublicFixture struct {
	foghornpb.UnimplementedEdgeProvisioningServiceServer
	phase atomic.Int32
}

func (f *bootstrapPublicFixture) PreRegisterEdge(_ context.Context, req *foghornpb.PreRegisterEdgeRequest) (*foghornpb.PreRegisterEdgeResponse, error) {
	if req.GetEnrollmentToken() != "bound-enrollment" || req.GetPreferredNodeId() != "stable-node" {
		return nil, grpcutil.BootstrapPendingError(grpcutil.BootstrapControlCellChanged, time.Second)
	}
	switch f.phase.Load() {
	case 0:
		return nil, grpcutil.BootstrapPendingError(grpcutil.BootstrapPublicIdentityPending, 2*time.Second)
	case 1:
		return nil, grpcutil.BootstrapPendingError(grpcutil.BootstrapControlCellChanged, time.Second)
	default:
		return &foghornpb.PreRegisterEdgeResponse{NodeId: "stable-node", ClusterId: "private-customer", FoghornGrpcAddr: "foghorn.private-customer.example.test:18029", EdgeDomain: "edge-stable-node.private-customer.example.test"}, nil
	}
}

func TestBootstrapEdgePublicRendezvousGraphQL(t *testing.T) {
	// A subprocess isolates system-root initialization. Bridge receives no custom
	// CA, ServerName override or insecure flag; its normal system trust is tested.
	if os.Getenv("FRAMEWORKS_BOOTSTRAP_TLS_CHILD") != "1" {
		binary, err := os.Executable()
		if err != nil {
			t.Fatal(err)
		}
		cmd := exec.CommandContext(t.Context(), binary, "-test.run=^TestBootstrapEdgePublicRendezvousGraphQL$", "-test.v")
		cmd.Env = append(os.Environ(), "FRAMEWORKS_BOOTSTRAP_TLS_CHILD=1")
		if output, runErr := cmd.CombinedOutput(); runErr != nil {
			t.Fatalf("public bootstrap subprocess: %v\n%s", runErr, output)
		}
		return
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	leaf := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "bootstrap fixture"}, DNSNames: []string{"localhost"}, NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, err := x509.CreateCertificate(rand.Reader, leaf, leaf, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := tls.X509KeyPair(certPEM, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}))
	if err != nil {
		t.Fatal(err)
	}
	roots := filepath.Join(t.TempDir(), "roots.pem")
	if writeErr := os.WriteFile(roots, certPEM, 0600); writeErr != nil {
		t.Fatal(writeErr)
	}
	t.Setenv("SSL_CERT_FILE", roots)
	listener, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	fixture := &bootstrapPublicFixture{}
	server := grpc.NewServer(grpc.Creds(credentials.NewTLS(&tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12})), grpc.UnaryInterceptor(grpcutil.SanitizeUnaryServerInterceptor()))
	foghornpb.RegisterEdgeProvisioningServiceServer(server, fixture)
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(server.Stop)
	_, port, err := net.SplitHostPort(listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	publicAddr := net.JoinHostPort("localhost.", port)
	qm := &clientstest.FakeQuartermaster{ValidateBootstrapTokenFn: func(_ context.Context, token string) (*quartermasterpb.ValidateBootstrapTokenResponse, error) {
		if token != "bound-enrollment" {
			t.Error("Bridge changed enrollment credential")
		}
		return &quartermasterpb.ValidateBootstrapTokenResponse{Valid: true, Kind: "edge_node", ClusterId: "private-customer", FoghornGrpcAddr: "unissued-private.invalid:18029", BootstrapFoghornGrpcAddr: publicAddr}, nil
	}}
	clients := clientstest.Clients(clientstest.WithQuartermaster(qm))
	resolver := graph.NewResolver(clients, logging.NewLogger(), nil, resolvers.ResolverConfig{})
	t.Cleanup(func() { _ = resolver.Shutdown() })
	bridge := handler.NewDefaultServer(generated.NewExecutableSchema(generated.Config{Resolvers: resolver}))
	query := `{"query":"mutation { bootstrapEdge(input:{token:\"bound-enrollment\",preferredNodeId:\"stable-node\"}) { __typename ... on BootstrapEdgePending { clusterId phase retryAfterSeconds } ... on BootstrapEdgeResponse { clusterId nodeId foghornGrpcAddr } ... on ValidationError { message } } }"}`
	for phase, want := range []string{"public_identity", "control_assignment", "ready"} {
		fixture.phase.Store(int32(phase))
		req := httptest.NewRequestWithContext(t.Context(), "POST", "/graphql", strings.NewReader(query))
		req.Header.Set("Content-Type", "application/json")
		response := httptest.NewRecorder()
		bridge.ServeHTTP(response, req)
		var result struct {
			Data struct {
				Bootstrap map[string]any `json:"bootstrapEdge"`
			}
			Errors []json.RawMessage
		}
		if jsonErr := json.Unmarshal(response.Body.Bytes(), &result); jsonErr != nil {
			t.Fatal(jsonErr)
		}
		value := result.Data.Bootstrap
		if response.Code != 200 || len(result.Errors) != 0 || value["clusterId"] != "private-customer" {
			t.Fatalf("bootstrap public GraphQL result: %s", response.Body.String())
		}
		if want != "ready" {
			if value["__typename"] != "BootstrapEdgePending" || value["phase"] != want {
				t.Fatalf("typed retry phase lost over TLS/GraphQL: %s", response.Body.String())
			}
		} else if value["__typename"] != "BootstrapEdgeResponse" || value["nodeId"] != "stable-node" || value["foghornGrpcAddr"] != "foghorn.private-customer.example.test:18029" {
			t.Fatalf("ready private identity lost: %s", response.Body.String())
		}
	}
}
