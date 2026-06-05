package control

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
)

// Per-segment local cache index — sidecar
// Live (non-durable) state about every DVR segment file present on this
// node. Foghorn's foghorn.dvr_segments table is the durable source of
// truth; this index is a hot cache for eviction-decision speed and active-
// view refcounting. After a sidecar restart it rebuilds from disk by
// walking dvr/<stream>/<dvr_hash>/segments/ and asking Foghorn for ledger
// state via SendRestoreLocalSegmentIndex (bounded ~500 names per call).
//
// Bounded-operations invariant: this index is keyed by the local disk
// inventory the sidecar can see, not by any per-artifact total. A 90-day
// 24/7 stream that has evicted most of its archive locally only carries
// entries for what is actually on disk right now.

// LocalSegmentRef is one segment entry in the local cache index.
type LocalSegmentRef struct {
	DVRArtifactID   string
	SegmentName     string
	LocalPath       string
	SizeBytes       int64
	Uploaded        bool      // ledger said status='uploaded'
	InRollingWindow bool      // currently referenced by the active Mist playlist (if any)
	ActiveRecording bool      // belongs to an active DVRJob on this node
	ActiveViews     int       // refcount of in-flight playbacks/relay reads holding this segment
	LastAccessed    time.Time // bumped on view acquire
	PinnedUntil     time.Time // active-view lease (clip harvest, in-flight finalization)
	LedgerStatus    string    // pending|uploaded|failed_upload|deleted_local|orphan_unreachable|lost_local|reclaimed
}

type localSegmentKey struct {
	DVRArtifactID string
	SegmentName   string
}

// LocalSegmentIndex is the process-global in-memory index. Safe for
// concurrent use.
type LocalSegmentIndex struct {
	mu      sync.RWMutex
	entries map[localSegmentKey]*LocalSegmentRef
	logger  logging.Logger
}

var (
	localSegmentIndex   *LocalSegmentIndex
	localSegmentIndexMu sync.Mutex
)

// LocalSegmentIndexInstance returns the process-global index, lazily
// constructing it on first call.
func LocalSegmentIndexInstance(logger logging.Logger) *LocalSegmentIndex {
	localSegmentIndexMu.Lock()
	defer localSegmentIndexMu.Unlock()
	if localSegmentIndex == nil {
		localSegmentIndex = &LocalSegmentIndex{
			entries: make(map[localSegmentKey]*LocalSegmentRef),
			logger:  logger,
		}
	}
	return localSegmentIndex
}

// MarkUploaded records that a segment finished uploading to S3. Idempotent.
// Called from the syncSpecificSegment success path so the index agrees with
// the ledger without an extra round-trip.
func (idx *LocalSegmentIndex) MarkUploaded(dvrHash, segmentName, localPath string, sizeBytes int64) {
	if idx == nil {
		return
	}
	idx.TrackCachedSegment(dvrHash, segmentName, localPath, sizeBytes, true)
}

// TrackCachedSegment records a local segment file that is already backed by
// S3. Recording uploads pass activeRecording=true; chapter relay reuse passes
// false because those files are a playback cache, not part of the active
// recorder's hot set.
func (idx *LocalSegmentIndex) TrackCachedSegment(dvrHash, segmentName, localPath string, sizeBytes int64, activeRecording bool) {
	if idx == nil {
		return
	}
	key := localSegmentKey{dvrHash, segmentName}
	idx.mu.Lock()
	defer idx.mu.Unlock()
	ref, ok := idx.entries[key]
	if !ok {
		ref = &LocalSegmentRef{
			DVRArtifactID: dvrHash,
			SegmentName:   segmentName,
		}
		idx.entries[key] = ref
	}
	ref.LocalPath = localPath
	ref.SizeBytes = sizeBytes
	ref.Uploaded = true
	ref.LedgerStatus = "uploaded"
	ref.ActiveRecording = activeRecording
}

func (idx *LocalSegmentIndex) PinCachedSegment(dvrHash, segmentName string, until time.Time) {
	if idx == nil || until.IsZero() {
		return
	}
	key := localSegmentKey{dvrHash, segmentName}
	idx.mu.Lock()
	defer idx.mu.Unlock()
	if ref, ok := idx.entries[key]; ok && until.After(ref.PinnedUntil) {
		ref.PinnedUntil = until
		ref.LastAccessed = time.Now()
	}
}

// MarkRollingWindow updates the InRollingWindow flag for a segment. The
// sidecar's active DVRManager calls this when it parses the current Mist
// playlist; segments that fall out of the window flip to false.
func (idx *LocalSegmentIndex) MarkRollingWindow(dvrHash, segmentName string, inWindow bool) {
	if idx == nil {
		return
	}
	key := localSegmentKey{dvrHash, segmentName}
	idx.mu.Lock()
	defer idx.mu.Unlock()
	if ref, ok := idx.entries[key]; ok {
		ref.InRollingWindow = inWindow
	}
}

// AcquireView bumps the ActiveViews refcount for a segment. Called when a
// chapter playback or relay read starts using this local file. Pair with
// ReleaseView on completion.
func (idx *LocalSegmentIndex) AcquireView(dvrHash, segmentName string) {
	if idx == nil {
		return
	}
	key := localSegmentKey{dvrHash, segmentName}
	idx.mu.Lock()
	defer idx.mu.Unlock()
	if ref, ok := idx.entries[key]; ok {
		ref.ActiveViews++
		ref.LastAccessed = time.Now()
	}
}

// ReleaseView decrements the ActiveViews refcount.
func (idx *LocalSegmentIndex) ReleaseView(dvrHash, segmentName string) {
	if idx == nil {
		return
	}
	key := localSegmentKey{dvrHash, segmentName}
	idx.mu.Lock()
	defer idx.mu.Unlock()
	if ref, ok := idx.entries[key]; ok && ref.ActiveViews > 0 {
		ref.ActiveViews--
	}
}

// Forget removes a segment entry. Called after the sidecar deletes the
// local file (post-eviction) so the index doesn't grow unbounded.
func (idx *LocalSegmentIndex) Forget(dvrHash, segmentName string) {
	if idx == nil {
		return
	}
	idx.mu.Lock()
	defer idx.mu.Unlock()
	delete(idx.entries, localSegmentKey{dvrHash, segmentName})
}

// LocalPath returns the indexed on-disk path for a segment, if present.
// Finalize-time retry uses this after the active DVRJob has been removed:
// the local file may still exist under the DVR directory even though Mist
// has no active Mist push.
func (idx *LocalSegmentIndex) LocalPath(dvrHash, segmentName string) (string, bool) {
	if idx == nil {
		return "", false
	}
	idx.mu.RLock()
	defer idx.mu.RUnlock()
	ref, ok := idx.entries[localSegmentKey{dvrHash, segmentName}]
	if !ok || ref.LocalPath == "" {
		return "", false
	}
	return ref.LocalPath, true
}

// EvictionEligible reports whether a segment satisfies the full eviction
// predicate: uploaded, outside the live rolling window, no active views,
// last access older than the cache TTL.
func (idx *LocalSegmentIndex) EvictionEligible(dvrHash, segmentName string, cacheTTL time.Duration) bool {
	if idx == nil {
		return false
	}
	idx.mu.RLock()
	defer idx.mu.RUnlock()
	ref, ok := idx.entries[localSegmentKey{dvrHash, segmentName}]
	if !ok {
		return false
	}
	if !ref.Uploaded || ref.InRollingWindow || ref.ActiveViews > 0 {
		return false
	}
	if !ref.PinnedUntil.IsZero() && time.Now().Before(ref.PinnedUntil) {
		return false
	}
	if cacheTTL > 0 && !ref.LastAccessed.IsZero() && time.Since(ref.LastAccessed) < cacheTTL {
		return false
	}
	return true
}

// RestoreFromDisk walks the local DVR storage tree, batches discovered
// (artifact_hash, segment_name) pairs, and asks Foghorn for current
// ledger state via SendRestoreLocalSegmentIndex. Populates the in-memory
// index from the responses. Bounded by local disk inventory only — never
// asks Foghorn for "all segments for this DVR".
//
// Layout assumed: {basePath}/dvr/{stream_internal_name}/{dvr_artifact_id}/segments/{segment_name}
//
// Called once at sidecar startup, after the control stream connects. Any
// subsequent restart goes through the same path; concurrent calls are
// safe (the index is keyed; later writes overwrite earlier).
func (idx *LocalSegmentIndex) RestoreFromDisk(ctx context.Context, basePath string) error {
	if idx == nil {
		return errors.New("local segment index not initialized")
	}
	dvrRoot := filepath.Join(basePath, "dvr")
	streamDirs, err := os.ReadDir(dvrRoot)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	const batchSize = 500
	for _, streamDir := range streamDirs {
		if !streamDir.IsDir() {
			continue
		}
		artifactDirs, err := os.ReadDir(filepath.Join(dvrRoot, streamDir.Name()))
		if err != nil {
			continue
		}
		for _, artifactDir := range artifactDirs {
			if !artifactDir.IsDir() {
				continue
			}
			dvrHash := artifactDir.Name()
			segDir := filepath.Join(dvrRoot, streamDir.Name(), dvrHash, "segments")
			segEntries, err := os.ReadDir(segDir)
			if err != nil {
				continue
			}
			batch := make([]string, 0, batchSize)
			localPaths := make(map[string]string, len(segEntries))
			sizes := make(map[string]int64, len(segEntries))
			active := IsActiveDVR(dvrHash)
			for _, segEntry := range segEntries {
				if segEntry.IsDir() {
					continue
				}
				name := segEntry.Name()
				// Skip dotfiles and any non-segment files
				if strings.HasPrefix(name, ".") {
					continue
				}
				info, err := segEntry.Info()
				if err != nil {
					continue
				}
				batch = append(batch, name)
				localPaths[name] = filepath.Join(segDir, name)
				sizes[name] = info.Size()
				if len(batch) >= batchSize {
					if err := idx.flushRestoreBatch(ctx, dvrHash, batch, localPaths, sizes, active); err != nil {
						idx.logger.WithError(err).WithField("dvr_hash", dvrHash).Warn("Restore batch failed; continuing")
					}
					batch = batch[:0]
				}
			}
			if len(batch) > 0 {
				if err := idx.flushRestoreBatch(ctx, dvrHash, batch, localPaths, sizes, active); err != nil {
					idx.logger.WithError(err).WithField("dvr_hash", dvrHash).Warn("Final restore batch failed; continuing")
				}
			}
		}
	}
	return nil
}

func (idx *LocalSegmentIndex) flushRestoreBatch(
	ctx context.Context,
	dvrHash string,
	names []string,
	localPaths map[string]string,
	sizes map[string]int64,
	active bool,
) error {
	resp, err := SendRestoreLocalSegmentIndex(ctx, dvrHash, names)
	if err != nil {
		return err
	}
	known := make(map[string]*ipcpb.DVRSegmentRef, len(resp.GetSegments()))
	for _, s := range resp.GetSegments() {
		known[s.GetSegmentName()] = s
	}
	idx.mu.Lock()
	defer idx.mu.Unlock()
	for _, name := range names {
		ref := &LocalSegmentRef{
			DVRArtifactID:   dvrHash,
			SegmentName:     name,
			LocalPath:       localPaths[name],
			SizeBytes:       sizes[name],
			ActiveRecording: active,
			LastAccessed:    time.Time{},
		}
		if seg, ok := known[name]; ok {
			ref.LedgerStatus = seg.GetStatus()
			ref.Uploaded = seg.GetStatus() == "uploaded" || seg.GetStatus() == "deleted_local"
		}
		// Names not in the ledger response (orphans on disk) get inserted
		// with empty LedgerStatus so the eviction sweep can reclaim them.
		idx.entries[localSegmentKey{dvrHash, name}] = ref
	}
	return nil
}

// Snapshot returns a copy of all index entries. Used by ops/inspection
// surfaces; not used on the hot path. Bounded by current disk inventory.
func (idx *LocalSegmentIndex) Snapshot() []LocalSegmentRef {
	if idx == nil {
		return nil
	}
	idx.mu.RLock()
	defer idx.mu.RUnlock()
	out := make([]LocalSegmentRef, 0, len(idx.entries))
	for _, ref := range idx.entries {
		out = append(out, *ref)
	}
	return out
}
