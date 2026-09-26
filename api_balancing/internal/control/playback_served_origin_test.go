package control

import (
	"context"
	"database/sql"
	"strings"
	"sync"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"

	commodorepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"
	foghornfederationpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/foghorn_federation"
)

// servedClustersForTest makes this Foghorn serve the given clusters for one test.
func servedClustersForTest(t *testing.T, ids ...string) {
	t.Helper()
	prev := servedClusters.Load()
	t.Cleanup(func() { servedClusters.Store(prev) })
	served := &sync.Map{}
	for _, id := range ids {
		served.Store(id, struct{}{})
	}
	servedClusters.Store(served)
}

// recordingFedClient counts PrepareArtifact calls; any call fails the resolve.
type recordingFedClient struct{ calls int }

func (c *recordingFedClient) PrepareArtifact(ctx context.Context, clusterID, addr string, req *foghornfederationpb.PrepareArtifactRequest) (*foghornfederationpb.PrepareArtifactResponse, error) {
	c.calls++
	return &foghornfederationpb.PrepareArtifactResponse{}, nil
}

func TestIsRemoteOriginClusterRespectsServedClusters(t *testing.T) {
	servedClustersForTest(t, "central-primary", "demo-media")
	deps := &PlaybackDependencies{LocalClusterID: "central-primary"}
	for origin, want := range map[string]bool{
		"":                false,
		"central-primary": false,
		"demo-media":      false, // another media cluster of this cell
		"us-primary":      true,
	} {
		if got := isRemoteOriginCluster(origin, deps); got != want {
			t.Fatalf("isRemoteOriginCluster(%q) = %v, want %v", origin, got, want)
		}
	}
}

func TestLocalOriginRelayableReadsFreshCompleteOrigin(t *testing.T) {
	servedClustersForTest(t, "central-primary", "demo-media")
	mockDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer mockDB.Close()
	deps := &PlaybackDependencies{DB: mockDB, LocalClusterID: "central-primary"}

	mock.ExpectQuery("FROM foghorn.artifact_nodes").WithArgs("hash-1").
		WillReturnRows(sqlmock.NewRows([]string{"node_id", "base_url"}).AddRow("edge-node-1", "http://edge:8082"))
	if !localOriginRelayable(context.Background(), deps, "hash-1", "demo-media") {
		t.Fatal("a fresh complete origin copy in a served cluster must be relayable")
	}
	mock.ExpectQuery("FROM foghorn.artifact_nodes").WithArgs("hash-2").WillReturnError(sql.ErrNoRows)
	if localOriginRelayable(context.Background(), deps, "hash-2", "demo-media") {
		t.Fatal("without a fresh origin copy the relay has nothing to serve")
	}
	if localOriginRelayable(context.Background(), deps, "hash-3", "us-primary") {
		t.Fatal("another cell's artifact is never relayed from a local origin row")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// An artifact whose origin is another media cluster of this cell must not be
// prepared or adopted through federation, even when this replica has no row
// for it yet.
func TestArtifactPlaybackNeverFederatesAServedOrigin(t *testing.T) {
	servedClustersForTest(t, "central-primary", "demo-media")
	mockDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer mockDB.Close()
	mock.ExpectQuery("FROM foghorn.artifacts").WillReturnError(sql.ErrNoRows)

	fed := &recordingFedClient{}
	deps := &PlaybackDependencies{DB: mockDB, FedClient: fed, LocalClusterID: "central-primary"}
	_, err = resolveArtifactPlaybackWithResp(context.Background(), deps, "pb-1", &commodorepb.ResolveArtifactPlaybackIDResponse{
		ArtifactHash: "hash-1", TenantId: "tenant-1", ContentType: "vod", OriginClusterId: "demo-media",
	}, false)
	if fed.calls != 0 {
		t.Fatalf("served origin went through federation PrepareArtifact %d times", fed.calls)
	}
	if err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("got %v, want a local not-found", err)
	}
}
