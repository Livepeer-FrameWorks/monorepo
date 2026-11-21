package handlers

import (
	"context"
	"errors"
	"fmt"
	"testing"

	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
)

func TestRoutedLivepeerExitFallsBackWithoutRequeueingJob(t *testing.T) {
	for _, code := range []int{1, -9, 137} {
		t.Run(fmt.Sprint(code), func(t *testing.T) {
			stream := fmt.Sprintf("processing+livepeer-exit-%d", code)
			processExitCh := RegisterProcessExitListener(stream)
			t.Cleanup(func() { UnregisterProcessExitListener(stream) })
			RouteProcessExit(ProcessExitEvent{
				StreamName: stream, ProcessType: "Livepeer", ExitCode: code,
				BootCount: 1, Status: "retrying", Reason: "gateway unavailable",
			})
			req := &ipcpb.ProcessingJobRequest{
				ProcessesJson: `[{"process":"Livepeer","source_track":"maxbps","track_select":"video=maxbps"}]`,
			}
			h := &ProcessingJobHandler{}
			_, _, err := h.waitForProcessingStreamReady(context.Background(), discardLog(), nil, req, stream,
				req.GetProcessesJson(), processExitCh, nil, nil, map[string]int{})
			var fallback *livepeerReadinessFallbackError
			if !errors.As(err, &fallback) {
				t.Fatalf("routed Livepeer exit must run local fallback without spending a job retry; got %T: %v", err, err)
			}
		})
	}
}

func TestRoutedAVExitPreservesResourceFailureBeforeEventConsumption(t *testing.T) {
	const stream = "processing+av-resource-exit"
	processExitCh := RegisterProcessExitListener(stream)
	t.Cleanup(func() { UnregisterProcessExitListener(stream) })
	RouteProcessExit(ProcessExitEvent{
		StreamName: stream, ProcessType: "AV", Config: `{"codec":"H264"}`,
		ExitCode: 1, BootCount: 1, Status: "retrying",
	})
	if len(processExitCh) != 1 {
		t.Fatal("AV exit was not queued")
	}
	var resource *processingResourceError
	if err := processingResourceFailure(stream, nil); !errors.As(err, &resource) {
		t.Fatalf("AV resource failure must survive until its queued event is consumed; got %v", err)
	}
}
