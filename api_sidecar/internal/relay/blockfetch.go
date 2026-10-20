package relay

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
)

// blockFills tracks the cold blocks this process is copying from their
// upstream into the block cache. One fill runs per (asset, block); every
// reader of that block, the one that started the fill included, reads the
// block's tmp file as the fill extends it.
//
// A fill does not belong to any reader. It runs on a context without the
// request's cancellation, so Mist abandoning a read (its 5 s source read
// timeout) does not abort the fill, and the retry that follows reads on from
// the bytes the fill already holds. The fill ends on its own: when the block is
// complete, or when the upstream bounds give it up (each attempt's dial, header
// and read-stall bounds, and maxBlockFetchResumes).
//
// When admission keeps a read off disk (CacheMemoryOnly), the block is filled
// into memory instead (claimMemoryFill), with the same sharing and the same
// independence from readers.
type blockFills struct {
	mu       sync.Mutex
	inflight map[string]*blockFill
	// mem holds the memory fills, running and lingering, at most
	// maxMemoryFills of them.
	mem map[string]*blockFill
	// wg counts the fill goroutines still running.
	wg sync.WaitGroup
}

func newBlockFills() *blockFills {
	return &blockFills{inflight: make(map[string]*blockFill), mem: make(map[string]*blockFill)}
}

// maxMemoryFills bounds the blocks held in memory by memory fills, running or
// lingering. A memory-only read that finds them all running streams its span
// from the upstream without a fill.
const maxMemoryFills = 4

// memoryFillLinger is how long a memory fill's bytes stay after the fill ends,
// so the retry of a reader that gave up before the fill ended, which Mist
// sends right after its source read timeout, still reads them. A fill that
// ended early is resumed from them by the next fill of its block.
const memoryFillLinger = 2 * mistSourceReadTimeout

// running reports whether a fill of the block with key runs.
func (b *blockFills) running(key string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	_, ok := b.inflight[key]
	return ok
}

// blockFetchKey identifies one block of one asset.
func blockFetchKey(store *BlockStore, idx int64) string {
	return fmt.Sprintf("%s|%d", store.Dir(), idx)
}

// blockFill is one block being written to its tmp file, or to mem for a
// memory fill. written counts the bytes of the block, from its first, that are
// written; they never change once written. changed is closed and replaced
// whenever written or done changes.
type blockFill struct {
	tmpPath   string
	blockPath string
	// mem is the whole block of a memory fill; nil for a disk fill.
	mem []byte
	// sourceURL is the upstream URL the fill started from.
	sourceURL string
	// object is the identity of the object the fill copies. A reader of
	// another object does not join the fill.
	object string

	mu      sync.Mutex
	written int64
	done    bool
	err     error
	changed chan struct{}
}

// fillState is a snapshot of a fill for one reader.
type fillState struct {
	written int64
	done    bool
	err     error
	changed <-chan struct{}
}

func (f *blockFill) state() fillState {
	f.mu.Lock()
	defer f.mu.Unlock()
	return fillState{written: f.written, done: f.done, err: f.err, changed: f.changed}
}

func (f *blockFill) advance(n int64) {
	f.mu.Lock()
	f.written += n
	close(f.changed)
	f.changed = make(chan struct{})
	f.mu.Unlock()
}

func (f *blockFill) finish(err error) {
	f.mu.Lock()
	f.done, f.err = true, err
	close(f.changed)
	f.changed = make(chan struct{})
	f.mu.Unlock()
}

// fillReader reads the bytes a fill has written.
type fillReader interface {
	io.ReaderAt
	io.Closer
}

// memFillReader reads a memory fill's block. The bytes below the fill's
// written count never change, so readers need no lock.
type memFillReader struct{ *bytes.Reader }

func (memFillReader) Close() error { return nil }

// open opens the fill's bytes for reading: a memory fill's block; else the
// tmp file while the fill writes it, the block once the fill renamed it. A
// file that is neither (the fill removed it after a disk failure, or the block
// was evicted) is a fillDiskError.
func (f *blockFill) open() (fillReader, error) {
	if f.mem != nil {
		return memFillReader{bytes.NewReader(f.mem)}, nil
	}
	file, err := os.Open(f.tmpPath)
	if err == nil {
		return file, nil
	}
	if file, blockErr := os.Open(f.blockPath); blockErr == nil {
		return file, nil
	}
	return nil, fillDiskError{err: err}
}

// fillDiskError is a fill that could not write the block cache. The upstream
// bytes were fine; a playback reader continues its span straight from the
// upstream.
type fillDiskError struct{ err error }

func (e fillDiskError) Error() string { return "block cache write: " + e.err.Error() }

func (e fillDiskError) Unwrap() error { return e.err }

// fillSource is what a new fill fetches from and reports to.
type fillSource struct {
	target upstreamTarget
	// object is the identity of the object target reads (objectIdentity).
	object string
	// renew replaces target when a mid-block resume is refused with 401/403.
	renew   func() (upstreamTarget, bool)
	defrost func(int64)
}

// claimBlockFill returns the fill of span's block, starting it when none
// runs. A nil fill with a nil error means the block is already warm. A tmp
// file left by an earlier fill of this process that ended early holds the
// block's first bytes; the new fill keeps them and requests only the rest.
func (s *Server) claimBlockFill(ctx context.Context, store *BlockStore, span blockSpan, totalSize int64, src fillSource) (*blockFill, error) {
	key := blockFetchKey(store, span.Idx)
	s.fills.mu.Lock()
	defer s.fills.mu.Unlock()
	if f, ok := s.fills.inflight[key]; ok {
		if !sameObject(f.object, src.object) {
			return nil, fillDiskError{err: errors.New("block fill copies another object")}
		}
		coldfetchCoalesced.WithLabelValues("follower").Inc()
		return f, nil
	}
	if store.HasBlock(span.Idx) {
		return nil, nil
	}
	if err := os.MkdirAll(store.Dir(), 0o755); err != nil {
		return nil, fillDiskError{err: err}
	}
	tmpPath := store.BlockPath(span.Idx) + ".tmp"
	file, err := os.OpenFile(tmpPath, os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, fillDiskError{err: err}
	}
	blockStart, blockEnd := store.BlockRange(span.Idx, totalSize)
	expected := blockEnd - blockStart + 1
	var have int64
	if info, statErr := file.Stat(); statErr == nil && info.Size() < expected {
		have = info.Size()
	}
	if err := file.Truncate(have); err != nil {
		_ = file.Close()
		return nil, fillDiskError{err: err}
	}
	if _, err := file.Seek(have, io.SeekStart); err != nil {
		_ = file.Close()
		return nil, fillDiskError{err: err}
	}
	f := &blockFill{tmpPath: tmpPath, blockPath: store.BlockPath(span.Idx), sourceURL: src.target.url, object: src.object, written: have, changed: make(chan struct{})}
	s.fills.inflight[key] = f
	coldfetchCoalesced.WithLabelValues("leader").Inc()
	s.fills.wg.Add(1)
	go func() {
		defer s.fills.wg.Done()
		err := s.runBlockFill(context.WithoutCancel(ctx), f, file, span.Idx, blockStart, blockEnd, totalSize, have, src)
		s.fills.mu.Lock()
		delete(s.fills.inflight, key)
		s.fills.mu.Unlock()
		f.finish(err)
	}()
	return f, nil
}

// claimMemoryFill returns the fill of span's block for a read admission keeps
// off disk, starting a memory fill when none runs: a disk fill of the block
// already running, else the block's memory fill, running or lingering. A
// lingering memory fill that ended early holds the block's first bytes; the
// new fill keeps them and requests only the rest. A nil fill means
// maxMemoryFills fills are running and the read streams without one.
func (s *Server) claimMemoryFill(ctx context.Context, store *BlockStore, span blockSpan, totalSize int64, src fillSource) *blockFill {
	key := blockFetchKey(store, span.Idx)
	blockStart, blockEnd := store.BlockRange(span.Idx, totalSize)
	expected := blockEnd - blockStart + 1
	s.fills.mu.Lock()
	defer s.fills.mu.Unlock()
	if f, ok := s.fills.inflight[key]; ok {
		if !sameObject(f.object, src.object) {
			return nil
		}
		coldfetchCoalesced.WithLabelValues("follower").Inc()
		return f
	}
	var have int64
	var mem []byte
	if prev, ok := s.fills.mem[key]; ok {
		st := prev.state()
		sameSource := sameObject(prev.object, src.object)
		if sameSource && (!st.done || st.err == nil) {
			coldfetchCoalesced.WithLabelValues("follower").Inc()
			return prev
		}
		// A running fill of another object keeps its block for its own
		// readers; this read streams without a fill.
		if !st.done {
			return nil
		}
		delete(s.fills.mem, key)
		if sameSource && int64(len(prev.mem)) == expected {
			mem, have = prev.mem, st.written
		}
	}
	if len(s.fills.mem) >= maxMemoryFills && !s.dropEndedMemoryFillLocked() {
		return nil
	}
	if mem == nil {
		mem = make([]byte, expected)
	}
	f := &blockFill{mem: mem, sourceURL: src.target.url, object: src.object, written: have, changed: make(chan struct{})}
	s.fills.mem[key] = f
	coldfetchCoalesced.WithLabelValues("leader").Inc()
	s.fills.wg.Add(1)
	go func() {
		defer s.fills.wg.Done()
		err := s.runMemoryFill(context.WithoutCancel(ctx), f, span.Idx, blockStart, blockEnd, totalSize, have, src)
		f.finish(err)
		time.AfterFunc(memoryFillLinger, func() {
			s.fills.mu.Lock()
			defer s.fills.mu.Unlock()
			if s.fills.mem[key] == f {
				delete(s.fills.mem, key)
			}
		})
	}()
	return f
}

// dropEndedMemoryFillLocked removes one lingering memory fill to make room for
// a new one. It reports false when every memory fill is still running.
func (s *Server) dropEndedMemoryFillLocked() bool {
	for key, f := range s.fills.mem {
		if f.state().done {
			delete(s.fills.mem, key)
			return true
		}
	}
	return false
}

// runMemoryFill copies bytes [blockStart+have, blockEnd] of the upstream into
// f.mem. Readers that already hold the bytes keep reading them after the fill
// leaves blockFills.
func (s *Server) runMemoryFill(ctx context.Context, f *blockFill, blockIdx, blockStart, blockEnd, totalSize, have int64, src fillSource) error {
	source := upstreamSourceLabel(src.target.grantID)
	n, err := s.fetchBlockInto(ctx, &memFillWriter{fill: f, off: have}, blockIdx, blockStart, blockEnd, totalSize, have, src)
	if err != nil {
		defrostBlocks.WithLabelValues(source, "error").Inc()
		return err
	}
	s.recordBlockFilled(source, src, n-have)
	return nil
}

// runBlockFill copies bytes [blockStart+have, blockEnd] of the upstream into
// file and renames it to the block's path once the block is complete. When
// the fill ends early on an upstream failure the tmp file keeps the bytes
// written so far; a disk failure removes it.
func (s *Server) runBlockFill(ctx context.Context, f *blockFill, file *os.File, blockIdx, blockStart, blockEnd, totalSize, have int64, src fillSource) error {
	source := upstreamSourceLabel(src.target.grantID)
	writer := &fillWriter{file: file, fill: f}
	n, err := s.fetchBlockInto(ctx, writer, blockIdx, blockStart, blockEnd, totalSize, have, src)
	if writer.err != nil {
		_ = file.Close()
		_ = os.Remove(f.tmpPath)
		defrostBlocks.WithLabelValues(source, "error").Inc()
		return fillDiskError{err: writer.err}
	}
	if err != nil {
		_ = file.Close()
		defrostBlocks.WithLabelValues(source, "error").Inc()
		return err
	}
	syncErr := file.Sync()
	closeErr := file.Close()
	if err := errors.Join(syncErr, closeErr); err != nil {
		_ = os.Remove(f.tmpPath)
		return fillDiskError{err: err}
	}
	if err := os.Rename(f.tmpPath, f.blockPath); err != nil {
		_ = os.Remove(f.tmpPath)
		return fillDiskError{err: err}
	}
	s.recordBlockFilled(source, src, n-have)
	return nil
}

// recordBlockFilled counts a completed fill's upstream bytes.
func (s *Server) recordBlockFilled(source string, src fillSource, fetched int64) {
	defrostBlocks.WithLabelValues(source, "success").Inc()
	defrostBytes.WithLabelValues(source).Add(float64(fetched))
	// Only cold S3 read-through (not peer relay) feeds the per-asset defrost
	// aggregator: peer reads are cross-cluster, not a frozen-artifact cost.
	if src.target.grantID == "" && src.defrost != nil {
		src.defrost(fetched)
	}
}

// fetchBlockInto copies bytes [blockStart+have, blockEnd] of the upstream to
// writer and returns the count of the block's bytes written, have included. An
// upstream body that fails mid-block is requested again from the first byte
// not yet written, up to maxBlockFetchResumes times. A writer failure ends the
// copy; the writer keeps that error.
func (s *Server) fetchBlockInto(ctx context.Context, writer io.Writer, blockIdx, blockStart, blockEnd, totalSize, have int64, src fillSource) (int64, error) {
	target := src.target
	source := upstreamSourceLabel(target.grantID)
	expected := blockEnd - blockStart + 1

	fetchStart := time.Now()
	req, resp, err := requestUpstreamRange(ctx, target.client, target.url, target.grantID, blockStart+have, blockEnd)
	if err != nil {
		return have, upstreamPhaseError{op: "upstream block fetch", err: err}
	}
	headersAt := time.Now()
	defrostTTFB.WithLabelValues(source).Observe(headersAt.Sub(fetchStart).Seconds())
	if cr := resp.Header.Get("Content-Range"); have > 0 && resp.StatusCode == http.StatusPartialContent && !strings.HasPrefix(cr, fmt.Sprintf("bytes %d-", blockStart+have)) {
		_ = resp.Body.Close()
		return have, upstreamPhaseError{op: "upstream block fetch", err: fmt.Errorf("upstream answered Content-Range %q, want start %d", cr, blockStart+have)}
	}
	if resp.StatusCode != http.StatusPartialContent {
		// 200 OK is acceptable only for a request from the asset's first byte
		// to its last; otherwise the upstream ignored Range and would fill the
		// block with bytes from the start of the object.
		if resp.StatusCode != http.StatusOK || blockStart+have != 0 || blockEnd != totalSize-1 {
			_ = resp.Body.Close()
			return have, upstreamStatusError{StatusCode: resp.StatusCode}
		}
	}

	copyBuf := make([]byte, 256*1024)
	var body io.Reader = &firstByteReader{r: resp.Body, onFirst: func() {
		s.observeFirstBodyByte(req, source, blockIdx, fetchStart, headersAt)
	}}
	n := have
	attemptStart := have
	var copyErr error
	for resumes := 0; ; resumes++ {
		upstream := &upstreamReader{r: body}
		copied, err := io.CopyBuffer(writer, io.LimitReader(upstream, expected-n), copyBuf)
		n += copied
		copyErr = err
		if err == nil || upstream.err == nil || resumes >= maxBlockFetchResumes {
			break
		}
		_ = resp.Body.Close()
		defrostBlockResumes.WithLabelValues(source).Inc()
		if s.logger != nil {
			s.logger.WithError(err).WithFields(logging.Fields{
				"upstream_host": req.URL.Host,
				"source":        source,
				"block_idx":     blockIdx,
				"block_offset":  n,
			}).Warn("blockcache: upstream body failed mid-block; resuming from the last written byte")
		}
		renew := src.renew
		src.renew = nil
		next, resumeErr := resumeUpstreamRange(ctx, &target, renew, blockStart+n, blockEnd)
		if resumeErr != nil {
			copyErr = fmt.Errorf("%w; resume at block offset %d: %s", err, n, resumeErr.Error())
			break
		}
		resp, body, attemptStart = next, next.Body, n
	}
	_ = resp.Body.Close()

	if copyErr != nil {
		return n, upstreamPhaseError{op: "upstream block copy", err: copyErr}
	}
	if n != expected {
		if upstreamDeclaredShort(resp, expected-attemptStart, totalSize) {
			return n, upstreamPhaseError{op: "upstream block copy", err: upstreamShortSourceError{blockIdx: blockIdx, got: n, expected: expected}}
		}
		return n, upstreamPhaseError{op: "upstream block copy", err: fmt.Errorf("block %d short: copied %d bytes, expected %d", blockIdx, n, expected)}
	}
	return n, nil
}

// fillWriter writes upstream bytes to the fill's tmp file and publishes each
// write to the fill's readers. A failed write is kept in err, which tells it
// apart from an upstream read failure in the copy that feeds it.
type fillWriter struct {
	file *os.File
	fill *blockFill
	err  error
}

func (w *fillWriter) Write(p []byte) (int, error) {
	n, err := w.file.Write(p)
	if n > 0 {
		w.fill.advance(int64(n))
	}
	if err != nil {
		w.err = err
	}
	return n, err
}

// memFillWriter writes upstream bytes into a memory fill's block from off and
// publishes each write to the fill's readers. The copy into it is limited to
// the block, so a write never runs past the block's end.
type memFillWriter struct {
	fill *blockFill
	off  int64
}

func (w *memFillWriter) Write(p []byte) (int, error) {
	n := copy(w.fill.mem[w.off:], p)
	w.off += int64(n)
	if n > 0 {
		w.fill.advance(int64(n))
	}
	if n < len(p) {
		return n, io.ErrShortWrite
	}
	return n, nil
}
