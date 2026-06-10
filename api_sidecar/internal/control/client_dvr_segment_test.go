package control

import (
	"context"
	"testing"

	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
)

// DVR-segment control RPCs come in two shapes:
//   - Blocking round-trips (RecordDVRSegment, RequestEvictableSegments,
//     SendRestoreLocalSegmentIndex) where Foghorn's ledger answer is needed
//     before the sidecar acts. Disconnected must error, never hang; the
//     response router delivers to the exact waiter by request_id.
//   - Fire-and-forget reports (MarkDVRSegmentUploaded, DVRSegmentDropped,
//     DVRStreamEndNotification, ThumbnailUploadRequest, FreezeProgress) that
//     just stamp a control message. Disconnected errors; connected emits the
//     right payload.
// connectFake/clearConn/waitForControlMessage live in the sibling test files.

func TestRecordDVRSegmentRoundTrip(t *testing.T) {
	stream := connectFake(t)

	done := make(chan struct{})
	var resp *ipcpb.RecordDVRSegmentResponse
	var err error
	go func() {
		defer close(done)
		resp, err = RecordDVRSegment(context.Background(), "dvr-1", "seg-1", "/data/seg-1.ts", 0, 2000, 2000)
	}()

	sent := waitForControlMessage(t, stream.sendCh, "record dvr segment request")
	req := sent.GetRecordDvrSegmentRequest()
	if req == nil {
		t.Fatalf("expected RecordDvrSegmentRequest payload, got %T", sent.GetPayload())
	}
	if req.GetDvrHash() != "dvr-1" || req.GetSegmentName() != "seg-1" || req.GetDurationMs() != 2000 {
		t.Fatalf("request fields not forwarded: %+v", req)
	}
	if req.GetRecoveryInsert() {
		t.Fatal("live RecordDVRSegment must not set recovery_insert")
	}

	handleRecordDVRSegmentResponse(&ipcpb.RecordDVRSegmentResponse{
		RequestId: req.GetRequestId(),
		Accepted:  true,
		Sequence:  7,
	})

	waitForTestDone(t, done, "record dvr segment round trip")
	if err != nil || !resp.GetAccepted() || resp.GetSequence() != 7 {
		t.Fatalf("unexpected result: resp=%+v err=%v", resp, err)
	}
}

// The recovery variant flips recovery_insert so Foghorn rebuilds ledger rows
// for already-finalized artifacts — the only path allowed to bypass live
// terminal-rejection.
func TestRecordRecoveredDVRSegmentSetsRecoveryInsert(t *testing.T) {
	stream := connectFake(t)

	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = RecordRecoveredDVRSegment(context.Background(), "dvr-1", "seg-1", "/data/seg-1.ts", 0, 2000, 2000)
	}()

	sent := waitForControlMessage(t, stream.sendCh, "recovered record dvr segment request")
	if !sent.GetRecordDvrSegmentRequest().GetRecoveryInsert() {
		t.Fatal("RecordRecoveredDVRSegment must set recovery_insert=true")
	}

	handleRecordDVRSegmentResponse(&ipcpb.RecordDVRSegmentResponse{
		RequestId: sent.GetRecordDvrSegmentRequest().GetRequestId(),
		Accepted:  true,
	})
	waitForTestDone(t, done, "recovered record dvr segment round trip")
}

func TestRecordDVRSegmentDisconnected(t *testing.T) {
	clearConn()
	if _, err := RecordDVRSegment(context.Background(), "dvr-1", "seg-1", "/p", 0, 1, 1); err == nil {
		t.Fatal("expected error with no control stream")
	}
}

func TestRequestEvictableSegmentsRoundTrip(t *testing.T) {
	stream := connectFake(t)

	done := make(chan struct{})
	var resp *ipcpb.EvictableSegmentsResponse
	var err error
	go func() {
		defer close(done)
		resp, err = RequestEvictableSegments(context.Background(), "dvr-1", 5)
	}()

	sent := waitForControlMessage(t, stream.sendCh, "evictable segments request")
	req := sent.GetEvictableSegmentsRequest()
	if req == nil || req.GetDvrHash() != "dvr-1" || req.GetMaxCount() != 5 {
		t.Fatalf("request fields not forwarded: %+v", req)
	}

	handleEvictableSegmentsResponse(&ipcpb.EvictableSegmentsResponse{
		RequestId:    req.GetRequestId(),
		DvrHash:      "dvr-1",
		SegmentNames: []string{"seg-1", "seg-2"},
	})

	waitForTestDone(t, done, "evictable segments round trip")
	if err != nil || len(resp.GetSegmentNames()) != 2 {
		t.Fatalf("unexpected result: resp=%+v err=%v", resp, err)
	}
}

func TestRequestEvictableSegmentsDisconnected(t *testing.T) {
	clearConn()
	if _, err := RequestEvictableSegments(context.Background(), "dvr-1", 5); err == nil {
		t.Fatal("expected error with no control stream")
	}
}

func TestSendRestoreLocalSegmentIndexRoundTrip(t *testing.T) {
	stream := connectFake(t)

	done := make(chan struct{})
	var resp *ipcpb.RestoreLocalSegmentIndexResponse
	var err error
	go func() {
		defer close(done)
		resp, err = SendRestoreLocalSegmentIndex(context.Background(), "dvr-1", []string{"seg-1", "seg-2"})
	}()

	sent := waitForControlMessage(t, stream.sendCh, "restore local segment index request")
	req := sent.GetRestoreLocalSegmentIndexRequest()
	if req == nil || len(req.GetSegmentNames()) != 2 {
		t.Fatalf("request fields not forwarded: %+v", req)
	}

	handleRestoreLocalSegmentIndexResponse(&ipcpb.RestoreLocalSegmentIndexResponse{
		RequestId: req.GetRequestId(),
		DvrHash:   "dvr-1",
	})

	waitForTestDone(t, done, "restore local segment index round trip")
	if err != nil || resp.GetDvrHash() != "dvr-1" {
		t.Fatalf("unexpected result: resp=%+v err=%v", resp, err)
	}
}

// An empty batch is a no-op: it returns an empty response without round-tripping
// (the bounded-reconciliation invariant — there is no "give me everything" call).
func TestSendRestoreLocalSegmentIndexEmptyBatch(t *testing.T) {
	stream := connectFake(t)

	resp, err := SendRestoreLocalSegmentIndex(context.Background(), "dvr-1", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.GetDvrHash() != "dvr-1" {
		t.Fatalf("expected echo of dvr_hash, got %+v", resp)
	}
	select {
	case msg := <-stream.sendCh:
		t.Fatalf("empty batch should not send, but sent %T", msg.GetPayload())
	default:
	}
}

func TestSendRestoreLocalSegmentIndexDisconnected(t *testing.T) {
	clearConn()
	if _, err := SendRestoreLocalSegmentIndex(context.Background(), "dvr-1", []string{"seg-1"}); err == nil {
		t.Fatal("expected error with no control stream")
	}
}

func TestDVRSegmentResponseHandlersIgnoreUnknown(t *testing.T) {
	// No registered waiters; each must be a no-op rather than panic/block.
	handleRecordDVRSegmentResponse(&ipcpb.RecordDVRSegmentResponse{RequestId: "ghost"})
	handleEvictableSegmentsResponse(&ipcpb.EvictableSegmentsResponse{RequestId: "ghost"})
	handleRestoreLocalSegmentIndexResponse(&ipcpb.RestoreLocalSegmentIndexResponse{RequestId: "ghost"})
}

func TestFireAndForgetDVRSenders(t *testing.T) {
	t.Run("MarkDVRSegmentUploaded", func(t *testing.T) {
		stream := connectFake(t)
		if err := SendMarkDVRSegmentUploaded("dvr-1", "seg-1", 4096); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		got := waitForControlMessage(t, stream.sendCh, "mark uploaded").GetMarkDvrSegmentUploaded()
		if got == nil || got.GetDvrHash() != "dvr-1" || got.GetSegmentName() != "seg-1" || got.GetSizeBytes() != 4096 {
			t.Fatalf("payload not as expected: %+v", got)
		}
	})

	t.Run("DVRSegmentDropped", func(t *testing.T) {
		stream := connectFake(t)
		if err := SendDVRSegmentDropped("dvr-1", "seg-1", "disk_pressure", "/p", 0, 2000, 2000, 4096, true); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		got := waitForControlMessage(t, stream.sendCh, "segment dropped").GetDvrSegmentDropped()
		if got == nil || got.GetReason() != "disk_pressure" || !got.GetWasUploaded() {
			t.Fatalf("payload not as expected: %+v", got)
		}
	})

	t.Run("DVRStreamEndNotification", func(t *testing.T) {
		stream := connectFake(t)
		if err := SendDVRStreamEndNotification("live+stream-1", "node-1"); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		got := waitForControlMessage(t, stream.sendCh, "dvr stream end").GetDvrStopRequest()
		// Empty hash means "stop all recordings for this stream".
		if got == nil || got.GetInternalName() != "live+stream-1" || got.GetDvrHash() != "" {
			t.Fatalf("payload not as expected: %+v", got)
		}
	})

	t.Run("ThumbnailUploadRequest", func(t *testing.T) {
		stream := connectFake(t)
		if err := SendThumbnailUploadRequest("live+stream-1", []string{"/t/a.jpg", "/t/b.jpg"}); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		got := waitForControlMessage(t, stream.sendCh, "thumbnail upload").GetThumbnailUploadRequest()
		if got == nil || got.GetInternalName() != "live+stream-1" || len(got.GetFilePaths()) != 2 {
			t.Fatalf("payload not as expected: %+v", got)
		}
	})

	t.Run("FreezeProgress", func(t *testing.T) {
		stream := connectFake(t)
		if err := SendFreezeProgress("req-1", "asset-1", 42, 1024); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		got := waitForControlMessage(t, stream.sendCh, "freeze progress").GetFreezeProgress()
		if got == nil || got.GetPercent() != 42 || got.GetBytesUploaded() != 1024 {
			t.Fatalf("payload not as expected: %+v", got)
		}
	})
}

func TestFireAndForgetDVRSendersDisconnected(t *testing.T) {
	clearConn()
	if err := SendMarkDVRSegmentUploaded("d", "s", 1); err == nil {
		t.Fatal("MarkDVRSegmentUploaded: expected error when disconnected")
	}
	if err := SendDVRSegmentDropped("d", "s", "r", "/p", 0, 1, 1, 1, false); err == nil {
		t.Fatal("DVRSegmentDropped: expected error when disconnected")
	}
	if err := SendDVRStreamEndNotification("live+s", "n"); err == nil {
		t.Fatal("DVRStreamEndNotification: expected error when disconnected")
	}
	if err := SendThumbnailUploadRequest("live+s", []string{"/t.jpg"}); err == nil {
		t.Fatal("ThumbnailUploadRequest: expected error when disconnected")
	}
	if err := SendFreezeProgress("r", "a", 1, 1); err == nil {
		t.Fatal("FreezeProgress: expected error when disconnected")
	}
}
