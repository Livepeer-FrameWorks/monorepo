package control

import (
	"bufio"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	sidecarcfg "frameworks/api_sidecar/internal/config"
	"frameworks/api_sidecar/internal/storage"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/hls"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/mist"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"

	"google.golang.org/protobuf/types/known/timestamppb"
)

// HTTP client for S3 presigned URL uploads
var httpClient = &http.Client{
	Timeout: 2 * time.Minute,
	Transport: &http.Transport{
		TLSNextProto: map[string]func(string, *tls.Conn) http.RoundTripper{},
	},
}

// newHTTPRequest creates an HTTP request with context
func newHTTPRequest(ctx context.Context, method, url string, body io.Reader) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, method, url, body)
	if err != nil {
		return nil, err
	}
	return req, nil
}

// DVR push retry constants
const (
	MaxDVRRetries         = 10               // Maximum push recreation attempts
	InitialRetryDelay     = 5 * time.Second  // Initial delay between retries
	MaxRetryDelay         = 60 * time.Second // Maximum delay between retries
	dvrEvictionBatchSize  = 500
	maxDVREvictionBatches = 10
)

var (
	PushMonitorInterval       = 5 * time.Second // How often to check push status (var so tests can shorten it)
	initialPushRetryFor       = 30 * time.Second
	initialPushRetryEvery     = 2 * time.Second
	pushListVisibilityFor     = 2 * time.Second
	pushListVisibilityPollFor = 100 * time.Millisecond
	// dvrStartupTimeout bounds how long a job may stay "starting" (e.g. an
	// accepted-but-unconfirmed push whose exact identity never appears) before
	// the monitor stops it. A var so tests can shorten it.
	dvrStartupTimeout = 5 * time.Minute
)

// DVRJob represents a running DVR recording session
type DVRJob struct {
	DVRHash      string
	InternalName string
	// SourceRuntimeName is the Foghorn-authoritative Mist runtime name of
	// the source stream Mist will push from (live+<x>, pull+<x>, or bare
	// <x> for mist_native). Populated from DVRStartRequest.source_runtime_name.
	// Used verbatim — Helmsman never re-derives or applies a live+ default.
	SourceRuntimeName string
	SourceURL         string
	Config            *ipcpb.DVRConfig
	StartTime         time.Time
	PushID            int // MistServer push ID
	OutputDir         string
	ManifestPath      string
	SendFunc          func(*ipcpb.ControlMessage)
	Logger            logging.Logger

	// Progress tracking
	SegmentCount   int
	TotalSizeBytes uint64
	Status         string

	// Retry logic
	RetryCount      int
	LastPushAttempt time.Time
	MaxRetries      int
	TargetURI       string // Store for recreation
	StreamName      string // Store for recreation

	// Dual-storage: Incremental sync tracking
	SyncedSegments map[string]bool // Track which segments already synced to S3
	syncMutex      sync.Mutex      // Protects SyncedSegments

	// pushGeneration is bumped under DVRManager.mutex on every change to push
	// identity (PushID / StreamName / TargetURI) or terminal status. A push
	// recreator snapshots it before releasing the lock for Mist network calls
	// and commits a new PushID only if it is unchanged; a bumped generation
	// means another writer (a stop or a concurrent recreate) won the race, so
	// the freshly-created push is stale and must be stopped.
	pushGeneration uint64
}

// DVRMistClient abstracts MistServer push operations so tests can inject fakes.
type DVRMistClient interface {
	PushStart(streamName, targetURI string) error
	PushStop(pushID int) error
	PushList() ([]mist.PushInfo, error)
}

// DVRManager manages active DVR recording sessions
type DVRManager struct {
	logger      logging.Logger
	jobs        map[string]*DVRJob // DVR hash -> job
	mutex       sync.RWMutex
	storagePath string
	storageCap  uint64
	mistClient  DVRMistClient
	// diskCheck is the precondition called before starting/continuing a recording.
	// Tests inject a stub so they don't depend on host disk pressure.
	// Nil means use the production storage admission check.
	diskCheck func(path string, requiredBytes uint64) error
	// metaWriter persists the DVR job identity sidecar. Nil means production
	// writeDVRJobMeta; tests inject a per-manager stub for deterministic fault
	// injection (avoids mutable package-global shared state under parallel tests).
	metaWriter func(outputDir string, meta dvrJobMeta) error

	// startupTimeout / pushMonitorInterval override the package defaults per
	// manager. Zero means use the default. Kept per-manager (not a mutable
	// global) so tests can shorten them without racing monitor goroutines that
	// other tests left running.
	startupTimeout      time.Duration
	pushMonitorInterval time.Duration

	// stopTombstones records, per dvr_hash, the highest DVRStop command generation
	// seen. A DVRStart whose generation is <= a hash's stop tombstone is superseded by
	// a newer stop and MUST be rejected idempotently — otherwise a stop that overtook a
	// start (the start committed 'starting' on Foghorn but its DVRStart lost the race
	// to the DVRStop) would start a live writer behind a terminal Foghorn row. Bounded
	// by TTL eviction. Guarded by mutex.
	stopTombstones map[string]dvrStopTombstone

	// absenceEvidence accumulates, per dvr_hash, evidence that a recording's Mist push is
	// genuinely gone before any DESTRUCTIVE teardown (delete, restart-recovery completion,
	// startup-deadline terminalization). A single empty PushList is NOT proof of absence —
	// an accepted push can be momentarily unlisted — so a terminal action requires
	// dvrAbsenceThreshold observations, spaced at least dvrAbsenceMinInterval apart and
	// spanning at least dvrAbsenceGrace, that all found the push absent from a successful
	// list AND saw an UNCHANGED on-disk fingerprint (segment-file count, total bytes, and the
	// newest segment's name + mtime — see dvrFingerprint). Any change — a new segment, a growing
	// current segment, a rolling window advancing its newest — resets it, so a writer that is still
	// PROGRESSING on disk is never classified absent. Bounded residual (honest): a writer that is
	// genuinely live but STALLED (absent from a successful list AND advancing no segment for the full
	// convergence window) IS terminalized after the grace — this is a bounded-confidence filesystem
	// policy, not a liveness proof. Guarded by mutex.
	absenceEvidence map[string]*dvrAbsenceState

	// nowFn is the clock (nil = time.Now); tests inject a controllable clock so the
	// time-based absence grace/interval are deterministic.
	nowFn func() time.Time
}

const (
	// dvrAbsenceThreshold is the minimum number of no-progress absent observations.
	dvrAbsenceThreshold = 3
	// dvrAbsenceGrace is the minimum wall-clock span from the first absent observation to
	// convergence — a real elapsed grace, not just a count, so a burst of re-drives can never
	// terminalize a recording that has only briefly been unlisted.
	dvrAbsenceGrace = 30 * time.Second
	// dvrAbsenceMinInterval is the minimum spacing between COUNTED observations, so N rapid
	// concurrent teardown commands collapse to one observation instead of converging at once.
	dvrAbsenceMinInterval = 5 * time.Second
)

// dvrAbsenceState is the per-hash bounded-absence evidence.
type dvrAbsenceState struct {
	observations    int       // spaced no-change absent observations
	observed        bool      // at least one observation recorded (lastFingerprint is valid)
	lastFingerprint string    // recording fingerprint at the last observation
	firstAbsentAt   time.Time // first counted absent observation (grace anchor)
	lastCountedAt   time.Time // last counted observation (min-interval rate limit)
}

func (dm *DVRManager) now() time.Time {
	if dm.nowFn != nil {
		return dm.nowFn()
	}
	return time.Now()
}

type dvrStopTombstone struct {
	gen int64
	at  time.Time
}

// dvrStopTombstoneTTL bounds the tombstone map. It must exceed the window in which a
// stale DVRStart can still be (re-)dispatched for a stopped hash — comfortably past
// DVRStartingRecoveryJob's hard grace — so a real superseding start is never missed.
const dvrStopTombstoneTTL = time.Hour

// recordDVRStopTombstone marks dvrHash stopped at command generation gen (highest
// wins), so a later-arriving lower-or-equal-generation DVRStart is rejected. Called
// on every DVRStop, before the stop is applied, so a stop that arrives before its
// recording even exists still blocks the racing start.
func (dm *DVRManager) recordDVRStopTombstone(dvrHash string, gen int64) {
	if dvrHash == "" {
		return
	}
	now := time.Now()
	dm.mutex.Lock()
	defer dm.mutex.Unlock()
	if dm.stopTombstones == nil {
		dm.stopTombstones = make(map[string]dvrStopTombstone)
	}
	if cur, ok := dm.stopTombstones[dvrHash]; !ok || gen > cur.gen {
		dm.stopTombstones[dvrHash] = dvrStopTombstone{gen: gen, at: now}
	}
	// Opportunistic eviction keeps the map bounded (small in practice).
	for h, t := range dm.stopTombstones {
		if now.Sub(t.at) > dvrStopTombstoneTTL {
			delete(dm.stopTombstones, h)
		}
	}
}

// observeAbsenceConverged records ONE bounded-absence observation for a DVR whose push was
// found absent from a SUCCESSFUL PushList, and reports whether the evidence has converged
// (the push is now treated as genuinely gone). segments/sizeBytes are the recording's
// current manifest segment count and total recorded byte size; readOK is false when the
// recording could not be located/read (inconclusive — NEVER counts as idle, never converges).
//
// Progress since the last observation — MORE segments OR a LARGER byte size (a live writer
// still appending to its current segment grows the bytes even without a new manifest entry)
// — resets the evidence. Convergence requires all of: >= dvrAbsenceThreshold observations,
// each spaced >= dvrAbsenceMinInterval apart (so rapid re-drives collapse to one), and a
// total span >= dvrAbsenceGrace. A caller MUST NOT do anything destructive until this
// returns true.
func (dm *DVRManager) observeAbsenceConverged(dvrHash, fingerprint string, readOK bool) bool {
	if dvrHash == "" || !readOK {
		return false
	}
	now := dm.now()
	dm.mutex.Lock()
	defer dm.mutex.Unlock()
	if dm.absenceEvidence == nil {
		dm.absenceEvidence = make(map[string]*dvrAbsenceState)
	}
	st := dm.absenceEvidence[dvrHash]
	if st == nil {
		st = &dvrAbsenceState{}
		dm.absenceEvidence[dvrHash] = st
	}
	if st.observed && fingerprint != st.lastFingerprint {
		// ANY change to the fingerprint (a new segment in a bounded rolling window — where
		// count stays constant and bytes can stay flat or fall — a growing current segment, a
		// new newest-segment mtime/name) means the writer is still live. Reset the window.
		st.observations = 0
		st.lastFingerprint = fingerprint
		st.firstAbsentAt = time.Time{}
		st.lastCountedAt = time.Time{}
		return false
	}
	// Rate-limit: an observation closer than the min interval to the last counted one does not
	// advance the evidence (bounds a burst of concurrent/rapid teardown attempts).
	if st.observed && !st.lastCountedAt.IsZero() && now.Sub(st.lastCountedAt) < dvrAbsenceMinInterval {
		return false
	}
	if st.firstAbsentAt.IsZero() {
		st.firstAbsentAt = now
	}
	st.observed = true
	st.lastFingerprint = fingerprint
	st.lastCountedAt = now
	st.observations++
	return st.observations >= dvrAbsenceThreshold && now.Sub(st.firstAbsentAt) >= dvrAbsenceGrace
}

// clearAbsenceEvidence drops a DVR's accumulated absence evidence — called when a live push
// is found (or the recording is otherwise resolved) so a later absence starts a fresh count.
func (dm *DVRManager) clearAbsenceEvidence(dvrHash string) {
	if dvrHash == "" {
		return
	}
	dm.mutex.Lock()
	delete(dm.absenceEvidence, dvrHash)
	dm.mutex.Unlock()
}

// dvrStartSupersededByStop reports whether a DVRStart at startGen for dvrHash is
// superseded by an already-recorded stop (tombstone generation >= startGen).
func (dm *DVRManager) dvrStartSupersededByStop(dvrHash string, startGen int64) bool {
	dm.mutex.RLock()
	defer dm.mutex.RUnlock()
	t, ok := dm.stopTombstones[dvrHash]
	return ok && t.gen >= startGen
}

// persistJobMeta writes the start-descriptor sidecar via the manager's injected
// writer (production writeDVRJobMeta when unset).
func (dm *DVRManager) persistJobMeta(outputDir string, meta dvrJobMeta) error {
	if dm.metaWriter != nil {
		return dm.metaWriter(outputDir, meta)
	}
	return writeDVRJobMeta(outputDir, meta)
}

func (dm *DVRManager) hasSpaceFor(path string, requiredBytes uint64) error {
	if dm.diskCheck != nil {
		return dm.diskCheck(path, requiredBytes)
	}
	return storage.HasSpaceForWithinCapacity(path, requiredBytes, dm.storageCap)
}

// Global DVR manager instance
var dvrManager *DVRManager
var dvrManagerOnce sync.Once

// initDVRManager initializes the global DVR manager
func initDVRManager() {
	dvrManagerOnce.Do(func() {
		logger := logging.NewLoggerWithService("dvr-manager")
		storagePath := sidecarcfg.GetStoragePath()

		dvrManager = &DVRManager{
			logger:      logger,
			jobs:        make(map[string]*DVRJob),
			storagePath: storagePath,
			storageCap:  sidecarcfg.GetStorageCapacityBytes(),
			mistClient:  mist.NewClient(logger),
		}

		logger.WithField("storage_path", storagePath).Info("DVR manager initialized")
	})
}

// GetDVRManager returns the global DVR manager instance
func GetDVRManager() *DVRManager {
	return dvrManager
}

// RecoverActiveDVRJobsFromMist rebuilds in-memory DVR jobs after a Helmsman
// restart by matching local DVR directories with Mist's active push list.
func RecoverActiveDVRJobsFromMist(basePath string, logger logging.Logger) error {
	initDVRManager()
	return dvrManager.recoverActiveDVRJobsFromMist(basePath, logger)
}

// GetActiveDVRHashes returns DVR hashes that are currently recording.
func GetActiveDVRHashes() map[string]bool {
	if dvrManager == nil {
		return map[string]bool{}
	}

	dvrManager.mutex.RLock()
	defer dvrManager.mutex.RUnlock()

	hashes := make(map[string]bool, len(dvrManager.jobs))
	for hash := range dvrManager.jobs {
		hashes[hash] = true
	}
	return hashes
}

// IsActiveDVR reports whether the given DVR hash is currently recording on
// this node. Required guard at every DVR cleanup site: an active DVR must
// never have its directory or its rolling manifest deleted, and unsynced
// segments may only be evicted with an explicit DVRSegmentDropped report.
func IsActiveDVR(dvrHash string) bool {
	if dvrManager == nil || dvrHash == "" {
		return false
	}
	dvrManager.mutex.RLock()
	defer dvrManager.mutex.RUnlock()
	_, ok := dvrManager.jobs[dvrHash]
	return ok
}

// LookupActiveDVR returns the active DVRJob for a hash, if any. Returns
// (nil, false) when the DVR is not active on this node.
func LookupActiveDVR(dvrHash string) (*DVRJob, bool) {
	if dvrManager == nil || dvrHash == "" {
		return nil, false
	}
	dvrManager.mutex.RLock()
	defer dvrManager.mutex.RUnlock()
	job, ok := dvrManager.jobs[dvrHash]
	return job, ok
}

// LookupActiveDVRByInternalName resolves a rolling-DVR playback token (the
// internal_name after the "dvr+" prefix, which the edge serves as
// dvr+<internal_name>) to the recording's dvr_hash and rolling manifest path.
// The jobs map is keyed by dvr_hash, so this scans under the read lock and
// returns a value snapshot — no *DVRJob escapes the lock, so the caller can
// never race a concurrent mutation of the job's fields.
//
// It returns ok=false (unresolved) both when NO active recording matches and when
// MORE THAN ONE does. Fresh-per-session recording can leave an old and a new
// recording sharing an internal_name (possibly on the same node); there is no stable
// identity here to pick the right one, so it FAILS CLOSED rather than returning an
// arbitrary map-iteration match — the caller then installs a degraded DVR lease and
// destructive cleanup pauses for the internal_name (protecting both) instead of
// pinning one manifest and leaving the other's files exposed.
func LookupActiveDVRByInternalName(internalName string) (dvrHash, manifestPath string, ok bool) {
	if dvrManager == nil || internalName == "" {
		return "", "", false
	}
	dvrManager.mutex.RLock()
	defer dvrManager.mutex.RUnlock()
	var matchHash, matchManifest string
	matches := 0
	for _, job := range dvrManager.jobs {
		if job.InternalName == internalName {
			matches++
			matchHash, matchManifest = job.DVRHash, job.ManifestPath
		}
	}
	if matches != 1 {
		return "", "", false
	}
	return matchHash, matchManifest, true
}

// SegmentInRollingManifest reports whether a segment file is referenced by
// the active DVR's current rolling Mist manifest. Used as the third clause
// of the eviction predicate so we never delete a file that the live
// playlist still advertises.
func SegmentInRollingManifest(job *DVRJob, segmentName string) bool {
	if job == nil || job.ManifestPath == "" || segmentName == "" {
		return false
	}
	data, err := os.ReadFile(job.ManifestPath)
	if err != nil {
		return false
	}
	// Cheap substring check; the manifest writes "segments/<name>" entries.
	return strings.Contains(string(data), "/"+segmentName) || strings.Contains(string(data), segmentName+"\n")
}

// EvictUploadedSegments evicts segments from local disk for a DVR.
// Caller passes the candidate segment names. Only Foghorn-authoritative
// sources should produce candidates — chapter reclaim sweep
// (ReclaimDVRSegment control messages) or the disk-pressure fallback
// (RequestEvictableSegments). Each candidate is checked for active
// rolling-manifest membership before deletion; survivors emit a
// DVRSegmentDropped(was_uploaded=true) so Foghorn marks deleted_local.
//
// Returns the number of files actually deleted.
func (dm *DVRManager) EvictUploadedSegments(dvrHash string, candidates []string, reason string) (deleted int, freedBytes uint64) {
	if len(candidates) == 0 {
		return 0, 0
	}
	job, jobActive := LookupActiveDVR(dvrHash)
	// Resolve the DVR segments directory. While the DVR job is active
	// the canonical path is on job.OutputDir; after StopDVR the job is
	// removed but the segments directory remains on disk until reclaim
	// deletes it. Fall back to scanning storage/dvr/*/<dvr_hash>/
	// segments so post-stop reclaim still works.
	var (
		segmentsDir string
		logger      = dm.logger
	)
	if jobActive {
		segmentsDir = filepath.Join(job.OutputDir, "segments")
		logger = job.Logger
	} else {
		segmentsDir = resolveDVRSegmentsDirByHash(dvrHash)
		if segmentsDir == "" {
			// No active job AND no on-disk match — nothing to evict
			// locally. Still report the eviction so Foghorn can move
			// the ledger to deleted_local and run Phase B (S3 delete).
			for _, name := range candidates {
				if dropErr := SendDVRSegmentDropped(dvrHash, name, reason, "", 0, 0, 0, 0, true); dropErr != nil {
					logger.WithError(dropErr).WithField("segment", name).Debug("Failed to report missing segment as dropped (post-stop, no dir)")
				}
			}
			return 0, 0
		}
	}
	idx := localSegmentIndex
	for _, name := range candidates {
		// Rolling-manifest pin is only meaningful while the DVR is
		// recording. After stop the manifest is closed and every
		// segment is eligible.
		if jobActive && SegmentInRollingManifest(job, name) {
			continue
		}
		// Refuse to evict a segment currently pinned by an
		// active view (clip harvest, in-flight finalization).
		if idx != nil && !idx.EvictionEligible(dvrHash, name, 0) {
			// Skip — caller will retry after the active view or pin clears.
			continue
		}
		segPath := filepath.Join(segmentsDir, name)
		info, statErr := os.Stat(segPath)
		if statErr != nil {
			// Already gone — still report the eviction so Foghorn's view
			// matches reality.
			if dropErr := SendDVRSegmentDropped(dvrHash, name, reason, segPath, 0, 0, 0, 0, true); dropErr != nil {
				logger.WithError(dropErr).WithField("segment", name).Debug("Failed to report missing segment as dropped")
			}
			if idx != nil {
				idx.Forget(dvrHash, name)
			}
			continue
		}
		if err := os.Remove(segPath); err != nil {
			logger.WithError(err).WithField("segment", name).Warn("Failed to evict DVR segment")
			continue
		}
		if dropErr := SendDVRSegmentDropped(dvrHash, name, reason, segPath, 0, 0, 0, uint64(info.Size()), true); dropErr != nil {
			logger.WithError(dropErr).WithField("segment", name).Debug("Failed to report segment eviction")
		}
		if jobActive {
			job.syncMutex.Lock()
			delete(job.SyncedSegments, name)
			job.syncMutex.Unlock()
		}
		if idx != nil {
			idx.Forget(dvrHash, name)
		}
		deleted++
		freedBytes += uint64(info.Size())
	}
	return deleted, freedBytes
}

// resolveDVRSegmentsDirByHash scans storage/dvr/*/<dvr_hash>/segments for
// a matching directory. Returns "" when no DVR layout for the hash
// exists on disk. Used by post-stop reclaim where LookupActiveDVR
// misses; mirrors the resolveDVRDir helper used by chapter finalize.
func resolveDVRSegmentsDirByHash(dvrHash string) string {
	dir, _ := resolveDVRSegmentsDirByHashChecked(dvrHash)
	return dir
}

// resolveDVRSegmentsDirByHashChecked is resolveDVRSegmentsDirByHash with the scan status the
// absence-convergence path needs: scanOK is FALSE only when the storage root itself could not be
// scanned (a genuine read error → inconclusive). scanOK is TRUE when the scan succeeded — including
// when NO layout for the hash exists (dir==""), which is the STRONGEST evidence the recording is gone
// and MUST count toward absence convergence rather than being treated as an inconclusive read failure.
func resolveDVRSegmentsDirByHashChecked(dvrHash string) (segmentsDir string, scanOK bool) {
	dvrRoot := filepath.Join(sidecarcfg.GetStoragePath(), "dvr")
	entries, err := os.ReadDir(dvrRoot)
	if err != nil {
		if os.IsNotExist(err) {
			return "", true // no dvr root at all → nothing is recorded → genuinely absent
		}
		return "", false // genuine read error → inconclusive
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		candidate := filepath.Join(dvrRoot, e.Name(), dvrHash, "segments")
		if info, statErr := os.Stat(candidate); statErr == nil && info.IsDir() {
			return candidate, true
		}
	}
	return "", true // scanned the root, no layout for this hash → genuinely absent
}

// DropUnsyncedSegment force-evicts a single segment that has NOT been
// uploaded to S3. This is the data-loss path: the segment is reported as
// lost_local; internal chapter loss is marked failed_source_missing by the
// finalization queue. Use only when no other
// option remains. Reason should be one of disk_pressure / retention_expired
// / operator_cleanup.
// ErrDropRefusedByLease is returned by DropUnsyncedSegment when the lease
// guard refuses the drop for a non-emergency reason (currently disk_pressure).
// Retention-expired and operator-cleanup callers may force the drop after
// logging escalation; disk-pressure callers must skip and let the lease
// release before retrying.
var ErrDropRefusedByLease = fmt.Errorf("drop refused by active lease")

// DropLeaseChecker, if set, decides whether DropUnsyncedSegment may proceed
// for a given reason. Returning true means "lease held" and the drop will be
// refused for disk_pressure; for retention_expired / operator_cleanup the
// check is informational only (escalated to a warning log). Wired by the
// handlers package at startup so the control package stays free of a leases
// import.
var DropLeaseChecker func(dvrHash, segmentName string) bool

func (dm *DVRManager) DropUnsyncedSegment(dvrHash, segmentName, reason string) error {
	job, ok := LookupActiveDVR(dvrHash)
	if !ok {
		return fmt.Errorf("dvr %s not active", dvrHash)
	}
	if DropLeaseChecker != nil && DropLeaseChecker(dvrHash, segmentName) {
		switch reason {
		case "disk_pressure":
			job.Logger.WithFields(map[string]any{
				"dvr_hash":     dvrHash,
				"segment_name": segmentName,
				"reason":       reason,
			}).Warn("Refusing DropUnsyncedSegment under disk_pressure: lease held")
			return ErrDropRefusedByLease
		default:
			// retention_expired / operator_cleanup: data loss is the intent;
			// log loudly but proceed.
			job.Logger.WithFields(map[string]any{
				"dvr_hash":     dvrHash,
				"segment_name": segmentName,
				"reason":       reason,
			}).Warn("Forcing DropUnsyncedSegment despite active lease (non-disk-pressure caller)")
		}
	}
	segPath := filepath.Join(job.OutputDir, "segments", segmentName)
	var sizeBytes uint64
	if info, err := os.Stat(segPath); err == nil {
		sizeBytes = uint64(info.Size())
	}
	if err := os.Remove(segPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove unsynced segment: %w", err)
	}
	job.syncMutex.Lock()
	delete(job.SyncedSegments, segmentName)
	job.syncMutex.Unlock()
	return SendDVRSegmentDropped(dvrHash, segmentName, reason, segPath, 0, 0, 0, sizeBytes, false)
}

// HandleNewSegment handles a RECORDING_SEGMENT trigger for immediate sync.
// Mist's RecordingSegmentTrigger carries media-time bounds and duration; we
// pass them to Foghorn so the per-segment ledger row records canonical
// timing without re-deriving from filenames or wall-clock.
func (dm *DVRManager) HandleNewSegment(streamName, filePath string, mediaStartMs, mediaEndMs, durationMs int64) {
	dm.mutex.RLock()
	defer dm.mutex.RUnlock()

	// Find job matching the stream name
	var targetJob *DVRJob
	for _, job := range dm.jobs {
		if job.StreamName == streamName {
			targetJob = job
			break
		}
	}

	if targetJob == nil {
		// Not tracking this stream or not an active DVR
		return
	}

	// Verify file is within output directory to avoid path traversal
	if !strings.HasPrefix(filePath, targetJob.OutputDir) {
		dm.logger.WithFields(logging.Fields{
			"stream":     streamName,
			"file_path":  filePath,
			"output_dir": targetJob.OutputDir,
		}).Warn("Received RECORDING_SEGMENT for file outside DVR output directory")
		return
	}

	// Trigger sync for this specific segment
	go dm.syncSpecificSegment(targetJob, filePath, mediaStartMs, mediaEndMs, durationMs)
}

// syncSpecificSegment uploads a recorded TS segment to S3 as a
// recovery-source durability artifact. The per-segment S3 object is NOT
// playback infrastructure: active DVR playback reads the local rolling
// manifest on the recording origin, and other edges DTSC-pull from that
// origin. The S3 object exists so chapter finalization can recover from
// local segment loss (disk corruption, eviction edge case) and so the
// recording survives a recording-node loss until the chapter
// finalization queue produces the canonical .mkv. Once the chapter's
// playback artifact reaches state='frozen', the chapter_reclaim_sweep
// deletes the local TS file and the temporary S3 object.
//
// Records a 'pending' ledger row in Foghorn, uploads the segment to S3
// against the returned presigned URL, then reports the upload to mark
// the row 'uploaded'. Foghorn's ledger is the source of truth for
// eviction decisions; the in-memory SyncedSegments map only tracks
// which uploads this process has already initiated to avoid duplicate
// RecordDVRSegment calls.
func (dm *DVRManager) syncSpecificSegment(job *DVRJob, filePath string, mediaStartMs, mediaEndMs, durationMs int64) {
	if !IsConnected() {
		return
	}

	segName := filepath.Base(filePath)

	// Check if already synced
	job.syncMutex.Lock()
	if job.SyncedSegments[segName] {
		job.syncMutex.Unlock()
		return
	}
	job.syncMutex.Unlock()

	// Get segment size
	info, err := os.Stat(filePath)
	if err != nil {
		job.Logger.WithError(err).WithField("segment", segName).Warn("Segment file not found for sync")
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	resp, err := RecordDVRSegment(
		ctx,
		job.DVRHash, segName, filePath,
		mediaStartMs, mediaEndMs, durationMs,
	)
	if err != nil {
		job.Logger.WithError(err).WithField("segment", segName).Warn("Failed to record DVR segment with Foghorn")
		return
	}
	if !resp.GetAccepted() {
		reason := resp.GetReason()
		// Two rejection categories from Foghorn's RecordDVRSegment:
		//   1. dvr_terminal — artifact is in a terminal state. Hard-stop the
		//      Mist push: every subsequent segment hits the same rejection,
		//      so emit DVRSegmentDropped(was_uploaded=false) for the gap and
		//      then PushStop. This is the only terminal rejection.
		//   2. Everything else (s3_client_unavailable, presign_failed,
		//      insert_failed, dvr_artifact_not_found, missing metadata) —
		//      transient or caller-side. Skip THIS segment and let the
		//      next RECORDING_SEGMENT trigger try again. The reconciliation
		//      backstop (syncNewSegments) will eventually catch up if the
		//      transient condition resolves.
		if reason == "dvr_terminal" {
			job.Logger.WithFields(logging.Fields{
				"segment":  segName,
				"reason":   reason,
				"dvr_hash": job.DVRHash,
			}).Warn("DVR segment rejected as terminal; stopping local push")
			if dropErr := SendDVRSegmentDropped(job.DVRHash, segName, "artifact_terminal", filePath, mediaStartMs, mediaEndMs, durationMs, uint64(info.Size()), false); dropErr != nil {
				job.Logger.WithError(dropErr).WithField("segment", segName).Debug("Failed to report rejected segment as lost_local")
			}
			dm.stopJobAfterTerminalRejection(job)
			return
		}
		job.Logger.WithFields(logging.Fields{
			"segment":  segName,
			"reason":   reason,
			"dvr_hash": job.DVRHash,
		}).Warn("DVR segment record rejected; will retry on next trigger")
		return
	}
	if resp.GetPresignedPutUrl() == "" {
		job.Logger.WithField("segment", segName).Warn("No presigned URL returned for DVR segment")
		return
	}

	if err := dm.uploadSegmentToS3(ctx, filePath, resp.GetPresignedPutUrl()); err != nil {
		job.Logger.WithError(err).WithField("segment", segName).Warn("Failed to upload segment to S3")
		return
	}

	if err := SendMarkDVRSegmentUploaded(job.DVRHash, segName, uint64(info.Size())); err != nil {
		job.Logger.WithError(err).WithField("segment", segName).Warn("Failed to mark DVR segment uploaded with Foghorn")
		// Don't return — local cache below still records success; Foghorn
		// will eventually reconcile via the finalize-time retry path.
	}

	job.syncMutex.Lock()
	job.SyncedSegments[segName] = true
	job.syncMutex.Unlock()

	// Update the per-segment local index. Eviction consults this index
	// to keep segments held by an active view (clip harvest,
	// in-flight finalization) out of the deletion set.
	if idx := localSegmentIndex; idx != nil {
		idx.MarkUploaded(job.DVRHash, segName, filePath, info.Size())
	}

	job.Logger.WithFields(logging.Fields{
		"segment":  segName,
		"size_kb":  info.Size() / 1024,
		"dvr_hash": job.DVRHash,
		"sequence": resp.GetSequence(),
		"trigger":  "RECORDING_SEGMENT",
	}).Debug("DVR segment synced to S3 via trigger")

	// Source TS segments are pinned to local disk until every overlapping
	// chapter is frozen/reclaimed. Foghorn's chapter reclaim sweep owns
	// deletion via ReclaimDVRSegment; disk-pressure passes (see
	// monitorActiveDVRPressure) ask Foghorn for an authoritative evictable
	// list. Helmsman does NOT routinely evict on its own — that would
	// turn S3 recovery into the normal source for chapter finalization
	// instead of a recovery bridge.
}

// StartRecording starts a new DVR recording job. sourceRuntimeName is the
// Foghorn-authoritative Mist runtime name for the source stream (live+<x>
// / pull+<x> / bare native). Helmsman uses it verbatim as the Mist
// push_start.stream argument and never re-derives.
func (dm *DVRManager) StartRecording(dvrHash, streamID, internalName, sourceRuntimeName, sourceURL string, config *ipcpb.DVRConfig, sendFunc func(*ipcpb.ControlMessage)) error {
	dm.mutex.Lock()
	defer dm.mutex.Unlock()

	// A DVR recording is keyed by dvr+<internal_name>; an empty identity cannot be
	// persisted, recovered, or resolved, so reject it before creating anything.
	internalName = strings.TrimSpace(internalName)
	if internalName == "" {
		return fmt.Errorf("cannot start DVR recording %s: empty source internal_name", dvrHash)
	}

	// A repeat start for the same hash is an IDEMPOTENT ack, not an error: Foghorn's stale-'starting'
	// recovery re-dispatches SendDVRStart when a prior attempt's ack was lost, and re-issuing must
	// CONVERGE rather than emit a spurious failed DVRStopped. If a recording for this hash is already
	// active/starting for the SAME stream, report success and let the existing job's progress keep
	// driving the recording transition. A start for a DIFFERENT stream on the same hash is a genuine
	// conflict and stays an error.
	if existing, exists := dm.jobs[dvrHash]; exists {
		if existing.InternalName == internalName {
			// Reconcile the re-dispatched descriptor against the live job. Recovery
			// already restores the descriptor from job.json, so this is normally a
			// coherent no-op; it still folds in any newer source identity the
			// re-dispatch carries (and fails closed on a runtime mismatch).
			dm.reconcileDVRJobDescriptorLocked(existing, sourceRuntimeName, sourceURL, config)
			dm.logger.WithField("dvr_hash", dvrHash).Info("DVR start repeated for already-active recording; treating as idempotent ack")
			return nil
		}
		return fmt.Errorf("DVR recording already active for hash %s on a different stream (%s != %s)", dvrHash, existing.InternalName, internalName)
	}

	if err := os.MkdirAll(dm.storagePath, 0755); err != nil {
		return err
	}
	if err := dm.hasSpaceFor(dm.storagePath, 0); err != nil {
		return fmt.Errorf("insufficient disk space for DVR recording: %w", err)
	}

	outputDir := filepath.Join(dm.storagePath, "dvr", streamID, dvrHash)
	manifestPath := filepath.Join(outputDir, fmt.Sprintf("%s.m3u8", dvrHash))

	// Classify a pre-existing on-disk directory BEFORE mutating anything.
	// Recovery runs async on reconnect, so a re-dispatched start can reach here
	// before recovery registered the on-disk job; we must never overwrite the
	// identity, start a duplicate push over a live one, or delete data we did
	// not create.
	reuseExistingDir := false
	if info, statErr := os.Stat(outputDir); statErr == nil {
		if !info.IsDir() {
			return fmt.Errorf("DVR output path %s exists but is not a directory", outputDir)
		}
		persisted, ok := readDVRJobInternalName(outputDir)
		if !ok {
			return fmt.Errorf("existing DVR directory for %s has missing or unreadable identity; refusing to overwrite", dvrHash)
		}
		if persisted != internalName {
			return fmt.Errorf("existing DVR recording %s belongs to a different stream (%s); refusing to overwrite", dvrHash, persisted)
		}
		// Exact identity match: adopt an already-running recording idempotently
		// rather than starting a duplicate push.
		adopted, aerr := dm.adoptExistingDVRJobLocked(dvrHash, internalName, outputDir, manifestPath, sourceRuntimeName, sourceURL, config)
		if aerr != nil {
			return fmt.Errorf("adopt existing DVR recording %s: %w", dvrHash, aerr)
		}
		if adopted {
			return nil
		}
		// Identity matches but no live push. If the directory already holds
		// recorded media, the recording completed (or was interrupted) while we
		// were disconnected — Mist finished it and dropped the push. Restarting a
		// push with append=1&noendlist=1 over those segments would open a SECOND
		// writer against a stopped source and corrupt the rolling manifest (the
		// exact hazard monitorJob treats as terminal at "push gone + segments>0").
		// Fail closed: the media stays on disk for recovery/backfill; only an
		// EMPTY interrupted start (no media yet) may be reused and restarted.
		if dvrDirHasMedia(outputDir, manifestPath) {
			return fmt.Errorf("existing DVR recording %s has media but no live push; refusing to restart a completed/interrupted recording", dvrHash)
		}
		// Empty interrupted start: reuse the verified directory and restart,
		// WITHOUT rewriting the already-correct sidecar (never replace an identity).
		reuseExistingDir = true
	} else if !os.IsNotExist(statErr) {
		return fmt.Errorf("stat DVR output dir %s: %w", outputDir, statErr)
	}

	// createdDirs holds exactly the directories THIS call created (shallow→deep),
	// fsynced level-by-level so every new entry is crash-durable — not only the
	// leaf's immediate parent.
	var createdDirs []string
	if !reuseExistingDir {
		dirs, err := mkdirAllDurable(outputDir)
		if err != nil {
			removeCreatedDirs(dirs, func(path string, rbErr error) {
				dm.logger.WithError(rbErr).WithField("path", path).Warn("Left partially created DVR directory in place (non-empty)")
			})
			return fmt.Errorf("failed to create DVR output directory: %w", err)
		}
		createdDirs = dirs
	}
	// removeIfCreated rolls back ONLY what this attempt created: its own metadata
	// sidecar, then the created directories deepest-first with a NON-recursive
	// Remove. A directory that now holds foreign content (a live push's segments,
	// a sibling recording) is preserved, never wiped.
	removeIfCreated := func() {
		if len(createdDirs) == 0 {
			return
		}
		_ = os.Remove(filepath.Join(outputDir, dvrJobMetaFile))
		removeCreatedDirs(createdDirs, func(path string, rmErr error) {
			dm.logger.WithError(rmErr).WithField("path", path).Warn("Preserved non-empty DVR directory created by this start attempt")
		})
	}

	// Durably record the source internal_name into a dir WE created, before the
	// push (stream_id is NOT the internal_name). Mandatory: a recording whose
	// identity is not persisted must not start. The reuse path skips this — the
	// existing sidecar already matches and must not be replaced.
	if !reuseExistingDir {
		meta := dvrJobMeta{
			InternalName:      internalName,
			SourceRuntimeName: strings.TrimSpace(sourceRuntimeName),
			SourceURL:         strings.TrimSpace(sourceURL),
			SegmentDuration:   config.GetSegmentDuration(),
			DvrWindowSeconds:  config.GetDvrWindowSeconds(),
			MaxEntries:        config.GetMaxEntries(),
		}
		if err := dm.persistJobMeta(outputDir, meta); err != nil {
			removeIfCreated()
			return fmt.Errorf("persist DVR job metadata: %w", err)
		}
	}

	// Create DVR job
	job := &DVRJob{
		DVRHash:           dvrHash,
		InternalName:      internalName,
		SourceRuntimeName: sourceRuntimeName,
		SourceURL:         sourceURL,
		Config:            config,
		StartTime:         time.Now(),
		OutputDir:         outputDir,
		ManifestPath:      manifestPath,
		SendFunc:          sendFunc,
		Logger:            dm.logger,
		Status:            "starting",
		MaxRetries:        MaxDVRRetries,
		RetryCount:        0,
		SyncedSegments:    make(map[string]bool), // Initialize sync tracking
	}

	// Start the recording process via MistServer push. startDVRPush's state
	// machine returns an error ONLY for dvrPushNotStarted (no push was ever
	// created) — the only case where rolling the directory back is safe. An
	// accepted-but-unconfirmed push returns nil (job registered as "starting",
	// metadata kept) so we never delete a directory a live push may be writing.
	if err := dm.startDVRPush(job); err != nil {
		removeIfCreated()
		return fmt.Errorf("failed to start DVR push: %w", err)
	}

	// Store job
	dm.jobs[dvrHash] = job

	// Start progress monitoring
	go dm.monitorJob(job)

	job.Logger.Info("DVR recording job started")
	return nil
}

// dvrJobMetaFile is a small metadata sidecar written into a DVR job's
// OutputDir at recording start. It durably records the Mist internal_name of
// the source stream, which is DISTINCT from the stream_id path component of
// /storage/dvr/{stream_id}/{dvr_hash}/. Restart recovery reconstructs a job
// from the on-disk layout, where only stream_id is recoverable from the path;
// without this sidecar the recovered job cannot be resolved by internal_name
// (LookupActiveDVRByInternalName), so the rolling-DVR playback token
// dvr+<internal_name> would miss and install a degraded cleanup pause.
const dvrJobMetaFile = "job.json"

// dvrJobMetaVersion is the current descriptor format. readDVRJobMeta rejects a
// sidecar whose version differs, so a job.json that lacks the source identity
// resolves as unresolved (degraded protection) rather than as a recording
// without the source override the design depends on.
const dvrJobMetaVersion = 1

// dvrJobMeta is the durable start descriptor. Beyond internal_name it records
// the Foghorn-authoritative source identity (runtime name + remote source URL)
// and the config fields needed to rebuild the push target, so restart recovery
// restores a coherent {runtime, URL, config} tuple and re-pins the remote source
// override — Foghorn only re-dispatches requested/starting rows, never an
// established `recording`, so recovery, not re-dispatch, must carry this. A valid
// descriptor REQUIRES version + internal_name + source_runtime_name; source_url
// is legitimately empty for a local (non-remote) source, and the config fields
// fall back to defaults.
type dvrJobMeta struct {
	Version           int    `json:"version"`
	InternalName      string `json:"internal_name"`
	SourceRuntimeName string `json:"source_runtime_name"`
	SourceURL         string `json:"source_url,omitempty"`
	SegmentDuration   int32  `json:"segment_duration,omitempty"`
	DvrWindowSeconds  int32  `json:"dvr_window_seconds,omitempty"`
	MaxEntries        int32  `json:"max_entries,omitempty"`
}

// dvrConfig reconstructs the DVRConfig from the persisted descriptor.
func (m dvrJobMeta) dvrConfig() *ipcpb.DVRConfig {
	if m.SegmentDuration == 0 && m.DvrWindowSeconds == 0 && m.MaxEntries == 0 {
		return nil
	}
	return &ipcpb.DVRConfig{
		SegmentDuration:  m.SegmentDuration,
		DvrWindowSeconds: m.DvrWindowSeconds,
		MaxEntries:       m.MaxEntries,
	}
}

// writeDVRJobMeta atomically persists the start descriptor alongside the
// recording so restart recovery restores it exactly. It writes a temp file,
// fsyncs, and renames into place, and returns any error — the caller must not
// start a recording whose identity was not durably recorded, because a restart
// would otherwise resolve it under a substituted identity or a lost source.
func writeDVRJobMeta(outputDir string, meta dvrJobMeta) error {
	// Refuse an empty output dir — otherwise the temp file + rename would land in
	// the process working directory rather than the DVR job directory.
	if strings.TrimSpace(outputDir) == "" {
		return fmt.Errorf("refusing to persist DVR job metadata with an empty output dir")
	}
	meta.Version = dvrJobMetaVersion // stamp the current format; callers need not set it
	data, err := json.Marshal(meta)
	if err != nil {
		return fmt.Errorf("marshal DVR job metadata: %w", err)
	}
	tmp, err := os.CreateTemp(outputDir, dvrJobMetaFile+".tmp-*")
	if err != nil {
		return fmt.Errorf("create DVR job metadata temp file: %w", err)
	}
	tmpName := tmp.Name()
	if _, err = tmp.Write(data); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return fmt.Errorf("write DVR job metadata: %w", err)
	}
	if err = tmp.Sync(); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return fmt.Errorf("sync DVR job metadata: %w", err)
	}
	if err = tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf("close DVR job metadata: %w", err)
	}
	if err = os.Rename(tmpName, filepath.Join(outputDir, dvrJobMetaFile)); err != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf("rename DVR job metadata into place: %w", err)
	}
	// fsync the containing directory so the rename is durable across a crash,
	// not just the file contents.
	if err := syncDir(outputDir); err != nil {
		return fmt.Errorf("sync DVR job dir: %w", err)
	}
	return nil
}

// syncDir fsyncs a directory so a create/rename within it is durable across a
// crash, not just the affected file's contents.
func syncDir(path string) error {
	d, err := os.Open(path)
	if err != nil {
		return err
	}
	if err := d.Sync(); err != nil {
		_ = d.Close()
		return err
	}
	return d.Close()
}

// mkdirAllDurable creates dir and every missing ancestor, fsyncing each new
// directory's parent so the new entries survive a crash — MkdirAll + a single
// parent sync leaves intermediate ancestors (e.g. a freshly created dvr/) not
// durable. It returns exactly the directories THIS call created (shallowest
// first), so a caller can roll back precisely its own contribution. A level that
// already existed (Mkdir → EEXIST) is NOT reported as created, so rollback never
// touches a pre-existing directory. On error it returns the levels created so
// far, so the caller can still roll them back.
func mkdirAllDurable(dir string) (created []string, err error) {
	// Collect missing levels from the target up to the deepest existing ancestor.
	var missing []string
	for p := filepath.Clean(dir); ; {
		info, statErr := os.Stat(p)
		if statErr == nil {
			if !info.IsDir() {
				return nil, fmt.Errorf("%s exists but is not a directory", p)
			}
			break
		}
		if !os.IsNotExist(statErr) {
			return nil, statErr
		}
		missing = append(missing, p)
		parent := filepath.Dir(p)
		if parent == p {
			break // reached the filesystem root without an existing ancestor
		}
		p = parent
	}
	// Create shallowest→deepest, fsyncing each parent after the entry appears.
	for i := len(missing) - 1; i >= 0; i-- {
		p := missing[i]
		if mkErr := os.Mkdir(p, 0755); mkErr != nil {
			if os.IsExist(mkErr) {
				continue // appeared concurrently — not ours to roll back
			}
			return created, mkErr
		}
		created = append(created, p)
		if syncErr := syncDir(filepath.Dir(p)); syncErr != nil {
			return created, syncErr
		}
	}
	return created, nil
}

// removeCreatedDirs removes ONLY the directories this start attempt created,
// deepest-first, with a non-recursive Remove so a directory that unexpectedly
// holds foreign content (a live push's segments, a sibling recording) is
// preserved rather than wiped. It stops at the first non-empty directory (every
// shallower one is then non-empty too). Once ALL created dirs are removed it
// fsyncs the parent of the shallowest (the stable pre-existing ancestor) once so
// the removal is crash-durable. logf reports a preserved (or unsyncable) dir.
func removeCreatedDirs(created []string, logf func(path string, err error)) {
	for i := len(created) - 1; i >= 0; i-- {
		p := created[i]
		if rmErr := os.Remove(p); rmErr != nil {
			if logf != nil {
				logf(p, rmErr)
			}
			return
		}
	}
	// All created dirs removed: fsync the parent of the shallowest (the stable
	// pre-existing ancestor) so the removal is durable across a crash.
	if len(created) > 0 {
		if syncErr := syncDir(filepath.Dir(created[0])); syncErr != nil && logf != nil {
			logf(filepath.Dir(created[0]), syncErr)
		}
	}
}

// readDVRJobInternalName reads the persisted internal_name for a recovered DVR
// job. Returns ok=false when the sidecar is absent, unreadable, corrupt, or
// empty; the caller then leaves InternalName empty so the job stays unresolved
// (degraded cleanup protection) rather than adopting a substituted identity.
func readDVRJobInternalName(outputDir string) (string, bool) {
	meta, ok := readDVRJobMeta(outputDir)
	if !ok {
		return "", false
	}
	return meta.InternalName, true
}

// readDVRJobMeta reads the full start descriptor. ok=false when the sidecar is
// absent, unreadable, corrupt, or has an empty internal_name (the identity
// invariant); the caller then leaves the job unresolved rather than adopting a
// substituted identity.
func readDVRJobMeta(outputDir string) (dvrJobMeta, bool) {
	data, err := os.ReadFile(filepath.Join(outputDir, dvrJobMetaFile))
	if err != nil {
		return dvrJobMeta{}, false
	}
	var meta dvrJobMeta
	if err := json.Unmarshal(data, &meta); err != nil {
		return dvrJobMeta{}, false
	}
	// Require the current format and both source-identity fields. Incomplete metadata is rejected:
	// the job stays unresolved
	// (degraded protection) rather than resolving without its source identity.
	if meta.Version != dvrJobMetaVersion ||
		strings.TrimSpace(meta.InternalName) == "" ||
		strings.TrimSpace(meta.SourceRuntimeName) == "" {
		return dvrJobMeta{}, false
	}
	return meta, true
}

type localDVRDirectory struct {
	streamID     string
	dvrHash      string
	outputDir    string
	manifestPath string
}

// adoptExistingDVRJobLocked adopts an on-disk recording whose identity already
// matches the requested start but which in-memory state does not yet know about
// (async recovery has not registered it). If a live Mist push for the hash
// exists, it registers the recovered job and returns adopted=true (idempotent
// ack — no duplicate push, no metadata rewrite). If no live push exists it
// returns adopted=false so the caller can restart into the verified directory.
// The re-dispatched start descriptor (sourceRuntimeName/sourceURL/config) is
// reconciled onto the adopted job so a later push recreation keeps the remote
// DVR source override. Caller holds dm.mutex.
func (dm *DVRManager) adoptExistingDVRJobLocked(dvrHash, internalName, outputDir, manifestPath, sourceRuntimeName, sourceURL string, config *ipcpb.DVRConfig) (bool, error) {
	pushes, err := dm.mistClient.PushList()
	if err != nil {
		return false, fmt.Errorf("list Mist pushes: %w", err)
	}
	// Adopt only a push whose EXACT identity (runtime + hash) matches this start —
	// never a foreign push that merely shares the hash string, which would map our
	// recording to someone else's runtime.
	push, ok := findExactDVRPush(pushes, strings.TrimSpace(sourceRuntimeName), "", dvrHash)
	if !ok {
		return false, nil
	}
	if existing, exists := dm.jobs[dvrHash]; exists {
		dm.reconcileDVRJobDescriptorLocked(existing, sourceRuntimeName, sourceURL, config)
		return true, nil
	}
	duration := dvrManifestDuration(manifestPath)
	startTime := time.Now()
	if duration > 0 {
		startTime = startTime.Add(-duration)
	}
	job := &DVRJob{
		DVRHash:      dvrHash,
		InternalName: internalName,
		StartTime:    startTime,
		PushID:       push.ID,
		OutputDir:    outputDir,
		ManifestPath: manifestPath,
		SendFunc: func(msg *ipcpb.ControlMessage) {
			if serr := sendControlMessage(msg); serr != nil {
				dm.logger.WithError(serr).WithField("dvr_hash", dvrHash).Warn("Adopted DVR report was not delivered immediately")
			}
		},
		Logger:         dm.logger,
		SegmentCount:   dvrManifestSegmentCount(manifestPath),
		TotalSizeBytes: dvrDirectorySize(outputDir),
		Status:         "recording",
		TargetURI:      push.TargetURI,
		StreamName:     push.StreamName,
		MaxRetries:     MaxDVRRetries,
		SyncedSegments: make(map[string]bool),
	}
	// Fold the authoritative start descriptor into the Mist/disk-derived job and
	// re-register the source override BEFORE it is discoverable/monitored.
	dm.reconcileDVRJobDescriptorLocked(job, sourceRuntimeName, sourceURL, config)
	dm.jobs[dvrHash] = job
	go dm.monitorJob(job)
	dm.logger.WithFields(logging.Fields{
		"dvr_hash":      dvrHash,
		"internal_name": internalName,
		"push_id":       push.ID,
	}).Info("Adopted existing active DVR recording (idempotent start)")
	return true, nil
}

// reconcileDVRJobDescriptorLocked folds the durable start descriptor into a job
// restored from Mist+disk alone and re-registers the DVR source override so a
// later push recreation keeps the remote source. The {runtime, URL} pair must
// stay COHERENT: the descriptor's source_url belongs to the descriptor's
// source_runtime_name, so if the live push runs under a DIFFERENT runtime (the
// descriptor is stale relative to the push actually on disk) it is NOT valid to
// map the live runtime to the descriptor's URL — that would point the override
// at the wrong source. On such a mismatch we fail closed: keep the live push
// untouched and register no override. config is source-identity-independent and
// is always restored when missing. Caller holds dm.mutex.
func (dm *DVRManager) reconcileDVRJobDescriptorLocked(job *DVRJob, sourceRuntimeName, sourceURL string, config *ipcpb.DVRConfig) {
	if job.Config == nil {
		job.Config = config
	}
	sourceRuntimeName = strings.TrimSpace(sourceRuntimeName)
	sourceURL = strings.TrimSpace(sourceURL)
	if sourceRuntimeName == "" {
		return // no authoritative source identity to reconcile
	}
	if job.StreamName != "" && job.StreamName != sourceRuntimeName {
		// Incompatible identities — do NOT mix. Leave the live push as-is and
		// register nothing; a mismatched override would map the running runtime
		// to a stale source URL.
		dm.logger.WithFields(logging.Fields{
			"dvr_hash":        job.DVRHash,
			"push_stream":     job.StreamName,
			"descriptor_name": sourceRuntimeName,
		}).Warn("DVR start descriptor runtime differs from the live push; NOT registering a mismatched source override (fail closed)")
		return
	}
	// Coherent tuple: the descriptor's runtime matches the live push (or the job
	// has no push name yet). Restore the identity and pin the override.
	if job.SourceRuntimeName == "" {
		job.SourceRuntimeName = sourceRuntimeName
	}
	if job.SourceURL == "" {
		job.SourceURL = sourceURL
	}
	overrideName := strings.TrimSpace(job.StreamName)
	if overrideName == "" {
		overrideName = strings.TrimSpace(job.SourceRuntimeName)
	}
	if overrideName != "" && strings.TrimSpace(job.SourceURL) != "" {
		RegisterDVRSourceOverride(overrideName, job.SourceURL)
	}
}

func (dm *DVRManager) recoverActiveDVRJobsFromMist(basePath string, logger logging.Logger) error {
	pushes, err := dm.mistClient.PushList()
	if err != nil {
		return fmt.Errorf("list Mist pushes: %w", err)
	}
	dirs, err := scanLocalDVRDirectories(basePath)
	if err != nil {
		return err
	}
	for _, dir := range dirs {
		dm.recoverOneDVRDir(dir, pushes, logger)
	}
	return nil
}

// recoverOneDVRDir matches a single on-disk recording to its live push and
// reinstates the job (identity, push binding, and source override) under
// dm.mutex, so recovery is one serialized transition.
func (dm *DVRManager) recoverOneDVRDir(dir localDVRDirectory, pushes []mist.PushInfo, logger logging.Logger) {
	// A valid descriptor is REQUIRED: it names the exact runtime the push must run
	// under. Without it the on-disk job cannot be safely matched to a push, so skip
	// (the poller's degraded protection still pauses cleanup).
	meta, ok := readDVRJobMeta(dir.outputDir)
	if !ok {
		return
	}
	// Match ONLY a push under the descriptor's exact runtime — never a foreign push
	// that merely contains the DVR hash string under a different runtime.
	push, found := findExactDVRPush(pushes, meta.SourceRuntimeName, "", dir.dvrHash)
	if !found {
		return
	}

	dm.mutex.Lock()
	defer dm.mutex.Unlock()
	if _, active := dm.jobs[dir.dvrHash]; active {
		return
	}

	duration := dvrManifestDuration(dir.manifestPath)
	startTime := time.Now()
	if duration > 0 {
		startTime = startTime.Add(-duration)
	}
	job := &DVRJob{
		DVRHash:           dir.dvrHash,
		InternalName:      meta.InternalName,
		SourceRuntimeName: meta.SourceRuntimeName,
		SourceURL:         meta.SourceURL,
		StreamName:        push.StreamName,
		Config:            meta.dvrConfig(),
		StartTime:         startTime,
		PushID:            push.ID,
		OutputDir:         dir.outputDir,
		ManifestPath:      dir.manifestPath,
		TargetURI:         push.TargetURI,
		SendFunc: func(msg *ipcpb.ControlMessage) {
			if err := sendControlMessage(msg); err != nil {
				logger.WithError(err).WithField("dvr_hash", dir.dvrHash).Warn("Recovered DVR report was not delivered immediately")
			}
		},
		Logger:         dm.logger,
		SegmentCount:   dvrManifestSegmentCount(dir.manifestPath),
		TotalSizeBytes: dvrDirectorySize(dir.outputDir),
		Status:         "recording",
		MaxRetries:     MaxDVRRetries,
		SyncedSegments: make(map[string]bool),
	}
	// Re-pin the source override so a later push recreation keeps the remote source.
	if meta.SourceURL != "" {
		RegisterDVRSourceOverride(meta.SourceRuntimeName, meta.SourceURL)
	}
	dm.jobs[dir.dvrHash] = job
	go dm.monitorJob(job)
	logger.WithFields(logging.Fields{
		"dvr_hash":      dir.dvrHash,
		"stream_id":     dir.streamID,
		"push_id":       push.ID,
		"stream_name":   push.StreamName,
		"manifest_path": dir.manifestPath,
	}).Warn("Recovered active DVR job from Mist after restart")
}

func scanLocalDVRDirectories(basePath string) ([]localDVRDirectory, error) {
	dvrRoot := filepath.Join(basePath, "dvr")
	streamDirs, err := os.ReadDir(dvrRoot)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var dirs []localDVRDirectory
	for _, streamDir := range streamDirs {
		if !streamDir.IsDir() {
			continue
		}
		streamID := streamDir.Name()
		artifactDirs, err := os.ReadDir(filepath.Join(dvrRoot, streamID))
		if err != nil {
			continue
		}
		for _, artifactDir := range artifactDirs {
			if !artifactDir.IsDir() {
				continue
			}
			dvrHash := artifactDir.Name()
			outputDir := filepath.Join(dvrRoot, streamID, dvrHash)
			segmentsDir := filepath.Join(outputDir, "segments")
			if info, statErr := os.Stat(segmentsDir); statErr != nil || !info.IsDir() {
				continue
			}
			manifestPath := filepath.Join(outputDir, dvrHash+".m3u8")
			// The identity comes from the descriptor (readDVRJobMeta) at recovery
			// time, not from the path — recovery never substitutes the stream_id
			// path component for the internal_name.
			dirs = append(dirs, localDVRDirectory{
				streamID:     streamID,
				dvrHash:      dvrHash,
				outputDir:    outputDir,
				manifestPath: manifestPath,
			})
		}
	}
	return dirs, nil
}

// StopRecording FORCE-tears-down a DVR recording job (used by the startup-deadline
// path, which has already established via its own PushList that no live push
// exists). It terminalizes even if the push id is unknown.
func (dm *DVRManager) StopRecording(dvrHash string) error {
	return dm.stopRecording(dvrHash, func(msg *ipcpb.ControlMessage) {
		if err := sendControlMessage(msg); err != nil && !durableControlWasPersisted(err) && dm.logger != nil {
			dm.logger.WithError(err).WithField("dvr_hash", dvrHash).Warn("Failed to durably accept forced DVR completion")
		}
	}, true)
}

// ConfirmDVRPushStopped stops any live Mist push for dvrHash and reports whether a
// DESTRUCTIVE teardown (delete) may proceed. It emits no lifecycle event — the caller
// sends its own terminal report (e.g. 'deleted'). It returns false — DEFERRING the delete,
// files retained — when a writer may still be live: the push list could not be read, a
// present push would not stop, OR the push is merely ABSENT from a successful list and
// bounded-absence has not yet converged (an accepted push can be momentarily unlisted, so
// an empty list is NOT proof of absence). It returns true only when the push was found and
// stopped, or its absence converged via observeAbsenceConverged (repeated absence over a
// real grace with no segment/byte progress). The delete is re-driven by Foghorn's durable
// stop_pending obligation until convergence.
func (dm *DVRManager) ConfirmDVRPushStopped(dvrHash string) bool {
	dm.mutex.Lock()
	job, exists := dm.jobs[dvrHash]
	var streamName, targetURI string
	var pushID int
	if exists {
		job.Status = "finalizing"
		job.pushGeneration++
		pushID = job.PushID
		streamName, targetURI = job.StreamName, job.TargetURI
	}
	dm.mutex.Unlock()

	if dm.mistClient == nil {
		// Fail CLOSED: with no Mist client we cannot confirm the push is stopped, so we must
		// NOT authorize a destructive delete over a possibly-live writer. Defer. (In production
		// the client is always wired; tests inject a fake instead of relying on this path.)
		dm.logger.WithField("dvr_hash", dvrHash).Warn("DVR delete: no Mist client to confirm push stopped; deferring (fail closed)")
		return false
	}

	confirmed := false
	if pushID > 0 {
		if err := dm.mistClient.PushStop(pushID); err != nil {
			dm.logger.WithError(err).WithField("dvr_hash", dvrHash).Warn("DVR delete: failed to stop live push; deferring delete")
			return false
		}
		confirmed = true
	} else {
		pushes, listErr := dm.mistClient.PushList()
		if listErr != nil {
			dm.logger.WithError(listErr).WithField("dvr_hash", dvrHash).Warn("DVR delete: cannot list pushes to confirm; deferring delete")
			return false
		}
		// Match by exact identity when we know the stream name, else by the hash in
		// the target (the in-memory job may be gone after a restart).
		var push mist.PushInfo
		var ok bool
		if exists {
			push, ok = findExactDVRPush(pushes, streamName, targetURI, dvrHash)
		} else {
			push, ok = findDVRPushByHash(pushes, dvrHash)
		}
		if ok {
			// A live push is present: reset absence evidence and stop it.
			dm.clearAbsenceEvidence(dvrHash)
			if err := dm.mistClient.PushStop(push.ID); err != nil {
				dm.logger.WithError(err).WithFields(logging.Fields{"dvr_hash": dvrHash, "push_id": push.ID}).Warn("DVR delete: failed to stop live push; deferring delete")
				return false
			}
			confirmed = true
		} else {
			// Absent from a successful list is NOT proof of absence — an accepted push can be
			// momentarily unlisted. Require bounded-absence convergence (repeated absence over a
			// real grace + no segment/byte progress) before a DESTRUCTIVE delete. Until then
			// DEFER: return false so the caller retains the files (delete_pending) rather than
			// removing them while a possibly-live-but-unlisted writer might still exist.
			fp, readOK := dvrFingerprintByHash(dvrHash)
			if !dm.observeAbsenceConverged(dvrHash, fp, readOK) {
				dm.logger.WithField("dvr_hash", dvrHash).Info("DVR delete: push absent from list but absence not yet converged; deferring delete (files retained)")
				return false
			}
			dm.clearAbsenceEvidence(dvrHash)
			confirmed = true
		}
	}

	if confirmed && exists {
		dm.mutex.Lock()
		if dm.jobs[dvrHash] == job {
			delete(dm.jobs, dvrHash)
			ClearDVRSourceOverride(job.StreamName)
		}
		dm.mutex.Unlock()
	}
	return confirmed
}

// StopRecordingWithSender stops a DVR recording job on a Foghorn stop command and
// sends the terminal notification through sendFunc. It is CONSERVATIVE: it never
// reports completion while the writer may still be live (an unconfirmed stop leaves
// the job and the durable stop obligation open for retry). If Helmsman restarted
// after Mist finished writing the recording, the in-memory job is gone but the
// on-disk DVR layout is still authoritative enough to emit completion.
func (dm *DVRManager) StopRecordingWithSender(dvrHash string, sendFunc func(*ipcpb.ControlMessage)) error {
	return dm.stopRecording(dvrHash, sendFunc, false)
}

// stopRecording is the shared body. authoritative=true means the caller has already
// established there is nothing live to orphan (or is a destructive teardown), so an
// unconfirmed stop still terminalizes; authoritative=false keeps the job open on an
// unconfirmed stop so a live writer is never falsely reported complete.
func (dm *DVRManager) stopRecording(dvrHash string, sendFunc func(*ipcpb.ControlMessage), authoritative bool) error {
	dm.mutex.Lock()
	job, exists := dm.jobs[dvrHash]
	if !exists {
		dm.mutex.Unlock()
		return dm.stopRecoveredRecording(dvrHash, sendFunc)
	}
	if sendFunc != nil {
		job.SendFunc = sendFunc
	}
	// Mark the job finalizing and bump the generation under the lock before
	// releasing it for the Mist call: maintainPushStatus skips finalizing jobs,
	// and the generation bump makes any recreate already mid-flight commit
	// stale (its new push gets stopped) so it can't resurrect the push we are
	// about to stop. Archive playback is per-chapter VOD artifacts produced by
	// the chapter-finalization pipeline; the rolling Mist playlist stays
	// local-only and is never uploaded to S3.
	job.Status = "finalizing"
	job.pushGeneration++
	stopPushID := job.PushID
	dm.mutex.Unlock()

	// Stop the MistServer push (no lock held across the Mist call) and track whether
	// the stop is CONFIRMED. We must never report completion or delete the job while
	// a writer may still be live.
	stopConfirmed := true
	if stopPushID > 0 {
		if err := dm.mistClient.PushStop(stopPushID); err != nil {
			job.Logger.WithError(err).Warn("Failed to stop MistServer push; leaving stop obligation open")
			stopConfirmed = false
		}
	} else {
		// PushID 0 means an accepted-but-unconfirmed start/recreate whose id we never
		// resolved. A push MAY be live under our identity. We can ONLY confirm a stop
		// by finding it in the list and stopping it: absence from a successful list is
		// NOT authoritative here (the same reason the monitor never treats an empty
		// list as "not started" for a PushID-0 job) — the accepted push may be live
		// but unlisted. So an empty or failed list leaves the stop unconfirmed and the
		// obligation open; Foghorn's stop_pending obligation re-sends DVRStop, which
		// re-drives this until the push becomes listable and is adopted then stopped.
		stopConfirmed = false
		if pushes, listErr := dm.mistClient.PushList(); listErr != nil {
			job.Logger.WithError(listErr).Warn("Failed to list pushes to confirm DVR stop; leaving stop obligation open")
		} else if push, ok := findExactDVRPush(pushes, job.StreamName, job.TargetURI, job.DVRHash); ok {
			if stopErr := dm.mistClient.PushStop(push.ID); stopErr != nil {
				job.Logger.WithError(stopErr).WithField("push_id", push.ID).Warn("Failed to stop unconfirmed DVR push by identity; leaving stop obligation open")
			} else {
				stopConfirmed = true // found and stopped the live push — confirmed
			}
		} else {
			job.Logger.Warn("Unconfirmed DVR push not present in a successful list; cannot confirm stop (absence is not authoritative), leaving obligation open")
		}
	}

	if !stopConfirmed && !authoritative {
		// A Foghorn stop we could not confirm: emit NO DVRStopped — neither success
		// nor failed. A 'failed' report would terminalize the artifact on Foghorn and
		// clear its stop obligation while the writer may still be live (handleDVRStop
		// turns our returned error into a failed report). Keep the job at 'finalizing'
		// so maintainPushStatus does not recreate the push, and return nil so no
		// terminal report is sent. Foghorn's durable stop_pending obligation re-sends
		// DVRStop, re-driving this stop until PushStop confirms. A force teardown
		// (delete / startup deadline) sets authoritative and terminalizes regardless.
		job.Logger.Warn("DVR stop not confirmed; leaving job for stop-obligation retry (no terminal report emitted)")
		return nil
	}

	// Final sync: flush remaining segments to S3. syncNewSegments uses
	// job.syncMutex internally and is idempotent (SyncedSegments tracks
	// what's already uploaded).
	dm.syncNewSegments(job)

	dm.mutex.Lock()
	streamName := job.StreamName
	delete(dm.jobs, dvrHash)
	ClearDVRSourceOverride(streamName)
	dm.mutex.Unlock()

	// Job is removed and now goroutine-private; send the terminal notification
	// without holding the manager lock across the control-stream write. A lost
	// DVRStopped is backstopped by Foghorn's stop_pending obligation, which re-sends
	// DVRStop (then handled via stopRecoveredRecording) until the ack lands.
	dm.sendCompletion(job, "completed", "")

	job.Logger.Info("DVR recording job stopped")
	return nil
}

func (dm *DVRManager) stopRecoveredRecording(dvrHash string, sendFunc func(*ipcpb.ControlMessage)) error {
	segmentsDir := resolveDVRSegmentsDirByHash(dvrHash)
	if segmentsDir == "" {
		return fmt.Errorf("DVR recording not found for hash %s", dvrHash)
	}
	outputDir := filepath.Dir(segmentsDir)
	streamID := filepath.Base(filepath.Dir(outputDir))
	manifestPath := filepath.Join(outputDir, dvrHash+".m3u8")
	duration := dvrManifestDuration(manifestPath)
	startTime := time.Now()
	if duration > 0 {
		startTime = startTime.Add(-duration)
	}

	job := &DVRJob{
		DVRHash:        dvrHash,
		InternalName:   streamID,
		StartTime:      startTime,
		OutputDir:      outputDir,
		ManifestPath:   manifestPath,
		SendFunc:       sendFunc,
		Logger:         dm.logger,
		TotalSizeBytes: dvrDirectorySize(outputDir),
		Status:         "finalizing",
		SyncedSegments: make(map[string]bool),
	}

	dm.logger.WithFields(logging.Fields{
		"dvr_hash":      dvrHash,
		"manifest_path": manifestPath,
		"output_dir":    outputDir,
	}).Warn("Recovered DVR stop from on-disk recording after missing in-memory job")

	// A live Mist push may still be writing this DVR even though the in-memory job
	// was lost (a stop that arrived before restart recovery rebuilt the job map).
	// Confirm the writer is stopped BEFORE reporting completion; matched by the hash
	// in the push target since the runtime stream name is unknown here. If we cannot
	// confirm (list failed, or a found push would not stop), emit NO terminal report
	// so Foghorn's durable stop_pending obligation re-sends DVRStop and re-drives it.
	if dm.mistClient != nil {
		if pushes, listErr := dm.mistClient.PushList(); listErr != nil {
			dm.logger.WithError(listErr).WithField("dvr_hash", dvrHash).Warn("Recovered DVR stop: cannot list pushes to confirm; leaving stop obligation open")
			return nil
		} else if push, ok := findDVRPushByHash(pushes, dvrHash); ok {
			// A live push is present: reset any absence evidence and stop it.
			dm.clearAbsenceEvidence(dvrHash)
			if stopErr := dm.mistClient.PushStop(push.ID); stopErr != nil {
				dm.logger.WithError(stopErr).WithFields(logging.Fields{"dvr_hash": dvrHash, "push_id": push.ID}).Warn("Recovered DVR stop: failed to stop live push; leaving stop obligation open")
				return nil
			}
		} else {
			// Push ABSENT from a successful list is NOT proof of absence — an accepted push can
			// be momentarily unlisted. Accumulate bounded evidence (repeated absence over a real
			// grace + no segment/byte progress) and complete ONLY once it converges. Until then
			// defer: emit no terminal report, so Foghorn's durable stop_pending obligation
			// re-sends DVRStop and re-drives this — each re-send is one observation.
			fp, readOK := dvrFingerprint(outputDir)
			if !dm.observeAbsenceConverged(dvrHash, fp, readOK) {
				dm.logger.WithField("dvr_hash", dvrHash).Info("Recovered DVR stop: push absent from list but absence not yet converged; deferring completion")
				return nil
			}
			dm.clearAbsenceEvidence(dvrHash)
		}
	}

	dm.syncNewSegments(job)
	dm.sendCompletion(job, "completed", "")
	return nil
}

func dvrManifestDuration(manifestPath string) time.Duration {
	manifestBody, err := os.ReadFile(manifestPath)
	if err != nil {
		return 0
	}
	parsed, err := hls.Parse(string(manifestBody))
	if err != nil || parsed == nil {
		return 0
	}
	var total time.Duration
	for _, seg := range parsed.Segments {
		if seg.Duration <= 0 {
			continue
		}
		total += time.Duration(seg.Duration * float64(time.Second))
	}
	return total
}

// dvrDirHasMedia reports whether a DVR output directory already contains recorded
// media: either the manifest lists at least one segment, or a .ts segment file
// exists under segments/. A directory with media must never be restarted into
// (see StartRecording), only adopted (live push) or left for recovery/backfill.
func dvrDirHasMedia(outputDir, manifestPath string) bool {
	if dvrManifestSegmentCount(manifestPath) > 0 {
		return true
	}
	entries, err := os.ReadDir(filepath.Join(outputDir, "segments"))
	if err != nil {
		return false
	}
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".ts") {
			return true
		}
	}
	return false
}

func dvrManifestSegmentCount(manifestPath string) int {
	manifestBody, err := os.ReadFile(manifestPath)
	if err != nil {
		return 0
	}
	parsed, err := hls.Parse(string(manifestBody))
	if err != nil || parsed == nil {
		return 0
	}
	return len(parsed.Segments)
}

// dvrFingerprint returns a change-detecting fingerprint of the recording's on-disk state —
// segment-file count, total bytes, and the NEWEST segment's name + mtime — plus readOK. It reads
// ONLY the segment files' own metadata, NOT the manifest: a manifest parse yielding 0 is
// indistinguishable from an idle writer, so it must not feed the absence signal. The segment files
// alone carry writer progress: any write activity changes the fingerprint, INCLUDING a bounded
// rolling window where old segments are evicted as new ones arrive (count constant, bytes
// flat/falling) but the newest segment name/mtime advances; a stalled writer leaves it identical.
// readOK is false ONLY on a genuine read error (a ReadDir or per-entry Info failure): absence must
// never be inferred from a failed read. A not-yet-existent segments dir is a legitimate empty
// recording (readOK, stable "empty").
func dvrFingerprint(outputDir string) (fingerprint string, readOK bool) {
	if outputDir == "" {
		return "", false
	}
	segmentsDir := filepath.Join(outputDir, "segments")
	entries, err := os.ReadDir(segmentsDir)
	if err != nil {
		if os.IsNotExist(err) {
			return "empty", true // nothing written yet / already removed — definitively empty
		}
		return "", false // genuine read error → inconclusive, never converge
	}
	var count int
	var totalBytes, newestMtime int64
	var newestName string
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".ts") {
			continue
		}
		info, ierr := e.Info()
		if ierr != nil {
			return "", false // partial read → inconclusive, do NOT report a partial fingerprint
		}
		count++
		totalBytes += info.Size()
		if mt := info.ModTime().UnixNano(); mt > newestMtime || (mt == newestMtime && e.Name() > newestName) {
			newestMtime = mt
			newestName = e.Name()
		}
	}
	return fmt.Sprintf("c=%d;b=%d;n=%s;m=%d", count, totalBytes, newestName, newestMtime), true
}

// dvrFingerprintByHash resolves a DVR's recording from its hash (the in-memory job may be
// gone) and returns its fingerprint for bounded-absence convergence on the delete path. A recording
// whose on-disk layout is genuinely gone reports the stable "absent" fingerprint with readOK=true, so
// repeated observations CONVERGE and the durable delete/stop obligation completes — the strongest
// absence evidence must not be mistaken for an inconclusive read (which would strand the obligation
// forever). readOK is false only when storage itself could not be scanned.
func dvrFingerprintByHash(dvrHash string) (string, bool) {
	segmentsDir, scanOK := resolveDVRSegmentsDirByHashChecked(dvrHash)
	if !scanOK {
		return "", false // storage unscannable → inconclusive
	}
	if segmentsDir == "" {
		return "absent", true // scanned, no layout for the hash → genuinely gone, converges
	}
	outputDir := filepath.Dir(segmentsDir)
	return dvrFingerprint(outputDir)
}

func dvrDirectorySize(outputDir string) uint64 {
	var total uint64
	if err := filepath.WalkDir(outputDir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		info, statErr := d.Info()
		if statErr != nil {
			return statErr
		}
		if info.Size() < 0 {
			return nil
		}
		total += uint64(info.Size())
		return nil
	}); err != nil {
		return total
	}
	return total
}

// dvrTargetURI builds the Mist push target for a DVR recording from its output
// dir, hash, and config. It is deterministic, so recovery and monitor recreate
// can rebuild the exact same target as the original start.
func dvrTargetURI(outputDir, dvrHash string, config *ipcpb.DVRConfig) string {
	segmentDuration := int(config.GetSegmentDuration())
	if segmentDuration <= 0 {
		segmentDuration = 6 // Default 6 seconds
	}
	// Live DVR window for Mist's targetAge. Foghorn resolves the effective window
	// via pkg/dvrpolicy and stamps it into DVRConfig.dvr_window_seconds.
	windowSeconds := int(config.GetDvrWindowSeconds())
	if windowSeconds <= 0 {
		windowSeconds = 7200 // 2 hours default
	}
	// maxEntries caps manifest playlist size to avoid huge multi-day playlists
	// breaking HLS parsers. Foghorn-resolved value already accounts for tier +
	// cluster ceilings; fall back to ceil(window/segment) if not provided.
	maxEntries := int(config.GetMaxEntries())
	if maxEntries <= 0 {
		maxEntries = (windowSeconds + segmentDuration - 1) / segmentDuration
		if maxEntries < 1 {
			maxEntries = 1
		}
	}
	// Segments go to {outputDir}/segments/, manifest at {outputDir}/{hash}.m3u8.
	// nounlink=1 stops Mist deleting segment files when pruning the rolling
	// playlist (segment removal is owned by the chapter reclaim sweep). Record
	// only source A/V tracks — derived renditions make MistInHLS reject the playlist.
	return fmt.Sprintf("%s/%s/$minute_$segmentCounter.ts#m3u8=../%s.m3u8&audio=source&video=source&subtitle=none&meta=none&split=%d&targetAge=%d&maxEntries=%d&append=1&noendlist=1&nounlink=1",
		outputDir, "segments", dvrHash, segmentDuration, windowSeconds, maxEntries,
	)
}

// startDVRPush starts DVR recording via MistServer push API
func (dm *DVRManager) startDVRPush(job *DVRJob) error {
	targetURI := dvrTargetURI(job.OutputDir, job.DVRHash, job.Config)

	// Foghorn is the sole authority for the source runtime name —
	// resolved from the stream registry's RuntimeNameFor(ingest_mode,
	// internal_name) and supplied via DVRStartRequest.source_runtime_name.
	// Helmsman uses it verbatim and fails closed when missing.
	streamName := strings.TrimSpace(job.SourceRuntimeName)
	if streamName == "" {
		return fmt.Errorf("DVR start missing source_runtime_name for hash %s", job.DVRHash)
	}
	job.TargetURI = targetURI
	job.StreamName = streamName
	sourceOverrideRegistered := false
	if sourceURL := strings.TrimSpace(job.SourceURL); sourceURL != "" {
		RegisterDVRSourceOverride(streamName, sourceURL)
		sourceOverrideRegistered = true
	}

	// The job is still private (not yet in dm.jobs and no monitor running),
	// so reading its identity here races with no other writer.
	snap := pushIdentity{streamName: job.StreamName, targetURI: job.TargetURI, dvrHash: job.DVRHash}
	pushID, outcome, err := dm.ensureInitialPush(snap, job.Logger)
	switch outcome {
	case dvrPushConfirmed:
		job.PushID = pushID
		job.Status = "recording"
		job.Logger.WithFields(logging.Fields{
			"push_id": pushID,
			"stream":  streamName,
			"target":  targetURI,
		}).Info("Started DVR recording via MistServer push")
		return nil
	case dvrPushAcceptedUnconfirmed:
		// PushStart was accepted but we could not confirm the push within the
		// window. A push may be live and writing, so we must NOT retry creation
		// (double writer) nor fail closed (which would delete recovery metadata).
		// Register the job as still-starting with an unknown PushID; the monitor
		// loop reconciles it by exact identity (maintainPushStatus) and the
		// startup timeout is the backstop if it never confirms.
		job.PushID = 0
		job.Status = "starting"
		job.Logger.WithFields(logging.Fields{
			"stream": streamName,
			"target": targetURI,
			"error":  errString(err),
		}).Warn("DVR push accepted but unconfirmed; monitor will reconcile by identity")
		return nil
	default: // dvrPushNotStarted — no push was ever created; safe for the caller to roll back.
		if sourceOverrideRegistered {
			ClearDVRSourceOverride(streamName)
		}
		return fmt.Errorf("failed to start DVR push: %w", err)
	}
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// monitorJob monitors a DVR job's progress and performs incremental sync
func (dm *DVRManager) monitorJob(job *DVRJob) {
	pushInterval := dm.pushMonitorInterval
	if pushInterval <= 0 {
		pushInterval = PushMonitorInterval
	}
	startupTimeout := dm.startupTimeout
	if startupTimeout <= 0 {
		startupTimeout = dvrStartupTimeout
	}
	progressTicker := time.NewTicker(30 * time.Second) // Progress updates every 30s
	pushTicker := time.NewTicker(pushInterval)         // Push monitoring
	syncTicker := time.NewTicker(10 * time.Second)     // Incremental sync every 10s
	// One-shot startup deadline, created once outside the select so it actually
	// elapses (a time.After in the select would be rebuilt on every ticker fire).
	// It bounds how long a job may stay unconfirmed before final reconciliation.
	startupTimer := time.NewTimer(startupTimeout)
	defer progressTicker.Stop()
	defer pushTicker.Stop()
	defer syncTicker.Stop()
	defer startupTimer.Stop()

	for {
		select {
		case <-progressTicker.C:
			dm.mutex.RLock()
			_, exists := dm.jobs[job.DVRHash]
			dm.mutex.RUnlock()

			if !exists {
				return // Job completed or stopped
			}

			if err := dm.hasSpaceFor(dm.storagePath, 0); err != nil {
				// Disk pressure under an active DVR: try to reclaim local
				// space first by evicting uploaded-and-aged segments via
				// Foghorn's authoritative evictable list. Only kill the
				// push if pressure persists after the eviction pass.
				var totalEvicted int
				pressureRelieved := false
				for batch := 0; batch < maxDVREvictionBatches; batch++ {
					evictCtx, evictCancel := context.WithTimeout(context.Background(), 5*time.Second)
					resp, evictErr := RequestEvictableSegments(evictCtx, job.DVRHash, dvrEvictionBatchSize)
					evictCancel()
					if evictErr != nil || resp == nil || len(resp.GetSegmentNames()) == 0 {
						break
					}
					evicted, _ := dm.EvictUploadedSegments(job.DVRHash, resp.GetSegmentNames(), "disk_pressure")
					totalEvicted += evicted
					if evicted == 0 {
						break
					}
					if reEvalErr := dm.hasSpaceFor(dm.storagePath, 0); reEvalErr == nil {
						job.Logger.WithFields(logging.Fields{
							"segments_evicted": totalEvicted,
							"dvr_hash":         job.DVRHash,
						}).Warn("Disk pressure under active DVR relieved by segment eviction")
						pressureRelieved = true
						break
					}
				}
				if pressureRelieved {
					continue
				}
				if totalEvicted > 0 {
					job.Logger.WithFields(logging.Fields{
						"segments_evicted": totalEvicted,
						"dvr_hash":         job.DVRHash,
					}).Warn("Disk pressure under active DVR; evicted uploaded-and-aged segments")
				}
				// Eviction didn't suffice (or there was nothing to evict).
				// Stop cleanly so Foghorn can finalize what was uploaded.
				job.Logger.WithError(err).Error("Stopping DVR recording: disk pressure persists after eviction pass")
				// Commit the terminal status + bump the generation under the
				// lock (so an in-flight recreate goes stale) and snapshot the
				// fields the unlocked Mist call / cleanup need.
				dm.mutex.Lock()
				stopPushID := job.PushID
				streamName := job.StreamName
				job.Status = "failed"
				job.pushGeneration++
				dm.mutex.Unlock()
				if stopPushID > 0 {
					if stopErr := dm.mistClient.PushStop(stopPushID); stopErr != nil {
						job.Logger.WithError(stopErr).Warn("Failed to stop MistServer push during disk-full shutdown")
					}
				}
				dm.sendCompletion(job, "failed", sanitizeDvrStorageError(err))
				dm.mutex.Lock()
				delete(dm.jobs, job.DVRHash)
				ClearDVRSourceOverride(streamName)
				dm.mutex.Unlock()
				return
			}

			// Update progress and send notifications
			dm.updateProgress(job)

		case <-pushTicker.C:
			dm.mutex.RLock()
			_, exists := dm.jobs[job.DVRHash]
			dm.mutex.RUnlock()

			if !exists {
				return // Job completed or stopped
			}

			// Check and maintain push status
			dm.maintainPushStatus(job)

		case <-syncTicker.C:
			dm.mutex.RLock()
			_, exists := dm.jobs[job.DVRHash]
			dm.mutex.RUnlock()

			if !exists {
				return // Job completed or stopped
			}

			// Dual-storage: Sync new segments to S3
			dm.syncNewSegments(job)

		case <-startupTimer.C: // one-shot startup deadline
			dm.mutex.RLock()
			status := job.Status
			snap := pushIdentity{streamName: job.StreamName, targetURI: job.TargetURI, dvrHash: job.DVRHash, generation: job.pushGeneration}
			dm.mutex.RUnlock()
			if status != "starting" {
				continue // confirmed in time — nothing to reconcile
			}
			// AUTHORITATIVE final reconciliation before declaring the writer stopped.
			// An accepted-but-unconfirmed push (PushID 0) may still be live; we must
			// not blindly StopRecording (which cannot stop it by an unknown ID).
			pushes, listErr := dm.mistClient.PushList()
			if listErr != nil {
				// Mist is unreachable — absence was never established. RETAIN the job
				// (quarantined protection) rather than declaring it stopped, and
				// re-arm the deadline to reconcile again later.
				job.Logger.WithError(listErr).Warn("DVR startup deadline reached but Mist is unavailable; retaining quarantined job")
				startupTimer.Reset(startupTimeout)
				continue
			}
			if push, ok := findExactDVRPush(pushes, snap.streamName, snap.targetURI, snap.dvrHash); ok {
				// The push IS live — adopt it by real ID instead of stopping it.
				dm.clearAbsenceEvidence(snap.dvrHash)
				dm.withFreshGeneration(snap, func(j *DVRJob) {
					j.PushID = push.ID
					j.Status = "recording"
				})
				job.Logger.WithField("push_id", push.ID).Info("DVR push confirmed by identity at startup deadline; adopted")
				continue
			}
			// Mist responded and no matching push exists. A single absence is NOT proof — an
			// accepted push can be momentarily unlisted. Require bounded-absence convergence
			// (repeated absence over a real grace + no segment/byte progress) before
			// terminalizing; otherwise re-arm the deadline and reconcile again, retaining the job.
			fp, readOK := dvrFingerprint(job.OutputDir)
			if !dm.observeAbsenceConverged(snap.dvrHash, fp, readOK) {
				job.Logger.Info("DVR startup deadline: no live push but absence not yet converged; re-arming to reconcile again")
				startupTimer.Reset(startupTimeout)
				continue
			}
			dm.clearAbsenceEvidence(snap.dvrHash)
			job.Logger.Warn("DVR push never confirmed within the startup deadline (converged absence); stopping")
			if err := dm.StopRecording(job.DVRHash); err != nil {
				job.Logger.WithError(err).Warn("Failed to stop timed-out DVR job")
			}
			return
		}
	}
}

// updateProgress updates job progress and sends notification
func (dm *DVRManager) updateProgress(job *DVRJob) {
	// Check output directory for segments
	segmentCount := 0
	totalSize := uint64(0)

	// Check segments directory specifically
	segmentsDir := filepath.Join(job.OutputDir, "segments")
	if entries, err := os.ReadDir(segmentsDir); err == nil {
		for _, entry := range entries {
			if !entry.IsDir() {
				info, err := entry.Info()
				if err == nil {
					totalSize += uint64(info.Size())
					if filepath.Ext(entry.Name()) == ".ts" || filepath.Ext(entry.Name()) == ".m4s" {
						segmentCount++
					}
				}
			}
		}
	}

	// Commit the counters and snapshot what the progress message needs under
	// the lock (OutputDir is immutable post-creation, so the scan above is
	// lock-free; Status/SendFunc are mutated by other goroutines). The send
	// itself runs unlocked.
	dm.mutex.Lock()
	job.SegmentCount = segmentCount
	job.TotalSizeBytes = totalSize
	status := job.Status
	dvrHash := job.DVRHash
	sendFunc := job.SendFunc
	dm.mutex.Unlock()

	if sendFunc != nil {
		progress := &ipcpb.DVRProgress{
			DvrHash:      dvrHash,
			Status:       status,
			SegmentCount: int32(segmentCount),
			SizeBytes:    totalSize,
			Message:      fmt.Sprintf("Recording %d segments", segmentCount),
		}

		msg := &ipcpb.ControlMessage{
			SentAt:  timestamppb.Now(),
			Payload: &ipcpb.ControlMessage_DvrProgress{DvrProgress: progress},
		}
		sendFunc(msg)
	}
}

// maintainPushStatus intelligently maintains push status with retry logic
func (dm *DVRManager) maintainPushStatus(job *DVRJob) {
	// Snapshot the push identity + retry bookkeeping under the lock so the Mist
	// calls below run unlocked. The pointer check guards against this monitor
	// outliving its job (deleted then re-created for the same hash).
	dm.mutex.RLock()
	current, ok := dm.jobs[job.DVRHash]
	if !ok || current != job {
		dm.mutex.RUnlock()
		return
	}
	status := job.Status
	retryCount := job.RetryCount
	maxRetries := job.MaxRetries
	lastAttempt := job.LastPushAttempt
	segmentCount := job.SegmentCount
	snap := pushIdentity{
		streamName: job.StreamName,
		targetURI:  job.TargetURI,
		dvrHash:    job.DVRHash,
		pushID:     job.PushID,
		generation: job.pushGeneration,
	}
	dm.mutex.RUnlock()

	switch status {
	case "finalizing", "completed", "completed_partial", "failed":
		return // Don't maintain finalizing/terminal jobs
	}

	// Check if push still exists in push list
	pushes, err := dm.mistClient.PushList()
	if err != nil {
		job.Logger.WithError(err).Warn("Failed to check push status")
		return
	}

	// A job with PushID 0 is an accepted-but-unconfirmed start/recreate: ADOPT the
	// push by identity if it appears, and otherwise WAIT — never re-issue PushStart.
	// The command may have been accepted while Mist has not yet exposed it in the
	// push list; re-issuing would create a SECOND writer against the same target,
	// and adopt-by-identity cannot protect against a push the list does not reveal
	// (there is no idempotency key or authoritative rejection signal). A quarantined
	// job that never resolves is bounded by the session lifecycle: the source's
	// STREAM_END drives StopDVRForEndedSource on Foghorn, which finalizes it. Stop
	// remains safe regardless — StopRecordingWithSender stops by identity when the
	// push id is unknown.
	if snap.pushID == 0 {
		if push, ok := findExactDVRPush(pushes, snap.streamName, snap.targetURI, snap.dvrHash); ok {
			dm.withFreshGeneration(snap, func(j *DVRJob) {
				j.PushID = push.ID
				j.Status = "recording"
			})
			job.Logger.WithFields(logging.Fields{
				"dvr_hash": snap.dvrHash,
				"push_id":  push.ID,
			}).Info("Reconciled accepted-but-unconfirmed DVR push by identity")
		}
		return
	}

	// Look for our push
	pushFound := false
	pushHasErrors := false
	for _, push := range pushes {
		if push.ID == snap.pushID {
			pushFound = true

			// Check push logs for recoverable vs non-recoverable errors
			for _, log := range push.Logs {
				logLower := strings.ToLower(log)
				if strings.Contains(logLower, "error") || strings.Contains(logLower, "failed") {
					// Log error but don't immediately fail - could be transient DTSC issue
					job.Logger.WithField("push_log", log).Debug("Push error detected, may retry")
					pushHasErrors = true
				}
			}
			break
		}
	}

	// If the recorder push disappeared after producing segments, Mist has
	// normally completed the recording and removed the push. Treat that as the
	// terminal signal before trying to recreate anything; recreating here opens
	// a fresh append/noendlist writer against a stopped source.
	if !pushFound && segmentCount > 0 {
		dm.mutex.Lock()
		exists := dm.jobs[job.DVRHash] == job
		if exists {
			job.Status = "completed"
			job.pushGeneration++
			delete(dm.jobs, job.DVRHash)
			ClearDVRSourceOverride(job.StreamName)
		}
		dm.mutex.Unlock()
		if exists {
			job.Logger.Info("DVR recording completed successfully")
			dm.sendCompletion(job, "success", "")
		}
		return
	}

	// If push missing or has errors, attempt recreation (unless we've exceeded retries)
	if (!pushFound || pushHasErrors) && retryCount < maxRetries {
		// Calculate backoff delay
		retryDelay := dm.calculateRetryDelay(retryCount)
		if time.Since(lastAttempt) < retryDelay {
			return // Not time to retry yet
		}

		job.Logger.WithFields(logging.Fields{
			"retry_count": retryCount,
			"push_found":  pushFound,
			"has_errors":  pushHasErrors,
		}).Info("Recreating DVR push due to failure or absence")

		// If the current push is PRESENT but errored, STOP it by ID first —
		// otherwise createOrRecreatePush adopts the same broken push and nothing is
		// actually replaced. If it cannot be stopped, back off rather than risk a
		// second writer against the same DVR target.
		if pushHasErrors && snap.pushID > 0 {
			if stopErr := dm.mistClient.PushStop(snap.pushID); stopErr != nil {
				dm.withFreshGeneration(snap, func(j *DVRJob) {
					j.RetryCount++
					j.LastPushAttempt = time.Now()
				})
				job.Logger.WithError(stopErr).Warn("Failed to stop errored DVR push before recreate; backing off")
				return
			}
		}

		// Attempt to recreate push without the lock held.
		newPushID, issued, err := dm.createOrRecreatePush(snap)
		if err != nil {
			dm.withFreshGeneration(snap, func(j *DVRJob) {
				j.RetryCount++
				j.LastPushAttempt = time.Now()
				if issued {
					// A push may be live but unconfirmed — the next pass must NOT
					// re-issue PushStart (double writer). PushID 0 routes it to the
					// adopt-by-identity path, which reconciles the accepted push.
					j.PushID = 0
				}
			})
			job.Logger.WithError(err).WithFields(logging.Fields{"retry_count": retryCount, "issued": issued}).Warn("Failed to recreate push")
			return
		}

		// Commit the new push id only if our snapshot is still current.
		committed := dm.withFreshGeneration(snap, func(j *DVRJob) {
			j.PushID = newPushID
			j.pushGeneration++
			j.RetryCount++
			j.LastPushAttempt = time.Now()
		})
		if !committed {
			// A stop or a concurrent recreate won; the push we just started is
			// orphaned and must be stopped to avoid a double-writer onto the
			// same DVR target.
			if stopErr := dm.mistClient.PushStop(newPushID); stopErr != nil {
				job.Logger.WithError(stopErr).WithField("orphan_push_id", newPushID).Warn("Failed to stop orphaned DVR push after stale recreate")
			}
			return
		}
		job.Logger.WithFields(logging.Fields{
			"new_push_id": newPushID,
			"retry_count": retryCount + 1,
		}).Info("Successfully recreated DVR push")

		return
	}

	// If push disappeared and we've exhausted retries, fail the job. Mutate +
	// delete under the lock, then send the terminal notification unlocked (the
	// removed job is goroutine-private; never hold the lock across SendFunc).
	if !pushFound && retryCount >= maxRetries {
		dm.mutex.Lock()
		// Identity guard: only terminate if the map still holds THIS job. A
		// stop+restart for the same hash during the unlocked PushList above
		// would otherwise make this stale monitor kill the fresh job.
		exists := dm.jobs[job.DVRHash] == job
		if exists {
			job.Status = "failed"
			job.pushGeneration++
			delete(dm.jobs, job.DVRHash)
			ClearDVRSourceOverride(job.StreamName)
		}
		dm.mutex.Unlock()
		if exists {
			job.Logger.WithField("retry_count", retryCount).Error("DVR push failed after maximum retries")
			dm.sendCompletion(job, "failed", fmt.Sprintf("Push failed after %d retries", retryCount))
		}
		return
	}
}

// pushIdentity is a lock-free snapshot of a job's push-relevant fields, taken
// under DVRManager.mutex so the Mist network calls that follow (PushStart /
// PushStop / PushList) run without holding the global lock. generation pins the
// value at snapshot time; commits are gated on it being unchanged.
type pushIdentity struct {
	streamName string
	targetURI  string
	dvrHash    string
	pushID     int
	generation uint64
}

// withFreshGeneration runs fn under the manager lock iff the job still exists
// and its pushGeneration matches the snapshot. It returns false (without
// running fn) when stale — i.e. another writer advanced the generation while
// the caller was doing Mist I/O. fn must bump pushGeneration when it changes
// push identity so a slower concurrent recreate also goes stale.
func (dm *DVRManager) withFreshGeneration(snap pushIdentity, fn func(job *DVRJob)) bool {
	dm.mutex.Lock()
	defer dm.mutex.Unlock()
	job, ok := dm.jobs[snap.dvrHash]
	if !ok || job.pushGeneration != snap.generation {
		return false
	}
	fn(job)
	return true
}

// createOrRecreatePush ensures a MistServer push exists for the snapshot's
// identity, returning its ID. It reads only the snapshot (no lock held). It first
// ADOPTS an existing exact-identity push and issues PushStart (non-idempotent)
// only when none exists, so a caller that retries re-adopts rather than
// duplicating. Callers that need to REPLACE a specific push (e.g. an errored one)
// must PushStop it by ID first, so this function does not adopt the very push
// being replaced.
//
// `issued` reports whether this call issued a PushStart. When it did AND the push
// could not be confirmed (ambiguous error, or accepted-but-not-yet-visible), the
// error is returned WITH issued=true: the caller must NOT re-issue PushStart (a
// second writer), and should reconcile the accepted push by identity instead.
func (dm *DVRManager) createOrRecreatePush(snap pushIdentity) (pushID int, issued bool, err error) {
	if pushes, e := dm.mistClient.PushList(); e == nil {
		if push, ok := findExactDVRPush(pushes, snap.streamName, snap.targetURI, snap.dvrHash); ok {
			return push.ID, false, nil // adopted an existing push; nothing issued
		}
	}

	if pushErr := dm.mistClient.PushStart(snap.streamName, snap.targetURI); pushErr != nil {
		if errors.Is(pushErr, mist.ErrMistAmbiguous) {
			// The start may have been ACCEPTED despite the lost response. Try to
			// confirm; either way mark issued so the caller never re-issues.
			if pushes, listErr := dm.mistClient.PushList(); listErr == nil {
				if push, ok := findExactDVRPush(pushes, snap.streamName, snap.targetURI, snap.dvrHash); ok {
					return push.ID, true, nil
				}
			}
			return 0, true, fmt.Errorf("push start ambiguous, unconfirmed: %w", pushErr)
		}
		// A clean rejection means nothing was created — safe for the caller to retry.
		return 0, false, fmt.Errorf("failed to start push: %w", pushErr)
	}

	// PushStart succeeded — a push exists even if the listing hasn't caught up.
	deadline := time.Now().Add(pushListVisibilityFor)
	for {
		if pushes, e := dm.mistClient.PushList(); e == nil {
			if push, ok := findExactDVRPush(pushes, snap.streamName, snap.targetURI, snap.dvrHash); ok {
				return push.ID, true, nil
			}
		}
		if time.Now().Add(pushListVisibilityPollFor).After(deadline) {
			break
		}
		time.Sleep(pushListVisibilityPollFor)
	}

	return 0, true, fmt.Errorf("push started but not confirmed in push list")
}

// dvrPushOutcome distinguishes the three states an initial push attempt can end
// in — critical because PushStart is NON-idempotent, so "we never started" and
// "we started but couldn't confirm" must be handled differently: the former may
// retry / roll back; the latter must NOT retry PushStart and must NOT delete
// recovery metadata, because a live push may already be writing.
type dvrPushOutcome int

const (
	dvrPushConfirmed           dvrPushOutcome = iota // an exact-identity push is confirmed live
	dvrPushAcceptedUnconfirmed                       // PushStart was accepted; a push may be live but is unconfirmed
	dvrPushNotStarted                                // PushStart never succeeded; no push was created
)

// findExactDVRPush returns the push whose RUNTIME matches streamName exactly and
// whose target either equals targetURI or contains dvrHash. The runtime match is
// the hard guarantee (a foreign publisher's push under a different runtime never
// matches); the target side falls back to hash containment because Mist expands
// the target we send (see dvrPushMatches). Callers pass targetURI="" when they
// only know the runtime + hash.
func findExactDVRPush(pushes []mist.PushInfo, streamName, targetURI, dvrHash string) (mist.PushInfo, bool) {
	for _, push := range pushes {
		if dvrPushMatches(push, streamName, targetURI, dvrHash) {
			return push, true
		}
	}
	return mist.PushInfo{}, false
}

// findDVRPushByHash matches any push writing this DVR's target by the hash embedded
// in the target path, WITHOUT requiring the runtime stream name. Used only by the
// on-disk recovery stop, where the in-memory job (and thus the stream name) was
// lost, so the exact-identity match cannot be used.
func findDVRPushByHash(pushes []mist.PushInfo, dvrHash string) (mist.PushInfo, bool) {
	if dvrHash == "" {
		return mist.PushInfo{}, false
	}
	for _, push := range pushes {
		if strings.Contains(push.TargetURI, dvrHash) || strings.Contains(push.ActualURI, dvrHash) {
			return push, true
		}
	}
	return mist.PushInfo{}, false
}

// ensureInitialPush drives the initial DVR push as an idempotent state machine.
// It issues at most ONE PushStart for the whole call: each iteration first tries
// to CONFIRM an exact-identity push (adopting a prior iteration's accepted push
// or any pre-existing identical one); only if none is found and no start has yet
// been accepted does it PushStart. This never creates a second writer. It returns
// dvrPushConfirmed with the live PushID, dvrPushAcceptedUnconfirmed when a start
// was accepted but never confirmed within the window (caller keeps the job +
// metadata; the monitor reconciles by identity), or dvrPushNotStarted when
// PushStart never succeeded (caller may roll back).
func (dm *DVRManager) ensureInitialPush(snap pushIdentity, logger logging.Logger) (int, dvrPushOutcome, error) {
	pushStartAccepted := false
	var lastErr error
	deadline := time.Now().Add(initialPushRetryFor)
	for attempt := 0; ; attempt++ {
		if pushes, listErr := dm.mistClient.PushList(); listErr == nil {
			if push, ok := findExactDVRPush(pushes, snap.streamName, snap.targetURI, snap.dvrHash); ok {
				return push.ID, dvrPushConfirmed, nil
			}
		} else {
			lastErr = listErr
		}
		// Issue PushStart at most once across the whole call — it is non-idempotent.
		// A CLEAN rejection (Mist answered non-200) leaves us free to retry; an
		// AMBIGUOUS error (transport/decode after send) may have been accepted, so
		// we must treat it as issued and confirm via PushList, never re-issue.
		if !pushStartAccepted {
			startErr := dm.mistClient.PushStart(snap.streamName, snap.targetURI)
			if startErr == nil || errors.Is(startErr, mist.ErrMistAmbiguous) {
				pushStartAccepted = true
			}
			if startErr != nil {
				lastErr = startErr
			}
		}
		if time.Now().Add(initialPushRetryEvery).After(deadline) {
			if pushStartAccepted {
				return 0, dvrPushAcceptedUnconfirmed, lastErr
			}
			return 0, dvrPushNotStarted, lastErr
		}
		logger.WithFields(logging.Fields{
			"attempt":  attempt + 1,
			"accepted": pushStartAccepted,
			"stream":   snap.streamName,
		}).Warn("DVR initial push not confirmed yet; retrying")
		time.Sleep(initialPushRetryEvery)
	}
}

func dvrPushMatches(push mist.PushInfo, streamName, targetURI, dvrHash string) bool {
	if push.StreamName != streamName {
		return false
	}
	if targetURI != "" && (push.TargetURI == targetURI || push.ActualURI == targetURI) {
		return true
	}
	if dvrHash == "" {
		return false
	}
	return strings.Contains(push.TargetURI, dvrHash) || strings.Contains(push.ActualURI, dvrHash)
}

// calculateRetryDelay calculates exponential backoff delay for push retries
func (dm *DVRManager) calculateRetryDelay(retryCount int) time.Duration {
	// Exponential backoff: 5s, 10s, 20s, 40s, 60s (max)
	delay := min(InitialRetryDelay*time.Duration(1<<uint(retryCount)), MaxRetryDelay)
	return delay
}

// sendCompletion sends DVR completion notification
func (dm *DVRManager) sendCompletion(job *DVRJob, status string, errorMsg string) {
	// SendFunc and TotalSizeBytes are mutated by other goroutines under the
	// lock (StopRecordingWithSender / updateProgress); snapshot them here so the
	// terminal payload can't tear against an in-flight progress write. All
	// callers invoke this without holding dm.mutex, so the RLock is safe.
	dm.mutex.RLock()
	sendFunc := job.SendFunc
	sizeBytes := job.TotalSizeBytes
	dm.mutex.RUnlock()
	if sendFunc == nil {
		return
	}

	durationSeconds := int32(time.Since(job.StartTime).Seconds())

	stopped := &ipcpb.DVRStopped{
		DvrHash:         job.DVRHash,
		Status:          status,
		Error:           errorMsg,
		ManifestPath:    job.ManifestPath,
		DurationSeconds: durationSeconds,
		SizeBytes:       sizeBytes,
	}

	msg := &ipcpb.ControlMessage{
		SentAt:  timestamppb.Now(),
		Payload: &ipcpb.ControlMessage_DvrStopped{DvrStopped: stopped},
	}
	sendFunc(msg)
}

func sanitizeDvrStorageError(err error) string {
	if storage.IsInsufficientSpace(err) {
		return "Recording stopped: storage node out of space"
	}
	return "Recording stopped: storage error"
}

// GetActiveJobs returns information about active DVR jobs
func (dm *DVRManager) GetActiveJobs() map[string]string {
	dm.mutex.RLock()
	defer dm.mutex.RUnlock()

	jobs := make(map[string]string)
	for hash, job := range dm.jobs {
		jobs[hash] = job.Status
	}
	return jobs
}

// syncNewSegments is the reconciliation backstop for the rare case Mist's
// RECORDING_SEGMENT trigger was missed (process restart mid-segment, hard
// network blip between Mist and the trigger HTTP endpoint). RECORDING_SEGMENT
// remains the primary writer; this path discovers segments present on disk
// but absent from the in-memory uploaded cache and routes them through the
// same ledger primitives — RecordDVRSegment + MarkDVRSegmentUploaded — so
// they appear in foghorn.dvr_segments and are visible to the chapter
// finalization queue.
//
// Media timing comes from #EXT-X-PROGRAM-DATE-TIME when Mist writes it; once
// anchored, later entries in the same playlist advance by their EXTINF
// duration. Without PDT the rolling playlist has no media-clock anchor for
// segments before its first entry, so reconciliation must not fabricate
// chapter placement.
func (dm *DVRManager) syncNewSegments(job *DVRJob) {
	if !IsConnected() {
		return
	}

	manifestBody, err := os.ReadFile(job.ManifestPath)
	if err != nil {
		// Manifest may not exist yet in the first few seconds of a recording.
		return
	}
	parsed, err := hls.Parse(string(manifestBody))
	if err != nil || parsed == nil || len(parsed.Segments) == 0 {
		return
	}
	segmentsDir := filepath.Join(job.OutputDir, "segments")
	var newCount int
	var skippedNoClock int
	var nextClockMs int64

	for _, seg := range parsed.Segments {
		durationMs := int64(seg.Duration * 1000.0)
		mediaStartMs := seg.ProgramDateTimeMs
		if mediaStartMs <= 0 && nextClockMs > 0 {
			mediaStartMs = nextClockMs
		}
		if mediaStartMs > 0 {
			nextClockMs = mediaStartMs + durationMs
		}

		job.syncMutex.Lock()
		alreadySynced := job.SyncedSegments[seg.Name]
		job.syncMutex.Unlock()
		if alreadySynced {
			continue
		}

		segPath := filepath.Join(segmentsDir, seg.Name)
		info, err := os.Stat(segPath)
		if err != nil {
			// Segment file is gone (evicted) or not yet present. Don't
			// fabricate a ledger row.
			continue
		}
		if mediaStartMs <= 0 {
			skippedNoClock++
			continue
		}
		mediaEndMs := mediaStartMs + durationMs

		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		resp, err := RecordDVRSegment(ctx, job.DVRHash, seg.Name, segPath, mediaStartMs, mediaEndMs, durationMs)
		if err != nil || resp == nil {
			cancel()
			if err != nil {
				job.Logger.WithError(err).WithField("segment", seg.Name).Warn("Reconciliation: RecordDVRSegment failed")
			}
			continue
		}
		if !resp.GetAccepted() {
			cancel()
			reason := resp.GetReason()
			if reason == "dvr_terminal" {
				if dropErr := SendDVRSegmentDropped(
					job.DVRHash, seg.Name, "artifact_terminal", segPath,
					mediaStartMs, mediaEndMs, durationMs, uint64(info.Size()), false,
				); dropErr != nil {
					job.Logger.WithError(dropErr).WithField("segment", seg.Name).Debug("Reconciliation: DVRSegmentDropped emit failed")
				}
			} else {
				job.Logger.WithFields(logging.Fields{
					"segment": seg.Name,
					"reason":  reason,
				}).Warn("Reconciliation: RecordDVRSegment rejected; leaving segment retryable")
			}
			continue
		}
		if resp.GetPresignedPutUrl() == "" {
			cancel()
			continue
		}
		if upErr := dm.uploadSegmentToS3(ctx, segPath, resp.GetPresignedPutUrl()); upErr != nil {
			cancel()
			job.Logger.WithError(upErr).WithField("segment", seg.Name).Warn("Reconciliation: upload failed")
			continue
		}
		cancel()
		if markErr := SendMarkDVRSegmentUploaded(job.DVRHash, seg.Name, uint64(info.Size())); markErr != nil {
			job.Logger.WithError(markErr).WithField("segment", seg.Name).Warn("Reconciliation: MarkDVRSegmentUploaded failed")
		}
		job.syncMutex.Lock()
		job.SyncedSegments[seg.Name] = true
		job.syncMutex.Unlock()
		if idx := localSegmentIndex; idx != nil {
			idx.MarkUploaded(job.DVRHash, seg.Name, segPath, info.Size())
		}
		newCount++
	}

	if newCount > 0 {
		job.Logger.WithFields(logging.Fields{
			"reconciled": newCount,
			"dvr_hash":   job.DVRHash,
		}).Info("Reconciled DVR segments missed by RECORDING_SEGMENT trigger")
	}
	if skippedNoClock > 0 {
		job.Logger.WithFields(logging.Fields{
			"skipped":  skippedNoClock,
			"dvr_hash": job.DVRHash,
		}).Warn("Skipped DVR reconciliation segments without program-date-time")
	}
}

// parseManifestSegments extracts segment filenames from an HLS manifest
func (dm *DVRManager) parseManifestSegments(manifestPath string) ([]string, error) {
	file, err := os.Open(manifestPath)
	if err != nil {
		return nil, err
	}
	defer func() { _ = file.Close() }()

	var segments []string
	scanner := bufio.NewScanner(file)
	var pendingExtinf bool

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())

		if strings.HasPrefix(line, "#EXTINF:") {
			pendingExtinf = true
			continue
		}

		if pendingExtinf && !strings.HasPrefix(line, "#") && line != "" {
			// This is a segment filename (may have path like "segments/foo.ts")
			segName := filepath.Base(line)
			// Strip query params if present
			if idx := strings.Index(segName, "?"); idx > 0 {
				segName = segName[:idx]
			}
			segments = append(segments, segName)
			pendingExtinf = false
		}
	}

	return segments, scanner.Err()
}

// UploadSegmentForRetry is the exported entrypoint used by the
// finalize-time retry handler in handlers/storage_manager.go. It is a thin
// wrapper around the internal upload primitive so the handler does not
// need to import unexported package state.
func (dm *DVRManager) UploadSegmentForRetry(ctx context.Context, filePath, presignedURL string) error {
	return dm.uploadSegmentToS3(ctx, filePath, presignedURL)
}

// stopJobAfterTerminalRejection enforces the hard invariant that follows a
// dvr_terminal RecordDVRSegment rejection. The Foghorn-side artifact is no
// longer accepting segments; keeping Mist pushing locally just produces an
// unbounded stream of rejected uploads with no archive trail. Stop the
// push (best-effort; PushStop failures are logged but the job is removed
// regardless) and drop the local DVRJob.
func (dm *DVRManager) stopJobAfterTerminalRejection(job *DVRJob) {
	// Snapshot push identity + mark terminal + bump the generation under the
	// lock (so an in-flight recreate commits stale and stops its own push),
	// then stop this push without the lock held.
	dm.mutex.Lock()
	stopPushID := job.PushID
	streamName := job.StreamName
	job.Status = "failed"
	job.pushGeneration++
	dm.mutex.Unlock()
	if stopPushID > 0 && dm.mistClient != nil {
		if err := dm.mistClient.PushStop(stopPushID); err != nil {
			job.Logger.WithError(err).Warn("PushStop after terminal rejection failed; removing job anyway")
		}
	}
	dm.mutex.Lock()
	// Identity guard: only remove if the map still holds THIS job, so a
	// stop+restart for the same hash racing this terminal callback can't wipe
	// the fresh job.
	if dm.jobs[job.DVRHash] == job {
		delete(dm.jobs, job.DVRHash)
		ClearDVRSourceOverride(streamName)
	}
	dm.mutex.Unlock()
}

// uploadSegmentToS3 uploads a segment file using a presigned PUT URL via
// the shared HTTP client. Streaming the *os.File body uses constant
// memory regardless of segment size; Content-Length is set explicitly so
// the client never falls back to chunked encoding (some S3 endpoints
// reject chunked PUTs against presigned URLs).
func (dm *DVRManager) uploadSegmentToS3(ctx context.Context, filePath, presignedURL string) error {
	file, err := os.Open(filePath)
	if err != nil {
		return fmt.Errorf("failed to open segment file: %w", err)
	}
	defer func() { _ = file.Close() }()

	info, err := file.Stat()
	if err != nil {
		return fmt.Errorf("failed to stat segment file: %w", err)
	}

	req, err := newHTTPRequest(ctx, "PUT", presignedURL, file)
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}
	req.ContentLength = info.Size()
	req.Header.Set("Content-Type", "video/MP2T")

	resp, err := httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("failed to upload to S3: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode >= 300 {
		return fmt.Errorf("S3 upload failed with status %d", resp.StatusCode)
	}

	return nil
}

// Rolling manifest is local-only. Archive playback uses chapter VOD
// artifacts produced by the chapter finalization job.
