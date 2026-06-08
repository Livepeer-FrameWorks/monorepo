package control

import (
	"sync"
	"sync/atomic"
	"time"

	"frameworks/api_sidecar/internal/storage"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"

	"google.golang.org/protobuf/types/known/timestamppb"
)

// Durable forwarding for final-event and source-presence Mist triggers.
// Helmsman persists each such trigger to a local WAL before responding
// 200 OK to Mist, then forwards asynchronously via the existing
// HelmsmanControl bidi stream and waits for Foghorn's MistTriggerAck
// before truncating the WAL row. See docs/architecture/trigger-durability.md.

const (
	// triggerAckTimeout bounds how long a single forwarder pass waits for
	// a positive/negative ack before giving up and re-trying on the next
	// pass. Foghorn's downstream (Decklog + Kafka) is generally low-ms but
	// can spike under load; 30s is well past p99.
	triggerAckTimeout = 30 * time.Second

	// triggerForwarderTickInterval bounds how often the forwarder retries
	// independently of explicit wakeups, so timed-out or post-reconnect
	// entries get a fresh attempt without external prodding.
	triggerForwarderTickInterval = 10 * time.Second
)

var (
	triggerWAL              *storage.TriggerWAL
	triggerWALInitOnce      sync.Once
	triggerForwarderWakeup  = make(chan struct{}, 1)
	triggerForwarderStarted atomic.Bool

	pendingTriggerAcks   = make(map[string]chan *ipcpb.MistTriggerAck)
	pendingTriggerAcksMu sync.Mutex
)

// initTriggerForwarder opens the on-disk WAL and starts the background
// forwarder goroutine. Idempotent — safe to call multiple times. Errors
// opening the WAL are surfaced via logging since Helmsman cannot start
// without trigger durability and the caller can't usefully recover.
func initTriggerForwarder(logger logging.Logger) {
	triggerWALInitOnce.Do(func() {
		wal, err := storage.NewTriggerWAL(storage.DefaultTriggerWALDir())
		if err != nil {
			logger.WithError(err).Fatal("Failed to open trigger WAL; final-event triggers cannot be durably recorded")
		}
		triggerWAL = wal
		triggerForwarderStarted.Store(true)
		go triggerForwarderLoop(logger)
	})
}

// SendDurableMistTrigger persists a trigger to the WAL and notifies the
// forwarder. Returns once the durable write has fsynced — handlers can
// then safely respond 200 OK to Mist. The trigger MUST have RequestId
// set to a stable source_event_id
// (sha256(node_id || NUL || trigger_type || NUL || payload_raw)); duplicate
// deliveries from Mist with the same id collide on disk and are forwarded at
// most once.
func SendDurableMistTrigger(trigger *ipcpb.MistTrigger) error {
	if !triggerForwarderStarted.Load() || triggerWAL == nil {
		return errTriggerForwarderUnready
	}
	created, err := triggerWAL.Append(trigger)
	triggerType := trigger.GetTriggerType()
	if err != nil {
		TriggerWALAppends.WithLabelValues(triggerType, "error").Inc()
		return err
	}
	if created {
		TriggerWALAppends.WithLabelValues(triggerType, "appended").Inc()
	} else {
		TriggerWALAppends.WithLabelValues(triggerType, "duplicate").Inc()
	}
	updateTriggerWALDepthGauge()
	wakeupTriggerForwarder()
	return nil
}

func updateTriggerWALDepthGauge() {
	if triggerWAL == nil {
		return
	}
	if depth, err := triggerWAL.PendingDepth(); err == nil {
		TriggerWALPending.Set(float64(depth))
	}
}

func wakeupTriggerForwarder() {
	select {
	case triggerForwarderWakeup <- struct{}{}:
	default:
	}
}

// KickTriggerForwarder is the admin-endpoint hook that asks the forwarder
// to drain the WAL immediately rather than waiting for the next tick.
// Safe to call from any goroutine; returns ErrTriggerForwarderUnready
// when the forwarder hasn't booted yet.
func KickTriggerForwarder() error {
	if !triggerForwarderStarted.Load() {
		return errTriggerForwarderUnready
	}
	wakeupTriggerForwarder()
	return nil
}

// TriggerWALPendingDepth returns the count of outstanding durable
// triggers awaiting positive ack. Used by /internal/triggers/wal and by
// Grafana for the canonical "is anything stuck?" signal.
func TriggerWALPendingDepth() (int, error) {
	if triggerWAL == nil {
		return 0, errTriggerForwarderUnready
	}
	return triggerWAL.PendingDepth()
}

// ListTriggerWALPending returns the persisted MistTrigger envelopes in
// oldest-first order. Used by /internal/triggers/wal for inspection.
func ListTriggerWALPending() ([]*ipcpb.MistTrigger, error) {
	if triggerWAL == nil {
		return nil, errTriggerForwarderUnready
	}
	return triggerWAL.Pending()
}

// handleMistTriggerAck routes Foghorn's ack to whatever forwarder pass is
// blocked awaiting it. Calls that arrive after the ack channel has been
// removed (timeout, restart) are dropped — the next forwarder pass will
// re-send and observe the ack again.
func handleMistTriggerAck(ack *ipcpb.MistTriggerAck) {
	if ack == nil {
		return
	}
	pendingTriggerAcksMu.Lock()
	ch, ok := pendingTriggerAcks[ack.RequestId]
	pendingTriggerAcksMu.Unlock()
	if !ok {
		return
	}
	select {
	case ch <- ack:
	default:
	}
}

func triggerForwarderLoop(logger logging.Logger) {
	ticker := time.NewTicker(triggerForwarderTickInterval)
	defer ticker.Stop()
	for {
		drainTriggerWAL(logger)
		updateTriggerWALDepthGauge()
		select {
		case <-triggerForwarderWakeup:
		case <-ticker.C:
		}
	}
}

func drainTriggerWAL(logger logging.Logger) {
	stream := getStream()
	if stream == nil {
		return // no active stream; pending entries stay on disk
	}
	pending, err := triggerWAL.Pending()
	if err != nil {
		logger.WithError(err).Warn("Failed to read trigger WAL")
		return
	}
	removed := make(map[string]struct{})
	for _, trigger := range pending {
		if getStream() == nil {
			return // disconnect mid-drain; resume on reconnect
		}
		requestID := trigger.GetRequestId()
		if _, ok := removed[requestID]; ok {
			continue
		}
		if sendDurableTriggerAndAwaitAck(trigger, logger) {
			removed[requestID] = struct{}{}
		}
	}
}

func sendDurableTriggerAndAwaitAck(trigger *ipcpb.MistTrigger, logger logging.Logger) bool {
	requestID := trigger.GetRequestId()
	if requestID == "" {
		logger.Warn("Skipping WAL trigger with empty request_id")
		return false
	}

	ch := make(chan *ipcpb.MistTriggerAck, 1)
	pendingTriggerAcksMu.Lock()
	pendingTriggerAcks[requestID] = ch
	pendingTriggerAcksMu.Unlock()
	defer func() {
		pendingTriggerAcksMu.Lock()
		delete(pendingTriggerAcks, requestID)
		pendingTriggerAcksMu.Unlock()
	}()

	stream := getStream()
	if stream == nil {
		return false
	}
	msg := &ipcpb.ControlMessage{
		SentAt:  timestamppb.Now(),
		Payload: &ipcpb.ControlMessage_MistTrigger{MistTrigger: trigger},
	}
	logFields := TriggerSummaryFields(trigger, requestID)
	if err := stream.Send(msg); err != nil {
		logger.WithError(err).WithFields(logFields).Warn("Stream send failed; will retry from WAL")
		return false
	}

	triggerType := trigger.GetTriggerType()
	select {
	case ack := <-ch:
		if ack.GetSuccess() {
			TriggerAckOutcomes.WithLabelValues(triggerType, "success").Inc()
			if err := triggerWAL.Ack(requestID); err != nil {
				logger.WithError(err).WithField("source_event_id", requestID).Warn("Failed to truncate WAL entry after positive ack")
			}
			updateTriggerWALDepthGauge()
			return true
		}
		if ack.GetRetryable() {
			TriggerAckOutcomes.WithLabelValues(triggerType, "retryable").Inc()
			logFields["error_code"] = ack.GetErrorCode().String()
			logger.WithFields(logFields).Warn("Negative retryable ack; will retry on next forwarder pass")
			return false
		}
		TriggerAckOutcomes.WithLabelValues(triggerType, "non_retryable").Inc()
		logFields["error_code"] = ack.GetErrorCode().String()
		logFields["error_message"] = ack.GetErrorMessage()
		logger.WithFields(logFields).Error("Non-retryable trigger ack; moving entry to dead-letter")
		if err := triggerWAL.DeadLetter(requestID); err != nil {
			logger.WithError(err).WithFields(logFields).Warn("Failed to dead-letter non-retryable WAL entry")
			return false
		}
		updateTriggerWALDepthGauge()
		return true
	case <-time.After(triggerAckTimeout):
		TriggerAckOutcomes.WithLabelValues(triggerType, "timeout").Inc()
		logger.WithFields(logFields).Warn("Timed out waiting for trigger ack; will retry")
	}
	return false
}

// TriggerSummaryFields returns stable incident-response fields for a Mist trigger.
func TriggerSummaryFields(trigger *ipcpb.MistTrigger, requestID string) logging.Fields {
	fields := logging.Fields{
		"source_event_id": requestID,
	}
	if trigger == nil {
		return fields
	}
	fields["trigger_type"] = trigger.GetTriggerType()
	fields["tenant_id"] = trigger.GetTenantId()
	fields["node_id"] = trigger.GetNodeId()
	fields["stream_id"] = trigger.GetStreamId()
	fields["received_at_ms"] = trigger.GetTimestamp()
	if trigger.GetTimestamp() > 0 {
		fields["wal_age_ms"] = time.Now().UnixMilli() - trigger.GetTimestamp()
	}
	switch {
	case trigger.GetViewerDisconnect() != nil:
		p := trigger.GetViewerDisconnect()
		fields["stream_name"] = p.GetStreamName()
		fields["session_id"] = p.GetSessionId()
	case trigger.GetStreamEnd() != nil:
		fields["stream_name"] = trigger.GetStreamEnd().GetStreamName()
	case trigger.GetPushEnd() != nil:
		p := trigger.GetPushEnd()
		fields["stream_name"] = p.GetStreamName()
		fields["push_id"] = p.GetPushId()
	case trigger.GetRecordingComplete() != nil:
		fields["stream_name"] = trigger.GetRecordingComplete().GetStreamName()
	case trigger.GetRecordingSegment() != nil:
		p := trigger.GetRecordingSegment()
		fields["stream_name"] = p.GetStreamName()
		fields["duration_ms"] = p.GetDurationMs()
	case trigger.GetProcessBilling() != nil:
		p := trigger.GetProcessBilling()
		fields["stream_name"] = p.GetStreamName()
		fields["process_type"] = p.GetProcessType()
	}
	return fields
}

// errTriggerForwarderUnready is returned when SendDurableMistTrigger is
// called before the forwarder has booted. In normal operation this is
// only possible during a startup race; tests can pin against it.
var errTriggerForwarderUnready = newControlError("trigger forwarder not initialized")

func newControlError(s string) error {
	return &controlError{msg: s}
}

type controlError struct{ msg string }

func (e *controlError) Error() string { return e.msg }
