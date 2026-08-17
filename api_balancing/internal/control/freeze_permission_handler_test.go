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
)

// freezePermHarness wires the handler dependencies for a clip freeze-permission request: an injected
// identity (tenant/origin), the tenant storage-routing entitlement (official cluster + peers) via the
// tenantStorageRoutingFn seam, a server-owned node identity, the local cluster, and a sqlmock DB. The
// destination is the routing's official cluster; local mint requires it to equal the local cluster.
type freezePermHarness struct {
	mock            sqlmock.Sqlmock
	stream          *mockStream
	protocolVersion int32 // captured session version passed to the handler; defaults to a staged-freeze-capable version
	connFence       int64 // captured session fence; defaults to 0 (matches the registered test conn)
}

func setupFreezePermTest(t *testing.T, artifactTenant, origin, nodeTenant, nodeCluster, localCluster string, routing tenantStorageRouting) *freezePermHarness {
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
	tenantStorageRoutingFn = func(context.Context, string) (tenantStorageRouting, bool) { return routing, true }
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
	sm.SetNodeConnectionInfo(context.Background(), "node-1", "node-1.local", nodeTenant, nodeCluster, nil)

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

// BYOC→platform-official succeeds even when the artifact's ORIGIN is the BYOC cluster (which may advertise
// its own storage): the freeze routes to the tenant's OFFICIAL durable backend, not origin-first to itself.
// A presigned PUT is minted, the attempt is claimed, and a SERVER-MINTED attempt id is returned.
func TestFreezePermission_BYOCToOfficialApproved(t *testing.T) {
	routing := tenantStorageRouting{officialCluster: "official-a", peers: storagePeerSet("official-a")}
	// Origin is the BYOC cluster "byoc-a"; official/local is "official-a". storage_cluster_id is NULL (not
	// yet stored), so there is no remote-skip and the destination resolves to official-a.
	h := setupFreezePermTest(t, "tenant-a", "byoc-a", "tenant-a", "byoc-a", "official-a", routing)
	h.expectMetadataAndPossession("byoc-a", "", "pending", true)
	h.mock.ExpectBegin()
	h.mock.ExpectExec(`UPDATE foghorn.artifacts\s+SET storage_location = 'freezing'`).
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
	routing := tenantStorageRouting{officialCluster: "official-a", peers: storagePeerSet("official-a")}
	h := setupFreezePermTest(t, "tenant-a", "byoc-a", "tenant-a", "byoc-a", "official-a", routing)
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
	routing := tenantStorageRouting{officialCluster: "official-a", peers: storagePeerSet("official-a")}
	h := setupFreezePermTest(t, "tenant-a", "byoc-a", "tenant-a", "byoc-a", "official-a", routing)
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
	routing := tenantStorageRouting{officialCluster: "official-a", peers: storagePeerSet("official-a")}
	h := setupFreezePermTest(t, "tenant-a", "byoc-a", "tenant-a", "byoc-a", "official-a", routing)
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
	routing := tenantStorageRouting{officialCluster: "official-a", peers: storagePeerSet("official-a")}
	h := setupFreezePermTest(t, "tenant-a", "byoc-a", "tenant-a", "byoc-a", "official-a", routing)
	h.connFence = 999 // the registered test conn has fence 0, so this session was superseded
	h.expectMetadataAndPossession("byoc-a", "", "pending", true)
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
	setupSeams := func(t *testing.T, routing tenantStorageRouting, canMint bool) sqlmock.Sqlmock {
		mock, _, _ := setupArtifactTestDeps(t)
		prevRouting := tenantStorageRoutingFn
		tenantStorageRoutingFn = func(context.Context, string) (tenantStorageRouting, bool) { return routing, true }
		prevMint := canMintOfficialLocallyFn
		canMintOfficialLocallyFn = func(context.Context, string, string) bool { return canMint }
		prevLocal := localClusterID
		SetLocalClusterID("official-a")
		t.Cleanup(func() {
			tenantStorageRoutingFn = prevRouting
			canMintOfficialLocallyFn = prevMint
			SetLocalClusterID(prevLocal)
		})
		sm := state.ResetDefaultManagerForTests()
		t.Cleanup(func() { state.ResetDefaultManagerForTests() })
		sm.SetNodeInfo("node-1", "n", true, nil, nil, "", "", nil)
		sm.SetNodeConnectionInfo(context.Background(), "node-1", "n", "tenant-a", "byoc-a", nil)
		return mock
	}
	routingA := tenantStorageRouting{officialCluster: "official-a", peers: storagePeerSet("official-a")}

	t.Run("authorized clip → assignment", func(t *testing.T) {
		mock := setupSeams(t, routingA, true)
		mock.ExpectBegin()
		mock.ExpectExec(`UPDATE foghorn.artifacts\s+SET storage_location = 'freezing'`).WillReturnResult(sqlmock.NewResult(0, 1))
		mock.ExpectExec("INSERT INTO foghorn.freeze_publication_ledger").WillReturnResult(sqlmock.NewResult(0, 4))
		mock.ExpectCommit()
		a, reason, ok := PrepareLocalFreezeAssignment(context.Background(), "clip", "hash-1", "tenant-a", "s", "mp4", "byoc-a", "node-1", 30_000_000_000)
		if !ok || reason != "" || a.AttemptID == "" || a.StagingURL == "" {
			t.Fatalf("expected assignment, got ok=%v reason=%q a=%+v", ok, reason, a)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("unauthorized source → cluster_not_authorized", func(t *testing.T) {
		setupSeams(t, tenantStorageRouting{officialCluster: "official-a", peers: storagePeerSet("official-a")}, true)
		// Artifact tenant is tenant-b but the node is tenant-a on byoc-a → not authorized; no claim.
		_, reason, ok := PrepareLocalFreezeAssignment(context.Background(), "clip", "hash-1", "tenant-b", "s", "mp4", "byoc-a", "node-1", 30_000_000_000)
		if ok || reason != "cluster_not_authorized" {
			t.Fatalf("expected cluster_not_authorized, got ok=%v reason=%q", ok, reason)
		}
	})

	t.Run("unsupported type → unsupported_asset_type", func(t *testing.T) {
		setupSeams(t, routingA, true)
		_, reason, ok := PrepareLocalFreezeAssignment(context.Background(), "dvr", "hash-1", "tenant-a", "s", "mp4", "byoc-a", "node-1", 30_000_000_000)
		if ok || reason != "unsupported_asset_type" {
			t.Fatalf("expected unsupported_asset_type, got ok=%v reason=%q", ok, reason)
		}
	})

	t.Run("official backing not local → official_storage_remote", func(t *testing.T) {
		setupSeams(t, routingA, false)
		_, reason, ok := PrepareLocalFreezeAssignment(context.Background(), "clip", "hash-1", "tenant-a", "s", "mp4", "byoc-a", "node-1", 30_000_000_000)
		if ok || reason != "official_storage_remote" {
			t.Fatalf("expected official_storage_remote, got ok=%v reason=%q", ok, reason)
		}
	})
}

// The tenant's official cluster is entitled, but its durable-storage backing is NOT this cell's local
// backend (the resolver-less fallback requires official == local cluster), so the freeze is rejected —
// serving/entitlement is not storage ownership. No claim fires.
func TestFreezePermission_OfficialBackingNotLocalRejected(t *testing.T) {
	routing := tenantStorageRouting{officialCluster: "official-a", peers: storagePeerSet("official-a")}
	// local cluster is "other-cell" (≠ official-a), and no resolver is wired, so canMintOfficialLocally is false.
	h := setupFreezePermTest(t, "tenant-a", "byoc-a", "tenant-a", "byoc-a", "other-cell", routing)
	h.expectMetadataAndPossession("byoc-a", "", "pending", true)

	h.run()

	resp := h.lastResponse(t)
	if resp.GetApproved() || resp.GetReason() != "official_storage_remote" {
		t.Fatalf("expected official_storage_remote rejection, got approved=%v reason=%q", resp.GetApproved(), resp.GetReason())
	}
	if err := h.mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// A node whose SERVER-OWNED tenant is NOT the artifact's tenant (and whose cluster is neither the origin
// nor the official) is denied even though it self-reports possession — and NO attempt is claimed.
func TestFreezePermission_CrossTenantSourceDenied(t *testing.T) {
	routing := tenantStorageRouting{officialCluster: "official-a", peers: storagePeerSet("official-a")}
	h := setupFreezePermTest(t, "tenant-a", "official-a", "tenant-b", "byoc-b", "official-a", routing)
	h.expectMetadataAndPossession("official-a", "", "pending", true) // possession passes; authority must still deny
	// No claim UPDATE expected.

	h.run()

	resp := h.lastResponse(t)
	if resp.GetApproved() || resp.GetReason() != "cluster_not_authorized" {
		t.Fatalf("expected cluster_not_authorized denial, got approved=%v reason=%q", resp.GetApproved(), resp.GetReason())
	}
	if err := h.mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// Expired access to the official cluster (absent from the active/unexpired peer set) denies storage — no
// claim fires.
func TestFreezePermission_ExpiredAccessDenied(t *testing.T) {
	// official-a is the routing official, but the tenant no longer holds active/unexpired access to it.
	routing := tenantStorageRouting{officialCluster: "official-a", peers: storagePeerSet("some-other")}
	h := setupFreezePermTest(t, "tenant-a", "official-a", "tenant-a", "byoc-a", "official-a", routing)
	h.expectMetadataAndPossession("official-a", "", "pending", true)

	h.run()

	resp := h.lastResponse(t)
	if resp.GetApproved() || resp.GetReason() != "cluster_not_authorized" {
		t.Fatalf("expected cluster_not_authorized denial, got approved=%v reason=%q", resp.GetApproved(), resp.GetReason())
	}
	if err := h.mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// A remote official cluster whose artifact is NOT yet durably synced is rejected: this cell cannot mint or
// verify a remote object, and remote attribution alone is not proof of durability. No claim fires.
func TestFreezePermission_RemoteNotDurableRejected(t *testing.T) {
	// Authorization passes (official-a is entitled), but the artifact is ALREADY attributed to a remote
	// durable cluster ("remote-x") and is NOT yet synced — this cell cannot verify remote durability.
	routing := tenantStorageRouting{officialCluster: "official-a", peers: storagePeerSet("official-a")}
	h := setupFreezePermTest(t, "tenant-a", "byoc-a", "tenant-a", "byoc-a", "official-a", routing)
	h.expectMetadataAndPossession("byoc-a", "remote-x", "pending", true) // remote storage_cluster_id, not synced

	h.run()

	resp := h.lastResponse(t)
	if resp.GetApproved() || resp.GetReason() != "remote_not_durable" {
		t.Fatalf("expected remote_not_durable rejection, got approved=%v reason=%q", resp.GetApproved(), resp.GetReason())
	}
	if err := h.mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
