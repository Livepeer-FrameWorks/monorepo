package relay

import (
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"

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
// identity stays in the path (not a query parameter) so Mist's input
// + ".dtsh" sidecar mutation produces the right URL. The legacy flat
// /clip/<file> shape is rejected — Foghorn always emits the
// stream-scoped path now that output_stream_name is required on
// ClipPullRequest.
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
	p = strings.TrimPrefix(p, "/")
	if p == "" {
		return "", ""
	}
	parts := strings.SplitN(p, "/", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", ""
	}
	return parts[0], parts[1]
}

func (s *Server) serveFileWithStream(c *gin.Context, kind, streamInternal string) {
	file := c.Param("file")
	if file == "" || file == "/" {
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

	// Clip paths are always stream-scoped now (Foghorn requires
	// output_stream_name on ClipPullRequest, and the relay route
	// matches /clip/<stream>/<file>). VOD/upload paths stay flat at
	// storage/<kind>/<file>.
	var localPath string
	if kind == "clip" {
		if streamInternal == "" {
			c.AbortWithStatus(http.StatusNotFound)
			return
		}
		localPath = s.nestedClipPath(streamInternal, file)
	} else {
		localPath = s.canonicalFilePath(kind, file)
	}
	if served := s.serveWarmIfPresent(c, localPath); served {
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
	if res.State != pb.AssetState_ASSET_STATE_PLAYABLE || res.MediaPresignedURL == "" {
		s.notPlayable(c, res)
		return
	}
	s.fetchAndServe(c, kind, hash, ext, localPath, res)
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
	http.ServeContent(c.Writer, c.Request, filepath.Base(localPath), info.ModTime(), f)
	return true
}

// serveUpload handles processing-input reads. Always memory-only per
// admission policy (sequential one-shot). The route does not include a
// hash subdirectory — uploads are flat under {basePath}/upload/.
func (s *Server) serveUpload(c *gin.Context) {
	file := c.Param("file")
	if file == "" || file == "/" {
		c.AbortWithStatus(http.StatusNotFound)
		return
	}
	file = strings.TrimPrefix(file, "/")

	localPath := s.canonicalUploadPath(file)
	if info, err := os.Stat(localPath); err == nil && info.Mode().IsRegular() {
		// Unsafe-wrapper staging may have already materialized this file
		// locally; serve directly when present.
		f, err := os.Open(localPath)
		if err != nil {
			s.serverError(c, "open warm upload", err)
			return
		}
		defer f.Close()
		http.ServeContent(c.Writer, c.Request, filepath.Base(localPath), info.ModTime(), f)
		return
	}

	hash := hashFromFile(file)
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
	if res.State != pb.AssetState_ASSET_STATE_PLAYABLE || res.MediaPresignedURL == "" {
		s.notPlayable(c, res)
		return
	}
	// Upload reads are always memory-only — sequential one-shot. Bypass disk
	// admission entirely.
	s.streamRangeNoCache(c, res)
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
func (s *Server) fetchAndServe(c *gin.Context, kind, hash, ext, localPath string, res *ResolveResult) {
	if c.Request.Method == http.MethodHead {
		s.streamRangeNoCache(c, res)
		return
	}
	s.serveViaBlockCache(c, kind, hash, ext, localPath, res)
}

// streamRangeNoCache forwards Mist's Range to S3, copies the response
// straight back, and never touches disk. Used for memory-only admission
// outcomes and for processing-input reads.
func (s *Server) streamRangeNoCache(c *gin.Context, res *ResolveResult) {
	req, err := http.NewRequestWithContext(c.Request.Context(), c.Request.Method, res.MediaPresignedURL, nil)
	if err != nil {
		s.serverError(c, "build s3 request", err)
		return
	}
	if rng := c.Request.Header.Get("Range"); rng != "" {
		req.Header.Set("Range", rng)
	}
	resp, err := s.httpc.Do(req)
	if err != nil {
		s.serverError(c, "s3 fetch", err)
		return
	}
	defer resp.Body.Close()

	// Mirror status and the headers that Mist cares about. Content-Length,
	// Content-Range, Accept-Ranges drive seekability detection in
	// HTTP::URIReader.
	for _, h := range []string{"Content-Length", "Content-Range", "Content-Type", "Accept-Ranges", "ETag", "Last-Modified"} {
		if v := resp.Header.Get(h); v != "" {
			c.Writer.Header().Set(h, v)
		}
	}
	if c.Writer.Header().Get("Accept-Ranges") == "" {
		c.Writer.Header().Set("Accept-Ranges", "bytes")
	}
	c.Writer.WriteHeader(resp.StatusCode)
	if c.Request.Method == http.MethodHead {
		return
	}
	buf := make([]byte, 256*1024)
	if _, copyErr := io.CopyBuffer(c.Writer, resp.Body, buf); copyErr != nil {
		if s.logger != nil && !errors.Is(copyErr, context.Canceled) {
			s.logger.WithError(copyErr).Debug("relay no-cache stream aborted")
		}
	}
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
