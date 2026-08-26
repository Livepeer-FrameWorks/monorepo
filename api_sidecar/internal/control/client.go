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
	"os"
	"path/filepath"
	"sort"
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
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"

	"github.com/google/uuid"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

const foghornInternalServerName = "foghorn.internal"

// controlProtocolVersion is the control-protocol version this sidecar declares to Foghorn at registration.
// Foghorn gates protocol-dependent dispatch on this OBSERVED value. Bump only in lockstep with a wire-contract
// change Foghorn must gate on (and raise the corresponding *StagedProtocolMin there).
//   - Version 1 = honors the staged freeze contract: uploads to the attempt-scoped staging key the presigned PUT
//     targets and echoes the server-minted attempt id as SyncComplete.request_id.
//   - Version 2 = ALSO honors the staged THUMBNAIL contract: the presigned thumbnail PUTs target per-attempt
//     staging keys and this sidecar echoes ThumbnailUploadResponse.attempt_id in ThumbnailUploaded
//     (ThumbnailStagedProtocolMin=2 on Foghorn). Serving it below this version gets no thumbnail mint.
//   - Version 3 = durably records admitted ingest generations and rejects stale drain/push-target
//     commands whose exact source generation no longer owns the local runtime.
const controlProtocolVersion int32 = 3

// DeleteClipFunc is the function type for clip deletion
type DeleteClipFunc func(clipHash string) (uint64, error)

// DeleteDVRFunc is the function type for DVR deletion
type DeleteDVRFunc func(dvrHash string) (uint64, error)

// DeleteVodFunc is the function type for VOD deletion
type DeleteVodFunc func(vodHash string) (uint64, error)

type streamConn struct {
	stream ipcpb.HelmsmanControl_ConnectClient
	nodeID string
}

// lockedClientStream serializes Send on the single Helmsman→Foghorn control
// stream. gRPC's ClientStream.SendMsg is NOT safe for concurrent goroutines,
// but the Recv loop dispatches handlers as separate goroutines and many
// Request*/Send* helpers (the high-frequency RequestAuthorizeRelayPull among
// them) send on this same bidi stream. Wrapping the stream once at connect and
// storing that wrapper funnels every Send through this mutex with no call-site
// changes. Recv stays on the embedded stream: gRPC allows concurrent Send+Recv,
// only Send+Send is unsafe.
type lockedClientStream struct {
	ipcpb.HelmsmanControl_ConnectClient
	sendMu sync.Mutex
}

func (s *lockedClientStream) Send(msg *ipcpb.ControlMessage) error {
	s.sendMu.Lock()
	defer s.sendMu.Unlock()
	return s.HelmsmanControl_ConnectClient.Send(msg)
}

var activeConn atomic.Pointer[streamConn]

type ingestGenerationFence struct {
	sync.Mutex
	generation   string
	connectorPID int64
	active       bool
	references   int
	evictPending bool
}

type lockedIngestGenerationFence struct {
	*ingestGenerationFence
	runtimeName string
}

var admittedIngestGenerations = struct {
	sync.Mutex
	byRuntime map[string]*ingestGenerationFence
}{byRuntime: make(map[string]*ingestGenerationFence)}

var (
	ingestGenerationStoreMu sync.RWMutex
	ingestGenerationStore   *storage.IngestGenerationStore
)

// lockIngestFence pins the map entry before waiting for its runtime lock. The global map mutex is
// never held across that wait, so a blocked operation for one runtime cannot convoy other streams.
func lockIngestFence(runtimeName string, create bool) (*lockedIngestGenerationFence, bool) {
	runtimeName = strings.TrimSpace(runtimeName)
	if runtimeName == "" {
		return nil, false
	}
	admittedIngestGenerations.Lock()
	fence := admittedIngestGenerations.byRuntime[runtimeName]
	if fence == nil && create {
		fence = &ingestGenerationFence{}
		admittedIngestGenerations.byRuntime[runtimeName] = fence
	}
	if fence != nil {
		fence.references++
	}
	admittedIngestGenerations.Unlock()
	if fence == nil {
		return nil, false
	}
	fence.Lock()
	return &lockedIngestGenerationFence{ingestGenerationFence: fence, runtimeName: runtimeName}, true
}

func (f *lockedIngestGenerationFence) Unlock() {
	f.ingestGenerationFence.Unlock()
	admittedIngestGenerations.Lock()
	f.references--
	if f.references == 0 && f.evictPending {
		f.Lock()
		if !f.active && admittedIngestGenerations.byRuntime[f.runtimeName] == f.ingestGenerationFence {
			delete(admittedIngestGenerations.byRuntime, f.runtimeName)
		}
		f.evictPending = false
		f.ingestGenerationFence.Unlock()
	}
	admittedIngestGenerations.Unlock()
}

// RecordAdmittedIngestGeneration records the generation returned with an accepted PUSH_REWRITE.
// After stream end the value becomes a bounded tombstone that lets late control commands prove
// they still target the exact local publisher generation they were created for.
func RecordAdmittedIngestGeneration(runtimeName, generation string, connectorPID int64) error {
	runtimeName = strings.TrimSpace(runtimeName)
	generation = strings.TrimSpace(generation)
	if runtimeName == "" || generation == "" || connectorPID <= 0 {
		return errors.New("accepted ingest requires runtime, generation, and positive connector PID")
	}
	fence, _ := lockIngestFence(runtimeName, true)
	defer fence.Unlock()
	ingestGenerationStoreMu.RLock()
	store := ingestGenerationStore
	ingestGenerationStoreMu.RUnlock()
	if store != nil {
		if err := store.Put(runtimeName, generation, connectorPID); err != nil {
			return err
		}
	}
	fence.generation = generation
	fence.connectorPID = connectorPID
	fence.active = true
	return nil
}

// MarkAdmittedIngestGenerationEnded converts the current generation to a bounded tombstone after
// PUSH_INPUT_CLOSE is durably accepted. The in-memory value remains available for reordered
// commands, while the disk store can age/cap ended runtimes without evicting active publishers.
func MarkAdmittedIngestGenerationEnded(runtimeName string, connectorPID int64) error {
	return markAdmittedIngestGenerationEnded(runtimeName, "", connectorPID)
}

// MarkAdmittedIngestGenerationEndedExact tombstones only the exact admitted generation. Runtime
// absence reconciliation uses this after Foghorn acknowledges its event-time-fenced retirement; a
// delayed acknowledgement must not end a replacement generation even if Mist reuses a connector PID.
func MarkAdmittedIngestGenerationEndedExact(runtimeName, generation string, connectorPID int64) error {
	generation = strings.TrimSpace(generation)
	if generation == "" {
		return errors.New("ended ingest requires generation")
	}
	return markAdmittedIngestGenerationEnded(runtimeName, generation, connectorPID)
}

func markAdmittedIngestGenerationEnded(runtimeName, expectedGeneration string, connectorPID int64) error {
	runtimeName = strings.TrimSpace(runtimeName)
	if runtimeName == "" || connectorPID <= 0 {
		return errors.New("ended ingest requires runtime and positive connector PID")
	}
	fence, ok := lockIngestFence(runtimeName, false)
	if !ok {
		return nil
	}
	if fence.generation == "" {
		fence.Unlock()
		return nil
	}
	ingestGenerationStoreMu.RLock()
	store := ingestGenerationStore
	ingestGenerationStoreMu.RUnlock()
	if connectorPID != fence.connectorPID || (expectedGeneration != "" && expectedGeneration != fence.generation) {
		fence.Unlock()
		return nil
	}
	if store != nil {
		if err := store.MarkEnded(runtimeName, fence.generation, connectorPID); err != nil {
			fence.Unlock()
			return err
		}
	}
	fence.active = false
	fence.Unlock()
	if store != nil {
		store.SchedulePrune(func(evicted []string, maintenanceErr error) {
			evictInMemoryGenerationFences(evicted)
			if maintenanceErr != nil && pkgLogger != nil {
				pkgLogger.WithError(maintenanceErr).Warn("Ingest generation tombstone maintenance failed; automatic retry scheduled")
			}
		})
	}
	return nil
}

// AdmittedIngestGeneration is the persisted local identity of a runtime Helmsman approved.
type AdmittedIngestGeneration struct {
	RuntimeName  string
	Generation   string
	ConnectorPID int64
	UpdatedAt    time.Time
}

// ActiveAdmittedIngestGenerations returns a stable snapshot for authoritative Mist-runtime
// reconciliation. The persistent store is the source so the snapshot survives Helmsman restarts.
func ActiveAdmittedIngestGenerations() ([]AdmittedIngestGeneration, error) {
	ingestGenerationStoreMu.RLock()
	store := ingestGenerationStore
	ingestGenerationStoreMu.RUnlock()
	if store == nil {
		return nil, nil
	}
	records, err := store.Load()
	if err != nil {
		return nil, err
	}
	active := make([]AdmittedIngestGeneration, 0, len(records))
	for _, record := range records {
		if !record.Active {
			continue
		}
		active = append(active, AdmittedIngestGeneration{
			RuntimeName:  record.RuntimeName,
			Generation:   record.Generation,
			ConnectorPID: record.ConnectorPID,
			UpdatedAt:    time.UnixMilli(record.UpdatedAt),
		})
	}
	sort.Slice(active, func(i, j int) bool {
		return active[i].RuntimeName < active[j].RuntimeName
	})
	return active, nil
}

func initIngestGenerationStore(logger logging.Logger) {
	store, err := storage.NewIngestGenerationStore(storage.DefaultIngestGenerationStorePath())
	if err != nil {
		logger.WithError(err).Fatal("Failed to open ingest-generation fence store")
		return
	}
	records, err := store.Load()
	if err != nil {
		logger.WithError(err).Fatal("Failed to load ingest-generation fence store")
		return
	}
	rehydrateIngestGenerationFences(records)
	ingestGenerationStoreMu.Lock()
	ingestGenerationStore = store
	ingestGenerationStoreMu.Unlock()
	logger.WithField("generation_fences", len(records)).Info("Rehydrated persisted ingest-generation fences")
}

func rehydrateIngestGenerationFences(records map[string]storage.IngestGenerationRecord) {
	for runtimeName, record := range records {
		fence, _ := lockIngestFence(runtimeName, true)
		fence.generation = record.Generation
		fence.connectorPID = record.ConnectorPID
		fence.active = record.Active
		fence.Unlock()
	}
}

func evictInMemoryGenerationFences(runtimeNames []string) {
	for _, runtimeName := range runtimeNames {
		admittedIngestGenerations.Lock()
		fence := admittedIngestGenerations.byRuntime[runtimeName]
		if fence != nil {
			if fence.references > 0 {
				fence.evictPending = true
			} else {
				fence.Lock()
				if !fence.active {
					delete(admittedIngestGenerations.byRuntime, runtimeName)
				}
				fence.Unlock()
			}
		}
		admittedIngestGenerations.Unlock()
	}
}

func admittedIngestGeneration(runtimeName string) (string, bool) {
	fence, ok := lockIngestFence(runtimeName, false)
	if !ok {
		return "", false
	}
	generation := fence.generation
	fence.Unlock()
	return generation, generation != ""
}

func getStream() ipcpb.HelmsmanControl_ConnectClient {
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

func storeConn(stream ipcpb.HelmsmanControl_ConnectClient, nodeID string) {
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
	outbox    []*ipcpb.ControlMessage
	maxOutbox = 100
)

const (
	blockingTriggerTimeout            = 4 * time.Second
	lifecycleReconciliationTimeout    = 30 * time.Second
	desiredStateComponentApplyTimeout = 30 * time.Minute
	maxBlockingAttempts               = 3
	reconnectJitterPct                = 25
)

func blockingTimeoutForTrigger(triggerType string) time.Duration {
	if triggerType == string(mist.TriggerStreamLifecycle) {
		return lifecycleReconciliationTimeout
	}
	return blockingTriggerTimeout
}

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
	initTriggerForwarder(logger)
	initIngestGenerationStore(logger)
	// Recover managed-stream ownership from Mist's persisted config so
	// Foghorn-issued Retract commands work across sidecar restarts.
	HydrateAppliedManagedStreamsFromMist(logger)
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
	Response         string
	Abort            bool
	Action           ipcpb.MistTriggerAction
	Reason           string
	ErrorCode        ipcpb.IngestErrorCode
	IngestGeneration string
}

// SendMistTrigger forwards a typed MistServer trigger to Foghorn and returns response for blocking triggers
func SendMistTrigger(mistTrigger *ipcpb.MistTrigger, logger logging.Logger) (*MistTriggerResult, error) {
	return SendMistTriggerContext(context.Background(), mistTrigger, logger)
}

// SendMistTriggerContext forwards a trigger while honoring the originating HTTP request lifetime.
func SendMistTriggerContext(ctx context.Context, mistTrigger *ipcpb.MistTrigger, logger logging.Logger) (*MistTriggerResult, error) {
	triggerType := mistTrigger.TriggerType
	if !mistTrigger.Blocking {
		if err := sendMistTriggerOnce(triggerType, mistTrigger); err != nil {
			return &MistTriggerResult{Abort: true, ErrorCode: ipcpb.IngestErrorCode_INGEST_ERROR_INTERNAL}, err
		}
		return &MistTriggerResult{}, nil
	}

	attempts := max(maxBlockingAttempts, 1)
	deadline := time.Now().Add(blockingTimeoutForTrigger(triggerType))
	if requestDeadline, ok := ctx.Deadline(); ok && requestDeadline.Before(deadline) {
		deadline = requestDeadline
	}

	var lastErr error
	for attempt := 1; attempt <= attempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return &MistTriggerResult{Abort: true, ErrorCode: ipcpb.IngestErrorCode_INGEST_ERROR_TIMEOUT}, err
		}
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
		responseCh := make(chan *ipcpb.MistTriggerResponse, 1)
		pendingMutex <- struct{}{}
		pendingMistTriggers[mistTrigger.RequestId] = pendingMistTrigger{
			responseCh:  responseCh,
			triggerType: triggerType,
		}
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
		result, err := awaitMistTriggerResponseContext(ctx, responseCh, mistTrigger.RequestId, remaining)
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
	return &MistTriggerResult{Abort: true, ErrorCode: ipcpb.IngestErrorCode_INGEST_ERROR_TIMEOUT}, lastErr
}

type pendingMistTrigger struct {
	responseCh  chan *ipcpb.MistTriggerResponse
	triggerType string
}

// pendingMistTriggers tracks blocking trigger requests waiting for responses.
// The trigger type is part of the pending request because only an accepted
// PUSH_REWRITE response carries ingest-generation identity.
var (
	pendingMistTriggers = make(map[string]pendingMistTrigger)
	pendingMutex        = make(chan struct{}, 1) // Simple mutex using buffered channel
)

var errStreamDisconnected = errors.New("gRPC control stream disconnected")

func sendMistTriggerOnce(triggerType string, mistTrigger *ipcpb.MistTrigger) error {
	stream := getStream()
	if stream == nil {
		TriggersSent.WithLabelValues(triggerType, "stream_disconnected").Inc()
		return fmt.Errorf("gRPC control stream not connected")
	}

	msg := &ipcpb.ControlMessage{
		SentAt:  timestamppb.Now(),
		Payload: &ipcpb.ControlMessage_MistTrigger{MistTrigger: mistTrigger},
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
func awaitMistTriggerResponse(responseCh chan *ipcpb.MistTriggerResponse, requestID string, timeout time.Duration) (*MistTriggerResult, error) {
	return awaitMistTriggerResponseContext(context.Background(), responseCh, requestID, timeout)
}

func awaitMistTriggerResponseContext(ctx context.Context, responseCh chan *ipcpb.MistTriggerResponse, requestID string, timeout time.Duration) (*MistTriggerResult, error) {
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
			Response:         response.Response,
			Abort:            response.Abort || response.GetAction() == ipcpb.MistTriggerAction_MIST_TRIGGER_ACTION_DENY,
			Action:           response.GetAction(),
			Reason:           response.GetReason(),
			ErrorCode:        response.ErrorCode,
			IngestGeneration: response.GetIngestGeneration(),
		}, nil
	case <-ctx.Done():
		pendingMutex <- struct{}{}
		delete(pendingMistTriggers, requestID)
		<-pendingMutex
		return &MistTriggerResult{Abort: true, ErrorCode: ipcpb.IngestErrorCode_INGEST_ERROR_TIMEOUT}, ctx.Err()
	case <-disconnectCh:
		pendingMutex <- struct{}{}
		delete(pendingMistTriggers, requestID)
		<-pendingMutex

		return &MistTriggerResult{
			Abort:     true,
			ErrorCode: ipcpb.IngestErrorCode_INGEST_ERROR_INTERNAL,
		}, errStreamDisconnected
	case <-time.After(timeout):
		pendingMutex <- struct{}{}
		delete(pendingMistTriggers, requestID)
		<-pendingMutex

		return &MistTriggerResult{
			Abort:     true,
			ErrorCode: ipcpb.IngestErrorCode_INGEST_ERROR_TIMEOUT,
		}, fmt.Errorf("timeout waiting for MistTrigger response")
	}
}

// handleMistTriggerResponse processes MistTriggerResponse messages from the stream
func handleMistTriggerResponse(response *ipcpb.MistTriggerResponse) {
	if response == nil {
		return
	}
	pendingMutex <- struct{}{}
	pending, exists := pendingMistTriggers[response.GetRequestId()]
	<-pendingMutex
	if !exists {
		return
	}

	acceptedPush := pending.triggerType == string(mist.TriggerPushRewrite) &&
		!response.GetAbort() &&
		response.GetAction() != ipcpb.MistTriggerAction_MIST_TRIGGER_ACTION_DENY
	if acceptedPush {
		// Record before releasing the waiting HTTP handler. The Recv loop may immediately observe a
		// queued control command after this response; updating synchronously makes stream ordering a
		// generation fence even before the PUSH_REWRITE handler returns the approval to Mist.
		if err := RecordAdmittedIngestGeneration(response.GetResponse(), response.GetIngestGeneration(), response.GetIngestConnectorPid()); err != nil {
			if pkgLogger != nil {
				pkgLogger.WithError(err).WithField("runtime_name", response.GetResponse()).Error("Failed to persist accepted ingest generation; refusing publisher")
			}
			response.Abort = true
			response.Response = ""
			response.ErrorCode = ipcpb.IngestErrorCode_INGEST_ERROR_INTERNAL
		}
	}
	pending.responseCh <- response
}

func handleDesiredStateUpdate(ctx context.Context, logger logging.Logger, requestID string, update *ipcpb.DesiredStateUpdate, send func(*ipcpb.ControlMessage) error) {
	if update == nil {
		return
	}
	result := &ipcpb.UpdateApplyResult{
		NodeId:        update.GetNodeId(),
		TargetRelease: update.GetTargetRelease(),
	}
	restartSelf := false
	for _, component := range update.GetComponents() {
		if component == nil {
			continue
		}
		applyResult := &ipcpb.ComponentApplyResult{
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
	msg := &ipcpb.ControlMessage{
		RequestId: requestID,
		SentAt:    timestamppb.Now(),
		Payload:   &ipcpb.ControlMessage_UpdateApplyResult{UpdateApplyResult: result},
	}
	if sendDesiredStateResult(msg, restartSelf, logger, send) {
		scheduleSelfRestart(logger)
	}
}

func sendDesiredStateResult(msg *ipcpb.ControlMessage, restartSelf bool, logger logging.Logger, send func(*ipcpb.ControlMessage) error) bool {
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
		// Exiting here bypasses the signal handler, so announce the
		// planned restart explicitly — otherwise Foghorn sees a bare
		// disconnect and publishes the node unhealthy to DNS.
		if err := AnnounceRestart(logger); err != nil && logger != nil {
			logger.WithError(err).Warn("Failed to announce self-update restart to Foghorn")
		}
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

func waitForReconnection(timeout time.Duration) ipcpb.HelmsmanControl_ConnectClient {
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
func enqueueOutbox(msg *ipcpb.ControlMessage) {
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
func drainOutbox(stream ipcpb.HelmsmanControl_ConnectClient) {
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

func enqueueDurableOutbox(msg *ipcpb.ControlMessage) error {
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

func drainDurableOutbox(stream ipcpb.HelmsmanControl_ConnectClient) {
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
		var msg ipcpb.ControlMessage
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
func sendOrEnqueue(msg *ipcpb.ControlMessage) error {
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

	artifactDeleted := &ipcpb.ArtifactDeleted{
		ArtifactHash: artifactHash,
		ArtifactType: artifactType,
		FilePath:     filePath,
		Reason:       reason,
		NodeId:       getNodeID(),
		SizeBytes:    sizeBytes,
	}

	msg := &ipcpb.ControlMessage{SentAt: timestamppb.Now(), Payload: &ipcpb.ControlMessage_ArtifactDeleted{ArtifactDeleted: artifactDeleted}}
	return stream.Send(msg)
}

// SendModeChangeRequest sends an operational mode change request upstream to Foghorn.
// Called by the local HTTP API when an agent or CLI requests a mode change.
func SendModeChangeRequest(mode ipcpb.NodeOperationalMode, reason string) error {
	stream := getStream()
	if stream == nil {
		return fmt.Errorf("gRPC control stream not connected")
	}

	msg := &ipcpb.ControlMessage{
		SentAt: timestamppb.Now(),
		Payload: &ipcpb.ControlMessage_ModeChangeRequest{
			ModeChangeRequest: &ipcpb.ModeChangeRequest{
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
	client := ipcpb.NewHelmsmanControlClient(conn)
	stream, err := client.Connect(context.Background())
	if err != nil {
		return err
	}

	// Serialize every Send on this bidi stream for its lifetime: the Recv loop
	// below fans out handler goroutines and many Request*/Send* helpers
	// (RequestAuthorizeRelayPull on the hot relay path among them) send
	// concurrently, but gRPC's ClientStream.SendMsg is not concurrency-safe.
	stream = &lockedClientStream{HelmsmanControl_ConnectClient: stream}

	// Send Register using config values
	nodeID := cfg.NodeID
	roles := deriveRolesFromConfig(cfg)

	// Detect hardware specs at startup
	hwSpecs := sidecarcfg.DetectHardware(cfg.StorageLocalPath)

	reg := &ipcpb.ControlMessage{SentAt: timestamppb.Now(), Payload: &ipcpb.ControlMessage_Register{Register: &ipcpb.Register{
		NodeId:                 nodeID,
		Roles:                  roles,
		CapIngest:              cfg.CapIngest,
		CapEdge:                cfg.CapEdge,
		CapStorage:             cfg.CapStorage,
		CapProcessing:          cfg.CapProcessing,
		StorageLocal:           cfg.StorageLocalPath,
		StorageBucket:          cfg.StorageS3Bucket,
		StoragePrefix:          cfg.StorageS3Prefix,
		EnrollmentToken:        cfg.EnrollmentToken,
		Fingerprint:            collectNodeFingerprint(),
		CpuCores:               &hwSpecs.CPUCores,
		MemoryGb:               &hwSpecs.MemoryGB,
		DiskGb:                 &hwSpecs.DiskGB,
		RequestedMode:          parseRequestedMode(cfg.RequestedMode),
		RelayBaseUrl:           relayBaseURL(),
		AppliedManagedStreams:  snapshotAppliedManagedStreamsForRegister(),
		ControlProtocolVersion: controlProtocolVersion,
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
	// Kick the durable trigger forwarder so pending WAL entries get a
	// fresh send pass without waiting for the periodic tick.
	wakeupTriggerForwarder()
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
			case *ipcpb.ControlMessage_DvrStartRequest:
				go handleDVRStart(logger, x.DvrStartRequest, func(m *ipcpb.ControlMessage) { _ = stream.Send(m) }) //nolint:errcheck // best-effort report
			case *ipcpb.ControlMessage_DvrStopRequest:
				go handleDVRStop(logger, x.DvrStopRequest, func(m *ipcpb.ControlMessage) { _ = stream.Send(m) }) //nolint:errcheck // best-effort report
			case *ipcpb.ControlMessage_ClipDelete:
				go handleClipDelete(logger, x.ClipDelete, func(m *ipcpb.ControlMessage) { _ = stream.Send(m) }) //nolint:errcheck // best-effort report
			case *ipcpb.ControlMessage_DvrDelete:
				go handleDVRDelete(logger, x.DvrDelete, func(m *ipcpb.ControlMessage) { _ = stream.Send(m) }) //nolint:errcheck // best-effort report
			case *ipcpb.ControlMessage_VodDelete:
				go handleVodDelete(logger, x.VodDelete, func(m *ipcpb.ControlMessage) { _ = stream.Send(m) }) //nolint:errcheck // best-effort report
			case *ipcpb.ControlMessage_MistTriggerResponse:
				// Record accepted ingest generations in receive order. This may briefly wait for an
				// already-running command on the same runtime; that serialization is the fence that
				// prevents the command from mutating Mist after a successor is accepted.
				handleMistTriggerResponse(x.MistTriggerResponse)
			case *ipcpb.ControlMessage_MistTriggerAck:
				go handleMistTriggerAck(x.MistTriggerAck)
			case *ipcpb.ControlMessage_Error:
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
			case *ipcpb.ControlMessage_MistTrigger:
				// Foghorn-initiated command: seed immediate JSON poll/upload
				if x.MistTrigger != nil {
					if t := x.MistTrigger.GetTriggerType(); t == "seed_poll" || t == "seed_request" {
						if onSeed != nil {
							onSeed()
						}
					}
				}
			case *ipcpb.ControlMessage_ConfigSeed:
				// Receive desired config seed and trigger reconcile.
				// The sender callback lets Helmsman ACK back over the
				// existing bidi stream after TLS bundles are applied and
				// Caddy is reloaded; Foghorn gates DNS publishing on this.
				if x.ConfigSeed != nil {
					ackSender := func(m *ipcpb.ControlMessage) {
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
			case *ipcpb.ControlMessage_BalancerCapabilityUpdate:
				sidecarcfg.ApplyBalancerCapability(x.BalancerCapabilityUpdate)
			case *ipcpb.ControlMessage_DesiredStateUpdate:
				go handleDesiredStateUpdate(stream.Context(), logger, msg.GetRequestId(), x.DesiredStateUpdate, stream.Send)
			case *ipcpb.ControlMessage_FreezePermissionResponse:
				// Handle freeze permission response from Foghorn
				go handleFreezePermissionResponse(x.FreezePermissionResponse)
			case *ipcpb.ControlMessage_RecordDvrSegmentResponse:
				go handleRecordDVRSegmentResponse(x.RecordDvrSegmentResponse)
			case *ipcpb.ControlMessage_EvictableSegmentsResponse:
				go handleEvictableSegmentsResponse(x.EvictableSegmentsResponse)
			case *ipcpb.ControlMessage_RestoreLocalSegmentIndexResponse:
				go handleRestoreLocalSegmentIndexResponse(x.RestoreLocalSegmentIndexResponse)
			case *ipcpb.ControlMessage_RetryDvrSegmentUpload:
				if retryDVRSegmentHandler != nil {
					go retryDVRSegmentHandler(x.RetryDvrSegmentUpload)
				}
			case *ipcpb.ControlMessage_ReclaimDvrSegment:
				if reclaimDVRSegmentHandler != nil {
					go reclaimDVRSegmentHandler(x.ReclaimDvrSegment)
				}
			case *ipcpb.ControlMessage_FreezeRequest:
				if freezeRequestHandler != nil {
					go freezeRequestHandler(x.FreezeRequest)
				}
			case *ipcpb.ControlMessage_CanDeleteResponse:
				// Handle can-delete response from Foghorn
				go handleCanDeleteResponse(x.CanDeleteResponse)
			case *ipcpb.ControlMessage_RelayResolveResponse:
				// Relay resolve response: route to the waiting goroutine keyed
				// by request_id.
				go handleRelayResolveResponse(x.RelayResolveResponse)
			case *ipcpb.ControlMessage_AuthorizeRelayPullResponse:
				// Authorize-relay-pull response: route to the waiting goroutine.
				go handleAuthorizeRelayPullResponse(x.AuthorizeRelayPullResponse)
			case *ipcpb.ControlMessage_DtshSyncRequest:
				// Handle incremental .dtsh sync request from Foghorn
				if dtshSyncRequestHandler != nil {
					go dtshSyncRequestHandler(x.DtshSyncRequest)
				}
			case *ipcpb.ControlMessage_StopSessionsRequest:
				// Handle stop sessions request from Foghorn (billing suspension)
				go handleStopSessions(logger, x.StopSessionsRequest)
			case *ipcpb.ControlMessage_InvalidateSessionsRequest:
				// Re-run USER_NEW for active sessions after a playback policy
				// or signing-key change. Does NOT disconnect viewers.
				go handleInvalidateSessions(logger, x.InvalidateSessionsRequest)
			case *ipcpb.ControlMessage_DrainStreamRequest:
				// Old-owner drain after publisher replacement: unload lingering
				// buffer + disconnect viewers so they reselect via the new
				// owner. Idempotent. STALENESS FENCE: nuke_stream destroys by
				// runtime NAME with no session fencing at Mist, so a drain
				// that sat in a stalled control stream must NOT execute late —
				// by then the name may belong to a replacement publisher. A
				// command older than the acceptance window is refused with an
				// error response; the durable obligation re-dispatches a fresh
				// one if the drain is still owed.
				if sentAt := msg.GetSentAt(); sentAt == nil || time.Since(sentAt.AsTime()) > 15*time.Second {
					logger.WithFields(logging.Fields{
						"runtime_name": x.DrainStreamRequest.GetRuntimeName(),
						"age": func() string {
							if sentAt == nil {
								return "missing"
							}
							return time.Since(sentAt.AsTime()).String()
						}(),
					}).Warn("Refusing stale drain command (possible replacement publisher on this runtime name)")
					resp := &ipcpb.ControlMessage{Payload: &ipcpb.ControlMessage_DrainStreamResponse{
						DrainStreamResponse: &ipcpb.DrainStreamResponse{
							RuntimeName:      x.DrainStreamRequest.GetRuntimeName(),
							Error:            "drain command expired in transit",
							SourceGeneration: x.DrainStreamRequest.GetSourceGeneration(),
						},
					}}
					if err := stream.Send(resp); err != nil {
						logger.WithError(err).Warn("Failed to send stale-drain refusal")
					}
					continue
				}
				go handleDrainStream(logger, x.DrainStreamRequest, func(m *ipcpb.ControlMessage) {
					if err := stream.Send(m); err != nil {
						logger.WithError(err).Warn("Failed to send DrainStreamResponse")
					}
				})
			case *ipcpb.ControlMessage_ActivatePushTargets:
				if sentAt := msg.GetSentAt(); sentAt == nil || time.Since(sentAt.AsTime()) > 15*time.Second {
					logger.WithField("stream_name", x.ActivatePushTargets.GetStreamName()).Warn("Refusing stale push-target activation command")
					if err := stream.Send(&ipcpb.ControlMessage{Payload: &ipcpb.ControlMessage_ActivatePushTargetsResult{
						ActivatePushTargetsResult: &ipcpb.ActivatePushTargetsResult{
							StreamName:       x.ActivatePushTargets.GetStreamName(),
							Error:            "activation command expired in transit",
							SourceGeneration: x.ActivatePushTargets.GetSourceGeneration(),
						},
					}}); err != nil {
						logger.WithError(err).Warn("Failed to send stale-activation refusal")
					}
					continue
				}
				go handleActivatePushTargets(logger, x.ActivatePushTargets, func(m *ipcpb.ControlMessage) {
					if err := stream.Send(m); err != nil {
						logger.WithError(err).Warn("Failed to send ActivatePushTargetsResult")
					}
				})
			case *ipcpb.ControlMessage_DeactivatePushTargets:
				if sentAt := msg.GetSentAt(); sentAt == nil || time.Since(sentAt.AsTime()) > 15*time.Second {
					logger.WithField("stream_name", x.DeactivatePushTargets.GetStreamName()).Warn("Refusing stale push-target deactivation command")
					continue
				}
				go handleDeactivatePushTargets(logger, x.DeactivatePushTargets)
			case *ipcpb.ControlMessage_ApplyManagedStream:
				go handleApplyManagedStream(logger, x.ApplyManagedStream)
			case *ipcpb.ControlMessage_RetractManagedStream:
				go handleRetractManagedStream(logger, x.RetractManagedStream)
			case *ipcpb.ControlMessage_ValidateEdgeTokenResponse:
				handleValidateEdgeTokenResponse(msg.GetRequestId(), x.ValidateEdgeTokenResponse)
			case *ipcpb.ControlMessage_EdgeMistAdminSessionResponse:
				handleEdgeMistAdminSessionResponse(msg.GetRequestId(), x.EdgeMistAdminSessionResponse)
			case *ipcpb.ControlMessage_ThumbnailUploadResponse:
				// The ThumbnailUploaded echo is what drives Foghorn's publication (verify → CAS → publish); a
				// dropped send would silently strand the attempt, so route it through the retry outbox — a send
				// that fails (or a disconnected stream) is queued and redelivered on reconnect rather than lost.
				go handleThumbnailUploadResponse(logger, x.ThumbnailUploadResponse, func(m *ipcpb.ControlMessage) {
					if err := sendOrEnqueue(m); err != nil {
						logger.WithError(err).Warn("ThumbnailUploaded completion queued for retry on reconnect")
					}
				})
			case *ipcpb.ControlMessage_ProcessingJobRequest:
				if processingJobHandler != nil {
					go processingJobHandler(x.ProcessingJobRequest, func(m *ipcpb.ControlMessage) {
						if err := sendOrEnqueue(m); err != nil {
							logger.WithError(err).WithField("job_id", x.ProcessingJobRequest.GetJobId()).Warn("Processing job message queued for retry")
						}
					})
				}
			}
		}
	}()

	for {
		select {
		case <-hbTicker.C:
			// Heartbeat carries the verified-applied managed-streams
			// snapshot Foghorn uses as ground truth for the reconciler.
			// A Send error means the bidi stream is broken; surface it
			// so the outer Start loop reconnects (Foghorn will then
			// receive a fresh snapshot via the new Register).
			if err := stream.Send(&ipcpb.ControlMessage{
				SentAt: timestamppb.Now(),
				Payload: &ipcpb.ControlMessage_Heartbeat{Heartbeat: &ipcpb.Heartbeat{
					NodeId:                nodeID,
					AppliedManagedStreams: snapshotAppliedManagedStreamsForRegister(),
				}},
			}); err != nil {
				return fmt.Errorf("heartbeat send: %w", err)
			}
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

var hasSpaceFor = storage.HasSpaceFor

const minClipDownloadedBytes = 1024

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
	written, err := io.Copy(f, resp.Body)
	if err != nil {
		_ = os.Remove(tmpPath)
		return err
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return err
	}
	if written < minClipDownloadedBytes {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("mist returned too little media: %d bytes", written)
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
func collectNodeFingerprint() *ipcpb.NodeFingerprint {
	fp := &ipcpb.NodeFingerprint{}
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
func handleDVRStart(logger logging.Logger, req *ipcpb.DVRStartRequest, send func(*ipcpb.ControlMessage)) {
	dvrHash := req.GetDvrHash()
	streamID := req.GetStreamId()
	internalName := req.GetInternalName()
	sourceRuntimeName := req.GetSourceRuntimeName()
	sourceURL := req.GetSourceBaseUrl()
	requestID := req.GetRequestId()
	config := req.GetConfig()

	logger.WithFields(logging.Fields{
		"dvr_hash":            dvrHash,
		"stream_id":           streamID,
		"internal_name":       internalName,
		"source_runtime_name": sourceRuntimeName,
		"source_url":          sourceURL,
		"request_id":          requestID,
		"format":              config.GetFormat(),
		"window_seconds":      config.GetDvrWindowSeconds(),
		"max_entries":         config.GetMaxEntries(),
		"retention_until":     config.GetRetentionUntil(),
	}).Info("Starting DVR recording")

	// Initialize DVR manager if not already done
	initDVRManager()

	// Reject a start that a newer stop has already superseded. This closes the
	// stop-overtakes-start race: if the DVRStop for this recording reached us before
	// its DVRStart (the start committed 'starting' on Foghorn but lost the send race),
	// starting now would leave a live Mist writer behind a terminal Foghorn row. The
	// stop tombstone at generation >= this start's generation means the stop won, so
	// this start is a no-op. Foghorn's row is already stopping/terminal, so we emit no
	// report — the stop path owns the terminal state.
	if dvrManager.dvrStartSupersededByStop(dvrHash, req.GetCommandGeneration()) {
		logger.WithFields(logging.Fields{
			"dvr_hash":         dvrHash,
			"start_generation": req.GetCommandGeneration(),
			"request_id":       requestID,
		}).Warn("DVR start superseded by a newer stop (stop overtook start); rejecting idempotently, not starting")
		return
	}

	// Start the DVR recording job
	if err := dvrManager.StartRecording(dvrHash, streamID, internalName, sourceRuntimeName, sourceURL, config, send); err != nil {
		logger.WithFields(logging.Fields{
			"dvr_hash": dvrHash,
			"error":    err,
		}).Error("Failed to start DVR recording")

		// Send failure notification
		if send != nil {
			send(&ipcpb.ControlMessage{SentAt: timestamppb.Now(), Payload: &ipcpb.ControlMessage_DvrStopped{DvrStopped: &ipcpb.DVRStopped{
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
func handleDVRStop(logger logging.Logger, req *ipcpb.DVRStopRequest, send func(*ipcpb.ControlMessage)) {
	dvrHash := req.GetDvrHash()
	requestID := req.GetRequestId()

	logger.WithFields(logging.Fields{
		"dvr_hash":   dvrHash,
		"request_id": requestID,
	}).Info("Stopping DVR recording")

	// Initialize DVR manager if not already done
	initDVRManager()

	// Record the stop tombstone FIRST, before applying the stop, so a stop that
	// arrived ahead of its own DVRStart (the start still in flight) blocks that racing
	// start from creating a live writer behind this terminal stop.
	dvrManager.recordDVRStopTombstone(dvrHash, req.GetCommandGeneration())

	// Stop the DVR recording job
	if err := dvrManager.StopRecordingWithSender(dvrHash, send); err != nil {
		logger.WithFields(logging.Fields{
			"dvr_hash": dvrHash,
			"error":    err,
		}).Error("Failed to stop DVR recording")

		// Send failure notification
		if send != nil {
			send(&ipcpb.ControlMessage{SentAt: timestamppb.Now(), Payload: &ipcpb.ControlMessage_DvrStopped{DvrStopped: &ipcpb.DVRStopped{
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
func handleClipDelete(logger logging.Logger, req *ipcpb.ClipDeleteRequest, send func(*ipcpb.ControlMessage)) {
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
		send(&ipcpb.ControlMessage{SentAt: timestamppb.Now(), Payload: &ipcpb.ControlMessage_ArtifactDeleted{ArtifactDeleted: &ipcpb.ArtifactDeleted{
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
func handleDVRDelete(logger logging.Logger, req *ipcpb.DVRDeleteRequest, send func(*ipcpb.ControlMessage)) {
	dvrHash := req.GetDvrHash()
	requestID := req.GetRequestId()

	logger.WithFields(logging.Fields{
		"dvr_hash":   dvrHash,
		"request_id": requestID,
	}).Info("Deleting DVR recording files")

	// Initialize DVR manager if not already done
	initDVRManager()

	// Confirm the recording's Mist push is stopped BEFORE removing files: deleting
	// while a writer is live would leave Mist writing into a removed/recreated path.
	// If it cannot be confirmed (list failure, or a live push that won't stop), defer
	// the delete — Foghorn re-drives it.
	if !dvrManager.ConfirmDVRPushStopped(dvrHash) {
		logger.WithField("dvr_hash", dvrHash).Warn("DVR delete deferred: recording stop not confirmed; will retry")
		return
	}

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
		send(&ipcpb.ControlMessage{SentAt: timestamppb.Now(), Payload: &ipcpb.ControlMessage_DvrStopped{DvrStopped: &ipcpb.DVRStopped{
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
func handleVodDelete(logger logging.Logger, req *ipcpb.VodDeleteRequest, send func(*ipcpb.ControlMessage)) {
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
		send(&ipcpb.ControlMessage{SentAt: timestamppb.Now(), Payload: &ipcpb.ControlMessage_ArtifactDeleted{ArtifactDeleted: &ipcpb.ArtifactDeleted{
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
	dvrStopRequest := &ipcpb.DVRStopRequest{
		DvrHash:      "", // Empty hash means stop all recordings for this stream
		RequestId:    uuid.New().String(),
		InternalName: &internalName,
	}

	msg := &ipcpb.ControlMessage{SentAt: timestamppb.Now(), Payload: &ipcpb.ControlMessage_DvrStopRequest{DvrStopRequest: dvrStopRequest}}
	return stream.Send(msg)
}

// FreezePermissionHandler is called when Foghorn responds to a freeze permission request
type FreezePermissionHandler func(*ipcpb.FreezePermissionResponse)

// FreezeRequestHandler is called when Foghorn proactively requests a freeze/sync
type FreezeRequestHandler func(*ipcpb.FreezeRequest)

// DtshSyncRequestHandler is called when Foghorn sends a request to sync just the .dtsh file
type DtshSyncRequestHandler func(*ipcpb.DtshSyncRequest)

// ProcessingJobHandler is called when Foghorn sends a VOD processing job request
type ProcessingJobHandler func(*ipcpb.ProcessingJobRequest, func(*ipcpb.ControlMessage))

var (
	freezePermissionHandlers = make(map[string]chan *ipcpb.FreezePermissionResponse)
	freezePermissionMutex    = make(chan struct{}, 1)
	freezeRequestHandler     FreezeRequestHandler
	dtshSyncRequestHandler   DtshSyncRequestHandler
	processingJobHandler     ProcessingJobHandler

	// CanDelete request/response tracking
	canDeleteHandlers = make(map[string]chan *ipcpb.CanDeleteResponse)
	canDeleteMutex    = make(chan struct{}, 1)

	// RelayResolve request/response tracking. Keyed by request_id (the relay
	// generates a UUID per outstanding resolve) because the same asset can be
	// resolved concurrently for different sessions.
	relayResolveHandlers = make(map[string]chan *ipcpb.RelayResolveResponse)
	relayResolveMutex    = make(chan struct{}, 1)

	// AuthorizeRelayPull request/response tracking (serving edge asks Foghorn
	// to authorize an inbound peer-relay pull). Same keyed-by-request_id model
	// as RelayResolve.
	authorizeRelayPullHandlers = make(map[string]chan *ipcpb.AuthorizeRelayPullResponse)
	authorizeRelayPullMutex    = make(chan struct{}, 1)
)

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
func RequestFreezePermission(ctx context.Context, assetType, assetHash string, sizeBytes uint64) (*ipcpb.FreezePermissionResponse, error) {
	stream := getStream()
	if stream == nil {
		return nil, fmt.Errorf("gRPC control stream not connected")
	}

	requestID := uuid.New().String()
	responseChan := make(chan *ipcpb.FreezePermissionResponse, 1)

	// Register for response
	freezePermissionMutex <- struct{}{}
	freezePermissionHandlers[requestID] = responseChan
	<-freezePermissionMutex

	// Send request
	req := &ipcpb.FreezePermissionRequest{
		RequestId: requestID,
		AssetType: assetType,
		AssetHash: assetHash,
		SizeBytes: sizeBytes,
	}

	msg := &ipcpb.ControlMessage{SentAt: timestamppb.Now(), Payload: &ipcpb.ControlMessage_FreezePermissionRequest{FreezePermissionRequest: req}}
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
func handleFreezePermissionResponse(response *ipcpb.FreezePermissionResponse) {
	freezePermissionMutex <- struct{}{}
	responseChan, exists := freezePermissionHandlers[response.RequestId]
	<-freezePermissionMutex

	if exists {
		responseChan <- response
	}
}

// RetryDVRSegmentUploadHandler is invoked when Foghorn asks the recording
// node to re-attempt upload of specific segments during finalization.
type RetryDVRSegmentUploadHandler func(*ipcpb.RetryDVRSegmentUpload)

// ReclaimDVRSegmentHandler is invoked when Foghorn issues a reclaim
// order for local DVR segment files after every overlapping chapter
// has reached state='frozen'. The handler MUST be idempotent
// (missing-file is success).
type ReclaimDVRSegmentHandler func(*ipcpb.ReclaimDVRSegment)

var (
	recordDVRSegmentHandlers  = make(map[string]chan *ipcpb.RecordDVRSegmentResponse)
	recordDVRSegmentMutex     = make(chan struct{}, 1)
	evictableSegmentsHandlers = make(map[string]chan *ipcpb.EvictableSegmentsResponse)
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
) (*ipcpb.RecordDVRSegmentResponse, error) {
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
) (*ipcpb.RecordDVRSegmentResponse, error) {
	return recordDVRSegment(ctx, dvrHash, segmentName, localPath, mediaStartMs, mediaEndMs, durationMs, true)
}

func recordDVRSegment(
	ctx context.Context,
	dvrHash, segmentName, localPath string,
	mediaStartMs, mediaEndMs, durationMs int64,
	recoveryInsert bool,
) (*ipcpb.RecordDVRSegmentResponse, error) {
	stream := getStream()
	if stream == nil {
		return nil, fmt.Errorf("gRPC control stream not connected")
	}
	requestID := uuid.New().String()
	ch := make(chan *ipcpb.RecordDVRSegmentResponse, 1)

	recordDVRSegmentMutex <- struct{}{}
	recordDVRSegmentHandlers[requestID] = ch
	<-recordDVRSegmentMutex

	cleanup := func() {
		recordDVRSegmentMutex <- struct{}{}
		delete(recordDVRSegmentHandlers, requestID)
		<-recordDVRSegmentMutex
	}

	req := &ipcpb.RecordDVRSegmentRequest{
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
	msg := &ipcpb.ControlMessage{SentAt: timestamppb.Now(), Payload: &ipcpb.ControlMessage_RecordDvrSegmentRequest{RecordDvrSegmentRequest: req}}
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

func handleRecordDVRSegmentResponse(resp *ipcpb.RecordDVRSegmentResponse) {
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
	msg := &ipcpb.ControlMessage{
		SentAt: timestamppb.Now(),
		Payload: &ipcpb.ControlMessage_MarkDvrSegmentUploaded{MarkDvrSegmentUploaded: &ipcpb.MarkDVRSegmentUploaded{
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
// lost_local; internal chapter loss moves to failed_source_missing).
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
	msg := &ipcpb.ControlMessage{
		SentAt: timestamppb.Now(),
		Payload: &ipcpb.ControlMessage_DvrSegmentDropped{DvrSegmentDropped: &ipcpb.DVRSegmentDropped{
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
func RequestEvictableSegments(ctx context.Context, dvrHash string, maxCount int32) (*ipcpb.EvictableSegmentsResponse, error) {
	stream := getStream()
	if stream == nil {
		return nil, fmt.Errorf("gRPC control stream not connected")
	}
	requestID := uuid.New().String()
	ch := make(chan *ipcpb.EvictableSegmentsResponse, 1)

	evictableSegmentsMutex <- struct{}{}
	evictableSegmentsHandlers[requestID] = ch
	<-evictableSegmentsMutex

	cleanup := func() {
		evictableSegmentsMutex <- struct{}{}
		delete(evictableSegmentsHandlers, requestID)
		<-evictableSegmentsMutex
	}

	req := &ipcpb.EvictableSegmentsRequest{
		RequestId: requestID,
		DvrHash:   dvrHash,
		MaxCount:  maxCount,
	}
	msg := &ipcpb.ControlMessage{SentAt: timestamppb.Now(), Payload: &ipcpb.ControlMessage_EvictableSegmentsRequest{EvictableSegmentsRequest: req}}
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

func handleEvictableSegmentsResponse(resp *ipcpb.EvictableSegmentsResponse) {
	evictableSegmentsMutex <- struct{}{}
	ch, exists := evictableSegmentsHandlers[resp.GetRequestId()]
	<-evictableSegmentsMutex
	if exists {
		ch <- resp
	}
}

var (
	restoreLocalSegmentIndexHandlers = make(map[string]chan *ipcpb.RestoreLocalSegmentIndexResponse)
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
func SendRestoreLocalSegmentIndex(ctx context.Context, dvrHash string, segmentNames []string) (*ipcpb.RestoreLocalSegmentIndexResponse, error) {
	stream := getStream()
	if stream == nil {
		return nil, fmt.Errorf("gRPC control stream not connected")
	}
	if len(segmentNames) == 0 {
		return &ipcpb.RestoreLocalSegmentIndexResponse{DvrHash: dvrHash}, nil
	}
	requestID := uuid.New().String()
	ch := make(chan *ipcpb.RestoreLocalSegmentIndexResponse, 1)

	restoreLocalSegmentIndexMutex <- struct{}{}
	restoreLocalSegmentIndexHandlers[requestID] = ch
	<-restoreLocalSegmentIndexMutex

	cleanup := func() {
		restoreLocalSegmentIndexMutex <- struct{}{}
		delete(restoreLocalSegmentIndexHandlers, requestID)
		<-restoreLocalSegmentIndexMutex
	}

	req := &ipcpb.RestoreLocalSegmentIndexRequest{
		RequestId:    requestID,
		DvrHash:      dvrHash,
		SegmentNames: segmentNames,
		NodeId:       getNodeID(),
	}
	msg := &ipcpb.ControlMessage{SentAt: timestamppb.Now(), Payload: &ipcpb.ControlMessage_RestoreLocalSegmentIndexRequest{RestoreLocalSegmentIndexRequest: req}}
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

func handleRestoreLocalSegmentIndexResponse(resp *ipcpb.RestoreLocalSegmentIndexResponse) {
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

	progress := &ipcpb.FreezeProgress{
		RequestId:     requestID,
		AssetHash:     assetHash,
		Percent:       percent,
		BytesUploaded: bytesUploaded,
	}

	msg := &ipcpb.ControlMessage{SentAt: timestamppb.Now(), Payload: &ipcpb.ControlMessage_FreezeProgress{FreezeProgress: progress}}
	return stream.Send(msg)
}

// SendStorageLifecycle sends a storage lifecycle event to Foghorn (for analytics).
// Queued for retry on disconnect since these feed ClickHouse storage_events.
func SendStorageLifecycle(data *ipcpb.StorageLifecycleData) error {
	trigger := &ipcpb.MistTrigger{
		TriggerType: "storage_lifecycle",
		RequestId:   uuid.New().String(),
		NodeId:      getNodeID(),
		Blocking:    false,
		TriggerPayload: &ipcpb.MistTrigger_StorageLifecycleData{
			StorageLifecycleData: data,
		},
	}

	msg := &ipcpb.ControlMessage{SentAt: timestamppb.Now(), Payload: &ipcpb.ControlMessage_MistTrigger{MistTrigger: trigger}}
	return sendOrEnqueue(msg)
}

// SendProcessBillingEvent sends a process billing event to Foghorn (for analytics/billing)
// ProcessBillingEvent tracks transcoding usage for Livepeer and native processes
func SendProcessBillingEvent(event *ipcpb.ProcessBillingEvent) error {
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

	trigger := &ipcpb.MistTrigger{
		TriggerType: "process_billing",
		RequestId:   uuid.New().String(),
		NodeId:      getNodeID(),
		Blocking:    false,
		TriggerPayload: &ipcpb.MistTrigger_ProcessBilling{
			ProcessBilling: event,
		},
	}

	msg := &ipcpb.ControlMessage{SentAt: timestamppb.Now(), Payload: &ipcpb.ControlMessage_MistTrigger{MistTrigger: trigger}}
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
// /internal/artifact/* read-through relay. Reads HELMSMAN_RELAY_BASE_URL
// when set (the dev compose bridge, where Mist resolves to a service name
// like http://helmsman:18007); falls back to http://127.0.0.1:18007 for
// production, where Mist and Helmsman share loopback on native hosts and
// inside the single edge container alike.
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

	responseChan := make(chan *ipcpb.CanDeleteResponse, 1)

	// Register for response
	canDeleteMutex <- struct{}{}
	canDeleteHandlers[assetHash] = responseChan
	<-canDeleteMutex

	// Send request
	req := &ipcpb.CanDeleteRequest{
		AssetHash: assetHash,
		NodeId:    getNodeID(),
	}

	msg := &ipcpb.ControlMessage{SentAt: timestamppb.Now(), Payload: &ipcpb.ControlMessage_CanDeleteRequest{CanDeleteRequest: req}}
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
func handleCanDeleteResponse(response *ipcpb.CanDeleteResponse) {
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
func RequestRelayResolve(ctx context.Context, req *ipcpb.RelayResolveRequest) (*ipcpb.RelayResolveResponse, error) {
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

	responseChan := make(chan *ipcpb.RelayResolveResponse, 1)

	relayResolveMutex <- struct{}{}
	relayResolveHandlers[req.GetRequestId()] = responseChan
	<-relayResolveMutex

	msg := &ipcpb.ControlMessage{
		RequestId: req.GetRequestId(),
		SentAt:    timestamppb.Now(),
		Payload:   &ipcpb.ControlMessage_RelayResolveRequest{RelayResolveRequest: req},
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
func handleRelayResolveResponse(response *ipcpb.RelayResolveResponse) {
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

// RequestAuthorizeRelayPull asks Foghorn whether an inbound peer-relay pull
// (which presented an opaque grant id) is authorized. The serving edge holds
// no signing key; Foghorn matches the grant it minted at resolve time against
// the artifact + exact request path. Fail-closed: any error/timeout is the
// caller's cue to deny.
func RequestAuthorizeRelayPull(ctx context.Context, req *ipcpb.AuthorizeRelayPullRequest) (*ipcpb.AuthorizeRelayPullResponse, error) {
	stream := getStream()
	if stream == nil {
		return nil, fmt.Errorf("gRPC control stream not connected")
	}
	if req == nil || req.GetRequestId() == "" {
		return nil, fmt.Errorf("authorize relay pull request must have a request_id")
	}

	responseChan := make(chan *ipcpb.AuthorizeRelayPullResponse, 1)

	authorizeRelayPullMutex <- struct{}{}
	authorizeRelayPullHandlers[req.GetRequestId()] = responseChan
	<-authorizeRelayPullMutex

	defer func() {
		authorizeRelayPullMutex <- struct{}{}
		delete(authorizeRelayPullHandlers, req.GetRequestId())
		<-authorizeRelayPullMutex
	}()

	msg := &ipcpb.ControlMessage{
		RequestId: req.GetRequestId(),
		SentAt:    timestamppb.Now(),
		Payload:   &ipcpb.ControlMessage_AuthorizeRelayPullRequest{AuthorizeRelayPullRequest: req},
	}
	if err := stream.Send(msg); err != nil {
		return nil, fmt.Errorf("failed to send authorize relay pull request: %w", err)
	}

	select {
	case resp := <-responseChan:
		return resp, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-time.After(5 * time.Second):
		return nil, fmt.Errorf("timeout waiting for authorize relay pull response")
	}
}

// handleAuthorizeRelayPullResponse routes inbound responses to the waiting
// goroutine, keyed by request_id.
func handleAuthorizeRelayPullResponse(response *ipcpb.AuthorizeRelayPullResponse) {
	if response == nil || response.GetRequestId() == "" {
		return
	}
	authorizeRelayPullMutex <- struct{}{}
	responseChan, exists := authorizeRelayPullHandlers[response.GetRequestId()]
	<-authorizeRelayPullMutex
	if exists {
		// Non-blocking: the channel is buffered(1) and the waiter takes
		// exactly one. A duplicate/late response must not wedge this
		// goroutine on a full channel.
		select {
		case responseChan <- response:
		default:
		}
	}
}

// SendSyncComplete notifies Foghorn that a sync operation has completed.
// Called after successfully uploading an artifact to S3 (while keeping the local copy).
// dtshIncluded indicates whether the .dtsh index file was included in the sync.
// localMissing=true signals the local source file is gone (ENOENT) before sync;
// Foghorn marks the row sync_status='lost_local' (terminal) and stops retries.
func SendSyncComplete(requestID, assetHash, status string, sizeBytes uint64, errMsg string, dtshIncluded bool, localMissing bool) error {
	complete := &ipcpb.SyncComplete{
		RequestId:    requestID,
		AssetHash:    assetHash,
		Status:       status,
		SizeBytes:    sizeBytes,
		Error:        errMsg,
		DtshIncluded: dtshIncluded,
		LocalMissing: localMissing,
	}

	msg := &ipcpb.ControlMessage{SentAt: timestamppb.Now(), Payload: &ipcpb.ControlMessage_SyncComplete{SyncComplete: complete}}
	return sendOrEnqueue(msg)
}

// handleStopSessions terminates all sessions for the given streams on this node
// Called when a tenant is suspended due to insufficient balance
func handleStopSessions(logger logging.Logger, req *ipcpb.StopSessionsRequest) {
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
func handleInvalidateSessions(logger logging.Logger, req *ipcpb.InvalidateSessionsRequest) {
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

// handleDrainStream drops the lingering Mist buffer for a runtime name
// and disconnects its viewer sessions on this node. Issued by Foghorn's
// admission-effects worker after the database-confirmed source projection moves to a new publisher
// node; the durable obligation keeps re-dispatching until the DrainStreamResponse confirms the
// drain, so viewers do not remain on a stale buffer. Idempotent — an absent stream returns success
// with unloaded=false.
func handleDrainStream(logger logging.Logger, req *ipcpb.DrainStreamRequest, send func(*ipcpb.ControlMessage)) {
	runtimeName := strings.TrimSpace(req.GetRuntimeName())
	if runtimeName == "" {
		return
	}
	priorGeneration := strings.TrimSpace(req.GetPriorOwnerSourceGeneration())
	generationFence, generationKnown := lockIngestFence(runtimeName, false)
	currentGeneration := ""
	if generationKnown {
		currentGeneration = generationFence.generation
		generationKnown = currentGeneration != ""
	}
	if priorGeneration == "" || !generationKnown {
		if generationFence != nil {
			generationFence.Unlock()
		}
		logger.WithFields(logging.Fields{
			"runtime_name":              runtimeName,
			"expected_prior_generation": priorGeneration,
		}).Warn("Refusing drain without an exact local publisher generation")
		if send != nil {
			send(&ipcpb.ControlMessage{Payload: &ipcpb.ControlMessage_DrainStreamResponse{
				DrainStreamResponse: &ipcpb.DrainStreamResponse{
					RuntimeName:      runtimeName,
					Error:            "local publisher generation is unknown",
					SourceGeneration: req.GetSourceGeneration(),
				},
			}})
		}
		return
	}
	if currentGeneration != priorGeneration {
		generationFence.Unlock()
		logger.WithFields(logging.Fields{
			"runtime_name":              runtimeName,
			"expected_prior_generation": priorGeneration,
			"current_generation":        currentGeneration,
		}).Warn("Refusing drain for a superseded local publisher generation")
		// A different admitted generation proves the exact prior generation is no longer the local
		// runtime owner. Complete the drain obligation without touching Mist.
		if send != nil {
			send(&ipcpb.ControlMessage{Payload: &ipcpb.ControlMessage_DrainStreamResponse{
				DrainStreamResponse: &ipcpb.DrainStreamResponse{
					RuntimeName:      runtimeName,
					SourceGeneration: req.GetSourceGeneration(),
				},
			}})
		}
		return
	}

	cfg := currentConfig
	if cfg == nil {
		generationFence.Unlock()
		logger.WithField("runtime_name", runtimeName).Warn("config not initialized; cannot drain stream")
		if send != nil {
			send(&ipcpb.ControlMessage{Payload: &ipcpb.ControlMessage_DrainStreamResponse{
				DrainStreamResponse: &ipcpb.DrainStreamResponse{
					RuntimeName:      runtimeName,
					Error:            "config not initialized",
					SourceGeneration: req.GetSourceGeneration(),
				},
			}})
		}
		return
	}
	mistClient := mist.NewClient(logger)
	if cfg.MistServerURL != "" {
		mistClient.BaseURL = cfg.MistServerURL
	}

	logger.WithFields(logging.Fields{
		"runtime_name": runtimeName,
		"reason":       req.GetReason(),
	}).Info("Draining lingering Mist stream after publisher replacement")

	// Disconnect viewer sessions first so they reselect via PLAY_REWRITE
	// at the new owner. The Mist stop_sessions API doesn't return a count;
	// the response counter is best-effort/0 today.
	var partialErr string
	if err := mistClient.StopSessions(runtimeName); err != nil {
		logger.WithFields(logging.Fields{
			"runtime_name": runtimeName,
			"error":        err,
		}).Warn("StopSessions during drain failed; continuing")
		partialErr = "stop_sessions: " + err.Error()
	}

	// NukeStream (MistUtilNuke) is the correct operation for wildcard
	// instances like live+<x> / processing+<x>: deletestream only removes
	// configured stream entries (Controller::Storage["streams"]), and
	// wildcard instances are runtime-only — not in that map. nuke_stream
	// stops input/pull PIDs, wipes stream state, kills lingering
	// processes, and unlinks locks. Asynchronous on the controller side;
	// dispatch success counts as unloaded=true since there's no
	// synchronous completion signal.
	unloaded := false
	if err := mistClient.NukeStream(runtimeName); err != nil {
		logger.WithFields(logging.Fields{
			"runtime_name": runtimeName,
			"error":        err,
		}).Warn("NukeStream during drain failed")
		if partialErr != "" {
			partialErr += "; "
		}
		partialErr += "nuke_stream: " + err.Error()
	} else {
		unloaded = true
	}

	// Clear any DVR override the previous owner registered for this runtime name so publisher
	// replacement does not leave a STREAM_SOURCE override pointing at a closed publisher.
	ClearDVRSourceOverride(runtimeName)
	// The destructive Mist operations and generation recording share this runtime fence. A
	// successor response either won before our check (making this command a no-op) or waits until
	// the exact prior generation is fully drained.
	generationFence.Unlock()

	if send != nil {
		send(&ipcpb.ControlMessage{Payload: &ipcpb.ControlMessage_DrainStreamResponse{
			DrainStreamResponse: &ipcpb.DrainStreamResponse{
				RuntimeName:      runtimeName,
				Unloaded:         unloaded,
				Error:            partialErr,
				SourceGeneration: req.GetSourceGeneration(),
			},
		}})
	}
}

func handleActivatePushTargets(logger logging.Logger, req *ipcpb.ActivatePushTargets, respond func(*ipcpb.ControlMessage)) {
	if req == nil || len(req.Targets) == 0 {
		return
	}
	// report sends the correlated outcome back to Foghorn. converged=true is the ONLY completion
	// signal for the durable activation obligation — a delivery-only model would silently lose the
	// command to a crash, list failure, or PushStart failure.
	report := func(converged bool, cause string) {
		if respond == nil {
			return
		}
		respond(&ipcpb.ControlMessage{Payload: &ipcpb.ControlMessage_ActivatePushTargetsResult{
			ActivatePushTargetsResult: &ipcpb.ActivatePushTargetsResult{
				StreamName:       req.StreamName,
				Converged:        converged,
				Error:            cause,
				SourceGeneration: req.GetSourceGeneration(),
			},
		}})
	}
	requestedGeneration := strings.TrimSpace(req.GetSourceGeneration())
	generationFence, generationKnown := lockIngestFence(req.GetStreamName(), false)
	currentGeneration := ""
	activeGeneration := false
	if generationKnown {
		currentGeneration = generationFence.generation
		activeGeneration = generationFence.active
	}
	if requestedGeneration == "" || currentGeneration == "" || currentGeneration != requestedGeneration || !activeGeneration {
		if generationFence != nil {
			generationFence.Unlock()
		}
		logger.WithFields(logging.Fields{
			"stream_name":        req.GetStreamName(),
			"command_generation": requestedGeneration,
			"current_generation": currentGeneration,
		}).Warn("Refusing push-target activation for a superseded ingest generation")
		report(false, "activation generation is not current")
		return
	}

	cfg := currentConfig
	if cfg == nil {
		generationFence.Unlock()
		logger.Warn("config not initialized; cannot activate push targets")
		report(false, "config not initialized")
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

	// Request start, then confirm presence. Mist's controller serializes startPush and internally
	// dedupes an already-active (stream, target) pair, so re-dispatched activations (the durable
	// obligation retries until the converged acknowledgement) cannot create duplicate writers — no
	// Helmsman-side locking or pre-listing is needed for that.
	firstFailure := ""
	started := false
	for _, target := range req.Targets {
		err := mistClient.PushStart(req.StreamName, target.TargetUri)
		if err != nil {
			logger.WithFields(logging.Fields{
				"stream_name": req.StreamName,
				"target_id":   target.TargetId,
				"target_name": target.Name,
				"error":       err,
			}).Error("Failed to start push to target")
			if firstFailure == "" {
				firstFailure = err.Error()
			}
			continue
		}
		started = true

		logger.WithFields(logging.Fields{
			"stream_name": req.StreamName,
			"target_id":   target.TargetId,
			"target_name": target.Name,
		}).Info("Started push to multistream target")
	}
	// Confirm PROCESS CREATION, not just command parsing: Mist's push_start API succeeds even when
	// the push process could not be spawned, so re-list and require every requested target to be
	// present before reporting converged. Anything beyond creation is Mist's contract — a one-shot
	// push that later dies emits PUSH_END (and only configured auto_push entries restart), so
	// continuing health is deliberately NOT this acknowledgement's business.
	if firstFailure == "" && started {
		if confirm, confirmErr := mistClient.PushList(); confirmErr != nil {
			firstFailure = "post-start push list unavailable: " + confirmErr.Error()
		} else {
			nowLive := livePushTargetURIs(confirm, req.StreamName)
			for _, target := range req.Targets {
				if !nowLive[target.TargetUri] {
					firstFailure = "push process not created for " + target.TargetId
					break
				}
			}
		}
	}
	generationFence.Unlock()
	report(firstFailure == "", firstFailure)
}

// livePushTargetURIs maps the target URIs that currently have a live Mist push for the stream. Used
// for the POST-start confirmation: every requested target must be present before the handler
// acknowledges converged (process creation proof — Mist itself dedupes duplicate starts).
func livePushTargetURIs(pushes []mist.PushInfo, streamName string) map[string]bool {
	live := make(map[string]bool, len(pushes))
	for _, push := range pushes {
		if push.StreamName == streamName && push.TargetURI != "" {
			live[push.TargetURI] = true
		}
	}
	return live
}

func handleDeactivatePushTargets(logger logging.Logger, req *ipcpb.DeactivatePushTargets) {
	if req == nil || req.StreamName == "" {
		return
	}
	requestedGeneration := strings.TrimSpace(req.GetSourceGeneration())
	generationFence, generationKnown := lockIngestFence(req.GetStreamName(), false)
	currentGeneration := ""
	if generationKnown {
		currentGeneration = generationFence.generation
	}
	if requestedGeneration == "" || currentGeneration == "" || currentGeneration != requestedGeneration {
		if generationFence != nil {
			generationFence.Unlock()
		}
		logger.WithFields(logging.Fields{
			"stream_name":        req.GetStreamName(),
			"command_generation": requestedGeneration,
			"current_generation": currentGeneration,
		}).Warn("Refusing push-target deactivation for a superseded ingest generation")
		return
	}

	cfg := currentConfig
	if cfg == nil {
		generationFence.Unlock()
		return
	}

	mistClient := mist.NewClient(logger)
	if cfg.MistServerURL != "" {
		mistClient.BaseURL = cfg.MistServerURL
	}

	// List active pushes and stop any matching this stream
	pushes, err := mistClient.PushList()
	if err != nil {
		generationFence.Unlock()
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
	generationFence.Unlock()

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
	resp      *ipcpb.ValidateEdgeTokenResponse
	expiresAt time.Time
}

var (
	pendingEdgeTokenValidations = make(map[string]chan *ipcpb.ValidateEdgeTokenResponse)
	pendingEdgeTokenMutex       = make(chan struct{}, 1)
	edgeTokenCache              sync.Map // token string -> *edgeTokenResult
	edgeTokenCacheTTL           = 5 * time.Minute
)

func handleValidateEdgeTokenResponse(requestID string, resp *ipcpb.ValidateEdgeTokenResponse) {
	pendingEdgeTokenMutex <- struct{}{}
	ch, exists := pendingEdgeTokenValidations[requestID]
	<-pendingEdgeTokenMutex

	if exists {
		ch <- resp
	}
}

// ValidateEdgeToken sends a token to Foghorn for validation and returns the result.
// Results are cached with a TTL to avoid round-tripping on every request.
func ValidateEdgeToken(ctx context.Context, token string) (*ipcpb.ValidateEdgeTokenResponse, error) {
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
	responseCh := make(chan *ipcpb.ValidateEdgeTokenResponse, 1)

	pendingEdgeTokenMutex <- struct{}{}
	pendingEdgeTokenValidations[requestID] = responseCh
	<-pendingEdgeTokenMutex

	msg := &ipcpb.ControlMessage{
		RequestId: requestID,
		SentAt:    timestamppb.Now(),
		Payload: &ipcpb.ControlMessage_ValidateEdgeTokenRequest{
			ValidateEdgeTokenRequest: &ipcpb.ValidateEdgeTokenRequest{Token: token},
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
	resp      *ipcpb.EdgeMistAdminSessionResponse
	expiresAt time.Time
}

var (
	pendingMistAdminSessions = make(map[string]chan *ipcpb.EdgeMistAdminSessionResponse)
	pendingMistAdminMutex    = make(chan struct{}, 1)
	mistAdminSessionCache    sync.Map // token string -> *mistAdminSessionResult
	mistAdminSessionCacheTTL = 1 * time.Minute
)

func handleEdgeMistAdminSessionResponse(requestID string, resp *ipcpb.EdgeMistAdminSessionResponse) {
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
func ValidateMistAdminSession(ctx context.Context, token string) (*ipcpb.EdgeMistAdminSessionResponse, error) {
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
	responseCh := make(chan *ipcpb.EdgeMistAdminSessionResponse, 1)

	pendingMistAdminMutex <- struct{}{}
	pendingMistAdminSessions[requestID] = responseCh
	<-pendingMistAdminMutex

	msg := &ipcpb.ControlMessage{
		RequestId: requestID,
		SentAt:    timestamppb.Now(),
		Payload: &ipcpb.ControlMessage_EdgeMistAdminSessionRequest{
			EdgeMistAdminSessionRequest: &ipcpb.EdgeMistAdminSessionRequest{Token: token},
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

func parseRequestedMode(mode string) ipcpb.NodeOperationalMode {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "draining", "drain":
		return ipcpb.NodeOperationalMode_NODE_OPERATIONAL_MODE_DRAINING
	case "maintenance", "maint":
		return ipcpb.NodeOperationalMode_NODE_OPERATIONAL_MODE_MAINTENANCE
	case "", "normal":
		return ipcpb.NodeOperationalMode_NODE_OPERATIONAL_MODE_NORMAL
	default:
		return ipcpb.NodeOperationalMode_NODE_OPERATIONAL_MODE_UNSPECIFIED
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
	req := &ipcpb.ThumbnailUploadRequest{
		InternalName: internalName,
		FilePaths:    filePaths,
	}

	msg := &ipcpb.ControlMessage{
		RequestId: requestID,
		SentAt:    timestamppb.Now(),
		Payload:   &ipcpb.ControlMessage_ThumbnailUploadRequest{ThumbnailUploadRequest: req},
	}
	return stream.Send(msg)
}

// handleThumbnailUploadResponse uploads thumbnail files to S3 using presigned URLs
// from Foghorn, then sends a ThumbnailUploaded confirmation.
func handleThumbnailUploadResponse(logger logging.Logger, resp *ipcpb.ThumbnailUploadResponse, send func(*ipcpb.ControlMessage)) {
	thumbnailKey := resp.GetThumbnailKey()
	uploads := resp.GetUploads()

	logger.WithFields(logging.Fields{
		"thumbnail_key": thumbnailKey,
		"upload_count":  len(uploads),
	}).Debug("Received thumbnail presigned URLs from Foghorn")

	presignedClient := storage.NewPresignedClient(logger)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	var uploadedKeys []string
	failedUploads := 0
	for _, upload := range uploads {
		localPath := upload.GetLocalPath()
		if localPath == "" {
			logger.WithField("file_name", upload.GetFileName()).Warn("No local_path in thumbnail upload response")
			failedUploads++
			continue
		}
		if upload.GetPresignedUrl() == "" {
			logger.WithField("file_name", upload.GetFileName()).Warn("No presigned URL in thumbnail upload response")
			failedUploads++
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
				failedUploads++
				continue
			}
			normalized := normalizeThumbnailVTTReferences(string(data))
			if err := presignedClient.UploadBytesToPresignedURL(ctx, upload.GetPresignedUrl(), []byte(normalized), nil); err != nil {
				logger.WithFields(logging.Fields{
					"file_name":  upload.GetFileName(),
					"local_path": localPath,
					"s3_key":     upload.GetS3Key(),
					"error":      err,
				}).Error("Failed to upload thumbnail to S3")
				failedUploads++
				continue
			}
		} else {
			// Read the whole file first: Mist can still be rewriting a
			// thumbnail when THUMBNAIL_UPDATED fires, and a stat-then-stream
			// upload dies with a ContentLength/body-length mismatch when the
			// file changes underneath it. A single in-memory snapshot keeps
			// length and body consistent (thumbnails are small).
			data, err := os.ReadFile(localPath)
			if err != nil {
				logger.WithFields(logging.Fields{
					"file_name":  upload.GetFileName(),
					"local_path": localPath,
					"error":      err,
				}).Error("Failed to read thumbnail file")
				failedUploads++
				continue
			}
			if err := presignedClient.UploadBytesToPresignedURL(ctx, upload.GetPresignedUrl(), data, nil); err != nil {
				logger.WithFields(logging.Fields{
					"file_name":  upload.GetFileName(),
					"local_path": localPath,
					"s3_key":     upload.GetS3Key(),
					"error":      err,
				}).Error("Failed to upload thumbnail to S3")
				failedUploads++
				continue
			}
		}

		uploadedKeys = append(uploadedKeys, upload.GetS3Key())
		logger.WithFields(logging.Fields{
			"file_name": upload.GetFileName(),
			"s3_key":    upload.GetS3Key(),
		}).Debug("Thumbnail uploaded to S3")
	}

	if failedUploads > 0 || len(uploadedKeys) != len(uploads) {
		logger.WithFields(logging.Fields{
			"uploaded_count": len(uploadedKeys),
			"failed_count":   failedUploads,
			"expected_count": len(uploads),
		}).Warn("Thumbnail upload incomplete; not marking thumbnail ready")
		return
	}

	// Notify Foghorn that upload is complete. Echo the server-minted attempt id so Foghorn can bind this
	// confirmation to the assignment it minted (the presigned s3_key values were per-attempt STAGING keys).
	uploaded := &ipcpb.ThumbnailUploaded{
		ThumbnailKey: thumbnailKey,
		S3Keys:       uploadedKeys,
		AttemptId:    resp.GetAttemptId(),
	}
	send(&ipcpb.ControlMessage{
		SentAt:  timestamppb.Now(),
		Payload: &ipcpb.ControlMessage_ThumbnailUploaded{ThumbnailUploaded: uploaded},
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
