package relay

import (
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/gin-gonic/gin"

	"frameworks/api_sidecar/internal/control"
	pb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto"
)

// serveSidecarGetWithStream handles GET/HEAD for sidecar requests.
// Mist computes the dtsh URL as source + ".dtsh"; the relay must
// respond even before any local dtsh exists so Mist can decide whether
// to generate one.
//
// Branches:
//  1. Local dtsh present → http.ServeContent (sendfile, range support).
//  2. Cold + Foghorn has dtsh_presigned_get → fetch from S3, optionally
//     cache to disk per admission policy.
//  3. No dtsh anywhere → 404 (triggers Mist to generate + PUT it).
//
// streamInternal is the path-encoded stream context for clip URLs
// (clip/<stream>/<file>.dtsh). It selects the nested sidecar layout so
// generated sidecars land next to the clip's media file.
func (s *Server) serveSidecarGetWithStream(c *gin.Context, kind, hash, file, streamInternal string) {
	nestedPath := s.nestedSidecarPathFor(kind, file, streamInternal)
	if nestedPath != "" {
		if info, err := os.Stat(nestedPath); err == nil && info.Mode().IsRegular() && info.Size() > 0 {
			f, err := os.Open(nestedPath)
			if err != nil {
				s.serverError(c, "open warm dtsh (nested)", err)
				return
			}
			defer f.Close()
			http.ServeContent(c.Writer, c.Request, filepath.Base(nestedPath), info.ModTime(), f)
			return
		}
	}

	localPath := s.canonicalFilePath(kind, file)
	if info, err := os.Stat(localPath); err == nil && info.Mode().IsRegular() && info.Size() > 0 {
		f, err := os.Open(localPath)
		if err != nil {
			s.serverError(c, "open warm dtsh", err)
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
		Hint:      pb.RelayResolveRequest_RELAY_HINT_RANDOM_ACCESS,
	}
	res, err := s.resolveCached(rc)
	if err != nil {
		s.serverError(c, "relay resolve (dtsh)", err)
		return
	}
	if res.DtshPresignedGet == "" {
		// Foghorn has no sidecar uploaded yet. Returning 404 is the signal
		// for Mist to generate one and PUT it back.
		c.Status(http.StatusNotFound)
		return
	}

	// Fetch dtsh from S3 and write it through to disk while streaming
	// the response. Sidecars are small (typically <10 MiB) and dropping
	// the local copy would re-fetch on every replay; we always tee for
	// HEAD-less full GETs. HEAD and ranged requests stream-only so the
	// disk write happens at most once per cold node (next GET from any
	// session will hit the warm branch above).
	req, err := http.NewRequestWithContext(c.Request.Context(), c.Request.Method, res.DtshPresignedGet, nil)
	if err != nil {
		s.serverError(c, "build s3 dtsh request", err)
		return
	}
	if rng := c.Request.Header.Get("Range"); rng != "" {
		req.Header.Set("Range", rng)
	}
	resp, err := s.httpc.Do(req)
	if err != nil {
		s.serverError(c, "s3 dtsh fetch", err)
		return
	}
	defer resp.Body.Close()

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

	rangedRequest := c.Request.Header.Get("Range") != ""
	cacheLocally := !rangedRequest && resp.StatusCode == http.StatusOK
	if !cacheLocally {
		if _, copyErr := io.Copy(c.Writer, resp.Body); copyErr != nil && s.logger != nil {
			s.logger.WithError(copyErr).Debug("relay dtsh stream aborted")
		}
		return
	}

	// Tee to disk: tmpfile → atomic rename, so a half-written file
	// never ends up at the canonical path.
	if mkErr := os.MkdirAll(filepath.Dir(localPath), 0o755); mkErr != nil {
		// Disk write failed before it began; fall back to stream-only
		// so playback still works.
		if _, copyErr := io.Copy(c.Writer, resp.Body); copyErr != nil && s.logger != nil {
			s.logger.WithError(copyErr).Debug("relay dtsh stream aborted (mkdir failed)")
		}
		return
	}
	tmp := localPath + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		if _, err := io.Copy(c.Writer, resp.Body); err != nil && s.logger != nil {
			s.logger.WithError(err).Debug("relay dtsh stream aborted (open tmp failed)")
		}
		return
	}
	// Client first, disk on a separate goroutine behind a bounded
	// channel. Mist always receives the sidecar from S3 at network
	// speed; the cache fill is opportunistic and abandoned silently if
	// disk fails or falls behind.
	tee := newTolerantTee(c.Writer, f, func(diskErr error) {
		if s.logger != nil {
			s.logger.WithError(diskErr).WithField("local_path", localPath).Debug("relay dtsh disk write failed or fell behind; abandoning cache")
		}
	})
	_, copyErr := io.Copy(tee, resp.Body)
	tee.Close() // drain the disk worker so SecondaryAlive reflects final state
	if copyErr != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		if s.logger != nil {
			s.logger.WithError(copyErr).Debug("relay dtsh stream aborted (client gone)")
		}
		return
	}
	// Rename only when the disk side actually captured the full sidecar.
	if !tee.SecondaryAlive() {
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
// Mist's externalWriter path. Two sinks for the body:
//
//  1. Local disk under the canonical sidecar path (tmpfile → fsync →
//     atomic rename). For stream-scoped clips this is the nested
//     clips/<stream>/<file> path so the sidecar lands next to the
//     writer's media file. Mist gets 200 OK as soon as this completes;
//     next cold playback on this node skips header generation.
//  2. S3 via the dtsh_presigned_put URL Foghorn handed back on the most
//     recent RelayResolve for this asset. Done in a background goroutine
//     so Mist isn't blocked on S3 latency. The FreezeHandoff is still
//     called as a backstop — the existing freeze reconciler picks the
//     sidecar up on its next pass if the direct PUT failed or no
//     presigned URL was minted.
func (s *Server) putSidecarWithStream(c *gin.Context, kind, streamInternal string) {
	file := strings.TrimPrefix(c.Param("file"), "/")
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
	if _, err := io.Copy(f, c.Request.Body); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		if !errors.Is(err, io.ErrUnexpectedEOF) {
			s.serverError(c, "write sidecar body", err)
		}
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
	if err := os.Rename(tmp, localPath); err != nil {
		s.serverError(c, "rename sidecar", err)
		return
	}

	// S3 PUT via the presigned URL the relay cached on the last resolve.
	// Async + best-effort: the local file is durable; freeze reconciler
	// covers misses. On PUT success we send SyncComplete with
	// dtsh_included=true so Foghorn flips foghorn.artifacts.dtsh_synced and
	// future cold nodes see DtshPresignedGet on resolve.
	if putURL := s.cachedDtshPutURL(kind, hash); putURL != "" {
		go s.uploadDtshToS3(kind, hash, putURL, localPath)
	}
	if s.freeze != nil {
		s.freeze.OnLocalDtshGenerated(kind, hash, localPath)
	}
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

// cachedDtshPutURL returns the .dtsh PUT URL Foghorn minted on the most
// recent RelayResolve for this asset, or "" if none is cached. Only
// consults the in-memory resolve cache — does not issue a fresh resolve to
// avoid a control-stream roundtrip on every PUT.
func (s *Server) cachedDtshPutURL(kind, hash string) string {
	if s.cache == nil {
		return ""
	}
	if r, ok := s.cache.Get(kind, hash); ok {
		return r.DtshPresignedPut
	}
	return ""
}

// uploadDtshToS3 PUTs the local .dtsh file to the supplied presigned URL
// and, on success, sends a SyncComplete to Foghorn with dtsh_included=true
// so the artifact row gets dtsh_synced=true and future cold-node resolves
// can return DtshPresignedGet. Best-effort: failures fall through to the
// freeze reconciler.
func (s *Server) uploadDtshToS3(kind, hash, putURL, localPath string) {
	body, err := os.Open(localPath)
	if err != nil {
		if s.logger != nil {
			s.logger.WithError(err).WithField("local_path", localPath).Debug("relay direct .dtsh upload: open local file failed")
		}
		return
	}
	defer body.Close()
	info, err := body.Stat()
	if err != nil {
		return
	}
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPut, putURL, body)
	if err != nil {
		return
	}
	req.ContentLength = info.Size()
	resp, err := s.httpc.Do(req)
	if err != nil {
		if s.logger != nil {
			s.logger.WithError(err).Debug("relay direct .dtsh upload: PUT failed")
		}
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		if s.logger != nil {
			s.logger.WithField("status", resp.StatusCode).Debug("relay direct .dtsh upload: non-2xx")
		}
		return
	}

	// Tell Foghorn the sidecar is durable. Synthesizes a request_id so the
	// SyncComplete is well-formed; assetHash is the artifact (vod/clip)
	// hash — DVR sidecars don't flow through this path.
	if kind != "vod" && kind != "clip" {
		return
	}
	rid, idErr := newRequestID()
	if idErr != nil {
		return
	}
	if err := control.SendSyncComplete(rid, hash, "success", "", 0, "", true, false); err != nil && s.logger != nil {
		s.logger.WithError(err).WithField("asset_hash", hash).Debug("relay direct .dtsh upload: SyncComplete send failed; freeze reconciler will retry")
	}
}
