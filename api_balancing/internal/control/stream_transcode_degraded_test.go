package control

import (
	"context"
	"testing"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
)

type degradedMark struct {
	tenantID, nodeID, internalName, reason string
}

func stubTranscodeDegradedStore(t *testing.T, tenantByStream map[string]string) *[]degradedMark {
	t.Helper()
	var marks []degradedMark
	oldTenant, oldMark := ingestSessionTenantForStream, markIngestSessionTranscodeDegraded
	ingestSessionTenantForStream = func(internalName string) string { return tenantByStream[internalName] }
	markIngestSessionTranscodeDegraded = func(_ context.Context, tenantID, nodeID, internalName, reason string) (int64, error) {
		marks = append(marks, degradedMark{tenantID, nodeID, internalName, reason})
		return 1, nil
	}
	t.Cleanup(func() { ingestSessionTenantForStream, markIngestSessionTranscodeDegraded = oldTenant, oldMark })
	return &marks
}

func TestStreamTranscodeDegradedMarksLiveIngestSession(t *testing.T) {
	marks := stubTranscodeDegradedStore(t, map[string]string{"abc": "tenant-1"})
	processStreamTranscodeDegraded(&ipcpb.StreamTranscodeDegraded{
		Stream: "live+abc", FailedProcessType: "Livepeer", Reason: "ER_FORMAT_SPECIFIC: Livepeer upload fatal HTTP status 403", ReplacementCount: 4,
	}, "edge-1", logging.NewLogger())

	if len(*marks) != 1 {
		t.Fatalf("marks = %+v, want one ingest session mark", *marks)
	}
	got := (*marks)[0]
	if got.tenantID != "tenant-1" || got.nodeID != "edge-1" || got.internalName != "abc" || got.reason == "" {
		t.Fatalf("mark = %+v", got)
	}
}

func TestStreamTranscodeDegradedLeavesProcessingAndUnknownStreamsUnmarked(t *testing.T) {
	marks := stubTranscodeDegradedStore(t, map[string]string{})
	processStreamTranscodeDegraded(&ipcpb.StreamTranscodeDegraded{Stream: "processing+hash", FailedProcessType: "Livepeer", ReplacementCount: 2}, "edge-1", logging.NewLogger())
	processStreamTranscodeDegraded(&ipcpb.StreamTranscodeDegraded{Stream: "live+unknown", FailedProcessType: "Livepeer", ReplacementCount: 2}, "edge-1", logging.NewLogger())
	if len(*marks) != 0 {
		t.Fatalf("marks = %+v, want none", *marks)
	}
}
