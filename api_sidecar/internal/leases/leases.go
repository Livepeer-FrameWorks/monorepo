// Package leases protects local cached artifacts from deletion while Mist is
// using them. Two lease kinds:
//
//   - SourceLease lives between STREAM_SOURCE (returns local path) and
//     STREAM_END. This is the primary disk protection. Mist re-reads the file
//     across viewers, seeks, and reconnects; the file must stay until the
//     stream itself is gone.
//   - ViewerLease lives between USER_NEW and USER_END. This is heat/accounting
//     only — viewer churn must not toggle disk protection because a single
//     stream serves many viewers without firing new STREAM_SOURCE events.
//
// A file is deletable only when both lease types are absent for it.
package leases

import (
	"errors"
	"os"
	"sync"
	"time"
)

// ErrLeaseHeld is returned by destructive helpers when at least one lease
// still pins the target.
var ErrLeaseHeld = errors.New("lease held")

// AssetKey identifies a logical asset. For VOD the Hash is the Mist
// internal_name (the suffix of vod+), not the artifact hash — those are not
// the same value in Foghorn. For DVR the Hash is the dvr_artifact_id.
type AssetKey struct {
	Type      string // "vod" | "dvr"
	Hash      string
	ChapterID string // dvr only
}

// SegmentIndex is the subset of control.LocalSegmentIndex this package needs.
// Decoupling via interface keeps the leases package free of an api_sidecar/internal/control import.
type SegmentIndex interface {
	AcquireView(dvrHash, segmentName string)
	ReleaseView(dvrHash, segmentName string)
}

// SourceLease — primary disk protection.
type SourceLease struct {
	StreamName   string
	LocalPaths   []string
	Key          AssetKey
	SegmentNames []string // dvr only
	Degraded     bool     // dvr with unrecoverable segment list; cleanup pauses while held
	Acquired     time.Time
	LastSeen     time.Time
	missingPolls int // reconciliation 2-strikes
}

// ViewerLease — heat / accounting.
type ViewerLease struct {
	SessionID    string
	StreamName   string
	LocalPath    string
	Acquired     time.Time
	LastSeen     time.Time
	missingPolls int // reconciliation 2-strikes
	refs         int // for idempotent re-acquire of same sessionID
}

// Tracker is the process-global lease tracker. Safe for concurrent use.
type Tracker struct {
	mu sync.RWMutex

	sources map[string]*SourceLease // streamName → lease
	viewers map[string]*ViewerLease // sessionID  → lease

	// Reverse indexes for fast lookups during cleanup paths.
	pathSource  map[string]map[string]struct{}   // localPath → set of streamNames
	pathViewer  map[string]map[string]struct{}   // localPath → set of sessionIDs
	assetSource map[AssetKey]map[string]struct{} // assetKey → set of streamNames

	// Segment-level refcount fan-out for DVR. Nil-safe: when nil, AcquireSource
	// for DVR still tracks the SegmentNames in the SourceLease but skips view
	// refcounting (useful in tests).
	segments SegmentIndex

	// Heat is bumped on viewer first-acquire.
	heat *HeatTracker

	// degradedDvrCount is the count of DVR source leases with Degraded=true.
	// When >0, DVR destructive cleanup must pause.
	degradedDvrCount int
}

// NewTracker constructs a tracker. Pass a SegmentIndex implementation
// (typically control.LocalSegmentIndexInstance(...)) and the heat tracker.
func NewTracker(segments SegmentIndex, heat *HeatTracker) *Tracker {
	return &Tracker{
		sources:     make(map[string]*SourceLease),
		viewers:     make(map[string]*ViewerLease),
		pathSource:  make(map[string]map[string]struct{}),
		pathViewer:  make(map[string]map[string]struct{}),
		assetSource: make(map[AssetKey]map[string]struct{}),
		segments:    segments,
		heat:        heat,
	}
}

// AcquireSource installs (or refreshes) a SourceLease for streamName. For DVR
// leases with a non-empty segmentNames list, every named segment gets an
// AcquireView call on the underlying segment index. Calling AcquireSource
// again for the same streamName with the same segment list refreshes LastSeen
// without double-incrementing ActiveViews.
func (t *Tracker) AcquireSource(streamName string, localPaths []string, key AssetKey, segmentNames []string, degraded bool) {
	if t == nil || streamName == "" {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()

	if existing, ok := t.sources[streamName]; ok {
		existing.LastSeen = time.Now()
		existing.missingPolls = 0
		return
	}

	lease := &SourceLease{
		StreamName:   streamName,
		LocalPaths:   append([]string(nil), localPaths...),
		Key:          key,
		SegmentNames: append([]string(nil), segmentNames...),
		Degraded:     degraded,
		Acquired:     time.Now(),
		LastSeen:     time.Now(),
	}
	t.sources[streamName] = lease

	for _, p := range lease.LocalPaths {
		t.addPathSource(p, streamName)
	}
	if key.Hash != "" || key.Type != "" {
		t.addAssetSource(key, streamName)
	}

	if key.Type == "dvr" && t.segments != nil {
		for _, seg := range lease.SegmentNames {
			t.segments.AcquireView(key.Hash, seg)
		}
	}

	if degraded {
		t.degradedDvrCount++
	}
}

// ReleaseSource removes a SourceLease and undoes its segment refcounts.
// Idempotent for unknown streamName.
func (t *Tracker) ReleaseSource(streamName string) {
	if t == nil || streamName == "" {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.releaseSourceLocked(streamName)
}

func (t *Tracker) releaseSourceLocked(streamName string) {
	lease, ok := t.sources[streamName]
	if !ok {
		return
	}
	delete(t.sources, streamName)

	for _, p := range lease.LocalPaths {
		t.removePathSource(p, streamName)
	}
	if lease.Key.Hash != "" || lease.Key.Type != "" {
		t.removeAssetSource(lease.Key, streamName)
	}

	if lease.Key.Type == "dvr" && t.segments != nil {
		for _, seg := range lease.SegmentNames {
			t.segments.ReleaseView(lease.Key.Hash, seg)
		}
	}

	if lease.Degraded && t.degradedDvrCount > 0 {
		t.degradedDvrCount--
	}
}

// AcquireViewer is idempotent for same sessionID: refreshes LastSeen, bumps
// the internal refcount, and does not double-bump heat. First-time acquires
// touch the heat tracker once.
func (t *Tracker) AcquireViewer(sessionID, streamName, localPath string) {
	if t == nil || sessionID == "" {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()

	if existing, ok := t.viewers[sessionID]; ok {
		existing.LastSeen = time.Now()
		existing.missingPolls = 0
		existing.refs++
		return
	}

	lease := &ViewerLease{
		SessionID:  sessionID,
		StreamName: streamName,
		LocalPath:  localPath,
		Acquired:   time.Now(),
		LastSeen:   time.Now(),
		refs:       1,
	}
	t.viewers[sessionID] = lease
	if localPath != "" {
		t.addPathViewer(localPath, sessionID)
	}
	if t.heat != nil && localPath != "" {
		t.heat.Touch(localPath)
	}
}

// ReleaseViewer removes a ViewerLease. Idempotent for unknown sessionID.
func (t *Tracker) ReleaseViewer(sessionID string) {
	if t == nil || sessionID == "" {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.releaseViewerLocked(sessionID)
}

func (t *Tracker) releaseViewerLocked(sessionID string) {
	lease, ok := t.viewers[sessionID]
	if !ok {
		return
	}
	delete(t.viewers, sessionID)
	if lease.LocalPath != "" {
		t.removePathViewer(lease.LocalPath, sessionID)
	}
}

// HasSourceLease reports whether streamName already has a SourceLease.
// Used by the boot-recovery rebuild path to avoid clobbering an existing
// lease with a fresh one.
func (t *Tracker) HasSourceLease(streamName string) bool {
	if t == nil || streamName == "" {
		return false
	}
	t.mu.RLock()
	defer t.mu.RUnlock()
	_, ok := t.sources[streamName]
	return ok
}

// DeletePathIfUnleased is the TOCTOU-safe deletion path for clip/VOD files.
// The lease check and the os.Remove run under the same lock, so a STREAM_SOURCE
// arriving between the check and the remove cannot interleave: AcquireSource
// will block until this function returns, then either install the lease for a
// still-present file (cleanup didn't fire) or against a gone path (cleanup
// succeeded — Mist's STREAM_SOURCE will then resolve elsewhere).
//
// Trade-off: the tracker mutex is held during a small synchronous unlink. For
// clip/VOD this is a single file system call. DVR directory removal uses
// DeleteDVRDirIfUnleased instead, which may take longer on big trees; callers
// should not invoke that while time-sensitive lease ops are in flight.
func (t *Tracker) DeletePathIfUnleased(absPath string) error {
	if t == nil {
		return os.Remove(absPath)
	}
	if absPath == "" {
		return errors.New("empty path")
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if _, ok := t.pathSource[absPath]; ok {
		return ErrLeaseHeld
	}
	if _, ok := t.pathViewer[absPath]; ok {
		return ErrLeaseHeld
	}
	return os.Remove(absPath)
}

// DeleteDVRDirIfUnleased is the TOCTOU-safe DVR directory removal. Tracker
// mutex is held during the recursive remove; callers should not be holding it
// elsewhere. Refuses when degraded-cleanup is active (any DVR source lease
// without a resolved hash exists) so an unresolved chapter cannot lose its
// backing tree.
func (t *Tracker) DeleteDVRDirIfUnleased(dvrDir, dvrHash string) error {
	if t == nil {
		return os.RemoveAll(dvrDir)
	}
	if dvrDir == "" {
		return errors.New("empty dvr dir")
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.degradedDvrCount > 0 {
		return ErrLeaseHeld
	}
	for k := range t.assetSource {
		if k.Type == "dvr" && k.Hash == dvrHash {
			return ErrLeaseHeld
		}
	}
	return os.RemoveAll(dvrDir)
}

// IsPathLeased returns true when at least one source or viewer lease pins path.
func (t *Tracker) IsPathLeased(path string) bool {
	if t == nil || path == "" {
		return false
	}
	t.mu.RLock()
	defer t.mu.RUnlock()
	if _, ok := t.pathSource[path]; ok {
		return true
	}
	if _, ok := t.pathViewer[path]; ok {
		return true
	}
	return false
}

// IsAssetLeased returns true when any source lease pins the asset key.
// Useful for DVR directory deletes that need to refuse if any chapter is
// currently held.
func (t *Tracker) IsAssetLeased(key AssetKey) bool {
	if t == nil {
		return false
	}
	t.mu.RLock()
	defer t.mu.RUnlock()
	// Asset-level check: match by Type+Hash regardless of ChapterID. Any
	// chapter lease for the same DVR hash counts.
	for k := range t.assetSource {
		if k.Type == key.Type && k.Hash == key.Hash {
			return true
		}
	}
	return false
}

// DegradedDvrCleanupActive reports whether any DVR source lease is in degraded
// mode. Callers use this to pause DVR destructive cleanup.
func (t *Tracker) DegradedDvrCleanupActive() bool {
	if t == nil {
		return false
	}
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.degradedDvrCount > 0
}

// ReconcileSources releases SourceLeases that have been absent from Mist's
// active-streams report for two consecutive successful polls. Callers MUST
// only invoke after a successful GetActiveStreams call; on poll error they
// must not call this function.
func (t *Tracker) ReconcileSources(present map[string]struct{}) []string {
	if t == nil {
		return nil
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	var released []string
	for name, lease := range t.sources {
		if _, ok := present[name]; ok {
			lease.missingPolls = 0
			lease.LastSeen = time.Now()
			continue
		}
		lease.missingPolls++
		if lease.missingPolls >= 2 {
			released = append(released, name)
		}
	}
	for _, name := range released {
		t.releaseSourceLocked(name)
	}
	return released
}

// ReconcileViewers does the same for ViewerLeases against active-clients.
func (t *Tracker) ReconcileViewers(present map[string]struct{}) []string {
	if t == nil {
		return nil
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	var released []string
	for id, lease := range t.viewers {
		if _, ok := present[id]; ok {
			lease.missingPolls = 0
			lease.LastSeen = time.Now()
			continue
		}
		lease.missingPolls++
		if lease.missingPolls >= 2 {
			released = append(released, id)
		}
	}
	for _, id := range released {
		t.releaseViewerLocked(id)
	}
	return released
}

// SourceCount returns the current number of active source leases. For metrics.
func (t *Tracker) SourceCount() int {
	if t == nil {
		return 0
	}
	t.mu.RLock()
	defer t.mu.RUnlock()
	return len(t.sources)
}

// ViewerCount returns the current number of active viewer leases. For metrics.
func (t *Tracker) ViewerCount() int {
	if t == nil {
		return 0
	}
	t.mu.RLock()
	defer t.mu.RUnlock()
	return len(t.viewers)
}

// Reverse-index helpers (must be called with t.mu held).

func (t *Tracker) addPathSource(path, streamName string) {
	set, ok := t.pathSource[path]
	if !ok {
		set = make(map[string]struct{})
		t.pathSource[path] = set
	}
	set[streamName] = struct{}{}
}

func (t *Tracker) removePathSource(path, streamName string) {
	set, ok := t.pathSource[path]
	if !ok {
		return
	}
	delete(set, streamName)
	if len(set) == 0 {
		delete(t.pathSource, path)
	}
}

func (t *Tracker) addPathViewer(path, sessionID string) {
	set, ok := t.pathViewer[path]
	if !ok {
		set = make(map[string]struct{})
		t.pathViewer[path] = set
	}
	set[sessionID] = struct{}{}
}

func (t *Tracker) removePathViewer(path, sessionID string) {
	set, ok := t.pathViewer[path]
	if !ok {
		return
	}
	delete(set, sessionID)
	if len(set) == 0 {
		delete(t.pathViewer, path)
	}
}

func (t *Tracker) addAssetSource(key AssetKey, streamName string) {
	set, ok := t.assetSource[key]
	if !ok {
		set = make(map[string]struct{})
		t.assetSource[key] = set
	}
	set[streamName] = struct{}{}
}

func (t *Tracker) removeAssetSource(key AssetKey, streamName string) {
	set, ok := t.assetSource[key]
	if !ok {
		return
	}
	delete(set, streamName)
	if len(set) == 0 {
		delete(t.assetSource, key)
	}
}
