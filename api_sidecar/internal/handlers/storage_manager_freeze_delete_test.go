package handlers

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sirupsen/logrus"
	logtest "github.com/sirupsen/logrus/hooks/test"

	"frameworks/api_sidecar/internal/storage"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
)

// Staging rc16: a clip was deleted ~2 s before its proactive freeze upload got an answer. The node removed the clip
// files under the in-flight upload, the retry failed to reopen the file, and the freeze was logged as an ERROR and
// reported as a lost local copy. A delete is an authoritative end of the artifact: the freeze must stop at once,
// report a plain failure that names the deletion, and not raise a missing-copy alarm.
func TestHandleFreezeRequest_ClipDeletedDuringUploadEndsCleanly(t *testing.T) {
	h := setupDeleteHandlers(t)
	sm := newTestStorageManager(t)
	sm.basePath = h.storagePath
	hook := logtest.NewLocal(sm.logger)
	logger.AddHook(hook)

	const hash = "202610010245238df6112ab7d37d6f"
	clipDir := filepath.Join(h.storagePath, "clips", "stream-a")
	if err := os.MkdirAll(clipDir, 0o755); err != nil {
		t.Fatal(err)
	}
	clipPath := filepath.Join(clipDir, hash+".mkv")
	if err := os.WriteFile(clipPath, make([]byte, 64*1024), 0o644); err != nil {
		t.Fatal(err)
	}

	var puts atomic.Int32
	putStarted := make(chan struct{}, 4)
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		puts.Add(1)
		putStarted <- struct{}{}
		select {
		case <-release:
			w.WriteHeader(http.StatusServiceUnavailable)
		case <-r.Context().Done():
		}
	}))
	defer server.Close()
	sm.presignedClient = storage.NewPresignedClient(sm.logger)

	type syncReport struct {
		status, errMsg string
		localMissing   bool
	}
	var mu sync.Mutex
	var reports []syncReport
	var lifecycle []ipcpb.StorageLifecycleData_Action
	sm.sendSyncComplete = func(_, _, status string, _ uint64, errMsg string, _ bool, localMissing bool) error {
		mu.Lock()
		defer mu.Unlock()
		reports = append(reports, syncReport{status: status, errMsg: errMsg, localMissing: localMissing})
		return nil
	}
	sm.sendStorageLifecycle = func(d *ipcpb.StorageLifecycleData) error {
		mu.Lock()
		defer mu.Unlock()
		lifecycle = append(lifecycle, d.GetAction())
		return nil
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		sm.HandleFreezeRequest(&ipcpb.FreezeRequest{
			RequestId: "req-clip", AssetHash: hash, AssetType: "clip", LocalPath: clipPath,
			PresignedPutUrl: server.URL + "/clips/" + hash + ".mkv.staging.req-clip",
		})
	}()

	select {
	case <-putStarted:
	case <-time.After(10 * time.Second):
		t.Fatal("freeze upload never reached object storage")
	}
	if _, err := h.DeleteClip(hash); err != nil {
		t.Fatalf("DeleteClip: %v", err)
	}
	close(release)
	select {
	case <-done:
	case <-time.After(20 * time.Second):
		t.Fatal("freeze did not finish after the clip was deleted")
	}

	mu.Lock()
	defer mu.Unlock()
	if len(reports) != 1 {
		t.Fatalf("sync reports = %+v, want exactly one", reports)
	}
	if r := reports[0]; r.status != "failed" || r.localMissing || !strings.Contains(r.errMsg, "deleted") {
		t.Fatalf("sync report = %+v, want status=failed, local_missing=false and a reason naming the deletion", r)
	}
	for _, a := range lifecycle {
		if a == ipcpb.StorageLifecycleData_ACTION_LOCAL_MISSING || a == ipcpb.StorageLifecycleData_ACTION_SYNC_FAILED {
			t.Fatalf("freeze of a deleted clip emitted lifecycle %v; a deletion is not a lost or failed copy", a)
		}
	}
	if n := puts.Load(); n != 1 {
		t.Fatalf("object storage saw %d PUTs, want 1: nothing may be uploaded for the clip after its deletion", n)
	}
	for _, e := range hook.AllEntries() {
		if e.Level <= logrus.ErrorLevel {
			t.Fatalf("freeze of a deleted clip logged at %s: %q %v", e.Level, e.Message, e.Data)
		}
	}
}

// A freeze command Foghorn sent before the deletion can reach the node after the clip files are gone. That is the
// deletion's outcome, not a lost local copy.
func TestHandleFreezeRequest_ArrivesAfterClipDeleteEndsCleanly(t *testing.T) {
	h := setupDeleteHandlers(t)
	sm := newTestStorageManager(t)
	sm.basePath = h.storagePath
	hook := logtest.NewLocal(sm.logger)

	const hash = "20261001024523deadbeef00112233"
	clipDir := filepath.Join(h.storagePath, "clips", "stream-a")
	if err := os.MkdirAll(clipDir, 0o755); err != nil {
		t.Fatal(err)
	}
	clipPath := filepath.Join(clipDir, hash+".mkv")
	if err := os.WriteFile(clipPath, []byte("clip"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := h.DeleteClip(hash); err != nil {
		t.Fatalf("DeleteClip: %v", err)
	}

	var gotStatus, gotErr string
	gotLocalMissing := true
	sm.sendSyncComplete = func(_, _, status string, _ uint64, errMsg string, _ bool, localMissing bool) error {
		gotStatus, gotErr, gotLocalMissing = status, errMsg, localMissing
		return nil
	}
	sm.sendStorageLifecycle = func(d *ipcpb.StorageLifecycleData) error {
		t.Errorf("freeze of a deleted clip sent lifecycle %v", d.GetAction())
		return nil
	}
	sm.HandleFreezeRequest(&ipcpb.FreezeRequest{
		RequestId: "req-late", AssetHash: hash, AssetType: "clip", LocalPath: clipPath,
		PresignedPutUrl: "https://s3.example.com/clip.mkv?presigned",
	})

	if gotStatus != "failed" || gotLocalMissing || !strings.Contains(gotErr, "deleted") {
		t.Fatalf("sync report = (%q, %q, local_missing=%v), want a failure naming the deletion without local_missing", gotStatus, gotErr, gotLocalMissing)
	}
	for _, e := range hook.AllEntries() {
		if e.Level <= logrus.ErrorLevel {
			t.Fatalf("late freeze of a deleted clip logged at %s: %q", e.Level, e.Message)
		}
	}
}
