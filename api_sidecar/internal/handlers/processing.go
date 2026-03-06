package handlers

import (
	"bufio"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	sidecarcfg "frameworks/api_sidecar/internal/config"
	"frameworks/pkg/logging"
	"frameworks/pkg/mist"
	pb "frameworks/pkg/proto"

	"github.com/sirupsen/logrus"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// ProcessingJobHandler handles VOD processing jobs from Foghorn.
// Creates a temporary non-wildcard stream (process_{hash}) in MistServer
// with the source and MistProc* processes configured per-job.
// Global triggers (STREAM_SOURCE, PUSH_END) fire for it; Foghorn prefix-matches "process_".
type ProcessingJobHandler struct {
	logger        logging.Logger
	mistServerURL string
	storagePath   string
}

func NewProcessingJobHandler(logger logging.Logger, mistServerURL, storagePath string) *ProcessingJobHandler {
	return &ProcessingJobHandler{
		logger:        logger,
		mistServerURL: mistServerURL,
		storagePath:   storagePath,
	}
}

// Handle executes a processing job: creates temp stream, polls metadata, reports result.
func (h *ProcessingJobHandler) Handle(req *pb.ProcessingJobRequest, send func(*pb.ControlMessage)) {
	log := h.logger.WithFields(logging.Fields{
		"job_id":        req.GetJobId(),
		"job_type":      req.GetJobType(),
		"artifact_hash": req.GetArtifactHash(),
	})

	log.Info("Processing job received")

	sourceURL := req.GetSourceUrl()
	if sourceURL == "" {
		h.sendResult(send, req.GetJobId(), "failed", "no source URL provided", nil)
		return
	}

	// Detect segmented (HLS) sources and rewrite manifest with presigned segment URLs
	isSegmented := isHLSSource(sourceURL, req.GetParams())
	if isSegmented {
		localManifest, err := h.rewriteHLSManifest(log, req)
		if err != nil {
			h.sendResult(send, req.GetJobId(), "failed", fmt.Sprintf("HLS manifest rewrite failed: %v", err), nil)
			return
		}
		sourceURL = localManifest
	}

	mistClient := mist.NewClient(h.logger)
	if h.mistServerURL != "" {
		mistClient.BaseURL = h.mistServerURL
	}

	streamName := "process_" + req.GetArtifactHash()

	// Build processes from current seed config (same as live streams get)
	processes := h.buildProcesses(req.GetParams())

	streamConfig := map[string]map[string]interface{}{
		streamName: {
			"source":        sourceURL,
			"realtime":      true,
			"stop_sessions": 0,
		},
	}
	if len(processes) > 0 {
		streamConfig[streamName]["processes"] = processes
	}

	if err := mistClient.AddStreams(streamConfig); err != nil {
		log.WithError(err).Error("Failed to configure processing stream")
		h.sendResult(send, req.GetJobId(), "failed", fmt.Sprintf("stream config failed: %v", err), nil)
		return
	}
	log.Info("Configured temp processing stream in MistServer")

	// Poll for stream metadata — MistServer parses container headers when the stream boots
	outputs := h.pollStreamMetadata(log, mistClient, streamName)

	// Send result with metadata. When the speed multiplier lands, this will instead
	// wait for PUSH_END to signal full processing completion.
	h.sendResult(send, req.GetJobId(), "completed", "", outputs)

	// Cleanup temp stream
	if err := mistClient.DeleteStream(streamName); err != nil {
		log.WithError(err).Warn("Failed to clean up processing stream")
	}

	if isSegmented {
		localPath := filepath.Join(h.storagePath, "processing", req.GetArtifactHash()+".m3u8")
		_ = os.Remove(localPath)
	}

	log.Info("Processing job completed")
}

// buildProcesses returns MistProc* config for the processing stream.
// Uses the same ProcessingConfig from the current seed (Livepeer, thumbnails, audio transcode).
func (h *ProcessingJobHandler) buildProcesses(params map[string]string) []map[string]interface{} {
	var procs []map[string]interface{}

	proc := sidecarcfg.GetProcessingConfig()

	// Audio transcode (same as live)
	procs = append(procs, map[string]interface{}{
		"process":       "AV",
		"codec":         "opus",
		"track_inhibit": "audio=opus",
		"track_select":  "video=none",
	})
	procs = append(procs, map[string]interface{}{
		"process":       "AV",
		"codec":         "AAC",
		"track_inhibit": "audio=aac",
		"track_select":  "video=none",
	})

	// Livepeer transcoding — VP9 output for VOD
	if proc != nil && proc.GetLivepeerGatewayAvailable() {
		gatewayURL := proc.GetLivepeerGatewayUrl()
		broadcasters := fmt.Sprintf(`[{"address":"%s"}]`, gatewayURL)

		procs = append(procs, map[string]interface{}{
			"process":                "Livepeer",
			"hardcoded_broadcasters": broadcasters,
			"target_profiles": []map[string]interface{}{
				{
					"name":          "480p",
					"bitrate":       512000,
					"fps":           15,
					"height":        480,
					"profile":       "VP9Profile0",
					"track_inhibit": "video=<850x480",
				},
				{
					"name":          "720p",
					"bitrate":       1024000,
					"fps":           25,
					"height":        720,
					"profile":       "VP9Profile0",
					"track_inhibit": "video=<1281x720",
				},
			},
			"track_inhibit": "video=<850x480",
		})
	}

	// Thumbnail sprite generation
	procs = append(procs, map[string]interface{}{
		"process":         "Thumbs",
		"track_select":    "video=maxbps",
		"track_inhibit":   "subtitle=all",
		"inconsequential": true,
		"exit_unmask":     true,
	})

	return procs
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

	resp, err := http.Get(manifestURL) //nolint:gosec // presigned URL from Foghorn
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

func (h *ProcessingJobHandler) sendResult(send func(*pb.ControlMessage), jobID, status, errMsg string, outputs map[string]string) {
	if send == nil {
		return
	}
	send(&pb.ControlMessage{
		Payload: &pb.ControlMessage_ProcessingJobResult{ProcessingJobResult: &pb.ProcessingJobResult{
			JobId:   jobID,
			Status:  status,
			Error:   errMsg,
			Outputs: outputs,
		}},
		SentAt: timestamppb.Now(),
	})
}
