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

	"github.com/gin-gonic/gin"

	"frameworks/api_sidecar/internal/admission"
)

// serveViaBlockCache is the relay's random-access entrypoint for single-file
// artifacts. The block cache needs a total size; when Foghorn did not provide
// one, the relay probes byte 0 and uses Content-Range as the source of truth.
func (s *Server) serveViaBlockCache(c *gin.Context, kind, hash, ext, localPath string, res *ResolveResult, intent admission.StorageIntent) string {
	totalSize := int64(res.ExpectedSizeBytes)
	if totalSize <= 0 {
		probedSize, err := s.probeTotalSize(c.Request.Context(), res.UpstreamURL(), res.PeerRelayGrantID)
		if err != nil {
			s.respondColdFetchError(c, err)
			return "error"
		}
		totalSize = probedSize
	}
	strictCache := intent == admission.IntentProcessingInput

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
	upstreamURL := res.UpstreamURL()
	recordDefrost := s.defrostRecorderFor(kind, hash)
	if cacheDecision == admission.CacheToDisk && !hasRange {
		if err := s.preflightFirstColdSpan(c.Request.Context(), store, spans[0], totalSize, upstreamURL, res.PeerRelayGrantID); err != nil {
			s.cache.Delete(kind, hash)
			s.respondColdFetchError(c, err)
			return "error"
		}
	}
	// Disk-touching block-store setup happens only for CacheToDisk and after
	// the cold preflight. Missing upstream sources should not leave a
	// meta-only .blocks directory behind.
	if cacheDecision == admission.CacheToDisk {
		store.CleanTmps()
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

	for _, span := range spans {
		if err := s.serveBlockSpan(c.Request.Context(), c.Writer, store, span, totalSize, upstreamURL, res.PeerRelayGrantID, cacheDecision, strictCache, recordDefrost); err != nil {
			// A peer-relay grant that 401/403s mid-stream is dead (origin
			// restarted, grant evicted before the resolve TTL lapsed). Drop the
			// resolve-cache entry so the next request re-resolves and re-mints
			// instead of replaying the dead grant. Headers are already sent for
			// this request, so this only heals subsequent ones. The un-ranged
			// cold preflight above handles the same case before headers; ranged
			// and memory-only requests skip it and land here.
			if res.PeerRelayGrantID != "" && isUpstreamAuthError(err) {
				s.cache.Delete(kind, hash)
			}
			if s.logger != nil && !isClientGone(err) {
				s.logger.WithError(err).WithField("block_idx", span.Idx).Debug("blockcache: span serve aborted")
			}
			return "error"
		}
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
func (s *Server) serveBlockSpan(ctx context.Context, w io.Writer, store *BlockStore, span blockSpan, totalSize int64, mediaURL, grantID string, decision admission.CacheDecision, strictCache bool, defrost func(int64)) error {
	if served, err := s.serveWarmBlock(w, store, span); served || err != nil {
		return err
	}
	if decision != admission.CacheToDisk || s.coldFetch == nil {
		return s.streamBlockFromS3(ctx, w, store, span, totalSize, mediaURL, grantID, decision, strictCache, defrost)
	}

	key := fmt.Sprintf("%s|%d", store.Dir(), span.Idx)
	leader, fetch := s.coldFetch.claim(key)
	if !leader {
		coldfetchCoalesced.WithLabelValues("follower").Inc()
		// Late arrival: wait for the leader, then read the warm block.
		// If the leader's disk write failed, fall through to an own
		// S3 fetch (no coalescing this time).
		select {
		case <-fetch.done:
		case <-ctx.Done():
			return ctx.Err()
		}
		if fetch.diskOk {
			if served, err := s.serveWarmBlock(w, store, span); served || err != nil {
				return err
			}
		}
		return s.streamBlockFromS3(ctx, w, store, span, totalSize, mediaURL, grantID, decision, strictCache, defrost)
	}
	coldfetchCoalesced.WithLabelValues("leader").Inc()

	// Leader path: run the fetch, then publish disk-write outcome.
	err := s.streamBlockFromS3(ctx, w, store, span, totalSize, mediaURL, grantID, decision, strictCache, defrost)
	s.coldFetch.finish(key, err == nil && store.HasBlock(span.Idx))
	return err
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
// fetch. The first writer to finish wins the rename; later writers
// see the warm block exists and drop their tmpfile.
func (s *Server) streamBlockFromS3(ctx context.Context, w io.Writer, store *BlockStore, span blockSpan, totalSize int64, mediaURL, grantID string, decision admission.CacheDecision, strictCache bool, defrost func(int64)) error {
	blockStart, blockEnd := store.BlockRange(span.Idx, totalSize)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, mediaURL, nil)
	if err != nil {
		return fmt.Errorf("build upstream block request: %w", err)
	}
	req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", blockStart, blockEnd))
	// Peer-relay upstream (origin Helmsman) requires the capability grant id
	// as Authorization: Bearer. Empty for S3 presigned URLs (auth in the URL
	// query string).
	if grantID != "" {
		req.Header.Set("Authorization", "Bearer "+grantID)
	}
	source := upstreamSourceLabel(grantID)
	fetchStart := time.Now()
	resp, err := s.httpc.Do(req)
	if err != nil {
		defrostBlocks.WithLabelValues(source, "error").Inc()
		return fmt.Errorf("upstream block fetch: %w", err)
	}
	defrostTTFB.WithLabelValues(source).Observe(time.Since(fetchStart).Seconds())
	defer resp.Body.Close()
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
	n, copyErr := io.CopyBuffer(tee, io.LimitReader(resp.Body, expected), copyBuf)
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
		return copyErr
	}
	if n != expected {
		defrostBlocks.WithLabelValues(source, "error").Inc()
		return fmt.Errorf("block %d short: copied %d bytes, expected %d", span.Idx, n, expected)
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

type upstreamStatusError struct {
	StatusCode int
}

func (e upstreamStatusError) Error() string {
	return fmt.Sprintf("upstream artifact fetch returned status %d", e.StatusCode)
}

// isUpstreamAuthError reports whether err is an upstream 401/403 — the signature
// of a dead peer-relay grant whose resolve-cache entry must be dropped so the
// next request re-mints rather than replaying it until the resolve TTL lapses.
func isUpstreamAuthError(err error) bool {
	var se upstreamStatusError
	return errors.As(err, &se) &&
		(se.StatusCode == http.StatusUnauthorized || se.StatusCode == http.StatusForbidden)
}

func (s *Server) probeTotalSize(ctx context.Context, mediaURL, grantID string) (int64, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, mediaURL, nil)
	if err != nil {
		return 0, fmt.Errorf("build upstream size probe: %w", err)
	}
	req.Header.Set("Range", "bytes=0-0")
	if grantID != "" {
		req.Header.Set("Authorization", "Bearer "+grantID)
	}
	resp, err := s.httpc.Do(req)
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

func (s *Server) preflightFirstColdSpan(ctx context.Context, store *BlockStore, span blockSpan, totalSize int64, mediaURL, grantID string) error {
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
	resp, err := s.httpc.Do(req)
	if err != nil {
		return fmt.Errorf("upstream block preflight fetch: %w", err)
	}
	defer resp.Body.Close()
	if _, err := io.Copy(io.Discard, io.LimitReader(resp.Body, 4*1024)); err != nil {
		return fmt.Errorf("drain upstream block preflight response: %w", err)
	}
	if resp.StatusCode == http.StatusPartialContent {
		return nil
	}
	if resp.StatusCode == http.StatusOK && blockStart == 0 && blockEnd == totalSize-1 {
		return nil
	}
	return upstreamStatusError{StatusCode: resp.StatusCode}
}

func (s *Server) respondColdFetchError(c *gin.Context, err error) {
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
			c.String(http.StatusBadGateway, "source fetch failed: upstream status %d", statusErr.StatusCode)
		}
		return
	}
	s.serverError(c, "upstream block preflight", err)
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
