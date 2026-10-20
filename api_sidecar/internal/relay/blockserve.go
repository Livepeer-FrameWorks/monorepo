package relay

// Block-aware cold-fetch + serve. The relay's only persistent caching
// path for single-file artifacts: each missing block is fetched from
// S3 with a block-aligned Range GET into a tmpfile that becomes the
// canonical .blk on full successful download, and readers are served
// from that tmpfile as it grows.
//
// Concurrency: when admission grants CacheToDisk, one block fill runs
// per (asset, block) independent of any reader (blockFills); every
// reader of the block, concurrent or retrying, reads the same fill.
// Memory-only viewers share a fill of the block held in memory, within
// maxMemoryFills blocks; past that each issues its own S3 range fetch.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
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
//
// An upstream that answers 404/410 before any body byte reached the client no
// longer holds the resolved object (a superseded upload deleted from storage).
// The resolution is renewed once and the whole request served again from it,
// size included, since the renewed object may be a different one; the request
// answers 404 only when the renewed resolution lacks its object too.
func (s *Server) serveViaBlockCache(c *gin.Context, kind, hash, ext, localPath string, res *ResolveResult, intent admission.StorageIntent, renew func(*ResolveResult) (*ResolveResult, bool)) string {
	status, gone := s.serveBlocksFrom(c, kind, hash, ext, localPath, res, intent, renew)
	if gone == nil {
		return status
	}
	if renew != nil {
		if fresh, ok := renew(gone.res); ok {
			status, gone = s.serveBlocksFrom(c, kind, hash, ext, localPath, fresh, intent, renew)
			if gone == nil {
				return status
			}
		}
	}
	s.cache.Delete(kind, hash, ext)
	if intent == admission.IntentProcessingInput {
		noteProcessingInputFailure(hash, upstreamFailureCause(c.Request.Context(), gone.err))
	}
	return s.respondColdFetchError(c, gone.op, gone.err)
}

// sourceGone is a block-cache serve whose upstream answered 404/410 before any
// body byte reached the client. res is the resolution the upstream answered.
type sourceGone struct {
	op  string
	err error
	res *ResolveResult
}

// serveBlocksFrom serves the request from res through the block cache. It
// answers every outcome itself except an upstream that lost its object before
// the body started, which it returns as sourceGone with nothing written.
func (s *Server) serveBlocksFrom(c *gin.Context, kind, hash, ext, localPath string, res *ResolveResult, intent admission.StorageIntent, renew func(*ResolveResult) (*ResolveResult, bool)) (string, *sourceGone) {
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
	// A processing input whose source answers a status no retry gets past is
	// noted when the shared probe learns it, even after every reader left, so
	// the processing job fails at once instead of booting the input again.
	var probeSettled func(error)
	if strictCache {
		probeSettled = func(err error) { noteProcessingInputUnavailable(hash, err) }
	}
	totalSize := int64(res.ExpectedSizeBytes)
	if totalSize <= 0 {
		probedSize, err := s.sharedProbeTotalSize(c.Request.Context(), upstreamClient, upstreamURL, res.PeerRelayGrantID, probeSettled)
		if err != nil && renewAfter(err) {
			probedSize, err = s.sharedProbeTotalSize(c.Request.Context(), upstreamClient, upstreamURL, res.PeerRelayGrantID, probeSettled)
		}
		if err != nil {
			if isUpstreamMissingError(err) {
				return "", &sourceGone{op: "upstream size probe", err: err, res: res}
			}
			noteUpstreamFailure(err)
			return s.respondColdFetchError(c, "upstream size probe", err), nil
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
		return "error", nil
	}
	if strictCache && cacheDecision != admission.CacheToDisk {
		c.Writer.Header().Set("Retry-After", "5")
		c.String(http.StatusServiceUnavailable, "processing input block cache unavailable")
		return "error", nil
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
		return "error", nil
	}
	if !hasRange {
		start, end = 0, totalSize-1
	}
	if start > totalSize-1 {
		c.Writer.Header().Set("Content-Range", fmt.Sprintf("bytes */%d", totalSize))
		c.AbortWithStatus(http.StatusRequestedRangeNotSatisfiable)
		return "error", nil
	}

	spans := spansForRange(start, end, store.BlockSize())
	if len(spans) == 0 {
		c.AbortWithStatus(http.StatusRequestedRangeNotSatisfiable)
		return "error", nil
	}
	recordDefrost := s.defrostRecorderFor(kind, hash)
	if cacheDecision == admission.CacheToDisk && !hasRange {
		err := s.preflightFirstColdSpan(c.Request.Context(), upstreamClient, store, spans[0], totalSize, upstreamURL, res.PeerRelayGrantID)
		if err != nil && renewAfter(err) {
			err = s.preflightFirstColdSpan(c.Request.Context(), upstreamClient, store, spans[0], totalSize, upstreamURL, res.PeerRelayGrantID)
		}
		if err != nil {
			if isUpstreamMissingError(err) {
				return "", &sourceGone{op: "upstream block preflight", err: err, res: res}
			}
			noteUpstreamFailure(err)
			s.cache.Delete(kind, hash, ext)
			return s.respondColdFetchError(c, "upstream block preflight", err), nil
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
				return "error", nil
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
	// A block fill outlives this request, so it renews a copy of the current
	// resolution and leaves this request's own resolution alone. Its renewal
	// resolves on a context this request ending does not cancel; the control
	// stream bounds each resolve on its own.
	var fillRenew func(*ResolveResult) (*ResolveResult, bool)
	if renew != nil {
		fillRenew = s.resolveRenewer(context.WithoutCancel(c.Request.Context()), kind, hash, ext)
	}
	fillFrom := func() fillSource {
		src := fillSource{
			target:  upstreamTarget{url: upstreamURL, grantID: res.PeerRelayGrantID, client: upstreamClient},
			defrost: recordDefrost,
		}
		if fillRenew != nil {
			fillRes := res
			src.renew = func() (upstreamTarget, bool) {
				fresh, ok := fillRenew(fillRes)
				if !ok {
					return upstreamTarget{}, false
				}
				fillRes = fresh
				return upstreamTarget{url: fresh.UpstreamURL(), grantID: fresh.PeerRelayGrantID, client: s.upstreamClient(fresh)}, true
			}
		}
		return src
	}
	for _, span := range spans {
		// A span fetch refused with 401/403 wrote nothing for that span, so it
		// is sent again on the renewed resolution without a gap in the body.
		err := s.serveBlockSpan(c.Request.Context(), upstreamClient, c.Writer, store, span, totalSize, upstreamURL, res.PeerRelayGrantID, cacheDecision, strictCache, recordDefrost, renewMidBlock, fillFrom)
		if err != nil && renewAfter(err) {
			err = s.serveBlockSpan(c.Request.Context(), upstreamClient, c.Writer, store, span, totalSize, upstreamURL, res.PeerRelayGrantID, cacheDecision, strictCache, recordDefrost, renewMidBlock, fillFrom)
		}
		if err == nil {
			renewedSinceProgress = false
			continue
		}
		// A refusal that renewal did not cure leaves no cached resolution
		// behind, so the next request resolves again.
		if res.renewable() && isUpstreamAuthError(err) {
			s.cache.Delete(kind, hash, ext)
		}
		// Gin defers the status until the first body write. A failed cold
		// fetch must not commit an empty 200/206 with an advertised length.
		if !c.Writer.Written() {
			c.Writer.Header().Del("Content-Length")
			c.Writer.Header().Del("Content-Range")
			if isUpstreamMissingError(err) {
				return "", &sourceGone{op: "block span serve", err: err, res: res}
			}
			noteUpstreamFailure(err)
			return s.respondColdFetchError(c, "block span serve", err), nil
		}
		// An object the upstream lost after the body started leaves no cached
		// resolution behind either.
		if res.renewable() && isUpstreamMissingError(err) {
			s.cache.Delete(kind, hash, ext)
		}
		if readerGone(c) {
			return statusClientGone, nil
		}
		if s.logger != nil && !isClientGone(err) {
			s.logger.WithError(err).WithField("block_idx", span.Idx).Debug("blockcache: span serve aborted")
		}
		return "error", nil
	}
	return "served", nil
}

// serveBlockSpan serves a single block's worth of bytes (clipped to
// [from,to] within the block). Warm path: open the disk block and copy the
// requested slice. Cold path with CacheToDisk: read the block's fill (see
// blockFills) as it lands in the block's tmp file, so the first byte to the
// client follows the first byte from the upstream and every concurrent or
// retrying reader of the block shares one upstream fetch. A playback reader
// whose fill cannot write the cache continues its span straight from the
// upstream; a processing input (strictCache) fails instead, because Mist will
// seek these bytes again. A memory-only read reads the block's memory fill the
// same way, and streams from the upstream without a fill when maxMemoryFills
// fills are running.
func (s *Server) serveBlockSpan(ctx context.Context, httpc *http.Client, w io.Writer, store *BlockStore, span blockSpan, totalSize int64, mediaURL, grantID string, decision admission.CacheDecision, strictCache bool, defrost func(int64), renewMidBlock func() (upstreamTarget, bool), fillFrom func() fillSource) error {
	if served, err := s.serveWarmBlock(w, store, span); served || err != nil {
		return err
	}
	var (
		fill *blockFill
		err  error
		pos  int64
	)
	for attempt := 0; ; attempt++ {
		fill, err = nil, nil
		if decision == admission.CacheToDisk {
			fill, err = s.claimBlockFill(ctx, store, span, totalSize, fillFrom())
			if err == nil && fill == nil {
				// The block turned warm between the first look and the claim.
				if served, warmErr := s.serveWarmBlock(w, store, span); served || warmErr != nil {
					return warmErr
				}
			}
		} else if fill = s.claimMemoryFill(ctx, store, span, totalSize, fillFrom()); fill == nil {
			return s.streamBlockFromS3(ctx, httpc, w, store, span, totalSize, mediaURL, grantID, defrost, renewMidBlock)
		}
		pos = span.From
		if err != nil || fill == nil {
			break
		}
		pos, err = readBlockFill(ctx, w, fill, span)
		if err == nil {
			return nil
		}
		// A shared fill started from another request's resolution (one that
		// was stale or superseded) answers that resolution's upstream status,
		// not this request's. Before any byte of the span went out, the block
		// is claimed once more; the failed fill has ended, so the claim starts
		// a fill from this request's resolution.
		var statusErr upstreamStatusError
		if attempt > 0 || pos != span.From || fill.sourceURL == mediaURL || !errors.As(err, &statusErr) {
			break
		}
	}
	var diskErr fillDiskError
	if (err == nil || errors.As(err, &diskErr)) && !strictCache && ctx.Err() == nil {
		if err != nil && s.logger != nil {
			s.logger.WithError(err).WithField("block_idx", span.Idx).Debug("blockcache: block fill could not write the cache; streaming the rest of the span without it")
		}
		return s.streamBlockFromS3(ctx, httpc, w, store, blockSpan{Idx: span.Idx, From: pos, To: span.To}, totalSize, mediaURL, grantID, defrost, renewMidBlock)
	}
	if err == nil {
		return fmt.Errorf("block %d evicted while it was being served", span.Idx)
	}
	// The caller answers a status error by serving the whole span again on a
	// renewed resolution, which after bytes of the span went out would repeat
	// them.
	var statusErr upstreamStatusError
	if pos > span.From && errors.As(err, &statusErr) {
		err = upstreamPhaseError{op: "upstream block fetch", err: errors.New(err.Error())}
	}
	return err
}

// readBlockFill copies span from fill's tmp file to w, waiting for the fill
// to write each byte before it is read. It returns the block position of the
// first byte not copied.
func readBlockFill(ctx context.Context, w io.Writer, fill *blockFill, span blockSpan) (int64, error) {
	flusher, canFlush := w.(http.Flusher)
	pos := span.From
	var file fillReader
	defer func() {
		if file != nil {
			_ = file.Close()
		}
	}()
	for pos <= span.To {
		st := fill.state()
		if pos < st.written {
			if file == nil {
				opened, err := fill.open()
				if err != nil {
					return pos, err
				}
				file = opened
			}
			end := min(st.written, span.To+1)
			n, err := io.Copy(w, io.NewSectionReader(file, pos, end-pos))
			pos += n
			if err != nil {
				return pos, err
			}
			if canFlush {
				flusher.Flush()
			}
			continue
		}
		if st.done {
			if st.err != nil {
				return pos, st.err
			}
			return pos, fmt.Errorf("block fill ended at block offset %d before byte %d", st.written, pos)
		}
		select {
		case <-st.changed:
		case <-ctx.Done():
			return pos, ctx.Err()
		}
	}
	return pos, nil
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

// streamBlockFromS3 issues a block-aligned upstream Range GET and streams the
// span's bytes to the client without caching them: the memory-only read path,
// and the rest of a playback span whose block fill could not write the cache.
//
// An upstream body that fails mid-block (a read stall, a dropped connection)
// is requested again from the first byte not yet copied, up to
// maxBlockFetchResumes times, and the client continues from there without a
// gap. A resume refused with 401/403 is sent once more on the resolution
// renewMidBlock returns.
func (s *Server) streamBlockFromS3(ctx context.Context, httpc *http.Client, w io.Writer, store *BlockStore, span blockSpan, totalSize int64, mediaURL, grantID string, defrost func(int64), renewMidBlock func() (upstreamTarget, bool)) error {
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

	// Range-clamped writer that only forwards the [span.From, span.To]
	// subrange; offset 0 is blockStart.
	clientSide := newClampedWriter(w, span.From, span.To)

	// 256 KiB copy buffer — sized for syscall economy without holding
	// any meaningful heap.
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
		upstream := &upstreamReader{r: body}
		copied, err := io.CopyBuffer(clientSide, io.LimitReader(upstream, expected-n), copyBuf)
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
// upstream failure apart from a write failure in the copy that consumes it.
type upstreamReader struct {
	r   io.Reader
	err error
}

func (u *upstreamReader) Read(p []byte) (int, error) {
	n, err := u.r.Read(p)
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

// isMissingStatus reports an upstream that no longer has the object.
func isMissingStatus(code int) bool {
	return code == http.StatusNotFound || code == http.StatusGone
}

// isUpstreamMissingError reports whether err is an upstream 404/410: the
// resolved object was deleted or superseded, which a fresh resolve may replace
// with the artifact's current object.
func isUpstreamMissingError(err error) bool {
	var se upstreamStatusError
	return errors.As(err, &se) && isMissingStatus(se.StatusCode)
}

// isUpstreamAuthError reports whether err is an upstream 401/403: an expired
// presigned URL or a dead peer-relay grant, which a fresh resolve replaces.
func isUpstreamAuthError(err error) bool {
	var se upstreamStatusError
	return errors.As(err, &se) && isAuthStatus(se.StatusCode)
}

// upstreamSizeProbeTimeout bounds one shared size probe. It covers the
// tenant-source client's dial and response-header bounds, so a source that
// answers slowly is still heard from once.
const upstreamSizeProbeTimeout = tenantSourceDialTimeout + tenantSourceHeaderTimeout

// sizeProbeLinger is how long a definitive size-probe answer is kept after
// the probe ends, so Mist's retry, sent right after its source read timeout,
// reads it instead of probing the upstream again.
const sizeProbeLinger = 2 * mistSourceReadTimeout

// sizeProbeGroup runs one upstream size probe per upstream object. Like a
// block fill, a probe does not belong to the reader that started it: it runs
// on a context bounded only by upstreamSizeProbeTimeout, so Mist abandoning a
// read (its 5 s source read timeout) does not abort it, and every reader of
// the object, concurrent or retrying, waits for the same answer. A size, or an
// upstream status a retry would get again, is kept for sizeProbeLinger; any
// other failure is dropped as the probe ends so the next reader probes again.
type sizeProbeGroup struct {
	mu    sync.Mutex
	calls map[string]*sizeProbeCall
	// wg counts the probe goroutines still running.
	wg sync.WaitGroup
}

// sizeProbeCall is one probe; size and err are set before done is closed.
type sizeProbeCall struct {
	done chan struct{}
	size int64
	err  error
}

// do returns the answer of the probe of key, starting it with run when none
// runs or lingers. settled, when set on the call that starts the probe,
// receives the probe's error (nil on success) once, before any reader does.
// A reader whose ctx ends first gets ctx's error; the probe continues.
func (g *sizeProbeGroup) do(ctx context.Context, key string, run func(context.Context) (int64, error), settled func(error)) (int64, error) {
	g.mu.Lock()
	if g.calls == nil {
		g.calls = make(map[string]*sizeProbeCall)
	}
	call, ok := g.calls[key]
	if !ok {
		call = &sizeProbeCall{done: make(chan struct{})}
		g.calls[key] = call
		g.wg.Add(1)
		go g.run(key, call, run, settled)
	}
	g.mu.Unlock()
	select {
	case <-call.done:
		return call.size, call.err
	case <-ctx.Done():
		return 0, fmt.Errorf("upstream size probe: %w", ctx.Err())
	}
}

func (g *sizeProbeGroup) run(key string, call *sizeProbeCall, run func(context.Context) (int64, error), settled func(error)) {
	defer g.wg.Done()
	ctx, cancel := context.WithTimeout(context.Background(), upstreamSizeProbeTimeout)
	call.size, call.err = run(ctx)
	cancel()
	if settled != nil {
		settled(call.err)
	}
	close(call.done)
	if call.err != nil && !definitiveUpstreamStatus(call.err) {
		g.forget(key, call)
		return
	}
	time.AfterFunc(sizeProbeLinger, func() { g.forget(key, call) })
}

func (g *sizeProbeGroup) forget(key string, call *sizeProbeCall) {
	g.mu.Lock()
	if g.calls[key] == call {
		delete(g.calls, key)
	}
	g.mu.Unlock()
}

// definitiveUpstreamStatus reports an upstream that answered a status a later
// request gets again: a client error other than a refused credential, which a
// renewed resolution may cure.
func definitiveUpstreamStatus(err error) bool {
	var statusErr upstreamStatusError
	return errors.As(err, &statusErr) && statusErr.StatusCode >= http.StatusBadRequest &&
		!transientUpstreamStatus(statusErr.StatusCode) && !isAuthStatus(statusErr.StatusCode)
}

// sharedProbeTotalSize is probeTotalSize through the server's sizeProbeGroup.
func (s *Server) sharedProbeTotalSize(ctx context.Context, httpc *http.Client, mediaURL, grantID string, settled func(error)) (int64, error) {
	return s.sizeProbes.do(ctx, mediaURL+"\x00"+grantID, func(probeCtx context.Context) (int64, error) {
		return s.probeTotalSize(probeCtx, httpc, mediaURL, grantID)
	}, settled)
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
	// A fill that is running already answered the upstream's status for
	// this block.
	if store.HasBlock(span.Idx) || s.fills.running(blockFetchKey(store, span.Idx)) {
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
// reached the client and returns the relay_requests_total status for it. op
// names the phase that failed; an upstreamPhaseError in err names it more
// precisely and replaces it. A reader that already left gets no answer.
func (s *Server) respondColdFetchError(c *gin.Context, op string, err error) string {
	if readerGone(c) {
		return statusClientGone
	}
	s.writeColdFetchError(c, op, err)
	return "error"
}

func (s *Server) writeColdFetchError(c *gin.Context, op string, err error) {
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

// statusClientGone is the relay_requests_total status of a request whose
// reader left before the relay finished answering it.
const statusClientGone = "client_gone"

// readerGone reports whether the request's reader has left: Mist abandoning a
// source read past its read timeout, or a viewer closing the connection,
// cancels the request context. Every upstream wait in a request runs on that
// context, so an error after it ends is the reader's departure, not a relay or
// upstream failure.
func readerGone(c *gin.Context) bool {
	return c.Request.Context().Err() != nil
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
