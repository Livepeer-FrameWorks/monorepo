package control

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/mist"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
)

func pushRewriteForSettle(triggerUUID string, pid int64, firedAt time.Time) *ipcpb.MistTrigger {
	return &ipcpb.MistTrigger{
		TriggerType: string(mist.TriggerPushRewrite),
		NodeId:      "edge-1",
		Blocking:    true,
		RequestId:   "request-" + triggerUUID,
		TriggerPayload: &ipcpb.MistTrigger_PushRewrite{PushRewrite: &ipcpb.PushRewriteTrigger{
			StreamName: "stream-key", Pid: pid, TriggerUuid: triggerUUID, TriggerUnixMillis: firedAt.UnixMilli(),
		}},
	}
}

func resetAcceptedPushExecutions(t *testing.T) {
	t.Helper()
	acceptedPushExecutions.Lock()
	prev := acceptedPushExecutions.at
	acceptedPushExecutions.at = make(map[string]time.Time)
	acceptedPushExecutions.Unlock()
	t.Cleanup(func() {
		acceptedPushExecutions.Lock()
		acceptedPushExecutions.at = prev
		acceptedPushExecutions.Unlock()
	})
}

// captureDeferredAdmissionChecks holds deferred checks until the test runs them.
func captureDeferredAdmissionChecks(t *testing.T) *[]func() {
	t.Helper()
	var checks []func()
	prev := scheduleAbandonedAdmissionCheck
	scheduleAbandonedAdmissionCheck = func(_ time.Duration, check func()) { checks = append(checks, check) }
	t.Cleanup(func() { scheduleAbandonedAdmissionCheck = prev })
	return &checks
}

func pendingAbandonments(t *testing.T) []*ipcpb.IngestAdmissionAbandoned {
	t.Helper()
	pending, err := triggerWAL.Pending()
	if err != nil {
		t.Fatalf("read WAL: %v", err)
	}
	var reports []*ipcpb.IngestAdmissionAbandoned
	for _, trigger := range pending {
		if trigger.GetTriggerType() != string(mist.TriggerIngestAdmissionAbandoned) {
			t.Fatalf("unexpected WAL entry %q", trigger.GetTriggerType())
		}
		reports = append(reports, trigger.GetIngestAdmissionAbandoned())
	}
	return reports
}

// Helmsman answered Mist 503 after forwarding the execution: Mist refuses the publisher and never
// delivers the execution again, so the abandonment is queued durably at once, once per execution.
func TestSettlePushRewriteAnswer_DeliveredNonAcceptReportsExecutionDurably(t *testing.T) {
	withTestTriggerWAL(t)
	resetAcceptedPushExecutions(t)
	checks := captureDeferredAdmissionChecks(t)
	trigger := pushRewriteForSettle("exec-timeout", 77, time.Now())

	timedOut := &MistTriggerResult{Abort: true, ErrorCode: ipcpb.IngestErrorCode_INGEST_ERROR_TIMEOUT, Forwarded: true}
	SettlePushRewriteAnswer(trigger, timedOut, false, true, logging.NewLogger())
	SettlePushRewriteAnswer(trigger, timedOut, false, true, logging.NewLogger())

	reports := pendingAbandonments(t)
	if len(reports) != 1 || reports[0].GetTriggerUuid() != "exec-timeout" || reports[0].GetConnectorPid() != 77 {
		t.Fatalf("abandonment reports = %+v, want exactly one for exec-timeout pid 77", reports)
	}
	if len(*checks) != 0 {
		t.Fatalf("a delivered answer scheduled %d deferred checks", len(*checks))
	}
}

// Helmsman refused an accept it could not persist: Foghorn's session is active and Mist gets a 503.
func TestSettlePushRewriteAnswer_LocallyRefusedAcceptIsReported(t *testing.T) {
	withTestTriggerWAL(t)
	resetAcceptedPushExecutions(t)
	refused := &MistTriggerResult{Abort: true, ErrorCode: ipcpb.IngestErrorCode_INGEST_ERROR_INTERNAL, IngestGeneration: "gen-1", Forwarded: true}
	SettlePushRewriteAnswer(pushRewriteForSettle("exec-refused", 78, time.Now()), refused, false, true, logging.NewLogger())
	if reports := pendingAbandonments(t); len(reports) != 1 || reports[0].GetTriggerUuid() != "exec-refused" {
		t.Fatalf("abandonment reports = %+v, want one for exec-refused", reports)
	}
}

// Nothing is reported for an execution Foghorn never received, one Foghorn itself denied for a
// business reason, or one whose accept reached Mist.
func TestSettlePushRewriteAnswer_SettledExecutionsAreNotReported(t *testing.T) {
	withTestTriggerWAL(t)
	resetAcceptedPushExecutions(t)
	checks := captureDeferredAdmissionChecks(t)
	logger := logging.NewLogger()

	SettlePushRewriteAnswer(pushRewriteForSettle("exec-unsent", 1, time.Now()),
		&MistTriggerResult{Abort: true, ErrorCode: ipcpb.IngestErrorCode_INGEST_ERROR_TIMEOUT}, false, true, logger)
	SettlePushRewriteAnswer(pushRewriteForSettle("exec-duplicate", 2, time.Now()),
		&MistTriggerResult{Abort: true, ErrorCode: ipcpb.IngestErrorCode_INGEST_ERROR_DUPLICATE_INGEST, Forwarded: true}, false, true, logger)
	SettlePushRewriteAnswer(pushRewriteForSettle("exec-accepted", 3, time.Now()),
		&MistTriggerResult{Response: "live+s", IngestGeneration: "gen-3", Forwarded: true}, true, true, logger)

	if reports := pendingAbandonments(t); len(reports) != 0 {
		t.Fatalf("settled executions were reported: %+v", reports)
	}
	if len(*checks) != 0 {
		t.Fatalf("settled executions scheduled %d deferred checks", len(*checks))
	}
	if !pushExecutionAccepted("exec-accepted") {
		t.Fatal("a delivered accept was not remembered for the deferred check")
	}
}

// When Mist's request was gone, Mist may deliver the same execution again and admit it. The report
// waits for the execution's Mist lifetime and is dropped when an accept for it reached Mist.
func TestSettlePushRewriteAnswer_UndeliveredAnswerWaitsForMistLifetime(t *testing.T) {
	withTestTriggerWAL(t)
	resetAcceptedPushExecutions(t)
	var delays []time.Duration
	var checks []func()
	prev := scheduleAbandonedAdmissionCheck
	scheduleAbandonedAdmissionCheck = func(delay time.Duration, check func()) {
		delays = append(delays, delay)
		checks = append(checks, check)
	}
	t.Cleanup(func() { scheduleAbandonedAdmissionCheck = prev })
	logger := logging.NewLogger()
	firedAt := time.Now()
	gone := &MistTriggerResult{Abort: true, ErrorCode: ipcpb.IngestErrorCode_INGEST_ERROR_TIMEOUT, Forwarded: true}

	SettlePushRewriteAnswer(pushRewriteForSettle("exec-redelivered", 10, firedAt), gone, false, false, logger)
	SettlePushRewriteAnswer(pushRewriteForSettle("exec-lost", 11, firedAt), gone, false, false, logger)
	if reports := pendingAbandonments(t); len(reports) != 0 {
		t.Fatalf("an undelivered answer was reported before Mist's lifetime: %+v", reports)
	}
	if len(checks) != 2 {
		t.Fatalf("deferred checks = %d, want 2", len(checks))
	}
	if minDelay := mist.BlockingTriggerLifetime; delays[0] < minDelay-time.Second {
		t.Fatalf("deferred check delay %v is shorter than Mist's blocking-trigger lifetime %v", delays[0], minDelay)
	}

	// Mist delivered exec-redelivered again and its accept reached Mist.
	SettlePushRewriteAnswer(pushRewriteForSettle("exec-redelivered", 10, firedAt),
		&MistTriggerResult{Response: "live+s", IngestGeneration: "gen-10", Forwarded: true}, true, true, logger)
	for _, check := range checks {
		check()
	}
	reports := pendingAbandonments(t)
	if len(reports) != 1 || reports[0].GetTriggerUuid() != "exec-lost" {
		t.Fatalf("abandonment reports = %+v, want only exec-lost", reports)
	}
}

func TestBuildIngestAdmissionAbandonedTrigger_IsDurableAndStablePerExecution(t *testing.T) {
	trigger := pushRewriteForSettle("exec-stable", 5, time.Now())
	first := buildIngestAdmissionAbandonedTrigger(trigger, time.Now())
	second := buildIngestAdmissionAbandonedTrigger(trigger, time.Now().Add(time.Minute))
	if !mist.IsDurableTriggerType(first.GetTriggerType()) {
		t.Fatalf("report type %q is not durable", first.GetTriggerType())
	}
	if first.GetRequestId() == "" || first.GetRequestId() != second.GetRequestId() {
		t.Fatalf("request ids %q and %q, want one stable id per execution", first.GetRequestId(), second.GetRequestId())
	}
	other := buildIngestAdmissionAbandonedTrigger(pushRewriteForSettle("exec-other", 5, time.Now()), time.Now())
	if other.GetRequestId() == first.GetRequestId() {
		t.Fatal("different executions share a report id")
	}
	if first.GetBlocking() {
		t.Fatal("the report must travel through the WAL, not as a blocking trigger")
	}
}

// Foghorn's accept arrives while the waiter's deadline expires: the response handler has already
// taken the request and persisted the generation, so the waiter must relay that accept rather than
// answer Mist with a timeout for a publisher its node has admitted.
func TestAwaitMistTriggerResponse_DeadlineRelaysAcceptAlreadyTaken(t *testing.T) {
	resetControlState(t)
	const requestID, runtime = "claimed-push", "live+claimed-at-deadline"
	responseCh := make(chan *ipcpb.MistTriggerResponse, 1)
	pendingMutex <- struct{}{}
	pendingMistTriggers[requestID] = pendingMistTrigger{responseCh: responseCh, triggerType: string(mist.TriggerPushRewrite)}
	<-pendingMutex

	// Holding the runtime's generation fence parks the response handler after it took the request.
	fence, _ := lockIngestFence(runtime, true)
	handled := make(chan struct{})
	go func() {
		defer close(handled)
		handleMistTriggerResponse(&ipcpb.MistTriggerResponse{
			RequestId: requestID, Response: runtime, Action: ipcpb.MistTriggerAction_MIST_TRIGGER_ACTION_VALUE,
			IngestGeneration: "gen-claimed", IngestConnectorPid: 91,
		})
	}()
	waitUntil(t, func() bool {
		pendingMutex <- struct{}{}
		_, pending := pendingMistTriggers[requestID]
		<-pendingMutex
		return !pending
	})
	go func() {
		time.Sleep(50 * time.Millisecond)
		fence.Unlock()
	}()

	result, err := awaitMistTriggerResponse(responseCh, requestID, 5*time.Millisecond)
	<-handled
	if err != nil || result.Abort || result.IngestGeneration != "gen-claimed" {
		t.Fatalf("result=%+v err=%v, want the accept the handler persisted", result, err)
	}
}

func waitUntil(t *testing.T, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for !condition() {
		if time.Now().After(deadline) {
			t.Fatal("condition not reached")
		}
		time.Sleep(time.Millisecond)
	}
}

// Forwarded distinguishes an execution Foghorn may have acted on from one that never left Helmsman.
func TestSendMistTriggerContext_ReportsWhetherThePushWasForwarded(t *testing.T) {
	resetControlState(t)
	stream := &fakeControlStream{sendCh: make(chan *ipcpb.ControlMessage, 4)}
	storeConn(stream, "edge-1")
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	result, err := SendMistTriggerContext(ctx, pushRewriteForSettle("exec-forwarded", 21, time.Now()), logging.NewLogger())
	if err == nil || result == nil || !result.Forwarded {
		t.Fatalf("unanswered forward: result=%+v err=%v, want Forwarded", result, err)
	}

	failing := &fakeControlStream{sendErr: errors.New("stream broken")}
	storeConn(failing, "edge-1")
	ctx2, cancel2 := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel2()
	result, err = SendMistTriggerContext(ctx2, pushRewriteForSettle("exec-unsent", 22, time.Now()), logging.NewLogger())
	if err == nil || result == nil || result.Forwarded {
		t.Fatalf("failed send: result=%+v err=%v, want not Forwarded", result, err)
	}
}
