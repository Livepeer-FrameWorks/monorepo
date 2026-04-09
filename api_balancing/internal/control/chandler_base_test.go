package control

import (
	"context"
	"errors"
	"testing"

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
