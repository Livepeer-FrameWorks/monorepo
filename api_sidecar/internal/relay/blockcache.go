package relay

// Per-asset fixed-size block cache for cold VOD/clip/upload byte-range
// playback. The relay's only persistent cache layer for single-file
// artifacts: hot artifacts have their canonical full file on disk
// (written by processing/clip-create/explicit defrost), cold artifacts
// live in their .blocks/ sidecar directory until enough blocks
// accumulate that pressure-driven cleanup or operator action removes
// them.
//
// Layout under the asset's canonical local path:
//
//	<asset>                     canonical full file (warm wins)
//	<asset>.blocks/             cold-fetch cache dir
//	  meta.json                 source identity (asset_hash, total_size, block_size)
//	  0000000000.blk            complete block 0
//	  0000000001.blk            complete block 1
//	  0000000002.blk.tmp        in-flight block 2 (deleted on restart)
//
// Restart recovery is just: open meta.json, drop *.blk.tmp. If meta is
// missing/unreadable or asset_hash doesn't match the relay's current
// resolve, the entire dir is dropped — content-addressed artifact hash
// is the source-of-truth identity (same hash → same bytes), so a meta
// mismatch means the asset was reprocessed/replaced.

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
)

// DefaultBlockSize is the fixed VOD block-cache granularity. 32 MiB
// balances S3 request count, file-count overhead, and overfetch on
// random seeks. At common rates: ~13s of media for 20 Mbps 4K and
// ~50s for 5 Mbps 1080p — comfortably smaller than Mist's 120s VOD
// page window so a single block fill covers a full input refresh
// without forcing a refetch, while keeping fan-out overfetch bounded
// for random seeks.
const DefaultBlockSize int64 = 32 * 1024 * 1024

// BlockMeta is persisted to <asset>.blocks/meta.json. AssetHash is the
// content-addressed identity check on restart: a different hash means
// the asset was reprocessed and the cache is stale.
type BlockMeta struct {
	AssetHash string `json:"asset_hash"`
	TotalSize int64  `json:"total_size"`
	BlockSize int64  `json:"block_size"`
	Ext       string `json:"ext,omitempty"`
}

// BlockStore is the per-asset block cache. Construct via NewBlockStore
// with the canonical local path of the asset (NOT the .blocks/ dir);
// the store appends the .blocks suffix internally so callers don't
// re-derive it.
type BlockStore struct {
	assetPath string // <storagePath>/vod/<hash>.<ext>
	blockSize int64
}

// NewBlockStore returns a store rooted at <assetPath>.blocks/.
// blockSize<=0 uses DefaultBlockSize.
func NewBlockStore(assetPath string, blockSize int64) *BlockStore {
	if blockSize <= 0 {
		blockSize = DefaultBlockSize
	}
	return &BlockStore{assetPath: assetPath, blockSize: blockSize}
}

// Dir returns the .blocks/ directory path. The store does not create
// it until the first write attempt.
func (s *BlockStore) Dir() string { return s.assetPath + ".blocks" }

// MetaPath returns the meta.json path.
func (s *BlockStore) MetaPath() string { return filepath.Join(s.Dir(), "meta.json") }

// BlockPath returns the on-disk path of a completed block file.
func (s *BlockStore) BlockPath(idx int64) string {
	return filepath.Join(s.Dir(), fmt.Sprintf("%010d.blk", idx))
}

func (s *BlockStore) tmpPath(idx int64) string {
	return s.BlockPath(idx) + ".tmp"
}

// BlockSize returns the configured block size in bytes.
func (s *BlockStore) BlockSize() int64 { return s.blockSize }

// BlockRange returns the [start, end] byte offsets of block idx in the
// full asset, clamped to totalSize-1.
func (s *BlockStore) BlockRange(idx, totalSize int64) (int64, int64) {
	start := idx * s.blockSize
	end := start + s.blockSize - 1
	if end > totalSize-1 {
		end = totalSize - 1
	}
	return start, end
}

// LoadMeta reads meta.json. Returns os.ErrNotExist when the dir is
// fresh (no cache yet for this asset).
func (s *BlockStore) LoadMeta() (*BlockMeta, error) {
	b, err := os.ReadFile(s.MetaPath())
	if err != nil {
		return nil, err
	}
	var m BlockMeta
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, fmt.Errorf("blockcache: invalid meta.json: %w", err)
	}
	return &m, nil
}

// SaveMeta writes meta.json atomically (tmpfile + rename) so a partial
// write on restart leaves the previous version intact.
func (s *BlockStore) SaveMeta(m *BlockMeta) error {
	if err := os.MkdirAll(s.Dir(), 0o755); err != nil {
		return err
	}
	b, err := json.Marshal(m)
	if err != nil {
		return err
	}
	tmp := s.MetaPath() + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, s.MetaPath())
}

// EnsureMeta validates an existing meta against the given asset hash +
// total size and rebuilds the dir from scratch when they don't match
// (asset reprocessed → all blocks stale). Creates a fresh meta when
// the dir is empty or unreadable.
func (s *BlockStore) EnsureMeta(assetHash, ext string, totalSize int64) (*BlockMeta, error) {
	m, err := s.LoadMeta()
	if err == nil && m.AssetHash == assetHash && m.TotalSize == totalSize && m.BlockSize == s.blockSize {
		return m, nil
	}
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		// Unreadable meta — wipe and rebuild rather than refuse the cache.
		_ = os.RemoveAll(s.Dir())
	} else if err == nil {
		// Identity mismatch — drop the stale cache entirely.
		_ = os.RemoveAll(s.Dir())
	}
	fresh := &BlockMeta{
		AssetHash: assetHash,
		TotalSize: totalSize,
		BlockSize: s.blockSize,
		Ext:       ext,
	}
	if err := s.SaveMeta(fresh); err != nil {
		return nil, err
	}
	return fresh, nil
}

// HasBlock reports whether block idx is complete on disk.
func (s *BlockStore) HasBlock(idx int64) bool {
	info, err := os.Stat(s.BlockPath(idx))
	return err == nil && info.Mode().IsRegular()
}

// ReadBlock opens a complete block for reading. Caller closes.
func (s *BlockStore) ReadBlock(idx int64) (io.ReadSeekCloser, error) {
	return os.Open(s.BlockPath(idx))
}

// WriteBlock writes data to <idx>.blk.tmp, fsync+close, then atomic
// renames into place. On any error the tmpfile is cleaned up.
func (s *BlockStore) WriteBlock(idx int64, data []byte) error {
	if err := os.MkdirAll(s.Dir(), 0o755); err != nil {
		return err
	}
	tmp := s.tmpPath(idx)
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, s.BlockPath(idx))
}

// CleanTmps removes any stale *.blk.tmp from a crash or restart. Safe
// to call on a non-existent dir.
func (s *BlockStore) CleanTmps() {
	entries, err := os.ReadDir(s.Dir())
	if err != nil {
		return
	}
	for _, e := range entries {
		name := e.Name()
		if filepath.Ext(name) == ".tmp" {
			_ = os.Remove(filepath.Join(s.Dir(), name))
		}
	}
}

// IsComplete reports whether every block in [0, lastBlock] is on disk.
func (s *BlockStore) IsComplete(totalSize int64) bool {
	if totalSize <= 0 {
		return false
	}
	last := lastBlockIndex(totalSize, s.blockSize)
	for i := int64(0); i <= last; i++ {
		if !s.HasBlock(i) {
			return false
		}
	}
	return true
}

// blockIndexForByte returns the block containing the given byte offset.
func blockIndexForByte(offset, blockSize int64) int64 {
	return offset / blockSize
}

// lastBlockIndex returns the index of the final block in a totalSize-byte asset.
func lastBlockIndex(totalSize, blockSize int64) int64 {
	if totalSize <= 0 {
		return 0
	}
	return (totalSize - 1) / blockSize
}

// blockSpan is one chunk of a Range request that lives within a single
// block. Concatenated spans cover the whole request range.
type blockSpan struct {
	Idx        int64
	BlockStart int64 // absolute byte where the block starts
	From       int64 // byte offset WITHIN the block to start at
	To         int64 // byte offset WITHIN the block (inclusive) to end at
}

// spansForRange splits the requested [start,end] byte range into
// per-block spans. end is inclusive (HTTP Range semantics).
func spansForRange(start, end, blockSize int64) []blockSpan {
	if end < start {
		return nil
	}
	out := []blockSpan{}
	firstIdx := blockIndexForByte(start, blockSize)
	lastIdx := blockIndexForByte(end, blockSize)
	for idx := firstIdx; idx <= lastIdx; idx++ {
		blockStart := idx * blockSize
		from := int64(0)
		if idx == firstIdx {
			from = start - blockStart
		}
		to := blockSize - 1
		if idx == lastIdx {
			to = end - blockStart
		}
		out = append(out, blockSpan{Idx: idx, BlockStart: blockStart, From: from, To: to})
	}
	return out
}

// parseRangeHeader returns the inclusive [start,end] byte offsets from
// an HTTP Range header value. Returns ok=false for missing/invalid
// headers or for multi-range requests (not supported). end clamps to
// totalSize-1; start may not exceed end.
func parseRangeHeader(h string, totalSize int64) (start, end int64, ok bool) {
	if h == "" || totalSize <= 0 {
		return 0, 0, false
	}
	const prefix = "bytes="
	if len(h) <= len(prefix) || h[:len(prefix)] != prefix {
		return 0, 0, false
	}
	spec := h[len(prefix):]
	// Multi-range (comma-separated) not supported.
	for i := 0; i < len(spec); i++ {
		if spec[i] == ',' {
			return 0, 0, false
		}
	}
	dash := -1
	for i := 0; i < len(spec); i++ {
		if spec[i] == '-' {
			dash = i
			break
		}
	}
	if dash < 0 {
		return 0, 0, false
	}
	s := spec[:dash]
	e := spec[dash+1:]
	switch {
	case s == "" && e == "":
		return 0, 0, false
	case s == "":
		// suffix range: last N bytes
		n, err := strconv.ParseInt(e, 10, 64)
		if err != nil || n <= 0 {
			return 0, 0, false
		}
		if n > totalSize {
			n = totalSize
		}
		return totalSize - n, totalSize - 1, true
	case e == "":
		start, err := strconv.ParseInt(s, 10, 64)
		if err != nil || start < 0 || start >= totalSize {
			return 0, 0, false
		}
		return start, totalSize - 1, true
	default:
		start, err := strconv.ParseInt(s, 10, 64)
		if err != nil || start < 0 {
			return 0, 0, false
		}
		end, err := strconv.ParseInt(e, 10, 64)
		if err != nil || end < start {
			return 0, 0, false
		}
		if end > totalSize-1 {
			end = totalSize - 1
		}
		return start, end, true
	}
}
