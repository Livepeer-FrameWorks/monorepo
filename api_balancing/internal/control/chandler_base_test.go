package control

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"frameworks/pkg/logging"
	pb "frameworks/pkg/proto"
)

func TestGetChandlerBaseURLUsesExplicitOverride(t *testing.T) {
	prevClusterID := localClusterID
	prevGetCluster := getClusterFn
	clearResolvedChandlerBaseURL()
	t.Cleanup(func() {
		localClusterID = prevClusterID
		getClusterFn = prevGetCluster
		clearResolvedChandlerBaseURL()
	})

	t.Setenv("CHANDLER_BASE_URL", "https://assets.frameworks.network")
	t.Setenv("CHANDLER_HOST", "ignored-host")
	t.Setenv("CHANDLER_PORT", "9999")

	localClusterID = "media-central-primary"
	getClusterFn = func(context.Context, string) (*pb.InfrastructureCluster, error) {
		return nil, errors.New("should not be called when override is set")
	}

	if got := getChandlerBaseURL(); got != "https://assets.frameworks.network" {
		t.Fatalf("expected explicit Chandler base override, got %q", got)
	}
}

func TestGetChandlerBaseURLDerivesPlatformDomainFromClusterMetadata(t *testing.T) {
	prevClusterID := localClusterID
	prevGetCluster := getClusterFn
	clearResolvedChandlerBaseURL()
	t.Cleanup(func() {
		localClusterID = prevClusterID
		getClusterFn = prevGetCluster
		clearResolvedChandlerBaseURL()
	})

	t.Setenv("CHANDLER_BASE_URL", "")
	t.Setenv("CHANDLER_HOST", "fallback-host")
	t.Setenv("CHANDLER_PORT", "18020")

	localClusterID = "media-central-primary"
	getClusterFn = func(context.Context, string) (*pb.InfrastructureCluster, error) {
		return &pb.InfrastructureCluster{
			ClusterId:   "media-central-primary",
			ClusterName: "Media Central Primary",
			BaseUrl:     "frameworks.network",
		}, nil
	}

	if got := getChandlerBaseURL(); got != "https://chandler.media-central-primary.frameworks.network" {
		t.Fatalf("expected platform-derived Chandler base URL, got %q", got)
	}

	getClusterFn = func(context.Context, string) (*pb.InfrastructureCluster, error) {
		return nil, errors.New("should use cached Chandler base URL after first resolve")
	}
	if got := getChandlerBaseURL(); got != "https://chandler.media-central-primary.frameworks.network" {
		t.Fatalf("expected cached platform-derived Chandler base URL, got %q", got)
	}
}

func TestGetChandlerBaseURLFallsBackToHostAndPort(t *testing.T) {
	prevClusterID := localClusterID
	prevGetCluster := getClusterFn
	clearResolvedChandlerBaseURL()
	t.Cleanup(func() {
		localClusterID = prevClusterID
		getClusterFn = prevGetCluster
		clearResolvedChandlerBaseURL()
	})

	t.Setenv("CHANDLER_BASE_URL", "")
	t.Setenv("CHANDLER_HOST", "chandler-public")
	t.Setenv("CHANDLER_PORT", "18020")

	localClusterID = "media-central-primary"
	getClusterFn = func(context.Context, string) (*pb.InfrastructureCluster, error) {
		return nil, errors.New("quartermaster unavailable")
	}

	if got := getChandlerBaseURL(); got != "http://chandler-public:18020" {
		t.Fatalf("expected legacy Chandler host/port fallback, got %q", got)
	}
}

func TestGetChandlerInternalBaseURLsUsesInternalOverride(t *testing.T) {
	t.Setenv("CHANDLER_INTERNAL_URL", "http://chandler-a:18020, http://chandler-b:18020/")
	t.Setenv("CHANDLER_HOST", "chandler-public")
	t.Setenv("CHANDLER_PORT", "9999")

	got := getChandlerInternalBaseURLs()
	want := []string{"http://chandler-a:18020", "http://chandler-b:18020"}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("expected internal Chandler overrides %#v, got %#v", want, got)
	}
}

func TestGetChandlerInternalBaseURLsFallsBackToManagedPublicBase(t *testing.T) {
	prevClusterID := localClusterID
	prevGetCluster := getClusterFn
	clearResolvedChandlerBaseURL()
	t.Cleanup(func() {
		localClusterID = prevClusterID
		getClusterFn = prevGetCluster
		clearResolvedChandlerBaseURL()
	})

	t.Setenv("CHANDLER_INTERNAL_URL", "")
	t.Setenv("CHANDLER_BASE_URL", "")
	t.Setenv("CHANDLER_HOST", "")
	t.Setenv("CHANDLER_PORT", "")

	localClusterID = "media-central-primary"
	getClusterFn = func(context.Context, string) (*pb.InfrastructureCluster, error) {
		return &pb.InfrastructureCluster{
			ClusterId:   "media-central-primary",
			ClusterName: "Media Central Primary",
			BaseUrl:     "frameworks.network",
		}, nil
	}

	got := getChandlerInternalBaseURLs()
	if len(got) != 1 || got[0] != "https://chandler.media-central-primary.frameworks.network" {
		t.Fatalf("expected managed Chandler base fallback, got %#v", got)
	}
}

func TestInvalidateChandlerThumbnailCache(t *testing.T) {
	var gotAuths []string
	var gotReqs []chandlerInvalidateRequest
	newServer := func() *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/internal/assets/cache/invalidate" {
				t.Fatalf("unexpected path %q", r.URL.Path)
			}
			gotAuths = append(gotAuths, r.Header.Get("Authorization"))
			var gotReq chandlerInvalidateRequest
			if err := json.NewDecoder(r.Body).Decode(&gotReq); err != nil {
				t.Fatalf("decode request: %v", err)
			}
			gotReqs = append(gotReqs, gotReq)
			w.WriteHeader(http.StatusOK)
		}))
	}
	srvA := newServer()
	defer srvA.Close()
	srvB := newServer()
	defer srvB.Close()

	t.Setenv("SERVICE_TOKEN", "svc-token")
	t.Setenv("CHANDLER_INTERNAL_URL", srvA.URL+","+srvB.URL)

	invalidateChandlerThumbnailCache("stream-id", []string{
		"thumbnails/stream-id/sprite.jpg",
		"thumbnails/stream-id/sprite.vtt",
		"thumbnails/stream-id/sprite.vtt",
	}, logging.NewLoggerWithService("test"))

	if len(gotAuths) != 2 {
		t.Fatalf("expected both Chandler instances to receive invalidation, got %d requests", len(gotAuths))
	}
	for _, gotAuth := range gotAuths {
		if gotAuth != "Bearer svc-token" {
			t.Fatalf("expected service token auth, got %q", gotAuth)
		}
	}
	for _, gotReq := range gotReqs {
		if gotReq.AssetKey != "stream-id" {
			t.Fatalf("expected asset key stream-id, got %q", gotReq.AssetKey)
		}
		if len(gotReq.Files) != 2 || gotReq.Files[0] != "sprite.jpg" || gotReq.Files[1] != "sprite.vtt" {
			t.Fatalf("unexpected files: %#v", gotReq.Files)
		}
	}
}

func TestInvalidateChandlerThumbnailCacheDeduplicatesBaseURLs(t *testing.T) {
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/internal/assets/cache/invalidate" {
			t.Fatalf("unexpected path %q", r.URL.Path)
		}
		calls++
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	t.Setenv("SERVICE_TOKEN", "svc-token")
	t.Setenv("CHANDLER_INTERNAL_URL", srv.URL+","+srv.URL+"/")

	invalidateChandlerThumbnailCache("stream-id", []string{
		"thumbnails/stream-id/sprite.jpg",
	}, logging.NewLoggerWithService("test"))

	if calls != 1 {
		t.Fatalf("expected one invalidation after URL dedupe, got %d", calls)
	}
}
