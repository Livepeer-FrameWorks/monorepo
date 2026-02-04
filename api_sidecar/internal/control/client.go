package control

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	sidecarcfg "frameworks/api_sidecar/internal/config"
	"frameworks/api_sidecar/internal/storage"
	"frameworks/pkg/logging"
	"frameworks/pkg/mist"
	pb "frameworks/pkg/proto"

	"github.com/google/uuid"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// DeleteClipFunc is the function type for clip deletion
type DeleteClipFunc func(clipHash string) (uint64, error)

// DeleteDVRFunc is the function type for DVR deletion
type DeleteDVRFunc func(dvrHash string) (uint64, error)

// DeleteVodFunc is the function type for VOD deletion
type DeleteVodFunc func(vodHash string) (uint64, error)

// Global state for metrics streaming
var (
	currentStream  pb.HelmsmanControl_ConnectClient
	currentNodeID  string
	currentConfig  *sidecarcfg.HelmsmanConfig
	onSeed         func()
	onStorageWrite func()
	deleteClipFn   DeleteClipFunc
	deleteDVRFn    DeleteDVRFunc
	deleteVodFn    DeleteVodFunc

	blockingGraceMs    int
	streamReconnected  = make(chan struct{})
	streamReconnectedM sync.Mutex

	disconnectNotify   = make(chan struct{})
	disconnectNotifyMu sync.Mutex

	jitterRandMu sync.Mutex
	jitterRand   = rand.New(rand.NewSource(time.Now().UnixNano()))
)

const (
	blockingTriggerTimeout = 5 * time.Second
	maxBlockingAttempts    = 3
	reconnectJitterPct     = 25
)

// SetOnSeed sets a callback invoked when Foghorn requests immediate JSON seed
func SetOnSeed(cb func()) {
	onSeed = cb
}

// SetOnStorageWrite sets a callback for successful local storage writes.
func SetOnStorageWrite(cb func()) {
	onStorageWrite = cb
}

// SetDeleteClipHandler sets the callback for clip deletion
func SetDeleteClipHandler(fn DeleteClipFunc) {
	deleteClipFn = fn
}

// SetDeleteDVRHandler sets the callback for DVR deletion
func SetDeleteDVRHandler(fn DeleteDVRFunc) {
	deleteDVRFn = fn
}

// SetDeleteVodHandler sets the callback for VOD deletion
func SetDeleteVodHandler(fn DeleteVodFunc) {
	deleteVodFn = fn
}

// Start launches the Helmsman control client and maintains the stream to Foghorn
func Start(logger logging.Logger, cfg *sidecarcfg.HelmsmanConfig) {
	currentConfig = cfg
	blockingGraceMs = cfg.BlockingGraceMs
	if blockingGraceMs > 0 {
		logger.WithField("grace_ms", blockingGraceMs).Info("Blocking trigger grace period enabled")
	}
	go func() {
		backoff := time.Second
		const maxBackoff = 30 * time.Second
		for {
			connStart := time.Now()
			if err := runClient(cfg.FoghornControlAddr, logger); err != nil {
				logger.WithError(err).Warn("Helmsman control client disconnected; retrying")
			}
			if time.Since(connStart) > maxBackoff {
				backoff = time.Second
			}
			time.Sleep(applyJitter(backoff, reconnectJitterPct))
			if backoff < maxBackoff {
				backoff *= 2
			}
		}
	}()
}

// GetCurrentNodeID returns the current node ID for building triggers
func GetCurrentNodeID() string {
	return currentNodeID
}

// MistTriggerResult carries the full response from Foghorn for blocking triggers
type MistTriggerResult struct {
	Response  string
	Abort     bool
	ErrorCode pb.IngestErrorCode
}

// SendMistTrigger forwards a typed MistServer trigger to Foghorn and returns response for blocking triggers
func SendMistTrigger(mistTrigger *pb.MistTrigger, logger logging.Logger) (*MistTriggerResult, error) {
	triggerType := mistTrigger.TriggerType
	if !mistTrigger.Blocking {
		if err := sendMistTriggerOnce(triggerType, mistTrigger); err != nil {
			return &MistTriggerResult{Abort: true, ErrorCode: pb.IngestErrorCode_INGEST_ERROR_INTERNAL}, err
		}
		return &MistTriggerResult{}, nil
	}

	attempts := maxBlockingAttempts
	if attempts < 1 {
		attempts = 1
	}
	deadline := time.Now().Add(blockingTriggerTimeout)

	var lastErr error
	for attempt := 1; attempt <= attempts; attempt++ {
		if time.Now().After(deadline) {
			break
		}

		stream := currentStream
		if stream == nil && blockingGraceMs > 0 {
			remaining := time.Until(deadline)
			if remaining <= 0 {
				break
			}
			grace := time.Duration(blockingGraceMs) * time.Millisecond
			if grace > remaining {
				grace = remaining
			}
			stream = waitForReconnection(grace)
		}
		if time.Now().After(deadline) {
			break
		}
		if stream == nil {
			TriggersSent.WithLabelValues(triggerType, "stream_disconnected").Inc()
			BlockingTriggerRetries.WithLabelValues(triggerType, "stream_disconnected").Inc()
			lastErr = fmt.Errorf("gRPC control stream not connected")
			continue
		}

		if err := sendMistTriggerOnce(triggerType, mistTrigger); err != nil {
			BlockingTriggerRetries.WithLabelValues(triggerType, "send_error").Inc()
			lastErr = err
			continue
		}

		remaining := time.Until(deadline)
		if remaining <= 0 {
			break
		}
		result, err := waitForMistTriggerResponseWithDisconnect(mistTrigger.RequestId, remaining)
		if err == nil {
			return result, nil
		}
		if errors.Is(err, errStreamDisconnected) {
			BlockingTriggerRetries.WithLabelValues(triggerType, "stream_disconnected").Inc()
			lastErr = err
			continue
		}
		return result, err
	}

	TriggersSent.WithLabelValues(triggerType, "exhausted").Inc()
	if lastErr == nil {
		lastErr = fmt.Errorf("blocking trigger attempts exhausted")
	}
	return &MistTriggerResult{Abort: true, ErrorCode: pb.IngestErrorCode_INGEST_ERROR_TIMEOUT}, lastErr
}

// pendingMistTriggers tracks blocking trigger requests waiting for responses
var (
	pendingMistTriggers = make(map[string]chan *pb.MistTriggerResponse)
	pendingMutex        = make(chan struct{}, 1) // Simple mutex using buffered channel
)

var errStreamDisconnected = errors.New("gRPC control stream disconnected")

func sendMistTriggerOnce(triggerType string, mistTrigger *pb.MistTrigger) error {
	stream := currentStream
	if stream == nil {
		TriggersSent.WithLabelValues(triggerType, "stream_disconnected").Inc()
		return fmt.Errorf("gRPC control stream not connected")
	}

	msg := &pb.ControlMessage{
		SentAt:  timestamppb.Now(),
		Payload: &pb.ControlMessage_MistTrigger{MistTrigger: mistTrigger},
	}

	if err := stream.Send(msg); err != nil {
		TriggersSent.WithLabelValues(triggerType, "send_error").Inc()
		return fmt.Errorf("failed to send MistTrigger: %w", err)
	}

	TriggersSent.WithLabelValues(triggerType, "sent").Inc()
	return nil
}

// waitForMistTriggerResponse waits for a MistTriggerResponse with matching requestID
func waitForMistTriggerResponseWithDisconnect(requestID string, timeout time.Duration) (*MistTriggerResult, error) {
	// Create response channel
	responseChan := make(chan *pb.MistTriggerResponse, 1)

	// Acquire mutex
	pendingMutex <- struct{}{}
	pendingMistTriggers[requestID] = responseChan
	<-pendingMutex // Release mutex

	disconnectNotifyMu.Lock()
	disconnectCh := disconnectNotify
	disconnectNotifyMu.Unlock()

	// Wait for response, disconnect, or timeout
	select {
	case response := <-responseChan:
		pendingMutex <- struct{}{}
		delete(pendingMistTriggers, requestID)
		<-pendingMutex

		return &MistTriggerResult{
			Response:  response.Response,
			Abort:     response.Abort,
			ErrorCode: response.ErrorCode,
		}, nil
	case <-disconnectCh:
		pendingMutex <- struct{}{}
		delete(pendingMistTriggers, requestID)
		<-pendingMutex

		return &MistTriggerResult{
			Abort:     true,
			ErrorCode: pb.IngestErrorCode_INGEST_ERROR_INTERNAL,
		}, errStreamDisconnected
	case <-time.After(timeout):
		pendingMutex <- struct{}{}
		delete(pendingMistTriggers, requestID)
		<-pendingMutex

		return &MistTriggerResult{
			Abort:     true,
			ErrorCode: pb.IngestErrorCode_INGEST_ERROR_TIMEOUT,
		}, fmt.Errorf("timeout waiting for MistTrigger response")
	}
}

// handleMistTriggerResponse processes MistTriggerResponse messages from the stream
func handleMistTriggerResponse(response *pb.MistTriggerResponse) {
	pendingMutex <- struct{}{}
	responseChan, exists := pendingMistTriggers[response.RequestId]
	<-pendingMutex

	if exists {
		responseChan <- response
	}
}

func notifyDisconnect() {
	disconnectNotifyMu.Lock()
	close(disconnectNotify)
	disconnectNotify = make(chan struct{})
	disconnectNotifyMu.Unlock()
}

func waitForReconnection(timeout time.Duration) pb.HelmsmanControl_ConnectClient {
	streamReconnectedM.Lock()
	reconnectCh := streamReconnected
	s := currentStream
	streamReconnectedM.Unlock()

	// Re-check after grabbing the channel: if we're already connected, don't wait.
	if s != nil {
		return s
	}

	select {
	case <-reconnectCh:
		return currentStream
	case <-time.After(timeout):
		return nil
	}
}

func applyJitter(backoff time.Duration, jitterPct int) time.Duration {
	if jitterPct <= 0 {
		return backoff
	}
	jitterRange := int64(backoff) * int64(jitterPct) / 100
	if jitterRange <= 0 {
		return backoff
	}
	jitterRandMu.Lock()
	jitter := jitterRand.Int63n(jitterRange*2+1) - jitterRange
	jitterRandMu.Unlock()
	return time.Duration(int64(backoff) + jitter)
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

// SendArtifactDeleted notifies Foghorn that an artifact has been deleted
func SendArtifactDeleted(artifactHash, filePath, reason, artifactType string, sizeBytes uint64) error {
	stream := currentStream
	if stream == nil {
		return fmt.Errorf("gRPC control stream not connected")
	}

	artifactDeleted := &pb.ArtifactDeleted{
		ClipHash:     artifactHash, // Deprecated, kept for compatibility
		ArtifactHash: artifactHash,
		ArtifactType: artifactType,
		FilePath:     filePath,
		Reason:       reason,
		NodeId:       currentNodeID,
		SizeBytes:    sizeBytes,
	}

	msg := &pb.ControlMessage{SentAt: timestamppb.Now(), Payload: &pb.ControlMessage_ArtifactDeleted{ArtifactDeleted: artifactDeleted}}
	return stream.Send(msg)
}

func runClient(addr string, logger logging.Logger) error {
	cfg := currentConfig
	if cfg == nil {
		return fmt.Errorf("config not initialized")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Configure TLS based on config
	var creds credentials.TransportCredentials
	if cfg.GRPCUseTLS {
		if cfg.GRPCTLSCertPath != "" && cfg.GRPCTLSKeyPath != "" {
			// Use client certificate for mutual TLS
			cert, err := tls.LoadX509KeyPair(cfg.GRPCTLSCertPath, cfg.GRPCTLSKeyPath)
			if err != nil {
				return fmt.Errorf("failed to load TLS certificates: %w", err)
			}
			creds = credentials.NewTLS(&tls.Config{
				Certificates: []tls.Certificate{cert},
			})
		} else {
			// Use TLS without client certificate
			creds = credentials.NewTLS(&tls.Config{})
		}

		logger.Info("Connecting to gRPC server with TLS")
	} else {
		creds = insecure.NewCredentials()
		logger.Info("Connecting to gRPC server with insecure connection")
	}

	conn, err := grpc.DialContext(ctx, addr, grpc.WithTransportCredentials(creds), grpc.WithBlock())
	if err != nil {
		return err
	}
	defer conn.Close()
	client := pb.NewHelmsmanControlClient(conn)
	stream, err := client.Connect(context.Background())
	if err != nil {
		return err
	}

	// Send Register using config values
	nodeID := cfg.NodeID
	roles := deriveRolesFromConfig(cfg)

	// Detect hardware specs at startup
	hwSpecs := sidecarcfg.DetectHardware(cfg.StorageLocalPath)

	reg := &pb.ControlMessage{SentAt: timestamppb.Now(), Payload: &pb.ControlMessage_Register{Register: &pb.Register{
		NodeId:          nodeID,
		Roles:           roles,
		CapIngest:       cfg.CapIngest,
		CapEdge:         cfg.CapEdge,
		CapStorage:      cfg.CapStorage,
		CapProcessing:   cfg.CapProcessing,
		StorageLocal:    cfg.StorageLocalPath,
		StorageBucket:   cfg.StorageS3Bucket,
		StoragePrefix:   cfg.StorageS3Prefix,
		EnrollmentToken: cfg.EnrollmentToken,
		Fingerprint:     collectNodeFingerprint(),
		CpuCores:        &hwSpecs.CPUCores,
		MemoryGb:        &hwSpecs.MemoryGB,
		DiskGb:          &hwSpecs.DiskGB,
		RequestedMode:   parseRequestedMode(cfg.RequestedMode),
	}}}
	if err := stream.Send(reg); err != nil {
		return err
	}

	// Store current stream for external access
	currentStream = stream
	currentNodeID = nodeID
	ControlStreamStatus.Set(1)
	streamReconnectedM.Lock()
	close(streamReconnected)
	streamReconnected = make(chan struct{})
	streamReconnectedM.Unlock()
	defer func() {
		currentStream = nil
		currentNodeID = ""
		ControlStreamStatus.Set(0)
		notifyDisconnect()
	}()

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
			case *pb.ControlMessage_ClipDelete:
				go handleClipDelete(logger, x.ClipDelete, func(m *pb.ControlMessage) { _ = stream.Send(m) })
			case *pb.ControlMessage_DvrDelete:
				go handleDVRDelete(logger, x.DvrDelete, func(m *pb.ControlMessage) { _ = stream.Send(m) })
			case *pb.ControlMessage_VodDelete:
				go handleVodDelete(logger, x.VodDelete, func(m *pb.ControlMessage) { _ = stream.Send(m) })
			case *pb.ControlMessage_MistTriggerResponse:
				// Handle response from Foghorn for blocking triggers
				go handleMistTriggerResponse(x.MistTriggerResponse)
			case *pb.ControlMessage_Error:
				if errMsg := x.Error; errMsg != nil {
					code := errMsg.GetCode()
					message := errMsg.GetMessage()
					logger.WithFields(logging.Fields{
						"code":    code,
						"message": message,
					}).Error("Received control error from Foghorn")
					switch code {
					case "ENROLLMENT_REQUIRED", "ENROLLMENT_FAILED", "ENROLLMENT_UNAVAILABLE":
						errCh <- fmt.Errorf("control error %s: %s", code, message)
						return
					}
				}
			case *pb.ControlMessage_MistTrigger:
				// Foghorn-initiated command: seed immediate JSON poll/upload
				if x.MistTrigger != nil {
					if t := x.MistTrigger.GetTriggerType(); t == "seed_poll" || t == "seed_request" {
						if onSeed != nil {
							onSeed()
						}
					}
				}
			case *pb.ControlMessage_ConfigSeed:
				// Receive desired config seed and trigger reconcile
				if x.ConfigSeed != nil {
					sidecarcfg.ApplySeed(x.ConfigSeed)
					// Adopt canonical node_id from seed if provided
					if nid := x.ConfigSeed.GetNodeId(); nid != "" {
						currentNodeID = nid
					}
				}
			case *pb.ControlMessage_FreezePermissionResponse:
				// Handle freeze permission response from Foghorn
				go handleFreezePermissionResponse(x.FreezePermissionResponse)
			case *pb.ControlMessage_DefrostRequest:
				// Handle defrost request from Foghorn
				if defrostRequestHandler != nil {
					go defrostRequestHandler(x.DefrostRequest)
				}
			case *pb.ControlMessage_CanDeleteResponse:
				// Handle can-delete response from Foghorn
				go handleCanDeleteResponse(x.CanDeleteResponse)
			case *pb.ControlMessage_DtshSyncRequest:
				// Handle incremental .dtsh sync request from Foghorn
				if dtshSyncRequestHandler != nil {
					go dtshSyncRequestHandler(x.DtshSyncRequest)
				}
			case *pb.ControlMessage_StopSessionsRequest:
				// Handle stop sessions request from Foghorn (billing suspension)
				go handleStopSessions(logger, x.StopSessionsRequest)
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
	cfg := currentConfig
	if cfg == nil {
		logger.Warn("config not initialized; dropping clip request")
		return
	}

	mistBase := req.GetSourceBaseUrl()
	if mistBase == "" {
		mistBase = cfg.MistServerURL
	}
	mistBase = strings.TrimRight(mistBase, "/")
	format := req.GetFormat()
	if format == "" {
		format = "mp4"
	}

	// Use clip_hash for secure file naming (no tenant info exposed)
	clipHash := req.GetClipHash()
	streamName := req.GetStreamName()

	// Build MistServer URL using stream name
	q := buildClipParams(req)
	clipURL := fmt.Sprintf("%s/view/%s.%s?%s", mistBase, streamName, format, q)

	root := cfg.StorageLocalPath
	if root == "" {
		logger.Warn("storage path not configured; dropping clip request")
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
		userErr := sanitizeStorageError(err)
		logger.WithError(err).WithFields(logging.Fields{
			"clip_url":   clipURL,
			"clip_hash":  clipHash,
			"request_id": requestID,
		}).Error("Clip pull failed")
		if send != nil {
			send(&pb.ControlMessage{SentAt: timestamppb.Now(), Payload: &pb.ControlMessage_ClipDone{ClipDone: &pb.ClipDone{RequestId: requestID, FilePath: dst, SizeBytes: 0, Status: "failed", Error: userErr}}})
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
	if onStorageWrite != nil {
		onStorageWrite()
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
		// StartMs is media time in seconds (despite the name), MistServer expects seconds
		parts = append(parts, "start="+strconv.FormatInt(req.GetStartMs(), 10))
	}
	if req.StopMs != nil {
		// StopMs is media time in seconds (despite the name), MistServer expects seconds
		parts = append(parts, "stop="+strconv.FormatInt(req.GetStopMs(), 10))
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

	parentDir := filepath.Dir(dst)
	if err = os.MkdirAll(parentDir, 0755); err != nil {
		return err
	}
	requiredBytes := uint64(0)
	if resp.ContentLength > 0 {
		requiredBytes = uint64(resp.ContentLength)
	}
	if err = storage.HasSpaceFor(parentDir, requiredBytes); err != nil {
		return err
	}

	tmpPath := dst + ".downloading"
	_ = os.Remove(tmpPath)
	f, err := os.Create(tmpPath)
	if err != nil {
		return err
	}
	defer f.Close()
	if _, err := io.Copy(f, resp.Body); err != nil {
		_ = os.Remove(tmpPath)
		return err
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return err
	}
	if err := os.Rename(tmpPath, dst); err != nil {
		_ = os.Remove(tmpPath)
		return err
	}
	return nil
}

func sanitizeStorageError(err error) string {
	if storage.IsInsufficientSpace(err) {
		return "Download failed: storage node out of space"
	}
	return "Download failed: please retry or contact support"
}

func deriveRolesFromConfig(cfg *sidecarcfg.HelmsmanConfig) []string {
	var roles []string
	if cfg.CapIngest {
		roles = append(roles, "ingest")
	}
	if cfg.CapEdge {
		roles = append(roles, "edge")
	}
	if cfg.CapStorage {
		roles = append(roles, "storage")
	}
	if cfg.CapProcessing {
		roles = append(roles, "processing")
	}
	return roles
}

// collectNodeFingerprint builds a stable fingerprint from local network/machine info.
func collectNodeFingerprint() *pb.NodeFingerprint {
	fp := &pb.NodeFingerprint{}
	ifaces, _ := net.Interfaces()
	// Collect local IPs (exclude loopback, link-local)
	for _, iface := range ifaces {
		addrs, _ := iface.Addrs()
		for _, a := range addrs {
			var ip net.IP
			switch v := a.(type) {
			case *net.IPNet:
				ip = v.IP
			case *net.IPAddr:
				ip = v.IP
			}
			if ip == nil || ip.IsLoopback() || ip.IsLinkLocalUnicast() {
				continue
			}
			if ip.To4() != nil {
				fp.LocalIpv4 = append(fp.LocalIpv4, ip.String())
			} else {
				fp.LocalIpv6 = append(fp.LocalIpv6, ip.String())
			}
		}
	}
	// Aggregate physical MACs (filter virtuals) into a single SHA-256
	var macs []string
	for _, iface := range ifaces {
		name := strings.ToLower(iface.Name)
		if strings.HasPrefix(name, "lo") || strings.HasPrefix(name, "docker") || strings.HasPrefix(name, "veth") || strings.HasPrefix(name, "br-") || strings.HasPrefix(name, "tun") || strings.HasPrefix(name, "tap") || strings.HasPrefix(name, "wg") {
			continue
		}
		if len(iface.HardwareAddr) == 0 {
			continue
		}
		macs = append(macs, strings.ToLower(iface.HardwareAddr.String()))
	}
	if len(macs) > 0 {
		sort.Strings(macs)
		sum := sha256.Sum256([]byte(strings.Join(macs, ",")))
		macHex := hex.EncodeToString(sum[:])
		fp.MacsSha256 = &macHex
	}
	// machine-id if present (host-provided, stable)
	if b, err := os.ReadFile("/etc/machine-id"); err == nil {
		mid := strings.TrimSpace(string(b))
		if mid != "" {
			sum := sha256.Sum256([]byte(mid))
			midHex := hex.EncodeToString(sum[:])
			fp.MachineIdSha256 = &midHex
		}
	}
	return fp
}

func urlEscape(s string) string {
	r := strings.NewReplacer(" ", "%20")
	return r.Replace(s)
}

// handleDVRStart handles DVR start requests from Foghorn (for storage nodes)
func handleDVRStart(logger logging.Logger, req *pb.DVRStartRequest, send func(*pb.ControlMessage)) {
	dvrHash := req.GetDvrHash()
	streamID := req.GetStreamId()
	internalName := req.GetInternalName()
	sourceURL := req.GetSourceBaseUrl()
	requestID := req.GetRequestId()
	config := req.GetConfig()

	logger.WithFields(logging.Fields{
		"dvr_hash":      dvrHash,
		"stream_id":     streamID,
		"internal_name": internalName,
		"source_url":    sourceURL,
		"request_id":    requestID,
		"format":        config.GetFormat(),
		"retention":     config.GetRetentionDays(),
	}).Info("Starting DVR recording")

	// Initialize DVR manager if not already done
	initDVRManager()

	// Start the DVR recording job
	if err := dvrManager.StartRecording(dvrHash, streamID, internalName, sourceURL, config, send); err != nil {
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

// handleClipDelete handles a clip delete request from Foghorn
func handleClipDelete(logger logging.Logger, req *pb.ClipDeleteRequest, send func(*pb.ControlMessage)) {
	clipHash := req.GetClipHash()
	requestID := req.GetRequestId()

	logger.WithFields(logging.Fields{
		"clip_hash":  clipHash,
		"request_id": requestID,
	}).Info("Deleting clip files")

	// Use the registered delete handler
	if deleteClipFn == nil {
		logger.Error("Clip delete handler not registered, cannot delete clip")
		return
	}

	sizeBytes, err := deleteClipFn(clipHash)
	if err != nil {
		logger.WithFields(logging.Fields{
			"clip_hash": clipHash,
			"error":     err,
		}).Error("Failed to delete clip files")
		return
	}

	// Send artifact deleted notification back to Foghorn
	if send != nil {
		send(&pb.ControlMessage{SentAt: timestamppb.Now(), Payload: &pb.ControlMessage_ArtifactDeleted{ArtifactDeleted: &pb.ArtifactDeleted{
			ClipHash:     clipHash, // Deprecated, kept for compatibility
			ArtifactHash: clipHash,
			ArtifactType: "clip",
			Reason:       "manual",
			NodeId:       currentNodeID,
			SizeBytes:    sizeBytes,
		}}})
	}

	logger.WithFields(logging.Fields{
		"clip_hash":  clipHash,
		"size_bytes": sizeBytes,
	}).Info("Clip deleted successfully")
}

// handleDVRDelete handles a DVR delete request from Foghorn
func handleDVRDelete(logger logging.Logger, req *pb.DVRDeleteRequest, send func(*pb.ControlMessage)) {
	dvrHash := req.GetDvrHash()
	requestID := req.GetRequestId()

	logger.WithFields(logging.Fields{
		"dvr_hash":   dvrHash,
		"request_id": requestID,
	}).Info("Deleting DVR recording files")

	// Initialize DVR manager if not already done
	initDVRManager()

	// Stop the recording first if it's active
	_ = dvrManager.StopRecording(dvrHash)

	// Use the registered delete handler
	if deleteDVRFn == nil {
		logger.Error("DVR delete handler not registered, cannot delete DVR")
		return
	}

	sizeBytes, err := deleteDVRFn(dvrHash)
	if err != nil {
		logger.WithFields(logging.Fields{
			"dvr_hash": dvrHash,
			"error":    err,
		}).Error("Failed to delete DVR files")
		return
	}

	// Send DVR stopped notification with deleted status
	if send != nil {
		send(&pb.ControlMessage{SentAt: timestamppb.Now(), Payload: &pb.ControlMessage_DvrStopped{DvrStopped: &pb.DVRStopped{
			RequestId: requestID,
			DvrHash:   dvrHash,
			Status:    "deleted",
		}}})
	}

	logger.WithFields(logging.Fields{
		"dvr_hash":   dvrHash,
		"size_bytes": sizeBytes,
	}).Info("DVR recording deleted successfully")
}

// handleVodDelete handles a VOD delete request from Foghorn
func handleVodDelete(logger logging.Logger, req *pb.VodDeleteRequest, send func(*pb.ControlMessage)) {
	vodHash := req.GetVodHash()
	requestID := req.GetRequestId()

	logger.WithFields(logging.Fields{
		"vod_hash":   vodHash,
		"request_id": requestID,
	}).Info("Deleting VOD asset files")

	// Use the registered delete handler
	if deleteVodFn == nil {
		logger.Error("VOD delete handler not registered, cannot delete VOD")
		return
	}

	sizeBytes, err := deleteVodFn(vodHash)
	if err != nil {
		logger.WithFields(logging.Fields{
			"vod_hash": vodHash,
			"error":    err,
		}).Error("Failed to delete VOD files")
		return
	}

	// Send artifact deleted notification back to Foghorn
	if send != nil {
		send(&pb.ControlMessage{SentAt: timestamppb.Now(), Payload: &pb.ControlMessage_ArtifactDeleted{ArtifactDeleted: &pb.ArtifactDeleted{
			ClipHash:     vodHash, // Deprecated, kept for compatibility
			ArtifactHash: vodHash,
			ArtifactType: "vod",
			Reason:       "manual",
			NodeId:       currentNodeID,
			SizeBytes:    sizeBytes,
		}}})
	}

	logger.WithFields(logging.Fields{
		"vod_hash":   vodHash,
		"size_bytes": sizeBytes,
	}).Info("VOD asset deleted successfully")
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

// ==================== Cold Storage (Freeze/Defrost) Functions ====================

// FreezePermissionHandler is called when Foghorn responds to a freeze permission request
type FreezePermissionHandler func(*pb.FreezePermissionResponse)

// DefrostRequestHandler is called when Foghorn sends a defrost request
type DefrostRequestHandler func(*pb.DefrostRequest)

// DtshSyncRequestHandler is called when Foghorn sends a request to sync just the .dtsh file
type DtshSyncRequestHandler func(*pb.DtshSyncRequest)

var (
	freezePermissionHandlers = make(map[string]chan *pb.FreezePermissionResponse)
	freezePermissionMutex    = make(chan struct{}, 1)
	defrostRequestHandler    DefrostRequestHandler
	dtshSyncRequestHandler   DtshSyncRequestHandler

	// CanDelete request/response tracking
	canDeleteHandlers = make(map[string]chan *pb.CanDeleteResponse)
	canDeleteMutex    = make(chan struct{}, 1)
)

// SetDefrostRequestHandler sets the callback for defrost requests from Foghorn
func SetDefrostRequestHandler(handler DefrostRequestHandler) {
	defrostRequestHandler = handler
}

// SetDtshSyncRequestHandler sets the callback for incremental .dtsh sync requests from Foghorn
func SetDtshSyncRequestHandler(handler DtshSyncRequestHandler) {
	dtshSyncRequestHandler = handler
}

// RequestFreezePermission asks Foghorn for permission and presigned URL to freeze an asset.
// This is a blocking call that waits for Foghorn's response.
func RequestFreezePermission(ctx context.Context, assetType, assetHash, localPath string, sizeBytes uint64, filenames []string) (*pb.FreezePermissionResponse, error) {
	stream := currentStream
	if stream == nil {
		return nil, fmt.Errorf("gRPC control stream not connected")
	}

	requestID := uuid.New().String()
	responseChan := make(chan *pb.FreezePermissionResponse, 1)

	// Register for response
	freezePermissionMutex <- struct{}{}
	freezePermissionHandlers[requestID] = responseChan
	<-freezePermissionMutex

	// Send request
	req := &pb.FreezePermissionRequest{
		RequestId: requestID,
		AssetType: assetType,
		AssetHash: assetHash,
		LocalPath: localPath,
		SizeBytes: sizeBytes,
		NodeId:    currentNodeID,
		Filenames: filenames,
	}

	msg := &pb.ControlMessage{SentAt: timestamppb.Now(), Payload: &pb.ControlMessage_FreezePermissionRequest{FreezePermissionRequest: req}}
	if err := stream.Send(msg); err != nil {
		// Cleanup
		freezePermissionMutex <- struct{}{}
		delete(freezePermissionHandlers, requestID)
		<-freezePermissionMutex
		return nil, fmt.Errorf("failed to send freeze permission request: %w", err)
	}

	// Wait for response with timeout
	select {
	case resp := <-responseChan:
		// Cleanup
		freezePermissionMutex <- struct{}{}
		delete(freezePermissionHandlers, requestID)
		<-freezePermissionMutex
		return resp, nil
	case <-ctx.Done():
		// Cleanup on timeout
		freezePermissionMutex <- struct{}{}
		delete(freezePermissionHandlers, requestID)
		<-freezePermissionMutex
		return nil, ctx.Err()
	case <-time.After(30 * time.Second):
		// Cleanup on default timeout
		freezePermissionMutex <- struct{}{}
		delete(freezePermissionHandlers, requestID)
		<-freezePermissionMutex
		return nil, fmt.Errorf("timeout waiting for freeze permission response")
	}
}

// handleFreezePermissionResponse processes FreezePermissionResponse messages from the stream
func handleFreezePermissionResponse(response *pb.FreezePermissionResponse) {
	freezePermissionMutex <- struct{}{}
	responseChan, exists := freezePermissionHandlers[response.RequestId]
	<-freezePermissionMutex

	if exists {
		responseChan <- response
	}
}

// SendFreezeProgress sends upload progress to Foghorn
func SendFreezeProgress(requestID, assetHash string, percent uint32, bytesUploaded uint64) error {
	stream := currentStream
	if stream == nil {
		return fmt.Errorf("gRPC control stream not connected")
	}

	progress := &pb.FreezeProgress{
		RequestId:     requestID,
		AssetHash:     assetHash,
		Percent:       percent,
		BytesUploaded: bytesUploaded,
	}

	msg := &pb.ControlMessage{SentAt: timestamppb.Now(), Payload: &pb.ControlMessage_FreezeProgress{FreezeProgress: progress}}
	return stream.Send(msg)
}

// SendFreezeComplete sends freeze completion status to Foghorn
func SendFreezeComplete(requestID, assetHash, status, s3URL string, sizeBytes uint64, errMsg string) error {
	stream := currentStream
	if stream == nil {
		return fmt.Errorf("gRPC control stream not connected")
	}

	complete := &pb.FreezeComplete{
		RequestId: requestID,
		AssetHash: assetHash,
		Status:    status,
		S3Url:     s3URL,
		SizeBytes: sizeBytes,
		Error:     errMsg,
	}

	msg := &pb.ControlMessage{SentAt: timestamppb.Now(), Payload: &pb.ControlMessage_FreezeComplete{FreezeComplete: complete}}
	return stream.Send(msg)
}

// SendDefrostProgress sends download progress to Foghorn
func SendDefrostProgress(requestID, assetHash string, percent uint32, bytesDownloaded uint64, segmentsDownloaded, totalSegments int32, message string) error {
	stream := currentStream
	if stream == nil {
		return fmt.Errorf("gRPC control stream not connected")
	}

	progress := &pb.DefrostProgress{
		RequestId:          requestID,
		AssetHash:          assetHash,
		Percent:            percent,
		BytesDownloaded:    bytesDownloaded,
		SegmentsDownloaded: segmentsDownloaded,
		TotalSegments:      totalSegments,
		Message:            message,
	}

	msg := &pb.ControlMessage{SentAt: timestamppb.Now(), Payload: &pb.ControlMessage_DefrostProgress{DefrostProgress: progress}}
	return stream.Send(msg)
}

// SendDefrostComplete sends defrost completion status to Foghorn
func SendDefrostComplete(requestID, assetHash, status, localPath string, sizeBytes uint64, errMsg string) error {
	stream := currentStream
	if stream == nil {
		return fmt.Errorf("gRPC control stream not connected")
	}

	complete := &pb.DefrostComplete{
		RequestId: requestID,
		AssetHash: assetHash,
		Status:    status,
		LocalPath: localPath,
		SizeBytes: sizeBytes,
		Error:     errMsg,
		NodeId:    currentNodeID,
	}

	msg := &pb.ControlMessage{SentAt: timestamppb.Now(), Payload: &pb.ControlMessage_DefrostComplete{DefrostComplete: complete}}
	return stream.Send(msg)
}

// SendStorageLifecycle sends a storage lifecycle event to Foghorn (for analytics)
// StorageLifecycleData is sent via MistTrigger payload
func SendStorageLifecycle(data *pb.StorageLifecycleData) error {
	stream := currentStream
	if stream == nil {
		return fmt.Errorf("gRPC control stream not connected")
	}

	// StorageLifecycleData is sent as a MistTrigger with storage_lifecycle_data payload
	trigger := &pb.MistTrigger{
		TriggerType: "storage_lifecycle",
		RequestId:   uuid.New().String(),
		NodeId:      currentNodeID,
		Blocking:    false,
		TriggerPayload: &pb.MistTrigger_StorageLifecycleData{
			StorageLifecycleData: data,
		},
	}

	msg := &pb.ControlMessage{SentAt: timestamppb.Now(), Payload: &pb.ControlMessage_MistTrigger{MistTrigger: trigger}}
	return stream.Send(msg)
}

// SendProcessBillingEvent sends a process billing event to Foghorn (for analytics/billing)
// ProcessBillingEvent tracks transcoding usage for Livepeer and native processes
func SendProcessBillingEvent(event *pb.ProcessBillingEvent) error {
	processType := event.ProcessType
	stream := currentStream
	if stream == nil {
		BillingEventsSent.WithLabelValues(processType, "stream_disconnected").Inc()
		return fmt.Errorf("gRPC control stream not connected")
	}

	// Ensure node_id is set
	if event.NodeId == "" {
		event.NodeId = currentNodeID
	}

	trigger := &pb.MistTrigger{
		TriggerType: "process_billing",
		RequestId:   uuid.New().String(),
		NodeId:      currentNodeID,
		Blocking:    false,
		TriggerPayload: &pb.MistTrigger_ProcessBilling{
			ProcessBilling: event,
		},
	}

	// TRACE: Log what we're sending
	fmt.Printf("[HELMSMAN TRACE] Sending process_billing trigger: payload_type=%T, payload_nil=%v\n",
		trigger.GetTriggerPayload(), trigger.GetTriggerPayload() == nil)

	msg := &pb.ControlMessage{SentAt: timestamppb.Now(), Payload: &pb.ControlMessage_MistTrigger{MistTrigger: trigger}}
	if err := stream.Send(msg); err != nil {
		BillingEventsSent.WithLabelValues(processType, "error").Inc()
		return err
	}
	BillingEventsSent.WithLabelValues(processType, "success").Inc()
	return nil
}

// IsConnected returns true if the control stream is connected
func IsConnected() bool {
	return currentStream != nil
}

// ==================== Dual-Storage (Sync/CanDelete) Functions ====================

// RequestCanDelete asks Foghorn if it's safe to delete a local artifact copy.
// Returns true if the asset is synced to S3 and can be safely deleted locally.
// Also returns warm_duration_ms (how long the asset was cached before eviction).
func RequestCanDelete(ctx context.Context, assetHash string) (bool, string, int64, error) {
	stream := currentStream
	if stream == nil {
		return false, "", 0, fmt.Errorf("gRPC control stream not connected")
	}

	responseChan := make(chan *pb.CanDeleteResponse, 1)

	// Register for response
	canDeleteMutex <- struct{}{}
	canDeleteHandlers[assetHash] = responseChan
	<-canDeleteMutex

	// Send request
	req := &pb.CanDeleteRequest{
		AssetHash: assetHash,
		NodeId:    currentNodeID,
	}

	msg := &pb.ControlMessage{SentAt: timestamppb.Now(), Payload: &pb.ControlMessage_CanDeleteRequest{CanDeleteRequest: req}}
	if err := stream.Send(msg); err != nil {
		// Cleanup
		canDeleteMutex <- struct{}{}
		delete(canDeleteHandlers, assetHash)
		<-canDeleteMutex
		return false, "", 0, fmt.Errorf("failed to send can-delete request: %w", err)
	}

	// Wait for response with timeout
	select {
	case resp := <-responseChan:
		// Cleanup
		canDeleteMutex <- struct{}{}
		delete(canDeleteHandlers, assetHash)
		<-canDeleteMutex
		return resp.SafeToDelete, resp.Reason, resp.WarmDurationMs, nil
	case <-ctx.Done():
		// Cleanup on timeout
		canDeleteMutex <- struct{}{}
		delete(canDeleteHandlers, assetHash)
		<-canDeleteMutex
		return false, "", 0, ctx.Err()
	case <-time.After(10 * time.Second):
		// Cleanup on default timeout
		canDeleteMutex <- struct{}{}
		delete(canDeleteHandlers, assetHash)
		<-canDeleteMutex
		return false, "", 0, fmt.Errorf("timeout waiting for can-delete response")
	}
}

// handleCanDeleteResponse processes CanDeleteResponse messages from the stream
func handleCanDeleteResponse(response *pb.CanDeleteResponse) {
	canDeleteMutex <- struct{}{}
	responseChan, exists := canDeleteHandlers[response.AssetHash]
	<-canDeleteMutex

	if exists {
		responseChan <- response
	}
}

// SendSyncComplete notifies Foghorn that a sync operation has completed.
// Called after successfully uploading an artifact to S3 (while keeping the local copy).
// dtshIncluded indicates whether the .dtsh index file was included in the sync.
func SendSyncComplete(requestID, assetHash, status, s3URL string, sizeBytes uint64, errMsg string, dtshIncluded bool) error {
	stream := currentStream
	if stream == nil {
		return fmt.Errorf("gRPC control stream not connected")
	}

	complete := &pb.SyncComplete{
		RequestId:    requestID,
		AssetHash:    assetHash,
		Status:       status,
		S3Url:        s3URL,
		SizeBytes:    sizeBytes,
		Error:        errMsg,
		NodeId:       currentNodeID,
		DtshIncluded: dtshIncluded,
	}

	msg := &pb.ControlMessage{SentAt: timestamppb.Now(), Payload: &pb.ControlMessage_SyncComplete{SyncComplete: complete}}
	return stream.Send(msg)
}

// handleStopSessions terminates all sessions for the given streams on this node
// Called when a tenant is suspended due to insufficient balance
func handleStopSessions(logger logging.Logger, req *pb.StopSessionsRequest) {
	if len(req.StreamNames) == 0 {
		return
	}

	cfg := currentConfig
	if cfg == nil {
		logger.Warn("config not initialized; cannot stop sessions")
		return
	}

	mistClient := mist.NewClient(logger)
	if cfg.MistServerURL != "" {
		mistClient.BaseURL = cfg.MistServerURL
	}

	logger.WithFields(logging.Fields{
		"tenant_id":    req.TenantId,
		"reason":       req.Reason,
		"stream_count": len(req.StreamNames),
		"stream_names": req.StreamNames,
	}).Info("Stopping sessions for suspended tenant")

	// Use batch stop_sessions API
	err := mistClient.StopSessionsMultiple(req.StreamNames)
	if err != nil {
		logger.WithFields(logging.Fields{
			"tenant_id":    req.TenantId,
			"stream_names": req.StreamNames,
			"error":        err,
		}).Error("Failed to stop sessions via MistServer API")
		return
	}

	logger.WithFields(logging.Fields{
		"tenant_id":    req.TenantId,
		"stream_names": req.StreamNames,
	}).Info("Successfully stopped sessions for suspended tenant")
}

// parseRequestedMode converts a string mode to protobuf enum for Register message.
func parseRequestedMode(mode string) pb.NodeOperationalMode {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "draining", "drain":
		return pb.NodeOperationalMode_NODE_OPERATIONAL_MODE_DRAINING
	case "maintenance", "maint":
		return pb.NodeOperationalMode_NODE_OPERATIONAL_MODE_MAINTENANCE
	case "", "normal":
		return pb.NodeOperationalMode_NODE_OPERATIONAL_MODE_NORMAL
	default:
		return pb.NodeOperationalMode_NODE_OPERATIONAL_MODE_UNSPECIFIED
	}
}
