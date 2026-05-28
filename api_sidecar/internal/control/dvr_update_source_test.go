package control

import (
	"errors"
	"sync"
	"testing"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/mist"
)

// stubMistClient is a deterministic DVRMistClient fake. PushList returns
// the recorded set; PushStart appends a new push with auto-incremented ID
// and the supplied stream/target; PushStop removes by ID.
type stubMistClient struct {
	mu       sync.Mutex
	nextID   int
	pushes   []mist.PushInfo
	calls    []string
	pushFail error
	listFail error
}

func newStubMist() *stubMistClient { return &stubMistClient{nextID: 100} }

func (s *stubMistClient) PushStart(streamName, targetURI string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, "start:"+streamName)
	if s.pushFail != nil {
		return s.pushFail
	}
	s.nextID++
	s.pushes = append(s.pushes, mist.PushInfo{
		ID:         s.nextID,
		StreamName: streamName,
		TargetURI:  targetURI,
	})
	return nil
}

func (s *stubMistClient) PushStop(pushID int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, "stop")
	if s.pushFail != nil {
		return s.pushFail
	}
	out := s.pushes[:0]
	for _, p := range s.pushes {
		if p.ID != pushID {
			out = append(out, p)
		}
	}
	s.pushes = out
	return nil
}

func (s *stubMistClient) PushList() ([]mist.PushInfo, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, "list")
	if s.listFail != nil {
		return nil, s.listFail
	}
	out := make([]mist.PushInfo, len(s.pushes))
	copy(out, s.pushes)
	return out, nil
}

// TestUpdateSource_MissingJob_Idempotent: caller has no way to know the
// hash is gone in the brief window between artifact lookup and dispatch
// arrival. Idempotent miss keeps the takeover path silent.
func TestUpdateSource_MissingJob_Idempotent(t *testing.T) {
	dm := &DVRManager{
		logger: logging.NewLogger(),
		jobs:   map[string]*DVRJob{},
	}
	refreshed, err := dm.UpdateSource("missing", "live+x", "dtsc://node/live+x")
	if refreshed {
		t.Error("refreshed should be false for missing job")
	}
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
}

// TestUpdateSource_HappyPath_StopsOldThenRecreates locks the operational
// sequence that distinguishes UpdateSource from StartRecording: stop the
// recorded PushID before any job mutation (so PushStop targets the
// actually-running push), swap overrides, then recreate. Reordering
// would either leak the old push (PushID overwritten before stop) or
// stop the wrong push (mutation before stop).
func TestUpdateSource_HappyPath_StopsOldThenRecreates(t *testing.T) {
	stub := newStubMist()
	stub.pushes = append(stub.pushes, mist.PushInfo{
		ID:         42,
		StreamName: "live+old-source",
		TargetURI:  "dvr+abc123",
	})

	dm := &DVRManager{
		logger:     logging.NewLogger(),
		mistClient: stub,
		jobs: map[string]*DVRJob{
			"abc123": {
				DVRHash:    "abc123",
				StreamName: "live+old-source",
				TargetURI:  "dvr+abc123",
				PushID:     42,
				Logger:     logging.NewLogger(),
			},
		},
	}

	RegisterDVRSourceOverride("live+old-source", "dtsc://old-node/live+old-source")
	t.Cleanup(func() { ClearDVRSourceOverride("live+old-source"); ClearDVRSourceOverride("live+new-source") })

	refreshed, err := dm.UpdateSource("abc123", "live+new-source", "dtsc://new-node/live+new-source")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !refreshed {
		t.Fatal("refreshed should be true on success")
	}

	// Job's PushID was updated to the recreated push.
	if dm.jobs["abc123"].PushID == 42 {
		t.Error("PushID still pointing at stopped push; new push not captured")
	}
	if dm.jobs["abc123"].StreamName != "live+new-source" {
		t.Errorf("StreamName not updated: %q", dm.jobs["abc123"].StreamName)
	}

	// Overrides: old cleared, new registered.
	if _, ok := GetDVRSourceOverride("live+old-source"); ok {
		t.Error("old override survived; takeover would pull from gone source")
	}
	if v, _ := GetDVRSourceOverride("live+new-source"); v != "dtsc://new-node/live+new-source" {
		t.Errorf("new override missing or wrong: %q", v)
	}
}

// TestUpdateSource_SameStreamNameDoesNotClearOverride: when the runtime
// name doesn't change (e.g. live+<x> stays live+<x>), the old override
// IS the new override after re-registration. Clearing it would create
// a window where the next DVR PushStart has no override.
func TestUpdateSource_SameStreamNameDoesNotClearOverride(t *testing.T) {
	stub := newStubMist()
	dm := &DVRManager{
		logger:     logging.NewLogger(),
		mistClient: stub,
		jobs: map[string]*DVRJob{
			"h": {
				DVRHash:    "h",
				StreamName: "live+same",
				TargetURI:  "dvr+h",
				PushID:     0,
				Logger:     logging.NewLogger(),
			},
		},
	}
	t.Cleanup(func() { ClearDVRSourceOverride("live+same") })

	_, err := dm.UpdateSource("h", "live+same", "dtsc://new/live+same")
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if v, _ := GetDVRSourceOverride("live+same"); v != "dtsc://new/live+same" {
		t.Errorf("override not refreshed in place: %q", v)
	}
}

// TestUpdateSource_PushRecreateFail_ReturnsErrButCommittedState: when
// createOrRecreatePush fails (Mist transient), UpdateSource has already
// mutated job state — return refreshed=true so the caller knows
// progress was made, plus the error so monitor-loop retry can resume.
func TestUpdateSource_PushRecreateFail_ReturnsErrButCommittedState(t *testing.T) {
	stub := newStubMist()
	stub.pushFail = errors.New("mist offline")

	dm := &DVRManager{
		logger:     logging.NewLogger(),
		mistClient: stub,
		jobs: map[string]*DVRJob{
			"h": {
				DVRHash:    "h",
				StreamName: "live+old",
				TargetURI:  "dvr+h",
				PushID:     7,
				Logger:     logging.NewLogger(),
			},
		},
	}
	t.Cleanup(func() { ClearDVRSourceOverride("live+old"); ClearDVRSourceOverride("live+new") })

	refreshed, err := dm.UpdateSource("h", "live+new", "dtsc://new/live+new")
	if err == nil {
		t.Fatal("expected error on push recreate failure")
	}
	if !refreshed {
		t.Error("refreshed should be true — state was mutated before recreate")
	}
	// StreamName commit happened even though recreate failed; monitor loop
	// will recreate on next tick using the committed name.
	if dm.jobs["h"].StreamName != "live+new" {
		t.Errorf("StreamName not committed before recreate: %q", dm.jobs["h"].StreamName)
	}
}
