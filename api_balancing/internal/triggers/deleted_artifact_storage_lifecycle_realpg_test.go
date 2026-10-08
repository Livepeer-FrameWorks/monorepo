//go:build schema_verify

package triggers

import (
	"context"
	"database/sql"
	"fmt"
	"os/exec"
	"testing"
	"time"

	"frameworks/api_balancing/internal/control"
	"frameworks/api_balancing/internal/identity"
	dbsql "github.com/Livepeer-FrameWorks/monorepo/pkg/database/sql"
	commodorepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/testutil/dockerpg"
	"github.com/google/uuid"
	_ "github.com/lib/pq"
)

// startFoghornTriggersRealPG starts a throwaway PostgreSQL with the embedded
// foghorn.sql baseline applied.
func startFoghornTriggersRealPG(t *testing.T) *sql.DB {
	t.Helper()
	if _, lookErr := exec.LookPath("docker"); lookErr != nil {
		t.Skip("docker not available")
	}
	name := fmt.Sprintf("fw-foghorn-triggers-realpg-%d", time.Now().UnixNano())
	t.Cleanup(func() { _, _ = dockerpg.CLI("rm", "-fv", name) })
	image, err := dockerpg.PostgresImage()
	if err != nil {
		t.Fatalf("resolve PostgreSQL test image: %v", err)
	}
	if out, runErr := dockerpg.Run("run", "-d", "--name", name, "-P", "-e", "POSTGRES_PASSWORD=harness", image); runErr != nil {
		t.Fatalf("docker run: %v\n%s", runErr, out)
	}
	port, err := dockerpg.DiscoverPublishedHostPort(name, "5432/tcp")
	if err != nil {
		t.Fatal(err)
	}
	conn, err := sql.Open("postgres", fmt.Sprintf("postgres://postgres:harness@127.0.0.1:%s/postgres?sslmode=disable", port))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	if err = dockerpg.WaitReady(conn, name); err != nil {
		t.Fatal(err)
	}
	schema, err := dbsql.Content.ReadFile("schema/foghorn.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = conn.ExecContext(context.Background(), string(schema)); err != nil {
		t.Fatalf("apply foghorn.sql: %v", err)
	}
	return conn
}

// A node evicts its copy of an artifact after the artifact was deleted. Commodore no longer knows the hash, but
// the cell's foghorn.artifacts tombstone still names its owner, so the storage_lifecycle sample reaches Decklog
// attributed to that tenant: for a peer copy of another cell's artifact (federated pointer with an origin
// cluster) and for the owning cell's own artifact (no origin cluster, so the local one). A hash neither the
// cell nor Commodore knows is still dropped rather than attributed to anyone.
func TestStorageEvictionOfDeletedArtifactReachesDecklogWithOwnerTenant_RealPG(t *testing.T) {
	conn := startFoghornTriggersRealPG(t)
	ctx := context.Background()

	peerTenant, ownTenant := uuid.NewString(), uuid.NewString()
	peerStream, ownStream := uuid.NewString(), uuid.NewString()
	peerHash, ownHash, unknownHash := "a1b2c3d4e5f60718293a4b5c6d7e8f90", "0f1e2d3c4b5a69788796a5b4c3d2e1f0", "ffeeddccbbaa99887766554433221100"
	if _, err := conn.ExecContext(ctx, `
		INSERT INTO foghorn.artifacts (artifact_hash, artifact_type, tenant_id, stream_id, origin_cluster_id, federated_pointer, status)
		VALUES ($1, 'clip', $2, $3, 'eu-1', true, 'deleted'),
		       ($4, 'clip', $5, $6, NULL, false, 'deleted')`,
		peerHash, peerTenant, peerStream, ownHash, ownTenant, ownStream); err != nil {
		t.Fatalf("seed deleted artifacts: %v", err)
	}

	registry := control.NewStreamRegistry(nil, "us-1", time.Minute)
	identity.SetDefault(identity.NewResolver(identity.Config{
		RegistryArtifact: registry.ArtifactIdentityLayer(conn),
		CommodoreArtifact: func(context.Context, string, string) (identity.ArtifactIdentity, error) {
			return identity.ArtifactIdentity{}, identity.ErrNotFound
		},
	}))
	t.Cleanup(func() { identity.SetDefault(nil) })

	commodoreClient, closeCommodore := setupCommodoreResolveIdentifierClient(t, &commodorepb.ResolveIdentifierResponse{Found: false}, nil)
	t.Cleanup(closeCommodore)
	capture, decklogClient := startDecklogCapture(t)
	p := newTestProcessor(t)
	p.clusterID = "us-1"
	p.commodoreClient = commodoreClient
	p.decklogClient = decklogClient

	for _, hash := range []string{peerHash, ownHash, unknownHash} {
		trigger := &ipcpb.MistTrigger{
			TriggerType: "storage_lifecycle",
			NodeId:      "edge-us",
			TriggerPayload: &ipcpb.MistTrigger_StorageLifecycleData{StorageLifecycleData: &ipcpb.StorageLifecycleData{
				Action: ipcpb.StorageLifecycleData_ACTION_EVICTED, AssetType: "clip", AssetHash: hash, SizeBytes: 4096,
			}},
		}
		if _, _, err := p.handleStorageLifecycleData(trigger); err != nil {
			t.Fatalf("handleStorageLifecycleData(%s): %v", hash, err)
		}
	}

	forwarded := map[string]*ipcpb.MistTrigger{}
	for _, trigger := range capture.received() {
		if sld := trigger.GetStorageLifecycleData(); sld != nil {
			forwarded[sld.GetAssetHash()] = trigger
		}
	}
	for _, want := range []struct{ hash, tenant, stream, origin string }{
		{peerHash, peerTenant, peerStream, "eu-1"},
		{ownHash, ownTenant, ownStream, "us-1"},
	} {
		got := forwarded[want.hash]
		if got == nil {
			t.Fatalf("eviction of deleted artifact %s never reached Decklog; forwarded=%v", want.hash, forwarded)
		}
		sld := got.GetStorageLifecycleData()
		if got.GetTenantId() != want.tenant || sld.GetStreamId() != want.stream || got.GetArtifactHash() != want.hash ||
			sld.GetOriginClusterId() != want.origin {
			t.Fatalf("eviction of %s forwarded tenant=%q stream=%q artifact=%q origin=%q, want tenant=%q stream=%q artifact=%q origin=%q",
				want.hash, got.GetTenantId(), sld.GetStreamId(), got.GetArtifactHash(), sld.GetOriginClusterId(),
				want.tenant, want.stream, want.hash, want.origin)
		}
	}
	if got := forwarded[unknownHash]; got != nil {
		t.Fatalf("eviction of an unknown artifact was forwarded with tenant %q", got.GetTenantId())
	}
}
