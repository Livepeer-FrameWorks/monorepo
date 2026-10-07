//go:build schema_verify

package federation

import (
	"context"
	"errors"
	"testing"

	"frameworks/api_balancing/internal/control"
	"frameworks/api_balancing/internal/database/foghorndb"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	clusterpeerpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/cluster_peer"
	commodorepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"
	foghornfederationpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/foghorn_federation"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// originCell answers PrepareArtifact as the origin cell would: not found for an
// artifact it deleted, ready for one it still holds.
type originCell struct {
	deleted map[string]bool
	asked   []string
}

func (o *originCell) PrepareArtifact(_ context.Context, clusterID, _ string, req *foghornfederationpb.PrepareArtifactRequest) (*foghornfederationpb.PrepareArtifactResponse, error) {
	o.asked = append(o.asked, clusterID+"/"+req.GetArtifactId())
	if o.deleted[req.GetArtifactId()] {
		return &foghornfederationpb.PrepareArtifactResponse{Error: control.PrepareArtifactNotFoundRefusal}, nil
	}
	return &foghornfederationpb.PrepareArtifactResponse{Ready: true, Url: "https://s3.example/" + req.GetArtifactId()}, nil
}

type unreachableOrigin struct{}

func (unreachableOrigin) PrepareArtifact(context.Context, string, string, *foghornfederationpb.PrepareArtifactRequest) (*foghornfederationpb.PrepareArtifactResponse, error) {
	return nil, status.Error(codes.Unavailable, "connection refused")
}

type staticPeers map[string]string

func (p staticPeers) GetPeerAddr(clusterID string) string { return p[clusterID] }

// The non-owning cell retires its adopted pointer to an artifact the origin
// cell deleted as soon as the origin tells it, after confirming the deletion
// with that origin; from then on its playback answers not found instead of
// routing viewers to an edge. A pointer to an artifact the origin still holds
// is left serving, whoever sends the command.
func TestRetireArtifactPointerOnOriginDeletion_RealPG(t *testing.T) {
	conn := startFederationRealPG(t)
	const (
		tenant  = "00000000-0000-4000-8000-0000000000e1"
		deleted = "retiredpointer000000000000000001"
		live    = "livepointer000000000000000000001"
	)
	ctx := context.WithValue(context.Background(), ctxkeys.KeyAuthType, "service")
	queries := foghorndb.New(conn)
	for _, hash := range []string{deleted, live} {
		if _, err := queries.AdoptRemoteArtifact(ctx, foghorndb.AdoptRemoteArtifactParams{
			ArtifactHash: hash, ArtifactType: "vod", TenantID: tenant, InternalName: "vod+" + hash,
			Format: "mp4", SyncStatus: "synced", OriginClusterID: "cell-eu",
		}); err != nil {
			t.Fatal(err)
		}
	}

	origin := &originCell{deleted: map[string]bool{deleted: true}}
	srv := NewFederationServer(FederationServerConfig{
		Logger: logging.NewLogger(), ClusterID: "cell-us", DB: conn,
		PeerManager: staticPeers{"cell-eu": "foghorn.eu:18019"}, AllowFederationMutations: true,
	})
	srv.originPreparer = origin

	retire := func(hash string) bool {
		t.Helper()
		resp, err := srv.ForwardArtifactCommand(ctx, &foghornfederationpb.ForwardArtifactCommandRequest{
			Command: RetireArtifactPointerCommand, ArtifactHash: hash, TenantId: tenant,
		})
		if err != nil {
			t.Fatalf("retire %s: %v", hash, err)
		}
		return resp.GetHandled()
	}
	pointerStatus := func(hash string) string {
		t.Helper()
		var status string
		if err := conn.QueryRow(`SELECT status FROM foghorn.artifacts WHERE artifact_hash = $1`, hash).Scan(&status); err != nil {
			t.Fatal(err)
		}
		return status
	}

	if !retire(deleted) {
		t.Fatalf("pointer to the artifact its origin deleted was not retired (status %q)", pointerStatus(deleted))
	}
	if got := pointerStatus(deleted); got != "deleted" {
		t.Fatalf("retired pointer status = %q, want deleted", got)
	}
	if retire(live) {
		t.Fatal("pointer to an artifact its origin still holds was retired")
	}
	if got := pointerStatus(live); got != "ready" {
		t.Fatalf("live pointer status = %q, want ready", got)
	}
	if len(origin.asked) != 2 || origin.asked[0] != "cell-eu/"+deleted || origin.asked[1] != "cell-eu/"+live {
		t.Fatalf("origin asked %v, want each pointer confirmed with cell-eu", origin.asked)
	}

	// An origin that cannot be reached when asked leaves the pointer serving and
	// reports the failure; the signed tombstone retires it later.
	unreachable := "unreachableorigin000000000000001"
	if _, err := queries.AdoptRemoteArtifact(ctx, foghorndb.AdoptRemoteArtifactParams{
		ArtifactHash: unreachable, ArtifactType: "vod", TenantID: tenant, InternalName: "vod+" + unreachable,
		Format: "mp4", SyncStatus: "synced", OriginClusterID: "cell-eu",
	}); err != nil {
		t.Fatal(err)
	}
	srv.originPreparer = unreachableOrigin{}
	resp, err := srv.ForwardArtifactCommand(ctx, &foghornfederationpb.ForwardArtifactCommandRequest{
		Command: RetireArtifactPointerCommand, ArtifactHash: unreachable, TenantId: tenant,
	})
	if err == nil || resp.GetHandled() {
		t.Fatalf("retire with the origin unreachable = %+v, %v; want an error and no retirement", resp, err)
	}
	if got := pointerStatus(unreachable); got != "ready" {
		t.Fatalf("pointer status with the origin unreachable = %q, want ready", got)
	}
	srv.originPreparer = origin

	// Playback of the retired pointer now resolves to not found.
	peer := &clusterpeerpb.TenantClusterPeer{ClusterId: "cell-eu"}
	_, err = control.ResolveArtifactPlaybackWithIdentity(ctx, &control.PlaybackDependencies{
		DB: conn, FedClient: origin, PeerResolver: staticPeers{"cell-eu": "foghorn.eu:18019"}, LocalClusterID: "cell-us",
	}, "playback-"+deleted, &commodorepb.ResolveArtifactPlaybackIDResponse{
		Found: true, TenantId: tenant, ContentType: "vod", ArtifactHash: deleted, OriginClusterId: "cell-eu",
		ClusterPeers: []*clusterpeerpb.TenantClusterPeer{peer}, AuthorityClusterPeers: []*clusterpeerpb.TenantClusterPeer{peer},
	})
	if !errors.Is(err, control.ErrPlaybackContentNotFound) {
		t.Fatalf("playback of the retired pointer = %v, want %v", err, control.ErrPlaybackContentNotFound)
	}

	// A re-resolve never re-adopts the retired pointer.
	if n, err := queries.AdoptRemoteArtifact(ctx, foghorndb.AdoptRemoteArtifactParams{
		ArtifactHash: deleted, ArtifactType: "vod", TenantID: tenant, SyncStatus: "synced", OriginClusterID: "cell-eu",
	}); err != nil || n != 0 {
		t.Fatalf("re-adoption of a retired pointer = %d, %v, want 0", n, err)
	}
}
