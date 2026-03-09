package handlers

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"frameworks/pkg/logging"
	"frameworks/pkg/mist"
	pb "frameworks/pkg/proto"

	"github.com/sirupsen/logrus"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// ProcessingJobHandler handles VOD processing jobs from Foghorn.
// Activates the processing+{hash} wildcard stream in MistServer.
// STREAM_SOURCE provides the presigned S3 URL for the source.
// STREAM_PROCESS provides the MistProc* config (VP9/thumbs/audio) from Commodore.
type ProcessingJobHandler struct {
	logger        logging.Logger
	mistServerURL string
	storagePath   string
}

// pendingJobs tracks in-flight processing jobs, signaled by PUSH_END.
var (
	pendingJobs   = map[string]chan struct{}{}
	pendingJobsMu sync.Mutex
)

// HasPendingJob returns true if a processing job is currently in-flight for the stream.
func HasPendingJob(streamName string) bool {
	pendingJobsMu.Lock()
	_, ok := pendingJobs[streamName]
	pendingJobsMu.Unlock()
	return ok
}

// SignalProcessingComplete is called from HandlePushEnd when a processing+ push ends.
func SignalProcessingComplete(streamName string) {
	pendingJobsMu.Lock()
	ch, ok := pendingJobs[streamName]
	pendingJobsMu.Unlock()
	if ok {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}

func NewProcessingJobHandler(logger logging.Logger, mistServerURL, storagePath string) *ProcessingJobHandler {
	return &ProcessingJobHandler{
		logger:        logger,
		mistServerURL: mistServerURL,
		storagePath:   storagePath,
	}
}

// Handle executes a processing job: activates the processing+ wildcard stream,
// starts a push to local disk as MKV, waits for PUSH_END, reports result.
func (h *ProcessingJobHandler) Handle(req *pb.ProcessingJobRequest, send func(*pb.ControlMessage)) {
	log := h.logger.WithFields(logging.Fields{
		"job_id":        req.GetJobId(),
		"job_type":      req.GetJobType(),
		"artifact_hash": req.GetArtifactHash(),
	})

	log.Info("Processing job received")
	streamName := "processing+" + req.GetArtifactHash()

	// For segmented (HLS) sources, rewrite manifest with presigned segment URLs
	var hlsManifestPath string
	if isHLSSource(req.GetSourceUrl(), req.GetParams()) {
		var err error
		hlsManifestPath, err = h.rewriteHLSManifest(log, req)
		if err != nil {
			h.sendResult(send, req.GetJobId(), "failed", fmt.Sprintf("HLS manifest rewrite failed: %v", err), nil, "", 0)
			return
		}
		log.WithField("local_manifest", hlsManifestPath).Info("Rewrote HLS manifest for processing")
	}

	// Register completion channel BEFORE activating stream
	doneCh := make(chan struct{}, 1)
	pendingJobsMu.Lock()
	pendingJobs[streamName] = doneCh
	pendingJobsMu.Unlock()

	defer func() {
		pendingJobsMu.Lock()
		delete(pendingJobs, streamName)
		pendingJobsMu.Unlock()
	}()

	mistClient := mist.NewClient(h.logger)
	if h.mistServerURL != "" {
		mistClient.BaseURL = h.mistServerURL
	}

	// MKV: only container MistServer can push to AND re-ingest that supports
	// the full codec set (H264/VP9/JPEG/AAC/opus). Raw absolute path — MistServer
	// matches against push_urls pattern "/*.mkv" → MistOutEBML.
	// Output goes to vod/ so the artifact poller discovers it for Foghorn registration.
	vodDir := filepath.Join(h.storagePath, "vod")
	if err := os.MkdirAll(vodDir, 0755); err != nil {
		log.WithError(err).Error("Failed to create vod directory")
		h.sendResult(send, req.GetJobId(), "failed", fmt.Sprintf("mkdir failed: %v", err), nil, "", 0)
		return
	}
	outputPath := filepath.Join(vodDir, req.GetArtifactHash()+".mkv")
	if err := mistClient.PushStart(streamName, outputPath); err != nil {
		log.WithError(err).Error("Failed to start push")
		h.sendResult(send, req.GetJobId(), "failed", fmt.Sprintf("push_start failed: %v", err), nil, "", 0)
		return
	}
	log.WithField("output_path", outputPath).Info("Started push for processing stream")

	// Poll for stream metadata while processing runs
	outputs := h.pollStreamMetadata(log, mistClient, streamName)

	// Wait for PUSH_END or timeout
	select {
	case <-doneCh:
		log.Info("Processing completed (PUSH_END received)")
	case <-time.After(30 * time.Minute):
		log.Warn("Processing timed out")
		h.sendResult(send, req.GetJobId(), "failed", "processing timed out", nil, outputPath, 0)
		return
	}

	// Get output file size for artifact registration
	var outputSizeBytes int64
	if fi, err := os.Stat(outputPath); err == nil {
		outputSizeBytes = fi.Size()
	}

	// Send result with output path so Foghorn can register the artifact
	// in the warm cache immediately (same pattern as defrost completion).
	// This must happen BEFORE DTSH generation because vod+ STREAM_SOURCE
	// resolves via Foghorn's in-memory state.
	h.sendResult(send, req.GetJobId(), "completed", "", outputs, outputPath, outputSizeBytes)
	log.Info("Processing job result sent, artifact registered with Foghorn")

	// Generate DTSH by booting the output as vod+ (no MistProc* re-trigger).
	// Foghorn now has the artifact registered, so vod+ STREAM_SOURCE resolves.
	vodStreamName := "vod+" + req.GetInternalName()
	if err := GenerateDTSH(h.mistServerURL, vodStreamName, log); err != nil {
		log.WithError(err).Warn("DTSH generation failed (will be generated on first playback)")
	}

	// Clean up rewritten HLS manifest (contains presigned URLs that expire)
	if hlsManifestPath != "" {
		if err := os.Remove(hlsManifestPath); err != nil && !os.IsNotExist(err) {
			log.WithError(err).Warn("Failed to remove rewritten HLS manifest")
		}
	}

	// Trigger storage check so the .mkv + .dtsh freeze to S3 promptly
	TriggerStorageCheck()
}

// pollStreamMetadata waits for MistServer to parse the stream headers and
// extracts codec, resolution, duration, etc. from the stream info API.
func (h *ProcessingJobHandler) pollStreamMetadata(log *logrus.Entry, mistClient *mist.Client, streamName string) map[string]string {
	outputs := map[string]string{}

	for i := 0; i < 30; i++ {
		time.Sleep(1 * time.Second)

		info, err := mistClient.GetStreamInfo(streamName)
		if err != nil {
			continue
		}
		if info.Metadata == nil {
			continue
		}

		extracted := extractTrackMetadata(info.Metadata)
		if len(extracted) > 0 {
			return extracted
		}
	}

	log.Warn("Timed out waiting for stream metadata")
	return outputs
}

// extractTrackMetadata extracts video/audio metadata from MistServer's stream info.
func extractTrackMetadata(meta map[string]interface{}) map[string]string {
	outputs := map[string]string{}

	metaRaw, ok := meta["meta"]
	if !ok {
		return outputs
	}
	metaMap, ok := metaRaw.(map[string]interface{})
	if !ok {
		return outputs
	}
	tracksRaw, ok := metaMap["tracks"]
	if !ok {
		return outputs
	}
	tracks, ok := tracksRaw.(map[string]interface{})
	if !ok {
		return outputs
	}

	for name, trackRaw := range tracks {
		track, ok := trackRaw.(map[string]interface{})
		if !ok {
			continue
		}

		if strings.HasPrefix(name, "video") {
			if v, ok := track["codec"].(string); ok {
				outputs["video_codec"] = v
			}
			if v, ok := track["width"].(float64); ok {
				outputs["width"] = strconv.Itoa(int(v))
			}
			if v, ok := track["height"].(float64); ok {
				outputs["height"] = strconv.Itoa(int(v))
			}
			if w, ok := outputs["width"]; ok {
				if ht, ok := outputs["height"]; ok {
					outputs["resolution"] = w + "x" + ht
				}
			}
			if v, ok := track["fpks"].(float64); ok && v > 0 {
				outputs["fps"] = fmt.Sprintf("%.2f", v/1000.0)
			}
			if v, ok := track["bps"].(float64); ok && v > 0 {
				outputs["bitrate_kbps"] = strconv.Itoa(int(v / 1000))
			}
		}

		if strings.HasPrefix(name, "audio") {
			if v, ok := track["codec"].(string); ok {
				outputs["audio_codec"] = v
			}
			if v, ok := track["channels"].(float64); ok {
				outputs["audio_channels"] = strconv.Itoa(int(v))
			}
			if v, ok := track["rate"].(float64); ok {
				outputs["audio_sample_rate"] = strconv.Itoa(int(v))
			}
		}
	}

	if v, ok := metaMap["lastms"].(float64); ok && v > 0 {
		outputs["duration_ms"] = strconv.Itoa(int(v))
	}

	return outputs
}

// GenerateDTSH boots a stream via the /json_{streamName}.js endpoint to trigger
// DTSH generation. MistServer's input module reads headers and writes the .dtsh
// file as a side effect. Works for any stream type (vod+, processing+, etc.)
// because our fork boots offline streams on HTTP GET.
func GenerateDTSH(mistServerURL, streamName string, log *logrus.Entry) error {
	if mistServerURL == "" {
		return fmt.Errorf("MISTSERVER_URL not configured")
	}
	url := strings.TrimRight(mistServerURL, "/") + "/json_" + streamName + ".js"

	for i := 0; i < 15; i++ {
		if i > 0 {
			time.Sleep(2 * time.Second)
		}
		httpReq, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, url, nil)
		resp, err := http.DefaultClient.Do(httpReq)
		if err != nil {
			log.WithError(err).Debug("DTSH generation: json endpoint not ready")
			continue
		}
		body, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			log.WithField("status", resp.StatusCode).Debug("DTSH generation: json endpoint returned error")
			continue
		}

		var data map[string]interface{}
		if err := json.Unmarshal(body, &data); err != nil {
			continue
		}
		if _, hasError := data["error"]; hasError {
			log.WithField("error", data["error"]).Debug("DTSH generation: stream not ready")
			continue
		}
		log.Info("DTSH generation completed via json endpoint")
		return nil
	}
	return fmt.Errorf("timed out waiting for DTSH generation")
}

// isHLSSource detects if the source is an HLS manifest (segmented).
func isHLSSource(sourceURL string, params map[string]string) bool {
	if strings.HasSuffix(strings.ToLower(sourceURL), ".m3u8") {
		return true
	}
	_, ok := params["segment_urls"]
	return ok
}

// rewriteHLSManifest downloads the HLS manifest, rewrites segment paths
// to presigned HTTPS URLs, and saves to local disk for MistServer to read.
func (h *ProcessingJobHandler) rewriteHLSManifest(log *logrus.Entry, req *pb.ProcessingJobRequest) (string, error) {
	params := req.GetParams()
	manifestURL := req.GetSourceUrl()

	httpReq, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, manifestURL, nil)
	resp, err := http.DefaultClient.Do(httpReq)
	if err != nil {
		return "", fmt.Errorf("download manifest: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("manifest download returned %d", resp.StatusCode)
	}

	// segment_urls: newline-separated "relative_path=presigned_url" pairs
	segmentURLs := map[string]string{}
	if raw, ok := params["segment_urls"]; ok {
		for _, pair := range strings.Split(raw, "\n") {
			parts := strings.SplitN(pair, "=", 2)
			if len(parts) == 2 {
				segmentURLs[parts[0]] = parts[1]
			}
		}
	}

	var rewritten strings.Builder
	scanner := bufio.NewScanner(resp.Body)
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "#") && strings.TrimSpace(line) != "" {
			segName := strings.TrimSpace(line)
			if presigned, ok := segmentURLs[segName]; ok {
				rewritten.WriteString(presigned)
			} else {
				rewritten.WriteString(line)
			}
		} else {
			rewritten.WriteString(line)
		}
		rewritten.WriteString("\n")
	}
	if err := scanner.Err(); err != nil {
		return "", fmt.Errorf("reading manifest: %w", err)
	}

	procDir := filepath.Join(h.storagePath, "processing")
	if err := os.MkdirAll(procDir, 0755); err != nil {
		return "", fmt.Errorf("create processing dir: %w", err)
	}
	localPath := filepath.Join(procDir, req.GetArtifactHash()+".m3u8")
	if err := os.WriteFile(localPath, []byte(rewritten.String()), 0644); err != nil {
		return "", fmt.Errorf("write rewritten manifest: %w", err)
	}

	log.WithFields(logging.Fields{
		"segments_mapped": len(segmentURLs),
		"local_path":      localPath,
	}).Info("Rewrote HLS manifest with presigned segment URLs")

	return localPath, nil
}

func (h *ProcessingJobHandler) sendResult(send func(*pb.ControlMessage), jobID, status, errMsg string, outputs map[string]string, outputPath string, outputSizeBytes int64) {
	if send == nil {
		return
	}
	send(&pb.ControlMessage{
		Payload: &pb.ControlMessage_ProcessingJobResult{ProcessingJobResult: &pb.ProcessingJobResult{
			JobId:           jobID,
			Status:          status,
			Error:           errMsg,
			Outputs:         outputs,
			OutputPath:      outputPath,
			OutputSizeBytes: outputSizeBytes,
		}},
		SentAt: timestamppb.Now(),
	})
}
