package leases

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// resetGlobalsAfter snapshots the process-global cleanup state and singletons
// and restores them on test cleanup, so a test that installs a tracker or
// flips the boot state machine can't leak into siblings.
func resetGlobalsAfter(t *testing.T) {
	t.Helper()
	bootMu.Lock()
	savedState := cleanupState
	savedReconcile := mistReconcileDone
	bootMu.Unlock()
	savedTracker := GlobalTracker()
	savedSources := GlobalSourceRegistry()
	savedHeat := GlobalHeat()
	savedDeferred := GlobalDeferredStore()
	t.Cleanup(func() {
		bootMu.Lock()
		cleanupState = savedState
		mistReconcileDone = savedReconcile
		bootMu.Unlock()
		Install(savedTracker, savedSources, savedHeat, savedDeferred)
	})
}

// setBootPaused forces the boot state machine back to its pre-reconcile posture.
func setBootPaused() {
	bootMu.Lock()
	cleanupState = int32(StateBootPaused)
	mistReconcileDone = false
	bootMu.Unlock()
}

// The boot state machine starts paused and unpauses for good once the first
// Mist reconcile round-trip completes.
func TestCleanupStateMachine(t *testing.T) {
	resetGlobalsAfter(t)
	setBootPaused()

	if got := GetCleanupState(); got != StateBootPaused {
		t.Fatalf("expected StateBootPaused at boot, got %v", got)
	}
	MarkMistReconcileDone()
	if got := GetCleanupState(); got != StateNormal {
		t.Fatalf("expected StateNormal after reconcile, got %v", got)
	}
}

// IsDestructiveCleanupAllowed gates every destructive path: no tracker means the
// lease subsystem is opt-out (always allowed); with a tracker installed it must
// stay denied until the boot pause clears.
func TestIsDestructiveCleanupAllowed(t *testing.T) {
	resetGlobalsAfter(t)

	t.Run("no tracker installed allows cleanup", func(t *testing.T) {
		Install(nil, nil, nil, nil)
		setBootPaused()
		if !IsDestructiveCleanupAllowed() {
			t.Fatal("nil tracker must allow cleanup regardless of boot state")
		}
	})

	t.Run("tracker installed denies during boot pause", func(t *testing.T) {
		Install(NewTracker(nil, NewHeatTracker()), nil, nil, nil)
		setBootPaused()
		if IsDestructiveCleanupAllowed() {
			t.Fatal("cleanup must be denied while boot-paused with a tracker")
		}
		MarkMistReconcileDone()
		if !IsDestructiveCleanupAllowed() {
			t.Fatal("cleanup must be allowed once reconcile completes")
		}
	})
}

// DeleteFileIfUnleased: empty path errors; with no tracker it removes directly;
// with a tracker pinning the path it refuses via the TOCTOU-safe delegate.
func TestDeleteFileIfUnleased_Wrapper(t *testing.T) {
	resetGlobalsAfter(t)

	if err := DeleteFileIfUnleased(""); err == nil {
		t.Fatal("empty path must error")
	}

	t.Run("nil tracker removes directly", func(t *testing.T) {
		Install(nil, nil, nil, nil)
		path := filepath.Join(t.TempDir(), "file.mp4")
		if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := DeleteFileIfUnleased(path); err != nil {
			t.Fatalf("delete with nil tracker: %v", err)
		}
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatal("file must be gone")
		}
	})

	t.Run("leased path is refused", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "leased.mp4")
		if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		tr := NewTracker(nil, NewHeatTracker())
		tr.AcquireSource("vod+abc", []string{path}, AssetKey{Type: "vod", Hash: "abc"}, nil, false)
		Install(tr, nil, nil, nil)

		if err := DeleteFileIfUnleased(path); !errors.Is(err, ErrLeaseHeld) {
			t.Fatalf("expected ErrLeaseHeld, got %v", err)
		}
		if _, err := os.Stat(path); err != nil {
			t.Fatal("leased file must survive")
		}
	})
}

// DeleteDVRDirIfUnleased: empty dir errors; with no tracker it removes the tree.
func TestDeleteDVRDirIfUnleased_Wrapper(t *testing.T) {
	resetGlobalsAfter(t)

	if err := DeleteDVRDirIfUnleased("", "h"); err == nil {
		t.Fatal("empty dvr dir must error")
	}

	Install(nil, nil, nil, nil)
	dvrDir := filepath.Join(t.TempDir(), "dvr", "stream", "h")
	if err := os.MkdirAll(filepath.Join(dvrDir, "segments"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dvrDir, "segments", "seg.ts"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := DeleteDVRDirIfUnleased(dvrDir, "h"); err != nil {
		t.Fatalf("delete dvr dir with nil tracker: %v", err)
	}
	if _, err := os.Stat(dvrDir); !os.IsNotExist(err) {
		t.Fatal("dvr dir must be gone")
	}
}
