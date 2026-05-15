package leases

import (
	"strings"
	"sync"
	"time"
)

// SourceEntry records the local path Mist was handed for a given playback
// stream name. STREAM_SOURCE writes; STREAM_END (or reconciliation) forgets.
// The entry is the authoritative anchor for what's actually open on disk,
// independent of the artifact index scan that may not have caught up yet.
type SourceEntry struct {
	StreamName   string
	LocalPath    string
	AssetType    string // "vod" | "dvr"
	InternalName string // vod+: result of mist.ExtractInternalName; NOT artifact_hash
	ChapterID    string // dvr only
	DvrHash      string // dvr only; derived from chapter registry or path layout
	Recorded     time.Time
}

type SourceRegistry struct {
	mu      sync.RWMutex
	entries map[string]*SourceEntry
}

func NewSourceRegistry() *SourceRegistry {
	return &SourceRegistry{entries: make(map[string]*SourceEntry)}
}

func (r *SourceRegistry) Record(e SourceEntry) {
	if r == nil || e.StreamName == "" {
		return
	}
	e.Recorded = time.Now()
	r.mu.Lock()
	defer r.mu.Unlock()
	r.entries[e.StreamName] = &e
}

func (r *SourceRegistry) Lookup(streamName string) (SourceEntry, bool) {
	if r == nil || streamName == "" {
		return SourceEntry{}, false
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	e, ok := r.entries[streamName]
	if !ok {
		return SourceEntry{}, false
	}
	return *e, true
}

func (r *SourceRegistry) Forget(streamName string) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.entries, streamName)
}

// IsLocalFilesystemResponse filters STREAM_SOURCE responses we care about.
// Mist returns a wide variety: absolute file paths, balance:<...>, HTTP URLs,
// presigned S3 URLs, etc. Only absolute local paths should produce a source
// lease — everything else is a redirect or an external source.
func IsLocalFilesystemResponse(response string) bool {
	if response == "" {
		return false
	}
	if !strings.HasPrefix(response, "/") {
		return false
	}
	// Reject anything that smells like a non-path indirection.
	if strings.HasPrefix(response, "//") {
		return false
	}
	lower := strings.ToLower(response)
	for _, scheme := range []string{"http:", "https:", "s3:", "balance:", "rtmp:", "rtsp:", "srt:"} {
		if strings.HasPrefix(lower, scheme) {
			return false
		}
	}
	return true
}
