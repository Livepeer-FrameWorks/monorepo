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
	"github.com/Livepeer-FrameWorks/monorepo/pkg/mist"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"

	"github.com/prometheus/client_golang/prometheus/testutil"
	"google.golang.org/grpc/metadata"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

type fakeControlStream struct {
	sendMu    sync.Mutex
	sent      []*ipcpb.ControlMessage
	sendErr   error
	sendCh    chan *ipcpb.ControlMessage
	sendCtx   context.Context
	sendBlock <-chan struct{}
}

func (f *fakeControlStream) Send(msg *ipcpb.ControlMessage) error {
	f.sendMu.Lock()
	f.sent = append(f.sent, msg)
	if f.sendCh != nil {
		f.sendCh <- msg
	}
	if f.sendBlock != nil {
		<-f.sendBlock
	}
	if f.sendCtx != nil {
		<-f.sendCtx.Done()
	}
	err := f.sendErr
	f.sendMu.Unlock()
	return err
}

func TestLockedClientStreamTransportDeadlineBreaksWedgedSend(t *testing.T) {
	oldTimeout := controlStreamSendTimeout
	controlStreamSendTimeout = 20 * time.Millisecond
	t.Cleanup(func() { controlStreamSendTimeout = oldTimeout })

	streamCtx, cancelStream := context.WithCancel(context.Background())
	fake := &fakeControlStream{sendCtx: streamCtx, sendErr: context.Canceled}
	stream := &lockedClientStream{HelmsmanControl_ConnectClient: fake, cancel: cancelStream}

	done := make(chan error, 1)
	go func() { done <- stream.Send(&ipcpb.ControlMessage{}) }()

	select {
	case err := <-done:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("send error = %v, want transport deadline", err)
		}
	case <-time.After(time.Second):
		t.Fatal("transport deadline did not cancel the owning control stream")
	}
	if streamCtx.Err() == nil {
		t.Fatal("owning control stream remained live after a wedged send deadline")
	}
}

func TestControlStreamWatchdogOutlivesOrdinarySendBudget(t *testing.T) {
	if controlStreamSendTimeout <= mistTriggerTransportSendTimeout {
		t.Fatalf(
			"control stream watchdog = %v, want greater than ordinary send budget %v",
			controlStreamSendTimeout,
			mistTriggerTransportSendTimeout,
		)
	}
}

func TestLockedClientStreamSuccessfulSendWinsTimeoutRace(t *testing.T) {
	oldTimeout := controlStreamSendTimeout
	controlStreamSendTimeout = 20 * time.Millisecond
	t.Cleanup(func() { controlStreamSendTimeout = oldTimeout })

	streamCtx, cancelStream := context.WithCancel(context.Background())
	fake := &fakeControlStream{sendCtx: streamCtx}
	stream := &lockedClientStream{HelmsmanControl_ConnectClient: fake, cancel: cancelStream}

	if err := stream.Send(&ipcpb.ControlMessage{}); err != nil {
		t.Fatalf("successful send reported timeout and would be replayed: %v", err)
	}
}

func TestLockedClientStreamCallerCancellationDoesNotBreakOwnedSend(t *testing.T) {
	oldTimeout := controlStreamSendTimeout
	controlStreamSendTimeout = time.Second
	t.Cleanup(func() { controlStreamSendTimeout = oldTimeout })

	t.Run("explicit cancellation", func(t *testing.T) {
		streamCtx, cancelStream := context.WithCancel(context.Background())
		release := make(chan struct{})
		fake := &fakeControlStream{
			sendCh:    make(chan *ipcpb.ControlMessage, 1),
			sendBlock: release,
		}
		stream := &lockedClientStream{HelmsmanControl_ConnectClient: fake, cancel: cancelStream}
		callerCtx, cancelCaller := context.WithCancel(context.Background())
		defer cancelCaller()
		done := make(chan error, 1)
		go func() { done <- stream.SendContext(callerCtx, &ipcpb.ControlMessage{}) }()

		select {
		case <-fake.sendCh:
		case <-time.After(time.Second):
			t.Fatal("send did not acquire the transport lane")
		}
		cancelCaller()
		select {
		case <-streamCtx.Done():
			t.Fatal("caller cancellation tore down the owned transport send")
		case err := <-done:
			t.Fatalf("send returned before the transport completed: %v", err)
		case <-time.After(40 * time.Millisecond):
		}

		close(release)
		select {
		case err := <-done:
			if err != nil {
				t.Fatalf("completed send error = %v", err)
			}
		case <-time.After(time.Second):
			t.Fatal("send did not complete after transport release")
		}
		if streamCtx.Err() != nil {
			t.Fatal("healthy transport was cancelled")
		}
	})
}

func TestLockedClientStreamCallerDeadlineAbandonsOwnedSendWithoutRecyclingTransport(t *testing.T) {
	oldTimeout := controlStreamSendTimeout
	controlStreamSendTimeout = time.Second
	t.Cleanup(func() { controlStreamSendTimeout = oldTimeout })

	streamCtx, cancelStream := context.WithCancel(context.Background())
	release := make(chan struct{})
	fake := &fakeControlStream{sendBlock: release}
	stream := &lockedClientStream{HelmsmanControl_ConnectClient: fake, cancel: cancelStream}
	callerCtx, cancelCaller := context.WithTimeout(context.Background(), 2*time.Millisecond)
	defer cancelCaller()

	err := stream.SendContext(callerCtx, &ipcpb.ControlMessage{})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("send error = %v, want caller deadline", err)
	}
	if streamCtx.Err() != nil {
		t.Fatal("caller deadline recycled the shared transport")
	}
	close(release)
	secondDone := make(chan error, 1)
	go func() { secondDone <- stream.Send(&ipcpb.ControlMessage{}) }()
	select {
	case err := <-secondDone:
		if err != nil {
			t.Fatalf("send after abandoned owner failed: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("abandoned owner did not release the send lane after transport completion")
	}
	if streamCtx.Err() != nil {
		t.Fatal("healthy transport was recycled after the abandoned send completed")
	}
}

func TestLockedClientStreamWaiterDeadlineDoesNotCancelHealthyOwner(t *testing.T) {
	streamCtx, cancelStream := context.WithCancel(context.Background())
	fake := &fakeControlStream{sendCtx: streamCtx, sendCh: make(chan *ipcpb.ControlMessage, 1)}
	stream := &lockedClientStream{HelmsmanControl_ConnectClient: fake, cancel: cancelStream}

	ownerDone := make(chan error, 1)
	go func() { ownerDone <- stream.Send(&ipcpb.ControlMessage{}) }()
	select {
	case <-fake.sendCh:
	case <-time.After(time.Second):
		t.Fatal("first send did not acquire the transport lane")
	}

	waitCtx, cancelWait := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancelWait()
	if err := stream.SendContext(waitCtx, &ipcpb.ControlMessage{}); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("waiting send error = %v, want deadline exceeded", err)
	}
	if streamCtx.Err() != nil {
		t.Fatal("a deadline waiting for the send lane cancelled the healthy owner")
	}

	cancelStream()
	select {
	case <-ownerDone:
	case <-time.After(time.Second):
		t.Fatal("first send did not exit after test cleanup")
	}
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
	testConn := &streamConn{stream: stream, epoch: "test"}
	publishConn(testConn)
	drainOutbox(testConn)
	if len(stream.sent) != 1 {
		t.Fatalf("expected durable outbox drain to send one message, got %d", len(stream.sent))
	}
	files, err = filepath.Glob(filepath.Join(os.Getenv("FRAMEWORKS_CONTROL_OUTBOX_DIR"), "*.pb.sent.test"))
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 {
		t.Fatalf("expected one in-flight durable row after drain, got %d", len(files))
	}
	if confirmErr := confirmDurableOutboxSends(); confirmErr != nil {
		t.Fatal(confirmErr)
	}
	files, err = filepath.Glob(filepath.Join(os.Getenv("FRAMEWORKS_CONTROL_OUTBOX_DIR"), "*.pb.sent.test"))
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 0 {
		t.Fatalf("expected confirmed durable row removed, got %d", len(files))
	}
}

func TestDurableOutboxReplaysUnconfirmedSendAfterReconnect(t *testing.T) {
	resetControlState(t)
	resetTestOutbox(t)
	dir := t.TempDir()
	t.Setenv("FRAMEWORKS_CONTROL_OUTBOX_DIR", dir)
	msg := &ipcpb.ControlMessage{Payload: &ipcpb.ControlMessage_SyncComplete{
		SyncComplete: &ipcpb.SyncComplete{RequestId: "uncertain-sync", Status: "synced"},
	}}
	if err := enqueueDurableOutbox(msg); err != nil {
		t.Fatal(err)
	}
	first := &fakeControlStream{}
	firstConn := &streamConn{stream: first, epoch: "test"}
	publishConn(firstConn)
	if err := drainDurableOutboxForConnection(firstConn); err != nil {
		t.Fatal(err)
	}
	if len(first.sent) != 1 {
		t.Fatalf("first connection sent %d messages, want 1", len(first.sent))
	}
	if err := prepareDurableOutboxForReconnect(); err != nil {
		t.Fatal(err)
	}
	second := &fakeControlStream{}
	secondConn := &streamConn{stream: second, epoch: "test"}
	publishConn(secondConn)
	if err := drainDurableOutboxForConnection(secondConn); err != nil {
		t.Fatal(err)
	}
	if len(second.sent) != 1 || second.sent[0].GetSyncComplete().GetRequestId() != "uncertain-sync" {
		t.Fatalf("unconfirmed transition was not replayed: %+v", second.sent)
	}
}

func TestDurableOutboxConfirmationIsScopedToConnectionEpoch(t *testing.T) {
	resetControlState(t)
	dir := t.TempDir()
	t.Setenv("FRAMEWORKS_CONTROL_OUTBOX_DIR", dir)
	msg := &ipcpb.ControlMessage{Payload: &ipcpb.ControlMessage_SyncComplete{
		SyncComplete: &ipcpb.SyncComplete{RequestId: "epoch-sync", Status: "synced"},
	}}
	if err := enqueueDurableOutbox(msg); err != nil {
		t.Fatal(err)
	}
	first := &streamConn{stream: &fakeControlStream{}, epoch: "epoch-a"}
	publishConn(first)
	if err := drainDurableOutboxForConnection(first); err != nil {
		t.Fatal(err)
	}

	secondStream := &fakeControlStream{}
	second := &streamConn{stream: secondStream, epoch: "epoch-b"}
	publishConn(second)
	if err := sendHeartbeatAndConfirmDurableOutbox(second, &ipcpb.ControlMessage{
		Payload: &ipcpb.ControlMessage_Heartbeat{Heartbeat: &ipcpb.Heartbeat{}},
	}); err != nil {
		t.Fatal(err)
	}
	oldInflight, err := filepath.Glob(filepath.Join(dir, "*.pb.sent.epoch-a"))
	if err != nil {
		t.Fatal(err)
	}
	if len(oldInflight) != 1 {
		t.Fatalf("successor heartbeat removed predecessor in-flight row: %v", oldInflight)
	}

	if err := prepareDurableOutboxForEpoch(second.epoch); err != nil {
		t.Fatal(err)
	}
	if err := drainDurableOutboxForConnection(second); err != nil {
		t.Fatal(err)
	}
	if got := secondStream.sent[len(secondStream.sent)-1].GetSyncComplete(); got.GetRequestId() != "epoch-sync" {
		t.Fatalf("replayed payload = %+v", got)
	}
}

func TestDurableOutboxRejectsDrainCapturedFromSupersededConnection(t *testing.T) {
	resetControlState(t)
	t.Setenv("FRAMEWORKS_CONTROL_OUTBOX_DIR", t.TempDir())
	msg := &ipcpb.ControlMessage{Payload: &ipcpb.ControlMessage_DvrStopped{
		DvrStopped: &ipcpb.DVRStopped{DvrHash: "stale-connection", Status: "completed"},
	}}
	if err := enqueueDurableOutbox(msg); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name   string
		active *streamConn
	}{
		{name: "successor published", active: &streamConn{stream: &fakeControlStream{}, epoch: "new"}},
		{name: "handoff has no published connection"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			oldStream := &fakeControlStream{}
			old := &streamConn{stream: oldStream, epoch: "old"}
			publishConn(tc.active)
			if err := drainDurableOutboxForConnection(old); !errors.Is(err, errStreamDisconnected) {
				t.Fatalf("stale drain error = %v, want disconnected", err)
			}
			if len(oldStream.sent) != 0 {
				t.Fatalf("stale connection sent %d durable rows", len(oldStream.sent))
			}
		})
	}
}

func TestDurableOutboxReconnectPreparationNeverOverwritesPendingRow(t *testing.T) {
	resetControlState(t)
	dir := t.TempDir()
	t.Setenv("FRAMEWORKS_CONTROL_OUTBOX_DIR", dir)
	pending := filepath.Join(dir, "same.pb")
	inflight := pending + ".sent.old"
	if err := os.WriteFile(pending, []byte("pending"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(inflight, []byte("inflight"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := prepareDurableOutboxForEpoch("new"); err == nil || !strings.Contains(err.Error(), "pending collision") {
		t.Fatalf("prepare error = %v, want collision", err)
	}
	for _, path := range []string{pending, inflight} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("preparation destroyed %s: %v", path, err)
		}
	}
}

func TestDurableOutboxStartupPreparationDoesNotWaitForControlPlane(t *testing.T) {
	resetControlState(t)
	dir := t.TempDir()
	t.Setenv("FRAMEWORKS_CONTROL_OUTBOX_DIR", dir)
	pending := filepath.Join(dir, "completion.pb")
	inflight := pending + ".sent.old-epoch"
	temporary := filepath.Join(dir, "interrupted.pb.tmp")
	if err := os.WriteFile(inflight, []byte("completion"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(temporary, []byte("partial"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := prepareDurableOutboxAtStartup(); err != nil {
		t.Fatal(err)
	}
	if payload, err := os.ReadFile(pending); err != nil || string(payload) != "completion" {
		t.Fatalf("uncertain completion was not restored before connect: payload=%q err=%v", payload, err)
	}
	if _, err := os.Stat(inflight); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("old in-flight path still exists: %v", err)
	}
	if _, err := os.Stat(temporary); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("crash-leftover temp path still exists: %v", err)
	}
}

func TestControlRegistrationWaitsForDurableOutboxPreparation(t *testing.T) {
	resetControlState(t)
	dir := t.TempDir()
	t.Setenv("FRAMEWORKS_CONTROL_OUTBOX_DIR", dir)
	pending := filepath.Join(dir, "same.pb")
	if err := os.WriteFile(pending, []byte("pending"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(pending+".sent.old", []byte("inflight"), 0o600); err != nil {
		t.Fatal(err)
	}

	stream := &fakeControlStream{}
	reg := &ipcpb.ControlMessage{Payload: &ipcpb.ControlMessage_Register{Register: &ipcpb.Register{NodeId: "node-1"}}}
	if _, err := prepareAndRegisterControlConnection(stream, "node-1", reg); err == nil || !strings.Contains(err.Error(), "pending collision") {
		t.Fatalf("registration preparation error = %v, want collision", err)
	}
	if len(stream.sent) != 0 {
		t.Fatalf("registration was sent before local preparation failed: %+v", stream.sent)
	}
}

func TestDurableOutboxDefaultsToPersistentHelmsmanState(t *testing.T) {
	t.Setenv("FRAMEWORKS_CONTROL_OUTBOX_DIR", "")
	stateDir := t.TempDir()
	t.Setenv("HELMSMAN_STATE_DIR", stateDir)
	if got, want := durableOutboxDir(), filepath.Join(stateDir, "control-outbox"); got != want {
		t.Fatalf("durable outbox dir = %q, want %q", got, want)
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

func TestSendDesiredStateResultDoesNotDuplicateClassifierFallback(t *testing.T) {
	resetControlState(t)
	resetTestOutbox(t)
	outboxFile := filepath.Join(t.TempDir(), "outbox-file")
	if err := os.WriteFile(outboxFile, []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("FRAMEWORKS_CONTROL_OUTBOX_DIR", outboxFile)
	msg := &ipcpb.ControlMessage{Payload: &ipcpb.ControlMessage_UpdateApplyResult{
		UpdateApplyResult: &ipcpb.UpdateApplyResult{NodeId: "node-1", TargetRelease: "v0.3.0"},
	}}
	if sendDesiredStateResult(msg, true, nil, sendControlMessage) {
		t.Fatal("restart must wait for durable persistence")
	}
	outboxMu.Lock()
	defer outboxMu.Unlock()
	if len(outbox) != 1 {
		t.Fatalf("bounded fallback contains %d copies, want 1", len(outbox))
	}
}

func TestProcessingProgressDropsAndTerminalResultSurvivesDisconnect(t *testing.T) {
	resetControlState(t)
	resetTestOutbox(t)
	dir := t.TempDir()
	t.Setenv("FRAMEWORKS_CONTROL_OUTBOX_DIR", dir)

	progress := &ipcpb.ControlMessage{Payload: &ipcpb.ControlMessage_ProcessingJobProgress{
		ProcessingJobProgress: &ipcpb.ProcessingJobProgress{JobId: "chapter-finalize-v2-3-c", ProgressPct: 42},
	}}
	for range maxOutbox * 2 {
		if err := sendProcessingJobMessage(progress); err == nil || !strings.Contains(err.Error(), "ephemeral message dropped") {
			t.Fatalf("disconnected progress error = %v, want dropped", err)
		}
	}
	outboxMu.Lock()
	memoryLen := len(outbox)
	outboxMu.Unlock()
	if memoryLen != 0 {
		t.Fatalf("ephemeral progress filled memory outbox: %d messages", memoryLen)
	}

	result := &ipcpb.ControlMessage{Payload: &ipcpb.ControlMessage_ProcessingJobResult{
		ProcessingJobResult: &ipcpb.ProcessingJobResult{JobId: "chapter-finalize-v2-3-c", Status: "completed"},
	}}
	if err := sendProcessingJobMessage(result); err == nil || !strings.Contains(err.Error(), "persisted for reconnect") {
		t.Fatalf("disconnected result error = %v, want durable retry", err)
	}
	files, err := filepath.Glob(filepath.Join(dir, "*.pb"))
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 {
		t.Fatalf("durable processing results = %d, want 1", len(files))
	}

	// More progress from other active jobs cannot evict the terminal result:
	// progress never enters either retry queue.
	for range maxOutbox * 2 {
		_ = sendProcessingJobMessage(&ipcpb.ControlMessage{Payload: &ipcpb.ControlMessage_ProcessingJobProgress{
			ProcessingJobProgress: &ipcpb.ProcessingJobProgress{JobId: "other-job", ProgressPct: 7},
		}})
	}
	stream := &fakeControlStream{}
	testConn := &streamConn{stream: stream, epoch: "test"}
	publishConn(testConn)
	drainOutbox(testConn)
	if len(stream.sent) != 1 || stream.sent[0].GetProcessingJobResult().GetJobId() != "chapter-finalize-v2-3-c" {
		t.Fatalf("reconnect delivered %+v, want durable chapter result", stream.sent)
	}
	files, err = filepath.Glob(filepath.Join(dir, "*.pb"))
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 0 {
		t.Fatalf("durable result remained after reconnect: %v", files)
	}
}

func TestControlDeliveryClassifierSeparatesSamplesFromTerminalTransitions(t *testing.T) {
	resetControlState(t)
	resetTestOutbox(t)
	dir := t.TempDir()
	t.Setenv("FRAMEWORKS_CONTROL_OUTBOX_DIR", dir)

	ephemeral := []*ipcpb.ControlMessage{
		{Payload: &ipcpb.ControlMessage_DvrProgress{DvrProgress: &ipcpb.DVRProgress{DvrHash: "dvr-live"}}},
		{Payload: &ipcpb.ControlMessage_MistTrigger{MistTrigger: &ipcpb.MistTrigger{
			TriggerType: "storage_lifecycle",
			TriggerPayload: &ipcpb.MistTrigger_StorageLifecycleData{
				StorageLifecycleData: &ipcpb.StorageLifecycleData{AssetHash: "asset-live"},
			},
		}}},
		{Payload: &ipcpb.ControlMessage_ProcessingJobResult{ProcessingJobResult: &ipcpb.ProcessingJobResult{
			JobId: "cache_update:asset-live", Status: "cache_update",
		}}},
	}
	for _, msg := range ephemeral {
		if err := sendControlMessage(msg); err == nil || !strings.Contains(err.Error(), "ephemeral message dropped") {
			t.Fatalf("ephemeral message error = %v, want explicit drop", err)
		}
	}

	durable := []*ipcpb.ControlMessage{
		{Payload: &ipcpb.ControlMessage_DvrStopped{DvrStopped: &ipcpb.DVRStopped{DvrHash: "dvr-terminal", Status: "completed"}}},
		{Payload: &ipcpb.ControlMessage_SyncComplete{SyncComplete: &ipcpb.SyncComplete{RequestId: "sync-terminal", Status: "synced"}}},
		{Payload: &ipcpb.ControlMessage_ThumbnailUploaded{ThumbnailUploaded: &ipcpb.ThumbnailUploaded{AttemptId: "thumbnail-terminal"}}},
		{Payload: &ipcpb.ControlMessage_ArtifactDeleted{ArtifactDeleted: &ipcpb.ArtifactDeleted{ArtifactHash: "artifact-terminal"}}},
		{Payload: &ipcpb.ControlMessage_MarkDvrSegmentUploaded{MarkDvrSegmentUploaded: &ipcpb.MarkDVRSegmentUploaded{DvrHash: "dvr-terminal", SegmentName: "segment-uploaded"}}},
		{Payload: &ipcpb.ControlMessage_DvrSegmentDropped{DvrSegmentDropped: &ipcpb.DVRSegmentDropped{DvrHash: "dvr-terminal", SegmentName: "segment-dropped"}}},
		{Payload: &ipcpb.ControlMessage_UpdateApplyResult{UpdateApplyResult: &ipcpb.UpdateApplyResult{NodeId: "node-terminal", TargetRelease: "v0.3.0"}}},
		{Payload: &ipcpb.ControlMessage_ConfigSeedApplyResult{ConfigSeedApplyResult: &ipcpb.ConfigSeedApplyResult{NodeId: "node-terminal", SeedVersion: 7}}},
	}
	for _, msg := range durable {
		if err := sendControlMessage(msg); err == nil || !strings.Contains(err.Error(), "persisted for reconnect") {
			t.Fatalf("durable message error = %v, want persisted retry", err)
		}
	}

	outboxMu.Lock()
	memoryLen := len(outbox)
	outboxMu.Unlock()
	if memoryLen != 0 {
		t.Fatalf("classified messages entered bounded memory outbox: %d", memoryLen)
	}
	files, err := filepath.Glob(filepath.Join(dir, "*.pb"))
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != len(durable) {
		t.Fatalf("durable files = %d, want %d", len(files), len(durable))
	}

	stream := &fakeControlStream{}
	testConn := &streamConn{stream: stream, epoch: "test"}
	publishConn(testConn)
	drainOutbox(testConn)
	if len(stream.sent) != len(durable) {
		t.Fatalf("reconnect delivered %d terminal transitions, want %d", len(stream.sent), len(durable))
	}
	for _, msg := range stream.sent {
		if classifyControlMessage(msg) != controlDeliveryDurable {
			t.Fatalf("reconnect delivered non-durable payload: %T", msg.GetPayload())
		}
	}
}

func TestArtifactDeletionFenceSurvivesDurableReplay(t *testing.T) {
	resetControlState(t)
	dir := t.TempDir()
	t.Setenv("FRAMEWORKS_CONTROL_OUTBOX_DIR", dir)
	if err := SendArtifactDeleted("artifact-replay", "/data/artifact", "cleanup", "vod", 42); err == nil || !durableControlWasPersisted(err) {
		t.Fatalf("disconnected deletion error = %v, want durable persistence", err)
	}
	files, err := filepath.Glob(filepath.Join(dir, "*.pb"))
	if err != nil || len(files) != 1 {
		t.Fatalf("durable deletion rows = %v, err=%v", files, err)
	}
	payload, err := os.ReadFile(files[0])
	if err != nil {
		t.Fatal(err)
	}
	var persisted ipcpb.ControlMessage
	if err := proto.Unmarshal(payload, &persisted); err != nil {
		t.Fatal(err)
	}
	want := persisted.GetArtifactDeleted().GetDeletedAtMs()
	if want <= 0 {
		t.Fatalf("persisted deletion timestamp = %d", want)
	}

	stream := &fakeControlStream{}
	conn := &streamConn{stream: stream, epoch: "replay"}
	publishConn(conn)
	if err := drainDurableOutboxForConnection(conn); err != nil {
		t.Fatal(err)
	}
	if len(stream.sent) != 1 || stream.sent[0].GetArtifactDeleted().GetDeletedAtMs() != want {
		t.Fatalf("replayed deletion fence = %+v, want %d", stream.sent, want)
	}
}

func TestDurableReplayPreservesOriginalEnvelopeTimeForLegacyDeletionFence(t *testing.T) {
	resetControlState(t)
	resetTestOutbox(t)
	dir := t.TempDir()
	t.Setenv("FRAMEWORKS_CONTROL_OUTBOX_DIR", dir)
	original := time.UnixMilli(1234)
	msg := &ipcpb.ControlMessage{
		SentAt: timestamppb.New(original),
		Payload: &ipcpb.ControlMessage_ArtifactDeleted{ArtifactDeleted: &ipcpb.ArtifactDeleted{
			ArtifactHash: "legacy-delete", NodeId: "edge-1",
		}},
	}
	if err := enqueueDurableOutbox(msg); err != nil {
		t.Fatal(err)
	}
	stream := &fakeControlStream{}
	conn := &streamConn{stream: stream, epoch: "legacy-replay"}
	publishConn(conn)
	if err := drainDurableOutboxForConnection(conn); err != nil {
		t.Fatal(err)
	}
	if len(stream.sent) != 1 || stream.sent[0].GetSentAt().AsTime().UnixMilli() != original.UnixMilli() {
		t.Fatalf("replay envelope time = %+v, want %d", stream.sent, original.UnixMilli())
	}
}

func TestConfigSeedAckSenderAcceptsDisconnectedDurablePersistence(t *testing.T) {
	resetControlState(t)
	resetTestOutbox(t)
	dir := t.TempDir()
	t.Setenv("FRAMEWORKS_CONTROL_OUTBOX_DIR", dir)
	before := testutil.ToFloat64(ControlDeliveryOutcomes.WithLabelValues("durable", "persisted"))
	msg := &ipcpb.ControlMessage{Payload: &ipcpb.ControlMessage_ConfigSeedApplyResult{
		ConfigSeedApplyResult: &ipcpb.ConfigSeedApplyResult{NodeId: "edge-1", SeedVersion: 12},
	}}
	if err := acceptConfigSeedApplyResult(logging.NewLogger(), msg); err != nil {
		t.Fatalf("disconnected durable acceptance = %v", err)
	}
	files, err := filepath.Glob(filepath.Join(dir, "*.pb"))
	if err != nil || len(files) != 1 {
		t.Fatalf("persisted seed ACK rows = %v, err=%v", files, err)
	}
	if after := testutil.ToFloat64(ControlDeliveryOutcomes.WithLabelValues("durable", "persisted")); after != before+1 {
		t.Fatalf("durable persisted outcomes = %v, want %v", after, before+1)
	}
}

func TestDurableOutboxTransientReadFailureRemainsPending(t *testing.T) {
	resetControlState(t)
	resetTestOutbox(t)
	dir := t.TempDir()
	t.Setenv("FRAMEWORKS_CONTROL_OUTBOX_DIR", dir)
	blockedPath := filepath.Join(dir, "000-unreadable.pb")
	blocked := &ipcpb.ControlMessage{Payload: &ipcpb.ControlMessage_SyncComplete{
		SyncComplete: &ipcpb.SyncComplete{RequestId: "transient", Status: "synced"},
	}}
	payload, err := proto.Marshal(blocked)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(blockedPath, payload, 0o600); err != nil {
		t.Fatal(err)
	}
	msg := &ipcpb.ControlMessage{Payload: &ipcpb.ControlMessage_DvrStopped{
		DvrStopped: &ipcpb.DVRStopped{DvrHash: "after-unreadable", Status: "completed"},
	}}
	if err := enqueueDurableOutbox(msg); err != nil {
		t.Fatal(err)
	}

	stream := &fakeControlStream{}
	conn := &streamConn{stream: stream, epoch: "read-retry"}
	publishConn(conn)
	originalReadFile := durableOutboxReadFile
	readAttempts := 0
	durableOutboxReadFile = func(path string) ([]byte, error) {
		if path == blockedPath && readAttempts == 0 {
			readAttempts++
			return nil, errors.New("transient read failure")
		}
		return os.ReadFile(path)
	}
	t.Cleanup(func() { durableOutboxReadFile = originalReadFile })
	if err := drainDurableOutboxForConnection(conn); err == nil {
		t.Fatal("transient read failure was treated as success")
	}
	if len(stream.sent) != 0 {
		t.Fatalf("later durable row overtook unreadable predecessor: %+v", stream.sent)
	}
	if _, err := os.Stat(blockedPath); err != nil {
		t.Fatalf("transiently unreadable row was not retained: %v", err)
	}
	if _, err := os.Stat(blockedPath + ".dead"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("transiently unreadable row was quarantined: %v", err)
	}
	if err := drainDurableOutboxForConnection(conn); err != nil {
		t.Fatal(err)
	}
	if len(stream.sent) != 2 {
		t.Fatalf("recovered drain sent %d rows, want 2", len(stream.sent))
	}
}

func TestDurableOutboxPermanentReadFailureIsQuarantinedWithoutHeadOfLineBlock(t *testing.T) {
	resetControlState(t)
	resetTestOutbox(t)
	dir := t.TempDir()
	t.Setenv("FRAMEWORKS_CONTROL_OUTBOX_DIR", dir)
	blockedPath := filepath.Join(dir, "000-unreadable.pb")
	if err := os.Mkdir(blockedPath, 0o700); err != nil {
		t.Fatal(err)
	}
	msg := &ipcpb.ControlMessage{Payload: &ipcpb.ControlMessage_DvrStopped{
		DvrStopped: &ipcpb.DVRStopped{DvrHash: "after-permanent-error", Status: "completed"},
	}}
	if err := enqueueDurableOutbox(msg); err != nil {
		t.Fatal(err)
	}
	stream := &fakeControlStream{}
	conn := &streamConn{stream: stream, epoch: "read-escalation"}
	publishConn(conn)
	before := testutil.ToFloat64(ControlOutboxScanErrors.WithLabelValues("drain_read"))
	for attempt := 1; attempt <= durableOutboxReadFailureLimit; attempt++ {
		err := drainDurableOutboxForConnection(conn)
		if attempt < durableOutboxReadFailureLimit && err == nil {
			t.Fatalf("read attempt %d unexpectedly succeeded", attempt)
		}
	}
	if after := testutil.ToFloat64(ControlOutboxScanErrors.WithLabelValues("drain_read")); after != before+durableOutboxReadFailureLimit {
		t.Fatalf("drain-read errors = %v, want %v", after, before+durableOutboxReadFailureLimit)
	}
	if _, err := os.Stat(blockedPath + ".dead"); err != nil {
		t.Fatalf("permanently unreadable row was not quarantined: %v", err)
	}
	if len(stream.sent) != 1 || stream.sent[0].GetDvrStopped().GetDvrHash() != "after-permanent-error" {
		t.Fatalf("later terminal transition did not progress after escalation: %+v", stream.sent)
	}
}

func TestDurableOutboxReadFailureStateDropsRemovedRows(t *testing.T) {
	resetControlState(t)
	resetTestOutbox(t)
	dir := t.TempDir()
	t.Setenv("FRAMEWORKS_CONTROL_OUTBOX_DIR", dir)
	removedPath := filepath.Join(dir, "removed.pb")
	durableOutboxReadFailures[removedPath] = 2

	conn := &streamConn{stream: &fakeControlStream{}, epoch: "removed-row"}
	publishConn(conn)
	if err := drainDurableOutboxForConnection(conn); err != nil {
		t.Fatal(err)
	}
	if _, exists := durableOutboxReadFailures[removedPath]; exists {
		t.Fatal("read-failure state retained a row no longer present on disk")
	}
}

func TestDurableOutboxCorruptMessageIsQuarantinedWithoutHeadOfLineBlock(t *testing.T) {
	resetControlState(t)
	resetTestOutbox(t)
	dir := t.TempDir()
	t.Setenv("FRAMEWORKS_CONTROL_OUTBOX_DIR", dir)
	corruptPath := filepath.Join(dir, "000-corrupt.pb")
	if err := os.WriteFile(corruptPath, []byte("not protobuf"), 0o600); err != nil {
		t.Fatal(err)
	}
	msg := &ipcpb.ControlMessage{Payload: &ipcpb.ControlMessage_DvrStopped{
		DvrStopped: &ipcpb.DVRStopped{DvrHash: "after-corrupt-row", Status: "completed"},
	}}
	if err := enqueueDurableOutbox(msg); err != nil {
		t.Fatal(err)
	}

	stream := &fakeControlStream{}
	conn := &streamConn{stream: stream, epoch: "corrupt-row"}
	publishConn(conn)
	beforeSent := testutil.ToFloat64(ControlDeliveryOutcomes.WithLabelValues("durable", "sent"))
	beforeDecode := testutil.ToFloat64(ControlOutboxScanErrors.WithLabelValues("drain_decode"))
	if err := drainDurableOutboxForConnection(conn); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(corruptPath + ".dead"); err != nil {
		t.Fatalf("corrupt row was not quarantined: %v", err)
	}
	if len(stream.sent) != 1 || stream.sent[0].GetDvrStopped().GetDvrHash() != "after-corrupt-row" {
		t.Fatalf("later terminal transition did not progress after corrupt row: %+v", stream.sent)
	}
	if afterSent := testutil.ToFloat64(ControlDeliveryOutcomes.WithLabelValues("durable", "sent")); afterSent != beforeSent+1 {
		t.Fatalf("durable sent outcomes = %v, want %v", afterSent, beforeSent+1)
	}
	if afterDecode := testutil.ToFloat64(ControlOutboxScanErrors.WithLabelValues("drain_decode")); afterDecode != beforeDecode+1 {
		t.Fatalf("durable decode errors = %v, want %v", afterDecode, beforeDecode+1)
	}
}

func TestDurableOutboxQuarantineRenameFailureRetainsHeadOfLine(t *testing.T) {
	resetControlState(t)
	resetTestOutbox(t)
	dir := t.TempDir()
	t.Setenv("FRAMEWORKS_CONTROL_OUTBOX_DIR", dir)
	corruptPath := filepath.Join(dir, "000-corrupt.pb")
	if err := os.WriteFile(corruptPath, []byte("not protobuf"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(corruptPath+".dead", 0o700); err != nil {
		t.Fatal(err)
	}
	msg := &ipcpb.ControlMessage{Payload: &ipcpb.ControlMessage_DvrStopped{
		DvrStopped: &ipcpb.DVRStopped{DvrHash: "after-quarantine-failure", Status: "completed"},
	}}
	if err := enqueueDurableOutbox(msg); err != nil {
		t.Fatal(err)
	}

	stream := &fakeControlStream{}
	conn := &streamConn{stream: stream, epoch: "quarantine-failure"}
	publishConn(conn)
	beforeRename := testutil.ToFloat64(ControlOutboxScanErrors.WithLabelValues("quarantine_rename"))
	if err := drainDurableOutboxForConnection(conn); err == nil {
		t.Fatal("quarantine rename failure was treated as success")
	}
	if _, err := os.Stat(corruptPath); err != nil {
		t.Fatalf("corrupt row was lost after failed quarantine: %v", err)
	}
	if len(stream.sent) != 0 {
		t.Fatalf("later durable row overtook unquarantined predecessor: %+v", stream.sent)
	}
	if afterRename := testutil.ToFloat64(ControlOutboxScanErrors.WithLabelValues("quarantine_rename")); afterRename != beforeRename+1 {
		t.Fatalf("quarantine rename errors = %v, want %v", afterRename, beforeRename+1)
	}

	if err := os.Remove(corruptPath + ".dead"); err != nil {
		t.Fatal(err)
	}
	if err := drainDurableOutboxForConnection(conn); err != nil {
		t.Fatal(err)
	}
	if len(stream.sent) != 1 || stream.sent[0].GetDvrStopped().GetDvrHash() != "after-quarantine-failure" {
		t.Fatalf("later durable row did not progress after quarantine recovered: %+v", stream.sent)
	}
}

func TestDurableOutboxQuarantineIsReapedAfterRetention(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("FRAMEWORKS_CONTROL_OUTBOX_DIR", dir)
	dead := filepath.Join(dir, "corrupt.pb.dead")
	if err := os.WriteFile(dead, []byte("corrupt"), 0o600); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-durableOutboxQuarantineRetention - time.Hour)
	if err := os.Chtimes(dead, old, old); err != nil {
		t.Fatal(err)
	}
	if err := sweepDurableOutboxQuarantineLocked(dir, time.Now()); err != nil {
		t.Fatal(err)
	}
	updateDurableOutboxMetricsLocked(dir)
	if _, err := os.Stat(dead); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expired quarantine row still exists: %v", err)
	}
	if got := testutil.ToFloat64(ControlOutboxQuarantined); got != 0 {
		t.Fatalf("quarantined gauge after reap = %v, want 0", got)
	}
}

func TestDuplicateMistTriggerResponseCannotBlockReceiveLoop(t *testing.T) {
	resetControlState(t)
	before := testutil.ToFloat64(ControlResponseDrops.WithLabelValues("late_or_duplicate"))
	const requestID = "duplicate-response"
	pendingMistTriggers[requestID] = pendingMistTrigger{
		responseCh:  make(chan *ipcpb.MistTriggerResponse, 1),
		triggerType: string(mist.TriggerPlayRewrite),
	}
	response := &ipcpb.MistTriggerResponse{RequestId: requestID}
	handleMistTriggerResponse(response)
	done := make(chan struct{})
	go func() {
		handleMistTriggerResponse(response)
		close(done)
	}()
	waitForTestDone(t, done, "duplicate Mist trigger response to be dropped")
	if after := testutil.ToFloat64(ControlResponseDrops.WithLabelValues("late_or_duplicate")); after != before {
		// Mist duplicates are claimed at the pending-map boundary and never reach
		// the response channel, so they should not be double-counted here.
		t.Fatalf("response handoff drops changed for map-level duplicate: %v -> %v", before, after)
	}
}

func TestOfferResponseCountsLateOrDuplicateDrop(t *testing.T) {
	ch := make(chan int, 1)
	ch <- 1
	before := testutil.ToFloat64(ControlResponseDrops.WithLabelValues("late_or_duplicate"))
	offerResponse(ch, 2)
	if after := testutil.ToFloat64(ControlResponseDrops.WithLabelValues("late_or_duplicate")); after != before+1 {
		t.Fatalf("response drop metric = %v, want %v", after, before+1)
	}
	if got := <-ch; got != 1 {
		t.Fatalf("late response displaced claimed response: got %d", got)
	}
}

func TestDurableOutboxMetricScanErrorIsObservable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(path, []byte("file"), 0o600); err != nil {
		t.Fatal(err)
	}
	before := testutil.ToFloat64(ControlOutboxScanErrors.WithLabelValues("metrics_readdir"))
	updateDurableOutboxMetricsLocked(path)
	if after := testutil.ToFloat64(ControlOutboxScanErrors.WithLabelValues("metrics_readdir")); after != before+1 {
		t.Fatalf("scan error metric = %v, want %v", after, before+1)
	}
}

func resetTestOutbox(t *testing.T) {
	t.Helper()
	durableOutboxMu.Lock()
	durableOutboxReadFailures = make(map[string]int)
	durableOutboxMu.Unlock()
	outboxMu.Lock()
	outbox = nil
	outboxMu.Unlock()
	t.Cleanup(func() {
		durableOutboxMu.Lock()
		durableOutboxReadFailures = make(map[string]int)
		durableOutboxMu.Unlock()
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
	pendingMistTriggers["timeout-test"] = pendingMistTrigger{responseCh: ch, triggerType: "TEST"}
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
	pendingMistTriggers["disconnect-test"] = pendingMistTrigger{responseCh: ch, triggerType: "TEST"}
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
	pendingMistTriggers map[string]pendingMistTrigger
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
	pendingMistTriggers = make(map[string]pendingMistTrigger)
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
		RequestId:          "req-reconnect",
		Response:           "ok",
		IngestGeneration:   "generation-reconnect",
		IngestConnectorPid: 301,
	})

	result := waitForMistTriggerResult(t, resultCh, "Mist trigger result after reconnect")
	err := waitForError(t, errCh, "Mist trigger error after reconnect")
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if result.Response != "ok" {
		t.Fatalf("expected response ok, got %q", result.Response)
	}
	if result.IngestGeneration != "generation-reconnect" {
		t.Fatalf("expected ingest generation propagation, got %q", result.IngestGeneration)
	}
	if len(stream.sent) != 1 {
		t.Fatalf("expected 1 send, got %d", len(stream.sent))
	}
}

func TestBlockingMistTriggerDeadlineDoesNotRecycleSharedStream(t *testing.T) {
	resetControlState(t)
	oldTimeout := controlStreamSendTimeout
	controlStreamSendTimeout = 200 * time.Millisecond
	t.Cleanup(func() { controlStreamSendTimeout = oldTimeout })

	streamCtx, cancelStream := context.WithCancel(context.Background())
	t.Cleanup(cancelStream)
	release := make(chan struct{})
	fake := &fakeControlStream{sendCh: make(chan *ipcpb.ControlMessage, 1), sendBlock: release}
	stream := &lockedClientStream{HelmsmanControl_ConnectClient: fake, cancel: cancelStream}
	storeConn(stream, "")
	trigger := &ipcpb.MistTrigger{
		TriggerType: "stream_start",
		RequestId:   "req-near-deadline",
		Blocking:    true,
	}
	requestCtx, cancelRequest := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancelRequest()

	resultCh := make(chan *MistTriggerResult, 1)
	errCh := make(chan error, 1)
	go func() {
		result, err := SendMistTriggerContext(requestCtx, trigger, logging.NewLogger())
		resultCh <- result
		errCh <- err
	}()
	waitForControlMessage(t, fake.sendCh, "near-deadline Mist trigger send")
	result := waitForMistTriggerResult(t, resultCh, "near-deadline Mist trigger result")
	err := waitForError(t, errCh, "near-deadline Mist trigger error")
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("trigger error = %v, want request deadline", err)
	}
	if result == nil || !result.Abort {
		t.Fatalf("deadline result = %#v, want abort", result)
	}
	if streamCtx.Err() != nil {
		t.Fatal("nearly exhausted trigger budget recycled the shared stream")
	}

	close(release)
	if err := stream.Send(&ipcpb.ControlMessage{}); err != nil {
		t.Fatalf("send after request deadline failed: %v", err)
	}
	if streamCtx.Err() != nil {
		t.Fatal("healthy shared stream was recycled after the timed-out request completed")
	}
}

func TestApplyJitterZeroPercent(t *testing.T) {
	d := 5 * time.Second
	got := applyJitter(d, 0)
	if got != d {
		t.Fatalf("expected %v, got %v", d, got)
	}
}

func TestBlockingTimeoutForTrigger(t *testing.T) {
	if got := blockingTimeoutForTrigger(string(mist.TriggerPlayRewrite)); got != blockingTriggerTimeout {
		t.Fatalf("PLAY_REWRITE timeout = %v, want %v", got, blockingTriggerTimeout)
	}
	if got := blockingTimeoutForTrigger(string(mist.TriggerStreamLifecycle)); got != lifecycleReconciliationTimeout {
		t.Fatalf("lifecycle recovery timeout = %v, want %v", got, lifecycleReconciliationTimeout)
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

	hook := make(chan string, 1)
	disconnectSubscribedHookMu.Lock()
	disconnectSubscribedHook = hook
	disconnectSubscribedHookMu.Unlock()
	t.Cleanup(func() {
		disconnectSubscribedHookMu.Lock()
		disconnectSubscribedHook = nil
		disconnectSubscribedHookMu.Unlock()
	})

	resultCh := make(chan *MistTriggerResult, 1)
	errCh := make(chan error, 1)
	go func() {
		result, err := SendMistTrigger(trigger, logger)
		resultCh <- result
		errCh <- err
	}()

	waitForControlMessage(t, stream1.sendCh, "initial Mist trigger send")
	waitForPendingTrigger(t, trigger.RequestId)

	deadline := time.After(time.Second)
	subscribed := false
	for !subscribed {
		select {
		case requestID := <-hook:
			if requestID == trigger.RequestId {
				subscribed = true
			}
		case <-deadline:
			t.Fatal("timeout waiting for disconnect subscription")
		}
	}

	notifyDisconnect()

	storeConn(stream2, "")
	signalReconnect()

	waitForControlMessage(t, stream2.sendCh, "retried Mist trigger send after reconnect")
	handleMistTriggerResponse(&ipcpb.MistTriggerResponse{
		RequestId:          trigger.RequestId,
		Response:           "ack",
		IngestGeneration:   "generation-retry",
		IngestConnectorPid: 302,
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
