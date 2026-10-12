package relay

// Block-aware cold-fetch + serve. The relay's only persistent caching
// path for single-file artifacts: each missing block is fetched from
// S3 with a block-aligned Range GET, streamed to the client through a
// range-clamped writer, and tee'd to a tmpfile that becomes the
// canonical .blk on full successful download. Disk side is best-
// effort; client side never waits on it.
//
// Concurrency: same-block cold fan-out coalesces via
// blockFetchCoalescer when admission grants CacheToDisk — the first
// viewer (leader) runs the S3 fetch + disk write + its own client
// stream, late viewers wait briefly for the leader and then serve
// from the warm block. Memory-only viewers bypass the coalescer
// (no shared warm file to wait for) and each issue their own S3
// range fetch.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	"github.com/gin-gonic/gin"

	"frameworks/api_sidecar/internal/admission"
)

// mistSourceReadTimeout is how long Mist waits for data on an HTTP source
// read before it abandons the read and reconnects.
const mistSourceReadTimeout = 5 * time.Second

// slowFirstBodyByteAfter is the first-body-byte latency on a cold block fetch
// that gets a warning. At half of Mist's read budget the upstream alone has
// spent enough of it that Mist is at risk of abandoning the read.
var slowFirstBodyByteAfter = mistSourceReadTimeout / 2

// serveViaBlockCache is the relay's random-access entrypoint for single-file
// artifacts. The block cache needs a total size; when Foghorn did not provide
// one, the relay probes byte 0 and uses Content-Range as the source of truth.
//
// Every upstream fetch is its own request, so an upstream that refuses res's
// credential (401/403: an expired presigned URL, a dead peer grant) is retried
// once with the resolution renew supplies, including mid-response: a read that
// outlives its presigned URL continues on the renewed one. A second refusal
// before any fetch succeeds on the renewed resolution ends the request.
func (s *Server) serveViaBlockCache(c *gin.Context, kind, hash, ext, localPath string, res *ResolveResult, intent admission.StorageIntent, renew func(*ResolveResult) (*ResolveResult, bool)) string {
	strictCache := intent == admission.IntentProcessingInput
	// A processing input whose upstream fails before Mist got a byte may keep
	// its input from booting; the processing job reads this note to tell a
	// storage stall from a bad source.
	noteUpstreamFailure := func(err error) {
		if strictCache {
			noteProcessingInputFailure(hash, upstreamFailureCause(c.Request.Context(), err))
		}
	}
	upstreamURL := res.UpstreamURL()
	upstreamClient := s.upstreamClient(res)
	renewedSinceProgress := false
	renewAfter := func(err error) bool {
		if renew == nil || renewedSinceProgress || !isUpstreamAuthError(err) {
			return false
		}
		fresh, ok := renew(res)
		if !ok {
			return false
		}
		res, upstreamURL, upstreamClient = fresh, fresh.UpstreamURL(), s.upstreamClient(fresh)
		renewedSinceProgress = true
		return true
	}
	totalSize := int64(res.ExpectedSizeBytes)
	if totalSize <= 0 {
		probedSize, err := s.probeTotalSize(c.Request.Context(), upstreamClient, upstreamURL, res.PeerRelayGrantID)
		if err != nil && renewAfter(err) {
			probedSize, err = s.probeTotalSize(c.Request.Context(), upstreamClient, upstreamURL, res.PeerRelayGrantID)
		}
		if err != nil {
			noteUpstreamFailure(err)
			s.respondColdFetchError(c, "upstream size probe", err)
			return "error"
		}
		totalSize = probedSize
	}

	// Build a BlockStore handle but defer disk-touching setup (mkdir,
	// EnsureMeta) until after admission.
	store := NewBlockStore(localPath, s.blockSize)
	cacheDecision := admission.CacheMemoryOnly
	if strictCache {
		cacheDecision = admission.CacheToDisk
	}
	if s.admitter != nil {
		dec, admitErr := s.admitter.Decide(c.Request.Context(), store.Dir(), intent, uint64(store.BlockSize()))
		if admitErr != nil && s.logger != nil {
			s.logger.WithError(admitErr).WithField("local_path", localPath).Debug("blockcache: admission errored")
		}
		cacheDecision = dec
	}
	// CacheReject means admission decided this request can't proceed
	// (boot pause, truly full disk). Surface 503 with Retry-After so
	// Mist retries and Foghorn can route elsewhere — silently degrading
	// to S3-memory would hide a real pressure event.
	if cacheDecision == admission.CacheReject {
		c.Writer.Header().Set("Retry-After", "5")
		c.AbortWithStatus(http.StatusServiceUnavailable)
		return "error"
	}
	if strictCache && cacheDecision != admission.CacheToDisk {
		c.Writer.Header().Set("Retry-After", "5")
		c.String(http.StatusServiceUnavailable, "processing input block cache unavailable")
		return "error"
	}
	if strictCache {
		recordProcessingInputSize(hash, totalSize)
	}

	// Distinguish "Range header absent" (full-object 200 OK) from
	// "Range header present but malformed/unsatisfiable" (must be 416,
	// not silent 200).
	rangeHeader := c.Request.Header.Get("Range")
	start, end, hasRange := parseRangeHeader(rangeHeader, totalSize)
	if rangeHeader != "" && !hasRange {
		c.Writer.Header().Set("Content-Range", fmt.Sprintf("bytes */%d", totalSize))
		c.AbortWithStatus(http.StatusRequestedRangeNotSatisfiable)
		return "error"
	}
	if !hasRange {
		start, end = 0, totalSize-1
	}
	if start > totalSize-1 {
		c.Writer.Header().Set("Content-Range", fmt.Sprintf("bytes */%d", totalSize))
		c.AbortWithStatus(http.StatusRequestedRangeNotSatisfiable)
		return "error"
	}

	spans := spansForRange(start, end, store.BlockSize())
	if len(spans) == 0 {
		c.AbortWithStatus(http.StatusRequestedRangeNotSatisfiable)
		return "error"
	}
	recordDefrost := s.defrostRecorderFor(kind, hash)
	if cacheDecision == admission.CacheToDisk && !hasRange {
		err := s.preflightFirstColdSpan(c.Request.Context(), upstreamClient, store, spans[0], totalSize, upstreamURL, res.PeerRelayGrantID)
		if err != nil && renewAfter(err) {
			err = s.preflightFirstColdSpan(c.Request.Context(), upstreamClient, store, spans[0], totalSize, upstreamURL, res.PeerRelayGrantID)
		}
		if err != nil {
			noteUpstreamFailure(err)
			s.cache.Delete(kind, hash)
			s.respondColdFetchError(c, "upstream block preflight", err)
			return "error"
		}
	}
	// Disk-touching block-store setup happens only for CacheToDisk and after
	// the cold preflight. Missing upstream sources should not leave a
	// meta-only .blocks directory behind.
	if cacheDecision == admission.CacheToDisk {
		// Tmp files left by an earlier process are removed once; later
		// ones belong to block fetches of this process that may still be
		// writing them.
		if _, cleaned := s.cleanedBlockDirs.LoadOrStore(store.Dir(), struct{}{}); !cleaned {
			store.CleanTmps()
		}
		if _, err := store.EnsureMeta(hash, ext, totalSize); err != nil {
			if strictCache {
				c.Writer.Header().Set("Retry-After", "5")
				s.serverError(c, "blockcache init", err)
				return "error"
			}
			if s.logger != nil {
				s.logger.WithError(err).WithField("local_path", localPath).Debug("blockcache: EnsureMeta failed; degrading to memory-only")
			}
			cacheDecision = admission.CacheMemoryOnly
		}
	}

	// Headers. 206 for ranged requests, 200 for full asset; Content-Range
	// only on 206. Content-Length is the served byte count, not
	// total_size, when ranged.
	served := end - start + 1
	if res.ContentType != "" {
		c.Writer.Header().Set("Content-Type", res.ContentType)
	}
	c.Writer.Header().Set("Accept-Ranges", "bytes")
	c.Writer.Header().Set("Content-Length", strconv.FormatInt(served, 10))
	if hasRange {
		c.Writer.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, totalSize))
		c.Writer.WriteHeader(http.StatusPartialContent)
	} else {
		c.Writer.WriteHeader(http.StatusOK)
	}

	// A block whose resume is refused with 401/403 after some of its bytes
	// were copied renews here and resumes from the copied offset; the renewed
	// resolution also serves every later span of this response.
	renewMidBlock := func() (upstreamTarget, bool) {
		if renew == nil {
			return upstreamTarget{}, false
		}
		fresh, ok := renew(res)
		if !ok {
			return upstreamTarget{}, false
		}
		res, upstreamURL, upstreamClient = fresh, fresh.UpstreamURL(), s.upstreamClient(fresh)
		renewedSinceProgress = true
		return upstreamTarget{url: upstreamURL, grantID: res.PeerRelayGrantID, client: upstreamClient}, true
	}
	for _, span := range spans {
		// A span fetch refused with 401/403 wrote nothing for that span, so it
		// is sent again on the renewed resolution without a gap in the body.
		err := s.serveBlockSpan(c.Request.Context(), upstreamClient, c.Writer, store, span, totalSize, upstreamURL, res.PeerRelayGrantID, cacheDecision, strictCache, recordDefrost, renewMidBlock)
		if err != nil && renewAfter(err) {
			err = s.serveBlockSpan(c.Request.Context(), upstreamClient, c.Writer, store, span, totalSize, upstreamURL, res.PeerRelayGrantID, cacheDecision, strictCache, recordDefrost, renewMidBlock)
		}
		if err == nil {
			renewedSinceProgress = false
			continue
		}
		// A refusal that renewal did not cure leaves no cached resolution
		// behind, so the next request resolves again.
		if res.renewable() && isUpstreamAuthError(err) {
			s.cache.Delete(kind, hash)
		}
		// Gin defers the status until the first body write. A failed cold
		// fetch must not commit an empty 200/206 with an advertised length.
		if !c.Writer.Written() {
			noteUpstreamFailure(err)
			c.Writer.Header().Del("Content-Length")
			c.Writer.Header().Del("Content-Range")
			s.respondColdFetchError(c, "block span serve", err)
		}
		if s.logger != nil && !isClientGone(err) {
			s.logger.WithError(err).WithField("block_idx", span.Idx).Debug("blockcache: span serve aborted")
		}
		return "error"
	}
	return "served"
}

// serveBlockSpan serves a single block's worth of bytes (clipped to
// [from,to] within the block). Warm path: open the disk block and copy
// the requested slice. Cold path: stream from S3 through a tolerant
// tee — bytes go to the client (range-clamped) AND to a disk tmpfile
// for the cache. First byte to client == first byte from S3; the
// client never waits for the full block to land. If the disk side
// fails (full, slow, permissions), the cache write is abandoned mid-
// stream and the client continues to receive bytes from S3.
//
// Same-block cold-fan-out is coalesced when admission allows
// CacheToDisk: the first viewer (leader) does the S3 fetch + disk
// write + its own client stream; subsequent viewers wait briefly for
// the leader to publish the warm block, then serve directly from
// disk. Without coalescing, N viewers on the same cold block would
// fire N parallel S3 range GETs and N tmpfiles. Memory-only viewers
// bypass the coalescer (no shared warm file to wait for).
func (s *Server) serveBlockSpan(ctx context.Context, httpc *http.Client, w io.Writer, store *BlockStore, span blockSpan, totalSize int64, mediaURL, grantID string, decision admission.CacheDecision, strictCache bool, defrost func(int64), renewMidBlock func() (upstreamTarget, bool)) error {
	if served, err := s.serveWarmBlock(w, store, span); served || err != nil {
		return err
	}
	if decision != admission.CacheToDisk || s.coldFetch == nil {
		return s.streamBlockFromS3(ctx, httpc, w, store, span, totalSize, mediaURL, grantID, decision, strictCache, defrost, nil, renewMidBlock)
	}

	// Only a registered leader writes the block's tmp file, so a late arrival
	// that does not find a warm block after its leader finished claims the
	// block again instead of writing beside another fetch. A late arrival
	// whose leader stopped reading upstream bytes, or a second leader that
	// also left no warm block, streams the block itself without caching it.
	key := blockFetchKey(store, span.Idx)
	for round := 0; ; round++ {
		leader, fetch := s.coldFetch.claim(key)
		if leader {
			coldfetchCoalesced.WithLabelValues("leader").Inc()
			err := s.streamBlockFromS3(ctx, httpc, w, store, span, totalSize, mediaURL, grantID, decision, strictCache, defrost, fetch.noteProgress, renewMidBlock)
			s.coldFetch.finish(key, err == nil && store.HasBlock(span.Idx))
			return err
		}
		coldfetchCoalesced.WithLabelValues("follower").Inc()
		finished, err := s.awaitColdFetch(ctx, fetch)
		if err != nil {
			return err
		}
		if !finished {
			coldfetchCoalesced.WithLabelValues("follower_stalled_leader").Inc()
			return s.streamBlockFromS3(ctx, httpc, w, store, span, totalSize, mediaURL, grantID, admission.CacheMemoryOnly, strictCache, defrost, nil, renewMidBlock)
		}
		if fetch.diskOk {
			if served, err := s.serveWarmBlock(w, store, span); served || err != nil {
				return err
			}
		}
		if round > 0 {
			return s.streamBlockFromS3(ctx, httpc, w, store, span, totalSize, mediaURL, grantID, admission.CacheMemoryOnly, strictCache, defrost, nil, renewMidBlock)
		}
	}
}

// awaitColdFetch waits for fetch's leader to finish. It returns false when
// the leader read no upstream byte for upstreamReadStall: the leader's
// upstream or its own client stalled, and the waiting reader is better served
// by its own fetch than by the leader's remaining stall bound.
func (s *Server) awaitColdFetch(ctx context.Context, fetch *coldFetch) (bool, error) {
	stall := s.upstreamReadStall
	if stall <= 0 {
		stall = relayUpstreamBounds.readStall
	}
	check := time.NewTicker(stall / 4)
	defer check.Stop()
	for {
		select {
		case <-fetch.done:
			return true, nil
		case <-ctx.Done():
			return false, ctx.Err()
		case <-check.C:
			if fetch.idleFor() >= stall {
				return false, nil
			}
		}
	}
}

// serveWarmBlock attempts to serve span from a warm on-disk block.
// Returns (true, nil) when served, (false, nil) when no warm block
// exists, (false, err) on I/O failure. A successful read records a
// HeatTracker touch on the .blocks dir so cleanup/pressure eviction
// see repeated playback as recency, not stale mtime.
func (s *Server) serveWarmBlock(w io.Writer, store *BlockStore, span blockSpan) (bool, error) {
	if !store.HasBlock(span.Idx) {
		return false, nil
	}
	f, err := store.ReadBlock(span.Idx)
	if err != nil {
		return false, fmt.Errorf("open block %d: %w", span.Idx, err)
	}
	defer f.Close()
	if span.From > 0 {
		if _, seekErr := f.Seek(span.From, io.SeekStart); seekErr != nil {
			return false, fmt.Errorf("seek block %d to %d: %w", span.Idx, span.From, seekErr)
		}
	}
	if _, err := io.CopyN(w, f, span.To-span.From+1); err != nil {
		return true, err
	}
	if s.heat != nil {
		s.heat.Touch(store.Dir())
	}
	return true, nil
}

// streamBlockFromS3 issues a block-aligned S3 Range GET and streams
// bytes to the client (range-clamped to the requested span) and,
// opportunistically, to a tmp cache file. The tmp file is fsync+rename
// to the canonical block path only on full successful download. Playback
// callers tolerate disk-side failure and keep serving the client; processing
// input callers use strictCache so a cache failure aborts and Mist retries
// instead of silently losing random-access locality.
//
// Same-block fan-out: callers of streamBlockFromS3 with CacheToDisk
// have already gone through the blockFetchCoalescer in serveBlockSpan,
// so at most one leader reaches here per (asset, block). Memory-only
// viewers bypass the coalescer and each issue their own S3 range
// fetch.
//
// An upstream body that fails mid-block (a read stall, a dropped
// connection) is requested again from the first byte not yet copied, up to
// maxBlockFetchResumes times, and the client and the tmp file continue from
// there without a gap. A resume refused with 401/403 is sent once more on the
// resolution renewMidBlock returns. onProgress, when set, is called on every
// upstream read that returned bytes.
func (s *Server) streamBlockFromS3(ctx context.Context, httpc *http.Client, w io.Writer, store *BlockStore, span blockSpan, totalSize int64, mediaURL, grantID string, decision admission.CacheDecision, strictCache bool, defrost func(int64), onProgress func(), renewMidBlock func() (upstreamTarget, bool)) error {
	blockStart, blockEnd := store.BlockRange(span.Idx, totalSize)
	source := upstreamSourceLabel(grantID)
	fetchStart := time.Now()
	req, resp, err := requestUpstreamRange(ctx, httpc, mediaURL, grantID, blockStart, blockEnd)
	if err != nil {
		defrostBlocks.WithLabelValues(source, "error").Inc()
		return upstreamPhaseError{op: "upstream block fetch", err: err}
	}
	headersAt := time.Now()
	defrostTTFB.WithLabelValues(source).Observe(headersAt.Sub(fetchStart).Seconds())
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusPartialContent {
		// 200 OK is acceptable only when the requested range covers the
		// whole asset — otherwise the upstream ignored Range and would
		// pollute block N with bytes from the start of the object.
		if resp.StatusCode != http.StatusOK || blockStart != 0 || blockEnd != totalSize-1 {
			defrostBlocks.WithLabelValues(source, "error").Inc()
			return upstreamStatusError{StatusCode: resp.StatusCode}
		}
	}

	// Client side: range-clamped writer that only forwards the
	// [span.From, span.To] subrange to the caller's writer. Tracks
	// position relative to the block (offset 0 == blockStart).
	clientSide := newClampedWriter(w, span.From, span.To)

	// Disk side: tmp file we'll rename into the canonical block path
	// on success. Playback can degrade to client-only stream; processing
	// input cannot, because Mist will seek these bytes again.
	var (
		tmpFile *os.File
		tmpPath string
	)
	if decision == admission.CacheToDisk {
		if mkErr := os.MkdirAll(store.Dir(), 0o755); mkErr == nil {
			tmpPath = store.BlockPath(span.Idx) + ".tmp"
			if f, openErr := os.OpenFile(tmpPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644); openErr == nil {
				tmpFile = f
			} else if strictCache {
				return fmt.Errorf("open strict block tmp %d: %w", span.Idx, openErr)
			} else if s.logger != nil {
				s.logger.WithError(openErr).WithField("block_idx", span.Idx).Debug("blockcache: open tmp failed; streaming without cache")
			}
		} else if strictCache {
			return fmt.Errorf("mkdir strict block cache: %w", mkErr)
		}
	}

	var (
		tee    io.Writer = clientSide
		teeTol *tolerantTee
	)
	if tmpFile != nil {
		if strictCache {
			tee = io.MultiWriter(clientSide, tmpFile)
		} else {
			teeTol = newTolerantTee(clientSide, tmpFile, func(diskErr error) {
				if s.logger != nil {
					s.logger.WithError(diskErr).WithField("block_idx", span.Idx).Debug("blockcache: disk write failed or fell behind mid-stream; abandoning cache for this block")
				}
			})
			tee = teeTol
		}
	}

	// 256 KiB copy buffer — sized for syscall economy without holding
	// any meaningful heap. Memory peak per in-flight fetch is this
	// buffer plus the kernel's socket/file buffers plus the
	// tolerantTee channel headroom (~4 buffers).
	copyBuf := make([]byte, 256*1024)
	expected := blockEnd - blockStart + 1
	var body io.Reader = &firstByteReader{r: resp.Body, onFirst: func() {
		s.observeFirstBodyByte(req, source, span.Idx, fetchStart, headersAt)
	}}
	var (
		n              int64
		copyErr        error
		attemptStart   int64
		renewedInBlock bool
		target         = upstreamTarget{url: mediaURL, grantID: grantID, client: httpc}
	)
	for resumes := 0; ; resumes++ {
		upstream := &upstreamReader{r: body, onProgress: onProgress}
		copied, err := io.CopyBuffer(tee, io.LimitReader(upstream, expected-n), copyBuf)
		n += copied
		copyErr = err
		if err == nil || upstream.err == nil || ctx.Err() != nil || resumes >= maxBlockFetchResumes {
			break
		}
		_ = resp.Body.Close()
		defrostBlockResumes.WithLabelValues(source).Inc()
		if s.logger != nil {
			s.logger.WithError(err).WithFields(logging.Fields{
				"upstream_host": req.URL.Host,
				"source":        source,
				"block_idx":     span.Idx,
				"block_offset":  n,
			}).Warn("blockcache: upstream body failed mid-block; resuming from the last copied byte")
		}
		var renew func() (upstreamTarget, bool)
		if !renewedInBlock && renewMidBlock != nil {
			renew = func() (upstreamTarget, bool) {
				renewedInBlock = true
				return renewMidBlock()
			}
		}
		next, resumeErr := resumeUpstreamRange(ctx, &target, renew, blockStart+n, blockEnd)
		if resumeErr != nil {
			// A refused resume is a status error only while no byte of the
			// block was copied: the caller answers an auth refusal by sending
			// the whole span again, which after copied bytes would repeat them.
			var statusErr upstreamStatusError
			if n == 0 && errors.As(resumeErr, &statusErr) {
				copyErr = statusErr
			} else {
				copyErr = fmt.Errorf("%w; resume at block offset %d: %s", err, n, resumeErr.Error())
			}
			break
		}
		resp, body, attemptStart = next, next.Body, n
	}
	if teeTol != nil {
		teeTol.Close() // drain the disk worker so SecondaryAlive reflects final state
	}

	// Disk-side close+rename happens only on a clean full transfer AND
	// when the async disk writer kept up with every chunk. Anything
	// else — short read, copy error, client gone, disk fell behind —
	// means the tmpfile is incomplete and gets removed.
	if tmpFile != nil {
		diskComplete := strictCache || (teeTol != nil && teeTol.SecondaryAlive())
		if copyErr == nil && n == expected && diskComplete {
			syncErr := tmpFile.Sync()
			closeErr := tmpFile.Close()
			if syncErr == nil && closeErr == nil {
				if renameErr := os.Rename(tmpPath, store.BlockPath(span.Idx)); renameErr != nil {
					if s.logger != nil {
						s.logger.WithError(renameErr).WithField("block_idx", span.Idx).Debug("blockcache: rename failed")
					}
					_ = os.Remove(tmpPath)
				}
			} else {
				_ = os.Remove(tmpPath)
			}
		} else {
			_ = tmpFile.Close()
			_ = os.Remove(tmpPath)
		}
	}

	if copyErr != nil {
		defrostBlocks.WithLabelValues(source, "error").Inc()
		return upstreamPhaseError{op: "upstream block copy", err: copyErr}
	}
	if n != expected {
		defrostBlocks.WithLabelValues(source, "error").Inc()
		if upstreamDeclaredShort(resp, expected-attemptStart, totalSize) {
			return upstreamPhaseError{op: "upstream block copy", err: upstreamShortSourceError{blockIdx: span.Idx, got: n, expected: expected}}
		}
		return upstreamPhaseError{op: "upstream block copy", err: fmt.Errorf("block %d short: copied %d bytes, expected %d", span.Idx, n, expected)}
	}
	defrostBlocks.WithLabelValues(source, "success").Inc()
	defrostBytes.WithLabelValues(source).Add(float64(n))
	// Only cold S3 read-through (not peer relay) feeds the per-asset defrost
	// aggregator — peer reads are cross-cluster, not a frozen-artifact cost.
	if grantID == "" && defrost != nil {
		defrost(n)
	}
	return nil
}

// maxBlockFetchResumes bounds how often one block fetch is requested again
// after its upstream body failed, so an upstream that keeps stalling fails
// the fetch within (1+maxBlockFetchResumes) read-stall bounds.
const maxBlockFetchResumes = 2

// requestUpstreamRange sends a GET for bytes [from, to] of mediaURL.
func requestUpstreamRange(ctx context.Context, httpc *http.Client, mediaURL, grantID string, from, to int64) (*http.Request, *http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, mediaURL, nil)
	if err != nil {
		return nil, nil, fmt.Errorf("build upstream block request: %w", err)
	}
	req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", from, to))
	// Peer-relay upstream (origin Helmsman) requires the capability grant id
	// as Authorization: Bearer. Empty for S3 presigned URLs (auth in the URL
	// query string).
	if grantID != "" {
		req.Header.Set("Authorization", "Bearer "+grantID)
	}
	resp, err := httpc.Do(req)
	if err != nil {
		return nil, nil, err
	}
	return req, resp, nil
}

// resumeUpstreamRange requests the rest of a block from byte from. A resume
// refused with 401/403 (the presigned URL expired while the block was in
// flight) is sent once more on the resolution renew returns, which replaces
// target; the request asks only for bytes not yet copied, so nothing repeats.
func resumeUpstreamRange(ctx context.Context, target *upstreamTarget, renew func() (upstreamTarget, bool), from, to int64) (*http.Response, error) {
	resp, err := requestResumeRange(ctx, *target, from, to)
	if err == nil || renew == nil || !isUpstreamAuthError(err) {
		return resp, err
	}
	fresh, ok := renew()
	if !ok {
		return nil, err
	}
	*target = fresh
	return requestResumeRange(ctx, fresh, from, to)
}

// requestResumeRange requests bytes [from, to] of target. Only a 206 whose
// Content-Range starts at from continues the block at the right offset.
func requestResumeRange(ctx context.Context, target upstreamTarget, from, to int64) (*http.Response, error) {
	_, resp, err := requestUpstreamRange(ctx, target.client, target.url, target.grantID, from, to)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusPartialContent {
		_ = resp.Body.Close()
		return nil, upstreamStatusError{StatusCode: resp.StatusCode}
	}
	if cr := resp.Header.Get("Content-Range"); !strings.HasPrefix(cr, fmt.Sprintf("bytes %d-", from)) {
		_ = resp.Body.Close()
		return nil, fmt.Errorf("upstream resume answered Content-Range %q, want start %d", cr, from)
	}
	return resp, nil
}

// upstreamReader records the error of a failed upstream read, which tells an
// upstream failure apart from a client write failure in the copy that
// consumes it, and reports reads that returned bytes to onProgress.
type upstreamReader struct {
	r          io.Reader
	onProgress func()
	err        error
}

func (u *upstreamReader) Read(p []byte) (int, error) {
	n, err := u.r.Read(p)
	if n > 0 && u.onProgress != nil {
		u.onProgress()
	}
	if err != nil && !errors.Is(err, io.EOF) {
		u.err = err
	}
	return n, err
}

// firstByteReader calls onFirst once, when the first non-empty read returns.
type firstByteReader struct {
	r       io.Reader
	onFirst func()
	seen    bool
}

func (f *firstByteReader) Read(p []byte) (int, error) {
	n, err := f.r.Read(p)
	if n > 0 && !f.seen {
		f.seen = true
		f.onFirst()
	}
	return n, err
}

// observeFirstBodyByte records when a cold block fetch's first body byte
// arrived, and warns when it took longer than slowFirstBodyByteAfter. The log
// carries the upstream host only: a presigned URL's query is a credential.
func (s *Server) observeFirstBodyByte(req *http.Request, source string, blockIdx int64, fetchStart, headersAt time.Time) {
	now := time.Now()
	elapsed := now.Sub(fetchStart)
	defrostFirstBodyByte.WithLabelValues(source).Observe(elapsed.Seconds())
	if elapsed < slowFirstBodyByteAfter || s.logger == nil {
		return
	}
	s.logger.WithFields(logging.Fields{
		"upstream_host":      req.URL.Host,
		"range":              req.Header.Get("Range"),
		"source":             source,
		"block_idx":          blockIdx,
		"elapsed_ms":         elapsed.Milliseconds(),
		"headers_to_body_ms": now.Sub(headersAt).Milliseconds(),
		"threshold_ms":       slowFirstBodyByteAfter.Milliseconds(),
	}).Warn("blockcache: upstream first body byte arrived late")
}

// upstreamPhaseError names the cold-fetch phase an error came from; it becomes
// the op of the error response when no byte has reached the client yet.
type upstreamPhaseError struct {
	op  string
	err error
}

func (e upstreamPhaseError) Error() string { return e.op + ": " + e.err.Error() }

func (e upstreamPhaseError) Unwrap() error { return e.err }

// upstreamShortSourceError is a block fetch whose upstream declared fewer bytes
// than the catalog size the block range was cut from: the stored object is
// shorter than its recorded size, and every later read of it is too.
type upstreamShortSourceError struct {
	blockIdx int64
	got      int64
	expected int64
}

func (e upstreamShortSourceError) Error() string {
	return fmt.Sprintf("block %d short: got %d bytes, expected %d: source is shorter than its catalog size", e.blockIdx, e.got, e.expected)
}

// upstreamDeclaredShort reports whether the upstream's own response headers
// state fewer bytes than the block needs: a Content-Range total below the
// catalog size, or a Content-Length below the requested block. A body that ends
// early against headers promising the full block is a broken transfer instead.
func upstreamDeclaredShort(resp *http.Response, expected, totalSize int64) bool {
	if total, ok := totalFromContentRange(resp.Header.Get("Content-Range")); ok && total < totalSize {
		return true
	}
	return resp.ContentLength >= 0 && resp.ContentLength < expected
}

// upstreamDeclaredLength is the byte count the upstream response declares for
// its body: Content-Length, else the Content-Range total.
func upstreamDeclaredLength(resp *http.Response) int64 {
	if resp.ContentLength >= 0 {
		return resp.ContentLength
	}
	total, _ := totalFromContentRange(resp.Header.Get("Content-Range"))
	return total
}

type upstreamStatusError struct {
	StatusCode int
}

func (e upstreamStatusError) Error() string {
	return fmt.Sprintf("upstream artifact fetch returned status %d", e.StatusCode)
}

// isAuthStatus reports an upstream refusing the request's credential.
func isAuthStatus(code int) bool {
	return code == http.StatusUnauthorized || code == http.StatusForbidden
}

// isUpstreamAuthError reports whether err is an upstream 401/403: an expired
// presigned URL or a dead peer-relay grant, which a fresh resolve replaces.
func isUpstreamAuthError(err error) bool {
	var se upstreamStatusError
	return errors.As(err, &se) && isAuthStatus(se.StatusCode)
}

func (s *Server) probeTotalSize(ctx context.Context, httpc *http.Client, mediaURL, grantID string) (int64, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, mediaURL, nil)
	if err != nil {
		return 0, fmt.Errorf("build upstream size probe: %w", err)
	}
	req.Header.Set("Range", "bytes=0-0")
	if grantID != "" {
		req.Header.Set("Authorization", "Bearer "+grantID)
	}
	resp, err := httpc.Do(req)
	if err != nil {
		return 0, fmt.Errorf("upstream size probe: %w", err)
	}
	defer resp.Body.Close()
	if _, err := io.Copy(io.Discard, io.LimitReader(resp.Body, 4*1024)); err != nil {
		return 0, fmt.Errorf("drain upstream size probe: %w", err)
	}
	if resp.StatusCode == http.StatusPartialContent {
		if total, ok := totalFromContentRange(resp.Header.Get("Content-Range")); ok && total > 0 {
			return total, nil
		}
		return 0, fmt.Errorf("upstream size probe missing Content-Range total")
	}
	return 0, upstreamStatusError{StatusCode: resp.StatusCode}
}

func (s *Server) preflightFirstColdSpan(ctx context.Context, httpc *http.Client, store *BlockStore, span blockSpan, totalSize int64, mediaURL, grantID string) error {
	if store.HasBlock(span.Idx) {
		return nil
	}
	blockStart, blockEnd := store.BlockRange(span.Idx, totalSize)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, mediaURL, nil)
	if err != nil {
		return fmt.Errorf("build upstream block preflight request: %w", err)
	}
	req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", blockStart, blockEnd))
	if grantID != "" {
		req.Header.Set("Authorization", "Bearer "+grantID)
	}
	resp, err := httpc.Do(req)
	if err != nil {
		return fmt.Errorf("upstream block preflight fetch: %w", err)
	}
	defer resp.Body.Close()
	if _, err := io.Copy(io.Discard, io.LimitReader(resp.Body, 4*1024)); err != nil {
		return fmt.Errorf("drain upstream block preflight response: %w", err)
	}
	if resp.StatusCode != http.StatusPartialContent && (resp.StatusCode != http.StatusOK || blockStart != 0 || blockEnd != totalSize-1) {
		return upstreamStatusError{StatusCode: resp.StatusCode}
	}
	if expected := blockEnd - blockStart + 1; upstreamDeclaredShort(resp, expected, totalSize) {
		return upstreamShortSourceError{blockIdx: span.Idx, got: upstreamDeclaredLength(resp), expected: expected}
	}
	return nil
}

// respondColdFetchError answers a cold fetch that failed before any body byte
// reached the client. op names the phase that failed; an upstreamPhaseError in
// err names it more precisely and replaces it.
func (s *Server) respondColdFetchError(c *gin.Context, op string, err error) {
	var statusErr upstreamStatusError
	if errors.As(err, &statusErr) {
		switch statusErr.StatusCode {
		case http.StatusNotFound, http.StatusGone:
			c.String(http.StatusNotFound, "source missing")
		case http.StatusRequestedRangeNotSatisfiable:
			c.Writer.Header().Set("Content-Range", "*")
			c.String(http.StatusRequestedRangeNotSatisfiable, "source range unavailable")
		case http.StatusUnauthorized, http.StatusForbidden:
			c.String(http.StatusBadGateway, "source authorization failed")
		default:
			if transientUpstreamStatus(statusErr.StatusCode) {
				c.Writer.Header().Set("Retry-After", "5")
				c.String(http.StatusServiceUnavailable, "source temporarily unavailable: upstream status %d", statusErr.StatusCode)
				return
			}
			c.String(http.StatusBadGateway, "source fetch failed: upstream status %d", statusErr.StatusCode)
		}
		return
	}
	var short upstreamShortSourceError
	if errors.As(err, &short) {
		if s.logger != nil {
			s.logger.WithError(err).WithField("path", c.Request.URL.Path).Warn("relay: source is shorter than its catalog size")
		}
		c.String(http.StatusBadGateway, "source shorter than its catalog size")
		return
	}
	var phaseErr upstreamPhaseError
	if errors.As(err, &phaseErr) {
		op, err = phaseErr.op, phaseErr.err
	}
	s.serverError(c, op, err)
}

func isClientGone(err error) bool {
	if err == nil {
		return false
	}
	// http.ErrAbortHandler, io.ErrShortWrite, and broken-pipe / connection-reset
	// from client disconnects all look the same: not actionable, just log debug.
	msg := err.Error()
	return strings.Contains(msg, "broken pipe") ||
		strings.Contains(msg, "connection reset") ||
		strings.Contains(msg, "context canceled")
}
