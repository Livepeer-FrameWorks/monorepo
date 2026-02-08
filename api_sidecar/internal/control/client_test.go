package control

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"frameworks/api_sidecar/internal/storage"
	pb "frameworks/pkg/proto"

	"github.com/prometheus/client_golang/prometheus/testutil"
	"google.golang.org/grpc/metadata"
)

type fakeControlStream struct {
	sendErr error
}

func (f *fakeControlStream) Send(*pb.ControlMessage) error {
	return f.sendErr
}

func (f *fakeControlStream) Recv() (*pb.ControlMessage, error) {
	return nil, io.EOF
}

func (f *fakeControlStream) Header() (metadata.MD, error) {
	return nil, nil
}

func (f *fakeControlStream) Trailer() metadata.MD {
	return nil
}

func (f *fakeControlStream) CloseSend() error {
	return nil
}

func (f *fakeControlStream) Context() context.Context {
	return context.Background()
}

func (f *fakeControlStream) SendMsg(interface{}) error {
	return nil
}

func (f *fakeControlStream) RecvMsg(interface{}) error {
	return nil
}

func TestSendMistTriggerOnceStreamDisconnected(t *testing.T) {
	currentStream = nil

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
	currentStream = &fakeControlStream{sendErr: fmt.Errorf("send failed")}

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
	go func() {
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
