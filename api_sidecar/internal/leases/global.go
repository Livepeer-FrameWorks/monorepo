package leases

import (
	"errors"
	"os"
	"sync"
)

// CleanupState reflects whether destructive cleanup paths may run. At boot
// the state is StateBootPaused until one successful Mist reconciliation
// has happened.
type CleanupState int32

const (
	StateBootPaused CleanupState = 1
	StateNormal     CleanupState = 0
)

// Process-global instances. Construct once in main; access via the package
// accessors below.
var (
	globalMu             sync.RWMutex
	globalTracker        *Tracker
	globalSourceRegistry *SourceRegistry
	globalHeat           *HeatTracker
	globalDeferredStore  *DeferredStore

	cleanupState      = int32(StateBootPaused)
	mistReconcileDone = false
	bootMu            sync.Mutex
)

// Install wires the singletons. Safe to call once at startup. Subsequent
// calls overwrite, which is useful for tests; production callers should
// invoke this exactly once.
func Install(tracker *Tracker, sources *SourceRegistry, heat *HeatTracker, deferred *DeferredStore) {
	globalMu.Lock()
	defer globalMu.Unlock()
	globalTracker = tracker
	globalSourceRegistry = sources
	globalHeat = heat
	globalDeferredStore = deferred
}

func GlobalTracker() *Tracker {
	globalMu.RLock()
	defer globalMu.RUnlock()
	return globalTracker
}

func GlobalSourceRegistry() *SourceRegistry {
	globalMu.RLock()
	defer globalMu.RUnlock()
	return globalSourceRegistry
}

func GlobalHeat() *HeatTracker {
	globalMu.RLock()
	defer globalMu.RUnlock()
	return globalHeat
}

func GlobalDeferredStore() *DeferredStore {
	globalMu.RLock()
	defer globalMu.RUnlock()
	return globalDeferredStore
}

// Cleanup state machine

func GetCleanupState() CleanupState {
	bootMu.Lock()
	defer bootMu.Unlock()
	return CleanupState(cleanupState)
}

// MarkMistReconcileDone records that the first successful Mist reconciliation
// round trip completed (both GetActiveStreams and GetClients). Cleanup
// unpauses once this fires.
func MarkMistReconcileDone() {
	bootMu.Lock()
	defer bootMu.Unlock()
	mistReconcileDone = true
	if mistReconcileDone {
		cleanupState = int32(StateNormal)
	}
}

// IsDestructiveCleanupAllowed returns false when the boot state machine is
// pausing destructive cleanup. Callers should treat the path as a skip-and-
// retry, not a failure.
//
// When the lease tracker has not been installed (e.g. unit tests that don't
// call InitLeases), this returns true: the lease subsystem is opt-in and
// callers without it behave as if there were never any leases to respect.
func IsDestructiveCleanupAllowed() bool {
	if GlobalTracker() == nil {
		return true
	}
	return GetCleanupState() == StateNormal
}

// Destructive-helper utilities. Both take the absolute local path and ask the
// global tracker. Returns ErrLeaseHeld when at least one lease pins the
// target.

// DeleteFileIfUnleased removes a single file (clip / VOD / sidecar) only
// when no lease holds it. TOCTOU-safe: the lease check and the os.Remove
// run under the tracker mutex inside Tracker.DeletePathIfUnleased.
func DeleteFileIfUnleased(absPath string) error {
	if absPath == "" {
		return errors.New("empty path")
	}
	t := GlobalTracker()
	if t == nil {
		return os.Remove(absPath)
	}
	return t.DeletePathIfUnleased(absPath)
}

// DeleteDVRDirIfUnleased recursively removes a DVR recording directory only
// when no chapter lease (for the dvr_hash) AND no degraded-DVR posture is
// active. TOCTOU-safe via Tracker.DeleteDVRDirIfUnleased.
func DeleteDVRDirIfUnleased(dvrDir, dvrHash string) error {
	if dvrDir == "" {
		return errors.New("empty dvr dir")
	}
	t := GlobalTracker()
	if t == nil {
		return os.RemoveAll(dvrDir)
	}
	return t.DeleteDVRDirIfUnleased(dvrDir, dvrHash)
}
