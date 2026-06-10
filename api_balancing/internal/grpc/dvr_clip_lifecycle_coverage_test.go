package grpc

import (
	"context"
	"testing"

	"frameworks/api_balancing/internal/control"
	"frameworks/api_balancing/internal/state"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	foghorncontrolpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/foghorn_control"
	sharedpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/shared"
	"github.com/sirupsen/logrus"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

// newLifecycleServer wires a FoghornGRPCServer over a fresh sqlmock with every
// optional client left nil (federation/purser/cleaner). The nil federation
// client makes forwardArtifactToFederation a no-op, so a tenant-filtered miss
// surfaces as NotFound rather than being masked by a peer round-trip.
func newLifecycleServer(t *testing.T) (*FoghornGRPCServer, sqlmock.Sqlmock) {
	t.Helper()
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return &FoghornGRPCServer{db: db, logger: logrus.New()}, mock
}

// ---- DeleteClip: tenant-ownership guard + missing-hash + happy soft-delete ----

// Invariant: clip_hash is a hard precondition; an empty hash never reaches the DB.
func TestDeleteClip_MissingHashInvalidArgument(t *testing.T) {
	srv, mock := newLifecycleServer(t)
	_, err := srv.DeleteClip(context.Background(), &sharedpb.DeleteClipRequest{ClipHash: ""})
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("expected InvalidArgument, got %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}

// Invariant: tenant isolation. The existence lookup filters by tenant_id, so a
// clip owned by another tenant returns zero rows; with no federation client the
// command must reject as NotFound and issue NO soft-delete UPDATE.
func TestDeleteClip_MismatchedTenantNotFound(t *testing.T) {
	srv, mock := newLifecycleServer(t)
	mock.ExpectQuery(`SELECT status, size_bytes`).
		WithArgs("clip-h", "tenant-intruder").
		WillReturnRows(sqlmock.NewRows([]string{
			"status", "size_bytes", "retention_until", "stream_internal_name",
			"tenant_id", "user_id", "format", "storage_cluster_id", "origin_cluster_id",
		})) // zero rows == sql.ErrNoRows

	_, err := srv.DeleteClip(context.Background(), &sharedpb.DeleteClipRequest{
		ClipHash: "clip-h",
		TenantId: "tenant-intruder",
	})
	if status.Code(err) != codes.NotFound {
		t.Fatalf("expected NotFound for foreign-tenant clip, got %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}

// Invariant: the happy path issues exactly the tenant-scoped soft-delete UPDATE
// (status->'deleted') and the processing-job cancellation UPDATE, both filtered
// by artifact_hash + tenant_id. artifactCleaner is nil so S3 cleanup defers but
// the soft-delete still succeeds.
func TestDeleteClip_SoftDeleteIssuesTenantScopedUpdate(t *testing.T) {
	srv, mock := newLifecycleServer(t)
	mock.ExpectQuery(`SELECT status, size_bytes`).
		WithArgs("clip-h", "tenant-a").
		WillReturnRows(sqlmock.NewRows([]string{
			"status", "size_bytes", "retention_until", "stream_internal_name",
			"tenant_id", "user_id", "format", "storage_cluster_id", "origin_cluster_id",
		}).AddRow("ready", nil, nil, "live+stream-1", "tenant-a", "user-1", "mkv", nil, nil))
	// node lookup: no live storage node -> skip Helmsman send
	mock.ExpectQuery(`SELECT node_id FROM foghorn.artifact_nodes`).
		WithArgs("clip-h").
		WillReturnRows(sqlmock.NewRows([]string{"node_id"}))
	mock.ExpectExec(`UPDATE foghorn.artifacts SET status = 'deleted'`).
		WithArgs("clip-h", "tenant-a").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`UPDATE foghorn.processing_jobs`).
		WithArgs("clip-h").
		WillReturnResult(sqlmock.NewResult(0, 0))

	resp, err := srv.DeleteClip(context.Background(), &sharedpb.DeleteClipRequest{
		ClipHash: "clip-h",
		TenantId: "tenant-a",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !resp.Success {
		t.Fatalf("expected success, got %+v", resp)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}

// ---- StopDVR: tenant-ownership guard + missing-hash + happy stopping path ----

// Invariant: dvr_hash is a hard precondition; an empty hash never reaches the DB.
func TestStopDVR_MissingHashInvalidArgument(t *testing.T) {
	srv, mock := newLifecycleServer(t)
	_, err := srv.StopDVR(context.Background(), &sharedpb.StopDVRRequest{DvrHash: ""})
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("expected InvalidArgument, got %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}

// Invariant: tenant isolation. The artifact lookup filters by tenant_id; a DVR
// owned by another tenant returns zero rows and, with no federation peer, must
// reject as NotFound without any 'stopping' UPDATE.
func TestStopDVR_MismatchedTenantNotFound(t *testing.T) {
	srv, mock := newLifecycleServer(t)
	mock.ExpectQuery(`SELECT status, COALESCE\(stream_internal_name`).
		WithArgs("dvr-h", "tenant-intruder").
		WillReturnRows(sqlmock.NewRows([]string{
			"status", "stream_internal_name", "size_bytes", "retention_until",
			"started_at", "ended_at", "tenant_id", "user_id",
		})) // zero rows

	_, err := srv.StopDVR(context.Background(), &sharedpb.StopDVRRequest{
		DvrHash:  "dvr-h",
		TenantId: "tenant-intruder",
	})
	if status.Code(err) != codes.NotFound {
		t.Fatalf("expected NotFound for foreign-tenant DVR, got %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}

// Invariant: an active 'recording' DVR with no known storage node cannot accept
// the Mist stop, so StopDVR rejects as Unavailable and must NOT issue the
// 'stopping' UPDATE (the state transition is gated on a reachable node).
func TestStopDVR_NoStorageNodeUnavailable(t *testing.T) {
	srv, mock := newLifecycleServer(t)
	mock.ExpectQuery(`SELECT status, COALESCE\(stream_internal_name`).
		WithArgs("dvr-h", "tenant-a").
		WillReturnRows(sqlmock.NewRows([]string{
			"status", "stream_internal_name", "size_bytes", "retention_until",
			"started_at", "ended_at", "tenant_id", "user_id",
		}).AddRow("recording", "live+stream-1", nil, nil, nil, nil, "tenant-a", "user-1"))
	mock.ExpectQuery(`SELECT node_id, size_bytes FROM foghorn.artifact_nodes`).
		WithArgs("dvr-h").
		WillReturnRows(sqlmock.NewRows([]string{"node_id", "size_bytes"})) // no node

	_, err := srv.StopDVR(context.Background(), &sharedpb.StopDVRRequest{
		DvrHash:  "dvr-h",
		TenantId: "tenant-a",
	})
	if status.Code(err) != codes.Unavailable {
		t.Fatalf("expected Unavailable, got %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}

// Invariant: a terminal DVR status (already completed/failed) is idempotent —
// StopDVR returns Success=false without sending a stop or issuing an UPDATE.
func TestStopDVR_AlreadyFinishedNoOp(t *testing.T) {
	srv, mock := newLifecycleServer(t)
	mock.ExpectQuery(`SELECT status, COALESCE\(stream_internal_name`).
		WithArgs("dvr-h", "tenant-a").
		WillReturnRows(sqlmock.NewRows([]string{
			"status", "stream_internal_name", "size_bytes", "retention_until",
			"started_at", "ended_at", "tenant_id", "user_id",
		}).AddRow("completed", "live+stream-1", nil, nil, nil, nil, "tenant-a", "user-1"))
	mock.ExpectQuery(`SELECT node_id, size_bytes FROM foghorn.artifact_nodes`).
		WithArgs("dvr-h").
		WillReturnRows(sqlmock.NewRows([]string{"node_id", "size_bytes"}))

	resp, err := srv.StopDVR(context.Background(), &sharedpb.StopDVRRequest{
		DvrHash:  "dvr-h",
		TenantId: "tenant-a",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Success {
		t.Fatalf("expected Success=false for already-finished DVR, got %+v", resp)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}

// ---- CreateClip: validation branches only (no source dispatch) ----

// Invariant: CreateClip rejects each missing required field before any DB I/O,
// with the documented gRPC code per field. These guards keep a malformed clip
// request from ever reaching coverage assessment / artifact writes.
func TestCreateClip_ValidationBranches(t *testing.T) {
	srv, _ := newLifecycleServer(t)
	cases := []struct {
		name string
		req  *sharedpb.CreateClipRequest
		code codes.Code
	}{
		{
			name: "missing stream_internal_name",
			req:  &sharedpb.CreateClipRequest{},
			code: codes.InvalidArgument,
		},
		{
			name: "missing tenant_id",
			req:  &sharedpb.CreateClipRequest{StreamInternalName: "live+s1"},
			code: codes.InvalidArgument,
		},
		{
			name: "missing internal_name",
			req:  &sharedpb.CreateClipRequest{StreamInternalName: "live+s1", TenantId: "tenant-a"},
			code: codes.InvalidArgument,
		},
		{
			name: "missing processes_json -> FailedPrecondition",
			req: &sharedpb.CreateClipRequest{
				StreamInternalName: "live+s1",
				TenantId:           "tenant-a",
				InternalName:       proto.String("clip-internal"),
			},
			code: codes.FailedPrecondition,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := srv.CreateClip(context.Background(), tc.req)
			if status.Code(err) != tc.code {
				t.Fatalf("got %v, want %v", status.Code(err), tc.code)
			}
		})
	}
}

// ---- startDVR: early validation branches only (no source dispatch) ----

// Invariant: startDVR rejects a missing internal_name / tenant_id with
// InvalidArgument before resolving any source or storage node.
func TestStartDVR_ValidationBranches(t *testing.T) {
	srv, _ := newLifecycleServer(t)
	cases := []struct {
		name string
		req  *sharedpb.StartDVRRequest
		code codes.Code
	}{
		{
			name: "missing internal_name",
			req:  &sharedpb.StartDVRRequest{},
			code: codes.InvalidArgument,
		},
		{
			name: "missing tenant_id",
			req:  &sharedpb.StartDVRRequest{InternalName: "live+s1"},
			code: codes.InvalidArgument,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := srv.StartDVR(context.Background(), tc.req)
			if status.Code(err) != tc.code {
				t.Fatalf("got %v, want %v", status.Code(err), tc.code)
			}
		})
	}
}

// ---- SetNodeOperationalMode / GetNodeHealth: node-control auth + state ----

// seedHealthyNode installs a fresh DefaultManager, registers a node owned by
// tenantID, and gives it a heartbeat + metrics so the health snapshot has
// non-zero fields to assert.
func seedHealthyNode(t *testing.T, nodeID, tenantID, clusterID string) *state.StreamStateManager {
	t.Helper()
	sm := state.ResetDefaultManagerForTests()
	t.Cleanup(sm.Shutdown)
	// PushOperationalMode reads control.registry (a *Registry nil until Init);
	// seed an empty one so the not-connected node yields ErrNotConnected (warned
	// and swallowed) instead of a nil-pointer panic.
	t.Cleanup(control.SetupTestRegistry("", nil))
	sm.SetNodeInfo(nodeID, "https://10.0.0.1", true, nil, nil, "eu-west", "", nil)
	sm.SetNodeConnectionInfo(context.Background(), nodeID, "10.0.0.1", tenantID, clusterID, nil)
	sm.UpdateNodeMetrics(nodeID, struct {
		CPU                  float64
		RAMMax               float64
		RAMCurrent           float64
		UpSpeed              float64
		DownSpeed            float64
		BWLimit              float64
		CapIngest            bool
		CapEdge              bool
		CapStorage           bool
		CapProcessing        bool
		Roles                []string
		StorageCapacityBytes uint64
		StorageUsedBytes     uint64
		ProcessingClasses    map[string]state.ClassCapacity
	}{CPU: 42, RAMMax: 16000, RAMCurrent: 8000, BWLimit: 1000, CapEdge: true})
	sm.UpdateNodeDiskUsage(nodeID, 1000, 250)
	sm.TouchNode(nodeID, true)
	return sm
}

// Invariant: node lifecycle control is owner-gated. A JWT bound to a different
// tenant than the node owner is rejected PermissionDenied and the node's mode
// stays unchanged (no silent routing mutation by a non-owner).
func TestSetNodeOperationalMode_ForeignTenantDenied(t *testing.T) {
	srv, _ := newLifecycleServer(t)
	sm := seedHealthyNode(t, "node-1", "tenant-owner", "cluster-1")

	ctx := context.WithValue(context.Background(), ctxkeys.KeyAuthType, "jwt")
	ctx = context.WithValue(ctx, ctxkeys.KeyTenantID, "tenant-intruder")

	_, err := srv.SetNodeOperationalMode(ctx, &foghorncontrolpb.SetNodeModeRequest{
		NodeId: "node-1",
		Mode:   string(state.NodeModeMaintenance),
	})
	if status.Code(err) != codes.PermissionDenied {
		t.Fatalf("expected PermissionDenied, got %v", err)
	}
	if got := sm.GetNodeOperationalMode("node-1"); got != state.NodeModeNormal {
		t.Fatalf("mode must be unchanged after denied set, got %q", got)
	}
}

// Invariant: an unknown node id is NotFound (the mode write must target a real
// node), checked before mutating any state.
func TestSetNodeOperationalMode_UnknownNodeNotFound(t *testing.T) {
	srv, _ := newLifecycleServer(t)
	seedHealthyNode(t, "node-1", "tenant-owner", "cluster-1")

	ctx := context.WithValue(context.Background(), ctxkeys.KeyAuthType, "service")
	_, err := srv.SetNodeOperationalMode(ctx, &foghorncontrolpb.SetNodeModeRequest{
		NodeId: "ghost",
		Mode:   string(state.NodeModeDraining),
	})
	if status.Code(err) != codes.NotFound {
		t.Fatalf("expected NotFound, got %v", err)
	}
}

// Invariant: a service-auth caller (shared control plane) may set the mode and
// the new mode persists in the manager and is echoed in the response.
func TestSetNodeOperationalMode_ServiceAuthPersistsMode(t *testing.T) {
	srv, _ := newLifecycleServer(t)
	sm := seedHealthyNode(t, "node-1", "tenant-owner", "cluster-1")

	ctx := context.WithValue(context.Background(), ctxkeys.KeyAuthType, "service")
	resp, err := srv.SetNodeOperationalMode(ctx, &foghorncontrolpb.SetNodeModeRequest{
		NodeId: "node-1",
		Mode:   "DRAINING", // mixed case -> normalized lowercase
		SetBy:  "ops",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Mode != string(state.NodeModeDraining) {
		t.Fatalf("response mode = %q, want draining", resp.Mode)
	}
	if got := sm.GetNodeOperationalMode("node-1"); got != state.NodeModeDraining {
		t.Fatalf("persisted mode = %q, want draining", got)
	}
}

// Invariant: an invalid mode string is rejected InvalidArgument and the prior
// mode is preserved (no partial/corrupt mode write).
func TestSetNodeOperationalMode_InvalidModeRejected(t *testing.T) {
	srv, _ := newLifecycleServer(t)
	sm := seedHealthyNode(t, "node-1", "tenant-owner", "cluster-1")

	ctx := context.WithValue(context.Background(), ctxkeys.KeyAuthType, "service")
	_, err := srv.SetNodeOperationalMode(ctx, &foghorncontrolpb.SetNodeModeRequest{
		NodeId: "node-1",
		Mode:   "turbo",
	})
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("expected InvalidArgument, got %v", err)
	}
	if got := sm.GetNodeOperationalMode("node-1"); got != state.NodeModeNormal {
		t.Fatalf("mode after invalid set = %q, want normal (unchanged)", got)
	}
}

// Invariant: GetNodeHealth is owner-gated identically to the mode setter — a
// foreign-tenant JWT is denied and learns nothing about the node.
func TestGetNodeHealth_ForeignTenantDenied(t *testing.T) {
	srv, _ := newLifecycleServer(t)
	seedHealthyNode(t, "node-1", "tenant-owner", "cluster-1")

	ctx := context.WithValue(context.Background(), ctxkeys.KeyAuthType, "jwt")
	ctx = context.WithValue(ctx, ctxkeys.KeyTenantID, "tenant-intruder")

	_, err := srv.GetNodeHealth(ctx, &foghorncontrolpb.GetNodeHealthRequest{NodeId: "node-1"})
	if status.Code(err) != codes.PermissionDenied {
		t.Fatalf("expected PermissionDenied, got %v", err)
	}
}

// Invariant: the owning tenant's JWT gets a health snapshot whose identity and
// metrics fields reflect the seeded node state (snapshot fidelity feeds the
// operator drain/maintenance UI). loadNodeComponentVersions queries the DB, so
// expect that read.
func TestGetNodeHealth_OwnerSnapshotFields(t *testing.T) {
	srv, mock := newLifecycleServer(t)
	seedHealthyNode(t, "node-1", "tenant-owner", "cluster-1")
	mock.ExpectQuery(`SELECT component, COALESCE\(current_version`).
		WithArgs("node-1").
		WillReturnRows(sqlmock.NewRows([]string{"component", "version"}).
			AddRow("helmsman", "1.2.3"))

	ctx := context.WithValue(context.Background(), ctxkeys.KeyAuthType, "jwt")
	ctx = context.WithValue(ctx, ctxkeys.KeyTenantID, "tenant-owner")

	resp, err := srv.GetNodeHealth(ctx, &foghorncontrolpb.GetNodeHealthRequest{NodeId: "node-1"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.NodeId != "node-1" || resp.TenantId != "tenant-owner" || resp.ClusterId != "cluster-1" {
		t.Fatalf("identity fields wrong: %+v", resp)
	}
	if !resp.IsHealthy {
		t.Fatalf("freshly-touched healthy node must report healthy")
	}
	if resp.CpuPercent != 42 || resp.RamUsedMb != 8000 || resp.RamMaxMb != 16000 {
		t.Fatalf("metric fields wrong: cpu=%v ramUsed=%v ramMax=%v", resp.CpuPercent, resp.RamUsedMb, resp.RamMaxMb)
	}
	if resp.DiskTotalBytes != 1000 || resp.DiskUsedBytes != 250 {
		t.Fatalf("disk fields wrong: total=%v used=%v", resp.DiskTotalBytes, resp.DiskUsedBytes)
	}
	if resp.LastHeartbeat == "" {
		t.Fatalf("last_heartbeat should be set after TouchNode")
	}
	if len(resp.ComponentVersions) != 1 || resp.ComponentVersions[0].Component != "helmsman" {
		t.Fatalf("component versions not surfaced: %+v", resp.ComponentVersions)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}

// Invariant: missing node_id is InvalidArgument for both node-control RPCs.
func TestNodeControl_MissingNodeIDInvalidArgument(t *testing.T) {
	srv, _ := newLifecycleServer(t)
	seedHealthyNode(t, "node-1", "tenant-owner", "cluster-1")
	ctx := context.WithValue(context.Background(), ctxkeys.KeyAuthType, "service")

	if _, err := srv.SetNodeOperationalMode(ctx, &foghorncontrolpb.SetNodeModeRequest{}); status.Code(err) != codes.InvalidArgument {
		t.Fatalf("SetNodeOperationalMode empty node_id: got %v", err)
	}
	if _, err := srv.GetNodeHealth(ctx, &foghorncontrolpb.GetNodeHealthRequest{}); status.Code(err) != codes.InvalidArgument {
		t.Fatalf("GetNodeHealth empty node_id: got %v", err)
	}
}
