package relay

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/gin-gonic/gin"

	"frameworks/api_sidecar/internal/control"
	"frameworks/api_sidecar/internal/dtsh"
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
	forceCloseForMistReader(c)

	nestedPath := s.nestedSidecarPathFor(kind, file, streamInternal)
	if nestedPath != "" {
		if info, err := os.Stat(nestedPath); err == nil && info.Mode().IsRegular() && info.Size() > 0 {
			if err := dtsh.ValidateFile(nestedPath); err != nil {
				if s.logger != nil {
					s.logger.WithError(err).WithField("local_path", nestedPath).Warn("relay warm dtsh invalid; removing and returning generation signal")
				}
				_ = os.Remove(nestedPath)
				c.Status(http.StatusNotFound)
				return
			}
			f, err := os.Open(nestedPath)
			if err != nil {
				if s.logger != nil {
					s.logger.WithError(err).WithField("local_path", nestedPath).Debug("relay warm dtsh open failed; returning generation signal")
				}
				c.Status(http.StatusNotFound)
				return
			}
			defer f.Close()
			http.ServeContent(c.Writer, c.Request, filepath.Base(nestedPath), info.ModTime(), f)
			return
		}
	}

	localPath := s.canonicalFilePath(kind, file)
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
		Hint:      pb.RelayResolveRequest_RELAY_HINT_RANDOM_ACCESS,
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

	// Fetch the full sidecar so we can validate its track metadata before
	// handing it to Mist or caching it locally.
	req, err := http.NewRequestWithContext(c.Request.Context(), http.MethodGet, fetchURL, nil)
	if err != nil {
		if s.logger != nil {
			s.logger.WithError(err).WithField("asset_hash", hash).Debug("relay dtsh request build failed; returning generation signal")
		}
		c.Status(http.StatusNotFound)
		return
	}
	if peerBearer != "" {
		req.Header.Set("Authorization", "Bearer "+peerBearer)
	}
	resp, err := s.httpc.Do(req)
	if err != nil {
		if s.logger != nil {
			s.logger.WithError(err).WithField("asset_hash", hash).Debug("relay dtsh fetch failed; returning generation signal")
		}
		s.cache.Delete(kind, hash)
		c.Status(http.StatusNotFound)
		return
	}
	defer resp.Body.Close()

	// Any sidecar fetch failure is a regenerate signal. The actual media GET is
	// the authoritative failure path; source.dtsh must not block Mist from
	// trying to generate a fresh index from the media source.
	if resp.StatusCode >= 400 {
		if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
			s.cache.Delete(kind, hash)
		}
		if s.logger != nil {
			s.logger.WithField("status", resp.StatusCode).WithField("asset_hash", hash).Debug("relay dtsh fetch returned error; returning generation signal")
		}
		c.Status(http.StatusNotFound)
		return
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		if s.logger != nil {
			s.logger.WithError(err).WithField("asset_hash", hash).Debug("relay dtsh fetch body failed; returning generation signal")
		}
		c.Status(http.StatusNotFound)
		return
	}
	if validateErr := dtsh.Validate(body); validateErr != nil {
		if s.logger != nil {
			s.logger.WithError(validateErr).WithField("asset_hash", hash).Warn("relay fetched invalid dtsh; returning generation signal")
		}
		c.Status(http.StatusNotFound)
		return
	}

	for _, h := range []string{"Content-Type", "ETag", "Last-Modified"} {
		if v := resp.Header.Get(h); v != "" {
			c.Writer.Header().Set(h, v)
		}
	}
	if c.Writer.Header().Get("Accept-Ranges") == "" {
		c.Writer.Header().Set("Accept-Ranges", "bytes")
	}
	c.Writer.Header().Set("Content-Length", fmt.Sprintf("%d", len(body)))
	c.Writer.WriteHeader(http.StatusOK)
	if c.Request.Method == http.MethodHead {
		return
	}
	if _, writeErr := c.Writer.Write(body); writeErr != nil && s.logger != nil {
		s.logger.WithError(writeErr).Debug("relay dtsh stream aborted")
	}

	// Cache the validated sidecar locally: tmpfile → atomic rename, so a
	// half-written file never ends up at the canonical path.
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
			s.logger.WithError(err).WithField("local_path", localPath).Warn("relay sidecar PUT contained invalid dtsh")
		}
		c.String(http.StatusBadRequest, "invalid sidecar body")
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
		dtshUpload.WithLabelValues("error").Inc()
		if s.logger != nil {
			s.logger.WithError(err).WithField("local_path", localPath).Debug("relay direct .dtsh upload: open local file failed")
		}
		return
	}
	defer body.Close()
	info, err := body.Stat()
	if err != nil {
		dtshUpload.WithLabelValues("error").Inc()
		return
	}
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPut, putURL, body)
	if err != nil {
		dtshUpload.WithLabelValues("error").Inc()
		return
	}
	req.ContentLength = info.Size()
	resp, err := s.httpc.Do(req)
	if err != nil {
		dtshUpload.WithLabelValues("error").Inc()
		if s.logger != nil {
			s.logger.WithError(err).Debug("relay direct .dtsh upload: PUT failed")
		}
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		dtshUpload.WithLabelValues("error").Inc()
		if s.logger != nil {
			s.logger.WithField("status", resp.StatusCode).Debug("relay direct .dtsh upload: non-2xx")
		}
		return
	}
	dtshUpload.WithLabelValues("ok").Inc()

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
