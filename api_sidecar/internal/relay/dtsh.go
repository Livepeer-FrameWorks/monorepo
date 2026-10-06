package relay

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
	"github.com/gin-gonic/gin"

	"frameworks/api_sidecar/internal/dtsh"
)

// dtshFetchTimeout bounds an upstream sidecar fetch below Mist's 5 s read
// timeout. Unlike a media read, a sidecar read Mist cannot complete only makes
// it generate the index itself, so answering fast is always safe.
var dtshFetchTimeout = 4 * time.Second

// serveSidecarGetWithStream handles GET/HEAD for sidecar requests.
// Mist computes the dtsh URL as source + ".dtsh"; the relay must
// respond even before any local dtsh exists so Mist can decide whether
// to generate one.
//
// Branches:
//  1. Local dtsh present → http.ServeContent (sendfile, range support).
//  2. Cold + Foghorn has dtsh_presigned_get (or a peer relay URL) → one
//     shared, deadline-bound upstream fetch per sidecar: GET downloads,
//     validates and caches it; HEAD only learns its size.
//  3. No dtsh anywhere, or the store reports it missing or invalid → 404
//     (triggers Mist to generate + PUT it).
//  4. The store did not answer usably in time → 503 with Retry-After.
//
// streamInternal is the path-encoded stream context for clip URLs
// (clip/<stream>/<file>.dtsh). It selects the nested sidecar layout so
// generated sidecars land next to the clip's media file.
func (s *Server) serveSidecarGetWithStream(c *gin.Context, kind, hash, file, streamInternal string) {
	forceCloseForMistReader(c)

	localPath := s.canonicalFilePath(kind, file)
	if nestedPath := s.nestedSidecarPathFor(kind, file, streamInternal); nestedPath != "" {
		localPath = nestedPath
	}

	if info, err := os.Stat(localPath); err == nil && info.Mode().IsRegular() && info.Size() > 0 {
		if err := dtsh.ValidateFile(localPath); err != nil {
			if s.logger != nil {
				s.logger.WithError(err).WithField("local_path", localPath).Warn("relay warm dtsh invalid; removing and returning generation signal")
			}
			_ = os.Remove(localPath)
			c.Status(http.StatusNotFound)
			return
		}
		f, err := os.Open(localPath)
		if err != nil {
			if s.logger != nil {
				s.logger.WithError(err).WithField("local_path", localPath).Debug("relay warm dtsh open failed; returning generation signal")
			}
			c.Status(http.StatusNotFound)
			return
		}
		defer f.Close()
		http.ServeContent(c.Writer, c.Request, filepath.Base(localPath), info.ModTime(), f)
		return
	}

	mediaFile := strings.TrimSuffix(file, ".dtsh")
	ext := filepath.Ext(mediaFile)
	rc := ResolveContext{
		Ctx:       c.Request.Context(),
		AssetKind: kind,
		AssetHash: hash,
		Ext:       ext,
		Hint:      ipcpb.RelayResolveRequest_RELAY_HINT_RANDOM_ACCESS,
	}
	res, err := s.resolveCached(rc)
	if err != nil {
		if s.logger != nil {
			s.logger.WithError(err).WithField("asset_hash", hash).Debug("relay dtsh resolve failed; returning generation signal")
		}
		c.Status(http.StatusNotFound)
		return
	}
	// Source order: S3 (synced) first, else a peer relay holding the hot
	// sidecar (origin node, not yet S3-synced). Foghorn sets at most one of
	// these per resolve. peerBearer is non-empty only on the peer path; the
	// grant authorizes both the media and its .dtsh path, validated online by
	// the origin edge's Foghorn.
	fetchURL := res.DtshPresignedGet
	peerBearer := ""
	if fetchURL == "" {
		fetchURL = res.PeerRelayDtshURL
		peerBearer = res.PeerRelayGrantID
	}
	if fetchURL == "" {
		// No sidecar anywhere yet. 404 is the signal for Mist to generate
		// one and PUT it back.
		dtshGeneration.WithLabelValues("lazy_404", "ok").Inc()
		c.Status(http.StatusNotFound)
		return
	}

	key := c.Request.Method + "|" + kind + "|" + streamInternal + "|" + file
	fetch := func(ctx context.Context) sidecarFetchResult {
		return s.fetchSidecarBody(ctx, fetchURL, peerBearer, localPath)
	}
	if c.Request.Method == http.MethodHead {
		fetch = func(ctx context.Context) sidecarFetchResult {
			return probeSidecarSize(ctx, s.httpc, fetchURL, peerBearer)
		}
	}
	result, err := s.sidecarFetches.do(c.Request.Context(), key, fetch)
	if err != nil {
		return // the reader left while another request's fetch ran
	}

	switch result.status {
	case http.StatusOK:
	case http.StatusNotFound:
		// The store reports no sidecar, or one that does not validate: Mist
		// generates a fresh one and PUTs it back.
		if s.logger != nil {
			s.logger.WithField("asset_hash", hash).WithField("reason", result.reason).Debug("relay dtsh not in store; returning generation signal")
		}
		c.Status(http.StatusNotFound)
		return
	default:
		if result.staleResolve {
			s.cache.Delete(kind, hash)
		}
		if s.logger != nil {
			s.logger.WithField("asset_hash", hash).WithField("reason", result.reason).Warn("relay dtsh fetch failed; answering retry-later")
		}
		dtshGeneration.WithLabelValues("fetch", "unavailable").Inc()
		c.Writer.Header().Set("Retry-After", "5")
		c.Status(http.StatusServiceUnavailable)
		return
	}

	for _, h := range []string{"Content-Type", "ETag", "Last-Modified"} {
		if v := result.header.Get(h); v != "" {
			c.Writer.Header().Set(h, v)
		}
	}
	c.Writer.Header().Set("Accept-Ranges", "bytes")
	c.Writer.Header().Set("Content-Length", strconv.FormatInt(result.size, 10))
	c.Writer.WriteHeader(http.StatusOK)
	if c.Request.Method == http.MethodHead {
		return
	}
	if _, writeErr := c.Writer.Write(result.body); writeErr != nil && s.logger != nil {
		s.logger.WithError(writeErr).Debug("relay dtsh stream aborted")
	}
}

// sidecarFetchResult is the outcome of one upstream sidecar fetch, shared by
// every request that waited on it. status is 200 (body or size known), 404
// (the store has no valid sidecar) or 503 (the store did not answer usably).
type sidecarFetchResult struct {
	status int
	body   []byte
	size   int64
	header http.Header
	reason string
	// staleResolve marks a failure that may come from the resolved URL itself
	// (expired presign, dead peer grant, unreachable upstream).
	staleResolve bool
}

// sidecarFetchGroup runs one upstream fetch per sidecar request key at a
// time. Each fetch is bounded by dtshFetchTimeout, below Mist's 5 s read
// timeout (mistserver lib/downloader.cpp dataTimeout), so Mist gets an
// answer instead of timing out and retrying into the same stall. A fetch runs
// detached from the request that started it: requests that joined it still
// get its result when that request's reader leaves.
type sidecarFetchGroup struct {
	mu       sync.Mutex
	inflight map[string]*sidecarFetchCall
}

type sidecarFetchCall struct {
	done   chan struct{}
	result sidecarFetchResult
}

func (g *sidecarFetchGroup) do(ctx context.Context, key string, fetch func(context.Context) sidecarFetchResult) (sidecarFetchResult, error) {
	g.mu.Lock()
	if call, ok := g.inflight[key]; ok {
		g.mu.Unlock()
		select {
		case <-call.done:
			return call.result, nil
		case <-ctx.Done():
			return sidecarFetchResult{}, ctx.Err()
		}
	}
	if g.inflight == nil {
		g.inflight = make(map[string]*sidecarFetchCall)
	}
	call := &sidecarFetchCall{done: make(chan struct{})}
	g.inflight[key] = call
	g.mu.Unlock()

	fetchCtx, cancel := context.WithTimeout(context.Background(), dtshFetchTimeout)
	call.result = fetch(fetchCtx)
	cancel()

	g.mu.Lock()
	delete(g.inflight, key)
	g.mu.Unlock()
	close(call.done)
	return call.result, nil
}

// sidecarFetchFailure classifies an upstream sidecar answer that is not a
// sidecar. Only a store that reports the object missing is a 404.
func sidecarFetchFailure(statusCode int) sidecarFetchResult {
	if statusCode == http.StatusNotFound || statusCode == http.StatusGone {
		return sidecarFetchResult{status: http.StatusNotFound, reason: fmt.Sprintf("upstream status %d", statusCode)}
	}
	return sidecarFetchResult{
		status:       http.StatusServiceUnavailable,
		reason:       fmt.Sprintf("upstream status %d", statusCode),
		staleResolve: statusCode == http.StatusUnauthorized || statusCode == http.StatusForbidden,
	}
}

func newSidecarRequest(ctx context.Context, fetchURL, bearer string) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, fetchURL, nil)
	if err != nil {
		return nil, err
	}
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	return req, nil
}

// fetchSidecarBody downloads and validates the whole sidecar, and caches a
// valid one at localPath (tmpfile, then atomic rename) so a half-written file
// never ends up at the canonical path.
func (s *Server) fetchSidecarBody(ctx context.Context, fetchURL, bearer, localPath string) sidecarFetchResult {
	req, err := newSidecarRequest(ctx, fetchURL, bearer)
	if err != nil {
		return sidecarFetchResult{status: http.StatusServiceUnavailable, reason: err.Error(), staleResolve: true}
	}
	resp, err := s.httpc.Do(req)
	if err != nil {
		return sidecarFetchResult{status: http.StatusServiceUnavailable, reason: err.Error(), staleResolve: true}
	}
	defer resp.Body.Close()
	if resp.StatusCode >= http.StatusBadRequest {
		return sidecarFetchFailure(resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return sidecarFetchResult{status: http.StatusServiceUnavailable, reason: err.Error()}
	}
	if validateErr := dtsh.Validate(body); validateErr != nil {
		return sidecarFetchResult{status: http.StatusNotFound, reason: "invalid sidecar: " + validateErr.Error()}
	}
	s.cacheSidecar(localPath, body)
	return sidecarFetchResult{status: http.StatusOK, body: body, size: int64(len(body)), header: resp.Header}
}

// probeSidecarSize answers a HEAD from a one-byte ranged read; a presigned GET
// URL is not valid for an upstream HEAD. The body is validated when it is read.
func probeSidecarSize(ctx context.Context, httpc *http.Client, fetchURL, bearer string) sidecarFetchResult {
	req, err := newSidecarRequest(ctx, fetchURL, bearer)
	if err != nil {
		return sidecarFetchResult{status: http.StatusServiceUnavailable, reason: err.Error(), staleResolve: true}
	}
	req.Header.Set("Range", "bytes=0-0")
	resp, err := httpc.Do(req)
	if err != nil {
		return sidecarFetchResult{status: http.StatusServiceUnavailable, reason: err.Error(), staleResolve: true}
	}
	defer resp.Body.Close()
	if _, err := io.Copy(io.Discard, io.LimitReader(resp.Body, 4*1024)); err != nil {
		return sidecarFetchResult{status: http.StatusServiceUnavailable, reason: err.Error()}
	}
	if resp.StatusCode >= http.StatusBadRequest {
		return sidecarFetchFailure(resp.StatusCode)
	}
	size := resp.ContentLength
	if resp.StatusCode == http.StatusPartialContent {
		total, ok := totalFromContentRange(resp.Header.Get("Content-Range"))
		if !ok {
			return sidecarFetchResult{status: http.StatusServiceUnavailable, reason: "sidecar size probe missing Content-Range total"}
		}
		size = total
	}
	if size <= 0 {
		return sidecarFetchResult{status: http.StatusNotFound, reason: "empty sidecar"}
	}
	return sidecarFetchResult{status: http.StatusOK, size: size, header: resp.Header}
}

func (s *Server) cacheSidecar(localPath string, body []byte) {
	if mkErr := os.MkdirAll(filepath.Dir(localPath), 0o755); mkErr != nil {
		return
	}
	tmp := localPath + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return
	}
	if _, err := f.Write(body); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmp)
		return
	}
	if err := os.Rename(tmp, localPath); err != nil && s.logger != nil {
		s.logger.WithError(err).Debug("relay dtsh rename failed")
	}
}

// putSidecar handles PUT /internal/artifact/<kind>/<file>.dtsh without a
// stream context. Wraps putSidecarWithStream with streamInternal="".
func (s *Server) putSidecar(c *gin.Context, kind string) {
	s.putSidecarWithStream(c, kind, "")
}

// putClipRoute dispatches PUT /clip/* by path shape (flat
// clip/<file>.dtsh vs stream-scoped clip/<stream>/<file>.dtsh), the
// PUT counterpart to serveClipRoute. See parseClipWildcardPath for
// the shape rules.
func (s *Server) putClipRoute(c *gin.Context) {
	stream, file := parseClipWildcardPath(c.Param("path"))
	if file == "" {
		c.AbortWithStatus(http.StatusNotFound)
		return
	}
	c.Params = append(c.Params, gin.Param{Key: "file", Value: file})
	s.putSidecarWithStream(c, "clip", stream)
}

// putSidecarWithStream handles PUT /internal/artifact/.../<file>.dtsh from
// Mist's externalWriter path. The body is written to local disk under the
// canonical sidecar path (tmpfile → fsync → atomic rename); for stream-scoped
// clips this is the nested clips/<stream>/<file> path so the sidecar lands next
// to the writer's media file. Mist gets 200 OK as soon as this completes, and
// the next cold playback on this node skips header generation. Durable storage
// of the index is the freeze subsystem's responsibility: OnLocalDtshGenerated
// hands the local file to it so Foghorn drives the verified, attempt-scoped
// staged publication. The relay never writes the .dtsh to S3 directly.
func (s *Server) putSidecarWithStream(c *gin.Context, kind, streamInternal string) {
	forceCloseForMistReader(c)

	file := strings.Trim(c.Param("file"), "/")
	if !safeRelayPathSegment(file) {
		c.AbortWithStatus(http.StatusNotFound)
		return
	}
	if !strings.HasSuffix(file, ".dtsh") {
		c.AbortWithStatus(http.StatusMethodNotAllowed)
		return
	}
	hash := hashFromFile(file)
	if hash == "" {
		c.AbortWithStatus(http.StatusBadRequest)
		return
	}
	localPath := s.canonicalFilePath(kind, file)
	if nested := s.nestedSidecarPathFor(kind, file, streamInternal); nested != "" {
		localPath = nested
	}
	if err := os.MkdirAll(filepath.Dir(localPath), 0o755); err != nil {
		s.serverError(c, "mkdir sidecar dir", err)
		return
	}
	tmp := localPath + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		s.serverError(c, "create sidecar tmp", err)
		return
	}
	written, err := io.Copy(f, c.Request.Body)
	if err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		if s.logger != nil {
			s.logger.WithError(err).WithField("local_path", localPath).Warn("relay sidecar PUT body ended before a durable sidecar could be written")
		}
		c.String(http.StatusBadRequest, "incomplete sidecar body")
		return
	}
	if written == 0 {
		_ = f.Close()
		_ = os.Remove(tmp)
		if s.logger != nil {
			s.logger.WithField("local_path", localPath).Warn("relay sidecar PUT body was empty")
		}
		c.String(http.StatusBadRequest, "empty sidecar body")
		return
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		s.serverError(c, "fsync sidecar", err)
		return
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmp)
		s.serverError(c, "close sidecar", err)
		return
	}
	if err := dtsh.ValidateFile(tmp); err != nil {
		_ = os.Remove(tmp)
		if s.logger != nil {
			s.logger.WithError(err).WithFields(logging.Fields{
				"local_path":     localPath,
				"bytes_received": written,
				"content_length": c.Request.ContentLength,
			}).Warn("relay sidecar PUT contained invalid dtsh")
		}
		c.String(http.StatusBadRequest, "invalid sidecar body")
		return
	}
	if err := os.Rename(tmp, localPath); err != nil {
		s.serverError(c, "rename sidecar", err)
		return
	}

	// Persisting the freshly-generated .dtsh to durable storage is the freeze subsystem's job, via the
	// server-assigned STAGED publication: OnLocalDtshGenerated records the local index so the next lifecycle
	// report advertises has_dtsh and Foghorn drives a verified, attempt-scoped TriggerDtshSync. The relay does
	// NOT upload the .dtsh directly — a direct PUT to a fixed key wrote an unverified, untracked object whose
	// synthesized completion matched no persisted attempt.
	if s.freeze != nil {
		s.freeze.OnLocalDtshGenerated(kind, hash, localPath)
	}
	dtshGeneration.WithLabelValues("putback", "ok").Inc()
	c.Writer.Header().Set("Content-Length", "0")
	c.Status(http.StatusOK)
}

// nestedSidecarPathFor returns the nested clip-writer sidecar path
// (storage/clips/<stream>/<file>) when kind=="clip" and a stream context
// is supplied (path-encoded segment from the /clip/:stream/:file route).
// Empty for other kinds or when no stream is supplied — caller falls
// back to the flat sidecar path.
func (s *Server) nestedSidecarPathFor(kind, file, streamInternal string) string {
	if kind != "clip" || streamInternal == "" {
		return ""
	}
	return filepath.Join(s.basePath, "clips", streamInternal, file)
}
