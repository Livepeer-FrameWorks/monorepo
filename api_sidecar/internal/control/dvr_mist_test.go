package control

import (
	"fmt"
	"sync/atomic"
	"testing"

	"frameworks/pkg/logging"
	"frameworks/pkg/mist"
	pb "frameworks/pkg/proto"
)

// fakeMistClient implements DVRMistClient for testing
type fakeMistClient struct {
	pushStartCalls int64
	pushStopCalls  int64
	pushListCalls  int64

	pushStartErr  error
	pushStopErr   error
	pushListErr   error
	pushListItems []mist.PushInfo

	lastStartStream string
	lastStartTarget string
	lastStopID      int
}

func (f *fakeMistClient) PushStart(streamName, targetURI string) error {
	atomic.AddInt64(&f.pushStartCalls, 1)
	f.lastStartStream = streamName
	f.lastStartTarget = targetURI
	return f.pushStartErr
}

func (f *fakeMistClient) PushStop(pushID int) error {
	atomic.AddInt64(&f.pushStopCalls, 1)
	f.lastStopID = pushID
	return f.pushStopErr
}

func (f *fakeMistClient) PushList() ([]mist.PushInfo, error) {
	atomic.AddInt64(&f.pushListCalls, 1)
	return f.pushListItems, f.pushListErr
}

// startAwareFakeMist simulates PushStart creating a push that PushList can find.
type startAwareFakeMist struct {
	pushIDToReturn int
	pushStartErr   error
	pushStopErr    error
	pushStopCalls  int64
	started        bool
	lastStreamName string
	lastTargetURI  string
}

func (s *startAwareFakeMist) PushStart(streamName, targetURI string) error {
	s.lastStreamName = streamName
	s.lastTargetURI = targetURI
	if s.pushStartErr != nil {
		return s.pushStartErr
	}
	s.started = true
	return nil
}

func (s *startAwareFakeMist) PushStop(pushID int) error {
	atomic.AddInt64(&s.pushStopCalls, 1)
	s.started = false
	return s.pushStopErr
}

func (s *startAwareFakeMist) PushList() ([]mist.PushInfo, error) {
	if s.started {
		return []mist.PushInfo{
			{
				ID:         s.pushIDToReturn,
				StreamName: s.lastStreamName,
				TargetURI:  s.lastTargetURI,
			},
		}, nil
	}
	return []mist.PushInfo{}, nil
}

// staleCleanupFakeMist returns existing pushes before PushStart, new push after.
type staleCleanupFakeMist struct {
	existingPushes []mist.PushInfo
	newPushID      int
	stoppedIDs     []int
	pushStarted    bool
	streamName     string
	targetURI      string
}

func (s *staleCleanupFakeMist) PushStart(streamName, targetURI string) error {
	s.pushStarted = true
	s.streamName = streamName
	s.targetURI = targetURI
	return nil
}

func (s *staleCleanupFakeMist) PushStop(pushID int) error {
	s.stoppedIDs = append(s.stoppedIDs, pushID)
	return nil
}

func (s *staleCleanupFakeMist) PushList() ([]mist.PushInfo, error) {
	if s.pushStarted {
		return []mist.PushInfo{
			{ID: s.newPushID, StreamName: s.streamName, TargetURI: s.targetURI},
		}, nil
	}
	return s.existingPushes, nil
}

func newDVRManagerWithMist(t *testing.T, mc DVRMistClient) *DVRManager {
	t.Helper()
	return &DVRManager{
		logger:      logging.NewLogger(),
		jobs:        make(map[string]*DVRJob),
		storagePath: t.TempDir(),
		mistClient:  mc,
		diskCheck:   func(string, uint64) error { return nil },
	}
}

// --- StartRecording ---

func TestStartRecording_CreatesDirectories(t *testing.T) {
	mc := &startAwareFakeMist{pushIDToReturn: 42}
	dm := newDVRManagerWithMist(t, mc)

	err := dm.StartRecording("hash-create", "stream-1", "test-internal", "http://source", &pb.DVRConfig{
		SegmentDuration: 6,
	}, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	job, exists := dm.jobs["hash-create"]
	if !exists {
		t.Fatal("expected job to be stored")
	}
	if job.Status != "recording" {
		t.Fatalf("expected status 'recording', got %s", job.Status)
	}
	if job.PushID != 42 {
		t.Fatalf("expected push ID 42, got %d", job.PushID)
	}
}

func TestStartRecording_PushStartCalled(t *testing.T) {
	mc := &startAwareFakeMist{pushIDToReturn: 10}
	dm := newDVRManagerWithMist(t, mc)

	err := dm.StartRecording("hash-push", "stream-1", "test-stream", "http://source", &pb.DVRConfig{
		SegmentDuration: 6,
	}, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if mc.lastStreamName != "live+test-stream" {
		t.Fatalf("expected stream name 'live+test-stream', got %s", mc.lastStreamName)
	}
}

func TestStartRecording_PushStartError(t *testing.T) {
	mc := &startAwareFakeMist{
		pushIDToReturn: 0,
		pushStartErr:   fmt.Errorf("mist connection refused"),
	}
	dm := newDVRManagerWithMist(t, mc)

	err := dm.StartRecording("hash-fail", "stream-1", "test-stream", "http://source", &pb.DVRConfig{}, nil)
	if err == nil {
		t.Fatal("expected error for PushStart failure")
	}

	if _, exists := dm.jobs["hash-fail"]; exists {
		t.Fatal("expected job not to be stored after failed start")
	}
}

// --- StopRecording ---

func TestStopRecording_PushStopCalled(t *testing.T) {
	mc := &startAwareFakeMist{pushIDToReturn: 77}
	dm := newDVRManagerWithMist(t, mc)

	err := dm.StartRecording("hash-stop", "stream-1", "test-stop", "http://source", &pb.DVRConfig{}, nil)
	if err != nil {
		t.Fatalf("start failed: %v", err)
	}

	job := dm.jobs["hash-stop"]
	if job.PushID != 77 {
		t.Fatalf("expected push ID 77, got %d", job.PushID)
	}

	err = dm.StopRecording("hash-stop")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if atomic.LoadInt64(&mc.pushStopCalls) != 1 {
		t.Fatalf("expected 1 PushStop call, got %d", atomic.LoadInt64(&mc.pushStopCalls))
	}
	if _, exists := dm.jobs["hash-stop"]; exists {
		t.Fatal("expected job to be removed after stop")
	}
}

func TestStopRecording_PushStopError(t *testing.T) {
	mc := &startAwareFakeMist{
		pushIDToReturn: 88,
		pushStopErr:    fmt.Errorf("mist unreachable"),
	}
	dm := newDVRManagerWithMist(t, mc)

	err := dm.StartRecording("hash-stoperr", "stream-1", "test-stoperr", "http://source", &pb.DVRConfig{}, nil)
	if err != nil {
		t.Fatalf("start failed: %v", err)
	}

	err = dm.StopRecording("hash-stoperr")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if _, exists := dm.jobs["hash-stoperr"]; exists {
		t.Fatal("expected job to be removed even after PushStop error")
	}
}

// --- createOrRecreatePush ---

func TestCreateOrRecreatePush_New(t *testing.T) {
	mc := &startAwareFakeMist{pushIDToReturn: 55}
	dm := newDVRManagerWithMist(t, mc)

	job := &DVRJob{
		DVRHash:    "hash-new",
		StreamName: "live+test",
		TargetURI:  "/data/dvr/hash-new",
		Logger:     logging.NewLogger(),
	}

	pushID, err := dm.createOrRecreatePush(job)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if pushID != 55 {
		t.Fatalf("expected push ID 55, got %d", pushID)
	}
}

func TestCreateOrRecreatePush_StaleCleanup(t *testing.T) {
	mc := &staleCleanupFakeMist{
		existingPushes: []mist.PushInfo{
			{ID: 10, StreamName: "live+test", TargetURI: "/data/dvr/hash-stale"},
		},
		newPushID: 99,
	}
	dm := newDVRManagerWithMist(t, mc)

	job := &DVRJob{
		DVRHash:    "hash-stale",
		StreamName: "live+test",
		TargetURI:  "/data/dvr/hash-stale",
		Logger:     logging.NewLogger(),
	}

	pushID, err := dm.createOrRecreatePush(job)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if pushID != 99 {
		t.Fatalf("expected push ID 99, got %d", pushID)
	}
	if len(mc.stoppedIDs) != 1 || mc.stoppedIDs[0] != 10 {
		t.Fatalf("expected old push 10 to be stopped, got %v", mc.stoppedIDs)
	}
}

func TestCreateOrRecreatePush_PushListError(t *testing.T) {
	mc := &fakeMistClient{
		pushListErr: fmt.Errorf("mist unavailable"),
	}
	dm := newDVRManagerWithMist(t, mc)

	job := &DVRJob{
		DVRHash:    "hash-listerr",
		StreamName: "live+test",
		TargetURI:  "/data/dvr/hash-listerr",
		Logger:     logging.NewLogger(),
	}

	// PushStart will succeed but subsequent PushList to find new push will fail
	_, err := dm.createOrRecreatePush(job)
	if err == nil {
		t.Fatal("expected error when PushList fails after PushStart")
	}
}

// --- maintainPushStatus ---

func TestMaintainPushStatus_Healthy(t *testing.T) {
	mc := &fakeMistClient{
		pushListItems: []mist.PushInfo{
			{ID: 42, StreamName: "live+test", TargetURI: "/data/dvr/hash"},
		},
	}
	dm := newDVRManagerWithMist(t, mc)

	job := &DVRJob{
		DVRHash:    "hash-healthy",
		PushID:     42,
		StreamName: "live+test",
		TargetURI:  "/data/dvr/hash",
		Status:     "recording",
		MaxRetries: 10,
		Logger:     logging.NewLogger(),
	}
	dm.jobs["hash-healthy"] = job

	dm.maintainPushStatus(job)

	if atomic.LoadInt64(&mc.pushStartCalls) != 0 {
		t.Fatal("expected no PushStart calls for healthy push")
	}
	if job.Status != "recording" {
		t.Fatalf("expected status to remain 'recording', got %s", job.Status)
	}
}

func TestMaintainPushStatus_Lost(t *testing.T) {
	mc := &startAwareFakeMist{pushIDToReturn: 99}
	dm := newDVRManagerWithMist(t, mc)

	job := &DVRJob{
		DVRHash:    "hash-lost",
		PushID:     42,
		StreamName: "live+test",
		TargetURI:  "/data/dvr/hash",
		Status:     "recording",
		MaxRetries: 10,
		RetryCount: 0,
		Logger:     logging.NewLogger(),
	}
	dm.jobs["hash-lost"] = job

	dm.maintainPushStatus(job)

	if job.PushID != 99 {
		t.Fatalf("expected new push ID 99, got %d", job.PushID)
	}
	if job.RetryCount != 1 {
		t.Fatalf("expected retry count 1, got %d", job.RetryCount)
	}
}

func TestMaintainPushStatus_ExhaustedRetries(t *testing.T) {
	mc := &fakeMistClient{
		pushListItems: []mist.PushInfo{},
	}
	dm := newDVRManagerWithMist(t, mc)

	job := &DVRJob{
		DVRHash:    "hash-exhausted",
		PushID:     42,
		StreamName: "live+test",
		TargetURI:  "/data/dvr/hash",
		Status:     "recording",
		MaxRetries: 3,
		RetryCount: 3,
		Logger:     logging.NewLogger(),
	}
	dm.jobs["hash-exhausted"] = job

	dm.maintainPushStatus(job)

	if _, exists := dm.jobs["hash-exhausted"]; exists {
		t.Fatal("expected job to be removed after exhausted retries")
	}
}

func TestMaintainPushStatus_StoppedJobSkipped(t *testing.T) {
	mc := &fakeMistClient{}
	dm := newDVRManagerWithMist(t, mc)

	job := &DVRJob{
		DVRHash: "hash-stopped",
		Status:  "stopped",
		Logger:  logging.NewLogger(),
	}

	dm.maintainPushStatus(job)

	if atomic.LoadInt64(&mc.pushListCalls) != 0 {
		t.Fatal("expected no PushList calls for stopped job")
	}
}

func TestMaintainPushStatus_PushWithErrors(t *testing.T) {
	mcWithErrors := &fakeMistClient{
		pushListItems: []mist.PushInfo{
			{
				ID:         42,
				StreamName: "live+test",
				TargetURI:  "/data/dvr/hash",
				Logs:       []string{"DTSC Error: connection failed"},
			},
		},
	}
	dm2 := newDVRManagerWithMist(t, mcWithErrors)

	job := &DVRJob{
		DVRHash:    "hash-errors",
		PushID:     42,
		StreamName: "live+test",
		TargetURI:  "/data/dvr/hash",
		Status:     "recording",
		MaxRetries: 10,
		RetryCount: 0,
		Logger:     logging.NewLogger(),
	}
	dm2.jobs["hash-errors"] = job

	// Push has errors → should attempt recreation
	// But since mcWithErrors doesn't support recreation well (PushStart always succeeds
	// but PushList returns same error push), the retry will fail to find a new push.
	// That's fine — we just verify the retry was attempted.
	dm2.maintainPushStatus(job)

	if job.RetryCount != 1 {
		t.Fatalf("expected retry count 1 after push errors, got %d", job.RetryCount)
	}
}

func TestMaintainPushStatus_CompletedNaturally(t *testing.T) {
	mc := &fakeMistClient{
		pushListItems: []mist.PushInfo{},
	}
	dm := newDVRManagerWithMist(t, mc)

	var completionSent bool
	job := &DVRJob{
		DVRHash:      "hash-natural",
		PushID:       42,
		StreamName:   "live+test",
		TargetURI:    "/data/dvr/hash",
		Status:       "recording",
		MaxRetries:   10,
		RetryCount:   10,
		SegmentCount: 5,
		Logger:       logging.NewLogger(),
		SendFunc: func(_ *pb.ControlMessage) {
			completionSent = true
		},
	}
	dm.jobs["hash-natural"] = job

	dm.maintainPushStatus(job)

	if _, exists := dm.jobs["hash-natural"]; exists {
		t.Fatal("expected job to be removed")
	}
	if !completionSent {
		t.Fatal("expected completion notification to be sent")
	}
}
