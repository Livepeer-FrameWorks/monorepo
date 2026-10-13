package livepeer

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	obs "github.com/Livepeer-FrameWorks/monorepo/pkg/networkobservation"
	lp "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/livepeer_observer"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/restream"
	"github.com/ethereum/go-ethereum/accounts"
	"github.com/ethereum/go-ethereum/crypto"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"math/big"
	"net"
	"strconv"
	"testing"
	"time"
)

type orchTest interface{}

func TestProbeValidatesDiscoverySignatureBeforeDialing(t *testing.T) {
	key, err := crypto.HexToECDSA("0000000000000000000000000000000000000000000000000000000000000001")
	if err != nil {
		t.Fatal(err)
	}
	address := crypto.PubkeyToAddress(key.PublicKey)
	for _, tc := range []struct {
		name   string
		digest []byte
		valid  bool
	}{
		{"livepeer", accounts.TextHash(crypto.Keccak256([]byte(address.Hex()))), true},
		{"ordinary-personal-sign", accounts.TextHash([]byte(address.Hex())), false},
		{"missing-prefix", crypto.Keccak256([]byte(address.Hex())), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sig, signErr := crypto.Sign(tc.digest, key)
			if signErr != nil {
				t.Fatal(signErr)
			}
			sig[64] += 27
			_, probeErr := NewProbe(address.Hex(), hex.EncodeToString(sig), true, time.Second)
			if (probeErr == nil) != tc.valid {
				t.Fatalf("valid=%t, discovery signature error=%v", tc.valid, probeErr)
			}
		})
	}
}

func TestDiscoveryAdmissionFailuresAreNotDowntime(t *testing.T) {
	for _, tc := range []struct {
		message, kind string
		code          codes.Code
	}{
		{"insufficient sender reserve", "observer_payment", codes.Unknown},
		{"Invalid orchestrator request: orchestrator req sig check failed", "authentication", codes.Unknown},
		{"authentication failed: broadcaster not allowed", "authentication", codes.Unknown},
		{"denied", "authentication", codes.PermissionDenied},
		{"busy", "capacity", codes.ResourceExhausted},
	} {
		t.Run(tc.kind+tc.message, func(t *testing.T) {
			listener, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			defer listener.Close()
			server := grpc.NewServer()
			server.RegisterService(&grpc.ServiceDesc{ServiceName: "net.Orchestrator", HandlerType: (*orchTest)(nil), Methods: []grpc.MethodDesc{{MethodName: "GetOrchestrator", Handler: func(_ any, _ context.Context, _ func(any) error, _ grpc.UnaryServerInterceptor) (any, error) {
				return nil, status.Error(tc.code, tc.message)
			}}}}, struct{}{})
			go server.Serve(listener)
			defer server.Stop()
			_, allowed, _ := net.ParseCIDR("127.0.0.1/32")
			probe := Probe{Timeout: time.Second, destinationPolicy: restream.DestinationPolicy{AllowedCIDRs: []*net.IPNet{allowed}}}
			result := probe.Check(context.Background(), obs.Assignment{Deadline: time.Now().Add(time.Second), Endpoint: obs.Endpoint{IP: "127.0.0.1", Resource: obs.Resource{URI: "http://" + listener.Addr().String()}}})
			if result.Check.Qualifying || result.Check.Success || result.Check.FailureKind != tc.kind {
				t.Fatalf("admission rejection counted as downtime: %+v", result.Check)
			}
		})
	}
}

func TestProbeRejectsUnsafeOrUnpinnedDestinations(t *testing.T) {
	for _, ip := range []string{"", "not-an-ip", "127.0.0.1", "10.0.0.1", "169.254.169.254", "100.64.0.1", "0.0.0.0", "224.0.0.1", "::1", "fd00::1", "fe80::1", "::ffff:127.0.0.1", "64:ff9b::a00:1"} {
		t.Run(ip, func(t *testing.T) {
			p := Probe{Timeout: 5 * time.Millisecond}
			result := p.Check(context.Background(), obs.Assignment{Endpoint: obs.Endpoint{IP: ip, Resource: obs.Resource{URI: "http://unresolvable.invalid:8935"}}, Deadline: time.Now().Add(time.Second)})
			if result.Check.Qualifying || result.Check.Success || result.Check.ActualIP != "" {
				t.Fatalf("unsafe or unpinned destination was attempted as an orchestrator check: %+v", result.Check)
			}
			if result.Check.FailureKind != "invalid_endpoint" && result.Check.FailureKind != "endpoint_policy" {
				t.Fatalf("destination must be rejected before RPC: %+v", result.Check)
			}
		})
	}
}

func TestProbePinsIPAndPreservesAuthority(t *testing.T) {
	listener, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	authority := "unresolvable.invalid:" + strconv.Itoa(listener.Addr().(*net.TCPAddr).Port)
	_ = authority
	server := grpc.NewServer()
	address := make([]byte, 20)
	broadcasterKey, err := crypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	broadcasterAddress := crypto.PubkeyToAddress(broadcasterKey.PublicKey)
	signature, err := crypto.Sign(accounts.TextHash(crypto.Keccak256([]byte(broadcasterAddress.Hex()))), broadcasterKey)
	if err != nil {
		t.Fatal(err)
	}
	signature[64] += 27
	server.RegisterService(&grpc.ServiceDesc{ServiceName: "net.Orchestrator", HandlerType: (*orchTest)(nil), Methods: []grpc.MethodDesc{{MethodName: "GetOrchestrator", Handler: func(_ any, ctx context.Context, decode func(any) error, _ grpc.UnaryServerInterceptor) (any, error) {
		var req lp.DiscoveryRequest
		if decodeErr := decode(&req); decodeErr != nil {
			return nil, decodeErr
		}
		md, _ := metadata.FromIncomingContext(ctx)
		if md.Get(":authority")[0] != authority {
			t.Errorf("authority changed: %v", md)
		}
		if !req.IgnoreCapacityCheck {
			t.Error("discovery must ignore transient job capacity")
		}
		wireSignature := append([]byte(nil), req.Sig...)
		if len(wireSignature) != 65 || wireSignature[64] < 27 || wireSignature[64] > 28 {
			t.Error("invalid wire discovery signature")
			return nil, status.Error(codes.Unauthenticated, "bad signature")
		}
		wireSignature[64] -= 27
		publicKey, signatureErr := crypto.SigToPub(accounts.TextHash(crypto.Keccak256([]byte(broadcasterAddress.Hex()))), wireSignature)
		if signatureErr != nil || crypto.PubkeyToAddress(*publicKey) != broadcasterAddress || hex.EncodeToString(req.Address) != hex.EncodeToString(broadcasterAddress.Bytes()) {
			t.Error("discovery signature did not authenticate the broadcaster")
			return nil, status.Error(codes.Unauthenticated, "bad signature")
		}
		return &lp.DiscoveryResponse{Transcoder: "http://" + authority, Address: address, PriceInfo: &lp.DiscoveryPrice{PricePerUnit: 4, PixelsPerUnit: 10}}, nil
	}}}}, struct{}{})
	go server.Serve(listener)
	defer server.Stop()
	resource := obs.Resource{ID: "0x" + hex.EncodeToString(address), URI: "http://" + authority}
	endpoint, err := obs.NewEndpoint(resource, "127.0.0.1", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	_, loopback, err := net.ParseCIDR("127.0.0.1/32")
	if err != nil {
		t.Fatal(err)
	}
	p, err := NewProbe(broadcasterAddress.Hex(), hex.EncodeToString(signature), false, 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	p.destinationPolicy = restream.DestinationPolicy{AllowedCIDRs: []*net.IPNet{loopback}}
	result := p.Check(context.Background(), obs.Assignment{Endpoint: endpoint, ObserverID: "test", Deadline: time.Now().Add(time.Minute)})
	if !result.Check.Success || result.Check.ActualIP != "127.0.0.1" || result.Quote == nil {
		t.Fatalf("pinned discovery failed: %+v", result)
	}
}

// Livepeer orchestrators advertise https://host:port. The pinned dial must
// send the advertised host:port as :authority and the bare hostname as SNI,
// as go-livepeer's broadcaster does.
func TestProbeDialsTLSOrchestratorAtAdvertisedHostAndPort(t *testing.T) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{SerialNumber: big.NewInt(1), DNSNames: []string{"unresolvable.invalid"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour)}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	var sni string
	serverTLS := &tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}}, GetConfigForClient: func(hello *tls.ClientHelloInfo) (*tls.Config, error) {
		sni = hello.ServerName
		return nil, nil
	}}
	listener, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	authority := "unresolvable.invalid:" + strconv.Itoa(listener.Addr().(*net.TCPAddr).Port)
	address := make([]byte, 20)
	var gotAuthority string
	server := grpc.NewServer(grpc.Creds(credentials.NewTLS(serverTLS)))
	server.RegisterService(&grpc.ServiceDesc{ServiceName: "net.Orchestrator", HandlerType: (*orchTest)(nil), Methods: []grpc.MethodDesc{{MethodName: "GetOrchestrator", Handler: func(_ any, ctx context.Context, decode func(any) error, _ grpc.UnaryServerInterceptor) (any, error) {
		var req lp.DiscoveryRequest
		if decodeErr := decode(&req); decodeErr != nil {
			return nil, decodeErr
		}
		if md, ok := metadata.FromIncomingContext(ctx); ok && len(md.Get(":authority")) > 0 {
			gotAuthority = md.Get(":authority")[0]
		}
		return &lp.DiscoveryResponse{Transcoder: "https://" + authority, Address: address}, nil
	}}}}, struct{}{})
	go server.Serve(listener)
	defer server.Stop()
	endpoint, err := obs.NewEndpoint(obs.Resource{ID: "0x" + hex.EncodeToString(address), URI: "https://" + authority}, "127.0.0.1", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	probe := &Probe{SkipTLSVerify: true, Timeout: 2 * time.Second}
	if err := probe.AllowPrivateNetworks([]string{"127.0.0.0/8"}); err != nil {
		t.Fatal(err)
	}
	result := probe.Check(context.Background(), obs.Assignment{Endpoint: endpoint, ObserverID: "test", Deadline: time.Now().Add(time.Minute)})
	if !result.Check.Success || result.Check.ActualIP != "127.0.0.1" {
		t.Fatalf("TLS discovery of %s failed: %+v", authority, result.Check)
	}
	if gotAuthority != authority || sni != "unresolvable.invalid" {
		t.Fatalf(":authority %q and SNI %q, want %q and the bare hostname", gotAuthority, sni, authority)
	}
}

func TestAllowPrivateNetworksAdmitsOnlyTheConfiguredNetworks(t *testing.T) {
	listener, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	server := grpc.NewServer()
	server.RegisterService(&grpc.ServiceDesc{ServiceName: "net.Orchestrator", HandlerType: (*orchTest)(nil), Methods: []grpc.MethodDesc{{MethodName: "GetOrchestrator", Handler: func(_ any, _ context.Context, _ func(any) error, _ grpc.UnaryServerInterceptor) (any, error) {
		return nil, status.Error(codes.ResourceExhausted, "busy")
	}}}}, struct{}{})
	go server.Serve(listener)
	defer server.Stop()
	check := func(cidrs ...string) obs.Check {
		t.Helper()
		probe := &Probe{Timeout: time.Second}
		if err := probe.AllowPrivateNetworks(cidrs); err != nil {
			t.Fatal(err)
		}
		return probe.Check(context.Background(), obs.Assignment{Deadline: time.Now().Add(time.Second), Endpoint: obs.Endpoint{IP: "127.0.0.1", Resource: obs.Resource{URI: "http://" + listener.Addr().String()}}}).Check
	}
	if got := check(); got.FailureKind != "endpoint_policy" {
		t.Fatalf("loopback orchestrator dialed without an allowed network: %+v", got)
	}
	if got := check("10.231.1.0/24"); got.FailureKind != "endpoint_policy" {
		t.Fatalf("an unrelated private network admitted loopback: %+v", got)
	}
	if got := check(" 10.231.1.0/24", "127.0.0.0/8 "); got.FailureKind != "capacity" {
		t.Fatalf("orchestrator inside an allowed network was not dialed: %+v", got)
	}
	if err := (&Probe{}).AllowPrivateNetworks([]string{"10.0.0.0/33"}); err == nil {
		t.Fatal("an invalid network was accepted")
	}
}
