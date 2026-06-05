package control

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"frameworks/api_sidecar/internal/storage"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"

	"github.com/prometheus/client_golang/prometheus/testutil"
	"google.golang.org/grpc/metadata"
)

type fakeControlStream struct {
	sendMu  sync.Mutex
	sent    []*ipcpb.ControlMessage
	sendErr error
	sendCh  chan *ipcpb.ControlMessage
}

func (f *fakeControlStream) Send(msg *ipcpb.ControlMessage) error {
	f.sendMu.Lock()
	f.sent = append(f.sent, msg)
	if f.sendCh != nil {
		f.sendCh <- msg
	}
	err := f.sendErr
	f.sendMu.Unlock()
	return err
}

func (f *fakeControlStream) Recv() (*ipcpb.ControlMessage, error) {
	return nil, io.EOF
}

func (f *fakeControlStream) Header() (metadata.MD, error) {
	return metadata.MD{}, nil
}

func (f *fakeControlStream) Trailer() metadata.MD {
	return metadata.MD{}
}

func (f *fakeControlStream) CloseSend() error {
	return nil
}

func (f *fakeControlStream) Context() context.Context {
	return context.Background()
}

func (f *fakeControlStream) SendMsg(_ any) error {
	return nil
}

func (f *fakeControlStream) RecvMsg(_ any) error {
	return nil
}

func waitForTestDone(t *testing.T, ch <-chan struct{}, reason string) {
	t.Helper()

	select {
	case <-ch:
	case <-time.After(5 * time.Second):
		t.Fatalf("timeout waiting for %s", reason)
	}
}

func waitForControlMessage(t *testing.T, ch <-chan *ipcpb.ControlMessage, reason string) *ipcpb.ControlMessage {
	t.Helper()

	select {
	case msg := <-ch:
		return msg
	case <-time.After(5 * time.Second):
		t.Fatalf("timeout waiting for %s", reason)
		return nil
	}
}

func waitForMistTriggerResult(t *testing.T, ch <-chan *MistTriggerResult, reason string) *MistTriggerResult {
	t.Helper()

	select {
	case result := <-ch:
		return result
	case <-time.After(5 * time.Second):
		t.Fatalf("timeout waiting for %s", reason)
		return nil
	}
}

func waitForError(t *testing.T, ch <-chan error, reason string) error {
	t.Helper()

	select {
	case err := <-ch:
		return err
	case <-time.After(5 * time.Second):
		t.Fatalf("timeout waiting for %s", reason)
		return nil
	}
}

func TestSendDesiredStateResultPersistsBeforeSelfRestart(t *testing.T) {
	resetTestOutbox(t)
	t.Setenv("FRAMEWORKS_CONTROL_OUTBOX_DIR", t.TempDir())

	msg := &ipcpb.ControlMessage{
		RequestId: "self-update-1",
		Payload: &ipcpb.ControlMessage_UpdateApplyResult{UpdateApplyResult: &ipcpb.UpdateApplyResult{
			NodeId: "node-1",
		}},
	}
	shouldRestart := sendDesiredStateResult(msg, true, nil, func(*ipcpb.ControlMessage) error {
		return fmt.Errorf("stream closed")
	})
	if !shouldRestart {
		t.Fatal("expected self-restart after durable outbox write")
	}
	files, err := filepath.Glob(filepath.Join(os.Getenv("FRAMEWORKS_CONTROL_OUTBOX_DIR"), "*.pb"))
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 {
		t.Fatalf("expected one durable outbox file, got %d", len(files))
	}
	outboxMu.Lock()
	memoryLen := len(outbox)
	outboxMu.Unlock()
	if memoryLen != 0 {
		t.Fatalf("expected durable self-update result without memory duplicate, got %d memory messages", memoryLen)
	}

	stream := &fakeControlStream{}
	drainOutbox(stream)
	if len(stream.sent) != 1 {
		t.Fatalf("expected durable outbox drain to send one message, got %d", len(stream.sent))
	}
	files, err = filepath.Glob(filepath.Join(os.Getenv("FRAMEWORKS_CONTROL_OUTBOX_DIR"), "*.pb"))
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 0 {
		t.Fatalf("expected durable outbox file removed after drain, got %d", len(files))
	}
}

func TestSendDesiredStateResultDoesNotRestartWithoutDurableOutbox(t *testing.T) {
	resetTestOutbox(t)
	outboxFile := filepath.Join(t.TempDir(), "outbox-file")
	if err := os.WriteFile(outboxFile, []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("FRAMEWORKS_CONTROL_OUTBOX_DIR", outboxFile)

	msg := &ipcpb.ControlMessage{
		RequestId: "self-update-2",
		Payload: &ipcpb.ControlMessage_UpdateApplyResult{UpdateApplyResult: &ipcpb.UpdateApplyResult{
			NodeId: "node-1",
		}},
	}
	shouldRestart := sendDesiredStateResult(msg, true, nil, func(*ipcpb.ControlMessage) error {
		return fmt.Errorf("stream closed")
	})
	if shouldRestart {
		t.Fatal("self-restart should wait for a sent or durable update result")
	}
	outboxMu.Lock()
	memoryLen := len(outbox)
	outboxMu.Unlock()
	if memoryLen != 1 {
		t.Fatalf("expected memory retry after durable outbox failure, got %d messages", memoryLen)
	}
}

func resetTestOutbox(t *testing.T) {
	t.Helper()
	outboxMu.Lock()
	outbox = nil
	outboxMu.Unlock()
	t.Cleanup(func() {
		outboxMu.Lock()
		outbox = nil
		outboxMu.Unlock()
	})
}

func TestSendMistTriggerOnceStreamDisconnected(t *testing.T) {
	clearConn()

	triggerType := "test_disconnect"
	before := testutil.ToFloat64(TriggersSent.WithLabelValues(triggerType, "stream_disconnected"))

	err := sendMistTriggerOnce(triggerType, &ipcpb.MistTrigger{TriggerType: triggerType})
	if err == nil {
		t.Fatal("expected error for disconnected stream")
	}

	after := testutil.ToFloat64(TriggersSent.WithLabelValues(triggerType, "stream_disconnected"))
	if after != before+1 {
		t.Fatalf("expected metric increment by 1, got %v -> %v", before, after)
	}
}

func TestSendMistTriggerOnceSendError(t *testing.T) {
	storeConn(&fakeControlStream{sendErr: fmt.Errorf("send failed")}, "")

	triggerType := "test_send_error"
	before := testutil.ToFloat64(TriggersSent.WithLabelValues(triggerType, "send_error"))

	err := sendMistTriggerOnce(triggerType, &ipcpb.MistTrigger{TriggerType: triggerType})
	if err == nil {
		t.Fatal("expected error from send")
	}

	after := testutil.ToFloat64(TriggersSent.WithLabelValues(triggerType, "send_error"))
	if after != before+1 {
		t.Fatalf("expected metric increment by 1, got %v -> %v", before, after)
	}
}

func TestWaitForMistTriggerResponseTimeout(t *testing.T) {
	ch := make(chan *ipcpb.MistTriggerResponse, 1)
	pendingMutex <- struct{}{}
	pendingMistTriggers["timeout-test"] = ch
	<-pendingMutex

	result, err := awaitMistTriggerResponse(ch, "timeout-test", 20*time.Millisecond)
	if err == nil {
		t.Fatal("expected timeout error")
	}
	if result == nil || !result.Abort || result.ErrorCode != ipcpb.IngestErrorCode_INGEST_ERROR_TIMEOUT {
		t.Fatalf("unexpected result: %#v", result)
	}

	pendingMutex <- struct{}{}
	_, exists := pendingMistTriggers["timeout-test"]
	<-pendingMutex
	if exists {
		t.Fatal("expected pending trigger to be cleaned up after timeout")
	}
}

func TestWaitForMistTriggerResponseDisconnect(t *testing.T) {
	resetControlState(t)

	ch := make(chan *ipcpb.MistTriggerResponse, 1)
	pendingMutex <- struct{}{}
	pendingMistTriggers["disconnect-test"] = ch
	<-pendingMutex

	done := make(chan struct{})
	go func() {
		defer close(done)
		time.Sleep(10 * time.Millisecond)
		notifyDisconnect()
	}()

	result, err := awaitMistTriggerResponse(ch, "disconnect-test", 100*time.Millisecond)
	if err == nil || !errors.Is(err, errStreamDisconnected) {
		t.Fatalf("expected disconnect error, got %v", err)
	}
	if result == nil || !result.Abort || result.ErrorCode != ipcpb.IngestErrorCode_INGEST_ERROR_INTERNAL {
		t.Fatalf("unexpected result: %#v", result)
	}
	<-done
}

func TestDownloadToFileDiskFull(t *testing.T) {
	originalHasSpaceFor := hasSpaceFor
	hasSpaceFor = func(path string, requiredBytes uint64) error {
		return fmt.Errorf("disk full: %w", storage.ErrInsufficientSpace)
	}
	t.Cleanup(func() {
		hasSpaceFor = originalHasSpaceFor
	})

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Length", "4")
		_, _ = w.Write([]byte("data"))
	}))
	t.Cleanup(server.Close)

	dst := filepath.Join(t.TempDir(), "clip.mp4")
	err := downloadToFile(server.URL, dst)
	if err == nil {
		t.Fatal("expected disk full error")
	}
	if !storage.IsInsufficientSpace(err) {
		t.Fatalf("expected insufficient space error, got %v", err)
	}
	message := sanitizeStorageError(err)
	if message != "Download failed: storage node out of space" {
		t.Fatalf("unexpected error message: %s", message)
	}
}

func TestDownloadToFileRejectsTinyMistResponse(t *testing.T) {
	originalHasSpaceFor := hasSpaceFor
	hasSpaceFor = func(string, uint64) error { return nil }
	t.Cleanup(func() {
		hasSpaceFor = originalHasSpaceFor
	})

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		body := strings.Repeat("x", 44)
		w.Header().Set("Content-Length", fmt.Sprintf("%d", len(body)))
		w.Header().Set("Content-Type", "video/webm")
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(server.Close)

	dst := filepath.Join(t.TempDir(), "clip.mkv")
	err := downloadToFile(server.URL, dst)
	if err == nil {
		t.Fatal("expected tiny media response error")
	}
	if !strings.Contains(err.Error(), "too little media") {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, statErr := os.Stat(dst); !os.IsNotExist(statErr) {
		t.Fatalf("expected destination to be absent, stat error: %v", statErr)
	}
}

type controlState struct {
	conn                *streamConn
	blockingGraceMs     int
	streamReconnected   chan struct{}
	disconnectNotify    chan struct{}
	pendingMistTriggers map[string]chan *ipcpb.MistTriggerResponse
	pendingMutex        chan struct{}
}

func resetControlState(t *testing.T) {
	t.Helper()
	prev := controlState{
		conn:                activeConn.Load(),
		blockingGraceMs:     blockingGraceMs,
		streamReconnected:   streamReconnected,
		disconnectNotify:    disconnectNotify,
		pendingMistTriggers: pendingMistTriggers,
		pendingMutex:        pendingMutex,
	}

	t.Cleanup(func() {
		activeConn.Store(prev.conn)
		blockingGraceMs = prev.blockingGraceMs
		streamReconnectedM.Lock()
		streamReconnected = prev.streamReconnected
		streamReconnectedM.Unlock()
		disconnectNotifyMu.Lock()
		disconnectNotify = prev.disconnectNotify
		disconnectNotifyMu.Unlock()
		pendingMistTriggers = prev.pendingMistTriggers
		pendingMutex = prev.pendingMutex
	})

	clearConn()
	blockingGraceMs = 0
	streamReconnectedM.Lock()
	streamReconnected = make(chan struct{})
	streamReconnectedM.Unlock()
	disconnectNotifyMu.Lock()
	disconnectNotify = make(chan struct{})
	disconnectNotifyMu.Unlock()
	pendingMistTriggers = make(map[string]chan *ipcpb.MistTriggerResponse)
	pendingMutex = make(chan struct{}, 1)
}

func signalReconnect() {
	streamReconnectedM.Lock()
	close(streamReconnected)
	streamReconnected = make(chan struct{})
	streamReconnectedM.Unlock()
}

func waitForPendingTrigger(t *testing.T, requestID string) {
	t.Helper()
	deadline := time.Now().Add(200 * time.Millisecond)
	for time.Now().Before(deadline) {
		pendingMutex <- struct{}{}
		_, exists := pendingMistTriggers[requestID]
		<-pendingMutex
		if exists {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	pendingMutex <- struct{}{}
	_, exists := pendingMistTriggers[requestID]
	<-pendingMutex
	if !exists {
		t.Fatalf("pending trigger %s not registered", requestID)
	}
}

func TestWaitForReconnectionReturnsStream(t *testing.T) {
	resetControlState(t)

	stream := &fakeControlStream{}
	storeConn(stream, "")

	got := waitForReconnection(20 * time.Millisecond)
	if got != stream {
		t.Fatalf("expected current stream, got %#v", got)
	}
}

func TestWaitForReconnectionTimeout(t *testing.T) {
	resetControlState(t)

	got := waitForReconnection(20 * time.Millisecond)
	if got != nil {
		t.Fatalf("expected nil stream on timeout, got %#v", got)
	}
}

func TestSendMistTriggerReconnectsAndReceivesResponse(t *testing.T) {
	resetControlState(t)
	blockingGraceMs = 200

	stream := &fakeControlStream{sendCh: make(chan *ipcpb.ControlMessage, 1)}
	logger := logging.NewLogger()
	trigger := &ipcpb.MistTrigger{
		TriggerType: "stream_start",
		RequestId:   "req-reconnect",
		Blocking:    true,
	}

	go func() {
		time.Sleep(10 * time.Millisecond)
		storeConn(stream, "")
		signalReconnect()
	}()

	resultCh := make(chan *MistTriggerResult, 1)
	errCh := make(chan error, 1)
	go func() {
		r, e := SendMistTrigger(trigger, logger)
		resultCh <- r
		errCh <- e
	}()

	waitForControlMessage(t, stream.sendCh, "reconnected Mist trigger send")
	waitForPendingTrigger(t, trigger.RequestId)
	handleMistTriggerResponse(&ipcpb.MistTriggerResponse{
		RequestId: "req-reconnect",
		Response:  "ok",
	})

	result := waitForMistTriggerResult(t, resultCh, "Mist trigger result after reconnect")
	err := waitForError(t, errCh, "Mist trigger error after reconnect")
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if result.Response != "ok" {
		t.Fatalf("expected response ok, got %q", result.Response)
	}
	if len(stream.sent) != 1 {
		t.Fatalf("expected 1 send, got %d", len(stream.sent))
	}
}

func TestApplyJitterZeroPercent(t *testing.T) {
	d := 5 * time.Second
	got := applyJitter(d, 0)
	if got != d {
		t.Fatalf("expected %v, got %v", d, got)
	}
}

func TestApplyJitterNegativePercent(t *testing.T) {
	d := 5 * time.Second
	got := applyJitter(d, -10)
	if got != d {
		t.Fatalf("expected %v, got %v", d, got)
	}
}

func TestApplyJitterBounds(t *testing.T) {
	base := 10 * time.Second
	pct := 25
	minExpected := time.Duration(float64(base) * (1 - float64(pct)/100))
	maxExpected := time.Duration(float64(base) * (1 + float64(pct)/100))

	for i := 0; i < 100; i++ {
		got := applyJitter(base, pct)
		if got < minExpected || got > maxExpected {
			t.Fatalf("iteration %d: %v outside [%v, %v]", i, got, minExpected, maxExpected)
		}
	}
}

func TestSendArtifactDeletedDisconnected(t *testing.T) {
	clearConn()
	err := SendArtifactDeleted("hash-1", "/path/file", "manual", "clip", 1024)
	if err == nil {
		t.Fatal("expected error for disconnected stream")
	}
}

func TestSendFreezeProgressDisconnected(t *testing.T) {
	clearConn()
	err := SendFreezeProgress("req-1", "hash-1", 50, 1024)
	if err == nil {
		t.Fatal("expected error for disconnected stream")
	}
}

func TestSendStorageLifecycleDisconnected(t *testing.T) {
	clearConn()
	err := SendStorageLifecycle(&ipcpb.StorageLifecycleData{})
	if err == nil {
		t.Fatal("expected error for disconnected stream")
	}
}

func TestSendProcessBillingEventDisconnected(t *testing.T) {
	clearConn()
	err := SendProcessBillingEvent(&ipcpb.ProcessBillingEvent{ProcessType: "test_disconnect"})
	if err == nil {
		t.Fatal("expected error for disconnected stream")
	}
}

func TestSendMistTriggerRetriesAfterDisconnect(t *testing.T) {
	resetControlState(t)

	stream1 := &fakeControlStream{sendCh: make(chan *ipcpb.ControlMessage, 1)}
	stream2 := &fakeControlStream{sendCh: make(chan *ipcpb.ControlMessage, 1)}
	storeConn(stream1, "")
	logger := logging.NewLogger()

	trigger := &ipcpb.MistTrigger{
		TriggerType: "stream_stop",
		RequestId:   "req-retry",
		Blocking:    true,
	}

	hook := make(chan struct{}, 1)
	disconnectSubscribedHook = hook
	t.Cleanup(func() { disconnectSubscribedHook = nil })

	resultCh := make(chan *MistTriggerResult, 1)
	errCh := make(chan error, 1)
	go func() {
		result, err := SendMistTrigger(trigger, logger)
		resultCh <- result
		errCh <- err
	}()

	waitForControlMessage(t, stream1.sendCh, "initial Mist trigger send")
	waitForPendingTrigger(t, trigger.RequestId)

	select {
	case <-hook:
		// ok
	case <-time.After(1 * time.Second):
		t.Fatal("timeout waiting for disconnect subscription")
	}

	notifyDisconnect()

	storeConn(stream2, "")
	signalReconnect()

	waitForControlMessage(t, stream2.sendCh, "retried Mist trigger send after reconnect")
	handleMistTriggerResponse(&ipcpb.MistTriggerResponse{
		RequestId: trigger.RequestId,
		Response:  "ack",
	})

	result := waitForMistTriggerResult(t, resultCh, "Mist trigger retry result")
	err := waitForError(t, errCh, "Mist trigger retry error")
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if result.Response != "ack" {
		t.Fatalf("expected response ack, got %q", result.Response)
	}
	if len(stream1.sent) != 1 {
		t.Fatalf("expected 1 send on first stream, got %d", len(stream1.sent))
	}
	if len(stream2.sent) != 1 {
		t.Fatalf("expected 1 send on second stream, got %d", len(stream2.sent))
	}

	pendingMutex <- struct{}{}
	pendingCount := len(pendingMistTriggers)
	<-pendingMutex
	if pendingCount != 0 {
		t.Fatalf("expected pending triggers cleaned up, found %d", pendingCount)
	}
}
