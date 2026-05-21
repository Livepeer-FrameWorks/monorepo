package control

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	sidecarcfg "frameworks/api_sidecar/internal/config"
	"frameworks/api_sidecar/internal/leases"
	"frameworks/api_sidecar/internal/storage"
	"frameworks/api_sidecar/internal/updater"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/grpcutil"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/mist"
	pb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto"

	"github.com/google/uuid"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

const foghornInternalServerName = "foghorn.internal"

// DeleteClipFunc is the function type for clip deletion
type DeleteClipFunc func(clipHash string) (uint64, error)

// DeleteDVRFunc is the function type for DVR deletion
type DeleteDVRFunc func(dvrHash string) (uint64, error)

// DeleteVodFunc is the function type for VOD deletion
type DeleteVodFunc func(vodHash string) (uint64, error)

type streamConn struct {
	stream pb.HelmsmanControl_ConnectClient
	nodeID string
}

var activeConn atomic.Pointer[streamConn]

func getStream() pb.HelmsmanControl_ConnectClient {
	c := activeConn.Load()
	if c == nil {
		return nil
	}
	return c.stream
}

func getNodeID() string {
	c := activeConn.Load()
	if c == nil {
		return ""
	}
	return c.nodeID
}

// GetNodeID returns the current node ID from the active control stream connection.
func GetNodeID() string {
	return getNodeID()
}

func storeConn(stream pb.HelmsmanControl_ConnectClient, nodeID string) {
	activeConn.Store(&streamConn{stream: stream, nodeID: nodeID})
}

func clearConn() {
	activeConn.Store(nil)
}

// Global state for metrics streaming
var (
	pkgLogger      logging.Logger
	currentConfig  *sidecarcfg.HelmsmanConfig
	onSeed         func()
	onStorageWrite func()
	deleteClipFn   DeleteClipFunc
	deleteDVRFn    DeleteDVRFunc
	deleteVodFn    DeleteVodFunc

	blockingGraceMs     int
	streamReconnected   = make(chan struct{})
	streamReconnectedM  sync.Mutex
	onControlConnected  func()
	onControlConnectedM sync.Mutex

	disconnectNotify   = make(chan struct{})
	disconnectNotifyMu sync.Mutex

	// test-only hook to avoid flake in disconnect retry tests
	disconnectSubscribedHook chan struct{}

	jitterRandMu sync.Mutex
	jitterRand   = rand.New(rand.NewSource(time.Now().UnixNano()))

	// Outbox for messages that failed to send during disconnect.
	// Drained on reconnect after successful re-registration.
	outboxMu  sync.Mutex
	outbox    []*pb.ControlMessage
	maxOutbox = 100
)

const (
	blockingTriggerTimeout            = 5 * time.Second
	desiredStateComponentApplyTimeout = 30 * time.Minute
	maxBlockingAttempts               = 3
	reconnectJitterPct                = 25
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

// SetOnControlConnected sets a callback that runs after each successful
// registration with Foghorn and before queued control messages are drained.
func SetOnControlConnected(fn func()) {
	onControlConnectedM.Lock()
	onControlConnected = fn
	onControlConnectedM.Unlock()
}

func notifyControlConnected() {
	onControlConnectedM.Lock()
	fn := onControlConnected
	onControlConnectedM.Unlock()
	if fn != nil {
		go fn()
	}
}

// Start launches the Helmsman control client and maintains the stream to Foghorn
func Start(logger logging.Logger, cfg *sidecarcfg.HelmsmanConfig) {
	pkgLogger = logger
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
	return getNodeID()
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

	attempts := max(maxBlockingAttempts, 1)
	deadline := time.Now().Add(blockingTriggerTimeout)

	var lastErr error
	for attempt := 1; attempt <= attempts; attempt++ {
		if time.Now().After(deadline) {
			break
		}

		stream := getStream()
		if stream == nil && blockingGraceMs > 0 {
			remaining := time.Until(deadline)
			if remaining <= 0 {
				break
			}
			grace := min(time.Duration(blockingGraceMs)*time.Millisecond, remaining)
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

		// Register response channel BEFORE send — same pattern as RequestFreezePermission.
		// The buffered channel catches Foghorn's reply even if it arrives before the select.
		responseCh := make(chan *pb.MistTriggerResponse, 1)
		pendingMutex <- struct{}{}
		pendingMistTriggers[mistTrigger.RequestId] = responseCh
		<-pendingMutex

		if err := sendMistTriggerOnce(triggerType, mistTrigger); err != nil {
			pendingMutex <- struct{}{}
			delete(pendingMistTriggers, mistTrigger.RequestId)
			<-pendingMutex
			BlockingTriggerRetries.WithLabelValues(triggerType, "send_error").Inc()
			lastErr = err
			continue
		}

		remaining := time.Until(deadline)
		if remaining <= 0 {
			pendingMutex <- struct{}{}
			delete(pendingMistTriggers, mistTrigger.RequestId)
			<-pendingMutex
			break
		}
		result, err := awaitMistTriggerResponse(responseCh, mistTrigger.RequestId, remaining)
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
	stream := getStream()
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

// awaitMistTriggerResponse waits on a pre-registered response channel.
// The channel must be created and inserted into pendingMistTriggers BEFORE
// the trigger is sent — otherwise a fast Foghorn reply races the registration.
func awaitMistTriggerResponse(responseCh chan *pb.MistTriggerResponse, requestID string, timeout time.Duration) (*MistTriggerResult, error) {
	disconnectNotifyMu.Lock()
	disconnectCh := disconnectNotify
	disconnectNotifyMu.Unlock()

	// test hook: allow tests to synchronize on subscription to disconnect notifications
	if disconnectSubscribedHook != nil {
		select {
		case disconnectSubscribedHook <- struct{}{}:
		default:
		}
	}

	// Wait for response, disconnect, or timeout
	select {
	case response := <-responseCh:
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

func handleDesiredStateUpdate(ctx context.Context, logger logging.Logger, requestID string, update *pb.DesiredStateUpdate, send func(*pb.ControlMessage) error) {
	if update == nil {
		return
	}
	result := &pb.UpdateApplyResult{
		NodeId:        update.GetNodeId(),
		TargetRelease: update.GetTargetRelease(),
	}
	restartSelf := false
	for _, component := range update.GetComponents() {
		if component == nil {
			continue
		}
		applyResult := &pb.ComponentApplyResult{
			Component: component.GetComponent(),
			Version:   component.GetVersion(),
		}
		if component.GetDrainRequired() {
			switch {
			case strings.TrimSpace(update.GetCordonToken()) == "":
				applyResult.Detail = "drain-required update missing cordon token"
				result.Components = append(result.Components, applyResult)
				continue
			case update.GetCordonTokenExpiresAt() == nil:
				applyResult.Detail = "drain-required update missing cordon token expiry"
				result.Components = append(result.Components, applyResult)
				continue
			case !update.GetCordonTokenExpiresAt().AsTime().After(time.Now()):
				applyResult.Detail = "drain-required update cordon token expired"
				result.Components = append(result.Components, applyResult)
				continue
			}
		}
		applyCtx, cancel := context.WithTimeout(ctx, desiredStateComponentApplyTimeout)
		outcome := updater.Apply(applyCtx, component)
		cancel()
		applyResult.Success = outcome.Success
		applyResult.Detail = outcome.Detail
		if outcome.RestartSelf {
			restartSelf = true
		}
		result.Components = append(result.Components, applyResult)
	}
	if logger != nil {
		logger.WithFields(logging.Fields{
			"node_id":        result.GetNodeId(),
			"target_release": result.GetTargetRelease(),
			"components":     len(result.GetComponents()),
		}).Info("Processed desired state update")
	}
	msg := &pb.ControlMessage{
		RequestId: requestID,
		SentAt:    timestamppb.Now(),
		Payload:   &pb.ControlMessage_UpdateApplyResult{UpdateApplyResult: result},
	}
	if sendDesiredStateResult(msg, restartSelf, logger, send) {
		scheduleSelfRestart(logger)
	}
}

func sendDesiredStateResult(msg *pb.ControlMessage, restartSelf bool, logger logging.Logger, send func(*pb.ControlMessage) error) bool {
	err := send(msg)
	if err == nil {
		return restartSelf
	}
	if restartSelf {
		if durableErr := enqueueDurableOutbox(msg); durableErr == nil {
			if logger != nil {
				logger.WithError(err).WithField("request_id", msg.GetRequestId()).Warn("Persisted self-update result after send failure")
			}
			return true
		} else if logger != nil {
			logger.WithError(durableErr).WithField("request_id", msg.GetRequestId()).Error("Failed to persist self-update result")
		}
	}
	enqueueOutbox(msg)
	if logger != nil {
		logger.WithError(err).WithField("request_id", msg.GetRequestId()).Warn("Failed to send desired state update result")
	}
	return false
}

func scheduleSelfRestart(logger logging.Logger) {
	go func() {
		time.Sleep(500 * time.Millisecond)
		if logger != nil {
			logger.Info("Restarting Helmsman after self-update")
		}
		os.Exit(0)
	}()
}

func notifyDisconnect() {
	disconnectNotifyMu.Lock()
	close(disconnectNotify)
	disconnectNotify = make(chan struct{})
	disconnectNotifyMu.Unlock()
}

func waitForReconnection(timeout time.Duration) pb.HelmsmanControl_ConnectClient {
	if s := getStream(); s != nil {
		return s
	}

	streamReconnectedM.Lock()
	reconnectCh := streamReconnected
	streamReconnectedM.Unlock()

	select {
	case <-reconnectCh:
		return getStream()
	case <-time.After(timeout):
		return nil
	}
}

// enqueueOutbox saves a message for retry on reconnect.
func enqueueOutbox(msg *pb.ControlMessage) {
	outboxMu.Lock()
	defer outboxMu.Unlock()
	if len(outbox) >= maxOutbox {
		if pkgLogger != nil {
			pkgLogger.WithField("outbox_size", maxOutbox).Warn("Outbox full, dropping oldest message")
		}
		outbox = outbox[1:]
	}
	outbox = append(outbox, msg)
}

// drainOutbox re-sends all queued messages on the current stream.
func drainOutbox(stream pb.HelmsmanControl_ConnectClient) {
	drainDurableOutbox(stream)

	outboxMu.Lock()
	pending := outbox
	outbox = nil
	outboxMu.Unlock()

	for _, msg := range pending {
		msg.SentAt = timestamppb.Now()
		if err := stream.Send(msg); err != nil {
			// Re-enqueue if send fails again
			enqueueOutbox(msg)
		}
	}
}

func enqueueDurableOutbox(msg *pb.ControlMessage) error {
	dir := durableOutboxDir()
	if mkdirErr := os.MkdirAll(dir, 0o700); mkdirErr != nil {
		return mkdirErr
	}
	payload, err := proto.Marshal(msg)
	if err != nil {
		return err
	}
	name := fmt.Sprintf("%d-%s.pb", time.Now().UnixNano(), safeOutboxID(msg.GetRequestId()))
	path := filepath.Join(dir, name)
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, payload, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func drainDurableOutbox(stream pb.HelmsmanControl_ConnectClient) {
	dir := durableOutboxDir()
	files, err := filepath.Glob(filepath.Join(dir, "*.pb"))
	if err != nil {
		if pkgLogger != nil {
			pkgLogger.WithError(err).Warn("Unable to list durable control outbox")
		}
		return
	}
	sort.Strings(files)
	for _, path := range files {
		payload, err := os.ReadFile(path)
		if err != nil {
			if pkgLogger != nil {
				pkgLogger.WithError(err).WithField("path", path).Warn("Unable to read durable control outbox message")
			}
			return
		}
		var msg pb.ControlMessage
		if err := proto.Unmarshal(payload, &msg); err != nil {
			if pkgLogger != nil {
				pkgLogger.WithError(err).WithField("path", path).Warn("Dropping unreadable durable control outbox message")
			}
			_ = os.Remove(path)
			continue
		}
		msg.SentAt = timestamppb.Now()
		if err := stream.Send(&msg); err != nil {
			if pkgLogger != nil {
				pkgLogger.WithError(err).WithField("path", path).Warn("Failed to drain durable control outbox")
			}
			return
		}
		if err := os.Remove(path); err != nil && pkgLogger != nil {
			pkgLogger.WithError(err).WithField("path", path).Warn("Failed to remove durable control outbox message")
		}
	}
}

func durableOutboxDir() string {
	if dir := strings.TrimSpace(os.Getenv("FRAMEWORKS_CONTROL_OUTBOX_DIR")); dir != "" {
		return dir
	}
	cacheDir, err := os.UserCacheDir()
	if err != nil {
		return filepath.Join(os.TempDir(), "frameworks-control-outbox")
	}
	return filepath.Join(cacheDir, "frameworks", "control-outbox")
}

func safeOutboxID(id string) string {
	id = strings.TrimSpace(id)
	if id == "" {
		return "message"
	}
	var b strings.Builder
	for _, r := range id {
		switch {
		case r >= 'a' && r <= 'z':
			b.WriteRune(r)
		case r >= 'A' && r <= 'Z':
			b.WriteRune(r)
		case r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '-' || r == '_':
			b.WriteRune(r)
		}
	}
	if b.Len() == 0 {
		return "message"
	}
	return b.String()
}

// sendOrEnqueue attempts to send a message on the active stream.
// If the stream is disconnected, the message is saved to the outbox for retry on reconnect.
func sendOrEnqueue(msg *pb.ControlMessage) error {
	stream := getStream()
	if stream == nil {
		enqueueOutbox(msg)
		return fmt.Errorf("gRPC control stream not connected (queued for retry)")
	}
	if err := stream.Send(msg); err != nil {
		enqueueOutbox(msg)
		return err
	}
	return nil
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

// SendArtifactDeleted notifies Foghorn that an artifact has been deleted
func SendArtifactDeleted(artifactHash, filePath, reason, artifactType string, sizeBytes uint64) error {
	stream := getStream()
	if stream == nil {
		return fmt.Errorf("gRPC control stream not connected")
	}

	artifactDeleted := &pb.ArtifactDeleted{
		ArtifactHash: artifactHash,
		ArtifactType: artifactType,
		FilePath:     filePath,
		Reason:       reason,
		NodeId:       getNodeID(),
		SizeBytes:    sizeBytes,
	}

	msg := &pb.ControlMessage{SentAt: timestamppb.Now(), Payload: &pb.ControlMessage_ArtifactDeleted{ArtifactDeleted: artifactDeleted}}
	return stream.Send(msg)
}

// SendModeChangeRequest sends an operational mode change request upstream to Foghorn.
// Called by the local HTTP API when an agent or CLI requests a mode change.
func SendModeChangeRequest(mode pb.NodeOperationalMode, reason string) error {
	stream := getStream()
	if stream == nil {
		return fmt.Errorf("gRPC control stream not connected")
	}

	msg := &pb.ControlMessage{
		SentAt: timestamppb.Now(),
		Payload: &pb.ControlMessage_ModeChangeRequest{
			ModeChangeRequest: &pb.ModeChangeRequest{
				RequestedMode: mode,
				Reason:        reason,
			},
		},
	}
	return stream.Send(msg)
}

func runClient(addr string, logger logging.Logger) error {
	cfg := currentConfig
	if cfg == nil {
		return fmt.Errorf("config not initialized")
	}

	// Use TLS whenever the deployment requires secure transport or trust
	// material is present. Bare Docker service names still use insecure
	// transport in local development when explicitly allowed.
	useTLS := !cfg.GRPCAllowInsecure ||
		cfg.GRPCTLSCAPath != "" ||
		(cfg.GRPCTLSCertPath != "" && cfg.GRPCTLSKeyPath != "") ||
		grpcutil.AddrIsFQDN(addr)
	var creds credentials.TransportCredentials
	if useTLS {
		if cfg.GRPCTLSCertPath != "" && cfg.GRPCTLSKeyPath != "" {
			cert, err := tls.LoadX509KeyPair(cfg.GRPCTLSCertPath, cfg.GRPCTLSKeyPath)
			if err != nil {
				return fmt.Errorf("failed to load TLS certificates: %w", err)
			}
			rootCAs, err := loadSidecarRootCAs(cfg.GRPCTLSCAPath)
			if err != nil {
				return err
			}
			creds = credentials.NewTLS(&tls.Config{
				MinVersion:   tls.VersionTLS12,
				RootCAs:      rootCAs,
				Certificates: []tls.Certificate{cert},
				ServerName:   foghornControlServerName(addr, cfg.GRPCTLSServerName),
			})
		} else {
			var err error
			creds, err = grpcutil.ClientTransportCredentials(grpcutil.ClientTLSConfig{
				CACertFile:        cfg.GRPCTLSCAPath,
				ServerName:        foghornControlServerName(addr, cfg.GRPCTLSServerName),
				DefaultServerName: foghornInternalServerName,
			}, logger)
			if err != nil {
				return err
			}
		}
		logger.Info("Connecting to gRPC server with TLS")
	} else {
		var err error
		creds, err = grpcutil.ClientTransportCredentials(grpcutil.ClientTLSConfig{
			AllowInsecure: true,
		}, logger)
		if err != nil {
			return err
		}
		logger.Info("Connecting to gRPC server without TLS")
	}

	conn, err := grpc.NewClient(
		addr,
		grpc.WithTransportCredentials(creds),
		grpc.WithConnectParams(grpc.ConnectParams{MinConnectTimeout: 10 * time.Second}),
	)
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close() }()
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
		RelayBaseUrl:    relayBaseURL(),
	}}}
	if err := stream.Send(reg); err != nil {
		return err
	}

	// Store current stream for external access
	storeConn(stream, nodeID)
	ControlStreamStatus.Set(1)
	streamReconnectedM.Lock()
	close(streamReconnected)
	streamReconnected = make(chan struct{})
	streamReconnectedM.Unlock()
	notifyControlConnected()

	// Re-send any messages queued during disconnect
	drainOutbox(stream)
	defer func() {
		clearConn()
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
				if errors.Is(err, io.EOF) {
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
				// Receive desired config seed and trigger reconcile.
				// The sender callback lets Helmsman ACK back over the
				// existing bidi stream after TLS bundles are applied and
				// Caddy is reloaded; Foghorn gates DNS publishing on this.
				if x.ConfigSeed != nil {
					ackSender := func(m *pb.ControlMessage) {
						if sendErr := stream.Send(m); sendErr != nil {
							logger.WithError(sendErr).Debug("Failed to send ConfigSeedApplyResult ACK")
						}
					}
					sidecarcfg.ApplySeed(x.ConfigSeed, ackSender)
					// Adopt canonical node_id from seed if provided
					if nid := x.ConfigSeed.GetNodeId(); nid != "" {
						storeConn(getStream(), nid)
					}
				}
			case *pb.ControlMessage_DesiredStateUpdate:
				go handleDesiredStateUpdate(stream.Context(), logger, msg.GetRequestId(), x.DesiredStateUpdate, stream.Send)
			case *pb.ControlMessage_FreezePermissionResponse:
				// Handle freeze permission response from Foghorn
				go handleFreezePermissionResponse(x.FreezePermissionResponse)
			case *pb.ControlMessage_RecordDvrSegmentResponse:
				go handleRecordDVRSegmentResponse(x.RecordDvrSegmentResponse)
			case *pb.ControlMessage_EvictableSegmentsResponse:
				go handleEvictableSegmentsResponse(x.EvictableSegmentsResponse)
			case *pb.ControlMessage_RestoreLocalSegmentIndexResponse:
				go handleRestoreLocalSegmentIndexResponse(x.RestoreLocalSegmentIndexResponse)
			case *pb.ControlMessage_RetryDvrSegmentUpload:
				if retryDVRSegmentHandler != nil {
					go retryDVRSegmentHandler(x.RetryDvrSegmentUpload)
				}
			case *pb.ControlMessage_ReclaimDvrSegment:
				if reclaimDVRSegmentHandler != nil {
					go reclaimDVRSegmentHandler(x.ReclaimDvrSegment)
				}
			case *pb.ControlMessage_DefrostRequest:
				// Handle defrost request from Foghorn
				if defrostRequestHandler != nil {
					go defrostRequestHandler(x.DefrostRequest)
				}
			case *pb.ControlMessage_FreezeRequest:
				if freezeRequestHandler != nil {
					go freezeRequestHandler(x.FreezeRequest)
				}
			case *pb.ControlMessage_CanDeleteResponse:
				// Handle can-delete response from Foghorn
				go handleCanDeleteResponse(x.CanDeleteResponse)
			case *pb.ControlMessage_RelayResolveResponse:
				// Relay resolve response: route to the waiting goroutine keyed
				// by request_id.
				go handleRelayResolveResponse(x.RelayResolveResponse)
			case *pb.ControlMessage_DtshSyncRequest:
				// Handle incremental .dtsh sync request from Foghorn
				if dtshSyncRequestHandler != nil {
					go dtshSyncRequestHandler(x.DtshSyncRequest)
				}
			case *pb.ControlMessage_StopSessionsRequest:
				// Handle stop sessions request from Foghorn (billing suspension)
				go handleStopSessions(logger, x.StopSessionsRequest)
			case *pb.ControlMessage_InvalidateSessionsRequest:
				// Re-run USER_NEW for active sessions after a playback policy
				// or signing-key change. Does NOT disconnect viewers.
				go handleInvalidateSessions(logger, x.InvalidateSessionsRequest)
			case *pb.ControlMessage_ActivatePushTargets:
				go handleActivatePushTargets(logger, x.ActivatePushTargets)
			case *pb.ControlMessage_DeactivatePushTargets:
				go handleDeactivatePushTargets(logger, x.DeactivatePushTargets)
			case *pb.ControlMessage_ValidateEdgeTokenResponse:
				handleValidateEdgeTokenResponse(msg.GetRequestId(), x.ValidateEdgeTokenResponse)
			case *pb.ControlMessage_EdgeMistAdminSessionResponse:
				handleEdgeMistAdminSessionResponse(msg.GetRequestId(), x.EdgeMistAdminSessionResponse)
			case *pb.ControlMessage_ThumbnailUploadResponse:
				go handleThumbnailUploadResponse(logger, x.ThumbnailUploadResponse, func(m *pb.ControlMessage) { _ = stream.Send(m) })
			case *pb.ControlMessage_ProcessingJobRequest:
				if processingJobHandler != nil {
					go processingJobHandler(x.ProcessingJobRequest, func(m *pb.ControlMessage) { _ = stream.Send(m) })
				}
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

func loadSidecarRootCAs(caPath string) (*x509.CertPool, error) {
	if strings.TrimSpace(caPath) == "" {
		pool, err := x509.SystemCertPool()
		if err != nil {
			return nil, fmt.Errorf("load system cert pool: %w", err)
		}
		if pool == nil {
			pool = x509.NewCertPool()
		}
		return pool, nil
	}

	pool, err := x509.SystemCertPool()
	if err != nil {
		return nil, fmt.Errorf("load system cert pool: %w", err)
	}
	if pool == nil {
		pool = x509.NewCertPool()
	}

	pemBytes, err := os.ReadFile(caPath)
	if err != nil {
		return nil, fmt.Errorf("read sidecar CA cert %q: %w", caPath, err)
	}
	if !pool.AppendCertsFromPEM(pemBytes) {
		return nil, fmt.Errorf("append sidecar CA cert %q: invalid PEM", caPath)
	}
	return pool, nil
}

func foghornControlServerName(addr, override string) string {
	if override = strings.TrimSpace(override); override != "" {
		return override
	}
	host := strings.TrimSpace(addr)
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	host = strings.Trim(strings.TrimSpace(host), "[]")
	if grpcutil.AddrIsFQDN(addr) && host != foghornInternalServerName && !strings.HasSuffix(host, ".internal") {
		return host
	}
	return foghornInternalServerName
}

func handleClipPull(logger logging.Logger, req *pb.ClipPullRequest, send func(*pb.ControlMessage)) {
	cfg := currentConfig
	if cfg == nil {
		logger.Warn("config not initialized; dropping clip request")
		return
	}

	mistBase := req.GetSourceBaseUrl()
	localMistSource := mistBase == ""
	if mistBase == "" {
		mistBase = deriveMistHTTPBase(cfg.MistServerURL)
	}
	mistBase = strings.TrimRight(mistBase, "/")
	format := req.GetFormat()
	if format == "" {
		format = "mp4"
	}

	// Use clip_hash for secure file naming (no tenant info exposed)
	clipHash := req.GetClipHash()
	// stream_name = the Mist source Foghorn picked (live internal_name,
	// dvr+<internal>, or vod+<chapter_artifact_hash>). Used ONLY for
	// the /view/ pull URL.
	sourceStreamName := req.GetStreamName()
	// output_stream_name = the owning stream's internal_name. All
	// clips for a given stream share this storage namespace
	// regardless of which historical surface they were cut from.
	// Required — Foghorn always populates this field. A missing
	// value is a Foghorn bug; reject the request rather than fall
	// back to the source-stream namespace (which would scatter
	// chapter clips under chapter-artifact-hashed dirs).
	outputStreamName := req.GetOutputStreamName()
	if outputStreamName == "" {
		logger.WithFields(logging.Fields{
			"clip_hash":     clipHash,
			"source_stream": sourceStreamName,
			"request_id":    req.GetRequestId(),
		}).Error("Clip pull rejected: output_stream_name is required")
		if send != nil {
			send(&pb.ControlMessage{
				SentAt: timestamppb.Now(),
				Payload: &pb.ControlMessage_ClipDone{ClipDone: &pb.ClipDone{
					RequestId: req.GetRequestId(),
					Status:    "failed",
					Error:     "missing output_stream_name (Foghorn bug)",
				}},
			})
		}
		return
	}
	logger.WithFields(logging.Fields{
		"clip_hash":     clipHash,
		"source_stream": sourceStreamName,
		"output_stream": outputStreamName,
		"source_kind":   req.GetSourceKind().String(),
		"source_dvr":    req.GetSourceDvrHash(),
		"source_vod":    req.GetSourceChapterArtifactHash(),
	}).Debug("Clip pull source dispatch")

	// Build MistServer URL using the source stream name.
	q := buildClipParams(req)
	if localMistSource && req.GetSourceKind() == pb.ClipPullRequest_SOURCE_KIND_LIVE && !strings.Contains(sourceStreamName, "+") {
		sourceStreamName = "live+" + sourceStreamName
	}
	clipURL := buildClipURL(mistBase, sourceStreamName, format, q)

	root := cfg.StorageLocalPath
	if root == "" {
		logger.Warn("storage path not configured; dropping clip request")
		return
	}

	// Storage path: clips/{output_stream_name}/{clip_hash}.{format}.
	// Foghorn's relay URL generation and DTSH boot path both resolve
	// the clip's owning stream via foghorn.artifacts.stream_internal_name,
	// so the file MUST land under that name — not the source surface.
	clipDir := filepath.Join(root, "clips", outputStreamName)
	_ = os.MkdirAll(clipDir, 0755)
	dst := filepath.Join(clipDir, fmt.Sprintf("%s.%s", clipHash, format))

	requestID := req.GetRequestId()

	// progress 0%
	if send != nil {
		send(&pb.ControlMessage{SentAt: timestamppb.Now(), Payload: &pb.ControlMessage_ClipProgress{ClipProgress: &pb.ClipProgress{RequestId: requestID, Percent: 0, Message: "starting"}}})
	}
	if err := downloadClipFile(clipURL, dst); err != nil {
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
		"file":          dst,
		"clip_hash":     clipHash,
		"source_stream": sourceStreamName,
		"output_stream": outputStreamName,
		"request_id":    requestID,
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

	// Proactively generate .dtsh for the clip so the first relay viewer
	// doesn't pay header-discovery latency over HTTP::URIReader and the
	// freeze pipeline uploads the sidecar alongside the media file. The
	// handlers package owns the actual GenerateDTSH HTTP poll, registered
	// via SetClipDTSHGenerator at startup to avoid the
	// handlers→control→handlers import cycle. Boots vod+<internal_name>
	// — Foghorn's STREAM_SOURCE for vod+ resolves via internal_name, not
	// clip_hash, so using the hash here would silently fail to resolve.
	internalName := req.GetInternalName()
	if gen := getClipDTSHGenerator(); gen != nil && internalName != "" {
		clipStreamName := "vod+" + internalName
		go gen(clipStreamName, clipHash)
	}
}

// ClipDTSHGenerator is registered by handlers at startup. Invoked
// asynchronously after a clip download completes; the implementation
// boots the stream in Mist via /json_<streamName>.js which triggers the
// input module to write a .dtsh sidecar.
type ClipDTSHGenerator func(streamName, clipHash string)

var (
	clipDTSHGen   ClipDTSHGenerator
	clipDTSHGenMu sync.RWMutex
)

// SetClipDTSHGenerator wires a clip-completion DTSH generator. Called by
// handlers package init.
func SetClipDTSHGenerator(g ClipDTSHGenerator) {
	clipDTSHGenMu.Lock()
	clipDTSHGen = g
	clipDTSHGenMu.Unlock()
}

func getClipDTSHGenerator() ClipDTSHGenerator {
	clipDTSHGenMu.RLock()
	defer clipDTSHGenMu.RUnlock()
	return clipDTSHGen
}

func deriveMistHTTPBase(base string) string {
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

func buildClipURL(base, streamName, format, query string) string {
	u, err := url.Parse(strings.TrimRight(strings.TrimSpace(base), "/"))
	if err != nil || u.Host == "" {
		return fmt.Sprintf("%s/%s.%s?%s", strings.TrimRight(base, "/"), streamName, format, query)
	}
	path := strings.TrimRight(u.Path, "/")
	if path == "" {
		path = "/" + streamName + "." + format
	} else {
		path = path + "/" + streamName + "." + format
	}
	u.Path = path
	u.RawQuery = query
	return u.String()
}

func buildClipParams(req *pb.ClipPullRequest) string {
	return buildClipParamsAt(req, time.Now().Unix())
}

func buildClipParamsAt(req *pb.ClipPullRequest, nowUnix int64) string {
	var parts []string
	if req.GetSourceKind() == pb.ClipPullRequest_SOURCE_KIND_LIVE && req.StartUnix != nil && req.StopUnix != nil {
		duration := req.GetStopUnix() - req.GetStartUnix()
		if duration < 1 {
			duration = 1
		}
		parts = append(parts, "startunix="+strconv.FormatInt(req.GetStartUnix()-nowUnix, 10))
		parts = append(parts, "duration="+strconv.FormatInt(duration, 10))
	} else if req.StartUnix != nil {
		parts = append(parts, "startunix="+strconv.FormatInt(req.GetStartUnix(), 10))
		if req.StopUnix != nil {
			parts = append(parts, "stopunix="+strconv.FormatInt(req.GetStopUnix(), 10))
		}
	}
	parts = append(parts, "dl="+urlEscape(fmt.Sprintf("%s.%s", req.GetOutputName(), req.GetFormat())))
	return strings.Join(parts, "&")
}

var hasSpaceFor = storage.HasSpaceFor
var downloadClipFile = downloadToFile

func downloadToFile(url, dst string) error {
	ctx := context.Background()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
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
	if err = hasSpaceFor(parentDir, requiredBytes); err != nil {
		return err
	}

	tmpPath := dst + ".downloading"
	_ = os.Remove(tmpPath)
	f, err := os.Create(tmpPath)
	if err != nil {
		return err
	}
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
		"dvr_hash":        dvrHash,
		"stream_id":       streamID,
		"internal_name":   internalName,
		"source_url":      sourceURL,
		"request_id":      requestID,
		"format":          config.GetFormat(),
		"window_seconds":  config.GetDvrWindowSeconds(),
		"max_entries":     config.GetMaxEntries(),
		"retention_until": config.GetRetentionUntil(),
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
	if errors.Is(err, leases.ErrLeaseHeld) {
		// Delete was queued because a lease (or boot pause) blocks immediate
		// removal. The deferred-delete drain will send ArtifactDeleted when
		// bytes are actually gone (see leases_init.go onDeleted callback).
		logger.WithField("clip_hash", clipHash).Info("Clip delete queued; awaiting lease release")
		return
	}
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
			ArtifactHash: clipHash,
			ArtifactType: "clip",
			Reason:       "manual",
			NodeId:       getNodeID(),
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
	if errors.Is(err, leases.ErrLeaseHeld) {
		logger.WithField("dvr_hash", dvrHash).Info("DVR delete queued; awaiting lease release")
		return
	}
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
	if errors.Is(err, leases.ErrLeaseHeld) {
		logger.WithField("vod_hash", vodHash).Info("VOD delete queued; awaiting lease release")
		return
	}
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
			ArtifactHash: vodHash,
			ArtifactType: "vod",
			Reason:       "manual",
			NodeId:       getNodeID(),
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
	stream := getStream()
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

// FreezePermissionHandler is called when Foghorn responds to a freeze permission request
type FreezePermissionHandler func(*pb.FreezePermissionResponse)

// DefrostRequestHandler is called when Foghorn sends a defrost request
type DefrostRequestHandler func(*pb.DefrostRequest)

// FreezeRequestHandler is called when Foghorn proactively requests a freeze/sync
type FreezeRequestHandler func(*pb.FreezeRequest)

// DtshSyncRequestHandler is called when Foghorn sends a request to sync just the .dtsh file
type DtshSyncRequestHandler func(*pb.DtshSyncRequest)

// ProcessingJobHandler is called when Foghorn sends a VOD processing job request
type ProcessingJobHandler func(*pb.ProcessingJobRequest, func(*pb.ControlMessage))

var (
	freezePermissionHandlers = make(map[string]chan *pb.FreezePermissionResponse)
	freezePermissionMutex    = make(chan struct{}, 1)
	defrostRequestHandler    DefrostRequestHandler
	freezeRequestHandler     FreezeRequestHandler
	dtshSyncRequestHandler   DtshSyncRequestHandler
	processingJobHandler     ProcessingJobHandler

	// CanDelete request/response tracking
	canDeleteHandlers = make(map[string]chan *pb.CanDeleteResponse)
	canDeleteMutex    = make(chan struct{}, 1)

	// RelayResolve request/response tracking. Keyed by request_id (the relay
	// generates a UUID per outstanding resolve) because the same asset can be
	// resolved concurrently for different sessions.
	relayResolveHandlers = make(map[string]chan *pb.RelayResolveResponse)
	relayResolveMutex    = make(chan struct{}, 1)
)

// SetDefrostRequestHandler sets the callback for defrost requests from Foghorn
func SetDefrostRequestHandler(handler DefrostRequestHandler) {
	defrostRequestHandler = handler
}

// SetFreezeRequestHandler sets the callback for proactive freeze requests from Foghorn
func SetFreezeRequestHandler(handler FreezeRequestHandler) {
	freezeRequestHandler = handler
}

// SetDtshSyncRequestHandler sets the callback for incremental .dtsh sync requests from Foghorn
func SetDtshSyncRequestHandler(handler DtshSyncRequestHandler) {
	dtshSyncRequestHandler = handler
}

// SetProcessingJobHandler sets the callback for processing job requests from Foghorn
func SetProcessingJobHandler(handler ProcessingJobHandler) {
	processingJobHandler = handler
}

// RequestFreezePermission asks Foghorn for permission and presigned URL to freeze an asset.
// This is a blocking call that waits for Foghorn's response.
func RequestFreezePermission(ctx context.Context, assetType, assetHash, localPath string, sizeBytes uint64, filenames []string) (*pb.FreezePermissionResponse, error) {
	stream := getStream()
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
		NodeId:    getNodeID(),
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

// RetryDVRSegmentUploadHandler is invoked when Foghorn asks the recording
// node to re-attempt upload of specific segments during finalization.
type RetryDVRSegmentUploadHandler func(*pb.RetryDVRSegmentUpload)

// ReclaimDVRSegmentHandler is invoked when Foghorn issues a reclaim
// order for local DVR segment files after every overlapping chapter
// has reached state='frozen'. The handler MUST be idempotent
// (missing-file is success).
type ReclaimDVRSegmentHandler func(*pb.ReclaimDVRSegment)

var (
	recordDVRSegmentHandlers  = make(map[string]chan *pb.RecordDVRSegmentResponse)
	recordDVRSegmentMutex     = make(chan struct{}, 1)
	evictableSegmentsHandlers = make(map[string]chan *pb.EvictableSegmentsResponse)
	evictableSegmentsMutex    = make(chan struct{}, 1)
	retryDVRSegmentHandler    RetryDVRSegmentUploadHandler
	reclaimDVRSegmentHandler  ReclaimDVRSegmentHandler
)

// SetRetryDVRSegmentHandler registers the callback for Foghorn-driven
// finalization retries. The handler runs on its own goroutine; if the local
// segment file still exists it should re-upload via the s3_key returned from
// the original RecordDVRSegment + emit MarkDVRSegmentUploaded on success, or
// emit DVRSegmentDropped(was_uploaded=false) when the local copy is gone.
func SetRetryDVRSegmentHandler(h RetryDVRSegmentUploadHandler) {
	retryDVRSegmentHandler = h
}

// SetReclaimDVRSegmentHandler registers the callback for Foghorn-driven
// reclaim orders. Invoked once every overlapping chapter has reached
// state='frozen'; the local segment file is safe to delete.
func SetReclaimDVRSegmentHandler(h ReclaimDVRSegmentHandler) {
	reclaimDVRSegmentHandler = h
}

// RecordDVRSegment asks Foghorn to insert a 'pending' ledger row for a new
// segment and mints a presigned PUT URL for the upload. Blocking; returns
// the response or an error / timeout. On Accepted=false the caller must
// not upload — the artifact is in a terminal state or the segment was
// rejected for another reason.
func RecordDVRSegment(
	ctx context.Context,
	dvrHash, segmentName, localPath string,
	mediaStartMs, mediaEndMs, durationMs int64,
) (*pb.RecordDVRSegmentResponse, error) {
	return recordDVRSegment(ctx, dvrHash, segmentName, localPath, mediaStartMs, mediaEndMs, durationMs, false)
}

// RecordRecoveredDVRSegment is used only by startup reconciliation after
// reading a local DVR manifest with PDT timing. It lets Foghorn rebuild
// missing ledger rows for finalized artifacts without weakening live
// RECORDING_SEGMENT terminal rejection.
func RecordRecoveredDVRSegment(
	ctx context.Context,
	dvrHash, segmentName, localPath string,
	mediaStartMs, mediaEndMs, durationMs int64,
) (*pb.RecordDVRSegmentResponse, error) {
	return recordDVRSegment(ctx, dvrHash, segmentName, localPath, mediaStartMs, mediaEndMs, durationMs, true)
}

func recordDVRSegment(
	ctx context.Context,
	dvrHash, segmentName, localPath string,
	mediaStartMs, mediaEndMs, durationMs int64,
	recoveryInsert bool,
) (*pb.RecordDVRSegmentResponse, error) {
	stream := getStream()
	if stream == nil {
		return nil, fmt.Errorf("gRPC control stream not connected")
	}
	requestID := uuid.New().String()
	ch := make(chan *pb.RecordDVRSegmentResponse, 1)

	recordDVRSegmentMutex <- struct{}{}
	recordDVRSegmentHandlers[requestID] = ch
	<-recordDVRSegmentMutex

	cleanup := func() {
		recordDVRSegmentMutex <- struct{}{}
		delete(recordDVRSegmentHandlers, requestID)
		<-recordDVRSegmentMutex
	}

	req := &pb.RecordDVRSegmentRequest{
		RequestId:      requestID,
		DvrHash:        dvrHash,
		SegmentName:    segmentName,
		MediaStartMs:   mediaStartMs,
		MediaEndMs:     mediaEndMs,
		DurationMs:     durationMs,
		LocalPath:      localPath,
		NodeId:         getNodeID(),
		RecoveryInsert: recoveryInsert,
	}
	msg := &pb.ControlMessage{SentAt: timestamppb.Now(), Payload: &pb.ControlMessage_RecordDvrSegmentRequest{RecordDvrSegmentRequest: req}}
	if err := stream.Send(msg); err != nil {
		cleanup()
		return nil, fmt.Errorf("failed to send record_dvr_segment_request: %w", err)
	}
	select {
	case resp := <-ch:
		cleanup()
		return resp, nil
	case <-ctx.Done():
		cleanup()
		return nil, ctx.Err()
	case <-time.After(30 * time.Second):
		cleanup()
		return nil, fmt.Errorf("timeout waiting for record_dvr_segment response")
	}
}

func handleRecordDVRSegmentResponse(resp *pb.RecordDVRSegmentResponse) {
	recordDVRSegmentMutex <- struct{}{}
	ch, exists := recordDVRSegmentHandlers[resp.GetRequestId()]
	<-recordDVRSegmentMutex
	if exists {
		ch <- resp
	}
}

// SendMarkDVRSegmentUploaded reports that an S3 upload completed for a
// segment. Fire-and-forget; Foghorn updates the ledger row asynchronously.
func SendMarkDVRSegmentUploaded(dvrHash, segmentName string, sizeBytes uint64) error {
	stream := getStream()
	if stream == nil {
		return fmt.Errorf("gRPC control stream not connected")
	}
	msg := &pb.ControlMessage{
		SentAt: timestamppb.Now(),
		Payload: &pb.ControlMessage_MarkDvrSegmentUploaded{MarkDvrSegmentUploaded: &pb.MarkDVRSegmentUploaded{
			RequestId:   uuid.New().String(),
			DvrHash:     dvrHash,
			SegmentName: segmentName,
			SizeBytes:   sizeBytes,
		}},
	}
	return stream.Send(msg)
}

// SendDVRSegmentDropped reports a forced eviction. wasUploaded distinguishes
// safe local cleanup (Foghorn marks deleted_local; chapter finalization
// recovers from S3) from data loss before upload (Foghorn marks
// lost_local; overlapping chapters move to failed_source_missing).
func SendDVRSegmentDropped(
	dvrHash, segmentName, reason, localPath string,
	mediaStartMs, mediaEndMs, durationMs int64,
	sizeBytes uint64,
	wasUploaded bool,
) error {
	stream := getStream()
	if stream == nil {
		return fmt.Errorf("gRPC control stream not connected")
	}
	msg := &pb.ControlMessage{
		SentAt: timestamppb.Now(),
		Payload: &pb.ControlMessage_DvrSegmentDropped{DvrSegmentDropped: &pb.DVRSegmentDropped{
			RequestId:    uuid.New().String(),
			DvrHash:      dvrHash,
			SegmentName:  segmentName,
			Reason:       reason,
			DurationMs:   durationMs,
			MediaStartMs: mediaStartMs,
			MediaEndMs:   mediaEndMs,
			SizeBytes:    sizeBytes,
			DroppedAt:    time.Now().Unix(),
			WasUploaded:  wasUploaded,
			LocalPath:    localPath,
		}},
	}
	return stream.Send(msg)
}

// RequestEvictableSegments asks Foghorn for the authoritative list of
// segments safe to delete locally for a DVR. Blocking; returns the
// response or an error / timeout.
func RequestEvictableSegments(ctx context.Context, dvrHash string, maxCount int32) (*pb.EvictableSegmentsResponse, error) {
	stream := getStream()
	if stream == nil {
		return nil, fmt.Errorf("gRPC control stream not connected")
	}
	requestID := uuid.New().String()
	ch := make(chan *pb.EvictableSegmentsResponse, 1)

	evictableSegmentsMutex <- struct{}{}
	evictableSegmentsHandlers[requestID] = ch
	<-evictableSegmentsMutex

	cleanup := func() {
		evictableSegmentsMutex <- struct{}{}
		delete(evictableSegmentsHandlers, requestID)
		<-evictableSegmentsMutex
	}

	req := &pb.EvictableSegmentsRequest{
		RequestId: requestID,
		DvrHash:   dvrHash,
		MaxCount:  maxCount,
	}
	msg := &pb.ControlMessage{SentAt: timestamppb.Now(), Payload: &pb.ControlMessage_EvictableSegmentsRequest{EvictableSegmentsRequest: req}}
	if err := stream.Send(msg); err != nil {
		cleanup()
		return nil, fmt.Errorf("failed to send evictable_segments_request: %w", err)
	}
	select {
	case resp := <-ch:
		cleanup()
		return resp, nil
	case <-ctx.Done():
		cleanup()
		return nil, ctx.Err()
	case <-time.After(15 * time.Second):
		cleanup()
		return nil, fmt.Errorf("timeout waiting for evictable_segments response")
	}
}

func handleEvictableSegmentsResponse(resp *pb.EvictableSegmentsResponse) {
	evictableSegmentsMutex <- struct{}{}
	ch, exists := evictableSegmentsHandlers[resp.GetRequestId()]
	<-evictableSegmentsMutex
	if exists {
		ch <- resp
	}
}

var (
	restoreLocalSegmentIndexHandlers = make(map[string]chan *pb.RestoreLocalSegmentIndexResponse)
	restoreLocalSegmentIndexMutex    = make(chan struct{}, 1)
)

// SendRestoreLocalSegmentIndex sends a bounded reconciliation batch to
// Foghorn after a sidecar restart. Caller batches discovered local
// (artifact_hash, segment_name) pairs into pages of ~500. Foghorn answers
// with current ledger state; the response populates the local cache index.
//
// This is the only sanctioned restart-reconciliation RPC — there is no
// "give me all segments for this DVR" call, in keeping with the
// bounded-operations invariant for unbounded artifact lifetime.
func SendRestoreLocalSegmentIndex(ctx context.Context, dvrHash string, segmentNames []string) (*pb.RestoreLocalSegmentIndexResponse, error) {
	stream := getStream()
	if stream == nil {
		return nil, fmt.Errorf("gRPC control stream not connected")
	}
	if len(segmentNames) == 0 {
		return &pb.RestoreLocalSegmentIndexResponse{DvrHash: dvrHash}, nil
	}
	requestID := uuid.New().String()
	ch := make(chan *pb.RestoreLocalSegmentIndexResponse, 1)

	restoreLocalSegmentIndexMutex <- struct{}{}
	restoreLocalSegmentIndexHandlers[requestID] = ch
	<-restoreLocalSegmentIndexMutex

	cleanup := func() {
		restoreLocalSegmentIndexMutex <- struct{}{}
		delete(restoreLocalSegmentIndexHandlers, requestID)
		<-restoreLocalSegmentIndexMutex
	}

	req := &pb.RestoreLocalSegmentIndexRequest{
		RequestId:    requestID,
		DvrHash:      dvrHash,
		SegmentNames: segmentNames,
		NodeId:       getNodeID(),
	}
	msg := &pb.ControlMessage{SentAt: timestamppb.Now(), Payload: &pb.ControlMessage_RestoreLocalSegmentIndexRequest{RestoreLocalSegmentIndexRequest: req}}
	if err := stream.Send(msg); err != nil {
		cleanup()
		return nil, fmt.Errorf("failed to send restore_local_segment_index_request: %w", err)
	}
	select {
	case resp := <-ch:
		cleanup()
		return resp, nil
	case <-ctx.Done():
		cleanup()
		return nil, ctx.Err()
	case <-time.After(30 * time.Second):
		cleanup()
		return nil, fmt.Errorf("timeout waiting for restore_local_segment_index response")
	}
}

func handleRestoreLocalSegmentIndexResponse(resp *pb.RestoreLocalSegmentIndexResponse) {
	restoreLocalSegmentIndexMutex <- struct{}{}
	ch, exists := restoreLocalSegmentIndexHandlers[resp.GetRequestId()]
	<-restoreLocalSegmentIndexMutex
	if exists {
		ch <- resp
	}
}

// SendFreezeProgress sends upload progress to Foghorn
func SendFreezeProgress(requestID, assetHash string, percent uint32, bytesUploaded uint64) error {
	stream := getStream()
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

// SendFreezeComplete sends freeze completion status to Foghorn.
// Set localMissing=true when the local source file is gone (ENOENT) so Foghorn
// can transition the row to sync_status='lost_local' and stop retrying.
func SendFreezeComplete(requestID, assetHash, status, s3URL string, sizeBytes uint64, errMsg string, localMissing bool) error {
	complete := &pb.FreezeComplete{
		RequestId:    requestID,
		AssetHash:    assetHash,
		Status:       status,
		S3Url:        s3URL,
		SizeBytes:    sizeBytes,
		Error:        errMsg,
		LocalMissing: localMissing,
	}

	msg := &pb.ControlMessage{SentAt: timestamppb.Now(), Payload: &pb.ControlMessage_FreezeComplete{FreezeComplete: complete}}
	return sendOrEnqueue(msg)
}

// SendDefrostProgress sends download progress to Foghorn
func SendDefrostProgress(requestID, assetHash string, percent uint32, bytesDownloaded uint64, segmentsDownloaded, totalSegments int32, message string) error {
	stream := getStream()
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

// SendDefrostComplete sends defrost completion status to Foghorn with no
// typed reason (REASON_UNSPECIFIED). Success and ready statuses use this.
func SendDefrostComplete(requestID, assetHash, status, localPath string, sizeBytes uint64, errMsg string) error {
	return SendDefrostCompleteWithReason(requestID, assetHash, status, localPath, sizeBytes, errMsg, pb.DefrostComplete_REASON_UNSPECIFIED)
}

// SendDefrostCompleteWithReason is the failure-path variant: lets the caller
// classify the failure (out-of-space, S3 error, local IO, presigned invalid)
// so Foghorn can route the retry logic.
func SendDefrostCompleteWithReason(requestID, assetHash, status, localPath string, sizeBytes uint64, errMsg string, reason pb.DefrostComplete_Reason) error {
	complete := &pb.DefrostComplete{
		RequestId: requestID,
		AssetHash: assetHash,
		Status:    status,
		LocalPath: localPath,
		SizeBytes: sizeBytes,
		Error:     errMsg,
		NodeId:    getNodeID(),
		Reason:    reason,
	}

	msg := &pb.ControlMessage{SentAt: timestamppb.Now(), Payload: &pb.ControlMessage_DefrostComplete{DefrostComplete: complete}}
	return sendOrEnqueue(msg)
}

// SendStorageLifecycle sends a storage lifecycle event to Foghorn (for analytics).
// Queued for retry on disconnect since these feed ClickHouse storage_events.
func SendStorageLifecycle(data *pb.StorageLifecycleData) error {
	trigger := &pb.MistTrigger{
		TriggerType: "storage_lifecycle",
		RequestId:   uuid.New().String(),
		NodeId:      getNodeID(),
		Blocking:    false,
		TriggerPayload: &pb.MistTrigger_StorageLifecycleData{
			StorageLifecycleData: data,
		},
	}

	msg := &pb.ControlMessage{SentAt: timestamppb.Now(), Payload: &pb.ControlMessage_MistTrigger{MistTrigger: trigger}}
	return sendOrEnqueue(msg)
}

// SendProcessBillingEvent sends a process billing event to Foghorn (for analytics/billing)
// ProcessBillingEvent tracks transcoding usage for Livepeer and native processes
func SendProcessBillingEvent(event *pb.ProcessBillingEvent) error {
	processType := event.ProcessType
	stream := getStream()
	if stream == nil {
		BillingEventsSent.WithLabelValues(processType, "stream_disconnected").Inc()
		return fmt.Errorf("gRPC control stream not connected")
	}

	// Ensure node_id is set
	if event.NodeId == "" {
		event.NodeId = getNodeID()
	}

	trigger := &pb.MistTrigger{
		TriggerType: "process_billing",
		RequestId:   uuid.New().String(),
		NodeId:      getNodeID(),
		Blocking:    false,
		TriggerPayload: &pb.MistTrigger_ProcessBilling{
			ProcessBilling: event,
		},
	}

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
	return getStream() != nil
}

// relayBaseURL returns the URL Mist on this node uses to reach Helmsman's
// /internal/artifact/* read-through relay. Reads HELMSMAN_RELAY_BASE_URL when
// set (container deployments where Mist resolves to a service name like
// http://helmsman:18007); falls back to http://127.0.0.1:18007 for the
// production colocated-with-Mist case.
func relayBaseURL() string {
	if v := strings.TrimSpace(os.Getenv("HELMSMAN_RELAY_BASE_URL")); v != "" {
		return strings.TrimRight(v, "/")
	}
	return "http://127.0.0.1:18007"
}

// RequestCanDelete asks Foghorn if it's safe to delete a local artifact copy.
// Returns true if the asset is synced to S3 and can be safely deleted locally.
// Also returns warm_duration_ms (how long the asset was cached before eviction).
func RequestCanDelete(ctx context.Context, assetHash string) (bool, string, int64, error) {
	stream := getStream()
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
		NodeId:    getNodeID(),
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

// RequestRelayResolve asks Foghorn for the durable source coordinates of an
// asset Helmsman is about to serve via the /internal/artifact/* relay. The
// response carries presigned media GET, optional .dtsh GET/PUT, expected
// size, and (for DVR) the full chapter segment ref list.
//
// Caller-side semantics:
//   - requestID must be unique per outstanding request (UUID recommended).
//   - The TTL on media_presigned_url is in the response; the relay should
//     cache resolves in memory for url_ttl_seconds * 0.8 and refresh on TTL
//     expiry to handle long playback sessions.
//   - state != PLAYABLE means the relay should not attempt to fetch S3 —
//     handle SOURCE_MISSING (404/500), ACTIVE_DVR (refuse + retry-after),
//     and GAP (HLS gap marker) at the HTTP layer.
func RequestRelayResolve(ctx context.Context, req *pb.RelayResolveRequest) (*pb.RelayResolveResponse, error) {
	stream := getStream()
	if stream == nil {
		return nil, fmt.Errorf("gRPC control stream not connected")
	}
	if req == nil || req.GetRequestId() == "" {
		return nil, fmt.Errorf("relay resolve request must have a request_id")
	}
	if req.NodeId == "" {
		req.NodeId = getNodeID()
	}

	responseChan := make(chan *pb.RelayResolveResponse, 1)

	relayResolveMutex <- struct{}{}
	relayResolveHandlers[req.GetRequestId()] = responseChan
	<-relayResolveMutex

	msg := &pb.ControlMessage{
		RequestId: req.GetRequestId(),
		SentAt:    timestamppb.Now(),
		Payload:   &pb.ControlMessage_RelayResolveRequest{RelayResolveRequest: req},
	}
	if err := stream.Send(msg); err != nil {
		relayResolveMutex <- struct{}{}
		delete(relayResolveHandlers, req.GetRequestId())
		<-relayResolveMutex
		return nil, fmt.Errorf("failed to send relay resolve request: %w", err)
	}

	defer func() {
		relayResolveMutex <- struct{}{}
		delete(relayResolveHandlers, req.GetRequestId())
		<-relayResolveMutex
	}()

	select {
	case resp := <-responseChan:
		return resp, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-time.After(10 * time.Second):
		return nil, fmt.Errorf("timeout waiting for relay resolve response")
	}
}

// handleRelayResolveResponse routes inbound RelayResolveResponse messages to
// the waiting goroutine. Keyed by request_id (NOT asset_hash) because the
// same asset is resolved concurrently per session.
func handleRelayResolveResponse(response *pb.RelayResolveResponse) {
	if response == nil || response.GetRequestId() == "" {
		return
	}
	relayResolveMutex <- struct{}{}
	responseChan, exists := relayResolveHandlers[response.GetRequestId()]
	<-relayResolveMutex
	if exists {
		responseChan <- response
	}
}

// SendSyncComplete notifies Foghorn that a sync operation has completed.
// Called after successfully uploading an artifact to S3 (while keeping the local copy).
// dtshIncluded indicates whether the .dtsh index file was included in the sync.
// localMissing=true signals the local source file is gone (ENOENT) before sync;
// Foghorn marks the row sync_status='lost_local' (terminal) and stops retries.
func SendSyncComplete(requestID, assetHash, status, s3URL string, sizeBytes uint64, errMsg string, dtshIncluded bool, localMissing bool) error {
	complete := &pb.SyncComplete{
		RequestId:    requestID,
		AssetHash:    assetHash,
		Status:       status,
		S3Url:        s3URL,
		SizeBytes:    sizeBytes,
		Error:        errMsg,
		NodeId:       getNodeID(),
		DtshIncluded: dtshIncluded,
		LocalMissing: localMissing,
	}

	msg := &pb.ControlMessage{SentAt: timestamppb.Now(), Payload: &pb.ControlMessage_SyncComplete{SyncComplete: complete}}
	return sendOrEnqueue(msg)
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

// handleInvalidateSessions re-runs USER_NEW for active sessions on the listed
// streams without disconnecting viewers. Used after a playback policy or
// signing-key change so MistServer's per-session decision cache is rebuilt
// against the fresh policy.
//
// Maps to MistServer's `invalidate_sessions` JSON API. Distinct from
// handleStopSessions — stop disconnects, invalidate re-evaluates.
func handleInvalidateSessions(logger logging.Logger, req *pb.InvalidateSessionsRequest) {
	if req == nil || len(req.StreamNames) == 0 {
		return
	}

	cfg := currentConfig
	if cfg == nil {
		logger.Warn("config not initialized; cannot invalidate sessions")
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
	}).Info("Invalidating sessions to re-run USER_NEW")

	if err := mistClient.InvalidateSessionsMultiple(req.StreamNames); err != nil {
		logger.WithFields(logging.Fields{
			"tenant_id":    req.TenantId,
			"reason":       req.Reason,
			"stream_names": req.StreamNames,
			"error":        err,
		}).Error("Failed to invalidate sessions via MistServer API")
		return
	}

	logger.WithFields(logging.Fields{
		"tenant_id":    req.TenantId,
		"reason":       req.Reason,
		"stream_names": req.StreamNames,
	}).Info("Successfully invalidated sessions; viewers will renegotiate against fresh policy")
}

// activePushes tracks MistServer push IDs for multistream targets.
// Key: stream_name, Value: map of target_id -> mist push ID.
var (
	activePushesMu sync.Mutex
	activePushes   = map[string]map[string]int{}
)

func handleActivatePushTargets(logger logging.Logger, req *pb.ActivatePushTargets) {
	if req == nil || len(req.Targets) == 0 {
		return
	}

	cfg := currentConfig
	if cfg == nil {
		logger.Warn("config not initialized; cannot activate push targets")
		return
	}

	mistClient := mist.NewClient(logger)
	if cfg.MistServerURL != "" {
		mistClient.BaseURL = cfg.MistServerURL
	}

	logger.WithFields(logging.Fields{
		"stream_name":  req.StreamName,
		"target_count": len(req.Targets),
	}).Info("Activating multistream push targets")

	activePushesMu.Lock()
	if activePushes[req.StreamName] == nil {
		activePushes[req.StreamName] = map[string]int{}
	}
	activePushesMu.Unlock()

	for _, target := range req.Targets {
		err := mistClient.PushStart(req.StreamName, target.TargetUri)
		if err != nil {
			logger.WithFields(logging.Fields{
				"stream_name": req.StreamName,
				"target_id":   target.TargetId,
				"target_name": target.Name,
				"error":       err,
			}).Error("Failed to start push to target")
			continue
		}

		logger.WithFields(logging.Fields{
			"stream_name": req.StreamName,
			"target_id":   target.TargetId,
			"target_name": target.Name,
		}).Info("Started push to multistream target")
	}
}

func handleDeactivatePushTargets(logger logging.Logger, req *pb.DeactivatePushTargets) {
	if req == nil || req.StreamName == "" {
		return
	}

	cfg := currentConfig
	if cfg == nil {
		return
	}

	mistClient := mist.NewClient(logger)
	if cfg.MistServerURL != "" {
		mistClient.BaseURL = cfg.MistServerURL
	}

	// List active pushes and stop any matching this stream
	pushes, err := mistClient.PushList()
	if err != nil {
		logger.WithFields(logging.Fields{
			"stream_name": req.StreamName,
			"error":       err,
		}).Warn("Failed to list pushes for deactivation")
		return
	}

	stopped := 0
	for _, push := range pushes {
		if push.StreamName == req.StreamName {
			if stopErr := mistClient.PushStop(push.ID); stopErr != nil {
				logger.WithFields(logging.Fields{
					"stream_name": req.StreamName,
					"push_id":     push.ID,
					"error":       stopErr,
				}).Warn("Failed to stop push")
			} else {
				stopped++
			}
		}
	}

	activePushesMu.Lock()
	delete(activePushes, req.StreamName)
	activePushesMu.Unlock()

	if stopped > 0 {
		logger.WithFields(logging.Fields{
			"stream_name":   req.StreamName,
			"stopped_count": stopped,
		}).Info("Deactivated multistream push targets")
	}
}

// parseRequestedMode converts a string mode to protobuf enum for Register message.
// Edge API token validation — Helmsman asks Foghorn, caches results with TTL.

type edgeTokenResult struct {
	resp      *pb.ValidateEdgeTokenResponse
	expiresAt time.Time
}

var (
	pendingEdgeTokenValidations = make(map[string]chan *pb.ValidateEdgeTokenResponse)
	pendingEdgeTokenMutex       = make(chan struct{}, 1)
	edgeTokenCache              sync.Map // token string -> *edgeTokenResult
	edgeTokenCacheTTL           = 5 * time.Minute
)

func handleValidateEdgeTokenResponse(requestID string, resp *pb.ValidateEdgeTokenResponse) {
	pendingEdgeTokenMutex <- struct{}{}
	ch, exists := pendingEdgeTokenValidations[requestID]
	<-pendingEdgeTokenMutex

	if exists {
		ch <- resp
	}
}

// ValidateEdgeToken sends a token to Foghorn for validation and returns the result.
// Results are cached with a TTL to avoid round-tripping on every request.
func ValidateEdgeToken(ctx context.Context, token string) (*pb.ValidateEdgeTokenResponse, error) {
	// Check cache first
	if cached, ok := edgeTokenCache.Load(token); ok {
		entry, _ := cached.(*edgeTokenResult)
		if entry != nil && time.Now().Before(entry.expiresAt) {
			return entry.resp, nil
		}
		edgeTokenCache.Delete(token)
	}

	stream := getStream()
	if stream == nil {
		return nil, fmt.Errorf("gRPC control stream not connected")
	}

	requestID := uuid.New().String()
	responseCh := make(chan *pb.ValidateEdgeTokenResponse, 1)

	pendingEdgeTokenMutex <- struct{}{}
	pendingEdgeTokenValidations[requestID] = responseCh
	<-pendingEdgeTokenMutex

	msg := &pb.ControlMessage{
		RequestId: requestID,
		SentAt:    timestamppb.Now(),
		Payload: &pb.ControlMessage_ValidateEdgeTokenRequest{
			ValidateEdgeTokenRequest: &pb.ValidateEdgeTokenRequest{Token: token},
		},
	}
	if err := stream.Send(msg); err != nil {
		pendingEdgeTokenMutex <- struct{}{}
		delete(pendingEdgeTokenValidations, requestID)
		<-pendingEdgeTokenMutex
		return nil, fmt.Errorf("failed to send token validation request: %w", err)
	}

	select {
	case resp := <-responseCh:
		pendingEdgeTokenMutex <- struct{}{}
		delete(pendingEdgeTokenValidations, requestID)
		<-pendingEdgeTokenMutex

		edgeTokenCache.Store(token, &edgeTokenResult{
			resp:      resp,
			expiresAt: time.Now().Add(edgeTokenCacheTTL),
		})
		return resp, nil
	case <-ctx.Done():
		pendingEdgeTokenMutex <- struct{}{}
		delete(pendingEdgeTokenValidations, requestID)
		<-pendingEdgeTokenMutex
		return nil, ctx.Err()
	case <-time.After(5 * time.Second):
		pendingEdgeTokenMutex <- struct{}{}
		delete(pendingEdgeTokenValidations, requestID)
		<-pendingEdgeTokenMutex
		return nil, fmt.Errorf("timeout waiting for token validation response")
	}
}

// Mist-admin session validation — same control-stream pattern as the
// edge-token path. Mirrored separately so the JWT-shaped session token
// and the opaque API token never share a cache slot.

type mistAdminSessionResult struct {
	resp      *pb.EdgeMistAdminSessionResponse
	expiresAt time.Time
}

var (
	pendingMistAdminSessions = make(map[string]chan *pb.EdgeMistAdminSessionResponse)
	pendingMistAdminMutex    = make(chan struct{}, 1)
	mistAdminSessionCache    sync.Map // token string -> *mistAdminSessionResult
	mistAdminSessionCacheTTL = 1 * time.Minute
)

func handleEdgeMistAdminSessionResponse(requestID string, resp *pb.EdgeMistAdminSessionResponse) {
	pendingMistAdminMutex <- struct{}{}
	ch, exists := pendingMistAdminSessions[requestID]
	<-pendingMistAdminMutex
	if exists {
		ch <- resp
	}
}

// ValidateMistAdminSession asks Foghorn to validate a session token; the
// connected nodeID is injected at the Foghorn relay so this client side
// passes only the token. Result cached briefly (well below the JWT exp)
// so a flurry of LSP asset requests does not round-trip per file.
func ValidateMistAdminSession(ctx context.Context, token string) (*pb.EdgeMistAdminSessionResponse, error) {
	if cached, ok := mistAdminSessionCache.Load(token); ok {
		entry, ok := cached.(*mistAdminSessionResult)
		if ok && entry != nil && time.Now().Before(entry.expiresAt) {
			return entry.resp, nil
		}
		mistAdminSessionCache.Delete(token)
	}

	stream := getStream()
	if stream == nil {
		return nil, fmt.Errorf("gRPC control stream not connected")
	}

	requestID := uuid.New().String()
	responseCh := make(chan *pb.EdgeMistAdminSessionResponse, 1)

	pendingMistAdminMutex <- struct{}{}
	pendingMistAdminSessions[requestID] = responseCh
	<-pendingMistAdminMutex

	msg := &pb.ControlMessage{
		RequestId: requestID,
		SentAt:    timestamppb.Now(),
		Payload: &pb.ControlMessage_EdgeMistAdminSessionRequest{
			EdgeMistAdminSessionRequest: &pb.EdgeMistAdminSessionRequest{Token: token},
		},
	}
	if err := stream.Send(msg); err != nil {
		pendingMistAdminMutex <- struct{}{}
		delete(pendingMistAdminSessions, requestID)
		<-pendingMistAdminMutex
		return nil, fmt.Errorf("send mist admin session validation: %w", err)
	}

	select {
	case resp := <-responseCh:
		pendingMistAdminMutex <- struct{}{}
		delete(pendingMistAdminSessions, requestID)
		<-pendingMistAdminMutex
		if resp.GetValid() {
			now := time.Now()
			cacheUntil := now.Add(mistAdminSessionCacheTTL)
			if exp := resp.GetExpiresAt(); exp > 0 {
				tokenExp := time.Unix(exp, 0)
				if tokenExp.Before(cacheUntil) {
					cacheUntil = tokenExp
				}
			}
			if cacheUntil.After(now) {
				mistAdminSessionCache.Store(token, &mistAdminSessionResult{
					resp:      resp,
					expiresAt: cacheUntil,
				})
			}
		}
		return resp, nil
	case <-ctx.Done():
		pendingMistAdminMutex <- struct{}{}
		delete(pendingMistAdminSessions, requestID)
		<-pendingMistAdminMutex
		return nil, ctx.Err()
	case <-time.After(5 * time.Second):
		pendingMistAdminMutex <- struct{}{}
		delete(pendingMistAdminSessions, requestID)
		<-pendingMistAdminMutex
		return nil, fmt.Errorf("timeout waiting for mist admin session response")
	}
}

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

// SendThumbnailUploadRequest sends a thumbnail upload request to Foghorn.
// Foghorn resolves internal_name to a stable S3 key and responds with presigned URLs.
func SendThumbnailUploadRequest(internalName string, filePaths []string) error {
	stream := getStream()
	if stream == nil {
		return fmt.Errorf("gRPC control stream not connected")
	}

	requestID := uuid.New().String()
	req := &pb.ThumbnailUploadRequest{
		InternalName: internalName,
		FilePaths:    filePaths,
	}

	msg := &pb.ControlMessage{
		RequestId: requestID,
		SentAt:    timestamppb.Now(),
		Payload:   &pb.ControlMessage_ThumbnailUploadRequest{ThumbnailUploadRequest: req},
	}
	return stream.Send(msg)
}

// handleThumbnailUploadResponse uploads thumbnail files to S3 using presigned URLs
// from Foghorn, then sends a ThumbnailUploaded confirmation.
func handleThumbnailUploadResponse(logger logging.Logger, resp *pb.ThumbnailUploadResponse, send func(*pb.ControlMessage)) {
	thumbnailKey := resp.GetThumbnailKey()
	uploads := resp.GetUploads()

	logger.WithFields(logging.Fields{
		"thumbnail_key": thumbnailKey,
		"upload_count":  len(uploads),
	}).Info("Received thumbnail presigned URLs from Foghorn")

	presignedClient := storage.NewPresignedClient(logger)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	var uploadedKeys []string
	for _, upload := range uploads {
		localPath := upload.GetLocalPath()
		if localPath == "" {
			logger.WithField("file_name", upload.GetFileName()).Warn("No local_path in thumbnail upload response")
			continue
		}

		if upload.GetFileName() == "sprite.vtt" {
			data, err := os.ReadFile(localPath)
			if err != nil {
				logger.WithFields(logging.Fields{
					"file_name":  upload.GetFileName(),
					"local_path": localPath,
					"error":      err,
				}).Error("Failed to read thumbnail VTT")
				continue
			}
			normalized := normalizeThumbnailVTTReferences(string(data))
			if err := presignedClient.UploadToPresignedURL(ctx, upload.GetPresignedUrl(), strings.NewReader(normalized), int64(len(normalized)), nil); err != nil {
				logger.WithFields(logging.Fields{
					"file_name":  upload.GetFileName(),
					"local_path": localPath,
					"s3_key":     upload.GetS3Key(),
					"error":      err,
				}).Error("Failed to upload thumbnail to S3")
				continue
			}
		} else if err := presignedClient.UploadFileToPresignedURL(ctx, upload.GetPresignedUrl(), localPath, nil); err != nil {
			logger.WithFields(logging.Fields{
				"file_name":  upload.GetFileName(),
				"local_path": localPath,
				"s3_key":     upload.GetS3Key(),
				"error":      err,
			}).Error("Failed to upload thumbnail to S3")
			continue
		}

		uploadedKeys = append(uploadedKeys, upload.GetS3Key())
		logger.WithFields(logging.Fields{
			"file_name": upload.GetFileName(),
			"s3_key":    upload.GetS3Key(),
		}).Info("Thumbnail uploaded to S3")
	}

	if len(uploadedKeys) == 0 {
		logger.Warn("No thumbnails uploaded successfully")
		return
	}

	// Notify Foghorn that upload is complete
	uploaded := &pb.ThumbnailUploaded{
		ThumbnailKey: thumbnailKey,
		S3Keys:       uploadedKeys,
	}
	send(&pb.ControlMessage{
		SentAt:  timestamppb.Now(),
		Payload: &pb.ControlMessage_ThumbnailUploaded{ThumbnailUploaded: uploaded},
	})
}

func normalizeThumbnailVTTReferences(vtt string) string {
	lines := strings.Split(vtt, "\n")
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		xywhIdx := strings.Index(trimmed, "#xywh=")
		if xywhIdx < 0 || !strings.Contains(trimmed[:xywhIdx], ".jpg") {
			continue
		}
		lines[i] = "sprite.jpg" + trimmed[xywhIdx:]
	}
	return strings.Join(lines, "\n")
}
