package relay

import (
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	pb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto"
)

// kindDir maps the URL kind to the on-disk directory name. "clip" → "clips"
// (existing layout uses plural for clips, singular for vod/upload).
func kindDir(kind string) string {
	if kind == "clip" {
		return "clips"
	}
	return kind
}

// canonicalFilePath returns the on-disk path the relay reads/writes for a
// file artifact (vod/clip). Flat layout: storage/<kindDir>/<hash>.<ext>.
func (s *Server) canonicalFilePath(kind, file string) string {
	return filepath.Join(s.basePath, kindDir(kind), file)
}

func (s *Server) canonicalUploadPath(file string) string {
	return filepath.Join(s.basePath, "upload", file)
}

// hashFromFile extracts the hash from "<hash>.<ext>" (and "<hash>.<ext>.dtsh").
func hashFromFile(file string) string {
	stripped := strings.TrimSuffix(file, ".dtsh")
	if ext := path.Ext(stripped); ext != "" {
		stripped = strings.TrimSuffix(stripped, ext)
	}
	return stripped
}

func contentTypeForFile(file string) string {
	ext := strings.ToLower(path.Ext(strings.TrimSuffix(file, ".dtsh")))
	switch ext {
	case ".m3u8":
		return "application/vnd.apple.mpegurl"
	case ".ts":
		return "video/mp2t"
	case ".mp4":
		return "video/mp4"
	case ".mov":
		return "video/quicktime"
	case ".mkv":
		return "video/x-matroska"
	case ".webm":
		return "video/webm"
	default:
		return ""
	}
}

// serveFile handles GET/HEAD for /internal/artifact/{vod|clip}/<file>.
// Three branches:
//
//  1. *.dtsh request → delegate to serveSidecarGet for sidecar semantics.
//  2. Complete local file → http.ServeContent (sendfile, full range support).
//  3. Cold → resolve via Foghorn, fetch from S3, optionally write-through to
//     disk based on admission policy.
//
// streamInternal is non-empty only for the stream-scoped clip route
// (/clip/<stream>/<file>). It controls the nested warm-path probe and
// the cold-fill destination so the cache lands at the same path the
// clip writer uses.
func (s *Server) serveFile(c *gin.Context, kind string) {
	s.serveFileWithStream(c, kind, "")
}

// serveClipRoute dispatches /clip/<stream>/<file> requests. Stream
// identity stays in the path so Mist's input + ".dtsh" sidecar
// mutation resolves against the same clip artifact.
func (s *Server) serveClipRoute(c *gin.Context) {
	stream, file := parseClipWildcardPath(c.Param("path"))
	if file == "" || stream == "" {
		c.AbortWithStatus(http.StatusNotFound)
		return
	}
	c.Params = append(c.Params, gin.Param{Key: "file", Value: file})
	s.serveFileWithStream(c, "clip", stream)
}

// parseClipWildcardPath splits a Gin wildcard "*path" capture into
// (streamInternal, file). The wildcard always begins with "/". Only
// two-segment paths (<stream>/<file>) are valid; anything else
// returns ("", "") and the caller 404s.
func parseClipWildcardPath(p string) (string, string) {
	p = strings.Trim(p, "/")
	if p == "" {
		return "", ""
	}
	parts := strings.Split(p, "/")
	if len(parts) != 2 || !safeRelayPathSegment(parts[0]) || !safeRelayPathSegment(parts[1]) {
		return "", ""
	}
	return parts[0], parts[1]
}

func (s *Server) serveFileWithStream(c *gin.Context, kind, streamInternal string) {
	forceCloseForMistReader(c)

	file := c.Param("file")
	if !safeRelayPathSegment(file) {
		c.AbortWithStatus(http.StatusNotFound)
		return
	}
	hash := hashFromFile(file)
	if hash == "" {
		c.AbortWithStatus(http.StatusNotFound)
		return
	}

	if strings.HasSuffix(file, ".dtsh") {
		s.serveSidecarGetWithStream(c, kind, hash, file, streamInternal)
		return
	}

	// One serve metric per media request. source/status start at their
	// defaults and are updated as the path resolves; the defer captures the
	// final values. Labels are bounded — no tenant/asset identity.
	format := relayFormatLabel(file)
	source := "local"
	status := "error"
	start := time.Now()
	defer func() {
		relayRequests.WithLabelValues(format, source, status).Inc()
		relayServeSeconds.WithLabelValues(format, source).Observe(time.Since(start).Seconds())
	}()

	// Clip artifacts are stream-scoped; VOD and upload artifacts stay
	// flat at storage/<kind>/<file>.
	var localPath string
	if kind == "clip" {
		if streamInternal == "" {
			status = "not_playable"
			c.AbortWithStatus(http.StatusNotFound)
			return
		}
		localPath = s.nestedClipPath(streamInternal, file)
	} else {
		localPath = s.canonicalFilePath(kind, file)
	}
	if served := s.serveWarmIfPresent(c, localPath); served {
		source, status = "local", "served"
		return
	}

	ext := path.Ext(strings.TrimSuffix(file, ".dtsh"))
	rc := ResolveContext{
		Ctx:       c.Request.Context(),
		AssetKind: kind,
		AssetHash: hash,
		Ext:       ext,
		Hint:      pb.RelayResolveRequest_RELAY_HINT_RANDOM_ACCESS,
	}
	res, err := s.resolveCached(rc)
	if err != nil {
		s.serverError(c, "relay resolve", err)
		return
	}
	source = upstreamSourceLabel(res.PeerRelayGrantID)
	if res.State != pb.AssetState_ASSET_STATE_PLAYABLE || res.UpstreamURL() == "" {
		status = "not_playable"
		s.notPlayable(c, res)
		return
	}
	status = s.fetchAndServe(c, kind, hash, ext, localPath, res)
}

// nestedClipPath returns storage/clips/<stream_internal_name>/<file>.
// Matches the clip-writer layout so relay playback hits the same
// on-disk file the writer produced.
func (s *Server) nestedClipPath(streamInternal, file string) string {
	return filepath.Join(s.basePath, "clips", streamInternal, file)
}

// serveWarmIfPresent serves localPath via http.ServeContent if it exists
// as a non-empty regular file. Returns true when it served (caller stops);
// false when no usable warm file at that path (caller continues).
func (s *Server) serveWarmIfPresent(c *gin.Context, localPath string) bool {
	info, err := os.Stat(localPath)
	if err != nil || !info.Mode().IsRegular() || info.Size() == 0 {
		return false
	}
	f, err := os.Open(localPath)
	if err != nil {
		s.serverError(c, "open warm file", err)
		return true
	}
	defer f.Close()
	if contentType := contentTypeForFile(localPath); contentType != "" {
		c.Header("Content-Type", contentType)
	}
	http.ServeContent(c.Writer, c.Request, filepath.Base(localPath), info.ModTime(), f)
	return true
}

// serveUpload handles processing-input reads. Always memory-only per
// admission policy (sequential one-shot). The route does not include a
// hash subdirectory — uploads are flat under {basePath}/upload/.
func (s *Server) serveUpload(c *gin.Context) {
	forceCloseForMistReader(c)

	file := c.Param("file")
	if !safeRelayPathSegment(file) {
		c.AbortWithStatus(http.StatusNotFound)
		return
	}
	hash := hashFromFile(file)
	if hash == "" {
		c.AbortWithStatus(http.StatusNotFound)
		return
	}
	if strings.HasSuffix(file, ".dtsh") {
		s.serveSidecarGetWithStream(c, "upload", hash, file, "")
		return
	}

	localPath := s.canonicalUploadPath(file)
	if info, err := os.Stat(localPath); err == nil && info.Mode().IsRegular() && info.Size() > 0 {
		// Unsafe-wrapper staging may have already materialized this file
		// locally; serve directly when present.
		f, err := os.Open(localPath)
		if err != nil {
			s.serverError(c, "open warm upload", err)
			return
		}
		defer f.Close()
		if contentType := contentTypeForFile(localPath); contentType != "" {
			c.Header("Content-Type", contentType)
		}
		http.ServeContent(c.Writer, c.Request, filepath.Base(localPath), info.ModTime(), f)
		return
	}

	ext := path.Ext(file)
	rc := ResolveContext{
		Ctx:       c.Request.Context(),
		AssetKind: "upload",
		AssetHash: hash,
		Ext:       ext,
		Hint:      pb.RelayResolveRequest_RELAY_HINT_SEQUENTIAL_ONESHOT,
	}
	res, err := s.resolveCached(rc)
	if err != nil {
		s.serverError(c, "relay resolve upload", err)
		return
	}
	if res.State != pb.AssetState_ASSET_STATE_PLAYABLE || res.UpstreamURL() == "" {
		s.notPlayable(c, res)
		return
	}
	// Upload reads are always memory-only — sequential one-shot. Bypass disk
	// admission entirely.
	s.streamRangeNoCacheWithOptions(c, res, noCacheOptions{RetryFullOn416: true})
}

func forceCloseForMistReader(c *gin.Context) {
	if !strings.Contains(c.Request.UserAgent(), "MistServer") {
		return
	}
	c.Request.Close = true
	c.Writer.Header().Set("Connection", "close")
}

// fetchAndServe is the cold-playback dispatcher. HEAD passes straight
// to S3 (no caching needed). GET routes through the block cache —
// block-aligned Range fetches, write-through to disk per admission,
// served as a 206 (or 200 for un-ranged). Concurrent cold viewers on
// the same (asset, block) coalesce via blockFetchCoalescer when
// admission allows CacheToDisk; memory-only fetches don't coalesce
// (no shared warm file to wait for). The block cache is the only
// persistent cache the relay maintains for single-file artifacts; the
// canonical warm-disk full file written by processing/clip-create
// always wins above it.
func (s *Server) fetchAndServe(c *gin.Context, kind, hash, ext, localPath string, res *ResolveResult) string {
	if c.Request.Method == http.MethodHead {
		return s.streamRangeNoCache(c, res)
	}
	return s.serveViaBlockCache(c, kind, hash, ext, localPath, res)
}

// streamRangeNoCache forwards Mist's Range to S3, copies the response
// straight back, and never touches disk. Used for memory-only admission
// outcomes and for processing-input reads.
func (s *Server) streamRangeNoCache(c *gin.Context, res *ResolveResult) string {
	return s.streamRangeNoCacheWithOptions(c, res, noCacheOptions{})
}

type noCacheOptions struct {
	RetryFullOn416 bool
}

func (s *Server) streamRangeNoCacheWithOptions(c *gin.Context, res *ResolveResult, opts noCacheOptions) string {
	method := c.Request.Method
	if method == http.MethodHead {
		method = http.MethodGet
	}
	upstream := res.UpstreamURL()
	req, err := http.NewRequestWithContext(c.Request.Context(), method, upstream, nil)
	if err != nil {
		s.serverError(c, "build upstream request", err)
		return "error"
	}
	if res.PeerRelayGrantID != "" {
		req.Header.Set("Authorization", "Bearer "+res.PeerRelayGrantID)
	}
	requestRange := ""
	if c.Request.Method == http.MethodHead {
		requestRange = "bytes=0-0"
		req.Header.Set("Range", "bytes=0-0")
	} else if rng := c.Request.Header.Get("Range"); rng != "" {
		requestRange = rng
		req.Header.Set("Range", rng)
	}
	resp, err := s.httpc.Do(req)
	if err != nil {
		s.serverError(c, "upstream fetch", err)
		return "error"
	}
	defer resp.Body.Close()

	if opts.RetryFullOn416 &&
		c.Request.Method != http.MethodHead &&
		requestRange != "" &&
		resp.StatusCode == http.StatusRequestedRangeNotSatisfiable {
		if _, discardErr := io.Copy(io.Discard, resp.Body); discardErr != nil && s.logger != nil {
			s.logger.WithError(discardErr).Debug("relay upload range retry discarded partial response")
		}
		resp.Body.Close()

		retryReq, retryErr := http.NewRequestWithContext(c.Request.Context(), method, upstream, nil)
		if retryErr != nil {
			s.serverError(c, "build upstream retry request", retryErr)
			return "error"
		}
		if res.PeerRelayGrantID != "" {
			retryReq.Header.Set("Authorization", "Bearer "+res.PeerRelayGrantID)
		}
		resp, err = s.httpc.Do(retryReq)
		if err != nil {
			s.serverError(c, "upstream retry fetch", err)
			return "error"
		}
		defer resp.Body.Close()
	}

	// Mirror status and the headers that Mist cares about. Content-Length,
	// Content-Range, Accept-Ranges drive seekability detection in
	// HTTP::URIReader.
	for _, h := range []string{"Content-Length", "Content-Range", "Content-Type", "Accept-Ranges", "ETag", "Last-Modified"} {
		if v := resp.Header.Get(h); v != "" {
			c.Writer.Header().Set(h, v)
		}
	}
	if c.Request.Method == http.MethodHead {
		if total, ok := totalFromContentRange(resp.Header.Get("Content-Range")); ok {
			c.Writer.Header().Set("Content-Length", strconv.FormatInt(total, 10))
			c.Writer.Header().Del("Content-Range")
		}
	}
	if c.Writer.Header().Get("Accept-Ranges") == "" {
		c.Writer.Header().Set("Accept-Ranges", "bytes")
	}
	if c.Request.Method == http.MethodHead {
		if resp.StatusCode == http.StatusPartialContent {
			c.Writer.WriteHeader(http.StatusOK)
		} else {
			c.Writer.WriteHeader(resp.StatusCode)
		}
		if resp.StatusCode >= 400 {
			return "error"
		}
		return "served"
	}
	c.Writer.WriteHeader(resp.StatusCode)
	buf := make([]byte, 256*1024)
	if _, copyErr := io.CopyBuffer(c.Writer, resp.Body, buf); copyErr != nil {
		if s.logger != nil && !errors.Is(copyErr, context.Canceled) {
			s.logger.WithError(copyErr).Debug("relay no-cache stream aborted")
		}
		return "error"
	}
	if resp.StatusCode >= 400 {
		return "error"
	}
	return "served"
}

func totalFromContentRange(v string) (int64, bool) {
	slash := strings.LastIndex(v, "/")
	if slash < 0 || slash == len(v)-1 {
		return 0, false
	}
	total, err := strconv.ParseInt(v[slash+1:], 10, 64)
	return total, err == nil && total >= 0
}

func (s *Server) notPlayable(c *gin.Context, res *ResolveResult) {
	switch res.State {
	case pb.AssetState_ASSET_STATE_SOURCE_MISSING:
		c.String(http.StatusNotFound, "source missing")
	case pb.AssetState_ASSET_STATE_ACTIVE_DVR:
		c.Writer.Header().Set("Retry-After", "5")
		c.String(http.StatusServiceUnavailable, "active dvr — segment not yet uploaded")
	default:
		c.String(http.StatusServiceUnavailable, "asset not playable: %s", res.Error)
	}
}

func (s *Server) serverError(c *gin.Context, what string, err error) {
	if s.logger != nil {
		s.logger.WithError(err).WithField("op", what).Error("relay server error")
	}
	c.String(http.StatusInternalServerError, "%s: %v", what, err)
}

func safeRelayPathSegment(seg string) bool {
	if seg == "" || seg == "." || seg == ".." {
		return false
	}
	return !strings.ContainsAny(seg, `/\`)
}
