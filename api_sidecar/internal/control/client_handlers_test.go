package control

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	sidecarcfg "frameworks/api_sidecar/internal/config"
	"frameworks/api_sidecar/internal/storage"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	pb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto"
)

func TestHandleDesiredStateUpdateQueuesResultOnSendFailure(t *testing.T) {
	outboxMu.Lock()
	outbox = nil
	outboxMu.Unlock()
	t.Cleanup(func() {
		outboxMu.Lock()
		outbox = nil
		outboxMu.Unlock()
	})

	handleDesiredStateUpdate(context.Background(), logging.NewLogger(), "req-update-1", &pb.DesiredStateUpdate{
		NodeId:        "node-1",
		TargetRelease: "stable:v1",
	}, func(*pb.ControlMessage) error {
		return errors.New("stream closed")
	})

	outboxMu.Lock()
	defer outboxMu.Unlock()
	if len(outbox) != 1 {
		t.Fatalf("outbox length = %d, want 1", len(outbox))
	}
	result := outbox[0].GetUpdateApplyResult()
	if result == nil {
		t.Fatal("queued message has no UpdateApplyResult payload")
	}
	if result.GetNodeId() != "node-1" || result.GetTargetRelease() != "stable:v1" {
		t.Fatalf("queued result = node %q target %q", result.GetNodeId(), result.GetTargetRelease())
	}
}

func TestBuildClipParams_CanonicalAbsoluteRange(t *testing.T) {
	startUnix := int64(1000)
	stopUnix := int64(2000)
	startMs := int64(500)
	stopMs := int64(1500)
	dur := int64(60)

	req := &pb.ClipPullRequest{
		StartUnix:   &startUnix,
		StopUnix:    &stopUnix,
		StartMs:     &startMs,
		StopMs:      &stopMs,
		DurationSec: &dur,
		OutputName:  "my clip",
		Format:      "mp4",
	}

	result := buildClipParamsAt(req, 3000)

	for _, want := range []string{
		"startunix=1000",
		"stopunix=2000",
		"dl=my%20clip.mp4",
	} {
		if !strings.Contains(result, want) {
			t.Fatalf("expected %q in result %q", want, result)
		}
	}
	for _, leaked := range []string{"start=500", "stop=1500", "duration=60"} {
		if strings.Contains(result, leaked) {
			t.Fatalf("mode-specific field leaked into /view params: %q in %q", leaked, result)
		}
	}
}

func TestBuildClipParams_LiveUsesRelativeMistRange(t *testing.T) {
	startUnix := int64(2990)
	stopUnix := int64(3020)
	req := &pb.ClipPullRequest{
		SourceKind: pb.ClipPullRequest_SOURCE_KIND_LIVE,
		StartUnix:  &startUnix,
		StopUnix:   &stopUnix,
		OutputName: "live clip",
		Format:     "mp4",
	}

	result := buildClipParamsAt(req, 3030)

	for _, want := range []string{
		"startunix=-40",
		"duration=30",
		"dl=live%20clip.mp4",
	} {
		if !strings.Contains(result, want) {
			t.Fatalf("expected %q in result %q", want, result)
		}
	}
	if strings.Contains(result, "stopunix=") {
		t.Fatalf("live clip should use duration, not stopunix: %q", result)
	}
}

func TestBuildClipParams_Empty(t *testing.T) {
	req := &pb.ClipPullRequest{
		OutputName: "simple",
		Format:     "mp4",
	}

	result := buildClipParams(req)
	if result != "dl=simple.mp4" {
		t.Fatalf("expected %q, got %q", "dl=simple.mp4", result)
	}
}

func TestDeriveMistHTTPBase(t *testing.T) {
	tests := []struct {
		name string
		base string
		want string
	}{
		{name: "api endpoint", base: "http://mistserver:4242/api2", want: "http://mistserver:8080"},
		{name: "already http output", base: "http://mistserver:8080", want: "http://mistserver:8080"},
		{name: "hostname only", base: "mistserver:4242", want: "http://mistserver:8080"},
		{name: "custom port", base: "http://mistserver:18090", want: "http://mistserver:18090"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := deriveMistHTTPBase(tc.base); got != tc.want {
				t.Fatalf("deriveMistHTTPBase(%q) = %q, want %q", tc.base, got, tc.want)
			}
		})
	}
}

func TestBuildClipURL(t *testing.T) {
	tests := []struct {
		name       string
		base       string
		streamName string
		want       string
	}{
		{
			name:       "direct mist http output",
			base:       "http://mistserver:8080",
			streamName: "live+stream-1",
			want:       "http://mistserver:8080/live+stream-1.mp4?startunix=1",
		},
		{
			name:       "edge view prefix",
			base:       "http://edge.example/view",
			streamName: "live+stream-1",
			want:       "http://edge.example/view/live+stream-1.mp4?startunix=1",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := buildClipURL(tc.base, tc.streamName, "mp4", "startunix=1")
			if got != tc.want {
				t.Fatalf("buildClipURL() = %q, want %q", got, tc.want)
			}
		})
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

	var sent []*pb.ControlMessage
	send := func(m *pb.ControlMessage) { sent = append(sent, m) }

	logger := logging.NewLogger()
	req := &pb.ClipDeleteRequest{ClipHash: "abc123", RequestId: "req-1"}
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

	var sent []*pb.ControlMessage
	send := func(m *pb.ControlMessage) { sent = append(sent, m) }

	logger := logging.NewLogger()
	req := &pb.ClipDeleteRequest{ClipHash: "abc123", RequestId: "req-1"}
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

	var sent []*pb.ControlMessage
	send := func(m *pb.ControlMessage) { sent = append(sent, m) }

	logger := logging.NewLogger()
	req := &pb.ClipDeleteRequest{ClipHash: "abc123", RequestId: "req-1"}
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

	var sent []*pb.ControlMessage
	send := func(m *pb.ControlMessage) { sent = append(sent, m) }

	logger := logging.NewLogger()
	req := &pb.VodDeleteRequest{VodHash: "vod-hash-1", RequestId: "req-2"}
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

	var sent []*pb.ControlMessage
	send := func(m *pb.ControlMessage) { sent = append(sent, m) }

	logger := logging.NewLogger()
	req := &pb.VodDeleteRequest{VodHash: "vod-hash-1", RequestId: "req-2"}
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
		logger: logging.NewLogger(),
		jobs:   make(map[string]*DVRJob),
	}
	t.Cleanup(func() {
		dvrManager = prevDM
	})
}

func TestHandleDVRDelete_Success(t *testing.T) {
	setupTestDVRManager(t)

	prev := deleteDVRFn
	deleteDVRFn = func(hash string) (uint64, error) { return 4096, nil }
	t.Cleanup(func() { deleteDVRFn = prev })

	var sent []*pb.ControlMessage
	send := func(m *pb.ControlMessage) { sent = append(sent, m) }

	logger := logging.NewLogger()
	req := &pb.DVRDeleteRequest{DvrHash: "dvr-hash-1", RequestId: "req-3"}
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

	var sent []*pb.ControlMessage
	send := func(m *pb.ControlMessage) { sent = append(sent, m) }

	logger := logging.NewLogger()
	req := &pb.DVRDeleteRequest{DvrHash: "dvr-hash-2", RequestId: "req-4"}
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

	var sent []*pb.ControlMessage
	send := func(m *pb.ControlMessage) { sent = append(sent, m) }

	logger := logging.NewLogger()
	req := &pb.DVRDeleteRequest{DvrHash: "dvr-hash-3", RequestId: "req-5"}
	handleDVRDelete(logger, req, send)

	if len(sent) != 0 {
		t.Fatalf("expected no messages sent on error, got %d", len(sent))
	}
}

func TestHandleVodDelete_Error(t *testing.T) {
	prev := deleteVodFn
	deleteVodFn = func(hash string) (uint64, error) { return 0, fmt.Errorf("access denied") }
	t.Cleanup(func() { deleteVodFn = prev })

	var sent []*pb.ControlMessage
	send := func(m *pb.ControlMessage) { sent = append(sent, m) }

	logger := logging.NewLogger()
	req := &pb.VodDeleteRequest{VodHash: "vod-hash-2", RequestId: "req-6"}
	handleVodDelete(logger, req, send)

	if len(sent) != 0 {
		t.Fatalf("expected no messages sent on error, got %d", len(sent))
	}
}

func TestHandleClipPull_NilConfig(t *testing.T) {
	prev := currentConfig
	currentConfig = nil
	t.Cleanup(func() { currentConfig = prev })

	var sent []*pb.ControlMessage
	send := func(m *pb.ControlMessage) { sent = append(sent, m) }

	logger := logging.NewLogger()
	req := &pb.ClipPullRequest{
		ClipHash:   "clip-1",
		StreamName: "stream-1",
		Format:     "mp4",
		OutputName: "test",
		RequestId:  "req-7",
	}
	handleClipPull(logger, req, send)

	if len(sent) != 0 {
		t.Fatalf("expected no messages sent when config is nil, got %d", len(sent))
	}
}

func TestHandleClipPull_LocalLiveSourceUsesMistStreamName(t *testing.T) {
	prev := currentConfig
	currentConfig = &sidecarcfg.HelmsmanConfig{
		MistServerURL:    "http://mistserver:4242/api2",
		StorageLocalPath: t.TempDir(),
	}
	t.Cleanup(func() { currentConfig = prev })

	var requestedURL string
	prevDownload := downloadClipFile
	downloadClipFile = func(url, dst string) error {
		requestedURL = url
		return os.WriteFile(dst, []byte("clip-bytes"), 0600)
	}
	t.Cleanup(func() { downloadClipFile = prevDownload })

	var sent []*pb.ControlMessage
	req := &pb.ClipPullRequest{
		ClipHash:         "clip-1",
		StreamName:       "stream-1",
		OutputStreamName: "stream-1",
		Format:           "mp4",
		OutputName:       "clip-1",
		RequestId:        "req-clip-1",
		SourceKind:       pb.ClipPullRequest_SOURCE_KIND_LIVE,
	}
	handleClipPull(logging.NewLogger(), req, func(m *pb.ControlMessage) { sent = append(sent, m) })

	if !strings.HasPrefix(requestedURL, "http://mistserver:8080/live+stream-1.mp4?") {
		t.Fatalf("requested URL = %q, want local Mist live stream path", requestedURL)
	}
	done := sent[len(sent)-1].GetClipDone()
	if done == nil || done.GetStatus() != "success" {
		t.Fatalf("expected successful clip done, got %#v", sent[len(sent)-1])
	}
	out := filepath.Join(currentConfig.StorageLocalPath, "clips", "stream-1", "clip-1.mp4")
	if b, err := os.ReadFile(out); err != nil || string(b) != "clip-bytes" {
		t.Fatalf("clip output = %q err=%v", string(b), err)
	}
}

func TestHandleDVRDelete_StopsRecordingFirst(t *testing.T) {
	setupTestDVRManager(t)

	// Add an active job so StopRecording has something to stop
	dvrManager.mutex.Lock()
	dvrManager.jobs["dvr-active"] = &DVRJob{
		DVRHash: "dvr-active",
		Status:  "recording",
		PushID:  0, // No MistServer push to worry about
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

	var sent []*pb.ControlMessage
	send := func(m *pb.ControlMessage) { sent = append(sent, m) }

	logger := logging.NewLogger()
	req := &pb.DVRDeleteRequest{DvrHash: "dvr-active", RequestId: "req-stop"}
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
