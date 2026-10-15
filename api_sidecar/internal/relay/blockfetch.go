package relay

import (
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
type blockFills struct {
	mu       sync.Mutex
	inflight map[string]*blockFill
	// wg counts the fill goroutines still running.
	wg sync.WaitGroup
}

func newBlockFills() *blockFills {
	return &blockFills{inflight: make(map[string]*blockFill)}
}

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

// blockFill is one block being written to its tmp file. written counts the
// bytes of the block, from its first, that are in the tmp file; they never
// change once written. changed is closed and replaced whenever written or done
// changes.
type blockFill struct {
	tmpPath   string
	blockPath string

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

// open opens the fill's file for reading: the tmp file while the fill writes
// it, the block once the fill renamed it. A file that is neither (the fill
// removed it after a disk failure, or the block was evicted) is a
// fillDiskError.
func (f *blockFill) open() (*os.File, error) {
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
	f := &blockFill{tmpPath: tmpPath, blockPath: store.BlockPath(span.Idx), written: have, changed: make(chan struct{})}
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

// runBlockFill copies bytes [blockStart+have, blockEnd] of the upstream into
// file and renames it to the block's path once the block is complete. An
// upstream body that fails mid-block is requested again from the first byte
// not yet written, up to maxBlockFetchResumes times. When the fill ends early
// on an upstream failure the tmp file keeps the bytes written so far; a disk
// failure removes it.
func (s *Server) runBlockFill(ctx context.Context, f *blockFill, file *os.File, blockIdx, blockStart, blockEnd, totalSize, have int64, src fillSource) error {
	target := src.target
	source := upstreamSourceLabel(target.grantID)
	expected := blockEnd - blockStart + 1
	keepPartial := func(err error) error {
		_ = file.Close()
		defrostBlocks.WithLabelValues(source, "error").Inc()
		return err
	}

	fetchStart := time.Now()
	req, resp, err := requestUpstreamRange(ctx, target.client, target.url, target.grantID, blockStart+have, blockEnd)
	if err != nil {
		return keepPartial(upstreamPhaseError{op: "upstream block fetch", err: err})
	}
	headersAt := time.Now()
	defrostTTFB.WithLabelValues(source).Observe(headersAt.Sub(fetchStart).Seconds())
	if cr := resp.Header.Get("Content-Range"); have > 0 && resp.StatusCode == http.StatusPartialContent && !strings.HasPrefix(cr, fmt.Sprintf("bytes %d-", blockStart+have)) {
		_ = resp.Body.Close()
		return keepPartial(upstreamPhaseError{op: "upstream block fetch", err: fmt.Errorf("upstream answered Content-Range %q, want start %d", cr, blockStart+have)})
	}
	if resp.StatusCode != http.StatusPartialContent {
		// 200 OK is acceptable only for a request from the asset's first byte
		// to its last; otherwise the upstream ignored Range and would fill the
		// block with bytes from the start of the object.
		if resp.StatusCode != http.StatusOK || blockStart+have != 0 || blockEnd != totalSize-1 {
			_ = resp.Body.Close()
			return keepPartial(upstreamStatusError{StatusCode: resp.StatusCode})
		}
	}

	writer := &fillWriter{file: file, fill: f}
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

	if writer.err != nil {
		_ = file.Close()
		_ = os.Remove(f.tmpPath)
		defrostBlocks.WithLabelValues(source, "error").Inc()
		return fillDiskError{err: writer.err}
	}
	if copyErr != nil {
		return keepPartial(upstreamPhaseError{op: "upstream block copy", err: copyErr})
	}
	if n != expected {
		if upstreamDeclaredShort(resp, expected-attemptStart, totalSize) {
			return keepPartial(upstreamPhaseError{op: "upstream block copy", err: upstreamShortSourceError{blockIdx: blockIdx, got: n, expected: expected}})
		}
		return keepPartial(upstreamPhaseError{op: "upstream block copy", err: fmt.Errorf("block %d short: copied %d bytes, expected %d", blockIdx, n, expected)})
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
	defrostBlocks.WithLabelValues(source, "success").Inc()
	defrostBytes.WithLabelValues(source).Add(float64(n - have))
	// Only cold S3 read-through (not peer relay) feeds the per-asset defrost
	// aggregator: peer reads are cross-cluster, not a frozen-artifact cost.
	if target.grantID == "" && src.defrost != nil {
		src.defrost(n - have)
	}
	return nil
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
