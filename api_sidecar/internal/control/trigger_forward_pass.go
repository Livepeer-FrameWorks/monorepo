package control

import (
	"strings"
	"sync"
	"time"

	"frameworks/api_sidecar/internal/storage"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"

	"google.golang.org/protobuf/types/known/timestamppb"
)

const (
	// triggerForwardWindow is how many durable triggers may await their ack on one connection.
	// Foghorn handles every trigger in its own goroutine and acks after a cross-region Kafka
	// commit, about 260 ms per entry on a production edge, so 64 in flight carry roughly 240
	// triggers/s, sixty times the peak intake measured there. A reconnect resends at most 64
	// entries, and one node never occupies more than 64 Foghorn handlers.
	triggerForwardWindow = 64

	// triggerForwardSampleWindow caps the slots billing samples may occupy, so a lifecycle
	// trigger always finds a free slot within one ack of being appended however deep the
	// sample backlog is.
	triggerForwardSampleWindow = 48

	// triggerForwardLookahead bounds how many decoded entries a pass holds per lane while it
	// looks past entries whose order key already has one in flight.
	triggerForwardLookahead = 1024
)

// triggerRetryNotBefore holds back entries whose last attempt failed, so the wakeup of every new
// append does not resend a failing window.
var triggerRetryNotBefore = struct {
	sync.Mutex
	at map[string]time.Time
}{at: make(map[string]time.Time)}

func deferTriggerRetry(requestID string, now time.Time) {
	triggerRetryNotBefore.Lock()
	triggerRetryNotBefore.at[requestID] = now.Add(triggerForwarderTickInterval)
	triggerRetryNotBefore.Unlock()
}

func clearTriggerRetry(requestID string) {
	triggerRetryNotBefore.Lock()
	delete(triggerRetryNotBefore.at, requestID)
	triggerRetryNotBefore.Unlock()
}

func triggerRetryDeferred(requestID string, now time.Time) bool {
	triggerRetryNotBefore.Lock()
	defer triggerRetryNotBefore.Unlock()
	at, ok := triggerRetryNotBefore.at[requestID]
	if !ok {
		return false
	}
	if !now.Before(at) {
		delete(triggerRetryNotBefore.at, requestID)
		return false
	}
	return true
}

// triggerOrderKey names the entries whose relative order Foghorn depends on. Entries sharing a
// key are delivered one at a time in WAL order; entries with different keys, or no key, overlap.
// Every end trigger of a stream shares the stream's key, so a PUSH_INPUT_CLOSE, STREAM_END,
// PUSH_END or recording end never overtakes an earlier one for the same runtime. A viewer's
// USER_END concerns only that session. Billing samples carry no ordering.
func triggerOrderKey(trigger *ipcpb.MistTrigger) string {
	stream := func(name string) string { return "stream:" + strings.TrimSpace(name) }
	switch {
	case trigger.GetProcessBilling() != nil:
		return ""
	case trigger.GetViewerDisconnect() != nil:
		p := trigger.GetViewerDisconnect()
		if session := strings.TrimSpace(p.GetSessionId()); session != "" {
			return "session:" + session
		}
		return stream(p.GetStreamName())
	case trigger.GetPushInputClose() != nil:
		return stream(trigger.GetPushInputClose().GetStreamName())
	case trigger.GetStreamEnd() != nil:
		return stream(trigger.GetStreamEnd().GetStreamName())
	case trigger.GetPushEnd() != nil:
		return stream(trigger.GetPushEnd().GetStreamName())
	case trigger.GetRecordingComplete() != nil:
		return stream(trigger.GetRecordingComplete().GetStreamName())
	case trigger.GetRecordingSegment() != nil:
		return stream(trigger.GetRecordingSegment().GetStreamName())
	case trigger.GetRestreamStatus() != nil:
		return stream(trigger.GetRestreamStatus().GetStreamName())
	default:
		return "type:" + trigger.GetTriggerType()
	}
}

type inflightTrigger struct {
	trigger  *ipcpb.MistTrigger
	key      string
	lane     storage.TriggerLane
	deadline time.Time
}

// laneCursor walks one WAL lane in path order.
type laneCursor struct {
	after     string
	buffer    []storage.PendingEntry
	exhausted bool
	reorders  uint64
}

// triggerForwardPass delivers the WAL on one connection with up to triggerForwardWindow entries
// awaiting their ack. An ack can only arrive on the connection its entry was sent on, so when that
// connection ends the pass ends and the next connection resends everything still in the WAL.
type triggerForwardPass struct {
	wal        *storage.TriggerWAL
	connection *streamConn
	logger     logging.Logger
	acks       chan *ipcpb.MistTriggerAck

	inflight map[string]*inflightTrigger
	busyKeys map[string]int
	// blockedKeys hold the keys whose oldest entry failed, or waits out its retry delay, in this
	// pass; later entries with the key stay behind it.
	blockedKeys map[string]struct{}

	lanes        [2]laneCursor
	laneInflight [2]int
}

func drainTriggerWAL(logger logging.Logger) {
	connection := getConnection()
	if connection == nil || connection.stream == nil || triggerWAL == nil {
		return // no active stream; pending entries stay on disk
	}
	pass := &triggerForwardPass{
		wal:         triggerWAL,
		connection:  connection,
		logger:      logger,
		acks:        make(chan *ipcpb.MistTriggerAck, 2*triggerForwardWindow),
		inflight:    make(map[string]*inflightTrigger),
		busyKeys:    make(map[string]int),
		blockedKeys: make(map[string]struct{}),
	}
	for lane := range pass.lanes {
		pass.lanes[lane].reorders = triggerWAL.Reorders(storage.TriggerLane(lane))
	}
	defer pass.release()
	pass.run()
}

func (p *triggerForwardPass) onConnection() bool {
	current := getConnection()
	return current != nil && current.epoch == p.connection.epoch
}

func (p *triggerForwardPass) run() {
	for {
		if !p.onConnection() {
			p.abandon("connection_ended")
			return
		}
		if !p.fill() {
			return
		}
		if len(p.inflight) == 0 {
			return
		}
		timer := time.NewTimer(time.Until(p.earliestDeadline()))
		select {
		case ack := <-p.acks:
			p.handleAck(ack)
		case <-p.connection.ended:
			timer.Stop()
			p.abandon("connection_ended")
			return
		case <-timer.C:
			p.expire(time.Now())
		case <-triggerForwarderWakeup:
			for lane := range p.lanes {
				p.lanes[lane].exhausted = false
			}
		}
		timer.Stop()
	}
}

// fill sends entries until the window is full or nothing more may be sent now. It returns false
// when a send failed and the pass must stop.
func (p *triggerForwardPass) fill() bool {
	for len(p.inflight) < triggerForwardWindow {
		trigger, lane := p.next()
		if trigger == nil {
			return true
		}
		if !p.send(trigger, lane) {
			return false
		}
	}
	return true
}

// next returns the entry to send now: a waiting admission's runtime end triggers first, then the
// oldest lifecycle entry whose order key is free, then the oldest billing sample while samples
// hold fewer than triggerForwardSampleWindow slots.
func (p *triggerForwardPass) next() (*ipcpb.MistTrigger, storage.TriggerLane) {
	now := time.Now()
	if trigger := p.nextPriority(now); trigger != nil {
		return trigger, storage.LaneLifecycle
	}
	if trigger := p.nextInLane(storage.LaneLifecycle, now); trigger != nil {
		return trigger, storage.LaneLifecycle
	}
	if p.laneInflight[storage.LaneSample] >= triggerForwardSampleWindow {
		return nil, storage.LaneSample
	}
	return p.nextInLane(storage.LaneSample, now), storage.LaneSample
}

func (p *triggerForwardPass) nextInLane(lane storage.TriggerLane, now time.Time) *ipcpb.MistTrigger {
	cursor := &p.lanes[lane]
	if reorders := p.wal.Reorders(lane); reorders != cursor.reorders {
		cursor.reorders = reorders
		cursor.after, cursor.buffer, cursor.exhausted = "", nil, false
	}
	for {
		if len(cursor.buffer) < triggerForwardLookahead/2 && !cursor.exhausted && !p.refill(lane) {
			return nil
		}
		if trigger := p.takeSendable(cursor, now); trigger != nil {
			return trigger
		}
		if cursor.exhausted || len(cursor.buffer) >= triggerForwardLookahead || !p.refill(lane) {
			return nil
		}
	}
}

func (p *triggerForwardPass) takeSendable(cursor *laneCursor, now time.Time) *ipcpb.MistTrigger {
	keysSeen := make(map[string]struct{})
	kept := cursor.buffer[:0]
	var picked *ipcpb.MistTrigger
	for _, entry := range cursor.buffer {
		if picked != nil {
			kept = append(kept, entry)
			continue
		}
		trigger := entry.Trigger
		id := trigger.GetRequestId()
		if _, sent := p.inflight[id]; sent || !p.wal.IsPending(id) {
			continue
		}
		key := triggerOrderKey(trigger)
		if key != "" {
			_, seen := keysSeen[key]
			_, blocked := p.blockedKeys[key]
			keysSeen[key] = struct{}{}
			if seen || blocked || p.busyKeys[key] > 0 {
				kept = append(kept, entry)
				continue
			}
		}
		if triggerRetryDeferred(id, now) {
			if key != "" {
				p.blockedKeys[key] = struct{}{}
			}
			continue
		}
		picked = trigger
	}
	cursor.buffer = kept
	return picked
}

// refill reads the entries after the lane cursor into its lookahead buffer. A read failure stops
// reading the lane for this pass; entries already in flight still complete.
func (p *triggerForwardPass) refill(lane storage.TriggerLane) bool {
	cursor := &p.lanes[lane]
	want := triggerForwardLookahead - len(cursor.buffer)
	entries, err := p.wal.PendingAfter(lane, cursor.after, want)
	if err != nil {
		cursor.exhausted = true
		p.logger.WithError(err).Warn("Failed to read trigger WAL entries; retrying on the next pass")
		return false
	}
	if len(entries) < want {
		cursor.exhausted = true
	}
	if len(entries) > 0 {
		cursor.after = entries[len(entries)-1].Path
		cursor.buffer = append(cursor.buffer, entries...)
	}
	return true
}

// nextPriority returns the oldest undelivered end trigger of a runtime an admission waits on.
func (p *triggerForwardPass) nextPriority(now time.Time) *ipcpb.MistTrigger {
	for _, runtime := range priorityRuntimeNames() {
		triggers, err := p.wal.PendingRuntimeBatch(runtime)
		if err != nil {
			p.logger.WithError(err).WithField("runtime_name", runtime).Warn("Failed to read a waiting runtime's end triggers from the WAL")
			continue
		}
		for _, trigger := range triggers {
			id := trigger.GetRequestId()
			if _, sent := p.inflight[id]; sent || p.busyKeys[triggerOrderKey(trigger)] > 0 || triggerRetryDeferred(id, now) {
				break
			}
			return trigger
		}
	}
	return nil
}

func (p *triggerForwardPass) send(trigger *ipcpb.MistTrigger, lane storage.TriggerLane) bool {
	requestID := trigger.GetRequestId()
	if requestID == "" {
		p.logger.WithField("trigger_type", trigger.GetTriggerType()).Warn("Skipping WAL trigger with empty request_id")
		return true
	}
	key := triggerOrderKey(trigger)
	pendingTriggerAcksMu.Lock()
	pendingTriggerAcks[requestID] = p.acks
	pendingTriggerAcksMu.Unlock()
	p.inflight[requestID] = &inflightTrigger{trigger: trigger, key: key, lane: lane, deadline: time.Now().Add(triggerAckTimeout)}
	p.laneInflight[lane]++
	if key != "" {
		p.busyKeys[key]++
	}
	msg := &ipcpb.ControlMessage{
		SentAt:  timestamppb.Now(),
		Payload: &ipcpb.ControlMessage_MistTrigger{MistTrigger: trigger},
	}
	if err := p.connection.stream.Send(msg); err != nil {
		p.logger.WithError(err).WithFields(TriggerSummaryFields(trigger, requestID)).Warn("Stream send failed; will retry from WAL")
		p.abandon("send_failed")
		return false
	}
	return true
}

func (p *triggerForwardPass) finish(requestID string) *inflightTrigger {
	entry, ok := p.inflight[requestID]
	if !ok {
		return nil
	}
	delete(p.inflight, requestID)
	p.laneInflight[entry.lane]--
	if entry.key != "" {
		if p.busyKeys[entry.key] <= 1 {
			delete(p.busyKeys, entry.key)
		} else {
			p.busyKeys[entry.key]--
		}
	}
	pendingTriggerAcksMu.Lock()
	if pendingTriggerAcks[requestID] == p.acks {
		delete(pendingTriggerAcks, requestID)
	}
	pendingTriggerAcksMu.Unlock()
	return entry
}

// failed keeps the entry in the WAL, holds it back for the retry delay, and keeps later entries
// with its order key behind it for the rest of the pass.
func (p *triggerForwardPass) failed(entry *inflightTrigger) {
	deferTriggerRetry(entry.trigger.GetRequestId(), time.Now())
	if entry.key != "" {
		p.blockedKeys[entry.key] = struct{}{}
	}
}

func (p *triggerForwardPass) handleAck(ack *ipcpb.MistTriggerAck) {
	requestID := ack.GetRequestId()
	entry := p.finish(requestID)
	if entry == nil {
		return
	}
	triggerType := entry.trigger.GetTriggerType()
	logFields := TriggerSummaryFields(entry.trigger, requestID)
	if ack.GetSuccess() {
		TriggerAckOutcomes.WithLabelValues(triggerType, "success").Inc()
		clearTriggerRetry(requestID)
		if err := p.wal.Ack(requestID); err != nil {
			p.logger.WithError(err).WithField("source_event_id", requestID).Warn("Failed to truncate WAL entry after positive ack")
		}
		updateTriggerWALDepthGauge()
		return
	}
	logFields["error_code"] = ack.GetErrorCode().String()
	if ack.GetRetryable() {
		TriggerAckOutcomes.WithLabelValues(triggerType, "retryable").Inc()
		p.logger.WithFields(logFields).Warn("Negative retryable ack; will retry on a later forwarder pass")
		p.failed(entry)
		return
	}
	TriggerAckOutcomes.WithLabelValues(triggerType, "non_retryable").Inc()
	logFields["error_message"] = ack.GetErrorMessage()
	p.logger.WithFields(logFields).Error("Non-retryable trigger ack; moving entry to dead-letter")
	if err := p.wal.DeadLetter(requestID); err != nil {
		p.logger.WithError(err).WithFields(logFields).Warn("Failed to dead-letter non-retryable WAL entry")
		p.failed(entry)
		return
	}
	clearTriggerRetry(requestID)
	updateTriggerWALDepthGauge()
}

func (p *triggerForwardPass) earliestDeadline() time.Time {
	var earliest time.Time
	for _, entry := range p.inflight {
		if earliest.IsZero() || entry.deadline.Before(earliest) {
			earliest = entry.deadline
		}
	}
	return earliest
}

func (p *triggerForwardPass) expire(now time.Time) {
	for requestID, entry := range p.inflight {
		if now.Before(entry.deadline) {
			continue
		}
		p.finish(requestID)
		TriggerAckOutcomes.WithLabelValues(entry.trigger.GetTriggerType(), "timeout").Inc()
		p.logger.WithFields(TriggerSummaryFields(entry.trigger, requestID)).Warn("Timed out waiting for trigger ack; will retry")
		p.failed(entry)
	}
}

// abandon ends every wait of this pass. The entries stay in the WAL; the next connection resends
// them under the same source_event_id, which every downstream consumer deduplicates on.
func (p *triggerForwardPass) abandon(outcome string) {
	for requestID, entry := range p.inflight {
		p.finish(requestID)
		TriggerAckOutcomes.WithLabelValues(entry.trigger.GetTriggerType(), outcome).Inc()
		p.logger.WithFields(TriggerSummaryFields(entry.trigger, requestID)).
			WithField("outcome", outcome).Warn("Trigger ack wait ended with the control connection; resending on the next connection")
	}
}

func (p *triggerForwardPass) release() {
	for requestID := range p.inflight {
		p.finish(requestID)
	}
}
