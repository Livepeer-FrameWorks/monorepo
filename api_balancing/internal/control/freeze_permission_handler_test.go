package control

import (
	"context"
	"errors"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"

	"frameworks/api_balancing/internal/identity"
	"frameworks/api_balancing/internal/state"
	"frameworks/api_balancing/internal/storage"
)

// freezePermHarness wires the handler dependencies for a clip freeze-permission request: an injected
// identity (tenant/origin), a server-owned node identity, the local cluster, and a sqlmock DB. The durable
// destination is the artifact's origin cluster; without a resolver, local mint requires the origin to be this
// Foghorn's configured cluster. The tenant cluster-routing seam FAILS, proving freeze never consults a
// tenant-level cluster.
type freezePermHarness struct {
	mock            sqlmock.Sqlmock
	stream          *mockStream
	protocolVersion int32 // captured session version passed to the handler; defaults to a staged-freeze-capable version
	connFence       int64 // captured session fence; defaults to 0 (matches the registered test conn)
}

func setupFreezePermTest(t *testing.T, artifactTenant, origin, nodeCluster, localCluster string) *freezePermHarness {
	t.Helper()
	mock, _, _ := setupArtifactTestDeps(t)

	identity.SetDefault(identity.NewResolver(identity.Config{
		RegistryArtifact: func(_ context.Context, hash string) (identity.ArtifactIdentity, error) {
			return identity.ArtifactIdentity{
				ArtifactHash: hash, Kind: "clip", TenantID: artifactTenant,
				StreamInternalName: "s-int", OriginClusterID: origin,
			}, nil
		},
	}))
	t.Cleanup(func() { identity.SetDefault(nil) })

	prevRouting := tenantStorageRoutingFn
	tenantStorageRoutingFn = func(context.Context, string) (tenantStorageRouting, bool) { return tenantStorageRouting{}, false }
	t.Cleanup(func() { tenantStorageRoutingFn = prevRouting })

	prevFactory := storageResolverFactory
	SetStorageResolverFactory(nil)
	t.Cleanup(func() { SetStorageResolverFactory(prevFactory) })

	prevLocal := localClusterID
	SetLocalClusterID(localCluster)
	t.Cleanup(func() { SetLocalClusterID(prevLocal) })

	sm := state.ResetDefaultManagerForTests()
	t.Cleanup(func() { state.ResetDefaultManagerForTests() })
	sm.SetNodeInfo("node-1", "node-1.local", true, nil, nil, "", "", nil)
	sm.SetNodeConnectionInfo(context.Background(), "node-1", "node-1.local", "", nodeCluster, nil)

	// Register a control connection so the handler's current-session (fence) binding sees node-1 as owned.
	// SetupTestRegistry installs a conn with fence 0; run() passes connFence 0 to match.
	h := &freezePermHarness{mock: mock, stream: &mockStream{}, protocolVersion: FreezeStagedProtocolMin}
	t.Cleanup(SetupTestRegistry("node-1", h.stream))
	return h
}

// expectMetadataAndPossession queues the tenant-scoped metadata read + the possession EXISTS check (both
// fire before the authorization gate). origin/storageCluster/syncStatus are what the row reports.
func (h *freezePermHarness) expectMetadataAndPossession(origin, storageCluster, syncStatus string, holds bool) {
	h.mock.ExpectQuery(`SELECT stream_internal_name, origin_cluster_id, storage_cluster_id, sync_status, COALESCE\(format, ''\)`).
		WillReturnRows(sqlmock.NewRows([]string{"stream_internal_name", "origin_cluster_id", "storage_cluster_id", "sync_status", "format"}).
			AddRow("s-int", origin, storageCluster, syncStatus, "mp4"))
	h.mock.ExpectQuery(`SELECT EXISTS`).
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(holds))
}

func (h *freezePermHarness) run() {
	processFreezePermissionRequest(&ipcpb.FreezePermissionRequest{
		RequestId: "req-1", AssetType: "clip", AssetHash: "hash-1", SizeBytes: 100,
	}, "node-1", h.protocolVersion, h.connFence, h.stream, logging.NewLogger())
}

func (h *freezePermHarness) lastResponse(t *testing.T) *ipcpb.FreezePermissionResponse {
	t.Helper()
	if len(h.stream.sent) == 0 {
		t.Fatal("no response sent")
	}
	resp := h.stream.sent[len(h.stream.sent)-1].GetFreezePermissionResponse()
	if resp == nil {
		t.Fatal("last message is not a FreezePermissionResponse")
	}
	return resp
}

// A clip produced in the EU cluster freezes into EU storage: the EU cell's node uploads, the EU Foghorn mints
// against its own backend, and storage_cluster_id stays NULL (= origin). The tenant-level routing seam fails in
// this harness, so approval proves no tenant "official" cluster is consulted.
func TestFreezePermission_OriginClusterClipApproved(t *testing.T) {
	h := setupFreezePermTest(t, "tenant-a", "platform-eu", "platform-eu", "platform-eu")
	h.expectMetadataAndPossession("platform-eu", "", "pending", true)
	h.mock.ExpectBegin()
	h.mock.ExpectExec(`UPDATE foghorn.artifacts\s+SET storage_location = 'freezing'`).
		WithArgs(sqlmock.AnyArg(), "node-1", "", sqlmock.AnyArg(), sqlmock.AnyArg(), "hash-1", "tenant-a").
		WillReturnResult(sqlmock.NewResult(0, 1))
	h.mock.ExpectExec("INSERT INTO foghorn.freeze_publication_ledger").WillReturnResult(sqlmock.NewResult(0, 4))
	h.mock.ExpectCommit()

	h.run()

	resp := h.lastResponse(t)
	if !resp.GetApproved() || resp.GetPresignedPutUrl() == "" {
		t.Fatalf("expected approval with a presigned URL, got approved=%v reason=%q", resp.GetApproved(), resp.GetReason())
	}
	if resp.GetAttemptId() == "" || resp.GetAttemptId() == "req-1" {
		t.Fatalf("expected a SERVER-MINTED attempt_id (not the node request id), got %q", resp.GetAttemptId())
	}
	if err := h.mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// A session that declared a pre-staged-freeze protocol version is DENIED before any DB work — admission is
// bound to the version CAPTURED for this connection, not re-looked-up (a reconnect cannot change the verdict).
func TestFreezePermission_OldProtocolDenied(t *testing.T) {
	h := setupFreezePermTest(t, "tenant-a", "platform-eu", "platform-eu", "platform-eu")
	h.protocolVersion = FreezeStagedProtocolMin - 1 // pre-staged-freeze sidecar
	// No metadata/possession/claim expectations: denial precedes all DB work.

	h.run()

	resp := h.lastResponse(t)
	if resp.GetApproved() {
		t.Fatal("a pre-staged-freeze session must be denied")
	}
	if resp.GetReason() != "sidecar_protocol_unsupported" {
		t.Fatalf("expected reason sidecar_protocol_unsupported, got %q", resp.GetReason())
	}
	if err := h.mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestFreezePermission_UnknownAssetHasStructuredReason(t *testing.T) {
	h := setupFreezePermTest(t, "tenant-a", "platform-eu", "platform-eu", "platform-eu")
	identity.SetDefault(identity.NewResolver(identity.Config{
		RegistryArtifact: func(context.Context, string) (identity.ArtifactIdentity, error) {
			return identity.ArtifactIdentity{}, identity.ErrNotFound
		},
	}))

	h.run()

	resp := h.lastResponse(t)
	if resp.GetApproved() || resp.GetReason() != "asset_not_found" {
		t.Fatalf("response=%+v, want denied asset_not_found", resp)
	}
	if err := h.mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestFreezePermission_IdentityOutageFailsClosed(t *testing.T) {
	h := setupFreezePermTest(t, "tenant-a", "platform-eu", "platform-eu", "platform-eu")
	identity.SetDefault(identity.NewResolver(identity.Config{
		RegistryArtifact: func(context.Context, string) (identity.ArtifactIdentity, error) {
			return identity.ArtifactIdentity{}, errors.New("registry unavailable")
		},
	}))

	h.run()

	resp := h.lastResponse(t)
	if resp.GetApproved() || resp.GetReason() != "identity_unavailable" {
		t.Fatalf("response=%+v, want denied identity_unavailable", resp)
	}
	if err := h.mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// The AUTHORITATIVE final owning send validates the CURRENT session's protocol: a proactive/relayed freeze
// to a locally-connected sidecar that (re)connected with a pre-staged-freeze protocol is refused with a
// non-relayable error, so an old sidecar can never receive a staged freeze even when the reconciler's
// advisory pre-check was bypassed (peer-owned) or raced a reconnect.
func TestSendLocalFreezeRequest_OldProtocolRejected(t *testing.T) {
	t.Cleanup(SetupTestRegistry("node-x", &mockStream{}))
	registry.mu.Lock()
	registry.conns["node-x"].protocolVersion = FreezeStagedProtocolMin - 1
	registry.mu.Unlock()

	err := SendLocalFreezeRequest("node-x", &ipcpb.FreezeRequest{RequestId: "r"})
	if !errors.Is(err, ErrFreezeProtocolUnsupported) {
		t.Fatalf("expected ErrFreezeProtocolUnsupported for an old sidecar, got %v", err)
	}
	// A current-protocol conn (SetupTestRegistry default) is delivered.
	t.Cleanup(SetupTestRegistry("node-y", &mockStream{}))
	if err := SendLocalFreezeRequest("node-y", &ipcpb.FreezeRequest{RequestId: "r"}); err != nil {
		t.Fatalf("current-protocol sidecar must accept the send, got %v", err)
	}
}

// A request dispatched from a SUPERSEDED connection (its captured fence no longer matches the node's current
// registered connection) is ignored before any claim/presign — the newer connection re-drives its freezes.
func TestFreezePermission_SupersededConnectionIgnored(t *testing.T) {
	h := setupFreezePermTest(t, "tenant-a", "platform-eu", "platform-eu", "platform-eu")
	h.connFence = 999 // the registered test conn has fence 0, so this session was superseded
	h.expectMetadataAndPossession("platform-eu", "", "pending", true)
	// No claim UPDATE expected: the fence check aborts before PrepareLocalFreezeAssignment.

	h.run()

	if len(h.stream.sent) != 0 {
		t.Fatalf("a superseded connection must get no response, sent %d", len(h.stream.sent))
	}
	if err := h.mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// PrepareLocalFreezeAssignment is the ONE shared contract both the interactive and reconciler paths use.
func TestPrepareLocalFreezeAssignment(t *testing.T) {
	setupSeams := func(t *testing.T, nodeCluster string, canMint bool) sqlmock.Sqlmock {
		mock, _, _ := setupArtifactTestDeps(t)
		prevRouting := tenantStorageRoutingFn
		tenantStorageRoutingFn = func(context.Context, string) (tenantStorageRouting, bool) { return tenantStorageRouting{}, false }
		prevMint := canMintOriginLocallyFn
		canMintOriginLocallyFn = func(context.Context, string, string) bool { return canMint }
		prevLocal := localClusterID
		SetLocalClusterID("platform-eu")
		t.Cleanup(func() {
			tenantStorageRoutingFn = prevRouting
			canMintOriginLocallyFn = prevMint
			SetLocalClusterID(prevLocal)
		})
		sm := state.ResetDefaultManagerForTests()
		t.Cleanup(func() { state.ResetDefaultManagerForTests() })
		sm.SetNodeInfo("node-1", "n", true, nil, nil, "", "", nil)
		sm.SetNodeConnectionInfo(context.Background(), "node-1", "n", "", nodeCluster, nil)
		return mock
	}

	t.Run("origin-cluster node → assignment into origin", func(t *testing.T) {
		mock := setupSeams(t, "platform-eu", true)
		mock.ExpectBegin()
		mock.ExpectExec(`UPDATE foghorn.artifacts\s+SET storage_location = 'freezing'`).WillReturnResult(sqlmock.NewResult(0, 1))
		mock.ExpectExec("INSERT INTO foghorn.freeze_publication_ledger").WillReturnResult(sqlmock.NewResult(0, 4))
		mock.ExpectCommit()
		a, reason, ok := PrepareLocalFreezeAssignment(context.Background(), "clip", "hash-1", "tenant-a", "s", "mp4", "platform-eu", "node-1", 30_000_000_000)
		if !ok || reason != "" || a.AttemptID == "" || a.StagingURL == "" {
			t.Fatalf("expected assignment, got ok=%v reason=%q a=%+v", ok, reason, a)
		}
		if a.DestCluster != "platform-eu" {
			t.Fatalf("DestCluster = %q, want the origin platform-eu", a.DestCluster)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("node outside the origin cluster → cluster_not_authorized", func(t *testing.T) {
		setupSeams(t, "platform-us", true)
		_, reason, ok := PrepareLocalFreezeAssignment(context.Background(), "clip", "hash-1", "tenant-a", "s", "mp4", "platform-eu", "node-1", 30_000_000_000)
		if ok || reason != "cluster_not_authorized" {
			t.Fatalf("expected cluster_not_authorized, got ok=%v reason=%q", ok, reason)
		}
	})

	t.Run("unknown origin → origin_cluster_unknown", func(t *testing.T) {
		setupSeams(t, "platform-eu", true)
		_, reason, ok := PrepareLocalFreezeAssignment(context.Background(), "clip", "hash-1", "tenant-a", "s", "mp4", "", "node-1", 30_000_000_000)
		if ok || reason != "origin_cluster_unknown" {
			t.Fatalf("expected origin_cluster_unknown, got ok=%v reason=%q", ok, reason)
		}
	})

	t.Run("unsupported type → unsupported_asset_type", func(t *testing.T) {
		setupSeams(t, "platform-eu", true)
		_, reason, ok := PrepareLocalFreezeAssignment(context.Background(), "dvr", "hash-1", "tenant-a", "s", "mp4", "platform-eu", "node-1", 30_000_000_000)
		if ok || reason != "unsupported_asset_type" {
			t.Fatalf("expected unsupported_asset_type, got ok=%v reason=%q", ok, reason)
		}
	})

	t.Run("origin backing not local → origin_storage_remote", func(t *testing.T) {
		setupSeams(t, "platform-eu", false)
		_, reason, ok := PrepareLocalFreezeAssignment(context.Background(), "clip", "hash-1", "tenant-a", "s", "mp4", "platform-eu", "node-1", 30_000_000_000)
		if ok || reason != "origin_storage_remote" {
			t.Fatalf("expected origin_storage_remote, got ok=%v reason=%q", ok, reason)
		}
	})
}

// With the production resolver wired, an EU-origin clip resolves to the EU cell's own backend and is minted
// locally, while a clip whose origin is the US cell resolves to federation and is refused here.
func TestPrepareLocalFreezeAssignment_ResolverUsesOrigin(t *testing.T) {
	mock, _, _ := setupArtifactTestDeps(t)
	euBacking := storage.S3Backing{Bucket: "frameworks-eu", Endpoint: "https://eu.example", Region: "eu-central-1"}
	prevFactory := storageResolverFactory
	SetStorageResolverFactory(func(context.Context, string) *storage.ClusterResolver {
		return &storage.ClusterResolver{
			LocalClusterID:       "platform-eu",
			LocalClusterServed:   func(id string) bool { return id == "platform-eu" },
			LocalS3Backing:       euBacking,
			LocalS3ClientPresent: true,
			AdvertisedBacking: func(id string) (storage.S3Backing, bool) {
				switch id {
				case "platform-eu":
					return euBacking, true
				case "platform-us":
					return storage.S3Backing{Bucket: "frameworks-us", Endpoint: "https://us.example", Region: "us-east-1"}, true
				}
				return storage.S3Backing{}, false
			},
		}
	})
	prevLocal := localClusterID
	SetLocalClusterID("platform-eu")
	t.Cleanup(func() {
		SetStorageResolverFactory(prevFactory)
		SetLocalClusterID(prevLocal)
	})
	sm := state.ResetDefaultManagerForTests()
	t.Cleanup(func() { state.ResetDefaultManagerForTests() })
	sm.SetNodeInfo("node-eu", "n", true, nil, nil, "", "", nil)
	sm.SetNodeConnectionInfo(context.Background(), "node-eu", "n", "", "platform-eu", nil)
	sm.SetNodeInfo("node-us", "n", true, nil, nil, "", "", nil)
	sm.SetNodeConnectionInfo(context.Background(), "node-us", "n", "", "platform-us", nil)

	mock.ExpectBegin()
	mock.ExpectExec(`UPDATE foghorn.artifacts\s+SET storage_location = 'freezing'`).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("INSERT INTO foghorn.freeze_publication_ledger").WillReturnResult(sqlmock.NewResult(0, 4))
	mock.ExpectCommit()
	a, reason, ok := PrepareLocalFreezeAssignment(context.Background(), "clip", "hash-eu", "tenant-a", "s", "mp4", "platform-eu", "node-eu", 30_000_000_000)
	if !ok || a.DestCluster != "platform-eu" {
		t.Fatalf("EU clip: ok=%v reason=%q dest=%q, want approved into platform-eu", ok, reason, a.DestCluster)
	}

	_, reason, ok = PrepareLocalFreezeAssignment(context.Background(), "clip", "hash-us", "tenant-a", "s", "mp4", "platform-us", "node-us", 30_000_000_000)
	if ok || reason != "origin_storage_remote" {
		t.Fatalf("US clip on the EU Foghorn: ok=%v reason=%q, want origin_storage_remote", ok, reason)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// The origin's durable-storage backing is NOT this cell's local backend (the resolver-less fallback requires the
// origin to be this Foghorn's cluster), so the freeze is rejected. No claim fires.
func TestFreezePermission_OriginBackingNotLocalRejected(t *testing.T) {
	h := setupFreezePermTest(t, "tenant-a", "platform-eu", "platform-eu", "other-cell")
	h.expectMetadataAndPossession("platform-eu", "", "pending", true)

	h.run()

	resp := h.lastResponse(t)
	if resp.GetApproved() || resp.GetReason() != "origin_storage_remote" {
		t.Fatalf("expected origin_storage_remote rejection, got approved=%v reason=%q", resp.GetApproved(), resp.GetReason())
	}
	if err := h.mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// A node on another cluster that holds a copy of an EU-origin clip (a warm copy elsewhere) may not upload it: only
// the origin cluster's nodes write into its storage. No attempt is claimed.
func TestFreezePermission_NodeOutsideOriginDenied(t *testing.T) {
	h := setupFreezePermTest(t, "tenant-a", "platform-eu", "platform-us", "platform-eu")
	h.expectMetadataAndPossession("platform-eu", "", "pending", true) // possession passes; authority must still deny

	h.run()

	resp := h.lastResponse(t)
	if resp.GetApproved() || resp.GetReason() != "cluster_not_authorized" {
		t.Fatalf("expected cluster_not_authorized denial, got approved=%v reason=%q", resp.GetApproved(), resp.GetReason())
	}
	if err := h.mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// An artifact already attributed to a remote storage cluster that is NOT yet durably synced is rejected: this
// cell cannot mint or verify a remote object, and remote attribution alone is not proof of durability.
func TestFreezePermission_RemoteNotDurableRejected(t *testing.T) {
	h := setupFreezePermTest(t, "tenant-a", "platform-eu", "platform-eu", "platform-eu")
	h.expectMetadataAndPossession("platform-eu", "remote-x", "pending", true) // remote storage_cluster_id, not synced

	h.run()

	resp := h.lastResponse(t)
	if resp.GetApproved() || resp.GetReason() != "remote_not_durable" {
		t.Fatalf("expected remote_not_durable rejection, got approved=%v reason=%q", resp.GetApproved(), resp.GetReason())
	}
	if err := h.mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
