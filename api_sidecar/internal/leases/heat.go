package leases

import (
	"sync"
	"time"
)

// HeatEntry tracks playback demand for a single local path.
type HeatEntry struct {
	AccessCount  uint64
	LastAccessed time.Time
}

// HeatTracker keeps per-path playback heat outside of any scan-replaced
// structure. The artifact index in poller.go is rebuilt by full replace on
// every scan, which erases heat written there.
type HeatTracker struct {
	mu      sync.RWMutex
	entries map[string]*HeatEntry
}

func NewHeatTracker() *HeatTracker {
	return &HeatTracker{entries: make(map[string]*HeatEntry)}
}

// Touch records one playback access for path. Called by ViewerLease acquire
// on first reference per session (not on idempotent re-acquire).
func (h *HeatTracker) Touch(path string) {
	if h == nil || path == "" {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	entry, ok := h.entries[path]
	if !ok {
		entry = &HeatEntry{}
		h.entries[path] = entry
	}
	entry.AccessCount++
	entry.LastAccessed = time.Now()
}

// Lookup returns the current heat for path. ok is false when nothing has
// touched this path yet.
func (h *HeatTracker) Lookup(path string) (HeatEntry, bool) {
	if h == nil || path == "" {
		return HeatEntry{}, false
	}
	h.mu.RLock()
	defer h.mu.RUnlock()
	entry, ok := h.entries[path]
	if !ok {
		return HeatEntry{}, false
	}
	return *entry, true
}

// ReapNotOnDisk drops entries whose paths no longer exist on disk. Optional
// periodic GC; callers pass a stat function that returns true when the path
// still exists.
func (h *HeatTracker) ReapNotOnDisk(exists func(path string) bool) int {
	if h == nil || exists == nil {
		return 0
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	removed := 0
	for path := range h.entries {
		if !exists(path) {
			delete(h.entries, path)
			removed++
		}
	}
	return removed
}
