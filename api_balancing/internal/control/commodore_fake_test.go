package control

import (
	"context"
	"net"
	"testing"
	"time"

	commodore "github.com/Livepeer-FrameWorks/monorepo/pkg/clients/commodore"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	commodorepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"
	"google.golang.org/grpc"
)

// fakeCommodoreInternal is a reusable in-process Commodore InternalService double. Each
// RPC under test is a settable func; unset RPCs return an empty response. It is
// served over a real localhost gRPC listener (no production seam needed — the
// commodore client constructor has no dialer injection, but AllowInsecure +
// 127.0.0.1 gives a genuine *commodore.GRPCClient pointed at this fake).
//
// All control resolvers reach Commodore through c.internal (InternalService), so
// faking this one service unlocks ResolveContent / artifact / pull-source / chapter
// resolution tests. Extend by adding a func field + override method here.
type fakeCommodoreInternal struct {
	commodorepb.UnimplementedInternalServiceServer

	pullSource         func(context.Context, *commodorepb.ResolvePullSourceByInternalNameRequest) (*commodorepb.ResolvePullSourceByInternalNameResponse, error)
	playbackID         func(context.Context, *commodorepb.ResolvePlaybackIDRequest) (*commodorepb.ResolvePlaybackIDResponse, error)
	clipHash           func(context.Context, *commodorepb.ResolveClipHashRequest) (*commodorepb.ResolveClipHashResponse, error)
	dvrHash            func(context.Context, *commodorepb.ResolveDVRHashRequest) (*commodorepb.ResolveDVRHashResponse, error)
	vodHash            func(context.Context, *commodorepb.ResolveVodHashRequest) (*commodorepb.ResolveVodHashResponse, error)
	internalName       func(context.Context, *commodorepb.ResolveInternalNameRequest) (*commodorepb.ResolveInternalNameResponse, error)
	artifactPlaybackID func(context.Context, *commodorepb.ResolveArtifactPlaybackIDRequest) (*commodorepb.ResolveArtifactPlaybackIDResponse, error)
	chapterPlaybackID  func(context.Context, *commodorepb.ResolveChapterPlaybackIDRequest) (*commodorepb.ResolveChapterPlaybackIDResponse, error)
	artifactInternal   func(context.Context, *commodorepb.ResolveArtifactInternalNameRequest) (*commodorepb.ResolveArtifactInternalNameResponse, error)
	streamContext      func(context.Context, *commodorepb.ResolveStreamContextRequest) (*commodorepb.ResolveStreamContextResponse, error)
}

func (f *fakeCommodoreInternal) ResolveStreamContext(ctx context.Context, req *commodorepb.ResolveStreamContextRequest) (*commodorepb.ResolveStreamContextResponse, error) {
	if f.streamContext != nil {
		return f.streamContext(ctx, req)
	}
	return &commodorepb.ResolveStreamContextResponse{}, nil
}

func (f *fakeCommodoreInternal) ResolveChapterPlaybackID(ctx context.Context, req *commodorepb.ResolveChapterPlaybackIDRequest) (*commodorepb.ResolveChapterPlaybackIDResponse, error) {
	if f.chapterPlaybackID != nil {
		return f.chapterPlaybackID(ctx, req)
	}
	return &commodorepb.ResolveChapterPlaybackIDResponse{}, nil
}

func (f *fakeCommodoreInternal) ResolveArtifactInternalName(ctx context.Context, req *commodorepb.ResolveArtifactInternalNameRequest) (*commodorepb.ResolveArtifactInternalNameResponse, error) {
	if f.artifactInternal != nil {
		return f.artifactInternal(ctx, req)
	}
	return &commodorepb.ResolveArtifactInternalNameResponse{}, nil
}

func (f *fakeCommodoreInternal) ResolvePullSourceByInternalName(ctx context.Context, req *commodorepb.ResolvePullSourceByInternalNameRequest) (*commodorepb.ResolvePullSourceByInternalNameResponse, error) {
	if f.pullSource != nil {
		return f.pullSource(ctx, req)
	}
	return &commodorepb.ResolvePullSourceByInternalNameResponse{}, nil
}

func (f *fakeCommodoreInternal) ResolvePlaybackID(ctx context.Context, req *commodorepb.ResolvePlaybackIDRequest) (*commodorepb.ResolvePlaybackIDResponse, error) {
	if f.playbackID != nil {
		return f.playbackID(ctx, req)
	}
	return &commodorepb.ResolvePlaybackIDResponse{}, nil
}

func (f *fakeCommodoreInternal) ResolveClipHash(ctx context.Context, req *commodorepb.ResolveClipHashRequest) (*commodorepb.ResolveClipHashResponse, error) {
	if f.clipHash != nil {
		return f.clipHash(ctx, req)
	}
	return &commodorepb.ResolveClipHashResponse{}, nil
}

func (f *fakeCommodoreInternal) ResolveDVRHash(ctx context.Context, req *commodorepb.ResolveDVRHashRequest) (*commodorepb.ResolveDVRHashResponse, error) {
	if f.dvrHash != nil {
		return f.dvrHash(ctx, req)
	}
	return &commodorepb.ResolveDVRHashResponse{}, nil
}

func (f *fakeCommodoreInternal) ResolveVodHash(ctx context.Context, req *commodorepb.ResolveVodHashRequest) (*commodorepb.ResolveVodHashResponse, error) {
	if f.vodHash != nil {
		return f.vodHash(ctx, req)
	}
	return &commodorepb.ResolveVodHashResponse{}, nil
}

func (f *fakeCommodoreInternal) ResolveInternalName(ctx context.Context, req *commodorepb.ResolveInternalNameRequest) (*commodorepb.ResolveInternalNameResponse, error) {
	if f.internalName != nil {
		return f.internalName(ctx, req)
	}
	return &commodorepb.ResolveInternalNameResponse{}, nil
}

func (f *fakeCommodoreInternal) ResolveArtifactPlaybackID(ctx context.Context, req *commodorepb.ResolveArtifactPlaybackIDRequest) (*commodorepb.ResolveArtifactPlaybackIDResponse, error) {
	if f.artifactPlaybackID != nil {
		return f.artifactPlaybackID(ctx, req)
	}
	return &commodorepb.ResolveArtifactPlaybackIDResponse{}, nil
}

// startFakeCommodoreServer serves fake on a localhost listener, points the package
// CommodoreClient global at a real client dialing it, and restores everything on
// cleanup.
func startFakeCommodoreServer(t *testing.T, fake *fakeCommodoreInternal) {
	t.Helper()
	lis, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	srv := grpc.NewServer()
	commodorepb.RegisterInternalServiceServer(srv, fake)
	go func() { _ = srv.Serve(lis) }()

	client, err := commodore.NewGRPCClient(commodore.GRPCConfig{
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

	prev := CommodoreClient
	CommodoreClient = client
	t.Cleanup(func() {
		CommodoreClient = prev
		_ = client.Close()
		srv.Stop()
		_ = lis.Close()
	})
}

// Smoke test: the harness yields a working client whose calls reach the fake.
func TestFakeCommodoreHarness(t *testing.T) {
	startFakeCommodoreServer(t, &fakeCommodoreInternal{
		pullSource: func(_ context.Context, req *commodorepb.ResolvePullSourceByInternalNameRequest) (*commodorepb.ResolvePullSourceByInternalNameResponse, error) {
			if req.GetInternalName() != "live+x" {
				t.Errorf("unexpected internal name %q", req.GetInternalName())
			}
			return &commodorepb.ResolvePullSourceByInternalNameResponse{Found: true, Enabled: true, SourceUri: "srt://up", TenantId: "t1"}, nil
		},
	})

	resp, err := CommodoreClient.ResolvePullSourceByInternalName(context.Background(), "live+x")
	if err != nil {
		t.Fatalf("round-trip failed: %v", err)
	}
	if !resp.GetFound() || resp.GetSourceUri() != "srt://up" || resp.GetTenantId() != "t1" {
		t.Fatalf("unexpected response: %+v", resp)
	}
}
