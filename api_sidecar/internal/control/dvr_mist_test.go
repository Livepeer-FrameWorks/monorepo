package control

import (
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/mist"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
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
	pushIDToReturn      int
	pushStartErr        error
	failStarts          int
	startCalls          int
	pushStopErr         error
	pushStopCalls       int64
	started             bool
	lastStreamName      string
	lastTargetURI       string
	listTargetURI       string
	listActualURI       string
	listEmptyAfterStart int
}

func (s *startAwareFakeMist) PushStart(streamName, targetURI string) error {
	s.startCalls++
	s.lastStreamName = streamName
	s.lastTargetURI = targetURI
	if s.pushStartErr != nil {
		return s.pushStartErr
	}
	if s.failStarts > 0 {
		s.failStarts--
		return fmt.Errorf("stream ended before DVR start")
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
		if s.listEmptyAfterStart > 0 {
			s.listEmptyAfterStart--
			return []mist.PushInfo{}, nil
		}
		targetURI := s.lastTargetURI
		if s.listTargetURI != "" {
			targetURI = s.listTargetURI
		}
		return []mist.PushInfo{
			{
				ID:         s.pushIDToReturn,
				StreamName: s.lastStreamName,
				TargetURI:  targetURI,
				ActualURI:  s.listActualURI,
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

func useFastInitialPushRetry(t *testing.T) {
	t.Helper()
	oldFor := initialPushRetryFor
	oldEvery := initialPushRetryEvery
	oldVisibleFor := pushListVisibilityFor
	oldVisiblePollFor := pushListVisibilityPollFor
	initialPushRetryFor = 5 * time.Millisecond
	initialPushRetryEvery = time.Millisecond
	pushListVisibilityFor = 5 * time.Millisecond
	pushListVisibilityPollFor = time.Millisecond
	t.Cleanup(func() {
		initialPushRetryFor = oldFor
		initialPushRetryEvery = oldEvery
		pushListVisibilityFor = oldVisibleFor
		pushListVisibilityPollFor = oldVisiblePollFor
	})
}

// --- StartRecording ---

func TestStartRecording_CreatesDirectories(t *testing.T) {
	mc := &startAwareFakeMist{pushIDToReturn: 42}
	dm := newDVRManagerWithMist(t, mc)

	err := dm.StartRecording("hash-create", "stream-1", "test-internal", "live+test-internal", "http://source", &ipcpb.DVRConfig{
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
	t.Cleanup(func() { ClearDVRSourceOverride("live+test-stream") })

	err := dm.StartRecording("hash-push", "stream-1", "test-stream", "live+test-stream", "http://source", &ipcpb.DVRConfig{
		SegmentDuration: 6,
	}, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if mc.lastStreamName != "live+test-stream" {
		t.Fatalf("expected stream name 'live+test-stream', got %s", mc.lastStreamName)
	}
	if got, ok := GetDVRSourceOverride("live+test-stream"); !ok || got != "http://source" {
		t.Fatalf("DVR source override = %q, %v; want http://source, true", got, ok)
	}
	for _, want := range []string{"audio=source", "video=source", "subtitle=none", "meta=none"} {
		if !strings.Contains(mc.lastTargetURI, want) {
			t.Fatalf("target URI %q missing %q", mc.lastTargetURI, want)
		}
	}
}

func TestStartRecording_UsesSourceRuntimeNameVerbatim(t *testing.T) {
	// Foghorn is authoritative for the runtime name. Helmsman uses what
	// Foghorn sends verbatim — for a mist_native source the value is the
	// bare internal name, NOT live+<internal>. This is the central bug
	// the runtime-name field exists to fix.
	mc := &startAwareFakeMist{pushIDToReturn: 11}
	dm := newDVRManagerWithMist(t, mc)

	err := dm.StartRecording("hash-local", "stream-1", "test-stream", "test-stream", "", &ipcpb.DVRConfig{
		SegmentDuration: 6,
	}, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if mc.lastStreamName != "test-stream" {
		t.Fatalf("expected bare 'test-stream' (mist_native), got %q", mc.lastStreamName)
	}
}

func TestStartRecording_RetriesInitialPushWarmup(t *testing.T) {
	useFastInitialPushRetry(t)
	mc := &startAwareFakeMist{pushIDToReturn: 12, failStarts: 2}
	dm := newDVRManagerWithMist(t, mc)

	err := dm.StartRecording("hash-retry", "stream-1", "test-stream", "live+test-stream", "dtsc://source/live+test-stream", &ipcpb.DVRConfig{
		SegmentDuration: 6,
	}, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if mc.startCalls != 3 {
		t.Fatalf("PushStart calls = %d, want 3", mc.startCalls)
	}
	if mc.lastStreamName != "live+test-stream" {
		t.Fatalf("stream source = %q, want live+test-stream", mc.lastStreamName)
	}
}

func TestStartRecording_RegistersSourceOverrideUnderRuntimeName(t *testing.T) {
	// When the source is on a remote node Foghorn passes a DTSC URL in
	// source_base_url plus the runtime name in source_runtime_name.
	// Helmsman uses the runtime name verbatim and registers the override
	// under that exact name — no derivation from the URL path.
	mc := &startAwareFakeMist{pushIDToReturn: 13}
	dm := newDVRManagerWithMist(t, mc)

	const sourceURL = "dtsc://edge-eu-1.media-eu-1.frameworks.network/view/live+test-stream"
	t.Cleanup(func() { ClearDVRSourceOverride("live+test-stream") })
	err := dm.StartRecording("hash-dtsc", "stream-1", "test-stream", "live+test-stream", sourceURL, &ipcpb.DVRConfig{
		SegmentDuration: 6,
	}, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if mc.lastStreamName != "live+test-stream" {
		t.Fatalf("expected Mist push stream live+test-stream, got %q", mc.lastStreamName)
	}
	if got, ok := GetDVRSourceOverride("live+test-stream"); !ok || got != sourceURL {
		t.Fatalf("DVR source override = %q, %v; want %q, true", got, ok, sourceURL)
	}
}

func TestStartRecording_PushStartError(t *testing.T) {
	useFastInitialPushRetry(t)
	mc := &startAwareFakeMist{
		pushIDToReturn: 0,
		pushStartErr:   fmt.Errorf("mist connection refused"),
	}
	dm := newDVRManagerWithMist(t, mc)

	err := dm.StartRecording("hash-fail", "stream-1", "test-stream", "live+test-stream", "http://source", &ipcpb.DVRConfig{}, nil)
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

	err := dm.StartRecording("hash-stop", "stream-1", "test-stop", "live+test-stop", "http://source", &ipcpb.DVRConfig{}, nil)
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

	err := dm.StartRecording("hash-stoperr", "stream-1", "test-stoperr", "live+test-stoperr", "http://source", &ipcpb.DVRConfig{}, nil)
	if err != nil {
		t.Fatalf("start failed: %v", err)
	}

	// StopRecording is the FORCE teardown (delete / startup deadline): it removes the
	// job even if PushStop failed. The conservative Foghorn-stop path
	// (StopRecordingWithSender) keeps the job on an unconfirmed stop — covered by
	// TestStopRecording_UnconfirmedStopKeepsJobAndEmitsNoReport.
	err = dm.StopRecording("hash-stoperr")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, exists := dm.jobs["hash-stoperr"]; exists {
		t.Fatal("force teardown must remove the job even after PushStop error")
	}
}

// --- createOrRecreatePush ---

// jobPushSnap builds the lock-free identity snapshot createOrRecreatePush now
// takes, from a test job that is not yet published into the manager.
func jobPushSnap(job *DVRJob) pushIdentity {
	return pushIdentity{streamName: job.StreamName, targetURI: job.TargetURI, dvrHash: job.DVRHash}
}

func TestCreateOrRecreatePush_New(t *testing.T) {
	mc := &startAwareFakeMist{pushIDToReturn: 55}
	dm := newDVRManagerWithMist(t, mc)

	job := &DVRJob{
		DVRHash:    "hash-new",
		StreamName: "live+test",
		TargetURI:  "/data/dvr/hash-new",
		Logger:     logging.NewLogger(),
	}

	pushID, _, err := dm.createOrRecreatePush(jobPushSnap(job))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if pushID != 55 {
		t.Fatalf("expected push ID 55, got %d", pushID)
	}
}

func TestCreateOrRecreatePush_WaitsForPushListVisibility(t *testing.T) {
	mc := &startAwareFakeMist{pushIDToReturn: 56, listEmptyAfterStart: 1}
	dm := newDVRManagerWithMist(t, mc)

	job := &DVRJob{
		DVRHash:    "hash-visible",
		StreamName: "live+test",
		TargetURI:  "/data/dvr/hash-visible",
		Logger:     logging.NewLogger(),
	}

	pushID, _, err := dm.createOrRecreatePush(jobPushSnap(job))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if pushID != 56 {
		t.Fatalf("expected push ID 56, got %d", pushID)
	}
}

// An existing push with the EXACT identity is ADOPTED idempotently, not
// stop-then-restarted — that avoids churn and, more importantly, a duplicate
// writer if a retry raced the restart.
func TestCreateOrRecreatePush_AdoptsExistingExactPush(t *testing.T) {
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

	pushID, _, err := dm.createOrRecreatePush(jobPushSnap(job))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if pushID != 10 {
		t.Fatalf("expected the existing exact push (10) to be adopted, got %d", pushID)
	}
	if len(mc.stoppedIDs) != 0 {
		t.Fatalf("adoption must not stop the existing push, stopped %v", mc.stoppedIDs)
	}
	if mc.pushStarted {
		t.Fatal("adoption must not start a duplicate push")
	}
}

func TestCreateOrRecreatePush_MatchesMistExpandedDVRTarget(t *testing.T) {
	const dvrHash = "20260526212719e6b54001bbf15619"
	mc := &startAwareFakeMist{
		pushIDToReturn: 77,
		listTargetURI:  "/storage/dvr/stream-1/" + dvrHash + "/segments/27_$segmentCounter.ts#m3u8=../" + dvrHash + ".m3u8",
	}
	dm := newDVRManagerWithMist(t, mc)

	job := &DVRJob{
		DVRHash:    dvrHash,
		StreamName: "dtsc://edge-eu-1.media-eu-1.frameworks.network/view/live+abc",
		TargetURI:  "/storage/dvr/stream-1/" + dvrHash + "/segments/$minute_$segmentCounter.ts#m3u8=../" + dvrHash + ".m3u8",
		Logger:     logging.NewLogger(),
	}

	pushID, _, err := dm.createOrRecreatePush(jobPushSnap(job))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if pushID != 77 {
		t.Fatalf("expected push ID 77, got %d", pushID)
	}
	if mc.startCalls != 1 {
		t.Fatalf("expected one PushStart call, got %d", mc.startCalls)
	}
}

// A push whose target Mist has EXPANDED (differs from the one we sent but still
// carries our hash under our runtime) is our push — it is adopted by exact
// runtime + hash identity, not restarted.
func TestCreateOrRecreatePush_AdoptsMistExpandedDVRTarget(t *testing.T) {
	const dvrHash = "20260526212719e6b54001bbf15619"
	mc := &staleCleanupFakeMist{
		existingPushes: []mist.PushInfo{
			{
				ID:         10,
				StreamName: "live+test",
				TargetURI:  "/storage/dvr/stream-1/" + dvrHash + "/segments/27_$segmentCounter.ts#m3u8=../" + dvrHash + ".m3u8",
			},
		},
		newPushID: 99,
	}
	dm := newDVRManagerWithMist(t, mc)

	job := &DVRJob{
		DVRHash:    dvrHash,
		StreamName: "live+test",
		TargetURI:  "/storage/dvr/stream-1/" + dvrHash + "/segments/$minute_$segmentCounter.ts#m3u8=../" + dvrHash + ".m3u8",
		Logger:     logging.NewLogger(),
	}

	pushID, _, err := dm.createOrRecreatePush(jobPushSnap(job))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if pushID != 10 {
		t.Fatalf("expected the Mist-expanded existing push (10) to be adopted, got %d", pushID)
	}
	if len(mc.stoppedIDs) != 0 || mc.pushStarted {
		t.Fatalf("adoption must not stop or restart; stopped=%v started=%v", mc.stoppedIDs, mc.pushStarted)
	}
}

func TestCreateOrRecreatePush_PushListError(t *testing.T) {
	useFastInitialPushRetry(t)
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
	_, _, err := dm.createOrRecreatePush(jobPushSnap(job))
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

func TestMaintainPushStatus_FinalizingJobSkipped(t *testing.T) {
	mc := &fakeMistClient{}
	dm := newDVRManagerWithMist(t, mc)

	// A finalizing job is on its way out; maintainPushStatus must not poke
	// MistServer for it.
	job := &DVRJob{
		DVRHash: "hash-finalizing",
		Status:  "finalizing",
		Logger:  logging.NewLogger(),
	}
	dm.jobs["hash-finalizing"] = job

	dm.maintainPushStatus(job)

	if atomic.LoadInt64(&mc.pushListCalls) != 0 {
		t.Fatal("expected no PushList calls for finalizing job")
	}
}

func TestMaintainPushStatus_PushWithErrors(t *testing.T) {
	useFastInitialPushRetry(t)
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
		SendFunc: func(_ *ipcpb.ControlMessage) {
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

func TestMaintainPushStatus_MissingPushWithSegmentsDoesNotRecreate(t *testing.T) {
	mc := &startAwareFakeMist{pushIDToReturn: 99}
	dm := newDVRManagerWithMist(t, mc)

	var completionSent bool
	job := &DVRJob{
		DVRHash:      "hash-natural-before-retry",
		PushID:       42,
		StreamName:   "live+test",
		TargetURI:    "/data/dvr/hash",
		Status:       "recording",
		MaxRetries:   10,
		RetryCount:   0,
		SegmentCount: 5,
		Logger:       logging.NewLogger(),
		SendFunc: func(_ *ipcpb.ControlMessage) {
			completionSent = true
		},
	}
	dm.jobs["hash-natural-before-retry"] = job

	dm.maintainPushStatus(job)

	if mc.startCalls != 0 {
		t.Fatalf("expected no PushStart calls for natural completion, got %d", mc.startCalls)
	}
	if _, exists := dm.jobs["hash-natural-before-retry"]; exists {
		t.Fatal("expected job to be removed")
	}
	if !completionSent {
		t.Fatal("expected completion notification to be sent")
	}
}

// erroredPushFakeMist holds a push that reports error logs; PushStop removes it,
// PushStart adds a fresh (clean) push. It records stop/start calls.
type erroredPushFakeMist struct {
	pushes     []mist.PushInfo
	stopCalls  []int
	startCalls int
	nextID     int
}

func (e *erroredPushFakeMist) PushList() ([]mist.PushInfo, error) {
	out := make([]mist.PushInfo, len(e.pushes))
	copy(out, e.pushes)
	return out, nil
}
func (e *erroredPushFakeMist) PushStop(id int) error {
	e.stopCalls = append(e.stopCalls, id)
	kept := e.pushes[:0]
	for _, p := range e.pushes {
		if p.ID != id {
			kept = append(kept, p)
		}
	}
	e.pushes = kept
	return nil
}
func (e *erroredPushFakeMist) PushStart(s, tURI string) error {
	e.startCalls++
	e.nextID++
	e.pushes = append(e.pushes, mist.PushInfo{ID: 900 + e.nextID, StreamName: s, TargetURI: tURI})
	return nil
}

// An errored (but present) push must be STOPPED and REPLACED by the monitor, not
// adopted as-is — otherwise a wedged push lingers forever.
func TestMaintainPushStatus_ErroredPushIsStoppedAndReplaced(t *testing.T) {
	useFastInitialPushRetry(t)
	const dvrHash = "hash-errored"
	target := "/data/dvr/s/" + dvrHash + "/segments/$c.ts#m3u8=../" + dvrHash + ".m3u8"
	mc := &erroredPushFakeMist{pushes: []mist.PushInfo{
		{ID: 7, StreamName: "live+x", TargetURI: target, Logs: []string{"error: DTSC connection reset"}},
	}}
	job := &DVRJob{
		DVRHash: dvrHash, InternalName: "x", StreamName: "live+x", TargetURI: target, PushID: 7,
		Config: &ipcpb.DVRConfig{}, Status: "recording", MaxRetries: MaxDVRRetries, SegmentCount: 3,
		Logger: logging.NewLogger(), SendFunc: func(*ipcpb.ControlMessage) {}, SyncedSegments: make(map[string]bool),
	}
	dm := &DVRManager{logger: logging.NewLogger(), jobs: map[string]*DVRJob{dvrHash: job}, mistClient: mc}

	dm.maintainPushStatus(job)

	if len(mc.stopCalls) != 1 || mc.stopCalls[0] != 7 {
		t.Fatalf("errored push (7) must be stopped, stopCalls=%v", mc.stopCalls)
	}
	if mc.startCalls != 1 {
		t.Fatalf("a fresh push must be started, startCalls=%d", mc.startCalls)
	}
	dm.mutex.RLock()
	pid := dm.jobs[dvrHash].PushID
	dm.mutex.RUnlock()
	if pid == 7 || pid == 0 {
		t.Fatalf("job must point at the NEW push, got PushID=%d", pid)
	}
}

// startOnceNeverVisibleFakeMist accepts PushStart but never lists the push
// (accepted-but-unconfirmed). It counts PushStart calls.
type startOnceNeverVisibleFakeMist struct{ startCalls int }

func (s *startOnceNeverVisibleFakeMist) PushList() ([]mist.PushInfo, error) { return nil, nil }
func (s *startOnceNeverVisibleFakeMist) PushStop(int) error                 { return nil }
func (s *startOnceNeverVisibleFakeMist) PushStart(string, string) error     { s.startCalls++; return nil }

// A recreate whose PushStart is accepted but never confirmed must NOT be
// re-issued on the next monitor pass (that would create a second writer). The
// job is quarantined at PushID 0 and only adopted by identity thereafter.
func TestMaintainPushStatus_UnconfirmedRecreateDoesNotReissue(t *testing.T) {
	useFastInitialPushRetry(t)
	const dvrHash = "hash-unconfirmed"
	target := "/data/dvr/s/" + dvrHash + "/segments/$c.ts#m3u8=../" + dvrHash + ".m3u8"
	mc := &startOnceNeverVisibleFakeMist{}
	job := &DVRJob{
		DVRHash: dvrHash, InternalName: "x", StreamName: "live+x", TargetURI: target, PushID: 7,
		Config: &ipcpb.DVRConfig{}, Status: "recording", MaxRetries: MaxDVRRetries, SegmentCount: 0,
		Logger: logging.NewLogger(), SendFunc: func(*ipcpb.ControlMessage) {}, SyncedSegments: make(map[string]bool),
	}
	dm := &DVRManager{logger: logging.NewLogger(), jobs: map[string]*DVRJob{dvrHash: job}, mistClient: mc}

	// Pass 1: push gone → recreate → PushStart accepted but never visible.
	dm.maintainPushStatus(job)
	dm.mutex.RLock()
	pid := dm.jobs[dvrHash].PushID
	dm.mutex.RUnlock()
	if pid != 0 {
		t.Fatalf("an unconfirmed recreate must quarantine at PushID 0, got %d", pid)
	}
	// Pass 2 (and 3): must NOT issue another PushStart.
	dm.maintainPushStatus(job)
	dm.maintainPushStatus(job)
	if mc.startCalls != 1 {
		t.Fatalf("PushStart must be issued exactly once across retries, got %d", mc.startCalls)
	}
}

// On stop, an accepted-but-unconfirmed push (we hold no id, PushID 0) must still
// be stopped by IDENTITY — never skipped — so a live writer is not orphaned and
// the recording is not falsely reported complete over it.
func TestStopRecording_UnconfirmedPushStoppedByIdentity(t *testing.T) {
	mc := &startAwareFakeMist{pushIDToReturn: 91}
	dm := newDVRManagerWithMist(t, mc)
	if err := dm.StartRecording("hash-unconf-stop", "stream-1", "test-u", "live+test-u", "http://source", &ipcpb.DVRConfig{}, nil); err != nil {
		t.Fatalf("start failed: %v", err)
	}
	// A push is live under our identity, but the job holds no id (quarantined).
	dm.mutex.Lock()
	dm.jobs["hash-unconf-stop"].PushID = 0
	dm.mutex.Unlock()

	if err := dm.StopRecording("hash-unconf-stop"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if atomic.LoadInt64(&mc.pushStopCalls) != 1 {
		t.Fatalf("a live push under our identity must be stopped even with PushID 0, got %d PushStop calls", atomic.LoadInt64(&mc.pushStopCalls))
	}
}

// A quarantined (PushID 0) recording whose accepted-but-unconfirmed push is not
// visible in the push list must NEVER re-issue PushStart — the command may have
// been accepted while Mist has not yet listed it, and re-issuing would create a
// second writer against the same target. Even with the retry grace long elapsed,
// the monitor adopts-if-listed and otherwise waits; convergence is the session
// lifecycle's job (Foghorn's STREAM_END → StopDVRForEndedSource), not a re-issue.
func TestMaintainPushStatus_QuarantinedPushNeverReissues(t *testing.T) {
	useFastInitialPushRetry(t)
	mc := &startAwareFakeMist{pushIDToReturn: 45}
	dm := newDVRManagerWithMist(t, mc)
	if err := dm.StartRecording("hash-quarantine", "stream-1", "test-q", "live+test-q", "http://source", &ipcpb.DVRConfig{}, nil); err != nil {
		t.Fatalf("start failed: %v", err)
	}
	dm.mutex.Lock()
	j := dm.jobs["hash-quarantine"]
	j.PushID = 0
	j.RetryCount = 1
	j.LastPushAttempt = time.Now().Add(-10 * time.Minute) // grace long elapsed
	dm.mutex.Unlock()
	mc.started = false // accepted-but-unlistable: push never appears in the list
	startsBefore := mc.startCalls

	dm.maintainPushStatus(j)
	dm.maintainPushStatus(j)

	if mc.startCalls != startsBefore {
		t.Fatalf("quarantined push must NOT re-issue (double-writer hazard), startCalls %d -> %d", startsBefore, mc.startCalls)
	}
}

// listFailFakeMist cannot confirm the push list (PushList errors), so an id-unknown
// stop can never prove nothing is live.
type listFailFakeMist struct{}

func (listFailFakeMist) PushList() ([]mist.PushInfo, error) { return nil, fmt.Errorf("list boom") }
func (listFailFakeMist) PushStop(int) error                 { return nil }
func (listFailFakeMist) PushStart(string, string) error     { return nil }

// When PushStop fails, the writer may still be live: the stop is NOT confirmed, so
// no DVRStopped (success OR failed) may be emitted — a failed report would
// terminalize the artifact on Foghorn and clear its stop obligation — and the job
// must be kept so the durable stop obligation re-drives the stop.
func TestStopRecording_UnconfirmedStopKeepsJobAndEmitsNoReport(t *testing.T) {
	mc := &startAwareFakeMist{pushIDToReturn: 55, pushStopErr: fmt.Errorf("mist unreachable")}
	dm := newDVRManagerWithMist(t, mc)
	if err := dm.StartRecording("hash-nostop", "stream-1", "test-n", "live+test-n", "http://source", &ipcpb.DVRConfig{}, nil); err != nil {
		t.Fatalf("start failed: %v", err)
	}
	stopped := 0
	sendFunc := func(m *ipcpb.ControlMessage) {
		if m.GetDvrStopped() != nil {
			stopped++
		}
	}
	if err := dm.StopRecordingWithSender("hash-nostop", sendFunc); err != nil {
		t.Fatalf("unconfirmed stop must return nil (no failed report), got %v", err)
	}
	if stopped != 0 {
		t.Fatalf("no DVRStopped may be emitted while the writer may be live, got %d", stopped)
	}
	dm.mutex.RLock()
	_, exists := dm.jobs["hash-nostop"]
	dm.mutex.RUnlock()
	if !exists {
		t.Fatal("job must be kept for stop-obligation retry when the stop is unconfirmed")
	}
}

// When the id is unknown (PushID 0) and PushList fails, we cannot confirm nothing
// is live — same invariant: no terminal report, keep the job.
func TestStopRecording_UnconfirmedListFailureKeepsJob(t *testing.T) {
	dm := newDVRManagerWithMist(t, listFailFakeMist{})
	job := &DVRJob{
		DVRHash: "hash-listfail", StreamName: "live+x", TargetURI: "/data/dvr/x/seg.ts",
		PushID: 0, Status: "recording", Config: &ipcpb.DVRConfig{},
		Logger: logging.NewLogger(), SyncedSegments: make(map[string]bool), StartTime: time.Now(),
	}
	dm.mutex.Lock()
	dm.jobs["hash-listfail"] = job
	dm.mutex.Unlock()

	stopped := 0
	sendFunc := func(m *ipcpb.ControlMessage) {
		if m.GetDvrStopped() != nil {
			stopped++
		}
	}
	if err := dm.StopRecordingWithSender("hash-listfail", sendFunc); err != nil {
		t.Fatalf("unconfirmed stop must return nil, got %v", err)
	}
	if stopped != 0 {
		t.Fatalf("no DVRStopped for an unconfirmable stop, got %d", stopped)
	}
	dm.mutex.RLock()
	_, exists := dm.jobs["hash-listfail"]
	dm.mutex.RUnlock()
	if !exists {
		t.Fatal("job must be kept when the stop cannot be confirmed")
	}
}

// PushID 0 with an EMPTY successful list must NOT confirm a stop: an accepted push
// can be live but unlisted (the same non-authoritative-absence invariant the
// monitor uses). No DVRStopped is emitted and the job is kept for the durable
// stop-obligation retry — never a false completion over a possibly-live writer.
func TestStopRecording_UnconfirmedEmptyListIsNotAuthoritative(t *testing.T) {
	dm := newDVRManagerWithMist(t, &startOnceNeverVisibleFakeMist{})
	job := &DVRJob{
		DVRHash: "hash-empty", StreamName: "live+x", TargetURI: "/data/dvr/x/seg.ts",
		PushID: 0, Status: "recording", Config: &ipcpb.DVRConfig{},
		Logger: logging.NewLogger(), SyncedSegments: make(map[string]bool), StartTime: time.Now(),
	}
	dm.mutex.Lock()
	dm.jobs["hash-empty"] = job
	dm.mutex.Unlock()

	stopped := 0
	sendFunc := func(m *ipcpb.ControlMessage) {
		if m.GetDvrStopped() != nil {
			stopped++
		}
	}
	if err := dm.StopRecordingWithSender("hash-empty", sendFunc); err != nil {
		t.Fatalf("unconfirmed stop must return nil, got %v", err)
	}
	if stopped != 0 {
		t.Fatalf("an empty list is not authoritative; no DVRStopped may be emitted, got %d", stopped)
	}
	dm.mutex.RLock()
	_, exists := dm.jobs["hash-empty"]
	dm.mutex.RUnlock()
	if !exists {
		t.Fatal("job must be kept when an empty list cannot confirm the stop")
	}
}
