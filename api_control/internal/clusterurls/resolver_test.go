package clusterurls

import (
	"testing"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	pb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto"
)

func TestBuildThumbnailAssetsUsesPublicChandlerBaseOverride(t *testing.T) {
	t.Setenv("CHANDLER_BASE_URL", "http://localhost:18090/")

	r := NewResolver(nil, logging.NewLogger())
	got := r.BuildThumbnailAssets("demo-media", "stream-uuid")

	if got == nil {
		t.Fatal("expected thumbnail assets")
	}
	if got.GetPosterUrl() != "http://localhost:18090/assets/stream-uuid/poster.jpg" {
		t.Fatalf("poster URL = %q", got.GetPosterUrl())
	}
	if got.GetSpriteJpgUrl() != "http://localhost:18090/assets/stream-uuid/sprite.jpg" {
		t.Fatalf("sprite jpg URL = %q", got.GetSpriteJpgUrl())
	}
	if got.GetSpriteVttUrl() != "http://localhost:18090/assets/stream-uuid/sprite.vtt" {
		t.Fatalf("sprite vtt URL = %q", got.GetSpriteVttUrl())
	}
}

func TestBuildThumbnailAssetsUsesClusterSnapshotWithoutOverride(t *testing.T) {
	t.Setenv("CHANDLER_BASE_URL", "")

	r := NewResolver(nil, logging.NewLogger())
	snapshot := map[string]string{"demo-media": "https://chandler.demo.frameworks.network"}
	r.snapshot.Store(&snapshot)

	got := r.BuildThumbnailAssets("demo-media", "stream-uuid")
	if got == nil {
		t.Fatal("expected thumbnail assets")
	}
	if got.GetPosterUrl() != "https://chandler.demo.frameworks.network/assets/stream-uuid/poster.jpg" {
		t.Fatalf("poster URL = %q", got.GetPosterUrl())
	}
}

func TestChandlerBaseForNormalizesClusterBaseURL(t *testing.T) {
	t.Setenv("CHANDLER_BASE_URL", "")

	got := chandlerBaseFor(&pb.InfrastructureCluster{
		ClusterId:   "media-eu-1",
		ClusterName: "Media EU 1",
		BaseUrl:     "https://frameworks.network/",
	})

	if got != "https://chandler.media-eu-1.frameworks.network" {
		t.Fatalf("chandlerBaseFor normalized URL base = %q", got)
	}
}
