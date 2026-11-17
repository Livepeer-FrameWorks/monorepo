package control

import (
	"context"
	"testing"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/streamident"
)

type degradedMark struct {
	tenantID, nodeID, internalName, runtimeName, reason string
}

func stubTranscodeDegradedStore(t *testing.T, tenantByStream map[string]string) *[]degradedMark {
	t.Helper()
	var marks []degradedMark
	oldTenant, oldMark := ingestSessionTenantForStream, markIngestSessionTranscodeDegraded
	ingestSessionTenantForStream = func(internalName string) string { return tenantByStream[internalName] }
	markIngestSessionTranscodeDegraded = func(_ context.Context, tenantID, nodeID, internalName, runtimeName, reason string) (int64, error) {
		marks = append(marks, degradedMark{tenantID, nodeID, internalName, runtimeName, reason})
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
	if got.tenantID != "tenant-1" || got.nodeID != "edge-1" || got.internalName != "abc" || got.runtimeName != "live+abc" || got.reason == "" {
		t.Fatalf("mark = %+v", got)
	}
}

func TestStreamTranscodeDegradedMarksBoundPublisher(t *testing.T) {
	marks := stubTranscodeDegradedStore(t, map[string]string{"abc": "tenant-1"})
	runtimeName, err := streamident.BoundRuntimeName("abc", streamident.RuntimePublisher, "10000000-0000-4000-8000-000000000002")
	if err != nil {
		t.Fatal(err)
	}
	processStreamTranscodeDegraded(&ipcpb.StreamTranscodeDegraded{
		Stream: runtimeName, FailedProcessType: "Livepeer", Reason: "gateway unavailable", ReplacementCount: 2,
	}, "edge-1", logging.NewLogger())
	if len(*marks) != 1 || (*marks)[0].tenantID != "tenant-1" || (*marks)[0].internalName != "abc" || (*marks)[0].runtimeName != runtimeName {
		t.Fatalf("bound publisher lost its logical owner: %+v", *marks)
	}
}

func TestStreamTranscodeDegradedRelayCannotMarkPublisher(t *testing.T) {
	marks := stubTranscodeDegradedStore(t, map[string]string{"abc": "tenant-1"})
	runtimeName, err := streamident.BoundRuntimeName("abc", streamident.RuntimeRelay, "10000000-0000-4000-8000-000000000002")
	if err != nil {
		t.Fatal(err)
	}
	processStreamTranscodeDegraded(&ipcpb.StreamTranscodeDegraded{Stream: runtimeName, FailedProcessType: "Livepeer"}, "edge-1", logging.NewLogger())
	if len(*marks) != 0 {
		t.Fatalf("relay marked an ingest publisher: %+v", *marks)
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
