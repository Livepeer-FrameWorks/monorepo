package handlers

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"frameworks/api_sidecar/internal/admission"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	pb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto"

	"github.com/sirupsen/logrus"
)

// unsafeWrapperExt inspects a source URL and returns the wrapper extension
// when it's one Mist cannot open over HTTP (.avi/.flv/.m4v). Returns "" for
// safe wrappers or unrecognized URLs.
//
// FLV uses fopen directly (mistserver/src/input/input_flv.cpp); AV input
// only auto-matches local paths (input_av.cpp); .m4v has no http
// source_match anywhere in MistServer.
func unsafeWrapperExt(sourceURL string) string {
	if sourceURL == "" {
		return ""
	}
	u, err := url.Parse(sourceURL)
	if err != nil {
		return ""
	}
	ext := strings.ToLower(filepath.Ext(u.Path))
	switch ext {
	case ".avi", ".flv", ".m4v":
		return ext
	default:
		return ""
	}
}

func (h *ProcessingJobHandler) buildLocalProcessingSourceURL(req *pb.ProcessingJobRequest) string {
	params := req.GetParams()
	sourceStream := strings.TrimSpace(params["source_stream_name"])
	if sourceStream == "" {
		return ""
	}
	sourceFormat := strings.TrimSpace(params["source_format"])
	if sourceFormat == "" {
		sourceFormat = "mkv"
	}
	startUnix, startErr := strconv.ParseInt(params["source_start_unix"], 10, 64)
	stopUnix, stopErr := strconv.ParseInt(params["source_stop_unix"], 10, 64)
	if startErr != nil || stopErr != nil || stopUnix <= startUnix {
		return ""
	}

	query := url.Values{}
	if params["source_kind"] == "live" {
		query.Set("startunix", strconv.FormatInt(startUnix-time.Now().Unix(), 10))
		query.Set("duration", strconv.FormatInt(stopUnix-startUnix, 10))
	} else {
		query.Set("startunix", strconv.FormatInt(startUnix, 10))
		query.Set("stopunix", strconv.FormatInt(stopUnix, 10))
	}

	base := deriveProcessingMistHTTPBase(h.mistServerURL)
	u, err := url.Parse(strings.TrimRight(base, "/"))
	if err != nil || u.Host == "" {
		return fmt.Sprintf("%s/%s.%s?%s", strings.TrimRight(base, "/"), sourceStream, sourceFormat, query.Encode())
	}
	u.Path = strings.TrimRight(u.Path, "/") + "/" + sourceStream + "." + sourceFormat
	u.RawQuery = query.Encode()
	return u.String()
}

func isClipProcessingSource(req *pb.ProcessingJobRequest) bool {
	params := req.GetParams()
	switch strings.TrimSpace(params["source_kind"]) {
	case "live", "dvr_rolling", "chapter":
		return strings.TrimSpace(params["source_stream_name"]) != ""
	default:
		return false
	}
}

func (h *ProcessingJobHandler) processingOutputPath(req *pb.ProcessingJobRequest, clipSource bool) (string, string, error) {
	outputDir := filepath.Join(h.storagePath, "vod")
	if clipSource {
		outputStreamName := strings.TrimSpace(req.GetParams()["output_stream_name"])
		if outputStreamName == "" {
			return "", "", fmt.Errorf("clip processing output stream unavailable")
		}
		outputDir = filepath.Join(h.storagePath, "clips", outputStreamName)
	}
	return outputDir, filepath.Join(outputDir, req.GetArtifactHash()+".mkv"), nil
}

func processingSourceExt(req *pb.ProcessingJobRequest) string {
	format := strings.Trim(strings.ToLower(strings.TrimSpace(req.GetParams()["source_format"])), ".")
	switch format {
	case "mp4", "mov", "mkv", "webm", "ts":
		return "." + format
	default:
		return ".mkv"
	}
}

func deriveProcessingMistHTTPBase(base string) string {
	u, err := url.Parse(strings.TrimSpace(base))
	if err != nil || u.Host == "" {
		host := strings.TrimPrefix(base, "http://")
		host = strings.TrimPrefix(host, "https://")
		host = strings.Split(host, "/")[0]
		host = strings.Split(host, ":")[0]
		if host == "" {
			return strings.TrimRight(base, "/")
		}
		return "http://" + host + ":8080"
	}
	port := u.Port()
	if port == "" || port == "4242" {
		port = "8080"
	}
	return u.Scheme + "://" + u.Hostname() + ":" + port
}

// stageUnsafeWrapper downloads the source URL to {storage}/processing/<hash><ext>
// before Mist's PushStart fires. The STREAM_SOURCE handler returns this
// staged path locally (no Foghorn roundtrip) so Mist's local-only FLV/AV
// inputs can open the file.
//
// Goes through Decide(IntentUnsafeImportStage) so admission can reject when
// disk is too tight — Foghorn picks another node, or the job retries later.
// The staged file is cleanup-eligible once processing completes (no playback
// lease against it).
func (h *ProcessingJobHandler) stageUnsafeWrapper(log *logrus.Entry, req *pb.ProcessingJobRequest, ext string) (string, error) {
	return h.stageSourceToProcessingDir(log, req, req.GetSourceUrl(), ext, admission.IntentUnsafeImportStage, "unsafe-wrapper")
}

func (h *ProcessingJobHandler) stageProcessingSource(log *logrus.Entry, req *pb.ProcessingJobRequest, sourceURL string) (string, error) {
	return h.stageSourceToProcessingDir(log, req, sourceURL, processingSourceExt(req), admission.IntentProcessingSourceStage, "processing-source")
}

func (h *ProcessingJobHandler) stageSourceToProcessingDir(log *logrus.Entry, req *pb.ProcessingJobRequest, sourceURL, ext string, intent admission.StorageIntent, label string) (string, error) {
	sourceURL = strings.TrimSpace(sourceURL)
	if sourceURL == "" {
		return "", fmt.Errorf("source URL is required")
	}
	procDir := filepath.Join(h.storagePath, "processing")
	if err := os.MkdirAll(procDir, 0o755); err != nil {
		return "", fmt.Errorf("mkdir processing dir: %w", err)
	}

	target := filepath.Join(procDir, req.GetArtifactHash()+ext)

	// Admission gate. We don't know the source size ahead of time —
	// HEAD-then-decide gives us a real number for the priority hierarchy
	// instead of guessing zero. On HEAD failure (S3 quirk, network) fall
	// back to admit-then-download with size=0; the engine handles unknowns.
	var sizeBytes uint64
	if size, ok := headContentLength(sourceURL); ok {
		sizeBytes = size
	}
	sm := GetStorageManager()
	if sm != nil {
		decision, err := sm.Decide(context.Background(), procDir, intent, sizeBytes)
		if err != nil || decision == admission.CacheReject {
			return "", fmt.Errorf("admission rejected %s stage (size=%d): %w", label, sizeBytes, err)
		}
	}

	// Download to a tmpfile, atomic-rename. Existing target is removed so
	// a retry doesn't serve stale content from a previous failed attempt.
	tmp := target + ".partial"
	if err := os.Remove(target); err != nil && !os.IsNotExist(err) {
		return "", fmt.Errorf("clear stale stage: %w", err)
	}
	out, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return "", fmt.Errorf("create stage tmpfile: %w", err)
	}
	resp, err := httpGetSource(sourceURL)
	if err != nil {
		_ = out.Close()
		_ = os.Remove(tmp)
		return "", fmt.Errorf("fetch source: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		_ = out.Close()
		_ = os.Remove(tmp)
		return "", fmt.Errorf("source HTTP status %d", resp.StatusCode)
	}
	written, copyErr := io.Copy(out, resp.Body)
	if copyErr != nil {
		_ = out.Close()
		_ = os.Remove(tmp)
		return "", fmt.Errorf("copy source: %w", copyErr)
	}
	if err := out.Sync(); err != nil {
		_ = out.Close()
		_ = os.Remove(tmp)
		return "", fmt.Errorf("fsync stage: %w", err)
	}
	if err := out.Close(); err != nil {
		_ = os.Remove(tmp)
		return "", fmt.Errorf("close stage: %w", err)
	}
	if err := os.Rename(tmp, target); err != nil {
		_ = os.Remove(tmp)
		return "", fmt.Errorf("rename stage: %w", err)
	}
	log.WithFields(logging.Fields{
		"bytes":      written,
		"stage_type": label,
	}).Debug("Wrote processing source stage")
	return target, nil
}

// headContentLength issues a HEAD to learn the source size for admission.
// Returns (size, true) on success.
func headContentLength(sourceURL string) (uint64, bool) {
	req, err := http.NewRequestWithContext(context.Background(), http.MethodHead, sourceURL, nil)
	if err != nil {
		return 0, false
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK || resp.ContentLength <= 0 {
		return 0, false
	}
	return uint64(resp.ContentLength), true
}

// httpGetSource fetches a presigned source URL. Uses
// NewRequestWithContext so the project's noctx lint check is satisfied;
// cancellation is via process shutdown for now.
func httpGetSource(sourceURL string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, sourceURL, nil)
	if err != nil {
		return nil, err
	}
	return http.DefaultClient.Do(req)
}
