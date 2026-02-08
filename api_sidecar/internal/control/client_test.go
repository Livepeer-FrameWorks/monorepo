package control

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"frameworks/api_sidecar/internal/storage"
	"frameworks/pkg/logging"
	pb "frameworks/pkg/proto"

	"github.com/prometheus/client_golang/prometheus/testutil"
	"google.golang.org/grpc/metadata"
)

type fakeControlStream struct {
	sendMu  sync.Mutex
	sent    []*pb.ControlMessage
	sendErr error
	sendCh  chan *pb.ControlMessage
}

func (f *fakeControlStream) Send(msg *pb.ControlMessage) error {
	f.sendMu.Lock()
	f.sent = append(f.sent, msg)
	if f.sendCh != nil {
		f.sendCh <- msg
	}
	err := f.sendErr
	f.sendMu.Unlock()
	return err
}

func (f *fakeControlStream) Recv() (*pb.ControlMessage, error) {
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

func TestSendMistTriggerOnceStreamDisconnected(t *testing.T) {
	clearConn()

	triggerType := "test_disconnect"
	before := testutil.ToFloat64(TriggersSent.WithLabelValues(triggerType, "stream_disconnected"))

	err := sendMistTriggerOnce(triggerType, &pb.MistTrigger{TriggerType: triggerType})
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

	err := sendMistTriggerOnce(triggerType, &pb.MistTrigger{TriggerType: triggerType})
	if err == nil {
		t.Fatal("expected error from send")
	}

	after := testutil.ToFloat64(TriggersSent.WithLabelValues(triggerType, "send_error"))
	if after != before+1 {
		t.Fatalf("expected metric increment by 1, got %v -> %v", before, after)
	}
}

func TestWaitForMistTriggerResponseTimeout(t *testing.T) {
	result, err := waitForMistTriggerResponseWithDisconnect("timeout-test", 20*time.Millisecond)
	if err == nil {
		t.Fatal("expected timeout error")
	}
	if result == nil || !result.Abort || result.ErrorCode != pb.IngestErrorCode_INGEST_ERROR_TIMEOUT {
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

	done := make(chan struct{})
	go func() {
		defer close(done)
		time.Sleep(10 * time.Millisecond)
		notifyDisconnect()
	}()

	result, err := waitForMistTriggerResponseWithDisconnect("disconnect-test", 100*time.Millisecond)
	if err == nil || !errors.Is(err, errStreamDisconnected) {
		t.Fatalf("expected disconnect error, got %v", err)
	}
	if result == nil || !result.Abort || result.ErrorCode != pb.IngestErrorCode_INGEST_ERROR_INTERNAL {
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

type controlState struct {
	conn                *streamConn
	blockingGraceMs     int
	streamReconnected   chan struct{}
	disconnectNotify    chan struct{}
	pendingMistTriggers map[string]chan *pb.MistTriggerResponse
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
	pendingMistTriggers = make(map[string]chan *pb.MistTriggerResponse)
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

	stream := &fakeControlStream{sendCh: make(chan *pb.ControlMessage, 1)}
	logger := logging.NewLogger()
	trigger := &pb.MistTrigger{
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

	<-stream.sendCh
	waitForPendingTrigger(t, trigger.RequestId)
	handleMistTriggerResponse(&pb.MistTriggerResponse{
		RequestId: "req-reconnect",
		Response:  "ok",
	})

	result := <-resultCh
	err := <-errCh
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

func TestSendDVRStartRequestDisconnected(t *testing.T) {
	clearConn()
	err := SendDVRStartRequest("tenant-1", "stream-1", "user-1", 7, "mp4", 10)
	if err == nil {
		t.Fatal("expected error for disconnected stream")
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

func TestSendDefrostProgressDisconnected(t *testing.T) {
	clearConn()
	err := SendDefrostProgress("req-1", "hash-1", 50, 1024, 5, 10, "downloading")
	if err == nil {
		t.Fatal("expected error for disconnected stream")
	}
}

func TestSendStorageLifecycleDisconnected(t *testing.T) {
	clearConn()
	err := SendStorageLifecycle(&pb.StorageLifecycleData{})
	if err == nil {
		t.Fatal("expected error for disconnected stream")
	}
}

func TestSendProcessBillingEventDisconnected(t *testing.T) {
	clearConn()
	err := SendProcessBillingEvent(&pb.ProcessBillingEvent{ProcessType: "test_disconnect"})
	if err == nil {
		t.Fatal("expected error for disconnected stream")
	}
}

func TestSendMistTriggerRetriesAfterDisconnect(t *testing.T) {
	resetControlState(t)

	stream1 := &fakeControlStream{sendCh: make(chan *pb.ControlMessage, 1)}
	stream2 := &fakeControlStream{sendCh: make(chan *pb.ControlMessage, 1)}
	storeConn(stream1, "")
	logger := logging.NewLogger()

	trigger := &pb.MistTrigger{
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

	<-stream1.sendCh
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

	<-stream2.sendCh
	handleMistTriggerResponse(&pb.MistTriggerResponse{
		RequestId: trigger.RequestId,
		Response:  "ack",
	})

	result := <-resultCh
	err := <-errCh
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
