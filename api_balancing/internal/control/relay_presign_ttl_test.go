package control

import (
	"fmt"
	"testing"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	foghornfederationpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/foghorn_federation"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
)

func sigV4URL(base string, signedAt time.Time, lifetime time.Duration) string {
	return fmt.Sprintf("%s?X-Amz-Algorithm=AWS4-HMAC-SHA256&X-Amz-Date=%s&X-Amz-Expires=%d&X-Amz-Signature=sig",
		base, signedAt.UTC().Format("20060102T150405Z"), int64(lifetime.Seconds()))
}

// The origin cell signs its cross-cell GETs for federatedPresignTTL. The
// requesting cell must hand Helmsman a resolve TTL that ends before the
// earliest signed expiry, or Helmsman replays an expired URL and Mist reads 403.
func TestCrossClusterPresignedResolveTTLNeverOutlivesSignedURL(t *testing.T) {
	previous := crossClusterDeps
	t.Cleanup(func() { SetCrossClusterArtifactDeps(previous) })
	signedAt := time.Now().Add(-2 * time.Minute)
	media := sigV4URL("https://storage.example/media.mkv", signedAt, 15*time.Minute)
	index := sigV4URL("https://storage.example/media.mkv.dtsh", signedAt, 15*time.Minute)
	fed := &fakeCrossClusterFedClient{responses: []*foghornfederationpb.PrepareArtifactResponse{{
		Ready: true, Url: media, DtshUrl: index, Format: "mkv",
	}}}
	SetCrossClusterArtifactDeps(makeCCDeps(fed, map[string]string{"origin": "peer:443"}))

	response := &ipcpb.RelayResolveResponse{}
	fillCrossClusterArtifact(t.Context(), &ipcpb.RelayResolveRequest{AssetHash: "hash"}, response, logging.NewLogger(), "", "origin", "tenant", "clip", nil)

	ttl := time.Duration(response.GetUrlTtlSeconds()) * time.Second
	remaining := time.Until(signedAt.Add(15 * time.Minute))
	if ttl <= 0 || ttl > remaining-presignExpiryMargin+time.Second {
		t.Fatalf("forwarded TTL %s must be positive and end at least %s before the signed expiry (%s left)", ttl, presignExpiryMargin, remaining.Round(time.Second))
	}
}

func TestCrossClusterPresignedResolveTTLWithoutSignedWindowIsConservative(t *testing.T) {
	previous := crossClusterDeps
	t.Cleanup(func() { SetCrossClusterArtifactDeps(previous) })
	fed := &fakeCrossClusterFedClient{responses: []*foghornfederationpb.PrepareArtifactResponse{{
		Ready: true, Url: "https://storage.example/media", Format: "mkv",
	}}}
	SetCrossClusterArtifactDeps(makeCCDeps(fed, map[string]string{"origin": "peer:443"}))

	response := &ipcpb.RelayResolveResponse{}
	fillCrossClusterArtifact(t.Context(), &ipcpb.RelayResolveRequest{AssetHash: "hash"}, response, logging.NewLogger(), "", "origin", "tenant", "vod", nil)

	if ttl := time.Duration(response.GetUrlTtlSeconds()) * time.Second; ttl <= 0 || ttl > unsignedPresignResolveTTL {
		t.Fatalf("forwarded TTL %s for a URL without a readable window, want (0, %s]", ttl, unsignedPresignResolveTTL)
	}
}

func TestPresignedResolveTTLBounds(t *testing.T) {
	now := time.Date(2026, 10, 12, 9, 0, 0, 0, time.UTC)
	long := sigV4URL("https://s/a", now, time.Hour)
	short := sigV4URL("https://s/b", now.Add(-14*time.Minute), 15*time.Minute)
	if got := presignedResolveTTL(now, relayURLTTL, long); got != time.Hour-presignExpiryMargin {
		t.Fatalf("1 h URL: ttl=%s want %s", got, time.Hour-presignExpiryMargin)
	}
	if got := presignedResolveTTL(now, 10*time.Minute, long); got != 10*time.Minute {
		t.Fatalf("ceiling must cap the TTL: got %s", got)
	}
	if got := presignedResolveTTL(now, relayURLTTL, long, short); got != 0 {
		t.Fatalf("a URL inside the margin must not be cached: got %s", got)
	}
}
