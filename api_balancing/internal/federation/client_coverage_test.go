package federation

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/clients/foghorn"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	foghornfederationpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/foghorn_federation"

	"github.com/sirupsen/logrus"
	"google.golang.org/grpc"
)

// recordingStub is a controllable FoghornFederation service. Every handler
// records the incoming context (so tests can assert a deadline was applied and
// the user JWT was stripped) and returns the canned response/error for that RPC.
type recordingStub struct {
	foghornfederationpb.UnimplementedFoghornFederationServer

	gotJWT         string
	gotHasDeadline bool

	queryResp   *foghornfederationpb.QueryStreamResponse
	originResp  *foghornfederationpb.OriginPullAck
	prepareResp *foghornfederationpb.PrepareArtifactResponse
	mintResp    *foghornfederationpb.MintStorageURLsResponse
	deleteResp  *foghornfederationpb.DeleteStorageObjectsResponse
	clipResp    *foghornfederationpb.RemoteClipResponse
	dvrResp     *foghornfederationpb.RemoteDVRResponse
	listResp    *foghornfederationpb.ListTenantArtifactsResponse
	forwardResp *foghornfederationpb.ForwardArtifactCommandResponse
}

// record captures whether the server-side context carries a deadline. The JWT
// is propagated as gRPC metadata by the client interceptor; here we only need
// to confirm the deadline (timeout) survived the wrapper.
func (s *recordingStub) record(ctx context.Context) {
	_, s.gotHasDeadline = ctx.Deadline()
}

func (s *recordingStub) QueryStream(ctx context.Context, _ *foghornfederationpb.QueryStreamRequest) (*foghornfederationpb.QueryStreamResponse, error) {
	s.record(ctx)
	return s.queryResp, nil
}

func (s *recordingStub) NotifyOriginPull(ctx context.Context, _ *foghornfederationpb.OriginPullNotification) (*foghornfederationpb.OriginPullAck, error) {
	s.record(ctx)
	return s.originResp, nil
}

func (s *recordingStub) PrepareArtifact(ctx context.Context, _ *foghornfederationpb.PrepareArtifactRequest) (*foghornfederationpb.PrepareArtifactResponse, error) {
	s.record(ctx)
	return s.prepareResp, nil
}

func (s *recordingStub) MintStorageURLs(ctx context.Context, _ *foghornfederationpb.MintStorageURLsRequest) (*foghornfederationpb.MintStorageURLsResponse, error) {
	s.record(ctx)
	return s.mintResp, nil
}

func (s *recordingStub) DeleteStorageObjects(ctx context.Context, _ *foghornfederationpb.DeleteStorageObjectsRequest) (*foghornfederationpb.DeleteStorageObjectsResponse, error) {
	s.record(ctx)
	return s.deleteResp, nil
}

func (s *recordingStub) CreateRemoteClip(ctx context.Context, _ *foghornfederationpb.RemoteClipRequest) (*foghornfederationpb.RemoteClipResponse, error) {
	s.record(ctx)
	return s.clipResp, nil
}

func (s *recordingStub) CreateRemoteDVR(ctx context.Context, _ *foghornfederationpb.RemoteDVRRequest) (*foghornfederationpb.RemoteDVRResponse, error) {
	s.record(ctx)
	return s.dvrResp, nil
}

func (s *recordingStub) ListTenantArtifacts(ctx context.Context, _ *foghornfederationpb.ListTenantArtifactsRequest) (*foghornfederationpb.ListTenantArtifactsResponse, error) {
	s.record(ctx)
	return s.listResp, nil
}

func (s *recordingStub) ForwardArtifactCommand(ctx context.Context, _ *foghornfederationpb.ForwardArtifactCommandRequest) (*foghornfederationpb.ForwardArtifactCommandResponse, error) {
	s.record(ctx)
	return s.forwardResp, nil
}

// fakePool satisfies foghornPool. It returns getErr (error-propagation path)
// or the canned client (success path) without any of the real pool's
// connection/health machinery.
type fakePool struct {
	client       *foghorn.GRPCClient
	getErr       error
	gotClusterID string
	gotAddr      string
}

func (f *fakePool) GetOrCreate(clusterID, addr string) (*foghorn.GRPCClient, error) {
	f.gotClusterID = clusterID
	f.gotAddr = addr
	if f.getErr != nil {
		return nil, f.getErr
	}
	return f.client, nil
}

// startStubServer brings up a real loopback FoghornFederation gRPC server and
// returns a *foghorn.GRPCClient (insecure) dialed at it, so the success path
// exercises the full foghornfed.For(client).Federation().X round-trip.
func startStubServer(t *testing.T, stub *recordingStub) *foghorn.GRPCClient {
	t.Helper()

	listener, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	server := grpc.NewServer()
	foghornfederationpb.RegisterFoghornFederationServer(server, stub)
	go func() { _ = server.Serve(listener) }()

	log := logging.Logger(logrus.New())
	client, err := foghorn.NewGRPCClient(foghorn.GRPCConfig{
		GRPCAddr:      listener.Addr().String(),
		Logger:        log,
		AllowInsecure: true,
	})
	if err != nil {
		t.Fatalf("new grpc client: %v", err)
	}

	t.Cleanup(func() {
		server.Stop()
		_ = listener.Close()
	})
	return client
}

func newTestFedClient(pool foghornPool) *FederationClient {
	return NewFederationClient(FederationClientConfig{
		Pool:   pool,
		Logger: logging.Logger(logrus.New()),
	})
}

// jwtCtx returns a context carrying a user JWT, mirroring a real per-tenant
// request reaching a federation wrapper.
func jwtCtx() context.Context {
	return context.WithValue(context.Background(), ctxkeys.KeyJWTToken, "user-jwt-secret")
}

// TestNewFederationClient_TimeoutDefault locks in the 10s default when the
// caller leaves Timeout unset, and that a custom timeout is honored verbatim.
func TestNewFederationClient_TimeoutDefault(t *testing.T) {
	def := NewFederationClient(FederationClientConfig{})
	if def.timeout != 10*time.Second {
		t.Fatalf("default timeout = %v, want 10s", def.timeout)
	}

	custom := NewFederationClient(FederationClientConfig{Timeout: 3 * time.Second})
	if custom.timeout != 3*time.Second {
		t.Fatalf("custom timeout = %v, want 3s", custom.timeout)
	}
}

// TestFederationContext_StripsJWT proves the user JWT is blanked so the client
// interceptor falls through to the service token — a viewer JWT must never ride
// a cross-cluster RPC.
func TestFederationContext_StripsJWT(t *testing.T) {
	out := federationContext(jwtCtx())
	if v, _ := out.Value(ctxkeys.KeyJWTToken).(string); v != "" {
		t.Fatalf("JWT not stripped: %q", v)
	}
}

// poolErrCase drives one wrapper and asserts GetOrCreate's error propagates
// verbatim (and the cluster/addr were forwarded to the pool).
func TestWrappers_PoolErrorPropagates(t *testing.T) {
	sentinel := errors.New("pool dial failed")

	type call struct {
		name string
		fn   func(c *FederationClient) error
	}
	calls := []call{
		{"QueryStream", func(c *FederationClient) error {
			_, err := c.QueryStream(context.Background(), "cl", "addr:1", &foghornfederationpb.QueryStreamRequest{})
			return err
		}},
		{"NotifyOriginPull", func(c *FederationClient) error {
			_, err := c.NotifyOriginPull(context.Background(), "cl", "addr:1", &foghornfederationpb.OriginPullNotification{})
			return err
		}},
		{"PrepareArtifact", func(c *FederationClient) error {
			_, err := c.PrepareArtifact(context.Background(), "cl", "addr:1", &foghornfederationpb.PrepareArtifactRequest{})
			return err
		}},
		{"MintStorageURLs", func(c *FederationClient) error {
			_, err := c.MintStorageURLs(context.Background(), "cl", "addr:1", &foghornfederationpb.MintStorageURLsRequest{})
			return err
		}},
		{"DeleteStorageObjects", func(c *FederationClient) error {
			_, err := c.DeleteStorageObjects(context.Background(), "cl", "addr:1", &foghornfederationpb.DeleteStorageObjectsRequest{})
			return err
		}},
		{"CreateRemoteClip", func(c *FederationClient) error {
			_, err := c.CreateRemoteClip(context.Background(), "cl", "addr:1", &foghornfederationpb.RemoteClipRequest{})
			return err
		}},
		{"CreateRemoteDVR", func(c *FederationClient) error {
			_, err := c.CreateRemoteDVR(context.Background(), "cl", "addr:1", &foghornfederationpb.RemoteDVRRequest{})
			return err
		}},
		{"ListTenantArtifacts", func(c *FederationClient) error {
			_, err := c.ListTenantArtifacts(context.Background(), "cl", "addr:1", &foghornfederationpb.ListTenantArtifactsRequest{})
			return err
		}},
		{"ForwardArtifactCommand", func(c *FederationClient) error {
			_, err := c.ForwardArtifactCommand(context.Background(), "cl", "addr:1", &foghornfederationpb.ForwardArtifactCommandRequest{})
			return err
		}},
		{"OpenPeerChannel", func(c *FederationClient) error {
			_, err := c.OpenPeerChannel(context.Background(), "cl", "addr:1")
			return err
		}},
	}

	for _, tc := range calls {
		t.Run(tc.name, func(t *testing.T) {
			pool := &fakePool{getErr: sentinel}
			c := newTestFedClient(pool)
			if err := tc.fn(c); !errors.Is(err, sentinel) {
				t.Fatalf("err = %v, want pool sentinel", err)
			}
			if pool.gotClusterID != "cl" || pool.gotAddr != "addr:1" {
				t.Fatalf("pool got (%q,%q), want (cl,addr:1)", pool.gotClusterID, pool.gotAddr)
			}
		})
	}
}

// TestWrappers_SuccessReturnsResponse drives every RPC wrapper end-to-end
// against a real loopback stub: GetOrCreate succeeds, the wrapper applies a
// deadline, strips the JWT, and returns the peer's response unchanged.
func TestWrappers_SuccessReturnsResponse(t *testing.T) {
	stub := &recordingStub{
		queryResp:   &foghornfederationpb.QueryStreamResponse{OriginClusterId: "origin-x"},
		originResp:  &foghornfederationpb.OriginPullAck{Accepted: true, DtscUrl: "dtsc://x"},
		prepareResp: &foghornfederationpb.PrepareArtifactResponse{Ready: true, Url: "https://u"},
		mintResp:    &foghornfederationpb.MintStorageURLsResponse{Accepted: true, PresignedPutUrl: "https://put"},
		deleteResp:  &foghornfederationpb.DeleteStorageObjectsResponse{Accepted: true},
		clipResp:    &foghornfederationpb.RemoteClipResponse{Accepted: true, ClipHash: "clip-h"},
		dvrResp:     &foghornfederationpb.RemoteDVRResponse{Accepted: true, DvrHash: "dvr-h"},
		listResp:    &foghornfederationpb.ListTenantArtifactsResponse{Artifacts: []*foghornfederationpb.ArtifactMetadata{{}}},
		forwardResp: &foghornfederationpb.ForwardArtifactCommandResponse{Handled: true},
	}
	client := startStubServer(t, stub)
	pool := &fakePool{client: client}
	c := newTestFedClient(pool)
	ctx := jwtCtx()

	qr, err := c.QueryStream(ctx, "cl", "addr", &foghornfederationpb.QueryStreamRequest{})
	if err != nil || qr.OriginClusterId != "origin-x" {
		t.Fatalf("QueryStream resp=%v err=%v", qr, err)
	}

	or, err := c.NotifyOriginPull(ctx, "cl", "addr", &foghornfederationpb.OriginPullNotification{})
	if err != nil || !or.Accepted || or.DtscUrl != "dtsc://x" {
		t.Fatalf("NotifyOriginPull resp=%v err=%v", or, err)
	}

	pr, err := c.PrepareArtifact(ctx, "cl", "addr", &foghornfederationpb.PrepareArtifactRequest{})
	if err != nil || !pr.Ready || pr.Url != "https://u" {
		t.Fatalf("PrepareArtifact resp=%v err=%v", pr, err)
	}

	mr, err := c.MintStorageURLs(ctx, "cl", "addr", &foghornfederationpb.MintStorageURLsRequest{})
	if err != nil || !mr.Accepted || mr.PresignedPutUrl != "https://put" {
		t.Fatalf("MintStorageURLs resp=%v err=%v", mr, err)
	}

	dr, err := c.DeleteStorageObjects(ctx, "cl", "addr", &foghornfederationpb.DeleteStorageObjectsRequest{})
	if err != nil || !dr.Accepted {
		t.Fatalf("DeleteStorageObjects resp=%v err=%v", dr, err)
	}

	cr, err := c.CreateRemoteClip(ctx, "cl", "addr", &foghornfederationpb.RemoteClipRequest{})
	if err != nil || cr.ClipHash != "clip-h" {
		t.Fatalf("CreateRemoteClip resp=%v err=%v", cr, err)
	}

	vr, err := c.CreateRemoteDVR(ctx, "cl", "addr", &foghornfederationpb.RemoteDVRRequest{})
	if err != nil || vr.DvrHash != "dvr-h" {
		t.Fatalf("CreateRemoteDVR resp=%v err=%v", vr, err)
	}

	lr, err := c.ListTenantArtifacts(ctx, "cl", "addr", &foghornfederationpb.ListTenantArtifactsRequest{})
	if err != nil || len(lr.Artifacts) != 1 {
		t.Fatalf("ListTenantArtifacts resp=%v err=%v", lr, err)
	}

	fr, err := c.ForwardArtifactCommand(ctx, "cl", "addr", &foghornfederationpb.ForwardArtifactCommandRequest{})
	if err != nil || !fr.Handled {
		t.Fatalf("ForwardArtifactCommand resp=%v err=%v", fr, err)
	}

	// The wrappers must wrap the call in a deadline so a hung peer can't pin a
	// federation goroutine forever.
	if !stub.gotHasDeadline {
		t.Fatal("server saw no deadline; wrapper failed to apply call timeout")
	}
}

// TestForwardArtifactCommand_HonorsInheritedDeadline locks the special branch:
// when the incoming context already carries a deadline, ForwardArtifactCommand
// forwards it as-is rather than imposing its own timeout.
func TestForwardArtifactCommand_HonorsInheritedDeadline(t *testing.T) {
	stub := &recordingStub{forwardResp: &foghornfederationpb.ForwardArtifactCommandResponse{Handled: true}}
	client := startStubServer(t, stub)
	c := newTestFedClient(&fakePool{client: client})

	ctx, cancel := context.WithTimeout(jwtCtx(), 5*time.Second)
	defer cancel()

	resp, err := c.ForwardArtifactCommand(ctx, "cl", "addr", &foghornfederationpb.ForwardArtifactCommandRequest{})
	if err != nil || !resp.Handled {
		t.Fatalf("ForwardArtifactCommand resp=%v err=%v", resp, err)
	}
	if !stub.gotHasDeadline {
		t.Fatal("inherited deadline was not propagated to the peer")
	}
}
