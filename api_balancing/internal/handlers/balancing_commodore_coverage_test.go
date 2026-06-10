package handlers

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"testing"
	"time"

	"frameworks/api_balancing/internal/control"
	"frameworks/api_balancing/internal/state"

	commodorecli "github.com/Livepeer-FrameWorks/monorepo/pkg/clients/commodore"
	qmcli "github.com/Livepeer-FrameWorks/monorepo/pkg/clients/quartermaster"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	commodorepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
	quartermasterpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/quartermaster"
	"google.golang.org/grpc"
)

// These tests unlock the two handleStreamBalancing branches that wave 2 could
// not reach because they are gated on a SUCCESSFUL Commodore resolution: the
// resolved target must carry a non-empty TenantID (prepaid/402 gate) or a
// FixedNode (VOD artifact pinning). Both are settable here because
// control.CommodoreClient — the concrete global ResolveStream reads — is an
// exported package var that a real client dialing a localhost fake can replace.

// commodoreBalancingFake is an in-process Commodore InternalService double whose
// only two RPCs (the ones handleStreamBalancing's resolution path exercises) are
// settable funcs. Unset RPCs return an empty/not-found response.
type commodoreBalancingFake struct {
	commodorepb.UnimplementedInternalServiceServer

	internalName     func(context.Context, *commodorepb.ResolveInternalNameRequest) (*commodorepb.ResolveInternalNameResponse, error)
	artifactInternal func(context.Context, *commodorepb.ResolveArtifactInternalNameRequest) (*commodorepb.ResolveArtifactInternalNameResponse, error)
}

func (f *commodoreBalancingFake) ResolveInternalName(ctx context.Context, req *commodorepb.ResolveInternalNameRequest) (*commodorepb.ResolveInternalNameResponse, error) {
	if f.internalName != nil {
		return f.internalName(ctx, req)
	}
	return &commodorepb.ResolveInternalNameResponse{}, nil
}

func (f *commodoreBalancingFake) ResolveArtifactInternalName(ctx context.Context, req *commodorepb.ResolveArtifactInternalNameRequest) (*commodorepb.ResolveArtifactInternalNameResponse, error) {
	if f.artifactInternal != nil {
		return f.artifactInternal(ctx, req)
	}
	return &commodorepb.ResolveArtifactInternalNameResponse{}, nil
}

// startBalancingCommodoreFake serves fake on a localhost gRPC listener, builds a
// real *commodore.GRPCClient against it, and points BOTH the resolution-path
// global (control.CommodoreClient, read by ResolveStream) and the
// handlers-package global (commodoreClient, read on the telemetry path) at it.
// Everything is restored on cleanup.
func startBalancingCommodoreFake(t *testing.T, fake *commodoreBalancingFake) {
	t.Helper()
	lis, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	srv := grpc.NewServer()
	commodorepb.RegisterInternalServiceServer(srv, fake)
	go func() { _ = srv.Serve(lis) }()

	client, err := commodorecli.NewGRPCClient(commodorecli.GRPCConfig{
		GRPCAddr:      lis.Addr().String(),
		AllowInsecure: true,
		Logger:        logging.NewLogger(),
		Timeout:       5 * time.Second,
	})
	if err != nil {
		srv.Stop()
		_ = lis.Close()
		t.Fatalf("commodore client: %v", err)
	}

	prevControl := control.CommodoreClient
	prevHandlers := commodoreClient
	control.CommodoreClient = client
	commodoreClient = client
	t.Cleanup(func() {
		control.CommodoreClient = prevControl
		commodoreClient = prevHandlers
		_ = client.Close()
		srv.Stop()
		_ = lis.Close()
	})
}

// fakeTenantService is a Quartermaster TenantService double serving a single
// settable ValidateTenant func. getBillingStatus falls through to
// quartermasterClient.ValidateTenant when triggerProcessor is nil, so this is
// the seam that drives the prepaid-suspended billing decision.
type fakeTenantService struct {
	quartermasterpb.UnimplementedTenantServiceServer
	validate func(context.Context, *quartermasterpb.ValidateTenantRequest) (*quartermasterpb.ValidateTenantResponse, error)
}

func (f *fakeTenantService) ValidateTenant(ctx context.Context, req *quartermasterpb.ValidateTenantRequest) (*quartermasterpb.ValidateTenantResponse, error) {
	if f.validate != nil {
		return f.validate(ctx, req)
	}
	return &quartermasterpb.ValidateTenantResponse{}, nil
}

// startQuartermasterFake stands up a localhost QM gRPC server exposing the fake
// TenantService, builds a real *qmclient.GRPCClient, and points the
// handlers-package quartermasterClient global at it. Restored on cleanup.
func startQuartermasterFake(t *testing.T, fake *fakeTenantService) {
	t.Helper()
	lis, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	srv := grpc.NewServer()
	quartermasterpb.RegisterTenantServiceServer(srv, fake)
	go func() { _ = srv.Serve(lis) }()

	client, err := qmcli.NewGRPCClient(qmcli.GRPCConfig{
		GRPCAddr:      lis.Addr().String(),
		AllowInsecure: true,
		Logger:        logging.NewLogger(),
		Timeout:       5 * time.Second,
	})
	if err != nil {
		srv.Stop()
		_ = lis.Close()
		t.Fatalf("quartermaster client: %v", err)
	}

	prev := quartermasterClient
	quartermasterClient = client
	t.Cleanup(func() {
		quartermasterClient = prev
		_ = client.Close()
		srv.Stop()
		_ = lis.Close()
	})
}

// Invariant: a prepaid owner whose account is SUSPENDED, with no x402 payment
// header presented, is blocked at /<stream> with HTTP 402 and the
// insufficient-balance body — viewer playback is gated on the owner's billing
// state, not just node availability. This is the access/payment gate that only
// fires when Commodore resolves a non-empty TenantID for the live stream.
func TestStreamBalancing_PrepaidSuspendedReturns402(t *testing.T) {
	balancingTestEnv(t)

	startBalancingCommodoreFake(t, &commodoreBalancingFake{
		internalName: func(_ context.Context, req *commodorepb.ResolveInternalNameRequest) (*commodorepb.ResolveInternalNameResponse, error) {
			// Resolution keys off the live+ internal name (prefix stripped).
			if req.GetInternalName() != "demo" {
				t.Errorf("ResolveInternalName got %q, want demo", req.GetInternalName())
			}
			return &commodorepb.ResolveInternalNameResponse{
				InternalName: "demo",
				TenantId:     "tenant-suspended",
				StreamId:     "stream-1",
			}, nil
		},
	})
	startQuartermasterFake(t, &fakeTenantService{
		validate: func(_ context.Context, req *quartermasterpb.ValidateTenantRequest) (*quartermasterpb.ValidateTenantResponse, error) {
			if req.GetTenantId() != "tenant-suspended" {
				t.Errorf("ValidateTenant got %q, want tenant-suspended", req.GetTenantId())
			}
			return &quartermasterpb.ValidateTenantResponse{
				Valid:        true,
				TenantId:     "tenant-suspended",
				BillingModel: "prepaid",
				IsSuspended:  true,
			}, nil
		},
	})

	c, w := ginCtxFor(t, "")
	handleStreamBalancing(c, "live+demo")

	if w.Code != http.StatusPaymentRequired {
		t.Fatalf("status = %d, want 402 (prepaid suspended)", w.Code)
	}
	var body map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal 402 body %q: %v", w.Body.String(), err)
	}
	if body["error"] != "insufficient_balance" {
		t.Fatalf("error = %v, want insufficient_balance", body["error"])
	}
}

// Invariant: a prepaid owner whose balance is NEGATIVE (not yet hard-suspended),
// with no payment header, is likewise blocked with 402. Locks that the
// is_balance_negative warning state alone gates new viewer playback.
func TestStreamBalancing_PrepaidNegativeBalanceReturns402(t *testing.T) {
	balancingTestEnv(t)

	startBalancingCommodoreFake(t, &commodoreBalancingFake{
		internalName: func(_ context.Context, _ *commodorepb.ResolveInternalNameRequest) (*commodorepb.ResolveInternalNameResponse, error) {
			return &commodorepb.ResolveInternalNameResponse{
				InternalName: "demo",
				TenantId:     "tenant-neg",
			}, nil
		},
	})
	startQuartermasterFake(t, &fakeTenantService{
		validate: func(_ context.Context, _ *quartermasterpb.ValidateTenantRequest) (*quartermasterpb.ValidateTenantResponse, error) {
			return &quartermasterpb.ValidateTenantResponse{
				Valid:             true,
				TenantId:          "tenant-neg",
				BillingModel:      "prepaid",
				IsBalanceNegative: true,
			}, nil
		},
	})

	c, w := ginCtxFor(t, "")
	handleStreamBalancing(c, "live+demo")

	if w.Code != http.StatusPaymentRequired {
		t.Fatalf("status = %d, want 402 (prepaid negative balance)", w.Code)
	}
}

// Invariant: a prepaid owner in GOOD standing (not suspended, balance positive)
// does NOT trip the 402 gate — viewer playback proceeds to normal node
// selection and the healthy local edge is returned. This is the negative
// control that proves the 402 gate keys on the billing flags, not merely on
// TenantID being populated.
func TestStreamBalancing_PrepaidHealthyProceedsToSelection(t *testing.T) {
	sm := balancingTestEnv(t)
	seedOriginEdge(t, sm, "edge-ok", "edge-ok.example", "demo")

	startBalancingCommodoreFake(t, &commodoreBalancingFake{
		internalName: func(_ context.Context, _ *commodorepb.ResolveInternalNameRequest) (*commodorepb.ResolveInternalNameResponse, error) {
			return &commodorepb.ResolveInternalNameResponse{
				InternalName: "demo",
				TenantId:     "tenant-ok",
			}, nil
		},
	})
	startQuartermasterFake(t, &fakeTenantService{
		validate: func(_ context.Context, _ *quartermasterpb.ValidateTenantRequest) (*quartermasterpb.ValidateTenantResponse, error) {
			return &quartermasterpb.ValidateTenantResponse{
				Valid:        true,
				TenantId:     "tenant-ok",
				BillingModel: "prepaid",
			}, nil
		},
	})

	c, w := ginCtxFor(t, "")
	handleStreamBalancing(c, "live+demo")

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (healthy prepaid not gated)", w.Code)
	}
	if w.Body.String() != "edge-ok.example" {
		t.Fatalf("selected node = %q, want edge-ok.example", w.Body.String())
	}
}

// Invariant: a postpaid owner is NEVER gated by the prepaid 402 path even when
// suspended/negative flags are set — only billing_model=="prepaid" reaches the
// payment branch. Locks that the billing-model discriminator (not just the
// suspension flag) controls the gate.
func TestStreamBalancing_PostpaidIgnoresSuspensionFlags(t *testing.T) {
	sm := balancingTestEnv(t)
	seedOriginEdge(t, sm, "edge-pp", "edge-pp.example", "demo")

	startBalancingCommodoreFake(t, &commodoreBalancingFake{
		internalName: func(_ context.Context, _ *commodorepb.ResolveInternalNameRequest) (*commodorepb.ResolveInternalNameResponse, error) {
			return &commodorepb.ResolveInternalNameResponse{
				InternalName: "demo",
				TenantId:     "tenant-pp",
			}, nil
		},
	})
	startQuartermasterFake(t, &fakeTenantService{
		validate: func(_ context.Context, _ *quartermasterpb.ValidateTenantRequest) (*quartermasterpb.ValidateTenantResponse, error) {
			return &quartermasterpb.ValidateTenantResponse{
				Valid:        true,
				TenantId:     "tenant-pp",
				BillingModel: "postpaid",
				IsSuspended:  true, // would gate a prepaid owner; postpaid must ignore it
			}, nil
		},
	})

	c, w := ginCtxFor(t, "")
	handleStreamBalancing(c, "live+demo")

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (postpaid not gated by suspension)", w.Code)
	}
	if w.Body.String() != "edge-pp.example" {
		t.Fatalf("selected node = %q, want edge-pp.example", w.Body.String())
	}
}

// seedArtifactNode makes nodeID/host an ACTIVE storage node that holds the
// artifact identified by clipHash, so applyArtifactPlacement (called from
// ResolveStream's vod+ branch) finds it via FindNodesByArtifactHash and pins
// target.FixedNode to this node's host. The node must be probe-verified
// (active:true) because FindNodesByArtifactHash skips non-active nodes.
func seedArtifactNode(t *testing.T, sm *state.StreamStateManager, nodeID, host, clipHash string) {
	t.Helper()
	seedNodeWithStream(t, sm, seedNode{
		nodeID: nodeID, host: host, active: true,
		ramMax: 100, ramCur: 10,
	}, "", 0, 0, 0)
	sm.SetNodeArtifacts(nodeID, []*ipcpb.StoredArtifact{
		{ClipHash: clipHash, FilePath: "/data/" + clipHash + ".mp4", StreamName: "vod+art"},
	})
}

// Invariant: a VOD whose artifact is resolved by Commodore to a hash present on
// a local storage node is pinned (FixedNode) and the viewer is sent to THAT
// storage node — a plain 200 hostname when no proto is requested. This is the
// fixed-node redirect decision that wave 2 could not reach without a successful
// artifact resolution.
func TestStreamBalancing_FixedNodeVodReturnsStorageHost(t *testing.T) {
	sm := balancingTestEnv(t)
	seedArtifactNode(t, sm, "store-1", "store-1.example", "arthash1")

	startBalancingCommodoreFake(t, &commodoreBalancingFake{
		artifactInternal: func(_ context.Context, req *commodorepb.ResolveArtifactInternalNameRequest) (*commodorepb.ResolveArtifactInternalNameResponse, error) {
			if req.GetInternalName() != "art" {
				t.Errorf("ResolveArtifactInternalName got %q, want art", req.GetInternalName())
			}
			return &commodorepb.ResolveArtifactInternalNameResponse{
				Found:        true,
				ArtifactHash: "arthash1",
				InternalName: "art",
				TenantId:     "tenant-vod",
				ContentType:  "vod",
			}, nil
		},
	})

	c, w := ginCtxFor(t, "")
	handleStreamBalancing(c, "vod+art")

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (fixed-node VOD)", w.Code)
	}
	if w.Body.String() != "store-1.example" {
		t.Fatalf("body = %q, want store-1.example (storage node pin)", w.Body.String())
	}
}

// Invariant: with proto= the fixed-node VOD becomes a 307 redirect to
// proto://<storageHost>/<originalStreamName>, preserving the original vod+ name
// in the Location. Locks the redirect form of the fixed-node decision.
func TestStreamBalancing_FixedNodeVodRedirectsWithProto(t *testing.T) {
	sm := balancingTestEnv(t)
	seedArtifactNode(t, sm, "store-2", "store-2.example", "arthash2")

	startBalancingCommodoreFake(t, &commodoreBalancingFake{
		artifactInternal: func(_ context.Context, _ *commodorepb.ResolveArtifactInternalNameRequest) (*commodorepb.ResolveArtifactInternalNameResponse, error) {
			return &commodorepb.ResolveArtifactInternalNameResponse{
				Found:        true,
				ArtifactHash: "arthash2",
				InternalName: "art2",
				TenantId:     "tenant-vod",
				ContentType:  "vod",
			}, nil
		},
	})

	c, w := ginCtxFor(t, "proto=https")
	handleStreamBalancing(c, "vod+art2")

	if w.Code != http.StatusTemporaryRedirect {
		t.Fatalf("status = %d, want 307 (fixed-node VOD redirect)", w.Code)
	}
	loc := w.Header().Get("Location")
	want := "https://store-2.example/vod+art2"
	if loc != want && loc[:len(want)] != want {
		t.Fatalf("Location = %q, want prefix %q", loc, want)
	}
}
