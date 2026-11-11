package foghorn

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
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	foghornpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/foghorn"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/status"
)

type bootstrapTLSReplica struct {
	foghornpb.UnimplementedEdgeProvisioningServiceServer
	calls atomic.Int64
}

func (s *bootstrapTLSReplica) PreRegisterEdge(_ context.Context, r *foghornpb.PreRegisterEdgeRequest) (*foghornpb.PreRegisterEdgeResponse, error) {
	s.calls.Add(1)
	if r.GetEnrollmentToken() != "opaque-enrollment" || r.GetPreferredNodeId() != "fixed-node" {
		return nil, status.Error(codes.InvalidArgument, "identity changed across transport retry")
	}
	return &foghornpb.PreRegisterEdgeResponse{NodeId: "fixed-node", ClusterId: "private-cluster", FoghornGrpcAddr: "foghorn.private-cluster.example:18029"}, nil
}

func bootstrapTestPKI(t *testing.T) (string, func(string) tls.Certificate) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	root := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "bootstrap test public root"}, IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour)}
	der, err := x509.CreateCertificate(rand.Reader, root, root, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	root, err = x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	rootPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	return string(rootPEM), func(name string) tls.Certificate {
		t.Helper()
		leafKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		leaf := &x509.Certificate{SerialNumber: big.NewInt(time.Now().UnixNano()), DNSNames: []string{name}, KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour)}
		certDER, err := x509.CreateCertificate(rand.Reader, leaf, root, &leafKey.PublicKey, key)
		if err != nil {
			t.Fatal(err)
		}
		keyDER, err := x509.MarshalPKCS8PrivateKey(leafKey)
		if err != nil {
			t.Fatal(err)
		}
		cert, err := tls.X509KeyPair(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certDER}), pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}))
		if err != nil {
			t.Fatal(err)
		}
		return cert
	}
}

func startBootstrapTLSReplica(t *testing.T, cert tls.Certificate) (string, *bootstrapTLSReplica) {
	t.Helper()
	listener, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := grpc.NewServer(grpc.Creds(credentials.NewTLS(&tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12})))
	replica := &bootstrapTLSReplica{}
	foghornpb.RegisterEdgeProvisioningServiceServer(server, replica)
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() { server.Stop(); _ = listener.Close() })
	return listener.Addr().String(), replica
}

func TestBootstrapPublicPoolSkipsDeadAndWrongCertificateReplicas(t *testing.T) {
	root, issue := bootstrapTestPKI(t)
	bad, badReplica := startBootstrapTLSReplica(t, issue("wrong.example"))
	good, goodReplica := startBootstrapTLSReplica(t, issue("localhost"))
	dead, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	deadAddr := dead.Addr().String()
	_ = dead.Close()
	client, err := NewGRPCClient(GRPCConfig{GRPCAddr: strings.Join([]string{deadAddr, bad, good}, ","), UseTLS: true, CACertPEM: root, ServerName: "localhost", Bootstrap: true, Timeout: 5 * time.Second, Logger: logging.NewLogger()})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	response, err := client.PreRegisterEdge(t.Context(), &foghornpb.PreRegisterEdgeRequest{EnrollmentToken: "opaque-enrollment", PreferredNodeId: "fixed-node"})
	if err != nil {
		t.Fatalf("verified replica was stranded by failed members: %v", err)
	}
	if response.GetClusterId() != "private-cluster" || response.GetFoghornGrpcAddr() != "foghorn.private-cluster.example:18029" {
		t.Fatalf("private identity lost: %v", response)
	}
	if goodReplica.calls.Load() == 0 || badReplica.calls.Load() != 0 {
		t.Fatalf("wrong-certificate member accepted an RPC: good=%d bad=%d", goodReplica.calls.Load(), badReplica.calls.Load())
	}
}

func TestBootstrapAllWrongCertificatesHonorsCallerCancellation(t *testing.T) {
	root, issue := bootstrapTestPKI(t)
	addr, replica := startBootstrapTLSReplica(t, issue("wrong.example"))
	client, err := NewGRPCClient(GRPCConfig{GRPCAddr: addr, UseTLS: true, CACertPEM: root, ServerName: "localhost", Bootstrap: true, Timeout: 5 * time.Second, Logger: logging.NewLogger()})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 300*time.Millisecond)
	defer cancel()
	started := time.Now()
	if _, err := client.PreRegisterEdge(ctx, &foghornpb.PreRegisterEdgeRequest{EnrollmentToken: "opaque-enrollment", PreferredNodeId: "fixed-node"}); err == nil {
		t.Fatal("wrong certificate accepted")
	}
	if time.Since(started) > 2*time.Second || replica.calls.Load() != 0 {
		t.Fatal("invalid transport consumed unbounded time or accepted a request")
	}
}

type retryBootstrapClient struct {
	calls   int
	request *foghornpb.PreRegisterEdgeRequest
	t       *testing.T
}

func (c *retryBootstrapClient) PreRegisterEdge(ctx context.Context, req *foghornpb.PreRegisterEdgeRequest, _ ...grpc.CallOption) (*foghornpb.PreRegisterEdgeResponse, error) {
	c.calls++
	deadline, ok := ctx.Deadline()
	if !ok || time.Until(deadline) > 2*time.Second {
		c.t.Error("one replica can consume the entire enrollment operation")
	}
	if c.request == nil {
		c.request = req
	} else if c.request != req {
		c.t.Error("retry changed enrollment identity")
	}
	if c.calls == 1 {
		return nil, status.Error(codes.DeadlineExceeded, "one replica unavailable")
	}
	return &foghornpb.PreRegisterEdgeResponse{NodeId: req.GetPreferredNodeId()}, nil
}
func TestBootstrapRetriesReplicaDeadlineWithinOperation(t *testing.T) {
	edge := &retryBootstrapClient{t: t}
	client := &GRPCClient{edge: edge, bootstrap: true, timeout: 5 * time.Second}
	response, err := client.PreRegisterEdge(t.Context(), &foghornpb.PreRegisterEdgeRequest{PreferredNodeId: "fixed-node", EnrollmentToken: "opaque-enrollment"})
	if err != nil || response.GetNodeId() != "fixed-node" || edge.calls != 2 {
		t.Fatalf("one replica deadline stranded advisory enrollment: response=%v calls=%d err=%v", response, edge.calls, err)
	}
}
