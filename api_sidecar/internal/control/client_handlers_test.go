package control

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	sidecarcfg "frameworks/api_sidecar/internal/config"
	"frameworks/api_sidecar/internal/storage"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/mist"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func TestHandleDesiredStateUpdatePersistsResultOnSendFailure(t *testing.T) {
	outboxDir := t.TempDir()
	t.Setenv("FRAMEWORKS_CONTROL_OUTBOX_DIR", outboxDir)
	outboxMu.Lock()
	outbox = nil
	outboxMu.Unlock()
	t.Cleanup(func() {
		outboxMu.Lock()
		outbox = nil
		outboxMu.Unlock()
	})

	handleDesiredStateUpdate(context.Background(), logging.NewLogger(), "req-update-1", &ipcpb.DesiredStateUpdate{
		NodeId:        "node-1",
		TargetRelease: "stable:v1",
	}, func(*ipcpb.ControlMessage) error {
		return errors.New("stream closed")
	})

	files, err := filepath.Glob(filepath.Join(outboxDir, "*.pb"))
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 {
		t.Fatalf("durable outbox files = %d, want 1", len(files))
	}
}

func TestHandleDesiredStateUpdateRejectsDrainRequiredWithoutCordonToken(t *testing.T) {
	var sent []*ipcpb.ControlMessage
	handleDesiredStateUpdate(context.Background(), logging.NewLogger(), "req-update-1", &ipcpb.DesiredStateUpdate{
		NodeId:        "node-1",
		TargetRelease: "stable:v1",
		Components: []*ipcpb.DesiredComponent{
			{
				Component:     "mist",
				Version:       "v1.2.3",
				ArtifactUrl:   "https://example.test/mist.tgz",
				DrainRequired: true,
			},
		},
	}, func(msg *ipcpb.ControlMessage) error {
		sent = append(sent, msg)
		return nil
	})

	if len(sent) != 1 {
		t.Fatalf("sent messages = %d, want 1", len(sent))
	}
	result := sent[0].GetUpdateApplyResult()
	if result == nil {
		t.Fatal("sent message has no UpdateApplyResult payload")
	}
	if got := len(result.GetComponents()); got != 1 {
		t.Fatalf("component results = %d, want 1", got)
	}
	component := result.GetComponents()[0]
	if component.GetSuccess() {
		t.Fatal("drain-required update without cordon token unexpectedly succeeded")
	}
	if component.GetDetail() != "drain-required update missing cordon token" {
		t.Fatalf("detail = %q, want missing cordon token", component.GetDetail())
	}
}

func TestHandleDesiredStateUpdateRejectsDrainRequiredExpiredCordonToken(t *testing.T) {
	var sent []*ipcpb.ControlMessage
	handleDesiredStateUpdate(context.Background(), logging.NewLogger(), "req-update-1", &ipcpb.DesiredStateUpdate{
		NodeId:               "node-1",
		TargetRelease:        "stable:v1",
		CordonToken:          "token",
		CordonTokenExpiresAt: timestamppb.New(time.Now().Add(-1 * time.Minute)),
		Components: []*ipcpb.DesiredComponent{
			{
				Component:     "mist",
				Version:       "v1.2.3",
				ArtifactUrl:   "https://example.test/mist.tgz",
				DrainRequired: true,
			},
		},
	}, func(msg *ipcpb.ControlMessage) error {
		sent = append(sent, msg)
		return nil
	})

	if len(sent) != 1 {
		t.Fatalf("sent messages = %d, want 1", len(sent))
	}
	component := sent[0].GetUpdateApplyResult().GetComponents()[0]
	if component.GetSuccess() {
		t.Fatal("drain-required update with expired cordon token unexpectedly succeeded")
	}
	if component.GetDetail() != "drain-required update cordon token expired" {
		t.Fatalf("detail = %q, want expired cordon token", component.GetDetail())
	}
}

func TestSanitizeStorageError_InsufficientSpace(t *testing.T) {
	err := fmt.Errorf("disk full: %w", storage.ErrInsufficientSpace)
	msg := sanitizeStorageError(err)
	if msg != "Download failed: storage node out of space" {
		t.Fatalf("unexpected message: %s", msg)
	}
}

func TestSanitizeStorageError_Other(t *testing.T) {
	err := fmt.Errorf("connection refused")
	msg := sanitizeStorageError(err)
	if msg != "Download failed: please retry or contact support" {
		t.Fatalf("unexpected message: %s", msg)
	}
}

func TestDeriveRolesFromConfig(t *testing.T) {
	t.Run("all capabilities", func(t *testing.T) {
		cfg := &sidecarcfg.HelmsmanConfig{
			CapIngest:     true,
			CapEdge:       true,
			CapStorage:    true,
			CapProcessing: true,
		}
		roles := deriveRolesFromConfig(cfg)
		want := []string{"ingest", "edge", "storage", "processing"}
		if len(roles) != len(want) {
			t.Fatalf("expected %d roles, got %d: %v", len(want), len(roles), roles)
		}
		for i, r := range roles {
			if r != want[i] {
				t.Fatalf("role[%d] = %q, want %q", i, r, want[i])
			}
		}
	})

	t.Run("no capabilities", func(t *testing.T) {
		cfg := &sidecarcfg.HelmsmanConfig{}
		roles := deriveRolesFromConfig(cfg)
		if len(roles) != 0 {
			t.Fatalf("expected empty roles, got %v", roles)
		}
	})

	t.Run("partial capabilities", func(t *testing.T) {
		cfg := &sidecarcfg.HelmsmanConfig{
			CapIngest:  true,
			CapStorage: true,
		}
		roles := deriveRolesFromConfig(cfg)
		want := []string{"ingest", "storage"}
		if len(roles) != len(want) {
			t.Fatalf("expected %d roles, got %d: %v", len(want), len(roles), roles)
		}
		for i, r := range roles {
			if r != want[i] {
				t.Fatalf("role[%d] = %q, want %q", i, r, want[i])
			}
		}
	})
}

func TestHandleClipDelete_NilHandler(t *testing.T) {
	prev := deleteClipFn
	deleteClipFn = nil
	t.Cleanup(func() { deleteClipFn = prev })

	var sent []*ipcpb.ControlMessage
	send := func(m *ipcpb.ControlMessage) { sent = append(sent, m) }

	logger := logging.NewLogger()
	req := &ipcpb.ClipDeleteRequest{ClipHash: "abc123", RequestId: "req-1"}
	handleClipDelete(logger, req, send)

	if len(sent) != 0 {
		t.Fatalf("expected no messages sent, got %d", len(sent))
	}
}

func TestHandleClipDelete_Success(t *testing.T) {
	prev := deleteClipFn
	deleteClipFn = func(hash string) (uint64, error) { return 1024, nil }
	t.Cleanup(func() { deleteClipFn = prev })

	storeConn(&fakeControlStream{}, "test-node")
	t.Cleanup(func() { clearConn() })

	var sent []*ipcpb.ControlMessage
	send := func(m *ipcpb.ControlMessage) { sent = append(sent, m) }

	logger := logging.NewLogger()
	req := &ipcpb.ClipDeleteRequest{ClipHash: "abc123", RequestId: "req-1"}
	handleClipDelete(logger, req, send)

	if len(sent) != 1 {
		t.Fatalf("expected 1 message sent, got %d", len(sent))
	}

	ad := sent[0].GetArtifactDeleted()
	if ad == nil {
		t.Fatal("expected ArtifactDeleted payload")
	}
	if ad.ArtifactHash != "abc123" {
		t.Fatalf("expected hash abc123, got %s", ad.ArtifactHash)
	}
	if ad.ArtifactType != "clip" {
		t.Fatalf("expected type clip, got %s", ad.ArtifactType)
	}
	if ad.SizeBytes != 1024 {
		t.Fatalf("expected size 1024, got %d", ad.SizeBytes)
	}
	if ad.Reason != "manual" {
		t.Fatalf("expected reason manual, got %s", ad.Reason)
	}
	if ad.NodeId != "test-node" {
		t.Fatalf("expected node_id test-node, got %s", ad.NodeId)
	}
}

func TestHandleClipDelete_Error(t *testing.T) {
	prev := deleteClipFn
	deleteClipFn = func(hash string) (uint64, error) { return 0, fmt.Errorf("permission denied") }
	t.Cleanup(func() { deleteClipFn = prev })

	var sent []*ipcpb.ControlMessage
	send := func(m *ipcpb.ControlMessage) { sent = append(sent, m) }

	logger := logging.NewLogger()
	req := &ipcpb.ClipDeleteRequest{ClipHash: "abc123", RequestId: "req-1"}
	handleClipDelete(logger, req, send)

	if len(sent) != 0 {
		t.Fatalf("expected no messages sent on error, got %d", len(sent))
	}
}

func TestHandleVodDelete_Success(t *testing.T) {
	prev := deleteVodFn
	deleteVodFn = func(hash string) (uint64, error) { return 2048, nil }
	t.Cleanup(func() { deleteVodFn = prev })

	storeConn(&fakeControlStream{}, "vod-node")
	t.Cleanup(func() { clearConn() })

	var sent []*ipcpb.ControlMessage
	send := func(m *ipcpb.ControlMessage) { sent = append(sent, m) }

	logger := logging.NewLogger()
	req := &ipcpb.VodDeleteRequest{VodHash: "vod-hash-1", RequestId: "req-2"}
	handleVodDelete(logger, req, send)

	if len(sent) != 1 {
		t.Fatalf("expected 1 message sent, got %d", len(sent))
	}

	ad := sent[0].GetArtifactDeleted()
	if ad == nil {
		t.Fatal("expected ArtifactDeleted payload")
	}
	if ad.ArtifactHash != "vod-hash-1" {
		t.Fatalf("expected hash vod-hash-1, got %s", ad.ArtifactHash)
	}
	if ad.ArtifactType != "vod" {
		t.Fatalf("expected type vod, got %s", ad.ArtifactType)
	}
	if ad.SizeBytes != 2048 {
		t.Fatalf("expected size 2048, got %d", ad.SizeBytes)
	}
	if ad.Reason != "manual" {
		t.Fatalf("expected reason manual, got %s", ad.Reason)
	}
	if ad.NodeId != "vod-node" {
		t.Fatalf("expected node_id vod-node, got %s", ad.NodeId)
	}
}

func TestHandleVodDelete_NilHandler(t *testing.T) {
	prev := deleteVodFn
	deleteVodFn = nil
	t.Cleanup(func() { deleteVodFn = prev })

	var sent []*ipcpb.ControlMessage
	send := func(m *ipcpb.ControlMessage) { sent = append(sent, m) }

	logger := logging.NewLogger()
	req := &ipcpb.VodDeleteRequest{VodHash: "vod-hash-1", RequestId: "req-2"}
	handleVodDelete(logger, req, send)

	if len(sent) != 0 {
		t.Fatalf("expected no messages sent, got %d", len(sent))
	}
}

func setupTestDVRManager(t *testing.T) {
	t.Helper()
	// Burn the sync.Once so handleDVRDelete's initDVRManager() call is a no-op
	initDVRManager()
	prevDM := dvrManager
	dvrManager = &DVRManager{
		logger:     logging.NewLogger(),
		jobs:       make(map[string]*DVRJob),
		mistClient: &fakeMistClient{},
	}
	t.Cleanup(func() {
		dvrManager = prevDM
	})
}

func TestHandleDVRDelete_Success(t *testing.T) {
	setupTestDVRManager(t)
	// This test exercises the delete MECHANICS (deleteDVRFn + terminal message), not the
	// bounded-absence policy (covered by TestObserveAbsenceConverged /
	// TestHandleDVRDelete_DefersWhilePushAbsent). Give the DVR a live push so
	// ConfirmDVRPushStopped confirms via the direct pushID>0 stop path — no nil-Mist
	// fail-open shortcut, which now correctly defers instead of authorizing deletion.
	dvrManager.mutex.Lock()
	dvrManager.jobs["dvr-hash-1"] = &DVRJob{DVRHash: "dvr-hash-1", Status: "recording", PushID: 77, Logger: logging.NewLogger()}
	dvrManager.mutex.Unlock()

	prev := deleteDVRFn
	deleteDVRFn = func(hash string) (uint64, error) { return 4096, nil }
	t.Cleanup(func() { deleteDVRFn = prev })

	var sent []*ipcpb.ControlMessage
	send := func(m *ipcpb.ControlMessage) { sent = append(sent, m) }

	logger := logging.NewLogger()
	req := &ipcpb.DVRDeleteRequest{DvrHash: "dvr-hash-1", RequestId: "req-3"}
	handleDVRDelete(logger, req, send)

	if len(sent) != 1 {
		t.Fatalf("expected 1 message sent, got %d", len(sent))
	}

	ds := sent[0].GetDvrStopped()
	if ds == nil {
		t.Fatal("expected DvrStopped payload")
	}
	if ds.DvrHash != "dvr-hash-1" {
		t.Fatalf("expected dvr hash dvr-hash-1, got %s", ds.DvrHash)
	}
	if ds.Status != "deleted" {
		t.Fatalf("expected status deleted, got %s", ds.Status)
	}
	if ds.RequestId != "req-3" {
		t.Fatalf("expected request_id req-3, got %s", ds.RequestId)
	}
}

func TestHandleDVRDelete_NilHandler(t *testing.T) {
	setupTestDVRManager(t)

	prev := deleteDVRFn
	deleteDVRFn = nil
	t.Cleanup(func() { deleteDVRFn = prev })

	var sent []*ipcpb.ControlMessage
	send := func(m *ipcpb.ControlMessage) { sent = append(sent, m) }

	logger := logging.NewLogger()
	req := &ipcpb.DVRDeleteRequest{DvrHash: "dvr-hash-2", RequestId: "req-4"}
	handleDVRDelete(logger, req, send)

	if len(sent) != 0 {
		t.Fatalf("expected no messages sent, got %d", len(sent))
	}
}

func TestHandleDVRDelete_Error(t *testing.T) {
	setupTestDVRManager(t)

	prev := deleteDVRFn
	deleteDVRFn = func(hash string) (uint64, error) { return 0, fmt.Errorf("not found") }
	t.Cleanup(func() { deleteDVRFn = prev })

	var sent []*ipcpb.ControlMessage
	send := func(m *ipcpb.ControlMessage) { sent = append(sent, m) }

	logger := logging.NewLogger()
	req := &ipcpb.DVRDeleteRequest{DvrHash: "dvr-hash-3", RequestId: "req-5"}
	handleDVRDelete(logger, req, send)

	if len(sent) != 0 {
		t.Fatalf("expected no messages sent on error, got %d", len(sent))
	}
}

func TestHandleVodDelete_Error(t *testing.T) {
	prev := deleteVodFn
	deleteVodFn = func(hash string) (uint64, error) { return 0, fmt.Errorf("access denied") }
	t.Cleanup(func() { deleteVodFn = prev })

	var sent []*ipcpb.ControlMessage
	send := func(m *ipcpb.ControlMessage) { sent = append(sent, m) }

	logger := logging.NewLogger()
	req := &ipcpb.VodDeleteRequest{VodHash: "vod-hash-2", RequestId: "req-6"}
	handleVodDelete(logger, req, send)

	if len(sent) != 0 {
		t.Fatalf("expected no messages sent on error, got %d", len(sent))
	}
}

func TestHandleDVRDelete_StopsRecordingFirst(t *testing.T) {
	setupTestDVRManager(t)

	// Add an active job with a LIVE push id so ConfirmDVRPushStopped takes the pushID>0 path:
	// it stops the push (fake succeeds) and confirms immediately, removing the job — no
	// bounded-absence deferral (that path is exercised by TestHandleDVRDelete_DefersWhilePushAbsent).
	dvrManager.mutex.Lock()
	dvrManager.jobs["dvr-active"] = &DVRJob{
		DVRHash: "dvr-active",
		Status:  "recording",
		PushID:  99, // live push → confirmed by direct PushStop
		Logger:  logging.NewLogger(),
	}
	dvrManager.mutex.Unlock()

	var deleteCalledWithHash string
	prev := deleteDVRFn
	deleteDVRFn = func(hash string) (uint64, error) {
		deleteCalledWithHash = hash

		// By the time deleteDVRFn runs, StopRecording should have
		// already removed the job from dvrManager.jobs
		dvrManager.mutex.RLock()
		_, stillActive := dvrManager.jobs[hash]
		dvrManager.mutex.RUnlock()
		if stillActive {
			t.Fatal("expected StopRecording to remove job before deleteDVRFn runs")
		}
		return 512, nil
	}
	t.Cleanup(func() { deleteDVRFn = prev })

	var sent []*ipcpb.ControlMessage
	send := func(m *ipcpb.ControlMessage) { sent = append(sent, m) }

	logger := logging.NewLogger()
	req := &ipcpb.DVRDeleteRequest{DvrHash: "dvr-active", RequestId: "req-stop"}
	handleDVRDelete(logger, req, send)

	if deleteCalledWithHash != "dvr-active" {
		t.Fatalf("expected deleteDVRFn called with dvr-active, got %q", deleteCalledWithHash)
	}
	if len(sent) != 1 {
		t.Fatalf("expected 1 message sent, got %d", len(sent))
	}
	ds := sent[0].GetDvrStopped()
	if ds == nil || ds.Status != "deleted" {
		t.Fatalf("expected DvrStopped with status deleted, got %+v", sent[0])
	}
}

// A delete must NOT remove files while a live Mist push cannot be stopped — that
// would leave Mist writing into a removed/recreated path. The delete is deferred
// (deleteDVRFn not called) and the job kept; Foghorn re-drives it.
func TestHandleDVRDelete_DefersWhenLivePushWontStop(t *testing.T) {
	initDVRManager()
	prevDM := dvrManager
	dvrHash := "dvr-livedelete"
	target := "/data/dvr/s/" + dvrHash + "/segments/$c.ts"
	mc := &fakeMistClient{
		pushListItems: []mist.PushInfo{{ID: 7, StreamName: "live+x", TargetURI: target}},
		pushStopErr:   fmt.Errorf("mist unreachable"),
	}
	dvrManager = &DVRManager{logger: logging.NewLogger(), jobs: make(map[string]*DVRJob), mistClient: mc}
	dvrManager.jobs[dvrHash] = &DVRJob{
		DVRHash: dvrHash, StreamName: "live+x", TargetURI: target, PushID: 0,
		Status: "recording", Logger: logging.NewLogger(),
	}
	t.Cleanup(func() { dvrManager = prevDM })

	deleteCalled := false
	prev := deleteDVRFn
	deleteDVRFn = func(string) (uint64, error) { deleteCalled = true; return 0, nil }
	t.Cleanup(func() { deleteDVRFn = prev })

	handleDVRDelete(logging.NewLogger(), &ipcpb.DVRDeleteRequest{DvrHash: dvrHash}, func(*ipcpb.ControlMessage) {})

	if deleteCalled {
		t.Fatal("delete must be deferred when the live push cannot be stopped")
	}
	if _, exists := dvrManager.jobs[dvrHash]; !exists {
		t.Fatal("job must be kept when the stop is unconfirmed (delete deferred)")
	}
}

// End-to-end: a DVRStop that overtakes its DVRStart (stop arrives first, higher
// generation) tombstones the hash, so the racing lower-generation DVRStart is
// rejected idempotently and no recording is created behind the terminal stop.
func TestHandleDVRStart_RejectedWhenStopTombstoned(t *testing.T) {
	initDVRManager()
	prevDM := dvrManager
	dvrManager = &DVRManager{logger: logging.NewLogger(), jobs: make(map[string]*DVRJob), mistClient: &fakeMistClient{}}
	t.Cleanup(func() { dvrManager = prevDM })

	// The stop (generation 2) reaches Helmsman first.
	handleDVRStop(logging.NewLogger(), &ipcpb.DVRStopRequest{DvrHash: "hash-race", RequestId: "r", CommandGeneration: 2}, func(*ipcpb.ControlMessage) {})
	// The racing start (generation 1) must not create a recording.
	handleDVRStart(logging.NewLogger(), &ipcpb.DVRStartRequest{DvrHash: "hash-race", InternalName: "x", CommandGeneration: 1}, func(*ipcpb.ControlMessage) {})

	dvrManager.mutex.RLock()
	_, exists := dvrManager.jobs["hash-race"]
	dvrManager.mutex.RUnlock()
	if exists {
		t.Fatal("a start superseded by a newer stop must not create a recording job")
	}
}

// Safety property: a delete whose Mist push is ABSENT from a successful list is
// NEVER executed while absence has not converged — an accepted push can be momentarily
// unlisted, and here the recording is not even resolvable on disk (inconclusive). Every
// attempt DEFERS: files retained, no terminal message, no destructive delete. (The
// convergence path — spaced observations + real elapsed grace + no byte progress — is
// covered deterministically by TestObserveAbsenceConverged.)
func TestHandleDVRDelete_DefersWhilePushAbsent(t *testing.T) {
	setupTestDVRManager(t) // fakeMistClient returns an empty PushList → push absent

	var deleteCalls int
	prev := deleteDVRFn
	deleteDVRFn = func(hash string) (uint64, error) { deleteCalls++; return 1, nil }
	t.Cleanup(func() { deleteDVRFn = prev })

	var sent []*ipcpb.ControlMessage
	send := func(m *ipcpb.ControlMessage) { sent = append(sent, m) }
	logger := logging.NewLogger()
	req := &ipcpb.DVRDeleteRequest{DvrHash: "dvr-absent", RequestId: "req-defer"}

	// Many attempts must all DEFER — a possibly-live-but-unlisted writer is never destroyed
	// on an empty list, and an unreadable recording is treated as inconclusive, never idle.
	for i := 0; i < dvrAbsenceThreshold+5; i++ {
		handleDVRDelete(logger, req, send)
		if deleteCalls != 0 {
			t.Fatalf("delete must be deferred on attempt %d (absence must not converge here)", i+1)
		}
		if len(sent) != 0 {
			t.Fatalf("no terminal message may be sent while deferring, got %d", len(sent))
		}
	}
}
