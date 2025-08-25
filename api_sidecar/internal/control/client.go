package control

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"frameworks/pkg/logging"
	pb "frameworks/pkg/proto"
	"frameworks/pkg/validation"

	"github.com/google/uuid"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// Global state for metrics streaming
var (
	currentStream pb.HelmsmanControl_ConnectClient
	currentNodeID string
	currentClient pb.HelmsmanControlClient
)

// Start launches the Helmsman control client and maintains the stream to Foghorn
func Start(logger logging.Logger) {
	addr := os.Getenv("FOGHORN_CONTROL_ADDR")
	if addr == "" {
		addr = "foghorn:18019"
	}
	go runClient(addr, logger)
}

// SendStreamHealth sends stream health updates via the gRPC control stream (replaces HTTP)
func SendStreamHealth(streamName, internalName string, isHealthy bool, details map[string]interface{}) error {
	stream := currentStream
	nodeID := currentNodeID
	if stream == nil {
		return fmt.Errorf("gRPC control stream not connected")
	}

	healthDetails := &pb.StreamHealthDetails{}
	if bufferState, ok := details["buffer_state"].(string); ok {
		healthDetails.BufferState = bufferState
	}
	if status, ok := details["status"].(string); ok {
		healthDetails.Status = status
	}
	if streamDetails, ok := details["stream_details"].(string); ok {
		healthDetails.StreamDetails = streamDetails
	}

	healthUpdate := &pb.StreamHealthUpdate{
		NodeId:       nodeID,
		StreamName:   streamName,
		InternalName: internalName,
		IsHealthy:    isHealthy,
		Timestamp:    time.Now().Unix(),
		Details:      healthDetails,
	}

	msg := &pb.ControlMessage{SentAt: timestamppb.Now(), Payload: &pb.ControlMessage_StreamHealthUpdate{StreamHealthUpdate: healthUpdate}}
	return stream.Send(msg)
}

// SendNodeMetrics sends node metrics via the gRPC control stream (replaces HTTP)
func SendNodeMetrics(nodeMetrics *validation.FoghornNodeUpdate, baseURL, outputsJSON string) error {
	stream := currentStream
	nodeID := currentNodeID
	if stream == nil {
		return fmt.Errorf("gRPC control stream not connected")
	}

	pbUpdate := &pb.NodeUpdate{
		NodeId:      nodeID,
		CpuTenths:   uint32(nodeMetrics.CPU * 10),
		RamMax:      uint64(nodeMetrics.RAMMax),
		RamCurrent:  uint64(nodeMetrics.RAMCurrent),
		UpSpeed:     uint64(nodeMetrics.UpSpeed),
		DownSpeed:   uint64(nodeMetrics.DownSpeed),
		BwLimit:     uint64(nodeMetrics.BWLimit),
		Latitude:    nodeMetrics.Location.Latitude,
		Longitude:   nodeMetrics.Location.Longitude,
		Location:    nodeMetrics.Location.Name,
		BaseUrl:     baseURL,
		IsHealthy:   true,
		EventType:   "node_update",
		Timestamp:   time.Now().Unix(),
		OutputsJson: outputsJSON,
	}

	if nodeMetrics.Capabilities.Ingest || nodeMetrics.Capabilities.Edge || nodeMetrics.Capabilities.Storage || nodeMetrics.Capabilities.Processing {
		pbUpdate.Capabilities = &pb.NodeCapabilities{
			Ingest:     nodeMetrics.Capabilities.Ingest,
			Edge:       nodeMetrics.Capabilities.Edge,
			Storage:    nodeMetrics.Capabilities.Storage,
			Processing: nodeMetrics.Capabilities.Processing,
			Roles:      nodeMetrics.Capabilities.Roles,
		}
	}

	if nodeMetrics.Storage.LocalPath != "" || nodeMetrics.Storage.S3Bucket != "" {
		pbUpdate.Storage = &pb.StorageInfo{
			LocalPath: nodeMetrics.Storage.LocalPath,
			S3Bucket:  nodeMetrics.Storage.S3Bucket,
			S3Prefix:  nodeMetrics.Storage.S3Prefix,
		}
	}

	if nodeMetrics.Limits != nil {
		pbUpdate.Limits = &pb.NodeLimits{
			MaxTranscodes:        int32(nodeMetrics.Limits.MaxTranscodes),
			StorageCapacityBytes: nodeMetrics.Limits.StorageCapacityBytes,
			StorageUsedBytes:     nodeMetrics.Limits.StorageUsedBytes,
		}
	}

	if len(nodeMetrics.Streams) > 0 {
		pbUpdate.Streams = make(map[string]*pb.StreamData)
		for streamName, streamData := range nodeMetrics.Streams {
			pbUpdate.Streams[streamName] = &pb.StreamData{
				Total:     streamData.Total,
				Inputs:    streamData.Inputs,
				BytesUp:   streamData.BytesUp,
				BytesDown: streamData.BytesDown,
				Bandwidth: streamData.Bandwidth,
			}
		}
	}

	for _, artifact := range nodeMetrics.Artifacts {
		// Parse stream name and clip hash from artifact path/ID
		streamName, clipHash := parseArtifactID(artifact.ID, artifact.Path)

		pbUpdate.Artifacts = append(pbUpdate.Artifacts, &pb.StoredArtifact{
			ClipHash:   clipHash,
			StreamName: streamName,
			FilePath:   artifact.Path,
			S3Url:      artifact.URL,
			SizeBytes:  artifact.SizeBytes,
			CreatedAt:  artifact.CreatedAt,
			Format:     artifact.Format,
		})
	}

	msg := &pb.ControlMessage{SentAt: timestamppb.Now(), Payload: &pb.ControlMessage_NodeUpdate{NodeUpdate: pbUpdate}}
	return stream.Send(msg)
}

// ResolveClipHash resolves a clip hash to tenant and stream info via gRPC (replaces HTTP)
func ResolveClipHash(ctx context.Context, clipHash string) (*pb.ClipHashResponse, error) {
	client := currentClient
	if client == nil {
		return nil, fmt.Errorf("gRPC client not connected")
	}

	req := &pb.ClipHashRequest{ClipHash: clipHash}
	return client.ResolveClipHash(ctx, req)
}

// SendDVRStartRequest sends a DVR start notification to Foghorn via the gRPC control stream
func SendDVRStartRequest(tenantID, internalName, userID string, retentionDays int, format string, segmentDuration int) error {
	stream := currentStream
	if stream == nil {
		return fmt.Errorf("gRPC control stream not connected")
	}

	config := &pb.DVRConfig{
		Enabled:         true, // If we're sending this, recording is enabled
		RetentionDays:   int32(retentionDays),
		Format:          format,
		SegmentDuration: int32(segmentDuration),
	}

	dvrRequest := &pb.DVRStartRequest{
		DvrHash:       "", // Will be generated by Foghorn
		InternalName:  internalName,
		SourceBaseUrl: "", // Will be constructed by Foghorn
		RequestId:     uuid.New().String(),
		Config:        config,
		TenantId:      tenantID,
		UserId:        userID,
	}

	msg := &pb.ControlMessage{SentAt: timestamppb.Now(), Payload: &pb.ControlMessage_DvrStartRequest{DvrStartRequest: dvrRequest}}
	return stream.Send(msg)
}

func runClient(addr string, logger logging.Logger) {
	for {
		if err := connectOnce(addr, logger); err != nil {
			logger.WithError(err).Warn("Control client disconnected; retrying")
			time.Sleep(2 * time.Second)
		}
	}
}

func connectOnce(addr string, logger logging.Logger) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	conn, err := grpc.DialContext(ctx, addr, grpc.WithTransportCredentials(insecure.NewCredentials()), grpc.WithBlock())
	if err != nil {
		return err
	}
	defer conn.Close()
	client := pb.NewHelmsmanControlClient(conn)
	stream, err := client.Connect(context.Background())
	if err != nil {
		return err
	}

	// Send Register
	nodeID := os.Getenv("NODE_NAME")
	if nodeID == "" {
		nodeID = hostnameFallback()
	}
	roles := splitCSV(os.Getenv("HELMSMAN_ROLES"))
	reg := &pb.ControlMessage{SentAt: timestamppb.Now(), Payload: &pb.ControlMessage_Register{Register: &pb.Register{
		NodeId:        nodeID,
		Roles:         roles,
		CapIngest:     envBoolDefault("HELMSMAN_CAP_INGEST", true),
		CapEdge:       envBoolDefault("HELMSMAN_CAP_EDGE", true),
		CapStorage:    envBoolDefault("HELMSMAN_CAP_STORAGE", true),
		CapProcessing: envBoolDefault("HELMSMAN_CAP_PROCESSING", true),
		StorageLocal:  os.Getenv("HELMSMAN_STORAGE_LOCAL_PATH"),
		StorageBucket: os.Getenv("HELMSMAN_STORAGE_S3_BUCKET"),
		StoragePrefix: os.Getenv("HELMSMAN_STORAGE_S3_PREFIX"),
	}}}
	if err := stream.Send(reg); err != nil {
		return err
	}

	// Store current stream and client
	currentStream = stream
	currentNodeID = nodeID
	currentClient = client
	defer func() { currentStream = nil; currentNodeID = ""; currentClient = nil }()

	// Heartbeat ticker
	hbTicker := time.NewTicker(30 * time.Second)
	defer hbTicker.Stop()

	// Receive loop and heartbeat sender
	errCh := make(chan error, 1)
	go func() {
		for {
			msg, err := stream.Recv()
			if err != nil {
				if err == io.EOF {
					errCh <- nil
				} else {
					errCh <- err
				}
				return
			}
			switch x := msg.GetPayload().(type) {
			case *pb.ControlMessage_ClipPullRequest:
				go handleClipPull(logger, x.ClipPullRequest, func(m *pb.ControlMessage) { _ = stream.Send(m) })
			case *pb.ControlMessage_DvrStartRequest:
				go handleDVRStart(logger, x.DvrStartRequest, func(m *pb.ControlMessage) { _ = stream.Send(m) })
			case *pb.ControlMessage_DvrStopRequest:
				go handleDVRStop(logger, x.DvrStopRequest, func(m *pb.ControlMessage) { _ = stream.Send(m) })
			}
		}
	}()

	for {
		select {
		case <-hbTicker.C:
			_ = stream.Send(&pb.ControlMessage{SentAt: timestamppb.Now(), Payload: &pb.ControlMessage_Heartbeat{Heartbeat: &pb.Heartbeat{NodeId: nodeID}}})
		case e := <-errCh:
			return e
		}
	}
}

func handleClipPull(logger logging.Logger, req *pb.ClipPullRequest, send func(*pb.ControlMessage)) {
	mistBase := req.GetSourceBaseUrl()
	if mistBase == "" {
		mistBase = os.Getenv("MISTSERVER_URL")
		if mistBase == "" {
			mistBase = "http://localhost:8080"
		}
	}
	mistBase = strings.TrimRight(mistBase, "/")
	format := req.GetFormat()
	if format == "" {
		format = "mp4"
	}

	// Use clip_hash for secure file naming (no tenant info exposed)
	clipHash := req.GetClipHash()
	streamName := req.GetStreamName()
	output := req.GetOutputName()
	if output == "" {
		output = clipHash // Use opaque clip hash as filename
	}

	// Build MistServer URL using stream name
	q := buildClipParams(req)
	clipURL := fmt.Sprintf("%s/view/%s.%s?%s", mistBase, streamName, format, q)

	root := os.Getenv("HELMSMAN_STORAGE_LOCAL_PATH")
	if root == "" {
		logger.Warn("HELMSMAN_STORAGE_LOCAL_PATH not set; dropping clip request")
		return
	}

	// Create secure storage path: clips/{stream_name}/{clip_hash}.{format}
	clipDir := filepath.Join(root, "clips", streamName)
	_ = os.MkdirAll(clipDir, 0755)
	dst := filepath.Join(clipDir, fmt.Sprintf("%s.%s", clipHash, format))

	requestID := req.GetRequestId()

	// progress 0%
	if send != nil {
		send(&pb.ControlMessage{SentAt: timestamppb.Now(), Payload: &pb.ControlMessage_ClipProgress{ClipProgress: &pb.ClipProgress{RequestId: requestID, Percent: 0, Message: "starting"}}})
	}
	if err := downloadToFile(clipURL, dst); err != nil {
		logger.WithError(err).WithFields(logging.Fields{
			"clip_url":   clipURL,
			"clip_hash":  clipHash,
			"request_id": requestID,
		}).Error("Clip pull failed")
		if send != nil {
			send(&pb.ControlMessage{SentAt: timestamppb.Now(), Payload: &pb.ControlMessage_ClipDone{ClipDone: &pb.ClipDone{RequestId: requestID, FilePath: dst, SizeBytes: 0, Status: "failed", Error: fmt.Sprintf("%v", err)}}})
		}
		return
	}
	info, _ := os.Stat(dst)
	logger.WithFields(logging.Fields{
		"file":        dst,
		"clip_hash":   clipHash,
		"stream_name": streamName,
		"request_id":  requestID,
		"bytes": func() int64 {
			if info != nil {
				return info.Size()
			}
			return 0
		}(),
	}).Info("Clip pulled successfully")

	if send != nil {
		var size uint64
		if info != nil {
			size = uint64(info.Size())
		}
		send(&pb.ControlMessage{SentAt: timestamppb.Now(), Payload: &pb.ControlMessage_ClipProgress{ClipProgress: &pb.ClipProgress{RequestId: requestID, Percent: 100, Message: "downloaded"}}})
		send(&pb.ControlMessage{SentAt: timestamppb.Now(), Payload: &pb.ControlMessage_ClipDone{ClipDone: &pb.ClipDone{RequestId: requestID, FilePath: dst, SizeBytes: size, Status: "success"}}})
	}
}

func buildClipParams(req *pb.ClipPullRequest) string {
	var parts []string
	if req.StartUnix != nil {
		parts = append(parts, "startunix="+strconv.FormatInt(req.GetStartUnix(), 10))
	}
	if req.StopUnix != nil {
		parts = append(parts, "stopunix="+strconv.FormatInt(req.GetStopUnix(), 10))
	}
	if req.StartMs != nil {
		parts = append(parts, "start="+strconv.FormatInt(req.GetStartMs()/1000, 10))
	}
	if req.StopMs != nil {
		parts = append(parts, "stop="+strconv.FormatInt(req.GetStopMs()/1000, 10))
	}
	if req.DurationSec != nil {
		parts = append(parts, "duration="+strconv.FormatInt(req.GetDurationSec(), 10))
	}
	parts = append(parts, "dl="+urlEscape(fmt.Sprintf("%s.%s", req.GetOutputName(), req.GetFormat())))
	return strings.Join(parts, "&")
}

func downloadToFile(url, dst string) error {
	resp, err := http.Get(url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("mist returned %d", resp.StatusCode)
	}
	f, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = io.Copy(f, resp.Body)
	return err
}

func splitCSV(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

func envBoolDefault(name string, def bool) bool {
	v := strings.ToLower(os.Getenv(name))
	if v == "" {
		return def
	}
	return v == "1" || v == "true" || v == "yes"
}

func hostnameFallback() string {
	h, _ := os.Hostname()
	if h == "" {
		h = "unknown-helmsman"
	}
	return h
}

func urlEscape(s string) string {
	r := strings.NewReplacer(" ", "%20")
	return r.Replace(s)
}

// parseArtifactID extracts stream name and clip hash from artifact ID or path
func parseArtifactID(artifactID, artifactPath string) (streamName, clipHash string) {
	// Try to extract from path first: clips/{stream_name}/{clip_hash}.{format}
	if strings.HasPrefix(artifactPath, "clips/") {
		parts := strings.Split(artifactPath, "/")
		if len(parts) >= 3 {
			streamName = parts[1]
			filename := parts[2]
			// Remove extension to get clip hash
			if lastDot := strings.LastIndex(filename, "."); lastDot > 0 {
				clipHash = filename[:lastDot]
				return
			}
		}
	}

	// Fallback: extract from artifact ID if it follows pattern stream_name/hash
	if strings.Contains(artifactID, "/") {
		parts := strings.SplitN(artifactID, "/", 2)
		if len(parts) == 2 {
			streamName = parts[0]
			clipHash = parts[1]
			return
		}
	}

	// Last resort: use artifact ID as clip hash and try to infer stream from path
	clipHash = artifactID
	if artifactPath != "" {
		// Try to extract stream name from any part of the path
		pathParts := strings.Split(strings.Trim(artifactPath, "/"), "/")
		for _, part := range pathParts {
			if part != "clips" && part != clipHash && !strings.Contains(part, ".") {
				streamName = part
				break
			}
		}
	}

	// Default fallback
	if streamName == "" {
		streamName = "unknown"
	}
	if clipHash == "" {
		clipHash = "unknown"
	}

	return
}

// handleDVRStart handles DVR start requests from Foghorn (for storage nodes)
func handleDVRStart(logger logging.Logger, req *pb.DVRStartRequest, send func(*pb.ControlMessage)) {
	dvrHash := req.GetDvrHash()
	internalName := req.GetInternalName()
	sourceURL := req.GetSourceBaseUrl()
	requestID := req.GetRequestId()
	config := req.GetConfig()

	logger.WithFields(logging.Fields{
		"dvr_hash":      dvrHash,
		"internal_name": internalName,
		"source_url":    sourceURL,
		"request_id":    requestID,
		"format":        config.GetFormat(),
		"retention":     config.GetRetentionDays(),
	}).Info("Starting DVR recording")

	// Initialize DVR manager if not already done
	initDVRManager()

	// Start the DVR recording job
	if err := dvrManager.StartRecording(dvrHash, internalName, sourceURL, config, send); err != nil {
		logger.WithFields(logging.Fields{
			"dvr_hash": dvrHash,
			"error":    err,
		}).Error("Failed to start DVR recording")

		// Send failure notification
		if send != nil {
			send(&pb.ControlMessage{SentAt: timestamppb.Now(), Payload: &pb.ControlMessage_DvrStopped{DvrStopped: &pb.DVRStopped{
				RequestId:       requestID,
				DvrHash:         dvrHash,
				Status:          "failed",
				Error:           err.Error(),
				ManifestPath:    "",
				DurationSeconds: 0,
				SizeBytes:       0,
			}}})
		}
		return
	}

	logger.WithField("dvr_hash", dvrHash).Info("DVR recording started successfully")
}

// handleDVRStop handles DVR stop requests from Foghorn
func handleDVRStop(logger logging.Logger, req *pb.DVRStopRequest, send func(*pb.ControlMessage)) {
	dvrHash := req.GetDvrHash()
	requestID := req.GetRequestId()

	logger.WithFields(logging.Fields{
		"dvr_hash":   dvrHash,
		"request_id": requestID,
	}).Info("Stopping DVR recording")

	// Initialize DVR manager if not already done
	initDVRManager()

	// Stop the DVR recording job
	if err := dvrManager.StopRecording(dvrHash); err != nil {
		logger.WithFields(logging.Fields{
			"dvr_hash": dvrHash,
			"error":    err,
		}).Error("Failed to stop DVR recording")

		// Send failure notification
		if send != nil {
			send(&pb.ControlMessage{SentAt: timestamppb.Now(), Payload: &pb.ControlMessage_DvrStopped{DvrStopped: &pb.DVRStopped{
				RequestId:       requestID,
				DvrHash:         dvrHash,
				Status:          "failed",
				Error:           err.Error(),
				ManifestPath:    "",
				DurationSeconds: 0,
				SizeBytes:       0,
			}}})
		}
		return
	}

	logger.WithField("dvr_hash", dvrHash).Info("DVR recording stopped successfully")
}

// SendDVRStreamEndNotification notifies Foghorn that a stream has ended and DVR recording should stop
func SendDVRStreamEndNotification(internalName, nodeID string) error {
	stream := currentStream
	if stream == nil {
		return fmt.Errorf("gRPC control stream not connected")
	}

	// Create a DVR stop request with stream context
	dvrStopRequest := &pb.DVRStopRequest{
		DvrHash:      "", // Empty hash means stop all recordings for this stream
		RequestId:    uuid.New().String(),
		InternalName: &internalName,
	}

	msg := &pb.ControlMessage{SentAt: timestamppb.Now(), Payload: &pb.ControlMessage_DvrStopRequest{DvrStopRequest: dvrStopRequest}}
	return stream.Send(msg)
}
