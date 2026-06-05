// Package admission carries the typed storage-admission contract used
// across Helmsman: handlers/storage_admission.go owns the
// implementation; relay, the processing pipeline, and the unsafe-wrapper
// stager all consume it via the Admitter interface.
//
// The types live in their own package so callers (relay, others) avoid
// an import cycle through the handlers package that owns the engine.
package admission

import "context"

// StorageIntent declares why a caller wants disk room. Constants are ordered
// high → low priority for disk admission: higher-priority intents may evict
// cached material that lower-priority intents wrote.
type StorageIntent string

const (
	// IntentDVRRecording — active DVR write. Must get disk; never
	// memory-only. Recording failure is loud (CacheReject) only when no
	// amount of eviction can free room.
	IntentDVRRecording StorageIntent = "dvr_recording"
	// IntentProcessingOutput — Mist push target for a transcode/remux job.
	// Must get disk; cleanup evicts relay cache first.
	IntentProcessingOutput StorageIntent = "processing_output"
	// IntentProcessingSourceStage — materialized clip source for processing.
	// Disk required; removed after the canonical output is produced or the
	// job fails.
	IntentProcessingSourceStage StorageIntent = "processing_source_stage"
	// IntentDVRChapterFinalization — canonical .mkv produced by the DVR
	// chapter finalization job. Same priority class as
	// IntentProcessingOutput; distinguished only for metrics + operator
	// accounting.
	IntentDVRChapterFinalization StorageIntent = "dvr_chapter_finalization"
	// IntentUnsafeImportStage — local download of an .avi/.flv/.m4v upload
	// because Mist's FLV/AV inputs are local-only. Disk required, but the
	// job is schedulable: admission may reject and let Foghorn retry on
	// another node.
	IntentUnsafeImportStage StorageIntent = "unsafe_import_stage"
	// IntentPlaybackCache — relay write-through cache fill for playback.
	// Opportunistic: CacheToDisk when healthy, CacheMemoryOnly above the
	// critical threshold, CacheReject only during boot pause. First class
	// of bytes evicted when higher-priority intents need room.
	IntentPlaybackCache StorageIntent = "playback_cache"
	// IntentProcessingInput — Mist relay read of a safe-wrapper upload.
	// Mist inputs may seek repeatedly while parsing MP4/MOV, so this must use
	// the relay block cache rather than direct memory-only pass-through.
	IntentProcessingInput StorageIntent = "processing_input"
	// IntentWarmCache — opportunistic background prefetch. Lowest priority;
	// skip under any pressure signal.
	IntentWarmCache StorageIntent = "warm_cache"
)

// CacheDecision is what admission tells the caller to do for this write/read.
type CacheDecision int

const (
	// CacheToDisk — disk has room (after optional cleanup). The caller
	// should write-through to the cache file and update extent metadata.
	CacheToDisk CacheDecision = iota
	// CacheMemoryOnly — disk is pressured. The caller should stream bytes
	// without writing to disk (bounded memory buffers only).
	CacheMemoryOnly
	// CacheReject — admission could not satisfy this intent. The caller
	// should fail typed (relay → 503, disk-write → ErrInsufficientSpace).
	CacheReject
)

// Admitter is the typed admission contract. handlers.StorageManager
// implements it; the relay accepts it as a dependency so disk-write
// decisions for cold fetches go through the same policy as
// processing-output and DVR reservations.
type Admitter interface {
	Decide(ctx context.Context, dir string, intent StorageIntent, sizeBytes uint64) (CacheDecision, error)
}
