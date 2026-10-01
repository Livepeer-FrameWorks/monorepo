package control

import (
	"strings"
	"sync"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/mist"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
	"github.com/google/uuid"
)

// A PUSH_REWRITE Helmsman forwarded can be admitted by Foghorn while Helmsman answers Mist with
// something else: the answer arrived after Helmsman stopped waiting, the control stream dropped
// after Foghorn committed, or Helmsman could not persist the accepted generation. Mist refuses the
// publisher on any answer other than an accept, so such an admission is an open session with no
// publisher. The session holds the stream, its placement claim, and its tenant capacity, and a
// reconnecting encoder is refused as a duplicate of it. Helmsman therefore reports every execution
// it answered without an accept after forwarding it, and Foghorn ends what the execution minted
// (AbandonIngestAdmission).
//
// The report is sent only once Mist can no longer deliver the execution again. An answer Mist
// received ends Mist's delivery loop, so it is reported at once. An answer Mist did not receive
// (the HTTP request was gone) leaves Mist free to deliver the same execution again, possibly to an
// accept; that case is reported after the execution's Mist lifetime, unless an accept for it
// reached Mist in the meantime.

// abandonedAdmissionLifetimeMargin is added to Mist's blocking-trigger lifetime before an
// unanswered execution is reported, so the last delivery's forward has finished.
const abandonedAdmissionLifetimeMargin = 5 * time.Second

var (
	// reportAbandonedPushAdmission queues the durable report; tests observe it.
	reportAbandonedPushAdmission = sendAbandonedPushAdmission
	// scheduleAbandonedAdmissionCheck runs check after delay; tests run it synchronously.
	scheduleAbandonedAdmissionCheck = func(delay time.Duration, check func()) { time.AfterFunc(delay, check) }
	abandonedAdmissionNow           = time.Now
)

// acceptedPushExecutions records the PUSH_REWRITE executions whose accept reached Mist, keyed by
// trigger UUID, for the deferred check of an execution Mist may have delivered again.
var acceptedPushExecutions = struct {
	sync.Mutex
	at map[string]time.Time
}{at: make(map[string]time.Time)}

// SettlePushRewriteAnswer runs after Helmsman answered a PUSH_REWRITE to Mist. relayedAccept is
// whether that answer admitted the publisher; delivered is whether Mist's request was still open
// when the answer was written, so Mist received it.
func SettlePushRewriteAnswer(mistTrigger *ipcpb.MistTrigger, result *MistTriggerResult, relayedAccept, delivered bool, logger logging.Logger) {
	pushRewrite := mistTrigger.GetPushRewrite()
	triggerUUID := strings.TrimSpace(pushRewrite.GetTriggerUuid())
	if pushRewrite == nil || triggerUUID == "" || pushRewrite.GetPid() <= 0 {
		return
	}
	if relayedAccept && delivered {
		rememberAcceptedPushExecution(triggerUUID)
		return
	}
	if result == nil || !result.Forwarded {
		// Foghorn never received the execution, so it admitted nothing.
		return
	}
	if !relayedAccept && foghornDecidedDenial(result) {
		// Foghorn denied the execution itself and released what it had minted.
		return
	}
	if delivered {
		reportAbandonedPushAdmissionLogged(mistTrigger, logger)
		return
	}
	firedAt := time.UnixMilli(pushRewrite.GetTriggerUnixMillis())
	if pushRewrite.GetTriggerUnixMillis() <= 0 {
		firedAt = abandonedAdmissionNow()
	}
	delay := max(time.Until(firedAt.Add(mist.BlockingTriggerLifetime+abandonedAdmissionLifetimeMargin)), 0)
	scheduleAbandonedAdmissionCheck(delay, func() {
		if pushExecutionAccepted(triggerUUID) {
			return
		}
		reportAbandonedPushAdmissionLogged(mistTrigger, logger)
	})
}

// foghornDecidedDenial reports whether result is Foghorn's own refusal of the execution. Timeouts
// and internal failures, including Helmsman refusing an accept it could not persist, leave the
// admission's state unknown.
func foghornDecidedDenial(result *MistTriggerResult) bool {
	if !result.Abort {
		return false
	}
	switch result.ErrorCode {
	case ipcpb.IngestErrorCode_INGEST_ERROR_INTERNAL, ipcpb.IngestErrorCode_INGEST_ERROR_TIMEOUT:
		return false
	}
	return result.IngestGeneration == ""
}

func reportAbandonedPushAdmissionLogged(mistTrigger *ipcpb.MistTrigger, logger logging.Logger) {
	pushRewrite := mistTrigger.GetPushRewrite()
	fields := logging.Fields{
		"trigger_uuid":  pushRewrite.GetTriggerUuid(),
		"connector_pid": pushRewrite.GetPid(),
	}
	if err := reportAbandonedPushAdmission(mistTrigger); err != nil {
		if logger != nil {
			logger.WithError(err).WithFields(fields).Error("Failed to queue the report of a PUSH_REWRITE answered without an accept; Foghorn keeps its session until the node re-registers")
		}
		return
	}
	if logger != nil {
		logger.WithFields(fields).Warn("PUSH_REWRITE answered without an accept after it was forwarded; reporting it so Foghorn ends what it admitted")
	}
}

func sendAbandonedPushAdmission(mistTrigger *ipcpb.MistTrigger) error {
	return SendDurableMistTrigger(buildIngestAdmissionAbandonedTrigger(mistTrigger, abandonedAdmissionNow()))
}

// buildIngestAdmissionAbandonedTrigger builds the durable report. Its request id is derived from the
// node and the execution, so a repeated report is one WAL entry.
func buildIngestAdmissionAbandonedTrigger(mistTrigger *ipcpb.MistTrigger, at time.Time) *ipcpb.MistTrigger {
	pushRewrite := mistTrigger.GetPushRewrite()
	nodeID := mistTrigger.GetNodeId()
	triggerUUID := strings.TrimSpace(pushRewrite.GetTriggerUuid())
	return &ipcpb.MistTrigger{
		TriggerType:       string(mist.TriggerIngestAdmissionAbandoned),
		NodeId:            nodeID,
		Timestamp:         at.Unix(),
		RequestId:         uuid.NewSHA1(uuid.NameSpaceURL, []byte("frameworks:ingest-admission-abandoned:"+nodeID+":"+triggerUUID)).String(),
		TriggerUnixMillis: at.UnixMilli(),
		TriggerPayload: &ipcpb.MistTrigger_IngestAdmissionAbandoned{IngestAdmissionAbandoned: &ipcpb.IngestAdmissionAbandoned{
			TriggerUuid:  triggerUUID,
			ConnectorPid: pushRewrite.GetPid(),
		}},
	}
}

func rememberAcceptedPushExecution(triggerUUID string) {
	now := abandonedAdmissionNow()
	acceptedPushExecutions.Lock()
	defer acceptedPushExecutions.Unlock()
	// Entries are needed only until the deferred check of the same execution has run.
	horizon := 2 * (mist.BlockingTriggerLifetime + abandonedAdmissionLifetimeMargin)
	for id, at := range acceptedPushExecutions.at {
		if now.Sub(at) > horizon {
			delete(acceptedPushExecutions.at, id)
		}
	}
	acceptedPushExecutions.at[triggerUUID] = now
}

func pushExecutionAccepted(triggerUUID string) bool {
	acceptedPushExecutions.Lock()
	defer acceptedPushExecutions.Unlock()
	_, ok := acceptedPushExecutions.at[triggerUUID]
	return ok
}
