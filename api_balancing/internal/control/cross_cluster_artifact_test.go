package control

import (
	"context"
	"errors"
	"strings"
	"testing"

	pb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto"
)

type fakeCrossClusterFedClient struct {
	responses []*pb.PrepareArtifactResponse
	errs      []error
	calls     []struct {
		clusterID string
		req       *pb.PrepareArtifactRequest
	}
}

func (f *fakeCrossClusterFedClient) PrepareArtifact(_ context.Context, clusterID, _ string, req *pb.PrepareArtifactRequest) (*pb.PrepareArtifactResponse, error) {
	idx := len(f.calls)
	f.calls = append(f.calls, struct {
		clusterID string
		req       *pb.PrepareArtifactRequest
	}{clusterID, req})
	if idx < len(f.errs) && f.errs[idx] != nil {
		return nil, f.errs[idx]
	}
	if idx < len(f.responses) {
		return f.responses[idx], nil
	}
	return &pb.PrepareArtifactResponse{}, nil
}

type fakeCrossClusterPeerResolver struct {
	addrs map[string]string
}

func (f *fakeCrossClusterPeerResolver) GetPeerAddr(clusterID string) string {
	return f.addrs[clusterID]
}

func makeCCDeps(fed *fakeCrossClusterFedClient, addrs map[string]string) *CrossClusterArtifactDeps {
	return &CrossClusterArtifactDeps{
		FedClient:      fed,
		PeerResolver:   &fakeCrossClusterPeerResolver{addrs: addrs},
		LocalClusterID: "cluster-local",
	}
}

func TestResolve_HappyPath_ReturnsURL(t *testing.T) {
	fed := &fakeCrossClusterFedClient{
		responses: []*pb.PrepareArtifactResponse{
			{Ready: true, Url: "https://peer/clip.mp4"},
		},
	}
	d := makeCCDeps(fed, map[string]string{"cluster-origin": "peer:443"})

	got, err := d.Resolve(context.Background(), "art-1", "clip", "tenant-1", "cluster-origin", nil)
	if err != nil {
		t.Fatalf("expected success, got %v", err)
	}
	if got.URL != "https://peer/clip.mp4" {
		t.Fatalf("URL = %q", got.URL)
	}
	if got.OriginClusterID != "cluster-origin" || got.StorageClusterID != "" {
		t.Fatalf("origin=%q storage=%q", got.OriginClusterID, got.StorageClusterID)
	}
}

func TestResolve_LocalCluster_Refused(t *testing.T) {
	d := makeCCDeps(&fakeCrossClusterFedClient{}, nil)
	if _, err := d.Resolve(context.Background(), "art-1", "clip", "tenant-1", "cluster-local", nil); !errors.Is(err, ErrCrossClusterArtifactUnavailable) {
		t.Fatalf("expected ErrCrossClusterArtifactUnavailable, got %v", err)
	}
}

func TestResolve_EmptyOrigin_Refused(t *testing.T) {
	d := makeCCDeps(&fakeCrossClusterFedClient{}, nil)
	if _, err := d.Resolve(context.Background(), "art-1", "clip", "tenant-1", "", nil); !errors.Is(err, ErrCrossClusterArtifactUnavailable) {
		t.Fatalf("expected ErrCrossClusterArtifactUnavailable, got %v", err)
	}
}

func TestResolve_UnknownPeerAddr_Refused(t *testing.T) {
	d := makeCCDeps(&fakeCrossClusterFedClient{}, map[string]string{})
	if _, err := d.Resolve(context.Background(), "art-1", "clip", "tenant-1", "cluster-origin", nil); !errors.Is(err, ErrCrossClusterArtifactUnavailable) {
		t.Fatalf("expected ErrCrossClusterArtifactUnavailable, got %v", err)
	}
}

func TestResolve_PrepareArtifactRPCFailure_Wrapped(t *testing.T) {
	fed := &fakeCrossClusterFedClient{
		errs: []error{errors.New("peer down")},
	}
	d := makeCCDeps(fed, map[string]string{"cluster-origin": "peer:443"})

	_, err := d.Resolve(context.Background(), "art-1", "clip", "tenant-1", "cluster-origin", nil)
	if err == nil || !strings.Contains(err.Error(), "peer down") {
		t.Fatalf("expected wrapped peer-down error, got %v", err)
	}
}

func TestResolve_OriginErrorString_Surfaced(t *testing.T) {
	fed := &fakeCrossClusterFedClient{
		responses: []*pb.PrepareArtifactResponse{
			{Error: "artifact metadata inconsistent"},
		},
	}
	d := makeCCDeps(fed, map[string]string{"cluster-origin": "peer:443"})

	_, err := d.Resolve(context.Background(), "art-1", "clip", "tenant-1", "cluster-origin", nil)
	if err == nil || !strings.Contains(err.Error(), "artifact metadata inconsistent") {
		t.Fatalf("expected origin error surfaced, got %v", err)
	}
}

func TestResolve_NotReady_Refused(t *testing.T) {
	fed := &fakeCrossClusterFedClient{
		responses: []*pb.PrepareArtifactResponse{
			{Ready: false},
		},
	}
	d := makeCCDeps(fed, map[string]string{"cluster-origin": "peer:443"})

	_, err := d.Resolve(context.Background(), "art-1", "clip", "tenant-1", "cluster-origin", nil)
	if !errors.Is(err, ErrCrossClusterArtifactUnavailable) {
		t.Fatalf("expected ErrCrossClusterArtifactUnavailable for not-ready, got %v", err)
	}
}

func TestResolve_StorageRedirect_FollowsToTarget(t *testing.T) {
	fed := &fakeCrossClusterFedClient{
		responses: []*pb.PrepareArtifactResponse{
			{RedirectClusterId: "cluster-storage"},
			{Ready: true, Url: "https://storage/clip.mp4"},
		},
	}
	d := makeCCDeps(fed, map[string]string{
		"cluster-origin":  "origin:443",
		"cluster-storage": "storage:443",
	})

	got, err := d.Resolve(context.Background(), "art-1", "clip", "tenant-1", "cluster-origin", nil)
	if err != nil {
		t.Fatalf("expected success after redirect, got %v", err)
	}
	if got.URL != "https://storage/clip.mp4" {
		t.Fatalf("URL = %q", got.URL)
	}
	if got.OriginClusterID != "cluster-origin" || got.StorageClusterID != "cluster-storage" {
		t.Fatalf("origin=%q storage=%q", got.OriginClusterID, got.StorageClusterID)
	}
	if len(fed.calls) != 2 || fed.calls[1].clusterID != "cluster-storage" {
		t.Fatalf("second call should target storage cluster, got %+v", fed.calls)
	}
}

func TestResolve_StorageRedirect_Unauthorized_AbortsBeforeSecondDial(t *testing.T) {
	// SSRF gate: a redirect target outside the tenant's allowlist must be
	// refused BEFORE the second PrepareArtifact dial (which carries tenant_id).
	fed := &fakeCrossClusterFedClient{
		responses: []*pb.PrepareArtifactResponse{
			{RedirectClusterId: "cluster-evil"},
			{Ready: true, Url: "https://evil/clip.mp4"}, // must never be reached
		},
	}
	d := makeCCDeps(fed, map[string]string{
		"cluster-origin": "origin:443",
		"cluster-evil":   "evil:443",
	})

	denyRedirect := func(c string) bool { return c != "cluster-evil" }
	_, err := d.Resolve(context.Background(), "art-1", "clip", "tenant-1", "cluster-origin", denyRedirect)
	if err == nil || !strings.Contains(err.Error(), "not authorized for tenant") {
		t.Fatalf("expected redirect-authorization refusal, got %v", err)
	}
	if len(fed.calls) != 1 {
		t.Fatalf("redirect must be refused before the second dial; got %d calls", len(fed.calls))
	}
}

func TestResolve_StorageRedirect_Authorized_FollowsToTarget(t *testing.T) {
	// A redirect the predicate allows proceeds to the second dial.
	fed := &fakeCrossClusterFedClient{
		responses: []*pb.PrepareArtifactResponse{
			{RedirectClusterId: "cluster-storage"},
			{Ready: true, Url: "https://storage/clip.mp4"},
		},
	}
	d := makeCCDeps(fed, map[string]string{
		"cluster-origin":  "origin:443",
		"cluster-storage": "storage:443",
	})

	got, err := d.Resolve(context.Background(), "art-1", "clip", "tenant-1", "cluster-origin", func(string) bool { return true })
	if err != nil {
		t.Fatalf("expected success, got %v", err)
	}
	if got.URL != "https://storage/clip.mp4" || len(fed.calls) != 2 {
		t.Fatalf("URL=%q calls=%d", got.URL, len(fed.calls))
	}
}

func TestResolve_StorageRedirect_LoopToOrigin_Refused(t *testing.T) {
	fed := &fakeCrossClusterFedClient{
		responses: []*pb.PrepareArtifactResponse{
			{RedirectClusterId: "cluster-origin"},
		},
	}
	d := makeCCDeps(fed, map[string]string{"cluster-origin": "origin:443"})

	_, err := d.Resolve(context.Background(), "art-1", "clip", "tenant-1", "cluster-origin", nil)
	if err == nil || !strings.Contains(err.Error(), "redirect loop") {
		t.Fatalf("expected redirect-loop refusal, got %v", err)
	}
}

func TestResolve_StorageRedirect_LoopToLocal_Refused(t *testing.T) {
	fed := &fakeCrossClusterFedClient{
		responses: []*pb.PrepareArtifactResponse{
			{RedirectClusterId: "cluster-local"},
		},
	}
	d := makeCCDeps(fed, map[string]string{"cluster-origin": "origin:443"})

	_, err := d.Resolve(context.Background(), "art-1", "clip", "tenant-1", "cluster-origin", nil)
	if err == nil || !strings.Contains(err.Error(), "redirect loop") {
		t.Fatalf("expected redirect-loop refusal, got %v", err)
	}
}

func TestResolve_StorageRedirect_ChainedRedirect_Refused(t *testing.T) {
	fed := &fakeCrossClusterFedClient{
		responses: []*pb.PrepareArtifactResponse{
			{RedirectClusterId: "cluster-storage"},
			{RedirectClusterId: "cluster-storage-2"}, // illegal: chained redirect
		},
	}
	d := makeCCDeps(fed, map[string]string{
		"cluster-origin":    "origin:443",
		"cluster-storage":   "storage:443",
		"cluster-storage-2": "storage2:443",
	})

	_, err := d.Resolve(context.Background(), "art-1", "clip", "tenant-1", "cluster-origin", nil)
	if err == nil || !strings.Contains(err.Error(), "chained storage redirect rejected") {
		t.Fatalf("expected chained-redirect refusal, got %v", err)
	}
}

func TestResolve_StorageRedirect_UnknownTargetAddr_Refused(t *testing.T) {
	fed := &fakeCrossClusterFedClient{
		responses: []*pb.PrepareArtifactResponse{
			{RedirectClusterId: "cluster-storage"},
		},
	}
	// PeerResolver only knows the origin, not the storage cluster.
	d := makeCCDeps(fed, map[string]string{"cluster-origin": "origin:443"})

	_, err := d.Resolve(context.Background(), "art-1", "clip", "tenant-1", "cluster-origin", nil)
	if !errors.Is(err, ErrCrossClusterArtifactUnavailable) {
		t.Fatalf("expected ErrCrossClusterArtifactUnavailable for unknown redirect target, got %v", err)
	}
}

func TestResolve_NilDeps_Refused(t *testing.T) {
	d := &CrossClusterArtifactDeps{LocalClusterID: "cluster-local"}
	if _, err := d.Resolve(context.Background(), "art-1", "clip", "tenant-1", "cluster-origin", nil); !errors.Is(err, ErrCrossClusterArtifactUnavailable) {
		t.Fatalf("expected ErrCrossClusterArtifactUnavailable with nil deps, got %v", err)
	}
}

func TestResolveCrossClusterArtifactURL_NoGlobalDeps_Refused(t *testing.T) {
	// Snapshot + clear global state to assert the package-level entrypoint
	// fails when deps aren't installed.
	crossClusterDepsMu.Lock()
	prev := crossClusterDeps
	crossClusterDeps = nil
	crossClusterDepsMu.Unlock()
	t.Cleanup(func() {
		crossClusterDepsMu.Lock()
		crossClusterDeps = prev
		crossClusterDepsMu.Unlock()
	})

	if _, err := ResolveCrossClusterArtifactURL(context.Background(), "art-1", "clip", "tenant-1", "cluster-origin", nil); !errors.Is(err, ErrCrossClusterArtifactUnavailable) {
		t.Fatalf("expected ErrCrossClusterArtifactUnavailable, got %v", err)
	}
}
