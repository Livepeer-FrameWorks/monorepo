package leases

import (
	"bufio"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// ChapterEntry maps a DVR chapter playback ID (the suffix of dvr+<chapter_id>)
// to the data Helmsman needs to acquire a lease: the DVR hash, the chapter's
// segment list, and the chapter manifest path.
type ChapterEntry struct {
	ChapterID    string
	DvrHash      string
	SegmentNames []string
	ManifestPath string
}

type ChapterRegistry struct {
	mu      sync.RWMutex
	entries map[string]*ChapterEntry
}

func NewChapterRegistry() *ChapterRegistry {
	return &ChapterRegistry{entries: make(map[string]*ChapterEntry)}
}

func (r *ChapterRegistry) Register(e ChapterEntry) {
	if r == nil || e.ChapterID == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	cp := e
	cp.SegmentNames = append([]string(nil), e.SegmentNames...)
	r.entries[e.ChapterID] = &cp
}

func (r *ChapterRegistry) Lookup(chapterID string) (ChapterEntry, bool) {
	if r == nil || chapterID == "" {
		return ChapterEntry{}, false
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	e, ok := r.entries[chapterID]
	if !ok {
		return ChapterEntry{}, false
	}
	cp := *e
	cp.SegmentNames = append([]string(nil), e.SegmentNames...)
	return cp, true
}

func (r *ChapterRegistry) Forget(chapterID string) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.entries, chapterID)
}

// Rehydrate walks {storageRoot}/dvr/*/<dvr_hash>/chapters/*.m3u8, parses each
// chapter manifest to extract its segment names, and repopulates the registry.
// Bounded by on-disk inventory.
func (r *ChapterRegistry) Rehydrate(storageRoot string) error {
	if r == nil {
		return errors.New("chapter registry is nil")
	}
	dvrRoot := filepath.Join(storageRoot, "dvr")
	info, err := os.Stat(dvrRoot)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if !info.IsDir() {
		return nil
	}

	streamEntries, err := os.ReadDir(dvrRoot)
	if err != nil {
		return err
	}
	for _, streamEntry := range streamEntries {
		if !streamEntry.IsDir() {
			continue
		}
		streamDir := filepath.Join(dvrRoot, streamEntry.Name())
		hashEntries, err := os.ReadDir(streamDir)
		if err != nil {
			continue
		}
		for _, hashEntry := range hashEntries {
			if !hashEntry.IsDir() {
				continue
			}
			dvrHash := hashEntry.Name()
			chaptersDir := filepath.Join(streamDir, dvrHash, "chapters")
			manifestEntries, err := os.ReadDir(chaptersDir)
			if err != nil {
				continue
			}
			for _, m := range manifestEntries {
				if m.IsDir() {
					continue
				}
				name := m.Name()
				if !strings.HasSuffix(name, ".m3u8") {
					continue
				}
				chapterID := strings.TrimSuffix(name, ".m3u8")
				manifestPath := filepath.Join(chaptersDir, name)
				segments, perr := parseManifestSegments(manifestPath)
				if perr != nil {
					continue
				}
				r.Register(ChapterEntry{
					ChapterID:    chapterID,
					DvrHash:      dvrHash,
					SegmentNames: segments,
					ManifestPath: manifestPath,
				})
			}
		}
	}
	return nil
}

// DeriveDvrHashFromPath extracts dvr_hash from a chapter manifest path that
// follows .../dvr/<stream>/<dvr_hash>/chapters/<chapter_id>.m3u8. Returns ""
// on shape mismatch.
func DeriveDvrHashFromPath(manifestPath string) string {
	if manifestPath == "" {
		return ""
	}
	chaptersDir := filepath.Dir(manifestPath)
	if filepath.Base(chaptersDir) != "chapters" {
		return ""
	}
	return filepath.Base(filepath.Dir(chaptersDir))
}

// parseManifestSegments returns the segment names referenced in an HLS
// manifest. Non-comment lines that are not gaps are treated as segment URIs
// of the form ../segments/<name>; we strip the directory and keep the basename.
func parseManifestSegments(manifestPath string) ([]string, error) {
	f, err := os.Open(manifestPath)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var names []string
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		base := filepath.Base(line)
		if base == "" || base == "." || base == "/" {
			continue
		}
		names = append(names, base)
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return names, nil
}
